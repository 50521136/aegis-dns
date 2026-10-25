package dns

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/id"
)

// Server 承载全部 DNS 协议监听。
//
// 端口与反代的关系（架构红线 C4）：53 / 853 / 443 全部由 dnsd 直接监听，
// 不经任何反向代理，因此反代重启不会中断 DNS。
type Server struct {
	cfg     *config.Config
	handler *Handler
	certs   *certmgr.Manager
	log     *slog.Logger

	udp *dns.Server
	tcp *dns.Server

	dotLn net.Listener
	doqLn *quic.Listener

	dohTLS   *http.Server
	dohPlain *http.Server
	doh3     *http3.Server

	// 启动错误汇总，便于在自检里一次性看到「哪个端口没起来」。
	startErrs []error
}

// dotIdleTimeout 是 DoT 连接的**空闲**超时（不是总时长）。
//
// 每收到一条查询就续期，所以只要客户端还在用就一直不断；
// 真正闲置 60 秒才回收。取 60 秒的理由：客户端保活间隔普遍在 30 秒量级
// （AdGuard Home、路由器健康检查），给两倍余量既能容忍一次丢包，
// 又不会让死连接占着 fd。
const dotIdleTimeout = 60 * time.Second

// NewServer 创建 DNS 服务器。
func NewServer(cfg *config.Config, h *Handler, certs *certmgr.Manager, log *slog.Logger) *Server {
	return &Server{cfg: cfg, handler: h, certs: certs, log: log}
}

// Start 启动全部监听。
//
// 采用「部分启动」语义：某个协议起不来（例如 53 被 systemd-resolved 占用，
// 见风险 R2）不会阻止其它协议工作 —— DoT/DoH 照常可用。
// 但若一个都没起来，返回错误让 systemd 拉起失败可见。
func (s *Server) Start(ctx context.Context) error {
	started := 0

	// --- UDP 53 ---
	if addr := s.cfg.DNS.ListenUDP; addr != "" {
		s.udp = &dns.Server{
			Addr:    addr,
			Net:     "udp",
			Handler: s.handler,
			// UDPSize 提到 4096 以支持较大的应答（DNSSEC、大 TXT）。
			UDPSize: 4096,
		}
		go func() {
			if err := s.udp.ListenAndServe(); err != nil {
				s.recordErr("UDP", addr, err)
			}
		}()
		started++
		s.log.Info("DNS over UDP 已启动", "addr", addr)
	}

	// --- TCP 53 ---
	if addr := s.cfg.DNS.ListenTCP; addr != "" {
		s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: s.handler}
		go func() {
			if err := s.tcp.ListenAndServe(); err != nil {
				s.recordErr("TCP", addr, err)
			}
		}()
		started++
		s.log.Info("DNS over TCP 已启动", "addr", addr)
	}

	// --- DoT 853 ---
	if addr := s.cfg.DNS.ListenDOT; addr != "" {
		if !s.certs.Available() {
			// 无证书时降级：只打警告，不阻止 UDP/TCP 服务（文档 3.4 兜底）。
			s.log.Warn("没有可用证书，跳过 DoT 监听（UDP/TCP 不受影响）", "addr", addr)
		} else {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				s.recordErr("DoT", addr, err)
			} else {
				s.dotLn = ln
				go s.serveDOT(ctx, ln)
				started++
				s.log.Info("DNS over TLS 已启动", "addr", addr)
			}
		}
	}

	// --- DoH 443 ---
	if addr := s.cfg.DNS.ListenDOH; addr != "" {
		if !s.certs.Available() {
			s.log.Warn("没有可用证书，跳过 DoH 监听", "addr", addr)
		} else {
			mux := http.NewServeMux()
			mux.HandleFunc("/dns-query", s.handleDoH)
			mux.HandleFunc("/dns-query/", s.handleDoH)
			mux.HandleFunc("/", s.handleDoHRoot)

			s.dohTLS = &http.Server{
				Addr:              addr,
				Handler:           mux,
				TLSConfig:         s.certs.TLSConfig(),
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
				ErrorLog:          nil,
			}
			go func() {
				// 证书由 GetCertificate 动态提供，因此传空字符串。
				if err := s.dohTLS.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
					s.recordErr("DoH", addr, err)
				}
			}()
			started++
			s.log.Info("DNS over HTTPS 已启动", "addr", addr)
		}
	}

	// --- 明文 DoH（可选，供反代终止 TLS 或本机调试）---
	if addr := s.cfg.DNS.ListenDOHPlain; addr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/dns-query", s.handleDoH)
		mux.HandleFunc("/dns-query/", s.handleDoH)
		s.dohPlain = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := s.dohPlain.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.recordErr("DoH(plain)", addr, err)
			}
		}()
		started++
		s.log.Info("明文 DoH 已启动（仅供本机或反代）", "addr", addr)
	}

	// --- DoQ 853/udp（可选）---
	//
	// 放在 DoT/DoH 之后：它依赖证书，且与 DoT 共用 853 端口号（TCP 与 UDP
	// 互不影响）。没证书时与 DoT 同样降级跳过，不阻止其它协议。
	if addr := s.cfg.DNS.ListenDOQ; addr != "" {
		if !s.certs.Available() {
			s.log.Warn("没有可用证书，跳过 DoQ 监听", "addr", addr)
		} else {
			s.startDOQ(ctx, addr)
			started++
		}
	}

	// --- DoH over HTTP/3（可选）---
	//
	// 与 DoH 用同一个 mux 和同一个 URL 形态，只是底层换成 UDP+QUIC，
	// 客户端靠 ALPN "h3" 区分。DoH（TCP）响应里会带 Alt-Svc 指过来（见 handleDoH）。
	if addr := s.cfg.DNS.ListenDOH3; addr != "" {
		if !s.certs.Available() {
			s.log.Warn("没有可用证书，跳过 DoH3 监听", "addr", addr)
		} else {
			mux := http.NewServeMux()
			mux.HandleFunc("/dns-query", s.handleDoH)
			mux.HandleFunc("/dns-query/", s.handleDoH)
			mux.HandleFunc("/", s.handleDoHRoot)
			s.doh3 = &http3.Server{
				Addr:       addr,
				Handler:    mux,
				TLSConfig:  s.certs.TLSConfigForALPN("h3"),
				QUICConfig: &quic.Config{MaxIdleTimeout: 120 * time.Second},
			}
			go func() {
				if err := s.doh3.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					s.recordErr("DoH3", addr, err)
				}
			}()
			started++
			s.log.Info("DNS over HTTP/3 已启动", "addr", addr, "alpn", "h3")
		}
	}

	if started == 0 {
		return fmt.Errorf("没有任何 DNS 协议成功启动：%v", s.startErrs)
	}
	return nil
}

