package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

// DoQ（DNS-over-QUIC，RFC 9250）接入。
//
// 与 DoT 的关系：同一个端口号 853，但传输层不同（DoT 是 TCP、DoQ 是 UDP），
// 所以两者可以同时开、互不冲突。
//
// 三个必须守住的协议细节，错一个客户端就「连不上」且服务端日志几乎无信息：
//
//  1. **ALPN 必须是 "doq"**。quic-go 用 NextProtos 协商，若沿用 DoT/DoH 那份
//     {"h2","http/1.1","dot"}，DoQ 客户端握手时直接 no application protocol。
//     所以这里用 certmgr.TLSConfigForALPN("doq") 单独给一份配置。
//  2. **一条流只承载一条查询**，报文前仍带 2 字节长度前缀（与 DNS-over-TCP 相同），
//     应答写完就关流（FIN）。客户端按流判断「应答结束」，不关流会一直等。
//  3. **不能按 UDP 处理应答截断**。QUIC 上不存在 512/EDNS 缓冲限制，
//     而 handler 的 writeMsg 会看 RemoteAddr().Network()=="udp" 就截断 ——
//     QUIC 的远端地址恰好就是 *net.UDPAddr，所以这里必须把地址包一层，
//     让 Network() 返回 "doq"，否则大应答（DNSSEC、长 TXT）会被莫名截断。
//
// 租户识别沿用 DoT 的同一条路径：QUIC 的 TLS 握手带 SNI，取首段做 client_id。

const (
	// doqALPN 是 RFC 9250 规定的应用层协议标识。
	doqALPN = "doq"
	// doqIdleTimeout 是连接空闲超时。与 DoT 的 60 秒对齐：客户端（尤其
	// 路由器/AdGuard Home）会长期复用同一条 QUIC 连接，掐太短会反复重建握手。
	doqIdleTimeout = 60 * time.Second
	// doqStreamTimeout 是单条流从读到写完的时限，防止半开流占住 goroutine。
	doqStreamTimeout = 10 * time.Second
	// doqMaxStreams 限制单连接并发流数，防止一个客户端开几万条流打内存。
	doqMaxStreams = 256
	// doqMaxMsgSize 是单条 DNS 报文的长度上限（与 TCP 的 2 字节前缀一致）。
	doqMaxMsgSize = 65535
)

// startDOQ 在 addr（UDP）上启动 DoQ 监听。
//
// 起不来只记录错误、不阻止其它协议：与 DoT/DoH 的部分启动语义一致。
func (s *Server) startDOQ(ctx context.Context, addr string) {
	tlsConf := s.certs.TLSConfigForALPN(doqALPN)
	ln, err := quic.ListenAddr(addr, tlsConf, &quic.Config{
		MaxIdleTimeout:        doqIdleTimeout,
		MaxIncomingStreams:    doqMaxStreams,
		MaxIncomingUniStreams: -1, // DoQ 只用双向流
	})
	if err != nil {
		s.recordErr("DoQ", addr, err)
		return
	}
	s.doqLn = ln
	go s.serveDOQConns(ctx, ln)
	s.log.Info("DNS over QUIC 已启动", "addr", addr, "alpn", doqALPN)
}

// serveDOQConns 接受 QUIC 连接。
func (s *Server) serveDOQConns(ctx context.Context, ln *quic.Listener) {
	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) || errors.Is(err, quic.ErrServerClosed) {
				return
			}
			s.log.Warn("DoQ accept 失败", "err", err)
			continue
		}
		go s.handleDOQConn(ctx, conn)
	}
}

// handleDOQConn 处理一条 QUIC 连接上的所有流。
func (s *Server) handleDOQConn(ctx context.Context, conn *quic.Conn) {
	defer func() { _ = conn.CloseWithError(0, "") }()

	// SNI 是**连接级**的（QUIC 的 TLS 握手只在连接建立时做一次），
	// 不像 DoH 那样每条请求都带 Host，所以这里取一次、给这条连接上所有流复用。
	sni := conn.ConnectionState().TLS.ServerName
	clientID := firstLabel(sni)

	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			// 连接关闭/空闲超时/客户端主动断开都走这里，属正常路径。
			return
		}
		go s.handleDOQStream(ctx, conn, stream, sni, clientID)
	}
}

// handleDOQStream 在一条双向流上完成「读一条查询 → 写一条应答 → 关流」。
func (s *Server) handleDOQStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, sni, clientID string) {
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(doqStreamTimeout))

	msg, err := readLengthPrefixed(stream)
	if err != nil {
		s.log.Debug("DoQ 读取查询失败", "remote", conn.RemoteAddr().String(), "err", err)
		return
	}

	w := &doqResponseWriter{stream: stream, remote: &protoAddr{addr: conn.RemoteAddr(), network: "doq"}}
	s.handler.Handle(w, msg, &IdentifyHint{
		ClientID: clientID,
		Source:   "sni",
		Host:     normalizeHost(sni),
		Proto:    "doq",
	})
}

// readLengthPrefixed 按 DNS-over-TCP/DoQ 的 2 字节长度前缀读一条报文。
func readLengthPrefixed(r io.Reader) (*dns.Msg, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 {
		return nil, errors.New("长度为 0 的报文")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	msg := new(dns.Msg)
	if err := msg.Unpack(buf); err != nil {
		return nil, err
	}
	return msg, nil
}

// doqResponseWriter 把应答写回 QUIC 流，格式为 2 字节长度前缀 + 报文。
type doqResponseWriter struct {
	stream *quic.Stream
	remote net.Addr
}

func (w *doqResponseWriter) LocalAddr() net.Addr  { return &protoAddr{network: "doq"} }
func (w *doqResponseWriter) RemoteAddr() net.Addr { return w.remote }

func (w *doqResponseWriter) WriteMsg(m *dns.Msg) error {
	if m == nil {
		return errors.New("应答为空")
	}
	b, err := m.Pack()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func (w *doqResponseWriter) Write(b []byte) (int, error) {
	if len(b) > doqMaxMsgSize {
		return 0, errors.New("应答超过 65535 字节")
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := w.stream.Write(hdr[:]); err != nil {
		return 0, err
	}
	return w.stream.Write(b)
}

func (w *doqResponseWriter) Close() error        { return nil }
func (w *doqResponseWriter) TsigStatus() error   { return nil }
func (w *doqResponseWriter) TsigTimersOnly(bool) {}
func (w *doqResponseWriter) Hijack()             {}

// protoAddr 是一个只用来「标注协议名」的地址包装。
//
// 存在理由：handler 的 writeMsg 用 RemoteAddr().Network()=="udp" 判断要不要
// 按 512/EDNS 缓冲截断。QUIC 的远端地址是 *net.UDPAddr（Network() 返回 "udp"），
// 直接透传会让 DoQ 上的大应答被当成 UDP 截断 —— 客户端拿到 TC=1 后
// 在 QUIC 上不会重试 TCP，只能报失败，且服务端看起来一切正常。
type protoAddr struct {
	addr    net.Addr
	network string
}

func (a *protoAddr) Network() string {
	if a.network != "" {
		return a.network
	}
	return "udp"
}

func (a *protoAddr) String() string {
	if a.addr == nil {
		return ""
	}
	return a.addr.String()
}
