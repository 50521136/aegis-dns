package dns

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// 这条测试守着「小服务器稳定运行」的核心承诺：
// 全局层必须与租户数无关地只占一份。
//
// 之前每个用户各建一份全局层，3 个用户 × 62 万条 = 3 份，
// 用户数一多内存就线性增长，把 1.7G 的机器打爆。
func TestGlobalLayerSharedAcrossUsers(t *testing.T) {
	const domains = 400000
	global := model.GlobalSnap{}
	global.SubBlock = make([]string, 0, domains)
	for i := 0; i < domains; i++ {
		global.SubBlock = append(global.SubBlock, fmt.Sprintf("tracker%d.example.global", i))
	}

	snap := &model.Snapshot{
		Version:  1,
		Schema:   model.SnapshotSchema,
		Defaults: model.Defaults{BlockingMode: model.BlockingNullIP, Upstreams: []string{"223.5.5.5"}, MaxInflight: 300, QueryTimeout: 5000},
		Global:   global,
	}
	// 10 个租户，全部使用全局规则
	for i := 0; i < 10; i++ {
		snap.Users = append(snap.Users, model.UserSnap{
			UserID:         fmt.Sprintf("usr_%d", i),
			ClientID:       fmt.Sprintf("cid%06d", i),
			Enabled:        true,
			UseGlobalRules: true,
		})
	}

	reg := NewRegistry(resolver.NewCache())
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)

	if err := reg.Build(snap); err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	usedMB := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / (1 << 20)
	t.Logf("10 个租户 + 40 万条全局订阅，占用 %.1f MB（仅供观察，不做断言）", usedMB)
	// 内存差值受 GC 时机影响很大，不适合做断言。
	// 「是否共享」由 rules 包的 TestGlobalLayerIsSharedByPointer 用指针同一性精确验证。

	// 功能校验：任一租户都应命中全局订阅里的域名
	s := reg.Load()
	for i := 0; i < 10; i++ {
		u := s.LookupClientID(fmt.Sprintf("cid%06d", i))
		if u == nil {
			t.Fatalf("找不到用户 usr_%d", i)
		}
		if d := u.Rules.Load().Match("x.tracker7.example.global", 1); d.Action != rules.ActionBlock {
			t.Errorf("usr_%d 未命中全局订阅，action=%q", i, d.Action)
		}
	}
}

// 关闭全局规则的用户必须拿不到全局层。
func TestUseGlobalRulesOffStillIsolated(t *testing.T) {
	snap := &model.Snapshot{
		Version:  1,
		Schema:   model.SnapshotSchema,
		Defaults: model.Defaults{BlockingMode: model.BlockingNullIP, Upstreams: []string{"223.5.5.5"}, MaxInflight: 300, QueryTimeout: 5000},
		Global: model.GlobalSnap{
			SubBlock: []string{"blocked.example"},
		},
		Users: []model.UserSnap{
			{UserID: "usr_on", ClientID: "cid_on", Enabled: true, UseGlobalRules: true},
			{UserID: "usr_off", ClientID: "cid_off", Enabled: true, UseGlobalRules: false},
		},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		t.Fatal(err)
	}
	s := reg.Load()
	if d := s.LookupClientID("cid_on").Rules.Load().Match("blocked.example", 1); d.Action != rules.ActionBlock {
		t.Error("开启全局规则的用户应命中")
	}
	if d := s.LookupClientID("cid_off").Rules.Load().Match("blocked.example", 1); d.Action == rules.ActionBlock {
		t.Error("关闭全局规则的用户不该被全局层命中")
	}
}
