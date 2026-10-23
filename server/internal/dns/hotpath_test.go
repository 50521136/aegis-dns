package dns

import (
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// 这个文件锁住热路径优化的「行为不变」契约。
//
// 优化动机来自基准数据：
//
//	answerFrom   5070ns / 23 allocs  →  1415ns / 8 allocs
//	maskIP        532ns /  1 alloc     →    62ns / 0 alloc
//	缓存命中路径  9244ns              →  6799ns
//
// 优化只能改「怎么算」，不能改「算出什么」：日志里的应答详情和客户端网段
// 是排障的第一手依据，格式一变，之前的排查经验就全部失效。

// TestFormatRRCommonTypes 断言常见记录类型的日志格式稳定。
func TestFormatRRCommonTypes(t *testing.T) {
	cases := []struct {
		rr   string
		want string
	}{
		{"www.example.com. 300 IN A 93.184.216.34", "A 93.184.216.34"},
		{"www.example.com. 300 IN AAAA 2606:2800:220:1:248:1893:25c8:1946", "AAAA 2606:2800:220:1:248:1893:25c8:1946"},
		{"www.example.com. 300 IN CNAME example.com.", "CNAME example.com."},
		{"example.com. 300 IN NS ns1.example.com.", "NS ns1.example.com."},
		{"example.com. 300 IN MX 10 mail.example.com.", "MX 10 mail.example.com."},
		{"4.3.2.1.in-addr.arpa. 300 IN PTR host.example.com.", "PTR host.example.com."},
		{"_sip._tcp.example.com. 300 IN SRV 10 60 5060 sip.example.com.", "SRV 10 60 5060 sip.example.com."},
	}
	for _, c := range cases {
		rr, err := dns.NewRR(c.rr)
		if err != nil {
			t.Fatalf("构造记录失败 %q: %v", c.rr, err)
		}
		if got := formatRR(rr); got != c.want {
			t.Errorf("%q\n  期望 %q\n  实际 %q", c.rr, c.want, got)
		}
	}
}

// TestFormatRRFallback 断言少见类型仍带出信息（不能返回空）。
func TestFormatRRFallback(t *testing.T) {
	rr, err := dns.NewRR(`example.com. 300 IN CAA 0 issue "letsencrypt.org"`)
	if err != nil {
		t.Skipf("构造 CAA 失败: %v", err)
	}
	got := formatRR(rr)
	if got == "" {
		t.Fatal("少见类型不能返回空，否则日志里看不到任何信息")
	}
	if !strings.Contains(got, "letsencrypt.org") {
		t.Fatalf("回退格式应保留记录内容，实际 %q", got)
	}
}

// TestAnswerFromCapsAtEight 断言最多 8 条 + 省略号。
func TestAnswerFromCapsAtEight(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("many.example.com.", dns.TypeA)
	for i := 0; i < 12; i++ {
		rr, _ := dns.NewRR("many.example.com. 300 IN A 10.0.0." + itoa(i))
		m.Answer = append(m.Answer, rr)
	}
	got := answerFrom(m)
	lines := strings.Split(got, "\n")
	if len(lines) != 9 { // 8 条 + "…"
		t.Fatalf("应输出 8 条加省略号，实际 %d 行：%q", len(lines), got)
	}
	if lines[8] != "…" {
		t.Fatalf("第 9 行应是省略号，实际 %q", lines[8])
	}
}

// TestMaskIPStable 断言脱敏结果与优化前逐字节一致。
func TestMaskIPStable(t *testing.T) {
	cases := []struct {
		ip   string
		want string
	}{
		{"117.136.110.163", "117.136.110.0/24"},
		{"1.2.3.4", "1.2.3.0/24"},
		{"255.255.255.255", "255.255.255.0/24"},
		{"10.0.0.1", "10.0.0.0/24"},
		{"192.168.1.1", "192.168.1.0/24"},
	}
	for _, c := range cases {
		got := maskIP(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("%s 期望 %q，实际 %q", c.ip, c.want, got)
		}
	}
}

// TestMaskIPCacheConsistent 断言缓存命中与首次计算给出相同结果。
//
// 掩码结果进了 sync.Map，如果键算错，不同网段会串成同一个值 ——
// 那会让日志里的来源定位完全失效。
func TestMaskIPCacheConsistent(t *testing.T) {
	pairs := [][2]string{
		{"117.136.110.163", "117.136.110.0/24"},
		{"117.136.111.5", "117.136.111.0/24"}, // 只差第三段，必须是不同结果
		{"117.137.110.7", "117.137.110.0/24"}, // 只差第二段
		{"118.136.110.9", "118.136.110.0/24"}, // 只差第一段
	}
	for round := 0; round < 3; round++ { // 多轮触发缓存命中路径
		for _, p := range pairs {
			if got := maskIP(net.ParseIP(p[0])); got != p[1] {
				t.Fatalf("第 %d 轮 %s 期望 %q，实际 %q", round, p[0], p[1], got)
			}
		}
	}
}

// TestMaskIPv6Unchanged 断言 IPv6 走 /48（这条路径没优化，行为不能变）。
//
// /48 = 48 位 = 前 6 个字节 = 3 组 4 位十六进制，所以输出形如
// "2001:0db8:1234::/48"，不是 6 组。
func TestMaskIPv6Unchanged(t *testing.T) {
	got := maskIP(net.ParseIP("2001:db8:1234:5678:9abc:def0:1234:5678"))
	if !strings.HasSuffix(got, "::/48") {
		t.Fatalf("IPv6 应输出 /48，实际 %q", got)
	}
	if got != "2001:0db8:1234::/48" {
		t.Fatalf("IPv6 前缀应取前 48 位，实际 %q", got)
	}
}

// TestMaskIPNil 断言空地址不 panic。
func TestMaskIPNil(t *testing.T) {
	if got := maskIP(nil); got != "" {
		t.Fatalf("nil 应返回空串，实际 %q", got)
	}
}
