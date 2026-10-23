package diagnose

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件测异步 worker：队列、限流、失败落库。
//
// 重点在「失败也要有交代」—— 卡在 pending 的反馈比失败更糟，
// 因为管理员会一直等一个永远不会来的结果。

// memStore 是 Store 的内存替身。
type memStore struct {
	mu     sync.Mutex
	done   map[string]model.Feedback
	failed map[string]string
}

func newMemStore() *memStore {
	return &memStore{done: map[string]model.Feedback{}, failed: map[string]string{}}
}

func (m *memStore) WriteAIDiagnosis(_ context.Context, f *model.Feedback) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done[f.ID] = *f
	return nil
}

func (m *memStore) MarkAIDiagnosisFailed(_ context.Context, fid, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed[fid] = reason
	return nil
}

func (m *memStore) snapshot() (map[string]model.Feedback, map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := make(map[string]model.Feedback, len(m.done))
	for k, v := range m.done {
		d[k] = v
	}
	f := make(map[string]string, len(m.failed))
	for k, v := range m.failed {
		f[k] = v
	}
	return d, f
}

// newTestWorker 起一个 worker，模型端用假服务器。
func newTestWorker(t *testing.T, respContent string, cfg LLMConfig) (*Worker, *memStore, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": respContent}},
			},
		})
	}))
	cfg.BaseURL = srv.URL
	if cfg.Model == "" {
		cfg.Model = "test-model"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}

	st := newMemStore()
	src := &fakeSource{userBlock: []string{"t.apkpure.net"}, enabled: true}
	w := NewWorker(src, st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() LLMConfig { return cfg },
		func() string { return "on_submit" })
	return w, st, func() { w.Close(); srv.Close() }
}

// TestWorkerWritesDiagnosis 断言正常流程会把结果落库。
func TestWorkerWritesDiagnosis(t *testing.T) {
	w, st, cleanup := newTestWorker(t,
		`{"verdict":"wrong_block","reason":"订阅误拦","action":{"type":"add_global_allow","domains":["t.apkpure.net"]},"reply":"已放行"}`,
		LLMConfig{})
	defer cleanup()

	w.Enqueue(model.Feedback{ID: "fb1", UserID: "u1", Kind: model.FeedbackWrongBlock, Domain: "t.apkpure.net"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := st.snapshot()
		if f, ok := done["fb1"]; ok {
			if f.AIStatus != model.AIDiagDone {
				t.Fatalf("ai_status 应为 done，实际 %q", f.AIStatus)
			}
			if f.AIAction != model.AIActGlobalAllow {
				t.Fatalf("action 应为 add_global_allow，实际 %q", f.AIAction)
			}
			if f.AIDomains != "t.apkpure.net" {
				t.Fatalf("domains 不对: %q", f.AIDomains)
			}
			if f.AIEvidence == "" {
				t.Fatal("证据 JSON 必须落库（审计用）")
			}
			if f.AIModel != "test-model" {
				t.Fatalf("模型名没记录: %q", f.AIModel)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("3 秒内没有落库")
}

// TestWorkerGuardDowngrades 断言闸门把危险建议降级后仍写库。
//
// 模型建议放行 com，最终落库的动作必须是 need_human，
// 且 ai_guard_notes 里能看到拒绝原因。
func TestWorkerGuardDowngrades(t *testing.T) {
	w, st, cleanup := newTestWorker(t,
		`{"verdict":"wrong_block","reason":"x","action":{"type":"add_global_allow","domains":["com"]},"reply":"已放行"}`,
		LLMConfig{})
	defer cleanup()

	// 证据里没有 com 这个域名，闸门必然拒绝。
	w.Enqueue(model.Feedback{ID: "fb2", UserID: "u1", Kind: model.FeedbackWrongBlock, Domain: "t.apkpure.net"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := st.snapshot()
		if f, ok := done["fb2"]; ok {
			if f.AIAction != model.AIActNeedHuman {
				t.Fatalf("危险建议应被降级为 need_human，实际 %q", f.AIAction)
			}
			if f.AIDomains != "" {
				t.Fatalf("降级后不该残留域名，实际 %q", f.AIDomains)
			}
			if !strings.Contains(f.AIGuardNotes, "没有") && !strings.Contains(f.AIGuardNotes, "拒绝") {
				t.Fatalf("应记录拒绝原因，实际 %q", f.AIGuardNotes)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("3 秒内没有落库")
}

// TestWorkerModelFailureMarked 断言模型失败时标记 failed 而不是停在 pending。
func TestWorkerModelFailureMarked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := newMemStore()
	src := &fakeSource{userBlock: []string{"a.example.com"}, enabled: true}
	cfg := LLMConfig{BaseURL: srv.URL, Model: "m", Timeout: 2 * time.Second}
	w := NewWorker(src, st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() LLMConfig { return cfg }, func() string { return "manual" })
	defer w.Close()

	w.Enqueue(model.Feedback{ID: "fb3", UserID: "u1", Domain: "a.example.com"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, failed := st.snapshot()
		if reason, ok := failed["fb3"]; ok {
			if !strings.Contains(reason, "模型调用失败") {
				t.Fatalf("失败原因应指明是模型调用，实际 %q", reason)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("模型失败后没有标记 failed，反馈会卡在 pending")
}

// TestWorkerRateLimit 断言超过每小时上限时跳过并给出说明。
func TestWorkerRateLimit(t *testing.T) {
	w, st, cleanup := newTestWorker(t,
		`{"verdict":"correct","reason":"x","action":{"type":"no_action"},"reply":"正常"}`,
		LLMConfig{MaxPerHour: 1})
	defer cleanup()

	w.Enqueue(model.Feedback{ID: "r1", UserID: "u1", Domain: "a.example.com"})
	// 等第一条处理完
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := st.snapshot()
		if _, ok := done["r1"]; ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 第二条应被限流
	w.Enqueue(model.Feedback{ID: "r2", UserID: "u1", Domain: "b.example.com"})

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, failed := st.snapshot()
		if reason, ok := failed["r2"]; ok {
			if !strings.Contains(reason, "上限") {
				t.Fatalf("限流原因应说明上限，实际 %q", reason)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("第二条没有被限流")
}

// TestWorkerShouldAutoRun 断言触发模式判断。
func TestWorkerShouldAutoRun(t *testing.T) {
	cases := map[string]bool{"on_submit": true, "manual": false, "off": false}
	for mode, want := range cases {
		m := mode
		w := &Worker{modeFn: func() string { return m }}
		if got := w.ShouldAutoRun(); got != want {
			t.Errorf("模式 %q 期望 %v，实际 %v", mode, want, got)
		}
	}
	// nil worker 不能 panic
	var nilW *Worker
	if nilW.ShouldAutoRun() {
		t.Fatal("nil worker 应返回 false")
	}
}
