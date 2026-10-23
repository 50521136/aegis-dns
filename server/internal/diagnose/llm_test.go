package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 这个文件测模型对接层。重点不是「能不能调通」，而是
// 「模型输出不听话时会不会把脏数据带进系统」。

// TestParseSuggestionPlainJSON 断言标准输出能解析。
func TestParseSuggestionPlainJSON(t *testing.T) {
	raw := `{"verdict":"wrong_block","reason":"订阅层误拦","action":{"type":"add_global_allow","domains":["t.apkpure.net"]},"reply":"已放行"}`
	sug, err := parseSuggestion(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if sug.Verdict != "wrong_block" || sug.Action.Type != "add_global_allow" {
		t.Fatalf("解析结果不对: %+v", sug)
	}
	if len(sug.Action.Domains) != 1 || sug.Action.Domains[0] != "t.apkpure.net" {
		t.Fatalf("域名不对: %v", sug.Action.Domains)
	}
}

// TestParseSuggestionWithMarkdownFence 断言被 ```json 包裹时仍能解析。
//
// 模型不按「只输出 JSON」的指令走是常态，这种情况如果不兼容，
// 线上会频繁报「模型输出不是合法 JSON」。
func TestParseSuggestionWithMarkdownFence(t *testing.T) {
	raw := "```json\n{\"verdict\":\"correct\",\"reason\":\"规则正确\",\"action\":{\"type\":\"no_action\"},\"reply\":\"这不是误杀\"}\n```"
	sug, err := parseSuggestion(raw)
	if err != nil {
		t.Fatalf("应容忍 Markdown 包裹，实际: %v", err)
	}
	if sug.Verdict != "correct" {
		t.Fatalf("verdict 不对: %q", sug.Verdict)
	}
}

// TestParseSuggestionWithPrefixText 断言 JSON 前有解释文字时仍能解析。
func TestParseSuggestionWithPrefixText(t *testing.T) {
	raw := `好的，我的判断如下：{"verdict":"unclear","reason":"证据不足","action":{"type":"need_human"},"reply":"需要人工确认"} 以上。`
	sug, err := parseSuggestion(raw)
	if err != nil {
		t.Fatalf("应容忍前后文字，实际: %v", err)
	}
	if sug.Action.Type != "need_human" {
		t.Fatalf("action 不对: %q", sug.Action.Type)
	}
}

// TestParseSuggestionRejectsBadEnums 断言非法枚举被拒绝。
//
// 这是关键防线：模型如果发明一个新 action（例如 "allow_everything"），
// 系统必须报错而不是「尽力理解」。
func TestParseSuggestionRejectsBadEnums(t *testing.T) {
	cases := []string{
		`{"verdict":"maybe","reason":"x","action":{"type":"no_action"},"reply":"x"}`,
		`{"verdict":"correct","reason":"x","action":{"type":"allow_everything"},"reply":"x"}`,
	}
	for _, raw := range cases {
		if _, err := parseSuggestion(raw); err == nil {
			t.Errorf("应拒绝非法枚举: %s", raw)
		}
	}
}

// TestParseSuggestionRejectsEmptyReply 断言缺回复文案时拒绝（need_human 除外）。
func TestParseSuggestionRejectsEmptyReply(t *testing.T) {
	bad := `{"verdict":"wrong_block","reason":"x","action":{"type":"add_global_allow","domains":["a.com"]},"reply":""}`
	if _, err := parseSuggestion(bad); err == nil {
		t.Fatal("add_global_allow 缺 reply 应被拒绝")
	}
	ok := `{"verdict":"unclear","reason":"x","action":{"type":"need_human"},"reply":""}`
	if _, err := parseSuggestion(ok); err != nil {
		t.Fatalf("need_human 允许空 reply，实际: %v", err)
	}
}

// TestDiagnoseAgainstFakeServer 端到端验证请求构造与响应解析。
func TestDiagnoseAgainstFakeServer(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
			Temperature float64 `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		// 回一个合法的模型响应
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"role":    "assistant",
					"content": `{"verdict":"wrong_block","reason":"订阅误拦","action":{"type":"add_global_allow","domains":["t.apkpure.net"]},"reply":"已放行"}`,
				}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "test-model", Timeout: 5 * time.Second}
	ev := blockEvidence("t.apkpure.net")
	sug, err := Diagnose(context.Background(), cfg, ev)
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if sug.Verdict != "wrong_block" {
		t.Fatalf("verdict 不对: %q", sug.Verdict)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization 头不对: %q", gotAuth)
	}
	if gotModel != "test-model" {
		t.Fatalf("模型名不对: %q", gotModel)
	}
}

// TestDiagnoseHTTPError 断言模型返回非 200 时报错而不是当成成功。
func TestDiagnoseHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, Model: "m", Timeout: 3 * time.Second}
	_, err := Diagnose(context.Background(), cfg, blockEvidence("a.example.com"))
	if err == nil {
		t.Fatal("401 应报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误里应带状态码，实际 %q", err.Error())
	}
}

// TestDiagnoseRequiresConfig 断言缺配置时明确报错。
func TestDiagnoseRequiresConfig(t *testing.T) {
	if _, err := Diagnose(context.Background(), LLMConfig{}, blockEvidence("a.com")); err == nil {
		t.Fatal("缺配置应报错")
	}
}
