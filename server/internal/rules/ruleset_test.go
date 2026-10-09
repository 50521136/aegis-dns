package rules

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// TestParseLine 覆盖文档 5.1 表格里的每一种语法。
func TestParseLine(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		want    *Parsed
		wantErr bool
		skip    bool
	}{
		{
			name: "Adblock 拦截",
			line: "||ads.example.com^",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com"},
		},
		{
			name: "Adblock 白名单",
			line: "@@||safe.example.com^",
			want: &Parsed{Kind: model.KindAllow, Domain: "safe.example.com"},
		},
		{
			name: "裸域名等价于 ||domain^",
			line: "ads.example.com",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com"},
		},
		{
			name: "hosts 0.0.0.0",
			line: "0.0.0.0 ads.example.com",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com"},
		},
		{
			name: "hosts 127.0.0.1",
			line: "127.0.0.1 ads.example.com",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com"},
		},
		{
			name: "dnstype 限定",
			line: "||ads.example.com^$dnstype=AAAA",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com", QTypes: []string{"AAAA"}},
		},
		{
			name: "dnstype 多值",
			line: "||ads.example.com^$dnstype=A|AAAA",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com", QTypes: []string{"A", "AAAA"}},
		},
		{
			name: "dnsrewrite IPv4",
			line: "||old.example.com^$dnsrewrite=1.2.3.4",
			want: &Parsed{Kind: model.KindRewriteA, Domain: "old.example.com", Value: "1.2.3.4"},
		},
		{
			name: "dnsrewrite IPv6",
			line: "||old.example.com^$dnsrewrite=2001:db8::1",
			want: &Parsed{Kind: model.KindRewriteAAAA, Domain: "old.example.com", Value: "2001:db8::1"},
		},
		{
			name: "dnsrewrite CNAME",
			line: "||old.example.com^$dnsrewrite=new.example.com",
			want: &Parsed{Kind: model.KindRewriteCNAME, Domain: "old.example.com", Value: "new.example.com"},
		},
		{
			name: "通配改写",
			line: "*.dev.local^$dnsrewrite=127.0.0.1",
			want: &Parsed{Kind: model.KindRewriteA, Domain: "dev.local", Wildcard: true, Value: "127.0.0.1"},
		},
		{
			name: "dnsmasq 形式",
			line: "address=/ads.example.com/0.0.0.0",
			want: &Parsed{Kind: model.KindBlock, Domain: "ads.example.com"},
		},
		{name: "注释 !", line: "! 这是注释", skip: true},
		{name: "注释 #", line: "# 这是注释", skip: true},
		{name: "空行", line: "   ", skip: true},
		{name: "未知通配位置", line: "ad*.example.com", skip: true},
		{name: "IP 字面量", line: "1.2.3.4", wantErr: true},
		{name: "空域名", line: "||^", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLine(tc.line)
			if tc.skip {
				if err != ErrSkip {
					t.Fatalf("期望 ErrSkip，实际 %v (got=%+v)", err, got)
				}
				return
			}
			if tc.wantErr {
				if err == nil || err == ErrSkip {
					t.Fatalf("期望解析失败，实际 err=%v got=%+v", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got.Kind != tc.want.Kind {
				t.Errorf("kind: 期望 %q 实际 %q", tc.want.Kind, got.Kind)
			}
			if got.Domain != tc.want.Domain {
				t.Errorf("domain: 期望 %q 实际 %q", tc.want.Domain, got.Domain)
			}
			if got.Value != tc.want.Value {
				t.Errorf("value: 期望 %q 实际 %q", tc.want.Value, got.Value)
			}
			if got.Wildcard != tc.want.Wildcard {
				t.Errorf("wildcard: 期望 %v 实际 %v", tc.want.Wildcard, got.Wildcard)
			}
			if len(got.QTypes) != len(tc.want.QTypes) {
				t.Errorf("qtypes: 期望 %v 实际 %v", tc.want.QTypes, got.QTypes)
			}
		})
	}
}

// TestMatchPriority 验证文档 5.3 的优先级顺序。
func TestMatchPriority(t *testing.T) {
	rs, err := Build([]model.Rule{
		{Kind: model.KindBlock, Pattern: "||ads.example.com^", Enabled: true},
		{Kind: model.KindAllow, Pattern: "||safe.ads.example.com^", Enabled: true},
		{Kind: model.KindRewriteA, Pattern: "||nas.home^", Value: "192.168.1.10", Enabled: true},
		{Kind: model.KindBlock, Pattern: "||blocked-by-sub.com^", Enabled: true},
	}, []string{"allowed-by-sub.com"}, []string{"tracker.example.net", "blocked-by-sub.com"})
	if err != nil {
		t.Fatalf("构建规则集失败: %v", err)
	}

	cases := []struct {
		qname  string
		want   Action
		source string
	}{
		// 优先级 1：用户白名单最高，即使父域被 block 也要放行。
		{"safe.ads.example.com", ActionAllow, "user_allow"},
		// 优先级 2：改写优先于拦截。
		{"nas.home", ActionRewrite, "user_rewrite"},
		// 优先级 3：用户自定义拦截，且子域继承。
		{"ads.example.com", ActionBlock, "user_block"},
		{"x.y.ads.example.com", ActionBlock, "user_block"},
		// 优先级 4：订阅白名单。
		{"allowed-by-sub.com", ActionAllow, "sub_allow"},
		// 优先级 5：订阅黑名单。
		{"tracker.example.net", ActionBlock, "sub_block"},
		// 未命中。
		{"example.org", ActionNone, ""},
	}

	for _, tc := range cases {
		t.Run(tc.qname, func(t *testing.T) {
			d := rs.Match(tc.qname, dns.TypeA)
			if d.Action != tc.want {
				t.Fatalf("动作: 期望 %v 实际 %v", tc.want, d.Action)
			}
			if d.Source != tc.source {
				t.Errorf("来源: 期望 %q 实际 %q", tc.source, d.Source)
			}
		})
	}
}

// TestRewriteQTypeScoping 验证 $dnstype 与「部分改写」语义（文档 5.5）。
func TestRewriteQTypeScoping(t *testing.T) {
	rs, err := Build([]model.Rule{
		{Kind: model.KindRewriteA, Pattern: "||only-a.test^", Value: "10.0.0.1", QTypes: []string{"A"}, Enabled: true},
	}, nil, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	if d := rs.Match("only-a.test", dns.TypeA); d.Action != ActionRewrite {
		t.Fatalf("A 查询应命中改写，实际 %v", d.Action)
	}
	// AAAA 查询不该命中限定 A 的规则，应继续转发上游。
	if d := rs.Match("only-a.test", dns.TypeAAAA); d.Action != ActionNone {
		t.Fatalf("AAAA 查询不应命中限定 A 的规则，实际 %v (source=%s)", d.Action, d.Source)
	}
}

// TestWildcardRule 验证 *.domain 同时匹配根域自身与所有子域。
//
// 这与 OISD / 1Hosts 等主流列表的语义一致：列表头明确写明
// 「*.example.com should block access to "example.com" and "subdomain.example.com"」。
// 若按子域-only 处理，这些列表的条目几乎全部失效。
func TestWildcardRule(t *testing.T) {
	rs, err := Build([]model.Rule{
		{Kind: model.KindBlock, Pattern: "*.dev.local", Enabled: true},
	}, nil, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	if d := rs.Match("dev.local", dns.TypeA); d.Action != ActionBlock {
		t.Fatalf("*.domain 应匹配根域自身，实际 %v", d.Action)
	}
	if d := rs.Match("a.dev.local", dns.TypeA); d.Action != ActionBlock {
		t.Fatalf("通配规则应匹配子域，实际 %v", d.Action)
	}
	if d := rs.Match("a.b.dev.local", dns.TypeA); d.Action != ActionBlock {
		t.Fatalf("通配规则应匹配多级子域，实际 %v", d.Action)
	}
	// 不能误伤后缀相同的其它域名。
	if d := rs.Match("notdev.local", dns.TypeA); d.Action != ActionNone {
		t.Fatalf("不应匹配 notdev.local，实际 %v", d.Action)
	}
	if d := rs.Match("xdev.local", dns.TypeA); d.Action != ActionNone {
		t.Fatalf("不应匹配 xdev.local，实际 %v", d.Action)
	}
}

// TestWildcardEqualsAdblockForm 验证 *.domain 与 ||domain^ 等价。
func TestWildcardEqualsAdblockForm(t *testing.T) {
	cases := []string{"dev.local", "a.dev.local", "a.b.dev.local", "other.com"}

	star, err := Build([]model.Rule{{Kind: model.KindBlock, Pattern: "*.dev.local", Enabled: true}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := Build([]model.Rule{{Kind: model.KindBlock, Pattern: "||dev.local^", Enabled: true}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range cases {
		a := star.Match(q, dns.TypeA).Action
		b := pipe.Match(q, dns.TypeA).Action
		if a != b {
			t.Errorf("%q：*.dev.local 得到 %v，||dev.local^ 得到 %v，两者应一致", q, a, b)
		}
	}
}

// TestBuildSkipsInvalidRules 验证非法规则不会中断构建（文档 5.4）。
func TestBuildSkipsInvalidRules(t *testing.T) {
	rs, err := Build([]model.Rule{
		{Kind: model.KindBlock, Pattern: "||^", Enabled: true},                    // 非法
		{Kind: "unknown_kind", Pattern: "||x.com^", Enabled: true},                // 未知类型
		{Kind: model.KindRewriteA, Pattern: "||y.com^", Value: "", Enabled: true}, // 缺目标
		{Kind: model.KindBlock, Pattern: "||good.com^", Enabled: true},            // 合法
	}, nil, nil)
	if err != nil {
		t.Fatalf("构建不应返回错误: %v", err)
	}
	if rs.Stats.UserBlock != 1 {
		t.Fatalf("应只保留 1 条合法规则，实际 %d", rs.Stats.UserBlock)
	}
	if d := rs.Match("good.com", dns.TypeA); d.Action != ActionBlock {
		t.Fatalf("合法规则应生效，实际 %v", d.Action)
	}
}

// TestParseContentStats 验证批量解析的统计口径。
func TestParseContentStats(t *testing.T) {
	content := `# 注释行
||ads.example.com^
! 另一个注释

@@||safe.example.com^
0.0.0.0 tracker.net
||^
`
	parsed, skipped, errs := ParseContent(content)
	if len(parsed) != 3 {
		t.Fatalf("期望解析出 3 条规则，实际 %d", len(parsed))
	}
	// 注释 2 条 + 空行 1 条 + 非法 1 条 + 结尾换行产生的空行 1 条 = 5
	if skipped != 5 {
		t.Errorf("期望跳过 5 行，实际 %d", skipped)
	}
	if len(errs) != 1 {
		t.Errorf("期望 1 条错误，实际 %d: %v", len(errs), errs)
	}
}
