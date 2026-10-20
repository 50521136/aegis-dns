package dns

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// TestFallbackRulesIncludeGlobalLayer 验证兜底规则集包含全局层。
//
// 背景：把公共规则列表统一放在全局层是最常见的部署方式。早先的
// buildFallbackRules 只汇总各用户自己的 SubBlock，完全忽略全局层 ——
// 那种部署下 fallback_policy=blocklist_only 会「一条都不拦」，
// 而管理员看到的是「策略设了却没生效」，极难定位。
func TestFallbackRulesIncludeGlobalLayer(t *testing.T) {
	snap := &model.Snapshot{
		Defaults: model.Defaults{FallbackPolicy: model.FallbackBlocklistOnly},
		Global: model.GlobalSnap{
			SubBlock: []string{"global-blocked.example"},
			SubAllow: []string{"global-allowed.example"},
		},
		Users: []model.UserSnap{
			{
				UserID:   "u1",
				ClientID: "u1cid",
				Enabled:  true,
				SubBlock: []string{"user-blocked.example"},
			},
		},
	}

	rs, err := buildFallbackRules(snap)
	if err != nil {
		t.Fatalf("构建兜底规则集失败: %v", err)
	}
	if rs.Stats.SubBlock == 0 {
		t.Fatal("兜底规则集不该为空：全局层的拦截项必须算进来")
	}

	// 直连命中检查：全局层与被启用用户的拦截项都应生效。
	for _, d := range []string{"global-blocked.example", "user-blocked.example"} {
		if !blockedBy(rs, d) {
			t.Errorf("%q 应被兜底规则集拦截", d)
		}
	}
	// 全局白名单要能覆盖拦截。
	if blockedBy(rs, "global-allowed.example") {
		t.Error("全局白名单里的域名不该被拦截")
	}
}

// TestFallbackRulesPassthroughIsEmpty 验证 passthrough 策略下兜底规则集为空。
func TestFallbackRulesPassthroughIsEmpty(t *testing.T) {
	snap := &model.Snapshot{
		Defaults: model.Defaults{FallbackPolicy: model.FallbackPassthrough},
		Global:   model.GlobalSnap{SubBlock: []string{"global-blocked.example"}},
	}
	rs, err := buildFallbackRules(snap)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if blockedBy(rs, "global-blocked.example") {
		t.Error("passthrough 策略下不该有任何拦截")
	}
}

// blockedBy 判断某个域名在规则集里是否被判为拦截。
func blockedBy(rs *rules.RuleSet, domain string) bool {
	return rs.Match(domain, 1).Action == rules.ActionBlock
}
