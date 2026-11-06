package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这个文件钉住三件容易被后人「顺手改回去」的事：
//  1. 推理模型把 token 全花在思考上、content 为空时，必须给出带 usage 的
//     明确错误，并自动加大 max_tokens 重试；
//  2. 主模型失败要依次回退，并且回报实际使用的模型名；
//  3. /models 列表要去重排序，失败时不影响诊断主链路。
//
// 线上实测：Sage-A1.5 / Agents-A1 在 max_tokens=800 时
// finish_reason=length、content=""、reasoning_tokens=800（额度已扣），
// 提到 1600 才正常输出 JSON。

const validSuggestionJSON = `{"verdict":"wrong_block","reason":"订阅误拦","action":{"type":"add_global_allow","domains":["t.apkpure.net"]},"reply":"已放行"}`

// TestDiagnoseRetriesOnThinkingTruncation 断言空输出会自动加大 max_tokens 重试。
func TestDiagnoseRetriesOnThinkingTruncation(t *testing.T) {
	var tokens []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		tokens = append(tokens, body.MaxTokens)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": ""},
				"finish_reason": "length",
			}},
			"usage": map[string]int{"completion_tokens": body.MaxTokens, "reasoning_tokens": body.MaxTokens},
		}
		if body.MaxTokens >= 1600 {
			resp = map[string]any{
				"choices": []map[string]any{{
					"message":       map[string]string{"role": "assistant", "content": validSuggestionJSON},
					"finish_reason": "stop",
				}},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, Model: "reasoning-model", Timeout: 3 * time.Second, MaxTokens: 800}
	sug, err := Diagnose(context.Background(), cfg, blockEvidence("t.apkpure.net"))
	if err != nil {
		t.Fatalf("加大重试后应成功，实际: %v", err)
	}
	if sug.Verdict != "wrong_block" || sug.Model != "reasoning-model" {
		t.Fatalf("结论或模型名不对: %+v", sug)
	}
	if len(tokens) != 2 || tokens[0] != 800 || tokens[1] != 1600 {
		t.Fatalf("应按 800 → 1600 重试，实际 %v", tokens)
	}
}

// TestDiagnoseTruncationErrorCarriesUsage 断言不重试时错误里要说清已消耗额度。
func TestDiagnoseTruncationErrorCarriesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "", "reasoning_content": "Thinking..."},
				"finish_reason": "length",
			}},
			"usage": map[string]int{"completion_tokens": 800, "reasoning_tokens": 800},
		})
	}))
	defer srv.Close()

	// maxTokens 已到自动重试上限，只会调一次。
	cfg := LLMConfig{BaseURL: srv.URL, Model: "m", Timeout: 3 * time.Second, MaxTokens: maxAutoRetryTokens}
	_, err := Diagnose(context.Background(), cfg, blockEvidence("a.example.com"))
	if err == nil {
		t.Fatal("空输出应报错")
	}
	for _, want := range []string{"max_tokens", "reasoning", "已消耗额度"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里应包含 %q，实际 %q", want, err.Error())
		}
	}
}

// TestDiagnoseFallsBackToNextModel 断言主模型失败后按顺序回退并回报实际模型。
func TestDiagnoseFallsBackToNextModel(t *testing.T) {
	var used []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		used = append(used, body.Model)
		if body.Model == "primary" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"model overloaded"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": validSuggestionJSON},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	cfg := LLMConfig{
		BaseURL: srv.URL, Model: "primary", Timeout: 3 * time.Second,
		FallbackModels: []string{"backup-1", "backup-2"},
	}
	sug, err := Diagnose(context.Background(), cfg, blockEvidence("t.apkpure.net"))
	if err != nil {
		t.Fatalf("回退后应成功，实际: %v", err)
	}
	if sug.Model != "backup-1" {
		t.Fatalf("应回报实际使用的回退模型，实际 %q", sug.Model)
	}
	if len(used) != 2 || used[0] != "primary" || used[1] != "backup-1" {
		t.Fatalf("应按 primary → backup-1 尝试，实际 %v", used)
	}
}

// TestModelChainDedup 断言回退链去重、去空、去掉与主模型重复的项。
func TestModelChainDedup(t *testing.T) {
	got := modelChain("a", []string{"", "b", "a", "b", " c "})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("长度不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项应为 %q，实际 %v", i, want[i], got)
		}
	}
}

// TestParseFallbackModels 断言配置串支持逗号/换行/空格混合分隔。
func TestParseFallbackModels(t *testing.T) {
	got := ParseFallbackModels("a, b\nc  d,,e")
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("长度不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项应为 %q，实际 %v", i, want[i], got)
		}
	}
}

// TestListModels 断言列表去重排序、带 Authorization、路径正确。
func TestListModels(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "z-model"}, {"id": "a-model"}, {"id": "a-model"}, {"id": ""},
			},
		})
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, APIKey: "sk-test"}
	got, err := ListModels(context.Background(), cfg)
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a-model" || got[1].ID != "z-model" {
		t.Fatalf("应去重并排序，实际 %+v", got)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization 不对: %q", gotAuth)
	}
}

// TestListModelsHTTPError 断言上游不支持 /models 时返回明确错误而不是空列表。
func TestListModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer srv.Close()

	_, err := ListModels(context.Background(), LLMConfig{BaseURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("应报 404，实际 %v", err)
	}
}

// TestListModelsUsesShortTimeout 钉住列表接口不被诊断超时拖住。
func TestListModelsUsesShortTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := ListModels(ctx, LLMConfig{BaseURL: srv.URL, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("不应被长诊断超时影响: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("应只调一次，实际 %d", calls.Load())
	}
}
