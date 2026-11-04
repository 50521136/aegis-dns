package rules

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 核心约定：kind 决定语义，pattern 只存域名。三种历史写法必须收敛到同一个值。
func TestNormalizePatternConverges(t *testing.T) {
	cases := []struct{ in, want string }{
		{"@@||public1.alidns.com^", "public1.alidns.com"},
		{"||public1.alidns.com^", "public1.alidns.com"},
		{"public1.alidns.com", "public1.alidns.com"},
		// |domain| 是有语义的（只匹配自身），不能像前缀那样被剥掉。
		{"|public1.alidns.com|", "|public1.alidns.com|"},
		{"||public1.alidns.com|", "public1.alidns.com"},
		{"@@||*.example.com^", "*.example.com"},
		{"||*.example.com^", "*.example.com"},
		{"*.example.com", "*.example.com"},
		// hosts 行现在会被归一成域名：用户从 hosts 文件抄一行填进规则框是最
		// 常见的动作，旧行为是报「含非法字符 " "」，看起来像格式不支持。
		// 只接受黑洞 IP，真实 IP 仍要报错（那是改写语义）——见下一条。
		{"0.0.0.0 ad.example.com", "ad.example.com"},
		{"127.0.0.1 ad.example.com", "ad.example.com"},
		{"142.250.157.188 mtalk.google.com", ""}, // 真实 IP 不是黑洞地址，必须报错
	}
	for _, c := range cases {
		got, err := NormalizePattern(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("NormalizePattern(%q) 期望报错，却得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePattern(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePattern(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// kind=allow 配 @@ 是自洽的，应当被接受并剥掉前缀。
func TestNormalizeRuleAllowAcceptsAtAt(t *testing.T) {
	r := &model.Rule{Kind: model.KindAllow, Pattern: "@@||safe.example.com^"}
	if err := NormalizeRule(r); err != nil {
		t.Fatalf("allow + @@ 不应报错: %v", err)
	}
	if r.Pattern != "safe.example.com" {
		t.Errorf("pattern = %q, 期望 safe.example.com", r.Pattern)
	}
}

// 最关键的一条：kind=block 配 @@ 前缀必须拒绝。
// 这种输入过去能入库，匹配时 @@ 被剥掉、kind 生效，实际是「拦截」——
// 和写规则的人想表达的意思完全相反，而且没有任何提示。
func TestNormalizeRuleBlockRejectsAtAt(t *testing.T) {
	r := &model.Rule{Kind: model.KindBlock, Pattern: "@@||public1.alidns.com^"}
	err := NormalizeRule(r)
	if err == nil {
		t.Fatal("kind=block 配 @@ 前缀必须报错，否则会静默按「拦截」生效")
	}
	t.Logf("正确拒绝: %v", err)
}

// allow 配不带 @@ 的写法（历史数据里 63 条这种）应当照常接受。
func TestNormalizeRuleAllowWithoutAtAt(t *testing.T) {
	for _, in := range []string{"||x.example.com^", "x.example.com"} {
		r := &model.Rule{Kind: model.KindAllow, Pattern: in}
		if err := NormalizeRule(r); err != nil {
			t.Errorf("allow + %q 不应报错: %v", in, err)
			continue
		}
		if r.Pattern != "x.example.com" {
			t.Errorf("allow + %q -> %q, 期望 x.example.com", in, r.Pattern)
		}
	}
}

// 规范化必须是幂等的：反复调用结果不变，否则迁移脚本会越跑越歪。
func TestNormalizeRuleIdempotent(t *testing.T) {
	r := &model.Rule{Kind: model.KindAllow, Pattern: "@@||a.example.com^"}
	if err := NormalizeRule(r); err != nil {
		t.Fatal(err)
	}
	first := r.Pattern
	for i := 0; i < 3; i++ {
		if err := NormalizeRule(r); err != nil {
			t.Fatalf("第 %d 次规范化报错: %v", i+2, err)
		}
		if r.Pattern != first {
			t.Fatalf("非幂等: %q -> %q", first, r.Pattern)
		}
	}
}

// IsNormalized 用于迁移后断言存量已清理。
func TestIsNormalized(t *testing.T) {
	// |domain| 现在是规范形态（精确匹配），从 no 挪到 yes。
	yes := []string{"example.com", "*.example.com", "a.b.c.example.com", "|example.com|"}
	no := []string{"@@||example.com^", "||example.com^", "", "*.example.com^", "|example.com"}
	for _, p := range yes {
		if !IsNormalized(p) {
			t.Errorf("IsNormalized(%q) 应为 true", p)
		}
	}
	for _, p := range no {
		if IsNormalized(p) {
			t.Errorf("IsNormalized(%q) 应为 false", p)
		}
	}
}

// 规范化后的 pattern 必须能被匹配层正确还原成域名，
// 否则「存进去看着对、匹配不上」是最难查的一类 bug。
func TestNormalizedPatternStillMatches(t *testing.T) {
	for _, in := range []string{"@@||pub.example.com^", "||pub.example.com^", "pub.example.com"} {
		r := &model.Rule{Kind: model.KindAllow, Pattern: in}
		if err := NormalizeRule(r); err != nil {
			t.Fatalf("%q 规范化失败: %v", in, err)
		}
		dom, wildcard, err := splitPattern(r.Pattern)
		if err != nil {
			t.Fatalf("%q 规范化后 splitPattern 报错: %v", r.Pattern, err)
		}
		if dom != "pub.example.com" || wildcard {
			t.Errorf("%q -> domain=%q wildcard=%t, 期望 pub.example.com/false", in, dom, wildcard)
		}
	}
}

// TestExactOnlyPattern 钉住「只匹配裸域名、不匹配子域」的语义。
//
// 为什么需要这个能力：DNS 层的规则默认是「自身 + 全部子域」，这在放行
// 时会把一整棵子树都打开。用户想只放行 apex 域、把子域留给其他规则时，
// 需要一个能表达「精确匹配」的写法 —— |domain|。
//
// 三个断言缺一不可：
//  1. 裸域名仍然覆盖子域（默认行为不能被改坏）
//  2. |domain| 只匹配自身
//  3. |domain| 存进库再读出来仍是 |domain|（否则作用域被悄悄放大）
func TestExactOnlyPattern(t *testing.T) {
	const apex = "githubusercontent.com"
	subs := []string{"github.githubusercontent.com", "raw.githubusercontent.com"}

	// 1. 裸域名：自身 + 子域都放行
	rs, err := Build(Layers{UserRules: []model.Rule{
		{Kind: model.KindAllow, Pattern: apex, Enabled: true},
	}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if got := rs.Match(apex, 1).Action; got != ActionAllow {
		t.Errorf("裸域名自身应放行，实际 %s", got)
	}
	for _, s := range subs {
		if got := rs.Match(s, 1).Action; got != ActionAllow {
			t.Errorf("裸域名应覆盖子域 %s，实际 %s", s, got)
		}
	}

	// 2. |domain|：只有自身
	rs2, err := Build(Layers{UserRules: []model.Rule{
		{Kind: model.KindAllow, Pattern: "|" + apex + "|", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if got := rs2.Match(apex, 1).Action; got != ActionAllow {
		t.Errorf("|%s| 应放行自身，实际 %s", apex, got)
	}
	for _, s := range subs {
		if got := rs2.Match(s, 1).Action; got == ActionAllow {
			t.Errorf("|%s| 不应放行子域 %s，实际 %s", apex, s, got)
		}
	}

	// 3. 规范化往返
	norm, err := NormalizePattern("|" + apex + "|")
	if err != nil {
		t.Fatalf("规范化失败: %v", err)
	}
	if norm != "|"+apex+"|" {
		t.Errorf("规范化 = %q，期望保持 |%s|（丢掉竖线会把作用域放大到全部子域）", norm, apex)
	}
	if !IsNormalized(norm) {
		t.Errorf("IsNormalized(%q) 应为 true", norm)
	}
}

// TestExactOnlyDoesNotShrinkOtherRules 同节点上先有普通规则、再来精确规则时，
// 普通规则的作用域不能被悄悄缩小。
func TestExactOnlyDoesNotShrinkOtherRules(t *testing.T) {
	rs, err := Build(Layers{UserRules: []model.Rule{
		{Kind: model.KindAllow, Pattern: "example.com", Enabled: true},
		{Kind: model.KindBlock, Pattern: "|example.com|", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	// allow 层先命中，子域仍应放行 —— block 是精确的，管不到子域。
	if got := rs.Match("a.example.com", 1).Action; got != ActionAllow {
		t.Errorf("子域应被 allow 放行，实际 %s", got)
	}
}
