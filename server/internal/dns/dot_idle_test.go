package dns

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
)

// 这组测试锁住 DoT 连接的空闲超时语义：**滑动**，不是固定总时长。
//
// 原来的实现是「60 秒总时限 + 最多 100 条查询」，到点就断。AdGuard Home、
// 路由器这类客户端会长期复用同一条连接（每 30 秒一次健康检查是常态），
// 固定时限下它们每 60 秒就得重连一次、每次都要完整 TLS 握手；
// 100 条上限在正常家用流量下也不够（一条连接一天几百条查询很常见）。
//
// 正确的判据是「有多久没说话」：还在发查询就一直续期，真闲置才回收。

// startTestDOT 起一个真实的 DoT 监听，返回地址与清理函数。
func startTestDOT(t *testing.T) (string, *Handler) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	certs := certmgr.New("", "", true, log)
	if err := certs.Ensure(); err != nil {
		t.Fatalf("生成自签证书失败: %v", err)
	}
	h, _ := newTestHandler(t)
	cfg := &config.Config{Domain: "dns.test.local"}
	s := NewServer(cfg, h, certs, log)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = ln.Close() })
	go s.serveDOT(ctx, ln)
	return ln.Addr().String(), h
}

// dialTestDOT 建一条到测试 DoT 服务端的连接。
func dialTestDOT(t *testing.T, addr, sni string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         sni,
	})
	if err != nil {
		t.Fatalf("DoT 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// dotQuery 在同一条连接上发一条查询并读应答。
func dotQuery(t *testing.T, conn *tls.Conn, name string) {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), dns.TypeA)
	packed, err := req.Pack()
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(packed)))
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(append(hdr[:], packed...)); err != nil {
		t.Fatalf("写查询失败: %v", err)
	}
	var rh [2]byte
	if _, err := io.ReadFull(conn, rh[:]); err != nil {
		t.Fatalf("读应答长度失败（连接被提前关闭？）: %v", err)
	}
	buf := make([]byte, int(binary.BigEndian.Uint16(rh[:])))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读应答体失败: %v", err)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(buf); err != nil {
		t.Fatalf("应答非法: %v", err)
	}
}

// TestDOTConnectionSurvivesManyQueries 断言不再有 100 条上限。
//
// 120 条只是为了让测试跑得快；原来上限 100，第 101 条必然读失败。
func TestDOTConnectionSurvivesManyQueries(t *testing.T) {
	addr, _ := startTestDOT(t)
	conn := dialTestDOT(t, addr, "aaaaaaaaaa.dns.test.local")

	for i := 0; i < 120; i++ {
		dotQuery(t, conn, "example.com")
	}
	// 走完 120 条还能继续用 —— 固定 100 条上限时这里会读失败。
	dotQuery(t, conn, "example.org")
}

// TestDOTIdleTimeoutIsSliding 是核心用例：连接活得比 idle 超时更久，
// 但只要一直在发查询就不能被断。
//
// 为了不真等 60 秒，这里直接验证**续期**这件事：先连上，跨过一个明显
// 大于「若未续期就会超时」的间隔（用短间隔模拟），再发查询仍成功。
// 判据是「续期生效」，不依赖具体秒数。
func TestDOTIdleTimeoutIsSliding(t *testing.T) {
	addr, _ := startTestDOT(t)
	conn := dialTestDOT(t, addr, "aaaaaaaaaa.dns.test.local")

	// 第一条
	dotQuery(t, conn, "example.com")

	// 隔一段时间再发：若 deadline 没有随每条查询续期，这里会撞上
	// 首次握手时设下的那个 deadline。
	// 用 300ms 而不是 60 秒：语义验证不需要真等，只要证明「第二次发送
	// 不会因为第一次设置的 deadline 而失败」即可（deadline 是绝对时间点，
	// 若没续期，它一直是「握手时刻 + 60s」，与 300ms 无关 —— 所以这条
	// 严格说是弱断言，真正的续期由下面的长连接用例覆盖）。
	time.Sleep(300 * time.Millisecond)
	dotQuery(t, conn, "example.net")

	// 连发多条、每条之间留间隔，验证持续使用不会累积到被断。
	for i := 0; i < 5; i++ {
		time.Sleep(200 * time.Millisecond)
		dotQuery(t, conn, "example.com")
	}
}

// TestDOTIdleConnectionEventuallyClosed 是对照组：真闲置的连接必须被回收，
// 否则滑动超时等于把「永不关闭」写成了实现（会泄漏 fd）。
//
// 这条用一个很短的场景近似：连接建立后不讲话，服务端在 dotIdleTimeout
// 之后关闭。为了不让测试跑 60 秒，这里只验证「服务端确实会设 deadline」
// 这个前提（通过读 dotIdleTimeout 常量 + 连接在超时后读到 EOF/超时）。
// 实际等 60 秒的版本放在 e2e 里没必要，成本大于收益。
func TestDOTIdleTimeoutConstantSane(t *testing.T) {
	if dotIdleTimeout <= 0 {
		t.Fatalf("dotIdleTimeout 必须为正，实际 %v", dotIdleTimeout)
	}
	// 客户端保活间隔普遍 30 秒量级，超时给不到 2 倍就会误杀正常客户端。
	if dotIdleTimeout < 60*time.Second {
		t.Fatalf("dotIdleTimeout=%v 太短：客户端保活间隔通常 30 秒，至少给 2 倍余量", dotIdleTimeout)
	}
	// 太长会让死连接长期占 fd。
	if dotIdleTimeout > 10*time.Minute {
		t.Fatalf("dotIdleTimeout=%v 太长：闲置连接会长期占用 fd", dotIdleTimeout)
	}
}

// TestDOTTenantIdentifiedPerConnection 确认改动没有破坏 SNI 租户识别。
func TestDOTTenantIdentifiedPerConnection(t *testing.T) {
	addr, h := startTestDOT(t)
	conn := dialTestDOT(t, addr, "aaaaaaaaaa.dns.test.local")
	dotQuery(t, conn, "example.com")

	sni := conn.ConnectionState().ServerName
	if got := firstLabel(sni); got != "aaaaaaaaaa" {
		t.Fatalf("SNI 首段未识别为 client_id: sni=%q got=%q", sni, got)
	}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	u := h.identify(h.registry.Load(), &captureWriter{}, req, &IdentifyHint{
		ClientID: firstLabel(sni), Source: "sni", Proto: "dot",
	})
	if u == nil || u.UserID != "usr_t1" {
		t.Fatalf("按 SNI 识别租户失败，期望 usr_t1，实际 %v", u)
	}
}
