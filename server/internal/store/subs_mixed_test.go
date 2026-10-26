package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 一份订阅里同时有 block 与 allow 时，两种类别都必须落库。
//
// 缺陷形态：ReplaceSubEntries 只接受**一个** kind，于是 blocklist 订阅里的
// `@@` 放行条目被整批丢弃 —— 白名单例外静默失效，订阅状态却显示 ok。
// 用户报障的那份 rules.txt 有 3002 条 @@，全都丢了。
func TestReplaceSubEntriesMixedKeepsBothKinds(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "mixed.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser(ctx, "t", "t@example.com", "x", model.RoleUser)
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	sub := &model.Subscription{
		UserID: u.ID, Name: "x", URL: "http://x", ListType: "blocklist",
		Format: "auto", Enabled: true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建订阅失败: %v", err)
	}
	sid := sub.ID

	entries := []SubEntry{
		{Domain: "block1.example", Kind: "block"},
		{Domain: "block2.example", Kind: "block"},
		{Domain: "allow1.example", Kind: "allow"},
	}
	if err := st.ReplaceSubEntriesMixed(ctx, sid, entries); err != nil {
		t.Fatalf("混合写入失败: %v", err)
	}

	n, err := st.CountSubEntries(ctx, sid)
	if err != nil {
		t.Fatalf("数条目失败: %v", err)
	}
	if n != 3 {
		t.Fatalf("应写入 3 条，实际 %d 条（allow 条目被丢了？）", n)
	}

	block, allow, err := st.SubEntriesByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("读条目失败: %v", err)
	}
	if len(block) != 2 {
		t.Errorf("block 应为 2，实际 %d: %v", len(block), block)
	}
	if len(allow) != 1 || allow[0] != "allow1.example" {
		t.Errorf("allow 应为 [allow1.example]，实际 %v", allow)
	}
}

// 同一域名从 block 改成 allow 时，类别必须真的变过来。
//
// 只 INSERT ... WHERE NOT EXISTS 的写法会留下旧的 block 行，
// 于是「用户后来把它加进白名单」这个动作完全没效果。
func TestReplaceSubEntriesMixedUpdatesKind(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "kind.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser(ctx, "t", "t@example.com", "x", model.RoleUser)
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	sub := &model.Subscription{
		UserID: u.ID, Name: "x", URL: "http://x", ListType: "blocklist",
		Format: "auto", Enabled: true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建订阅失败: %v", err)
	}
	sid := sub.ID

	if err := st.ReplaceSubEntriesMixed(ctx, sid, []SubEntry{{Domain: "flip.example", Kind: "block"}}); err != nil {
		t.Fatalf("第一次写入失败: %v", err)
	}
	if err := st.ReplaceSubEntriesMixed(ctx, sid, []SubEntry{{Domain: "flip.example", Kind: "allow"}}); err != nil {
		t.Fatalf("第二次写入失败: %v", err)
	}

	block, allow, err := st.SubEntriesByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("读条目失败: %v", err)
	}
	if len(block) != 0 {
		t.Errorf("类别改成 allow 后不该还留在 block 里: %v", block)
	}
	if len(allow) != 1 || allow[0] != "flip.example" {
		t.Errorf("应变成 allow，实际 %v", allow)
	}
}
