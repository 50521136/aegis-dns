package splitrule

import (
	"encoding/base64"
	"testing"
)

// AdGuard 的 [/domain/]upstream 是 gfwlist 转换产物的实际格式，
// 解析不出来整个分流就失效，所以必须钉死。
func TestParseAdblockGuardSyntax(t *testing.T) {
	content := []byte(`# comment
[/0rz.tw/]tls://1.1.1.1
[/example.com/sub.example.com/]https://doh.example/dns-query
||blocked.org^
|http://plain.net/path
@@||whitelisted.com^
! another comment
bare-domain.io
`)
	got, err := Parse(content, "adblock")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := map[string]bool{
		"0rz.tw": true, "example.com": true, "sub.example.com": true,
		"blocked.org": true, "plain.net": true, "bare-domain.io": true,
	}
	seen := map[string]bool{}
	for _, d := range got {
		seen[d] = true
	}
	for d := range want {
		if !seen[d] {
			t.Errorf("缺少域名 %q，实际解析出: %v", d, got)
		}
	}
	// @@ 例外规则不能进集合，否则白名单域名会被推去境外上游
	if seen["whitelisted.com"] {
		t.Error("@@ 例外规则不应被收录")
	}
}

// 后缀匹配：a.b.example.com 命中 example.com。
func TestSetMatchSuffix(t *testing.T) {
	s := NewSet([]string{"example.com", "google.com", "twitter.com"}, false)

	cases := []struct {
		domain string
		want   bool
	}{
		{"example.com", true},
		{"www.example.com", true},
		{"a.b.c.example.com", true},
		{"WWW.EXAMPLE.COM", true},  // 大小写不敏感
		{"www.example.com.", true}, // 尾点
		{"notexample.com", false},  // 不能误匹配
		{"example.com.cn", false},  // 不能误匹配
		{"google.com", true},
		{"www.qq.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := s.Match(c.domain); got != c.want {
			t.Errorf("Match(%q) = %v, 期望 %v", c.domain, got, c.want)
		}
	}
}

// 空集合必须全部返回 false —— 规则没加载成功时不能把流量推向境外。
func TestEmptySetNeverMatches(t *testing.T) {
	for _, s := range []*Set{nil, NewSet(nil, false), NewSet([]string{}, false)} {
		if s.Match("www.google.com") {
			t.Error("空集合不应命中任何域名")
		}
	}
}

// 白名单语义：命中 → 国内（false），未命中 → 境外（true）。
func TestInverseSet(t *testing.T) {
	s := NewSet([]string{"qq.com", "baidu.com"}, true)
	if s.Match("www.qq.com") {
		t.Error("白名单模式下 qq.com 是国内域名，不该走境外")
	}
	if !s.Match("www.google.com") {
		t.Error("白名单模式下未命中应走境外")
	}
	if !s.Inverse() {
		t.Error("Inverse() 应返回 true")
	}
}

// gfwlist 原始文件是 Base64 编码的，必须能解开。
func TestParseGFWListBase64(t *testing.T) {
	plain := "||google.com^\n||twitter.com^\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	// 模拟真实文件的换行
	var wrapped []byte
	for i, c := range []byte(encoded) {
		wrapped = append(wrapped, c)
		if i > 0 && i%64 == 0 {
			wrapped = append(wrapped, '\n')
		}
	}

	got, err := Parse(wrapped, "gfwlist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	seen := map[string]bool{}
	for _, d := range got {
		seen[d] = true
	}
	if !seen["google.com"] || !seen["twitter.com"] {
		t.Errorf("Base64 gfwlist 解析不全: %v", got)
	}
}

// 明文形式也要能处理（有些镜像直接给明文）。
func TestParseGFWListPlainFallback(t *testing.T) {
	got, err := Parse([]byte("||google.com^\n"), "gfwlist")
	if err != nil {
		t.Fatalf("明文回退失败: %v", err)
	}
	if len(got) != 1 || got[0] != "google.com" {
		t.Errorf("明文解析结果 = %v, 期望 [google.com]", got)
	}
}

// dnsmasq 格式：server=/domain/ip
func TestParseDnsmasq(t *testing.T) {
	content := []byte(`# comment
server=/qq.com/114.114.114.114
server=/baidu.com/223.5.5.5
server=/cn/
`)
	got, err := Parse(content, "dnsmasq-inverse")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	seen := map[string]bool{}
	for _, d := range got {
		seen[d] = true
	}
	if !seen["qq.com"] || !seen["baidu.com"] {
		t.Errorf("dnsmasq 解析不全: %v", got)
	}
	// 单标签 "cn" 不含点，应被过滤
	if seen["cn"] {
		t.Error("单标签域名不应被收录")
	}
}

// 集合构建要去重，否则 11 万条里大量重复会白白占内存。
func TestNewSetDedup(t *testing.T) {
	s := NewSet([]string{"a.com", "A.COM", ".a.com", "a.com.", "b.com"}, false)
	if s.Size() != 2 {
		t.Errorf("去重后应为 2 条，实际 %d: %v", s.Size(), s.sorted)
	}
}

// 含通配符或非域名内容的规则必须跳过，不能污染集合。
func TestCleanRuleDomainRejects(t *testing.T) {
	bad := []string{"", "   ", "ad*.example.com",
		"no-dot", "http://", "||"}
	for _, b := range bad {
		if got := cleanRuleDomain(b); got != "" {
			t.Errorf("cleanRuleDomain(%q) = %q, 期望空", b, got)
		}
	}
}
