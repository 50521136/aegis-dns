package subsub

import (
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Format
	}{
		{
			name:    "adblock",
			content: "! Title: test\n||ads.example.com^\n||tracker.example.net^\n",
			want:    FormatAdblock,
		},
		{
			name:    "hosts",
			content: "# hosts file\n0.0.0.0 ads.example.com\n127.0.0.1 tracker.example.net\n",
			want:    FormatHosts,
		},
		{
			name:    "dnsmasq",
			content: "address=/ads.example.com/0.0.0.0\naddress=/tracker.example.net/0.0.0.0\n",
			want:    FormatDnsmasq,
		},
		{
			name:    "domain list",
			content: "ads.example.com\ntracker.example.net\nanalytics.example.io\n",
			want:    FormatDomain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sniff([]byte(tc.content)); got != tc.want {
				t.Fatalf("嗅探结果 %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestParseFormats(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantN   int
	}{
		{
			name:    "adblock",
			content: "||ads.example.com^\n@@||safe.example.com^\n",
			wantN:   2,
		},
		{
			name:    "hosts",
			content: "0.0.0.0 ads.example.com\n127.0.0.1 tracker.net\n",
			wantN:   2,
		},
		{
			name:    "纯域名列表",
			content: "ads.example.com\ntracker.net\n",
			wantN:   2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse([]byte(tc.content), FormatAuto, "blocklist")
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(res.Entries) != tc.wantN {
				t.Fatalf("期望 %d 条，实际 %d", tc.wantN, len(res.Entries))
			}
		})
	}
}

// TestParseRejectsEmpty 验证「内容为空视为异常」（文档 6.4）。
//
// 这是订阅系统最重要的一条契约：解析不出任何规则时必须报错，
// 让调用方保留旧数据，绝不能返回空列表把用户的规则集清空。
func TestParseRejectsEmpty(t *testing.T) {
	for _, content := range []string{
		"",
		"# 只有注释\n! 也是注释\n",
		"\n\n\n",
	} {
		if _, err := Parse([]byte(content), FormatAuto, "blocklist"); err == nil {
			t.Fatalf("内容 %q 应返回错误，而不是空列表", content)
		}
	}
}

// TestParseDeduplicates 验证去重与 allow 优先。
func TestParseDeduplicates(t *testing.T) {
	content := "||dup.example.com^\n||dup.example.com^\n@@||dup.example.com^\n"
	res, err := Parse([]byte(content), FormatAdblock, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("期望去重后 1 条，实际 %d", len(res.Entries))
	}
	if res.Entries[0].Kind != "allow" {
		t.Fatalf("allow 应优先于 block，实际 %q", res.Entries[0].Kind)
	}
}

// TestParseDowngradesRewrites 验证订阅里的 $dnsrewrite 按列表类型降级。
//
// 公共拦截列表用 ||x^$dnsrewrite=<黑洞域名> 表达「解析到拦截页」，
// 语义就是拦截。早先的实现把带 value 的行整条丢掉，实测某个源 1072 行
// 全被跳过、订阅直接变成 0 条 —— 规则静默消失，比报错还难发现。
func TestParseDowngradesRewrites(t *testing.T) {
	content := "||ads.example.com^\n||sinkhole.example.com^$dnsrewrite=ad-block.dns.adguard.com\n"

	res, err := Parse([]byte(content), FormatAdblock, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("改写行应降级为拦截而不是丢弃，期望 2 条，实际 %d", len(res.Entries))
	}
	if res.RewriteDowngraded != 1 {
		t.Errorf("降级计数应为 1，实际 %d", res.RewriteDowngraded)
	}
	for _, e := range res.Entries {
		if e.Domain == "sinkhole.example.com" && e.Kind != "block" {
			t.Errorf("blocklist 里的改写应降级为 block，实际 %q", e.Kind)
		}
	}

	// 白名单源里的改写意图是「别拦」，应降级为 allow。
	res2, err := Parse([]byte(content), FormatAdblock, "allowlist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, e := range res2.Entries {
		if e.Domain == "sinkhole.example.com" && e.Kind != "allow" {
			t.Errorf("allowlist 里的改写应降级为 allow，实际 %q", e.Kind)
		}
	}
}

// TestSplitByKind 验证按类型拆分。
func TestSplitByKind(t *testing.T) {
	block, allow := SplitByKind([]Entry{
		{Domain: "a.com", Kind: "block"},
		{Domain: "b.com", Kind: "allow"},
		{Domain: "c.com", Kind: "block"},
	})
	if len(block) != 2 || len(allow) != 1 {
		t.Fatalf("拆分错误: block=%d allow=%d", len(block), len(allow))
	}
}

// TestDescribeStatus 验证状态文本不会过长。
func TestDescribeStatus(t *testing.T) {
	if got := DescribeStatus(nil); got != "ok" {
		t.Fatalf("nil 应为 ok，实际 %q", got)
	}
	long := DescribeStatus(errStr(strings.Repeat("x", 500)))
	if len(long) > 210 {
		t.Fatalf("状态文本应被截断，实际长度 %d", len(long))
	}
	if !strings.HasPrefix(long, "error: ") {
		t.Fatalf("应以 error: 开头，实际 %q", long)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

// 裸 TLD 必须被丢弃。
//
// 线上事故：AdGuard Allowlist 解析出 `com` / `net` 两个单标签条目，
// 后缀匹配下它们命中所有 .com / .net 域名 —— 拦截率直接归零，
// 而列表本身看起来「只有 1028 条，很正常」。
func TestParseRejectsBareTLDAndJunk(t *testing.T) {
	content := []byte(`! Title: allowlist
@@||good.example.com^
@@com
@@net
@@clicks
@@_468x60_
@@_ad_300
@@192.168
@@||cdn.example.com^
`)
	res, err := Parse(content, FormatAdblock, "allowlist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := map[string]bool{}
	for _, e := range res.Entries {
		got[e.Domain] = true
	}
	for _, bad := range []string{"com", "net", "clicks", "_468x60_", "_ad_300", "192.168"} {
		if got[bad] {
			t.Errorf("非法条目 %q 不该进入规则集", bad)
		}
	}
	for _, ok := range []string{"good.example.com", "cdn.example.com"} {
		if !got[ok] {
			t.Errorf("正常域名 %q 被误丢", ok)
		}
	}
	if res.Invalid < 6 {
		t.Errorf("Invalid 计数不对: %d（期望至少 6）", res.Invalid)
	}
	t.Logf("保留 %d 条，丢弃非法 %d 条", len(res.Entries), res.Invalid)
}

func TestPlausibleDomain(t *testing.T) {
	good := []string{"example.com", "a.b.c.d.example.co.uk", "xn--fiqs8s.cn", "1example.com"}
	bad := []string{"", "com", "net", "192.168", "_dmarc.example.com", "-bad.com", "bad-.com",
		"a..b", "exa mple.com", "example.com/path", "*.example.com"}
	for _, d := range good {
		if !plausibleDomain(d) {
			t.Errorf("%q 应判为合法", d)
		}
	}
	for _, d := range bad {
		if plausibleDomain(d) {
			t.Errorf("%q 应判为非法", d)
		}
	}
}
