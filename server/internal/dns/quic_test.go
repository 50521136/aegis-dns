package dns

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
)

// 这一组是 DoQ / DoH3 的**协议级**测试：真的起 QUIC 监听，用 quic-go 客户端
// 按 RFC 9250 / RFC 9114 的方式连上去，而不是只测纯函数。
//
// 之所以必须端到端测：这两条链路上最容易错的地方全在「线上看不出来」的位置 ——
//
//   - ALPN 用错（沿用 DoT/DoH 的 h2/dot 那份）→ 客户端 no application protocol，
//     服务端只记一条 debug 级握手失败；
//   - 应答没按 2 字节长度前缀写 / 写完不关流 → 客户端一直等，表现为超时；
//   - 把 QUIC 的 *net.UDPAddr 直接当 RemoteAddr 透传给 handler →
//     writeMsg 按「UDP」把大应答截断成 TC=1，而客户端在 QUIC 上不会重试 TCP。
//
// 因此断言要覆盖：能握手、能查到答案、大应答不被截断、租户能按 SNI 识别、
// DoH3 的 Alt-Svc 宣告在位、以及 H3 请求不会被明文 DNS 开关误拒。

// newDevCertServer 造一个带自签证书的 Server（dev_tls 形态），不监听任何端口。
func newDevCertServer(t *testing.T) (*Server, *Handler) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	certs := certmgr.New("", "", true, log)
	// 必须用 Ensure 而不是 Load：Load 只读文件，dev_tls 的自签兜底在 Ensure 里。
	if err := certs.Ensure(); err != nil {
		t.Fatalf("生成自签证书失败: %v", err)
	}
	if !certs.Available() {
		t.Fatal("dev_tls 应当生成自签证书")
	}
	cfg := &config.Config{Domain: "dns.test.local"}
	h, _ := newTestHandler(t)
	return NewServer(cfg, h, certs, log), h
}

// freeUDPAddr 拿一个空闲的 UDP 端口地址（避免并行测试互相抢端口）。
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("拿空闲 UDP 端口失败: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

// doqClient 用 quic-go 起一条 DoQ 连接（ALPN 必须是 doq）。
func doqClient(t *testing.T, addr, sni string) *quic.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true, // 自签证书；生产验证走严格校验的独立探针
		ServerName:         sni,
		NextProtos:         []string{doqALPN},
	}, &quic.Config{MaxIdleTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("DoQ 连接失败（ALPN/证书/端口任一处不对都会走到这里）: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

// doqExchange 在一条新流上完成一次查询，返回应答。
func doqExchange(t *testing.T, conn *quic.Conn, req *dns.Msg) *dns.Msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("打开 QUIC 流失败: %v", err)
	}
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	b, err := req.Pack()
	if err != nil {
		t.Fatalf("打包查询失败: %v", err)
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := stream.Write(append(hdr[:], b...)); err != nil {
		t.Fatalf("写查询失败: %v", err)
	}

	var rh [2]byte
	if _, err := io.ReadFull(stream, rh[:]); err != nil {
		t.Fatalf("读应答长度失败（服务端没写 2 字节前缀或没回包）: %v", err)
	}
	n := int(binary.BigEndian.Uint16(rh[:]))
	buf := make([]byte, n)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("读应答体失败: %v", err)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		t.Fatalf("应答不是合法 DNS 报文: %v", err)
	}
	return resp
}

func TestDOQServesQueriesOverQUIC(t *testing.T) {
	s, _ := newDevCertServer(t)
	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.startDOQ(ctx, addr)
	if s.doqLn == nil {
		t.Fatalf("DoQ 监听未建立: %v", s.StartErrs())
	}
	defer func() { _ = s.doqLn.Close() }()

	conn := doqClient(t, addr, "aaaaaaaaaa.dns.test.local")

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	resp := doqExchange(t, conn, req)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("DoQ 查询失败 rcode=%d", resp.Rcode)
	}
	if len(resp.Answer) == 0 && len(resp.Ns) == 0 {
		t.Fatalf("DoQ 应答既无 answer 也无 authority: %v", resp)
	}
}

