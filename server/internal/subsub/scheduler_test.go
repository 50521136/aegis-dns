package subsub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这一组测试守的是「一次刷新只重建一次快照」这条约束。
//
// 早先的实现里，刷新成功后 Manager.onChanged 会重建一次，API 层的
// refreshSubAsync 又调了一次 touched —— 一次手动刷新写两遍全量快照；
// 更糟的是 304 未变更时 Manager 不回调，API 层却照样重建一次，
// 于是定时轮询（24 小时周期里每份订阅一次）会持续做无意义的 62MB 写盘。
//
// 判据必须是**回调次数与范围**，不能是「版本号对不对」—— 重复重建时
// 版本号照样递增，看起来一切正常。

func newSubTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sub.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newETagListServer 起一个带 ETag 的规则列表服务：
// 第二次带 If-None-Match 来取时回 304，用来构造「未变更」场景。
func newETagListServer(t *testing.T) *httptest.Server {
	t.Helper()
	const etag = `"v1"`
	const body = "||ads.example.com^\n||tracker.example.net^\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type changedCall struct {
	userID string
	global bool
}

// newRecordingManager 建一个记录 onChanged 调用的 Manager。
func newRecordingManager(t *testing.T, st *store.Store) (*Manager, func() []changedCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]changedCall, 0, 4)
	m := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(_ context.Context, userID string, global bool) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, changedCall{userID: userID, global: global})
		})
	return m, func() []changedCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]changedCall, len(calls))
		copy(out, calls)
		return out
	}
}

func TestRefreshNotifiesOnChangedOnceAndSkips304(t *testing.T) {
	ctx := context.Background()
	st := newSubTestStore(t)
	u, err := st.CreateUser(ctx, "subuser", "subuser@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}
	srv := newETagListServer(t)

	m, calls := newRecordingManager(t, st)
	sub := &model.Subscription{
		UserID:   u.ID,
		ListType: model.ListBlock,
		Name:     "etag-list",
		URL:      srv.URL,
		Format:   "auto",
		Enabled:  true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建测试订阅失败: %v", err)
	}

	if _, err := m.Refresh(ctx, sub); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("首次刷新后 onChanged 应恰好调用 1 次，实际 %d 次: %+v", len(got), got)
	}
	if got[0].userID != u.ID || got[0].global {
		t.Fatalf("用户级订阅的 onChanged 范围不对: %+v", got[0])
	}

	// 从库里重新读：LastETag 是 store 更新的，手里的结构体还是旧值，
	// 直接复用会发出空 If-None-Match，上游回 200 而不是 304，这条断言就测空了。
	reloaded, err := st.GetSub(ctx, u.ID, sub.ID)
	if err != nil {
		t.Fatalf("重读订阅失败: %v", err)
	}
	res, err := m.Refresh(ctx, reloaded)
	if err != nil {
		t.Fatalf("二次刷新失败: %v", err)
	}
	if !res.NotModified {
		t.Fatalf("前置条件不成立：二次刷新应当是 304，实际 status=%q", res.LastStatus)
	}
	if n := len(calls()); n != 1 {
		t.Fatalf("304 未变更不该触发快照重建，onChanged 调用数从 1 变成了 %d", n)
	}
}

func TestRefreshNotifiesGlobalScopeForGlobalSub(t *testing.T) {
	ctx := context.Background()
	st := newSubTestStore(t)
	u, err := st.CreateUser(ctx, "admin", "admin@test.local", "hash", "admin")
	if err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}
	srv := newETagListServer(t)

	m, calls := newRecordingManager(t, st)
	sub := &model.Subscription{
		UserID:   u.ID,
		ListType: model.ListBlock,
		Name:     "global-list",
		URL:      srv.URL,
		Format:   "auto",
		Enabled:  true,
		IsGlobal: true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建全局订阅失败: %v", err)
	}
	if _, err := m.Refresh(ctx, sub); err != nil {
		t.Fatalf("刷新全局订阅失败: %v", err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("全局订阅刷新后 onChanged 应恰好调用 1 次，实际 %d 次: %+v", len(got), got)
	}
	// 全局订阅必须上报 global=true：只按创建者重建会让其他启用全局层的
	// 用户一直停留在旧规则上，而界面上看不出任何异常。
	if !got[0].global {
		t.Fatalf("全局订阅的 onChanged 未上报 global=true: %+v", got[0])
	}
}

func TestRefreshFailureDoesNotNotify(t *testing.T) {
	ctx := context.Background()
	st := newSubTestStore(t)
	u, err := st.CreateUser(ctx, "failuser", "failuser@test.local", "hash", "user")
	if err != nil {
		t.Fatalf("建测试用户失败: %v", err)
	}
	m, calls := newRecordingManager(t, st)

	// 关掉的端口：拉取必然失败，条目没变，不能重建快照。
	sub := &model.Subscription{
		UserID:   u.ID,
		ListType: model.ListBlock,
		Name:     "dead",
		URL:      "http://127.0.0.1:1/list.txt",
		Format:   "auto",
		Enabled:  true,
	}
	if err := st.CreateSub(ctx, sub); err != nil {
		t.Fatalf("建测试订阅失败: %v", err)
	}
	if _, err := m.Refresh(ctx, sub); err == nil {
		t.Fatal("前置条件不成立：不可达地址应当刷新失败")
	}
	if n := len(calls()); n != 0 {
		t.Fatalf("刷新失败不该触发快照重建，实际调用 %d 次", n)
	}
}
