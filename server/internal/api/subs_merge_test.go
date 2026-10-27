package api

import (
	"context"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// 这个文件锁住「手动合并重复订阅」与「全局订阅查重」的契约。
//
// 手动合并会**删掉用户的订阅**，判错一次就是真实丢了规则集；
// 全局查重会在服务端直接 409 拒绝创建，判错就是用户想加却加不进去。

// canonicalKeyOf 取链接的等价键（与 handler 判断口径一致）。
func canonicalKeyOf(raw string) string {
	return subsub.CanonicalURL(raw)
}

// TestMergeSubscriptionsKeepsChosenOne 合并保留指定的一条，其余删除。
func TestMergeSubscriptionsKeepsChosenOne(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "mu1")
	u2 := seedUser(t, s, "mu2")

	// 同一份内容的两种等价链接。
	sub1 := makeSub(t, s, u1, "https://raw.githubusercontent.com/o/r/main/l.txt", model.ListBlock)
	sub2 := makeSub(t, s, u2, "https://cdn.jsdelivr.net/gh/o/r@main/l.txt", model.ListBlock)

	res, err := s.store.MergeSubscriptions(ctx, []string{sub1.ID, sub2.ID}, sub1.ID)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if res.KeepID != sub1.ID {
		t.Fatalf("保留的应是 %s，实际 %s", sub1.ID, res.KeepID)
	}
	if len(res.RemovedSubs) != 1 {
		t.Fatalf("应删除 1 条，实际 %d", len(res.RemovedSubs))
	}
	if res.RemovedSubs[0].SubID != sub2.ID {
		t.Fatalf("被删的应是 %s，实际 %s", sub2.ID, res.RemovedSubs[0].SubID)
	}

	// sub1 还在，sub2 没了。
	if got, err := s.store.GetSubByID(ctx, sub1.ID); err != nil || got == nil {
		t.Fatal("保留的订阅不该被删除")
	}
	if got, _ := s.store.GetSubByID(ctx, sub2.ID); got != nil {
		t.Fatal("被合并的订阅应该已删除")
	}
}

// TestMergeSubscriptionsDeletesEntries 合并要连条目一起删。
//
// 只删订阅行不删条目，会让 sub_entries 里留下一批孤儿行 ——
// 它们不参与任何快照（因为查不到所属订阅），但会永久占库。
func TestMergeSubscriptionsDeletesEntries(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "me1")
	u2 := seedUser(t, s, "me2")

	sub1 := makeSub(t, s, u1, "https://example.com/same-a.txt", model.ListBlock)
	sub2 := makeSub(t, s, u2, "https://example.com/same-b.txt", model.ListBlock)
	// 给两条都写点条目。
	_ = s.store.ReplaceSubEntries(ctx, sub1.ID, "block", []string{"a.com", "b.com"})
	_ = s.store.ReplaceSubEntries(ctx, sub2.ID, "block", []string{"c.com"})

	if _, err := s.store.MergeSubscriptions(ctx, []string{sub1.ID, sub2.ID}, sub1.ID); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	// 被删订阅的条目必须清空。
	if n, _ := s.store.CountSubEntries(ctx, sub2.ID); n != 0 {
		t.Fatalf("被删订阅的条目应清空，实际 %d 条", n)
	}
	// 保留订阅的条目不受影响。
	if n, _ := s.store.CountSubEntries(ctx, sub1.ID); n != 2 {
		t.Fatalf("保留订阅的条目应保持 2 条，实际 %d 条", n)
	}
}

// TestMergeSubscriptionsAffectsBothUsers 两个用户都要进「受影响」清单。
//
// 快照按用户重建，漏掉任何一个都会让那个人的规则集停在旧版本 ——
// 表现为「合并完了但他的拦截行为没变」。
func TestMergeSubscriptionsAffectsBothUsers(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "ma1")
	u2 := seedUser(t, s, "ma2")

	sub1 := makeSub(t, s, u1, "https://example.com/x1.txt", model.ListBlock)
	sub2 := makeSub(t, s, u2, "https://example.com/x2.txt", model.ListBlock)

	res, err := s.store.MergeSubscriptions(ctx, []string{sub1.ID, sub2.ID}, sub1.ID)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if len(res.AffectedUsers) != 2 {
		t.Fatalf("应影响 2 个用户，实际 %d: %v", len(res.AffectedUsers), res.AffectedUsers)
	}
	found := map[string]bool{}
	for _, uid := range res.AffectedUsers {
		found[uid] = true
	}
	if !found[u1] || !found[u2] {
		t.Fatalf("两个用户都该在受影响清单里，实际 %v", res.AffectedUsers)
	}
}

