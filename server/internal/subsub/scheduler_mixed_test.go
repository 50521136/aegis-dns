package subsub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// newStaticListServer 起一个返回固定内容的列表服务。
func newStaticListServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 刷新一条 blocklist 订阅时，其中的 @@ 放行条目必须一起落库。
//
// 这是用户报障链条的第二环：解析层修好混合格式后，Parse 已经能给出
// block + allow 两类条目（那份 rules.txt 有 3002 条 @@），但刷新路径
// 用 ReplaceSubEntries 只写**一个** kind，allow 条目被整批丢弃 ——
// 用户写了放行却仍被拦，订阅状态照样显示 ok。
func TestRefreshPersistsAllowEntriesFromBlocklist(t *testing.T) {
	ctx := context.Background()
	st := newSubTestStore(t)
	u, err := st.CreateUser(ctx, "mixuser", "mix@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}

	// 混合列表：2 条拦截 + 1 条放行 + 1 条 hosts 多域名
	const body = "||ads1.example^\n||ads2.example^\n@@||ok.example^\n0.0.0.0 h1.example h2.example\n"
	srv := newStaticListServer(t, body)

	m, _ := newRecordingManager(t, st)
	sub := &model.Subscription{
		UserID:   u.ID,
		ListType: model.ListBlock,
		Name:     "mixed",
		URL:      srv.URL,
		Format:   "auto",
		Enabled:  true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建测试订阅失败: %v", err)
	}
	if _, err := m.Refresh(ctx, sub); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	block, allow, err := st.SubEntriesByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("读条目失败: %v", err)
	}
	// ads1 + ads2 + h1 + h2 = 4 条拦截
	if len(block) != 4 {
		t.Errorf("拦截条目应为 4（ads1/ads2/h1/h2），实际 %d: %v", len(block), block)
	}
	if len(allow) != 1 || allow[0] != "ok.example" {
		t.Errorf("@@ 放行条目必须落库，实际 allow=%v（被丢弃了？）", allow)
	}

	n, err := st.CountSubEntries(ctx, sub.ID)
	if err != nil {
		t.Fatalf("数条目失败: %v", err)
	}
	if n != 5 {
		t.Errorf("入库总数应为 5，实际 %d", n)
	}
}

// allowlist 订阅用纯域名列表时必须全部落成 allow。
//
// 旧实现 kind 恒默认 block，allowlist 源解析出 allow=0，
// 而刷新路径对 allowlist 只取 allow —— 整份订阅入库 0 条。
func TestRefreshAllowlistPlainDomainsPersists(t *testing.T) {
	ctx := context.Background()
	st := newSubTestStore(t)
	u, err := st.CreateUser(ctx, "allowuser", "allow@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}

	srv := newStaticListServer(t, "good1.example\ngood2.example\n")
	m, _ := newRecordingManager(t, st)
	sub := &model.Subscription{
		UserID:   u.ID,
		ListType: model.ListAllow,
		Name:     "allow-plain",
		URL:      srv.URL,
		Format:   "auto",
		Enabled:  true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建测试订阅失败: %v", err)
	}
	if _, err := m.Refresh(ctx, sub); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	n, err := st.CountSubEntries(ctx, sub.ID)
	if err != nil {
		t.Fatalf("数条目失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("allowlist 的纯域名应入库 2 条，实际 %d 条（整份订阅失效）", n)
	}
	block, allow, err := st.SubEntriesByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("读条目失败: %v", err)
	}
	if len(allow) != 2 || len(block) != 0 {
		t.Errorf("应全部为 allow，实际 block=%v allow=%v", block, allow)
	}
}

var _ = store.SubEntry{}