func (s *Server) recordErr(proto, addr string, err error) {
	// ListenAndServe 在 Shutdown 时会返回 ErrServerClosed，不算错误。
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}
	wrapped := fmt.Errorf("%s 监听 %s 失败: %w", proto, addr, err)
	s.startErrs = append(s.startErrs, wrapped)
	s.log.Error("DNS 监听失败", "proto", proto, "addr", addr, "err", err)
}

// StartErrs 返回启动阶段的错误（自检用）。
func (s *Server) StartErrs() []error { return s.startErrs }

// Shutdown 优雅关闭全部监听。
func (s *Server) Shutdown(ctx context.Context) error {
	var errs []error
	if s.udp != nil {
		errs = append(errs, s.udp.ShutdownContext(ctx))
	}
	if s.tcp != nil {
		errs = append(errs, s.tcp.ShutdownContext(ctx))
	}
	if s.dotLn != nil {
		errs = append(errs, s.dotLn.Close())
	}
	if s.doqLn != nil {
		errs = append(errs, s.doqLn.Close())
	}
	if s.dohTLS != nil {
		errs = append(errs, s.dohTLS.Shutdown(ctx))
	}
	if s.dohPlain != nil {
		errs = append(errs, s.dohPlain.Shutdown(ctx))
	}
	if s.doh3 != nil {
		errs = append(errs, s.doh3.Close())
	}
	return errors.Join(errs...)
}

// === DoT ===

