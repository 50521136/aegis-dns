package id

import (
	"strings"
	"testing"
)

func TestClientIDFormat(t *testing.T) {
	for i := 0; i < 500; i++ {
		c := NewClientID()
		if len(c) != ClientIDLen {
			t.Fatalf("长度应为 %d，实际 %d (%q)", ClientIDLen, len(c), c)
		}
		if !ValidClientID(c) {
			t.Fatalf("%q 未通过校验", c)
		}
		// base32 小写字母表必须排除 0/1/8/9，避免与 O/I/B 混淆。
		for _, ch := range c {
			if strings.ContainsRune("0189", ch) {
				t.Fatalf("client_id %q 含被排除的字符 %q", c, string(ch))
			}
		}
	}
}

func TestClientIDUniqueness(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		c := NewClientID()
		if seen[c] {
			t.Fatalf("client_id 重复: %q", c)
		}
		seen[c] = true
	}
}

func TestValidClientID(t *testing.T) {
	// 校验接受 a-z0-9，长度 4-32。
	//
	// 生成侧仍用窄字符集（a-z2-7）避免 0/o、1/l 混淆，但校验侧必须更宽：
	// 用户自定义 client_id 时不该被内部约定绑住，叫 shenzhen01 是合理诉求。
	// 校验放宽不会让旧的合法值失效，也不会让非 ASCII / 符号混进来。
	valid := []string{
		"k7m2pqx4ab", "abcd", "a2345672345672345672345672345672",
		"k7m2p9xq4a", "shenzhen01", "k7m2pqx4a0", "k7m2pqx4a8", "k7m2pqx4a9",
	}
	invalid := []string{
		"", "abc", "K7M2PQX4AB", "k7m2pqx4ab!", strings.Repeat("a", 33),
		"home-dns", "home.dns", "home_dns", "中文中文中文",
	}
	for _, v := range valid {
		if !ValidClientID(v) {
			t.Errorf("%q 应通过校验", v)
		}
	}
	for _, v := range invalid {
		if ValidClientID(v) {
			t.Errorf("%q 不应通过校验", v)
		}
	}
}

func TestUUIDv7Monotonic(t *testing.T) {
	var prev string
	for i := 0; i < 2000; i++ {
		cur := UUIDv7()
		if prev != "" && cur <= prev {
			t.Fatalf("UUID v7 应单调递增: prev=%q cur=%q", prev, cur)
		}
		prev = cur
	}
}

func TestNewPrefix(t *testing.T) {
	for _, p := range []string{PrefixUser, PrefixRule, PrefixSub} {
		v := New(p)
		if !strings.HasPrefix(v, p) {
			t.Fatalf("%q 应以 %q 开头", v, p)
		}
		if len(v) != len(p)+36 {
			t.Fatalf("%q 长度应为 %d，实际 %d", v, len(p)+36, len(v))
		}
	}
}

func TestNewPAT(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p := NewPAT()
		if !strings.HasPrefix(p, "agx_") {
			t.Fatalf("PAT 应以 agx_ 开头: %q", p)
		}
		if len(p) < 40 {
			t.Fatalf("PAT 长度不足: %d", len(p))
		}
		if seen[p] {
			t.Fatalf("PAT 重复: %q", p)
		}
		seen[p] = true
	}
}

func TestCheckCustomClientID(t *testing.T) {
	cases := []struct {
		in  string
		ok  bool
		why string
	}{
		{"shenzhen01", true, "6 位以上小写字母+数字"},
		{"abcdef", true, "刚好 6 位"},
		{"myhome8888", true, "含 8"},
		{"home01", true, "含 0 和 1"},
		{"abcde", false, "5 位，不够"},
		{"", false, "空"},
		{"home-dns", false, "含连字符"},
		{"home.dns", false, "含点"},
		{"home_dns", false, "含下划线"},
		{"HomeDns", false, "含大写（调用方会先转小写，这里测原始校验）"},
		{"中文中文中文", false, "非 ASCII"},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false, "33 位，超长"},
	}
	for _, c := range cases {
		err := CheckCustomClientID(c.in)
		if (err == nil) != c.ok {
			t.Errorf("CheckCustomClientID(%q) err=%v，期望 ok=%v（%s）", c.in, err, c.ok, c.why)
		}
	}
}

// 生成侧的字符集必须保持窄：0/1/8/9 在不少字体里和 o/l 分不清。
func TestGeneratedClientIDStaysAmbiguityFree(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := NewClientID()
		if len(s) != ClientIDLen {
			t.Fatalf("生成长度 %d，期望 %d", len(s), ClientIDLen)
		}
		for _, c := range s {
			if strings.ContainsRune("0189", c) {
				t.Fatalf("生成的 client_id %q 含易混字符 %q", s, string(c))
			}
		}
		if !ValidClientID(s) {
			t.Fatalf("生成的 client_id %q 未通过校验", s)
		}
	}
}