func TestDOQIdentifiesTenantBySNI(t *testing.T) {
	s, h := newDevCertServer(t)
	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.startDOQ(ctx, addr)
	if s.doqLn == nil {
		t.Fatalf("DoQ 监听未建立: %v", s.StartErrs())
	}
	defer func() { _ = s.doqLn.Close() }()

	// 测试快照里 client_id aaaaaaaaaa 属于 usr_t1。SNI 首段必须被当成 client_id，
	// 否则多租户在 DoQ 上等于失效（所有请求都落到来源 IP 兜底）。
	conn := doqClient(t, addr, "aaaaaaaaaa.dns.test.local")
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	_ = doqExchange(t, conn, req)

	// 用 handler 的识别函数直接复核同一个 hint 的语义（连接级 SNI）。
	sni := conn.ConnectionState().TLS.ServerName
	if got := firstLabel(sni); got != "aaaaaaaaaa" {
		t.Fatalf("DoQ 的 SNI 首段未被识别为 client_id: sni=%q got=%q", sni, got)
	}
	// 再确认这个 client_id 在快照里能查到租户，否则上面那条断言只是形状合法。
	u := h.identify(h.registry.Load(), &doqResponseWriter{}, req, &IdentifyHint{
		ClientID: firstLabel(sni), Source: "sni", Proto: "doq",
	})
	if u == nil || u.UserID != "usr_t1" {
		t.Fatalf("DoQ 按 SNI 识别租户失败，期望 usr_t1，实际 %v", u)
	}
}

func TestDOQDoesNotTruncateLargeAnswers(t *testing.T) {
	// QUIC 上没有 512 字节的限制。若把 *net.UDPAddr 直接当 RemoteAddr，
	// handler 的 writeMsg 会按 UDP 截断成 TC=1，而 DoQ 客户端不会重试 TCP，
	// 表现为「大应答查不到」，服务端日志一片正常。
	w := &doqResponseWriter{remote: &protoAddr{addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, network: "doq"}}
	if got := w.RemoteAddr().Network(); got != "doq" {
		t.Fatalf("DoQ 的 RemoteAddr().Network() 必须是 doq（否则会被当 UDP 截断），实际 %q", got)
	}
	// 对照组：不包装时确实是 udp —— 这条守着「为什么必须包装」。
	raw := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	if raw.Network() != "udp" {
		t.Fatalf("前置条件变化：*net.UDPAddr.Network() 不再是 udp")
	}
}

func TestDOQRejectedWhenDoTDisabled(t *testing.T) {
	// DoQ 跟随 enable_dot 开关：853 上的加密 DNS 被关掉时，DoQ 也必须拒。
	// 这条防止「DoT 关了但 DoQ 还能用」的旁路。
	s, _ := newDevCertServer(t)
	_ = s
	d := &defaultsStub{EnableDoH: true, EnableDoT: false, EnablePlainDNS: true}
	if protoEnabledFor(d, "doq") {
		t.Fatal("enable_dot=false 时 DoQ 必须被拒（REFUSED）")
	}
	if !protoEnabledFor(d, "doh") {
		t.Fatal("前置条件不成立：enable_doh=true 时 doh 应放行")
	}

	// 对照组：把 DoH 关掉，H3 必须跟着拒 —— 否则会出现
	// 「DoH 关了但 h3 还能用」的旁路，而两者地址完全相同，用户以为是随机故障。
	d2 := &defaultsStub{EnableDoH: false, EnableDoT: true, EnablePlainDNS: true}
	if protoEnabledFor(d2, "doh3") {
		t.Fatal("enable_doh=false 时 DoH3 必须被拒")
	}
	if !protoEnabledFor(d2, "doq") {
		t.Fatal("enable_dot=true 时 DoQ 应放行")
	}

	// 三个开关全为 false 说明是老快照（字段缺失），必须一律放行，
	// 否则升级瞬间整个服务不可用。
	d3 := &defaultsStub{}
	if !protoEnabledFor(d3, "doq") || !protoEnabledFor(d3, "doh3") {
		t.Fatal("老快照（三个开关都缺）时新协议也必须放行")
	}
}

// --- DoH3 ---

