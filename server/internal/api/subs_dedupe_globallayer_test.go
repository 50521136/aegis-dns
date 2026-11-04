package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这组测试锁住「关掉全局层 = 不参与全局订阅查重/收录」这条边界。
//
// 背景：用户关掉「叠加全局规则」之后，全局订阅对他完全不生效。此时
// 创建私有订阅若仍按「全局已有此内容」回 409，他连自己想订的列表都加不进去，
// 而那份列表本来就只有他自己的订阅在拦。自动收录同理：收录会删掉他的订阅
// 再把内容转到全局层，对他等于凭空失去防护。

const (
	globalSubURL = "https://raw.githubusercontent.com/example/list/main/rules.txt"
	// 等价形态：jsDelivr 与 github raw 指向同一份内容，归一键应当相同。
	equivalentSubURL = "https://cdn.jsdelivr.net/gh/example/list@main/rules.txt"
)

// newSubDedupeTestServer 建一个带全局订阅与两个用户的测试环境。
//
// 返回的 store 给需要直接查库的用例（收录候选）用。
func newSubDedupeTestServer(t *testing.T) (s *Server, st *store.Store, on *model.User, off *model.User) {
	t.Helper()
	s, st = newGlobalLayerTestServer(t)
	ctx := context.Background()

	// 全局订阅的 user_id 有外键约束，必须先有真实用户行。
	owner, err := st.CreateUser(ctx, "globadmin", "globadmin@t.local", "x", "admin")
	if err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}

	gs := &model.Subscription{
		ID:       "sub_global_test",
		UserID:   owner.ID,
		Name:     "公共列表",
		URL:      globalSubURL,
		ListType: model.ListBlock,
		Format:   "auto",
		Enabled:  true,
		IsGlobal: true,
	}
	if err := st.CreateSub(ctx, gs); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}

	mkUser := func(name string, useGlobal bool) *model.User {
		u, err := st.CreateUser(ctx, name, name+"@t.local", "x", "user")
		if err != nil {
			t.Fatalf("建用户 %s 失败: %v", name, err)
		}
		if err := st.UpdateUserFields(ctx, u.ID, nil, nil, nil, &useGlobal, nil, nil); err != nil {
			t.Fatalf("设置 use_global_rules 失败: %v", err)
		}
		got, err := st.GetUserByID(ctx, u.ID)
		if err != nil {
			t.Fatalf("读回用户失败: %v", err)
		}
		return got
	}
	return s, st, mkUser("globon", true), mkUser("globoff", false)
}

// postSub 以给定用户身份调一次「创建我的订阅」。
//
// enabled=false：创建成功后 handler 会起后台 goroutine 拉取（refreshSubAsync），
// 而测试环境没有装配 subsub.Manager（会 nil 解引用）。关掉 enabled 只跳过
// 拉取这一步，查重/配额/落库这些被断言的路径一条都不少。
func postSub(t *testing.T, s *Server, u *model.User, url string) (int, string) {
	t.Helper()
	body := `{"name":"测试源","url":"` + url + `","list_type":"blocklist","format":"auto","enabled":false}`
	req := httptest.NewRequest("POST", "/api/v1/subscriptions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUser, u))
	rec := httptest.NewRecorder()
	s.handleCreateSub(rec, req)
	return rec.Code, rec.Body.String()
}

// TestCreateSubSkipsGlobalDedupeWhenGlobalLayerOff 验证关闭全局层后不被 409 拦。
func TestCreateSubSkipsGlobalDedupeWhenGlobalLayerOff(t *testing.T) {
	s, _, on, off := newSubDedupeTestServer(t)

	// 叠加全局层的用户：必须被拦（这份内容已经在全局层生效了）。
	code, resp := postSub(t, s, on, equivalentSubURL)
	if code != 409 {
		t.Fatalf("叠加全局层时应 409，实际 %d：%s", code, resp)
	}
	if !strings.Contains(resp, "全局订阅已存在此规则") {
		t.Errorf("409 的提示应说明全局订阅已存在，实际 %s", resp)
	}

	// 关掉全局层的用户：必须放行 —— 全局订阅对他不生效，拦下来等于
	// 让他连自己想订的列表都加不进去。
	code, resp = postSub(t, s, off, equivalentSubURL)
	if code != 201 {
		t.Fatalf("关闭全局层时应 201 放行，实际 %d：%s", code, resp)
	}
}

// TestGlobalLookupRespectsGlobalLayerOff 验证提示接口同样按开关返回。
//
// 前端据此决定要不要渲染「全局订阅已存在」并禁用提交按钮。
func TestGlobalLookupRespectsGlobalLayerOff(t *testing.T) {
	s, _, on, off := newSubDedupeTestServer(t)

	call := func(u *model.User) map[string]any {
		req := httptest.NewRequest("GET", "/api/v1/admin/subscriptions/global-lookup?url="+equivalentSubURL, nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKeyUser, u))
		rec := httptest.NewRecorder()
		s.handleGlobalSubByURL(rec, req)
		if rec.Code != 200 {
			t.Fatalf("状态码 %d，响应 %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析响应失败: %v（%s）", err, rec.Body.String())
		}
		return out
	}

	if got := call(on); got["exists"] != true {
		t.Errorf("叠加全局层时应提示已存在，实际 %v", got)
	}
	got := call(off)
	if got["exists"] != false {
		t.Errorf("关闭全局层时不应提示已存在，实际 %v", got)
	}
	// 必须显式带上「因为全局层关着」，否则排障时会以为接口漏查了。
	if got["global_layer_off"] != true {
		t.Errorf("关闭全局层时应带 global_layer_off=true，实际 %v", got)
	}
}