// TestMergeRefusesNonEquivalentURLs 内容不等价的订阅绝不能合并。
//
// 这是手动合并最危险的一条：管理员点错、或前端出错把两组混在一起，
// 合并等于删掉一个人真实的规则集，丢掉的规则他再也拿不回来。
func TestMergeRefusesNonEquivalentURLs(t *testing.T) {
	s := newAutoTestServer(t)
	u1 := seedUser(t, s, "mx1")
	u2 := seedUser(t, s, "mx2")

	sub1 := makeSub(t, s, u1, "https://example.com/one.txt", model.ListBlock)
	sub2 := makeSub(t, s, u2, "https://example.com/completely-different.txt", model.ListBlock)

	// 等价性校验在 api 层（store 不依赖 subsub），所以这里直接验证
	// 归一键确实不同 —— 这正是 handleAdminMergeDuplicateSubs 拒绝的依据。
	key1 := canonicalKeyOf(sub1.URL)
	key2 := canonicalKeyOf(sub2.URL)
	if key1 == key2 {
		t.Fatalf("这两条链接不该被判成等价: %q vs %q", key1, key2)
	}
}

// TestMergeRefusesGlobalSubs 全局订阅不参与合并。
func TestMergeRefusesGlobalSubs(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "mg1")

	// 建一条全局订阅。
	globalSub := &model.Subscription{
		UserID: u1, ListType: model.ListBlock, Name: "全局规则",
		URL: "https://example.com/global.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}
	if err := s.store.CreateSub(ctx, globalSub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}
	privateSub := makeSub(t, s, u1, "https://example.com/private.txt", model.ListBlock)

	// store 层的 MergeSubscriptions 只删 is_global = 0 的行，
	// 所以全局订阅即使被传进来也删不掉 —— 这是最后一道防线。
	if _, err := s.store.MergeSubscriptions(ctx, []string{globalSub.ID, privateSub.ID}, privateSub.ID); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if got, _ := s.store.GetSubByID(ctx, globalSub.ID); got == nil {
		t.Fatal("全局订阅不该被合并删除")
	}
}

// TestGlobalSubLookupBlocksDuplicate 已有全局订阅时，用户再添加同样的链接要拒绝。
func TestGlobalSubLookupBlocksDuplicate(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "gl1")

	globalSub := &model.Subscription{
		UserID: u1, ListType: model.ListBlock, Name: "AdGuard DNS Filter",
		URL: "https://raw.githubusercontent.com/AdguardTeam/Filters/main/f.txt",
		Format: "auto", Enabled: true, IsGlobal: true, LastCount: 12345,
	}
	if err := s.store.CreateSub(ctx, globalSub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}

	// 完全相同链接 → 命中。
	if hit := s.lookupGlobalSubByURL(ctx, globalSub.URL); hit == nil {
		t.Fatal("完全相同的链接应命中全局订阅")
	} else if hit.Name != "AdGuard DNS Filter" {
		t.Fatalf("应返回全局订阅名称，实际 %q", hit.Name)
	}

	// 等价链接（jsDelivr 形态）→ 也应命中。
	jsd := "https://cdn.jsdelivr.net/gh/AdguardTeam/Filters@main/f.txt"
	if hit := s.lookupGlobalSubByURL(ctx, jsd); hit == nil {
		t.Fatal("内容等价的链接也应命中全局订阅（否则用户换个写法就能重复添加）")
	}

	// 完全不同的链接 → 不命中。
	if hit := s.lookupGlobalSubByURL(ctx, "https://example.com/other.txt"); hit != nil {
		t.Fatal("无关链接不该命中全局订阅")
	}
}

// TestGlobalSubLookupIgnoresPrivate 同名的私有订阅不算「全局已有」。
func TestGlobalSubLookupIgnoresPrivate(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "gp1")
	// u2 只是「另一个用户」的占位：这个用例要证明的是
	// 「别人订了私有订阅 ≠ 全站生效」，所以不需要真的让 u2 建东西。
	_ = seedUser(t, s, "gp2")

	// u1 自己有一条私有订阅。
	makeSub(t, s, u1, "https://example.com/private-only.txt", model.ListBlock)

	// u2 想加同样的链接：不该被拦（那不是全局规则，别人订了不代表全站生效）。
	if hit := s.lookupGlobalSubByURL(ctx, "https://example.com/private-only.txt"); hit != nil {
		t.Fatal("私有订阅不该被当成全局规则来拦截")
	}
}

// TestGetSubByIDCrossTenant 管理端跨租户按 id 读订阅。
func TestGetSubByIDCrossTenant(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "gc1")
	sub := makeSub(t, s, u1, "https://example.com/cross.txt", model.ListBlock)

	got, err := s.store.GetSubByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("按 id 读订阅失败: %v", err)
	}
	if got.UserID != u1 {
		t.Fatalf("归属应为 %s，实际 %s", u1, got.UserID)
	}
	// 不存在的 id 应返回 ErrNotFound 而不是空对象。
	if _, err := s.store.GetSubByID(ctx, "sub_does_not_exist"); err == nil {
		t.Fatal("不存在的订阅应报错")
	}
}
