package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件锁住「全局规则的作用域只由 is_global 决定，不由 user_id 决定」这条契约。
//
// 起因是一个真实的线上缺陷：AI 自动放行（add_global_allow）写规则时把 user_id
// 记成了**提反馈的那个普通用户**，而列表/读取/删除全都按「当前登录人 + is_global」
// 过滤 —— 于是管理员在全局规则页看不到这些规则，也删不掉，但它们对全站生效。
// 这和全局订阅那次踩的是同一个坑。

func newRulesTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedRuleUser(t *testing.T, st *Store, name string, role string) string {
	t.Helper()
	u, err := st.CreateUser(context.Background(), name, name+"@test.local", "x", role)
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	return u.ID
}

func addRule(t *testing.T, st *Store, uid, kind, pattern string, global bool) *model.Rule {
	t.Helper()
	r := &model.Rule{UserID: uid, Kind: kind, Pattern: pattern, Enabled: true, IsGlobal: global}
	if err := st.CreateRule(context.Background(), r); err != nil {
		t.Fatalf("建规则失败: %v", err)
	}
	return r
}

// TestGlobalRulesVisibleRegardlessOfOwner 是核心回归：别人（含 AI 用普通用户身份）
// 建的全局规则，管理员必须能列出来。
func TestGlobalRulesVisibleRegardlessOfOwner(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	admin := seedRuleUser(t, st, "adm", model.RoleAdmin)
	tenant := seedRuleUser(t, st, "tnt", model.RoleUser)

	// 模拟 AI 自动放行：is_global=1，但 user_id 是提反馈的普通用户。
	addRule(t, st, tenant, "allow", "ai-added.example.com", true)
	// 管理员自己加的一条，作为对照。
	addRule(t, st, admin, "allow", "admin-added.example.com", true)

	list, total, err := st.ListRules(ctx, admin, true, "", "", nil, 50, 0)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("全局规则应列出 2 条（含他人/AI 添加的），实际 total=%d len=%d", total, len(list))
	}
	found := false
	for _, r := range list {
		if r.Pattern == "ai-added.example.com" {
			found = true
			if r.UserID != tenant {
				t.Fatalf("user_id 应保留为来源用户 %q，实际 %q", tenant, r.UserID)
			}
		}
	}
	if !found {
		t.Fatal("AI 添加的全局规则没有出现在列表里")
	}
}

// TestGlobalRuleDeletableRegardlessOfOwner 管理员必须能删掉别人加的全局规则。
func TestGlobalRuleDeletableRegardlessOfOwner(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	admin := seedRuleUser(t, st, "adm2", model.RoleAdmin)
	tenant := seedRuleUser(t, st, "tnt2", model.RoleUser)

	r := addRule(t, st, tenant, "allow", "ai-added2.example.com", true)

	got, err := st.GetRule(ctx, admin, r.ID, true)
	if err != nil {
		t.Fatalf("管理员按 id 取全局规则失败: %v", err)
	}
	if got.Pattern != r.Pattern {
		t.Fatalf("取到的规则不对: %q", got.Pattern)
	}
	if err := st.DeleteRule(ctx, admin, r.ID, true); err != nil {
		t.Fatalf("管理员删全局规则失败: %v", err)
	}
	if _, err := st.GetRule(ctx, admin, r.ID, true); err == nil {
		t.Fatal("删除后仍能取到，说明没真删")
	}
}

// TestGlobalRuleUpdateRegardlessOfOwner 改（比如停用）也必须能改。
func TestGlobalRuleUpdateRegardlessOfOwner(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	admin := seedRuleUser(t, st, "adm3", model.RoleAdmin)
	tenant := seedRuleUser(t, st, "tnt3", model.RoleUser)

	r := addRule(t, st, tenant, "allow", "ai-added3.example.com", true)
	got, err := st.GetRule(ctx, admin, r.ID, true)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	got.Enabled = false
	if err := st.UpdateRule(ctx, got); err != nil {
		t.Fatalf("管理员停用全局规则失败: %v", err)
	}
	back, err := st.GetRule(ctx, admin, r.ID, true)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if back.Enabled {
		t.Fatal("停用没有生效")
	}
}

