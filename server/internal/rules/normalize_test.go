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
		{"|public1.alidns.com|", "public1.alidns.com"},
		{"@@||*.example.com^", "*.example.com"},
		{"||*.example.com^", "*.example.com"},
		{"*.example.com", "*.example.com"},
		{"0.0.0.0 ad.example.com", ""}, // hosts 行不是 NormalizePattern 的输入
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
	yes := []string{"example.com", "*.example.com", "a.b.c.example.com"}
	no := []string{"@@||example.com^", "||example.com^", "|example.com|", "", "*.example.com^"}
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