// serveDOT 自实现 DoT 服务端。
//
// 为什么不用 dns.Server 的 tcp-tls 模式：那样拿不到连接的 SNI。
// 多租户识别完全依赖 SNI 首段（文档 3.2），所以这里自己 accept + handshake，
// 把 ServerName 取出来后随每条查询一起交给 handler。
func (s *Server) serveDOT(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("DoT accept 失败", "err", err)
			continue
		}
		go s.handleDOTConn(ctx, conn)
	}
}

func (s *Server) handleDOTConn(ctx context.Context, raw net.Conn) {
	defer raw.Close()

	tlsConn := tls.Server(raw, s.certs.TLSConfig())
	hsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := tlsConn.HandshakeContext(hsCtx)
	cancel()
	if err != nil {
		s.log.Debug("DoT 握手失败", "remote", raw.RemoteAddr().String(), "err", err)
		return
	}

	sni := tlsConn.ConnectionState().ServerName
	clientID := firstLabel(sni)

	// 必须用 tlsConn 而不是 raw 收发 DNS 报文。
	// 用 raw 会让服务端把 TLS 记录当成 DNS 长度前缀去解析：读到一个巨大的
	// 长度值后永久阻塞在读取上，客户端表现为连接超时而不是明确的错误 ——
	// 这类 bug 从日志里几乎看不出来。
	dnsConn := &dns.Conn{Conn: tlsConn}

	// 空闲超时必须是**滑动**的：每收到一条查询就把 deadline 往后推。
	//
	// 原来是固定 60 秒总时限 + 最多 100 条查询，到点就断 —— 而 AdGuard Home、
	// 路由器这类客户端会长期复用同一条连接（每 30 秒一次健康检查是常态）。
	// 固定时限下它们每 60 秒就要重建一次连接，每次都要完整 TLS 握手；
	// 100 条上限在正常家用流量下也够呛（一条连接一天几百条查询很常见）。
	//
	// 判据从「连接活了多久」改成「有多久没说话」：只要还在发查询就一直续期，
	// 真正闲置的连接照样在 60 秒后回收，不会泄漏。
	_ = tlsConn.SetDeadline(time.Now().Add(dotIdleTimeout))
	for {
		msg, err := dnsConn.ReadMsg()
		if err != nil {
			// 超时/对端关闭都走这里，属正常回收路径。
			return
		}
		// 收到一条就续期一次：空闲计时以「最后一次收到报文」为起点。
		_ = tlsConn.SetDeadline(time.Now().Add(dotIdleTimeout))
		w := &streamResponseWriter{conn: dnsConn, remote: raw.RemoteAddr(), local: raw.LocalAddr()}
		s.handler.Handle(w, msg, &IdentifyHint{ClientID: clientID, Source: "sni", Host: normalizeHost(sni)})
	}
}

// firstLabel 取主机名的第一段（k7m2p9xq4a.dns.example.com -> k7m2p9xq4a）。
//
// 只有形状合法的 client_id 才会被返回，否则返回空串。
//
// 这一点很关键：DoH 客户端用路径式地址时，Host 是**裸域名**
// （https://dns.example.com/dns-query/<client_id>），它的首段是 "dns" 这类
// 普通标签。若不校验，调用方会把它当成 client_id 认领下来、查一次查不到，
// 然后直接掉到来源 IP 兜底 —— 路径段就永远没机会被解析，文档承诺的路径式
// DoH 会静默失效（表现为「明明带上了 client_id，却走的是别人的规则」）。
func firstLabel(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || strings.Contains(host, ":") {
		// 含冒号的是 IPv6 字面量或带端口的 Host，一律不认。
		return ""
	}
	label, _, _ := strings.Cut(host, ".")
	if !id.ValidClientID(label) {
		return ""
	}
	return label
}

