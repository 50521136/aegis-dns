package rules

import (
	"fmt"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// BenchmarkMatchPerQuery 模拟 DNS 查询热路径：每笔查询都调用一次
// RuleSet.Match，测试（657k 个人 block + 536k 个人 allow + 共享全局层）
// 下的单笔开销。
func BenchmarkMatchPerQuery(b *testing.B) {
	// 657k 条个人拦截 + 536k 条个人放行 = 线上几个用户的典型规模。
	const (
		userBlock = 657_827
		userAllow = 536_098
	)
	userBlockDs := make([]string, userBlock)
	for i := range userBlockDs {
		userBlockDs[i] = fmt.Sprintf("trk%06d.example", i)
	}
	userAllowDs := make([]string, userAllow)
	for i := range userAllowDs {
		userAllowDs[i] = fmt.Sprintf("ok%06d.example", i)
	}
	global := &model.GlobalSnap{
		SubBlock: []string{"ads.example", "trk1.example", "px.example"},
		SubAllow: []string{"ok.example"},
	}

	userRules := make([]model.Rule, 0, 30)
	for i := 0; i < 3; i++ {
		userRules = append(userRules, model.Rule{Kind: model.KindAllow, Pattern: "own" + fmt.Sprint(i) + ".example", Enabled: true})
		userRules = append(userRules, model.Rule{Kind: model.KindBlock, Pattern: "own" + fmt.Sprint(i) + ".bad.example", Enabled: true})
		userRules = append(userRules, model.Rule{Kind: model.KindRewriteA, Pattern: "own" + fmt.Sprint(i) + ".redir.example", Value: "tgt.example"})
	}
	rs, err := BuildWithGlobal(BuildGlobal(global), Layers{
		UserRules: userRules,
		SubAllow:  userAllowDs,
		SubBlock:  userBlockDs,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		d := rs.Match("www.example.com", dns.TypeA)
		if d.Action != ActionNone {
			b.Fatalf("expected none, got %v", d.Action)
		}
	}
}

// BenchmarkMatchPerQueryHit 查询命中的情况（走到全局/个人订阅拦截层）。
func BenchmarkMatchPerQueryHit(b *testing.B) {
	const (
		userBlock = 657_827
		userAllow = 536_098
	)
	userBlockDs := make([]string, userBlock)
	for i := range userBlockDs {
		userBlockDs[i] = fmt.Sprintf("trk%06d.example", i)
	}
	userAllowDs := make([]string, userAllow)
	for i := range userAllowDs {
		userAllowDs[i] = fmt.Sprintf("ok%06d.example", i)
	}
	global := &model.GlobalSnap{
		SubBlock: []string{"ads.example", "trk1.example", "px.example"},
		SubAllow: []string{"ok.example"},
	}
	rs, err := BuildWithGlobal(BuildGlobal(global), Layers{
		SubAllow: userAllowDs,
		SubBlock: userBlockDs,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if d := rs.Match("ads.example", dns.TypeA); d.Action != ActionBlock {
			b.Fatalf("expected block, got %v", d.Action)
		}
	}
}