// TestPersonalRulesStillOwnerScoped 反向断言：个人层必须严格按归属隔离，
// 全局层放宽不能把个人层一起放开。
func TestPersonalRulesStillOwnerScoped(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	a := seedRuleUser(t, st, "pa", model.RoleUser)
	b := seedRuleUser(t, st, "pb", model.RoleUser)

	r := addRule(t, st, a, "allow", "personal-a.example.com", false)

	// b 看个人列表看不到 a 的。
	list, total, err := st.ListRules(ctx, b, false, "", "", nil, 50, 0)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("个人规则不应跨用户可见，实际 total=%d len=%d", total, len(list))
	}

	// b 按 id 也取不到（作用域不符 → 视为不存在）。
	if _, err := st.GetRule(ctx, b, r.ID, false); err == nil {
		t.Fatal("他人个人规则不应可读")
	}
	// b 删不掉。
	if err := st.DeleteRule(ctx, b, r.ID, false); err == nil {
		t.Fatal("他人个人规则不应可删")
	}
	// a 自己仍然可以。
	if _, err := st.GetRule(ctx, a, r.ID, false); err != nil {
		t.Fatalf("本人应能取到自己的规则: %v", err)
	}
}

// TestPersonalRouteCannotTouchGlobalRule 反向断言：用个人层的作用域去操作
// 一条全局规则必须失败，否则普通用户能借 /rules/{id} 改管理员的全局规则。
func TestPersonalRouteCannotTouchGlobalRule(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	tenant := seedRuleUser(t, st, "tnt4", model.RoleUser)

	r := addRule(t, st, tenant, "allow", "global-x.example.com", true)

	if _, err := st.GetRule(ctx, tenant, r.ID, false); err == nil {
		t.Fatal("个人作用域不应能读到全局规则")
	}
	if err := st.DeleteRule(ctx, tenant, r.ID, false); err == nil {
		t.Fatal("个人作用域不应能删全局规则")
	}
	// 规则必须还在。
	if _, err := st.GetRule(ctx, tenant, r.ID, true); err != nil {
		t.Fatalf("全局规则被误删: %v", err)
	}
}

// TestGlobalCountIsSiteWide 全局条数按全站算，与「谁在问」无关。
func TestGlobalCountIsSiteWide(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	admin := seedRuleUser(t, st, "adm5", model.RoleAdmin)
	tenant := seedRuleUser(t, st, "tnt5", model.RoleUser)

	addRule(t, st, tenant, "allow", "g1.example.com", true)
	addRule(t, st, admin, "block", "g2.example.com", true)
	addRule(t, st, tenant, "allow", "p1.example.com", false)

	na, err := st.CountRules(ctx, admin, true)
	if err != nil {
		t.Fatalf("CountRules(global): %v", err)
	}
	nt, err := st.CountRules(ctx, tenant, true)
	if err != nil {
		t.Fatalf("CountRules(global): %v", err)
	}
	if na != 2 || nt != 2 {
		t.Fatalf("全局条数应按全站算（2 条），实际 admin=%d tenant=%d", na, nt)
	}
	// 个人层仍按人算。
	if n, err := st.CountRules(ctx, admin, false); err != nil || n != 0 {
		t.Fatalf("admin 个人规则应为 0，实际 %d err=%v", n, err)
	}
	if n, err := st.CountRules(ctx, tenant, false); err != nil || n != 1 {
		t.Fatalf("tenant 个人规则应为 1，实际 %d err=%v", n, err)
	}
}

// TestBulkDeleteRulesScope 批量删除同样要区分作用域。
func TestBulkDeleteRulesScope(t *testing.T) {
	st := newRulesTestStore(t)
	ctx := context.Background()
	admin := seedRuleUser(t, st, "adm6", model.RoleAdmin)
	tenant := seedRuleUser(t, st, "tnt6", model.RoleUser)

	g1 := addRule(t, st, tenant, "allow", "bg1.example.com", true)
	g2 := addRule(t, st, admin, "allow", "bg2.example.com", true)
	p1 := addRule(t, st, tenant, "allow", "bp1.example.com", false)

	// 全局批量删：两条全局都能删掉。
	n, err := st.DeleteRules(ctx, admin, []string{g1.ID, g2.ID}, true)
	if err != nil {
		t.Fatalf("DeleteRules(global): %v", err)
	}
	if n != 2 {
		t.Fatalf("应删掉 2 条全局规则，实际 %d", n)
	}
	// 个人规则没被波及。
	if _, err := st.GetRule(ctx, tenant, p1.ID, false); err != nil {
		t.Fatalf("个人规则被误删: %v", err)
	}

	// 用个人作用域去删全局 id 必须无效（防止误用）。
	g3 := addRule(t, st, tenant, "allow", "bg3.example.com", true)
	if n, err := st.DeleteRules(ctx, tenant, []string{g3.ID}, false); err != nil || n != 0 {
		t.Fatalf("个人作用域不应删掉全局规则，n=%d err=%v", n, err)
	}
	if _, err := st.GetRule(ctx, tenant, g3.ID, true); err != nil {
		t.Fatalf("全局规则被误删: %v", err)
	}
}