// dohPathClientID 从 DoH 路径里取出 client_id（/dns-query/<client_id> -> <client_id>）。
//
// 只取第一段，且必须形状合法：/dns-query 之后可能还有别的东西（某些客户端会
// 拼 ?dns=... 或附加路径），取第一段即可；形状不合法的直接丢弃，避免把
// /dns-query/foo 这种垃圾当成租户 id 去查一次。
//
// 必须先确认路径确实以 /dns-query 开头：只做 TrimPrefix 的话，像 /other/abc
// 这种路径 TrimPrefix 不匹配、原样留下，随后取首段就得到 "other" —— 一个恰好
// 形状合法的假 client_id。虽然当前路由只会把 /dns-query* 交给本函数，
// 但依赖「调用方保证」的隐式契约太脆，这里显式挡掉。
func dohPathClientID(path string) string {
	if path != "/dns-query" && !strings.HasPrefix(path, "/dns-query/") {
		return ""
	}
	rest := strings.TrimPrefix(path, "/dns-query")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	// 与 firstLabel 一致地做小写归一：Host 和 URL 路径在客户端侧大小写写法不一，
	// 不归一就会出现「用大写 client_id 的 URL 认不出租户」。
	seg = strings.ToLower(seg)
	if !id.ValidClientID(seg) {
		return ""
	}
	return seg
}

// streamResponseWriter 是流式连接（DoT）的 ResponseWriter 实现。
type streamResponseWriter struct {
	conn   *dns.Conn
	remote net.Addr
	local  net.Addr
}

func (w *streamResponseWriter) LocalAddr() net.Addr  { return w.local }
func (w *streamResponseWriter) RemoteAddr() net.Addr { return w.remote }
func (w *streamResponseWriter) WriteMsg(m *dns.Msg) error {
	return w.conn.WriteMsg(m)
}
func (w *streamResponseWriter) Write(b []byte) (int, error) { return w.conn.Write(b) }
func (w *streamResponseWriter) Close() error                { return nil }
func (w *streamResponseWriter) TsigStatus() error           { return nil }
func (w *streamResponseWriter) TsigTimersOnly(bool)         {}
func (w *streamResponseWriter) Hijack()                     {}

// === DoH ===

// maxDoHBody 是 DoH 请求体的上限。
//
// 8KB 足够容纳最大的合法 DNS 查询（含 EDNS0 选项），
// 同时挡住用超大 body 打内存的尝试（文档 10.3 DoS 防护）。
const maxDoHBody = 8 << 10

// handleDoHRoot 处理非 /dns-query 路径：给出明确提示而不是 404 空白页。
func (s *Server) handleDoHRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "这是 DoH 端点。请在 /dns-query 发起 DNS-over-HTTPS 查询。\n")
}

