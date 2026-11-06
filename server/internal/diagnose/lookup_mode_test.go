package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件钉住「填了域名优先按域名查 / 没填域名按内容查且只给建议」。
//
// 用户原话：「如果填写了域名则优先以域名去查问题，如果没写域名，
// 则根据用户反馈的内容去进行问题查询，但此时不要直接结单和处理，
// 给建议就行，等我处理」。
//
// 两个最容易回退的点：
//  1. 把正文里顺带提到的域名和用户指认的域名混成一锅，模型对着无关
//     域名下结论；
//  2. 用户没填域名时，模型凭空给一批「建议放行」域名，被自动写成规则。

// TestCollectExplicitDomainWins 断言显式域名排在最前并标记为 explicit。
func TestCollectExplicitDomainWins(t *testing.T) {
	src := &fakeSource{
		userRules: []model.Rule{},
		gBlock:    []string{},
		enabled:   true,
	}
	fb := model.Feedback{
		ID: "fb1", UserID: "u1",
		// 域名栏填的是主域名；正文里还提了一个 URL 与一个短域名。
		Domain:  "api.example.com",
		Message: "打开 https://cdn.other.com/a.png 时 api.example.com 也连不上",
	}
	ev, err := Collect(context.Background(), src, fb, 0)
	if err != nil {
		t.Fatalf("取证失败: %v", err)
	}
	if ev.Mode != LookupByDomain || !ev.HasExplicitDomain {
		t.Fatalf("应走 by_domain，实际 mode=%q explicit=%v", ev.Mode, ev.HasExplicitDomain)
	}
	if len(ev.Domains) == 0 || ev.Domains[0].Domain != "api.example.com" {
		t.Fatalf("用户填的域名必须排第一，实际 %+v", ev.Domains)
	}
	if ev.Domains[0].Source != DomainSourceExplicit {
		t.Fatalf("第一个域名应标为 explicit，实际 %q", ev.Domains[0].Source)
	}
	foundMessage := false
	for _, d := range ev.Domains[1:] {
		if d.Source == DomainSourceMessage {
			foundMessage = true
		}
	}
	if !foundMessage {
		t.Fatal("正文里提到的域名应作为 message 线索保留在后面")
	}
}

// TestCollectContentModeUsesRecentHits 断言没填域名、正文也没域名时，
// 用近期被拦截记录补候选，并标为 recent。
func TestCollectContentModeUsesRecentHits(t *testing.T) {
	src := &fakeSource{
		enabled: true,
		recentHits: []DomainHit{
			{Domain: "ads.bilibili.com", Hits: 42},
			{Domain: "track.example.net", Hits: 7},
		},
	}
	fb := model.Feedback{
		ID: "fb2", UserID: "u1",
		Domain:  "",
		Message: "哔哩哔哩开屏广告又出来了", // 纯中文，抽不出域名
	}
	ev, err := Collect(context.Background(), src, fb, 0)
	if err != nil {
		t.Fatalf("取证失败: %v", err)
	}
	if ev.Mode != LookupByContent || ev.HasExplicitDomain {
		t.Fatalf("应走 by_content，实际 mode=%q explicit=%v", ev.Mode, ev.HasExplicitDomain)
	}
	if len(ev.Domains) != 2 {
		t.Fatalf("应有两个候选域名，实际 %+v", ev.Domains)
	}
	for _, d := range ev.Domains {
		if d.Source != DomainSourceRecent {
			t.Fatalf("候选应标为 recent，实际 %q (%s)", d.Source, d.Domain)
		}
	}
	if !strings.Contains(ev.CandidateNote, "不是用户填写的域名") {
		t.Fatalf("证据里应说明候选来源，实际 %q", ev.CandidateNote)
	}
}

// TestCollectContentModePrefersMessageDomains 断言正文里写了域名时
// 不额外拉近期记录，直接按正文域名取证。
func TestCollectContentModePrefersMessageDomains(t *testing.T) {
	src := &fakeSource{
		enabled:    true,
		recentHits: []DomainHit{{Domain: "should-not-appear.example", Hits: 999}},
	}
	fb := model.Feedback{
		ID: "fb3", UserID: "u1", Domain: "",
		Message: "这个域名打不开 https://raw.githubusercontent.com/x/y.txt",
	}
	ev, err := Collect(context.Background(), src, fb, 0)
	if err != nil {
		t.Fatalf("取证失败: %v", err)
	}
	if ev.Mode != LookupByContent {
		t.Fatalf("应走 by_content，实际 %q", ev.Mode)
	}
	if len(ev.Domains) != 1 || ev.Domains[0].Domain != "raw.githubusercontent.com" {
		t.Fatalf("应只用正文域名，实际 %+v", ev.Domains)
	}
	if ev.Domains[0].Source != DomainSourceMessage {
		t.Fatalf("应标为 message，实际 %q", ev.Domains[0].Source)
	}
}

