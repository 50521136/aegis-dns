package diagnose

import (
	"context"
	"errors"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件用假数据源验证「证据收集」的确定性部分。
//
// 不依赖 sqlite、不调用模型：证据收集是整个 Agent 的地基，
// 它错了后面全错，所以必须能单独测。

// fakeSource 是 Source 的替身。
type fakeSource struct {
	userRules   []model.Rule
	globalRules []model.Rule
	userBlock   []string
	userAllow   []string
	gBlock      []string
	gAllow      []string
	sources     map[string][]SubSource
	queries     []QuerySample
	blocked     int
	enabled     bool
	errOnRules  error
}

func (f *fakeSource) UserRules(context.Context, string) ([]model.Rule, error) {
	return f.userRules, f.errOnRules
}
func (f *fakeSource) GlobalRules(context.Context) ([]model.Rule, error) {
	return f.globalRules, nil
}
func (f *fakeSource) UserSubDomains(context.Context, string) ([]string, []string, error) {
	return f.userBlock, f.userAllow, nil
}
func (f *fakeSource) GlobalSubDomains(context.Context) ([]string, []string, error) {
	return f.gBlock, f.gAllow, nil
}
func (f *fakeSource) SubSourcesOf(context.Context, []string) (map[string][]SubSource, error) {
	return f.sources, nil
}
func (f *fakeSource) RecentQueries(context.Context, string, string, int) ([]QuerySample, int, error) {
	return f.queries, f.blocked, nil
}
func (f *fakeSource) UserEnabled(context.Context, string) (bool, error) {
	return f.enabled, nil
}

// TestCollectUserAllowWins 断言「用户放行」优先于「订阅黑名单」。
//
// 这是线上真实场景：用户自己加了 allow，但订阅里有同名 block，
// 用户以为「加了白名单没用」。证据必须如实反映最终判定是 allow。
func TestCollectUserAllowWins(t *testing.T) {
	src := &fakeSource{
		userRules: []model.Rule{
			{Kind: "allow", Pattern: "||github.com^", Enabled: true},
		},
		userBlock: []string{"github.com", "raw.githubusercontent.com"},
		enabled:   true,
	}
	ev, err := Collect(context.Background(), src, model.Feedback{
		ID: "fb1", UserID: "u1", Kind: model.FeedbackWrongBlock,
		Domain: "github.com", Message: "github 打不开",
	}, 3600)
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	if len(ev.Domains) == 0 {
		t.Fatal("没有收集到任何域名")
	}
	d := ev.Domains[0]
	if d.Domain != "github.com" {
		t.Fatalf("域名应被正确抽取，实际 %q", d.Domain)
	}
	if d.Action != "allow" {
		t.Fatalf("用户 allow 应优先于订阅 block，实际 action=%q layer=%q", d.Action, d.Layer)
	}
	if d.Layer != "user_allow" {
		t.Fatalf("命中层应是 user_allow，实际 %q", d.Layer)
	}
}

// TestCollectSubscriptionBlock 断言只有订阅拦截时如实标为 block 并带出来源。
func TestCollectSubscriptionBlock(t *testing.T) {
	src := &fakeSource{
		userBlock: []string{"t.apkpure.net"},
		sources: map[string][]SubSource{
			"t.apkpure.net": {{SubID: "sub1", Name: "公共广告列表", Kind: "block"}},
		},
		queries: []QuerySample{{Action: "blocked", RCode: "NOERROR"}},
		blocked: 3,
		enabled: true,
	}
	ev, err := Collect(context.Background(), src, model.Feedback{
		ID: "fb2", UserID: "u1", Kind: model.FeedbackWrongBlock,
		Domain: "t.apkpure.net", Message: "apkpure 转圈",
	}, 3600)
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	d := ev.Domains[0]
	if d.Action != "block" {
		t.Fatalf("应判为 block，实际 %q", d.Action)
	}
	if d.Layer != "sub_block" {
		t.Fatalf("命中层应是 sub_block，实际 %q", d.Layer)
	}
	if len(d.SubSources) != 1 || d.SubSources[0].Name != "公共广告列表" {
		t.Fatalf("应带出订阅来源，实际 %+v", d.SubSources)
	}
	if d.BlockedRecently != 3 {
		t.Fatalf("应带出近期拦截次数 3，实际 %d", d.BlockedRecently)
	}
}

// TestCollectGlobalAllowLayer 断言全局放行层也被如实识别。
func TestCollectGlobalAllowLayer(t *testing.T) {
	src := &fakeSource{
		globalRules: []model.Rule{
			{Kind: "allow", Pattern: "||api.miui.com^", Enabled: true},
		},
		userBlock: []string{"api.miui.com"},
		enabled:   true,
	}
	ev, err := Collect(context.Background(), src, model.Feedback{
		ID: "fb3", UserID: "u1", Kind: model.FeedbackWrongBlock, Domain: "api.miui.com",
	}, 3600)
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	d := ev.Domains[0]
	if d.Action != "allow" || d.Layer != "global_allow" {
		t.Fatalf("全局放行应生效，实际 action=%q layer=%q", d.Action, d.Layer)
	}
	if d.MatchedRule != "||api.miui.com^" {
		t.Fatalf("应回填命中的规则原文，实际 %q", d.MatchedRule)
	}
}

// TestCollectNoneAction 断言没命中任何规则时如实写 none（不是 block）。
//
// 这条很重要：模型看到 action=none 就该说「规则没拦你」，而不是编造拦截。
func TestCollectNoneAction(t *testing.T) {
	src := &fakeSource{enabled: true}
	ev, err := Collect(context.Background(), src, model.Feedback{
		ID: "fb4", UserID: "u1", Kind: model.FeedbackWrongBlock, Domain: "unmatched.example.org",
	}, 3600)
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	d := ev.Domains[0]
	if d.Action != "none" {
		t.Fatalf("未命中应写 none，实际 %q", d.Action)
	}
	if d.LayerLabel == "" {
		t.Fatal("layer_label 不能为空，否则模型看不到人话说明")
	}
}

// TestCollectPropagatesError 断言读取规则失败时返回错误而不是静默给空证据。
//
// 静默给空证据的后果：模型对着「什么都没命中」的证据说「规则没问题」，
// 管理员据此结案，而真正的原因根本没被看到。
func TestCollectPropagatesError(t *testing.T) {
	src := &fakeSource{errOnRules: errors.New("db is locked")}
	_, err := Collect(context.Background(), src, model.Feedback{ID: "fb5", UserID: "u1"}, 3600)
	if err == nil {
		t.Fatal("读取规则失败时必须返回错误，不能静默继续")
	}
}