// handleDoH 处理 DoH 查询。
//
// 支持两种形态（文档 3.2）：
//
//	https://<client_id>.<domain>/dns-query     —— Host 首段识别
//	https://<domain>/dns-query/<client_id>     —— 路径段识别（兼容不能改 Host 的客户端）
func (s *Server) handleDoH(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "仅支持 GET 与 POST", http.StatusMethodNotAllowed)
		return
	}

	var (
		body []byte
		err  error
	)
	if r.Method == http.MethodGet {
		b64 := r.URL.Query().Get("dns")
		if b64 == "" {
			http.Error(w, "缺少 dns 参数", http.StatusBadRequest)
			return
		}
		// DoH 规范要求 base64url 且无填充，但真实客户端会带填充，两种都接受。
		body, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(b64, "="))
		if err != nil {
			body, err = base64.URLEncoding.DecodeString(b64)
		}
		if err != nil {
			http.Error(w, "dns 参数不是合法的 base64url", http.StatusBadRequest)
			return
		}
	} else {
		ct := r.Header.Get("Content-Type")
		if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/dns-message") {
			http.Error(w, "Content-Type 必须为 application/dns-message", http.StatusUnsupportedMediaType)
			return
		}
		body, err = io.ReadAll(io.LimitReader(r.Body, maxDoHBody+1))
		if err != nil {
			http.Error(w, "读取请求体失败", http.StatusBadRequest)
			return
		}
		if len(body) > maxDoHBody {
			http.Error(w, "请求体过大", http.StatusRequestEntityTooLarge)
			return
		}
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(body); err != nil {
		http.Error(w, "不是合法的 DNS 报文", http.StatusBadRequest)
		return
	}

	// 识别租户：Host 首段优先，其次路径段。
	//
	// 路径段必须作为**独立的次级线索**传下去，不能在 Host 段非空时被跳过。
	// 路径式地址 https://<域名>/dns-query/<client_id> 的 Host 是裸域名，
	// firstLabel 校验后会返回空串；若这里写成「Host 段非空就不看路径」，
	// 那么一旦 Host 是别的什么东西，路径段就再也没机会被解析。
	clientID, source := "", ""
	host := r.Host
	if host == "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	if cid := firstLabel(host); cid != "" {
		clientID, source = cid, "host"
	}

	altID, altSource := "", ""
	if cid := dohPathClientID(r.URL.Path); cid != "" {
		altID, altSource = cid, "path"
	}
	// Host 没给出线索时，路径段直接升为主线索，保持 hint 语义直观。
	if clientID == "" {
		clientID, source, altID, altSource = altID, altSource, "", ""
	}

	cap := &captureWriter{remote: clientAddr(r)}
	// 显式标注协议，不靠 protoOf 的推断。
	//
	// 推断路径在两种情况下会走错：请求没带任何 client_id 线索时，
	// protoOf 会退到 RemoteAddr().Network() —— DoH over HTTP/3 的远端地址是
	// *net.UDPAddr，于是被当成「明文 UDP」，一旦管理员关掉明文 DNS，
	// 明明开着 DoH 的 H3 请求会被 REFUSED；TCP 上的 DoH 同理会落到「明文 TCP」。
	proto := "doh"
	if r.ProtoMajor >= 3 {
		proto = "doh3"
	}
	s.handler.Handle(cap, msg, &IdentifyHint{
		ClientID:    clientID,
		Source:      source,
		Host:        normalizeHost(host),
		AltClientID: altID,
		AltSource:   altSource,
		Proto:       proto,
	})

	if cap.msg == nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	out, err := cap.msg.Pack()
	if err != nil {
		http.Error(w, "打包应答失败", http.StatusInternalServerError)
		return
	}

	// 缓存头：DoH 客户端会按它决定是否复用连接上的应答。
	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Alt-Svc 是客户端发现 HTTP/3 的**唯一**正常途径（RFC 7838）：
	// 手写 h3:// 的只有 dnsproxy 这类工具，浏览器和系统解析器都是先走 TCP
	// 上的 DoH，看到这个头才切到 QUIC。所以开了 H3 就必须在这里宣告，
	// 光起 UDP 监听不会有任何客户端切过去。
	//
	// 只在 TLS 请求上加：明文 DoH（listen_doh_plain，给反代终止 TLS 用）
	// 上宣告 h3 没有意义，还会让客户端尝试一个不存在的 QUIC 端点。
	if r.TLS != nil && s.doh3 != nil {
		if err := s.doh3.SetQUICHeaders(w.Header()); err != nil {
			s.log.Debug("设置 Alt-Svc 失败", "err", err)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// captureWriter 把 handler 产出的应答捕获下来，供 DoH 回写为 HTTP body。
type captureWriter struct {
	msg    *dns.Msg
	remote net.Addr
}

func (w *captureWriter) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (w *captureWriter) RemoteAddr() net.Addr { return w.remote }
func (w *captureWriter) WriteMsg(m *dns.Msg) error {
	w.msg = m
	return nil
}
func (w *captureWriter) Write(b []byte) (int, error) {
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		return 0, err
	}
	w.msg = m
	return len(b), nil
}
func (w *captureWriter) Close() error        { return nil }
func (w *captureWriter) TsigStatus() error   { return nil }
func (w *captureWriter) TsigTimersOnly(bool) {}
func (w *captureWriter) Hijack()             {}

// clientAddr 从 HTTP 请求还原客户端地址。
func clientAddr(r *http.Request) net.Addr {
	host, portStr, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
		portStr = "0"
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return &net.TCPAddr{}
	}
	port, _ := strconv.Atoi(portStr)
	return &net.TCPAddr{IP: ip, Port: port}
}

// normalizeHost 把主机名规整成小写、无端口、无尾点的形式。
//
// 裸域名判断要跟配置里的 Domain 直接比字符串，任何形态差异（大写、
// 带端口、结尾的点）都会让比对失败，从而把裸域名误判成子域名放行。
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h, "]") {
		// 去掉 :port；IPv6 字面量带方括号，不按冒号切
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			h = h[:i]
		}
	}
	return h
}