// contentEvidence 造一份「按内容查」的证据。
func contentEvidence() *Evidence {
	return &Evidence{
		FeedbackID: "fb4",
		Message:    "开屏广告又出现了",
		Mode:       LookupByContent,
		Domains: []DomainEvidence{
			{Domain: "ads.bilibili.com", Source: DomainSourceRecent, Action: "block", Layer: "sub_block"},
			{Domain: "track.example.net", Source: DomainSourceRecent, Action: "block", Layer: "sub_block"},
		},
	}
}

// TestSanitizeForcesSuggestOnly 断言即使模型不听话给了放行清单，
// 按内容查模式也会被强制收敛成「只给建议」。
func TestSanitizeForcesSuggestOnly(t *testing.T) {
	ev := contentEvidence()
	sug := &Suggestion{
		Verdict: "wrong_block",
		Action:  Action{Type: "add_global_allow", Domains: []string{"ads.bilibili.com"}},
		Reply:   "已为你放行",
		Candidates: []string{
			"ads.bilibili.com",           // 合法，在证据里
			"https://track.example.net/", // 合法，需归一化
			"invented.example.com",       // 不在证据里，应丢弃
			"ADS.BILIBILI.COM",           // 重复，应去重
			"com",                        // 公共后缀形态，应丢弃
		},
	}
	sanitizeSuggestion(sug, ev)
	if sug.Verdict != "unclear" || sug.Action.Type != "need_human" {
		t.Fatalf("应强制收敛成 unclear/need_human，实际 %+v", sug)
	}
	if len(sug.Action.Domains) != 0 || sug.Reply != "" {
		t.Fatalf("不应留下可执行的放行清单或自动回复，实际 %+v", sug)
	}
	want := []string{"ads.bilibili.com", "track.example.net"}
	if len(sug.Candidates) != len(want) {
		t.Fatalf("候选应为 %v，实际 %v", want, sug.Candidates)
	}
	for i := range want {
		if sug.Candidates[i] != want[i] {
			t.Fatalf("第 %d 个候选应为 %q，实际 %v", i, want[i], sug.Candidates)
		}
	}
}

// TestSanitizeByDomainKeepsAction 断言按域名查模式不被上面的收敛误伤。
func TestSanitizeByDomainKeepsAction(t *testing.T) {
	ev := &Evidence{Mode: LookupByDomain, Domains: []DomainEvidence{
		{Domain: "a.example.com", Source: DomainSourceExplicit, Action: "block"},
	}}
	sug := &Suggestion{
		Verdict: "wrong_block", Reply: "已放行",
		Action: Action{Type: "add_global_allow", Domains: []string{"a.example.com"}},
	}
	sanitizeSuggestion(sug, ev)
	if sug.Action.Type != "add_global_allow" || sug.Reply != "已放行" || sug.Verdict != "wrong_block" {
		t.Fatalf("按域名查的结论不该被改写，实际 %+v", sug)
	}
	if len(sug.Candidates) != 0 {
		t.Fatalf("按域名查不该有候选，实际 %v", sug.Candidates)
	}
}

// TestDiagnoseContentModePromptAndResult 端到端：系统提示词带「没有填写域名」
// 约束，且模型给的放行清单被收敛掉。
func TestDiagnoseContentModePromptAndResult(t *testing.T) {
	var gotSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			gotSystem = body.Messages[0].Content
		}
		// 故意不听话：给一个可执行放行清单。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"content": `{"verdict":"wrong_block","reason":"误拦","action":{"type":"add_global_allow","domains":["ads.bilibili.com"]},"reply":"已放行","candidates":["ads.bilibili.com"]}`},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, Model: "m", Timeout: 3 * time.Second, MaxTokens: 1000}
	sug, err := Diagnose(context.Background(), cfg, contentEvidence())
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if !strings.Contains(gotSystem, "没有填写域名") {
		t.Fatalf("按内容查的提示词没生效: %q", gotSystem[:min(200, len(gotSystem))])
	}
	if sug.Action.Type != "need_human" || len(sug.Action.Domains) != 0 {
		t.Fatalf("按内容查不该留下可执行的放行清单，实际 %+v", sug.Action)
	}
	if len(sug.Candidates) != 1 || sug.Candidates[0] != "ads.bilibili.com" {
		t.Fatalf("候选应保留证据里的域名，实际 %v", sug.Candidates)
	}
}

// TestParseSuggestionContentModeToleratesEmptyReply 断言模型没听话、
// 给了空 reply 时也不会把整条诊断判失败。
func TestParseSuggestionContentModeToleratesEmptyReply(t *testing.T) {
	raw := `{"verdict":"unclear","reason":"证据不足","action":{"type":"add_user_allow","domains":[]},"reply":""}`
	if _, err := parseSuggestion(raw, LookupByContent); err != nil {
		t.Fatalf("按内容查模式应容忍空 reply，实际: %v", err)
	}
	if _, err := parseSuggestion(raw, LookupByDomain); err == nil {
		t.Fatal("按域名查模式仍应拒绝空 reply")
	}
}
