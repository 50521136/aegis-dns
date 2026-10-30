package rules

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这是这次迁移的核心保证：把 pattern 从 "@@||x^" / "||x^" 收敛成 "x" 之后，
// 匹配结果必须和迁移前完全一致。
//
// 只靠 splitPattern 的单元测试不够 —— 那验证的是单个函数，而真正决定
// 线上行为的是 Build 之后的 trie。所以这里走完整链路：同一组域名，
// 分别用迁移前的写法和迁移后的写法构建 RuleSet，逐个比对 Match 结果。
func TestMigrationPreservesMatching(t *testing.T) {
	// 迁移前的真实写法（线上 v1.1.13 及更早的数据）
	oldPatterns := []struct{ kind, pattern string }{
		{model.KindAllow, "@@||public1.alidns.com^"},
		{model.KindAllow, "||aedns.weixin.qq.com^"},
		{model.KindAllow, "api-ipv4.nrd.nie.163.com"},
		{model.KindAllow, "||sentry.io"},
		{model.KindAllow, "*.video.mi.com"},
		{model.KindBlock, "||ad.example.com^"},
		{model.KindBlock, "plain.example.com"},
		{model.KindBlock, "*.tracker.example.net"},
	}

	// 对应的迁移后写法（0024 迁移的产物）
	newPatterns := []struct{ kind, pattern string }{
		{model.KindAllow, "public1.alidns.com"},
		{model.KindAllow, "aedns.weixin.qq.com"},
		{model.KindAllow, "api-ipv4.nrd.nie.163.com"},
		{model.KindAllow, "sentry.io"},
		{model.KindAllow, "*.video.mi.com"},
		{model.KindBlock, "ad.example.com"},
		{model.KindBlock, "plain.example.com"},
		{model.KindBlock, "*.tracker.example.net"},
	}

	// 覆盖精确命中、子域命中、不命中、通配命中
	queries := []string{
		"public1.alidns.com",
		"sub.public1.alidns.com",
		"aedns.weixin.qq.com",
		"api-ipv4.nrd.nie.163.com",
		"sentry.io",
		"x.video.mi.com",
		"ad.example.com",
		"plain.example.com",
		"a.tracker.example.net",
		"unrelated.example.org",
		"doubleclick.net",
	}

	build := func(ps []struct{ kind, pattern string }) *RuleSet {
		rules := make([]model.Rule, 0, len(ps))
		for i, p := range ps {
			rules = append(rules, model.Rule{
				ID:      string(rune('a' + i)),
				Kind:    p.kind,
				Pattern: p.pattern,
				Enabled: true,
			})
		}
		rs, err := Build(Layers{UserRules: rules})
		if err != nil {
			t.Fatalf("Build 失败: %v", err)
		}
		return rs
	}

	oldRS := build(oldPatterns)
	newRS := build(newPatterns)

	for _, q := range queries {
		o := oldRS.Match(q, 1) // 1 = TypeA
		n := newRS.Match(q, 1)
		if o.Action != n.Action {
			t.Errorf("%s: 迁移前 action=%v, 迁移后 action=%v —— 行为变了", q, o.Action, n.Action)
		}
		if o.Source != n.Source {
			t.Errorf("%s: 迁移前 source=%q, 迁移后 source=%q —— 命中层级变了", q, o.Source, n.Source)
		}
		t.Logf("%-30s action=%-8v source=%s", q, n.Action, n.Source)
	}
}
