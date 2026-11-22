package dns

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// buildFallbackRulesPlain 合并所有原始条目，不做 seenBlock 手动去重——把这个
// 活交给 rules.Build 里 suffixSet.finish()（排序 + 就地去重），它按规范化的
// 域名去重，比 raw 字符串去重更彻底（"EXAMPLE.com" 和 "example.com" 可合并）。
func buildFallbackRulesPlain(snap *model.Snapshot) (*rules.RuleSet, error) {
	if snap.Defaults.FallbackPolicy != model.FallbackBlocklistOnly {
		return rules.Build(rules.Layers{})
	}
	var block []string
	block = append(block, snap.Global.SubBlock...)
	for _, u := range snap.Users {
		if !u.Enabled {
			continue
		}
		block = append(block, u.SubBlock...)
	}
	return rules.Build(rules.Layers{
		SubBlock: block,
		Global:   &model.GlobalSnap{SubAllow: snap.Global.SubAllow},
	})
}

// TestFallbackRulesDedupeWithOrWithoutMap compares the manual seenBlock
// de-duplicate in buildFallbackRules with the plain merge + suffixSet.finish():
// both must produce the same ruleset (statistics + match results), so that the
// map can be retired without a behaviour change.
func TestFallbackRulesDedupeWithOrWithoutMap(t *testing.T) {
	snap := &model.Snapshot{
		Defaults: model.Defaults{FallbackPolicy: model.FallbackBlocklistOnly},
		Global: model.GlobalSnap{
			SubBlock: []string{"global-blocked.example", "global-blocked.example", "global-allowed2.example"},
			SubAllow: []string{"global-allowed.example"},
		},
		Users: []model.UserSnap{
			{
				UserID:   "u1",
				ClientID: "u1cid",
				Enabled:  true,
				SubBlock: []string{"user-blocked.example", "user-blocked.example", "global-blocked.EXAMPLE"},
			},
		},
	}

	withMap, err := buildFallbackRules(snap)
	if err != nil {
		t.Fatalf("构建兜底规则集失败: %v", err)
	}

	withoutMap, err := buildFallbackRulesPlain(snap)
	if err != nil {
		t.Fatalf("构建兜底规则集失败: %v", err)
	}

	if withMap.Stats.SubBlock != withoutMap.Stats.SubBlock {
		t.Errorf("拦截数：期望 %d，实际 %d", withoutMap.Stats.SubBlock, withMap.Stats.SubBlock)
	}
	if withMap.Stats.SubAllow != withoutMap.Stats.SubAllow {
		t.Errorf("放行数：期望 %d，实际 %d", withoutMap.Stats.SubAllow, withMap.Stats.SubAllow)
	}
	for _, d := range []string{"global-blocked.example", "user-blocked.example", "global-allowed2.example", "global-allowed.example", "not-blocked.example"} {
		a := withMap.Match(d, 1)
		b := withoutMap.Match(d, 1)
		if a.Action != b.Action {
			t.Errorf("域名 %s 匹配: 期望 %v，实际 %v", d, b, a)
		}
	}
}
