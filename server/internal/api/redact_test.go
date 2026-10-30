package api

import (
	"strings"
	"testing"
)

// 用户看到的上游只能是协议类型，绝不能带出主机名或订阅 ID。
// 个人版 DoH 的地址里那段（909111-m2tdhgkjlcpife47）就是接入方账号标识。
func TestRedactUpstreamHidesHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://909111-m2tdhgkjlcpife47.alidns.com/dns-query", "DoH"},
		{"quic://dns.alidns.com:853", "DoQ"},
		{"tls://dns.alidns.com:853", "DoT"},
		{"https://sm2.doh.pub/dns-query", "DoH"},
		{"119.29.29.29:53", "UDP"},
		{"udp://223.5.5.5:53", "UDP"},
		{"tcp://223.5.5.5:53", "TCP"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := redactUpstream(c.in)
		if got != c.want {
			t.Errorf("redactUpstream(%q) = %q, 期望 %q", c.in, got, c.want)
		}
		if c.in != "" && got != "" {
			// 脱敏后的值里不能残留原始主机片段
			for _, leak := range []string{"alidns.com", "doh.pub", "909111", "223.5.5.5", "119.29.29.29"} {
				if strings.Contains(got, leak) {
					t.Errorf("redactUpstream(%q) 泄漏了 %q: %q", c.in, leak, got)
				}
			}
		}
	}
}

// 管理员必须能看到完整地址，否则无法定位是哪个上游出的问题。
func TestVisibleUpstreamForAdmin(t *testing.T) {
	addr := "https://909111-m2tdhgkjlcpife47.alidns.com/dns-query"
	if got := visibleUpstream(addr, true); got != addr {
		t.Errorf("管理员应看到完整地址，得到 %q", got)
	}
	if got := visibleUpstream(addr, false); got != "DoH" {
		t.Errorf("普通用户应只看到协议类型，得到 %q", got)
	}
}
