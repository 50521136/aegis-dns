package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这组测试锁住「全局层提示」的数据来源必须真实。
//
// 背景：管理员在全局层挂好公共规则集后，用户打开「我的规则 / 我的订阅」
// 看到的是一片空白（全局规则属于管理员，不在他的列表里），于是以为自己
// 毫无防护，再挂一份 OISD 之类的大订阅 —— 订阅条目按用户压平进快照，
// 重复挂既吃用户自己的订阅配额，也吃服务器内存。
//
// 所以前端要提示「管理员已内置 N 条，一般无需自己添加」，而这个 N 必须来自
// 真实统计：在没有任何全局内容时提示「已内置」是纯误导，比不提示更糟。

func newGlobalLayerTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gl.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{Domain: "dns.test.local"}
	cfg.API.JWTSecret = "0123456789abcdef0123456789abcdef"
	s := New(cfg, st, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, st
}

// callGlobalLayer 直接调 handler，注入已登录用户。
func callGlobalLayer(t *testing.T, s *Server, u *model.User) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/me/global-layer", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUser, u))
	rec := httptest.NewRecorder()
	s.handleMyGlobalLayer(rec, req)
	if rec.Code != 200 {
		t.Fatalf("状态码 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v（%s）", err, rec.Body.String())
	}
	return out
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func TestGlobalLayerEmptyMeansNoHint(t *testing.T) {
	s, st := newGlobalLayerTestServer(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "u1", "u1@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}

	out := callGlobalLayer(t, s, u)
	if out["has_rules"] != false {
		t.Fatalf("没有任何全局内容时 has_rules 必须为 false，实际 %v", out["has_rules"])
	}
	if num(out["rule_count"]) != 0 || num(out["sub_entry_count"]) != 0 {
		t.Fatalf("空库的统计应为 0，实际 %v", out)
	}
}

func TestGlobalLayerCountsRulesAndSubEntries(t *testing.T) {
	s, st := newGlobalLayerTestServer(t)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "admin@test.local", "hash", "admin")
	if err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	u, err := st.CreateUser(ctx, "u2", "u2@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}

	// 两条全局规则 + 一条用户自己的规则：用户自己的不能算进全局数字。
	for _, p := range []string{"ads.example.com", "track.example.net"} {
		if err := st.CreateRule(ctx, &model.Rule{
			UserID: admin.ID, Kind: model.KindBlock, Pattern: p, IsGlobal: true, Enabled: true,
		}); err != nil {
			t.Fatalf("建全局规则失败: %v", err)
		}
	}
	if err := st.CreateRule(ctx, &model.Rule{
		UserID: u.ID, Kind: model.KindBlock, Pattern: "mine.example.com", Enabled: true,
	}); err != nil {
		t.Fatalf("建用户规则失败: %v", err)
	}

	// 一份全局订阅带 3 条条目 + 一份用户自己的订阅（不能算进去）。
	gsub := &model.Subscription{
		UserID: admin.ID, Name: "global-list", URL: "https://example.com/g.txt",
		ListType: model.ListBlock, Format: "auto", Enabled: true, IsGlobal: true,
	}
	if err := st.CreateSub(ctx, gsub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}
	if err := st.ReplaceSubEntries(ctx, gsub.ID, "block",
		[]string{"a.example", "b.example", "c.example"}); err != nil {
		t.Fatalf("写全局订阅条目失败: %v", err)
	}
	usub := &model.Subscription{
		UserID: u.ID, Name: "my-list", URL: "https://example.com/u.txt",
		ListType: model.ListBlock, Format: "auto", Enabled: true,
	}
	if err := st.CreateSub(ctx, usub); err != nil {
		t.Fatalf("建用户订阅失败: %v", err)
	}
	if err := st.ReplaceSubEntries(ctx, usub.ID, "block", []string{"x.example"}); err != nil {
		t.Fatalf("写用户订阅条目失败: %v", err)
	}

	out := callGlobalLayer(t, s, u)
	if got := num(out["rule_count"]); got != 2 {
		t.Fatalf("全局规则应为 2 条（用户自己的不算），实际 %d", got)
	}
	if got := num(out["sub_count"]); got != 1 {
		t.Fatalf("全局订阅应为 1 份，实际 %d", got)
	}
	if got := num(out["sub_entry_count"]); got != 3 {
		t.Fatalf("全局订阅条目应为 3 条，实际 %d", got)
	}
	if out["has_rules"] != true {
		t.Fatalf("有全局内容时 has_rules 必须为 true，实际 %v", out["has_rules"])
	}
	if out["enabled"] != true {
		t.Fatalf("默认应视为开启全局层，实际 %v", out["enabled"])
	}
}

func TestGlobalLayerRespectsUserOptOut(t *testing.T) {
	s, st := newGlobalLayerTestServer(t)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "admin@test.local", "hash", "admin")
	if err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	u, err := st.CreateUser(ctx, "u3", "u3@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	if err := st.CreateRule(ctx, &model.Rule{
		UserID: admin.ID, Kind: model.KindBlock, Pattern: "ads.example.com", IsGlobal: true, Enabled: true,
	}); err != nil {
		t.Fatalf("建全局规则失败: %v", err)
	}

	// 用户自己关掉全局层：条数照旧存在（那是客观事实），但不能再提示
	// 「正在为你生效」—— 否则提示与实际行为矛盾。
	u.UseGlobalRules = false
	out := callGlobalLayer(t, s, u)
	if out["enabled"] != false {
		t.Fatalf("用户关闭全局层后 enabled 应为 false，实际 %v", out["enabled"])
	}
	if out["has_rules"] != false {
		t.Fatalf("用户关闭全局层后不应提示已内置，实际 has_rules=%v", out["has_rules"])
	}
	if num(out["rule_count"]) != 1 {
		t.Fatalf("统计数字本身应保持真实（1 条），实际 %d", num(out["rule_count"]))
	}
}

func TestGlobalLayerIgnoresDisabledGlobalRules(t *testing.T) {
	s, st := newGlobalLayerTestServer(t)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "admin@test.local", "hash", "admin")
	if err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	u, err := st.CreateUser(ctx, "u4", "u4@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	// 停用的全局规则不生效，不能计入「已内置」—— 否则用户被提示
	// 「管理员已内置 5 条」而实际一条都没在挡。
	if err := st.CreateRule(ctx, &model.Rule{
		UserID: admin.ID, Kind: model.KindBlock, Pattern: "off.example.com",
		IsGlobal: true, Enabled: false,
	}); err != nil {
		t.Fatalf("建停用全局规则失败: %v", err)
	}

	out := callGlobalLayer(t, s, u)
	if num(out["rule_count"]) != 0 {
		t.Fatalf("停用的全局规则不应计入，实际 %d", num(out["rule_count"]))
	}
	if out["has_rules"] != false {
		t.Fatalf("只剩停用规则时不应提示已内置，实际 %v", out["has_rules"])
	}
}