// TestPromotionCandidatesSkipGlobalLayerOffUsers 验证自动收录不统计关闭全局层的用户。
//
// 收录会把用户的私有订阅删掉、内容转到全局层；而全局层对他不生效 ——
// 等于管理员一次整理就把他的防护静默抹掉。这类用户不该参与计数，
// 也就不可能因为「凑够人数」而被删订阅。
func TestPromotionCandidatesSkipGlobalLayerOffUsers(t *testing.T) {
	_, st, on, off := newSubDedupeTestServer(t)
	ctx := context.Background()

	// 两个用户订阅同一份内容（都用等价形态之外的另一条链接，避免撞上
	// 上面那条全局订阅的归一键）。
	const userURL = "https://raw.githubusercontent.com/other/list/main/x.txt"
	mk := func(u *model.User, id string) {
		sub := &model.Subscription{
			ID: id, UserID: u.ID, Name: "同款", URL: userURL,
			ListType: model.ListBlock, Format: "auto", Enabled: true,
		}
		if err := st.CreateSub(ctx, sub); err != nil {
			t.Fatalf("建私有订阅失败: %v", err)
		}
	}
	mk(on, "sub_on_1")
	mk(off, "sub_off_1")

	cands, err := st.ListPromotionCandidates(ctx, 2)
	if err != nil {
		t.Fatalf("取收录候选失败: %v", err)
	}
	for _, c := range cands {
		for _, sub := range c.Subs {
			if sub.UserID == off.ID {
				t.Fatalf("关闭全局层的用户不应出现在收录候选里（sub=%s）", sub.ID)
			}
		}
	}

	// 反证：只有 1 个开全局层的用户订阅这份内容，凑不够 2 人，
	// 因此它根本不该成为候选 —— 证明上面那条不是「候选列表恰好为空」。
	for _, c := range cands {
		for _, u := range c.URLs {
			if strings.Contains(u, "other/list") {
				t.Fatalf("只有 1 个开全局层的用户，不该成为收录候选：%v", c.URLs)
			}
		}
	}

	// 再补一个开全局层的用户订阅同一份内容，凑够 2 人 → 必须成为候选。
	on2, err := st.CreateUser(ctx, "globon2", "globon2@t.local", "x", "user")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	if err := st.CreateSub(ctx, &model.Subscription{
		ID: "sub_on_2", UserID: on2.ID, Name: "同款3", URL: userURL,
		ListType: model.ListBlock, Format: "auto", Enabled: true,
	}); err != nil {
		t.Fatalf("建私有订阅失败: %v", err)
	}
	cands, err = st.ListPromotionCandidates(ctx, 2)
	if err != nil {
		t.Fatalf("取收录候选失败: %v", err)
	}
	found := false
	for _, c := range cands {
		for _, u := range c.URLs {
			if strings.Contains(u, "other/list") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("两个开全局层的用户订阅同一份内容，应成为收录候选")
	}
}
