package api

import (
	"context"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件锁住「全局订阅跨创建者可见」的契约。
//
// 线上实测故障：管理员把三个用户的重复订阅合并成一条全局订阅后，
// 后台「全局订阅」页**看不到那一条** —— 因为列表按 user_id = 当前管理员
// 过滤，而合并保留项的 user_id 是原租户（lz0423），不是 admin。
//
// 后果比「少显示一行」严重得多：这条订阅对全站在生效，管理员却看不到，
// 也就改不了、停不了、删不了 —— 等于一条看不见的规则。

// TestListAllGlobalSubsIgnoresCreator 全局订阅不按创建者过滤。
func TestListAllGlobalSubsIgnoresCreator(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	admin := seedUser(t, s, "ga_admin")
	tenant := seedUser(t, s, "ga_tenant")

	// 管理员自己建的一条。
	if err := s.store.CreateSub(ctx, &model.Subscription{
		UserID: admin, ListType: model.ListBlock, Name: "管理员建的",
		URL: "https://example.com/by-admin.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}
	// 由普通租户转来的全局订阅（合并/收录的产物）。
	if err := s.store.CreateSub(ctx, &model.Subscription{
		UserID: tenant, ListType: model.ListBlock, Name: "合并上来的",
		URL: "https://example.com/promoted.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}

	// 旧口径（按 uid）只能看到自己那条 —— 这正是 bug。
	own, err := s.store.ListSubs(ctx, admin, true)
	if err != nil {
		t.Fatalf("ListSubs 失败: %v", err)
	}
	if len(own) != 1 {
		t.Fatalf("按创建者查应只有 1 条，实际 %d", len(own))
	}

	// 新口径：两条都要看到。
	all, err := s.store.ListAllGlobalSubs(ctx)
	if err != nil {
		t.Fatalf("ListAllGlobalSubs 失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("全局订阅应有 2 条（含他人转来的），实际 %d", len(all))
	}
	found := map[string]bool{}
	for _, sub := range all {
		found[sub.Name] = true
	}
	if !found["管理员建的"] || !found["合并上来的"] {
		t.Fatalf("两条都该出现，实际 %v", found)
	}
}

// TestListAllGlobalSubsExcludesPrivate 私有订阅不能被当成全局订阅返回。
func TestListAllGlobalSubsExcludesPrivate(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "gp_u1")

	makeSub(t, s, u1, "https://example.com/private.txt", model.ListBlock)
	if err := s.store.CreateSub(ctx, &model.Subscription{
		UserID: u1, ListType: model.ListBlock, Name: "全局的",
		URL: "https://example.com/global.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}

	all, err := s.store.ListAllGlobalSubs(ctx)
	if err != nil {
		t.Fatalf("ListAllGlobalSubs 失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("只该返回 1 条全局订阅，实际 %d", len(all))
	}
	if all[0].Name != "全局的" {
		t.Fatalf("返回的应是全局订阅，实际 %q", all[0].Name)
	}
}

// TestUpdateAndDeleteGlobalSubByID 管理端能改 / 能删别人转来的全局订阅。
//
// 用错函数（UpdateSub / DeleteSub）时会因为 `WHERE user_id = ?` 不匹配
// 而返回 ErrNotFound —— 管理员在界面上只会看到「订阅不存在」，
// 而它明明就在列表里。
func TestUpdateAndDeleteGlobalSubByID(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	tenant := seedUser(t, s, "gd_tenant")

	sub := &model.Subscription{
		UserID: tenant, ListType: model.ListBlock, Name: "原名字",
		URL: "https://example.com/promote-me.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}
	if err := s.store.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}

	// 管理员（不是创建者）改名。
	sub.Name = "管理员改过的名字"
	sub.Enabled = false
	if err := s.store.UpdateSubByID(ctx, sub); err != nil {
		t.Fatalf("按 id 更新全局订阅失败: %v", err)
	}
	got, err := s.store.GetSubByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("读订阅失败: %v", err)
	}
	if got.Name != "管理员改过的名字" {
		t.Fatalf("名称应已更新，实际 %q", got.Name)
	}
	if got.Enabled {
		t.Fatal("停用应已生效")
	}

	// 旧的按归属更新会失败（这正是要避免的静默失败）。
	sub.Name = "不该生效"
	sub.Enabled = true
	sub.UserID = "usr_not_the_owner"
	if err := s.store.UpdateSub(ctx, sub); err == nil {
		t.Fatal("按归属更新非本人订阅应当失败（这是 UpdateSubByID 存在的理由）")
	}

	// 管理员删除。
	if err := s.store.DeleteSubByID(ctx, sub.ID); err != nil {
		t.Fatalf("按 id 删除全局订阅失败: %v", err)
	}
	if got, _ := s.store.GetSubByID(ctx, sub.ID); got != nil {
		t.Fatal("删除后不该还能读到")
	}
}

// TestDeleteSubByIDRemovesEntries 删全局订阅要连条目一起删。
func TestDeleteSubByIDRemovesEntries(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "ge_u1")

	sub := &model.Subscription{
		UserID: u1, ListType: model.ListBlock, Name: "带条目的",
		URL: "https://example.com/with-entries.txt", Format: "auto", Enabled: true, IsGlobal: true,
	}
	if err := s.store.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}
	if err := s.store.ReplaceSubEntries(ctx, sub.ID, "block", []string{"a.com", "b.com"}); err != nil {
		t.Fatalf("写条目失败: %v", err)
	}

	if err := s.store.DeleteSubByID(ctx, sub.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n, _ := s.store.CountSubEntries(ctx, sub.ID); n != 0 {
		t.Fatalf("条目应一并删除，实际还剩 %d 条", n)
	}
}
