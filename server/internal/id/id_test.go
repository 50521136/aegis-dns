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
	// 注意：设计文档 3.1 的「示例 k7m2p9xq4a」与同表给出的字符集
	// （a-z + 2-7，即 ^[a-z2-7]{10}$）自相矛盾 —— 该示例含 '9'，按字符集
	// 规则本应非法。实现以显式的字符集规则为准，示例仅视为笔误。
	valid := []string{"k7m2pqx4ab", "abcd", "a2345672345672345672345672345672"}
	invalid := []string{
		"", "abc", "K7M2PQX4AB", "k7m2p9xq4a", "k7m2pqx4ab!", strings.Repeat("a", 33),
		"k7m2pqx4a0", "k7m2pqx4a1", "k7m2pqx4a8", "k7m2pqx4a9",
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
