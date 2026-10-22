package dns

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

// 回归测试：缓存命中时，OPT 必须按「本次请求」重新校准。
//
// 之前缓存存的是首次应答的完整副本，带着首个客户端的 OPT。
// 后果有两个：
//   - 不带 EDNS 的客户端会收到一段它没请求过的 OPT（违反 RFC 6891 §6.1.1）
//   - 512 缓冲的客户端会收到一个标着 4096 的应答，路径 MTU 一卡就丢包
func TestCacheHitAlignsOPTToCurrentRequest(t *testing.T) {
	c := NewCache(100, 60, 86400)

	// 首个客户端：带 EDNS，自己声明 4096
	reqA := new(dns.Msg)
	reqA.SetQuestion("opt.example.com.", dns.TypeA)
	reqA.SetEdns0(4096, false)

	respA := new(dns.Msg)
	respA.SetReply(reqA)
	respA.Answer = append(respA.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: "opt.example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{1, 2, 3, 4},
	})
	respA.SetEdns0(4096, false)

	key := CacheKey{UserID: "u1", QName: "opt.example.com", QType: dns.TypeA}
	if !c.PutWith(key, respA, reqA, false) {
		t.Fatal("写入缓存失败")
	}

	// 情况一：客户端完全不带 EDNS —— 应答里不能有 OPT
	reqB := new(dns.Msg)
	reqB.SetQuestion("opt.example.com.", dns.TypeA)

	got, hit, _, _ := c.GetWithAllow(key, reqB)
	if !hit {
		t.Fatal("应当命中")
	}
	if opt := got.IsEdns0(); opt != nil {
		t.Errorf("请求无 EDNS 却返回了 OPT（UDPSize=%d），违反 RFC 6891", opt.UDPSize())
	}
	if len(got.Answer) != 1 {
		t.Errorf("应答记录不该被 OPT 处理连累，期望 1 条，实际 %d", len(got.Answer))
	}

	// 情况二：客户端带 EDNS 1232 —— OPT 要对齐成 1232，不能还是 4096
	reqC := new(dns.Msg)
	reqC.SetQuestion("opt.example.com.", dns.TypeA)
	reqC.SetEdns0(1232, false)

	got2, hit2, _, _ := c.GetWithAllow(key, reqC)
	if !hit2 {
		t.Fatal("第二次也应命中")
	}
	opt2 := got2.IsEdns0()
	if opt2 == nil {
		t.Fatal("请求带 EDNS，应答却没有 OPT")
	}
	if opt2.UDPSize() != 1232 {
		t.Errorf("OPT 的 UDP 大小没对齐，期望 1232，实际 %d", opt2.UDPSize())
	}
}

// fakeUDPWriter 是一个最小的 UDP ResponseWriter，用来验证截断。
type fakeUDPWriter struct {
	written *dns.Msg
	network string
}

func (w *fakeUDPWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
}
func (w *fakeUDPWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (w *fakeUDPWriter) WriteMsg(m *dns.Msg) error {
	w.written = m
	return nil
}
func (w *fakeUDPWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *fakeUDPWriter) Close() error                { return nil }
func (w *fakeUDPWriter) TsigStatus() error           { return nil }
func (w *fakeUDPWriter) TsigTimersOnly(bool)         {}
func (w *fakeUDPWriter) Hijack()                     {}

// bigResponse 造一个必然超过 512 字节的应答。
func bigResponse(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	for i := 0; i < 40; i++ {
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   "big.example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{10, 0, byte(i / 256), byte(i)},
		})
	}
	return resp
}

// 回归测试：UDP 上的大应答必须置 TC 位并截断。
//
// 之前完全没有截断处理，大应答整包走 UDP 发出去，客户端缓冲装不下
// 就直接丢 —— 表现为「偶尔查不到」，服务端这边看起来一切正常。
func TestUDPLargeResponseIsTruncated(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("big.example.com.", dns.TypeA)

	full := bigResponse(req)
	if full.Len() <= dns.MinMsgSize {
		t.Fatalf("测试前提不成立：应答只有 %d 字节，没超过 512", full.Len())
	}
	t.Logf("原始应答 %d 字节", full.Len())

	// 无 EDNS：上限 512，必须截断并置 TC
	w := &fakeUDPWriter{}
	writeMsg(w, req, bigResponse(req))
	if w.written == nil {
		t.Fatal("没有写出应答")
	}
	if !w.written.Truncated {
		t.Error("超过 512 字节却没有置 TC 位")
	}
	if n := w.written.Len(); n > dns.MinMsgSize {
		t.Errorf("截断后仍有 %d 字节，超过 512 上限", n)
	}

	// 带 EDNS 4096：能装下就不该截断
	reqE := new(dns.Msg)
	reqE.SetQuestion("big.example.com.", dns.TypeA)
	reqE.SetEdns0(4096, false)
	w2 := &fakeUDPWriter{}
	writeMsg(w2, reqE, bigResponse(reqE))
	if w2.written == nil {
		t.Fatal("没有写出应答")
	}
	if w2.written.Truncated {
		t.Errorf("4096 缓冲下不该截断（应答 %d 字节）", w2.written.Len())
	}
	if len(w2.written.Answer) != len(full.Answer) {
		t.Errorf("不该丢记录：期望 %d 条，实际 %d 条", len(full.Answer), len(w2.written.Answer))
	}
}

// TCP 是流，不能截断。
func TestTCPResponseNotTruncated(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("big.example.com.", dns.TypeA)

	w := &tcpWriter{}
	writeMsg(w, req, bigResponse(req))
	if w.written == nil {
		t.Fatal("没有写出应答")
	}
	if w.written.Truncated {
		t.Error("TCP 上不该截断")
	}
	if len(w.written.Answer) != 40 {
		t.Errorf("TCP 上不该丢记录，实际 %d 条", len(w.written.Answer))
	}
}

type tcpWriter struct{ written *dns.Msg }

func (w *tcpWriter) LocalAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53} }
func (w *tcpWriter) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (w *tcpWriter) WriteMsg(m *dns.Msg) error {
	w.written = m
	return nil
}
func (w *tcpWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *tcpWriter) Close() error                { return nil }
func (w *tcpWriter) TsigStatus() error           { return nil }
func (w *tcpWriter) TsigTimersOnly(bool)         {}
func (w *tcpWriter) Hijack()                     {}