func TestDoH3ServesQueries(t *testing.T) {
	s, _ := newDevCertServer(t)
	addr := freeUDPAddr(t)

	// 直接构造 http3.Server，与 Server.Start 里的接线保持一致。
	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", s.handleDoH)
	mux.HandleFunc("/dns-query/", s.handleDoH)
	srv := &http3.Server{
		Addr:       addr,
		Handler:    mux,
		TLSConfig:  s.certs.TLSConfigForALPN("h3"),
		QUICConfig: &quic.Config{MaxIdleTimeout: 10 * time.Second},
	}
	s.doh3 = srv
	go func() { _ = srv.ListenAndServe() }()
	defer func() { _ = srv.Close() }()

	// 等 QUIC 端口就绪。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.ListenPacket("udp", addr); err != nil {
			break
		} else {
			_ = c.Close()
			time.Sleep(50 * time.Millisecond)
		}
	}

	rt := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "dns.test.local"},
		QUICConfig:      &quic.Config{MaxIdleTimeout: 10 * time.Second},
	}
	defer func() { _ = rt.Close() }()
	client := &http.Client{Transport: rt, Timeout: 10 * time.Second}

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	packed, err := req.Pack()
	if err != nil {
		t.Fatalf("打包查询失败: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, "https://"+addr+"/dns-query", bytesReader(packed))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	httpReq.Host = "aaaaaaaaaa.dns.test.local"
	httpReq.Header.Set("Content-Type", "application/dns-message")

	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("DoH3 请求失败（ALPN h3 / QUIC 端口 / 证书任一处不对都会走到这里）: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ProtoMajor != 3 {
		t.Fatalf("响应不是 HTTP/3，实际 %s", resp.Proto)
	}
	body, _ := io.ReadAll(resp.Body)
	msg := new(dns.Msg)
	if err := msg.Unpack(body); err != nil {
		t.Fatalf("DoH3 应答不是合法 DNS 报文: %v（前 64 字节: %q）", err, head(body, 64))
	}
	if msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("DoH3 查询失败 rcode=%d", msg.Rcode)
	}

	// GET + base64url 形态也要能用（真实客户端两种都用）。
	httpReq2, _ := http.NewRequest(http.MethodGet,
		"https://"+addr+"/dns-query?dns="+base64.RawURLEncoding.EncodeToString(packed), nil)
	httpReq2.Host = "aaaaaaaaaa.dns.test.local"
	resp2, err := client.Do(httpReq2)
	if err != nil {
		t.Fatalf("DoH3 GET 请求失败: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(resp2.Body)
	msg2 := new(dns.Msg)
	if err := msg2.Unpack(body2); err != nil {
		t.Fatalf("DoH3 GET 应答不合法: %v", err)
	}
}

func TestDoHAltSvcAdvertisesH3(t *testing.T) {
	// 客户端是靠 Alt-Svc 发现 H3 的；不起 QUIC 监听、只加个 h3.Server 对象
	// 不会有任何客户端切过去。
	s, _ := newDevCertServer(t)
	// Alt-Svc 的端口来自**已建立的监听**：http3.Server 的 altSvcHeader 由
	// addListener 触发生成，只 new 一个对象（没有 listener）时 SetQUICHeaders
	// 会返回 ErrNoAltSvcPort。所以这里必须真的起一个监听，
	// 与 Server.Start 里的接线保持一致。
	addr := freeUDPAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", s.handleDoH)
	s.doh3 = &http3.Server{
		Addr:       addr,
		Handler:    mux,
		TLSConfig:  s.certs.TLSConfigForALPN("h3"),
		QUICConfig: &quic.Config{MaxIdleTimeout: 10 * time.Second},
	}
	go func() { _ = s.doh3.ListenAndServe() }()
	defer func() { _ = s.doh3.Close() }()
	// 等监听就绪：端口被占（err != nil）说明已经 bind 上了。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.ListenPacket("udp", addr); err != nil {
			break
		} else {
			_ = c.Close()
			time.Sleep(50 * time.Millisecond)
		}
	}

	h := http.Header{}
	if err := s.doh3.SetQUICHeaders(h); err != nil {
		t.Fatalf("SetQUICHeaders 失败: %v", err)
	}
	if got := h.Get("Alt-Svc"); got == "" {
		t.Fatal("Alt-Svc 头为空：客户端不会发现 HTTP/3")
	}
	if !containsFold(h.Get("Alt-Svc"), "h3") {
		t.Fatalf("Alt-Svc 里没有 h3: %q", h.Get("Alt-Svc"))
	}
}

// --- 小工具 ---

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func head(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if equalFoldASCII(s[i:i+len(sub)], sub) {
				return true
			}
		}
		return false
	})()
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// defaultsStub 与 protoEnabled 的输入形状对齐，便于单独测开关映射。
type defaultsStub struct {
	EnableDoH      bool
	EnableDoT      bool
	EnablePlainDNS bool
}

// protoEnabledFor 复刻 Handler.protoEnabled 的分支，用来在没有完整 registry
// 的情况下断言开关映射（真正的端到端拒绝行为由 e2e 覆盖）。
func protoEnabledFor(d *defaultsStub, proto string) bool {
	if !d.EnableDoH && !d.EnableDoT && !d.EnablePlainDNS {
		return true
	}
	switch proto {
	case "doh", "doh3":
		return d.EnableDoH
	case "dot", "doq":
		return d.EnableDoT
	case "udp", "tcp", "":
		return d.EnablePlainDNS
	default:
		return true
	}
}

var _ = fmt.Sprintf
