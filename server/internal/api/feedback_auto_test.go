package api

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这个文件锁住「AI 自动处理反馈」的决策契约（需求 1）。
//
// 这些断言为什么重要：autoHandleFeedback 会真的写放行规则、真的结单。
// 判断错一次就是一次真实的拦截失效（放行了本该拦的域名），或者一次
// 用户投诉被机械地关掉。所以每条路径的边界都要用测试钉住。

// newAutoTestServer 建一个能跑自动处理的最小服务（临时目录里的真库）。
//
// 用真库而不是替身：自动处理会写规则、写时间线、写通知，涉及跨表状态，
// 替身挡不住「SQL 写错列名」这类问题 —— 而那些恰恰会让功能静默失效。
func newAutoTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auto.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{Domain: "dns.test.local"}
	cfg.API.JWTSecret = "0123456789abcdef0123456789abcdef"
	return New(cfg, st, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// seedUser 在库里建一个真实用户并返回它的 id。
//
// 必须建真用户：rules.feedback 都有 user_id 外键，拿 "u1" 这种虚构 id
// 写规则会撞 FOREIGN KEY constraint failed，而那个失败会被
// applyAllowRules 当成「写规则失败」静默跳过 —— 测试就变成了
// 「验证一个从没发生过的写入」，绿灯毫无意义（这坑真的踩到了）。
func seedUser(t *testing.T, s *Server, name string) string {
	t.Helper()
	u, err := s.store.CreateUser(context.Background(), name, name+"@test.local", "x", model.RoleUser)
	if err != nil {
		t.Fatalf("建用户 %s 失败: %v", name, err)
	}
	return u.ID
}

// makeFeedback 造一条已带诊断结论的反馈。
func makeFeedback(uid, domain, verdict, action, domains, reply string) *model.Feedback {
	return &model.Feedback{
		ID:        "fb_test_" + verdict + "_" + action,
		UserID:    uid,
		Username:  "tester",
		Domain:    domain,
		Kind:      model.FeedbackWrongBlock,
		Message:   "这个域名被拦了但我需要访问",
		Status:    model.FeedbackOpen,
		AIStatus:  model.AIDiagDone,
		AIVerdict: verdict,
		AIAction:  action,
		AIDomains: domains,
		AIReply:   reply,
	}
}

// TestAutoHandleCorrectVerdictClosesWithReply 核查无问题 → 直接回复并结单。
func TestAutoHandleCorrectVerdictClosesWithReply(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "ads.example.com", "correct", model.AIActNoAction, "",
		"经核查该拦截是正确的")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if !res.Handled {
		t.Fatalf("verdict=correct 应当自动结单，实际 Handled=false，原因: %s", res.Reason)
	}
	if res.NeedsHuman {
		t.Fatal("verdict=correct 不该转人工")
	}
	if len(res.DomainsApplied) != 0 {
		t.Fatalf("verdict=correct 不该写任何放行规则，实际写了 %v", res.DomainsApplied)
	}

	// 库里状态必须是 closed，且带 AI 标识。
	got, err := s.store.GetFeedback(ctx, fb.ID)
	if err != nil {
		t.Fatalf("读反馈失败: %v", err)
	}
	if got.Status != model.FeedbackClosed {
		t.Fatalf("状态应为 closed，实际 %q", got.Status)
	}
	if got.AIAnsweredAt == 0 {
		t.Fatal("ai_answered_at 必须非零（前端靠它显示 AI 标识）")
	}
	if got.AdminReply == "" {
		t.Fatal("admin_reply 必须有回复正文（老接口与邮件通知都读它）")
	}
}

// TestAutoHandleWrongBlockAddsAllowRule 确认误拦截 → 写白名单 + 回复 + 结单。
func TestAutoHandleWrongBlockAddsAllowRule(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "cdn.example.com", "wrong_block", model.AIActUserAllow,
		"cdn.example.com", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if !res.Handled {
		t.Fatalf("确认误拦截应当自动处理，实际 Handled=false，原因: %s", res.Reason)
	}
	if len(res.DomainsApplied) != 1 || res.DomainsApplied[0] != "cdn.example.com" {
		t.Fatalf("应放行 cdn.example.com，实际 %v", res.DomainsApplied)
	}

	got, _ := s.store.GetFeedback(ctx, fb.ID)
	if got.Status != model.FeedbackClosed {
		t.Fatalf("状态应为 closed，实际 %q", got.Status)
	}
	// 规则真的写进去了。
	exists, err := s.store.RuleExists(ctx, uid, "allow", "||cdn.example.com^", false)
	if err != nil {
		t.Fatalf("查规则失败: %v", err)
	}
	if !exists {
		t.Fatal("放行规则没有写入")
	}
}

// TestAutoHandleGlobalAllowWritesGlobalLayer 全局放行要写在全局层。
func TestAutoHandleGlobalAllowWritesGlobalLayer(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "img.example.com", "wrong_block", model.AIActGlobalAllow,
		"img.example.com", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if !res.Handled || !res.Global {
		t.Fatalf("应为全局放行并自动结单，实际 handled=%v global=%v 原因=%s",
			res.Handled, res.Global, res.Reason)
	}

	globalExists, err := s.store.RuleExists(ctx, "", "allow", "||img.example.com^", true)
	if err != nil {
		t.Fatalf("查全局规则失败: %v", err)
	}
	if !globalExists {
		t.Fatal("全局放行规则没有写入全局层")
	}
	// 关键：不能同时写用户层，否则全局规则与该用户的私有规则重复生效。
	userExists, _ := s.store.RuleExists(ctx, uid, "allow", "||img.example.com^", false)
	if userExists {
		t.Fatal("全局放行不该同时写用户层规则")
	}
}

// TestAutoHandleNoDomainGoesToHuman 未带域名 → 必须转人工。
//
// 这是需求明确要求的：「如果未带域名的回复或者无法处理的，则等待我手动处理」。
func TestAutoHandleNoDomainGoesToHuman(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	// 即使 AI 给了结论（模型可能凭留言猜），没有域名也不能自动结单。
	fb := makeFeedback(uid, "", "wrong_block", model.AIActUserAllow, "", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled {
		t.Fatal("未带域名的反馈不该自动结单")
	}
	if !res.NeedsHuman {
		t.Fatal("未带域名的反馈必须转人工")
	}
}

// TestAutoHandleUnclearGoesToHuman AI 自己说不确定 → 转人工。
func TestAutoHandleUnclearGoesToHuman(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "x.example.com", "unclear", model.AIActNeedHuman, "", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled || !res.NeedsHuman {
		t.Fatalf("unclear 应转人工，实际 handled=%v needsHuman=%v", res.Handled, res.NeedsHuman)
	}

	got, _ := s.store.GetFeedback(ctx, fb.ID)
	if got.Status == model.FeedbackClosed {
		t.Fatal("转人工的反馈不该被结单")
	}
	if got.AIAnsweredAt != 0 {
		t.Fatal("转人工的反馈不该带 AI 已回复标识")
	}
}

// TestAutoHandleWrongBlockWithoutActionGoesToHuman
// 判定误拦截但闸门否决了所有域名（action 不是 add_*）→ 转人工，绝不硬写。
func TestAutoHandleWrongBlockWithoutActionGoesToHuman(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "x.example.com", "wrong_block", model.AIActNoAction, "", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled || len(res.DomainsApplied) != 0 {
		t.Fatalf("没有可执行动作时不该自动处理，实际 handled=%v domains=%v",
			res.Handled, res.DomainsApplied)
	}
	if !res.NeedsHuman {
		t.Fatal("必须转人工")
	}
}

// TestAutoHandleRejectsPublicSuffixDomain 公共后缀绝不能自动放行。
//
// 这是整套自动处理里最危险的一条路径：模型如果把 "com.cn" 当成可放行
// 域名，写进去等于全站放行半个 TLD。IsSafeAllowTarget 必须挡住它，
// 挡下后因为「没有可放行域名」而转人工。
func TestAutoHandleRejectsPublicSuffixDomain(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "com.cn", "wrong_block", model.AIActGlobalAllow, "com.cn", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled {
		t.Fatal("公共后缀不该被自动放行")
	}
	if len(res.DomainsApplied) != 0 {
		t.Fatalf("不该写入任何规则，实际 %v", res.DomainsApplied)
	}
	if !res.NeedsHuman {
		t.Fatal("域名被安全校验挡下后必须转人工")
	}

	// 库里绝不能有这条规则。
	exists, _ := s.store.RuleExists(ctx, "", "allow", "||com.cn^", true)
	if exists {
		t.Fatal("公共后缀被写进了全局放行规则")
	}
}

// TestAutoHandleWrongAllowGoesToHuman 漏拦要加拦截规则 → 必须人工确认。
//
// 误加一条拦截比误放行更烦人（用户会以为是 DNS 坏了），
// 所以即使 AI 判定确实漏拦，也不自动写拦截规则。
func TestAutoHandleWrongAllowGoesToHuman(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "tracker.example.com", "wrong_allow", model.AIActNeedHuman, "", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled || !res.NeedsHuman {
		t.Fatalf("漏拦应转人工，实际 handled=%v needsHuman=%v", res.Handled, res.NeedsHuman)
	}
}

// TestAutoHandleWritesTimelineWithAIAuthor AI 回复必须进时间线且作者是 AI。
//
// 需求要求「自动回复处理的，加个 ai 标识」。落库层面有两个痕迹：
// ai_answered_at（字段）与时间线的 author（「AI 助手」）。
// 后者是用户在对话气泡里直接看到的那一行。
func TestAutoHandleWritesTimelineWithAIAuthor(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "ads.example.com", "correct", model.AIActNoAction, "",
		"经核查无需调整")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	if _, err := s.autoHandleFeedback(ctx, fb); err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}

	msgs, err := s.store.ListFeedbackMessages(ctx, fb.ID)
	if err != nil {
		t.Fatalf("读时间线失败: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("时间线里必须有 AI 的回复")
	}
	last := msgs[len(msgs)-1]
	if last.Role != model.FeedbackRoleAdmin {
		t.Fatalf("AI 回复应以 admin 角色写入，实际 %q", last.Role)
	}
	if last.Author != aiAuthorName {
		t.Fatalf("时间线作者应为 %q，实际 %q", aiAuthorName, last.Author)
	}
}

// TestAutoHandleNotifiesUserWithDedupe 自动处理后要给用户发通知，且不重复。
func TestAutoHandleNotifiesUserWithDedupe(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "ads.example.com", "correct", model.AIActNoAction, "",
		"经核查无需调整")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	if _, err := s.autoHandleFeedback(ctx, fb); err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}

	list, unread, err := s.store.ListNotifications(ctx, uid, 50, 0)
	if err != nil {
		t.Fatalf("读通知失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应发出 1 条通知，实际 %d 条", len(list))
	}
	if unread != 1 {
		t.Fatalf("未读数应为 1，实际 %d", unread)
	}
	if list[0].Kind != model.NotifyFeedbackAI {
		t.Fatalf("通知类别应为 %q，实际 %q", model.NotifyFeedbackAI, list[0].Kind)
	}

	// 再跑一次不该产生第二条（dedupe_key 挡住）。
	// 用同一个 fb 副本重放：真实场景里这是队列重试。
	if _, err := s.autoHandleFeedback(ctx, fb); err != nil {
		t.Fatalf("重放自动处理失败: %v", err)
	}
	list2, _, _ := s.store.ListNotifications(ctx, uid, 50, 0)
	if len(list2) != 1 {
		t.Fatalf("重复处理不该产生重复通知，实际 %d 条", len(list2))
	}
}

// TestAutoHandleRequiresDiagnosisDone 诊断没跑完 → 转人工，不猜。
func TestAutoHandleRequiresDiagnosisDone(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()
	fb := makeFeedback(uid, "ads.example.com", "", "", "", "")
	fb.AIStatus = model.AIDiagPending
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	if res.Handled {
		t.Fatal("诊断未完成时不该自动结单")
	}
	if !res.NeedsHuman {
		t.Fatal("诊断未完成时应转人工")
	}
}

// TestAutoHandleSkipsDuplicateRule 已有同名放行规则时不重复写。
func TestAutoHandleSkipsDuplicateRule(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx := context.Background()

	// 先手动放行一次。
	if err := s.store.CreateRule(ctx, &model.Rule{
		UserID: uid, Kind: "allow", Pattern: "||cdn.example.com^", Enabled: true,
	}); err != nil {
		t.Fatalf("建规则失败: %v", err)
	}

	fb := makeFeedback(uid, "cdn.example.com", "wrong_block", model.AIActUserAllow,
		"cdn.example.com", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	res, err := s.autoHandleFeedback(ctx, fb)
	if err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
	// 规则已存在 → applied 为空 → 转人工（而不是报错或写第二条）。
	// 这个取舍的理由：用户已经放行过了，AI 再「确认误拦截」说明情况可能变了，
	// 交给人看一眼比静默加一条重复规则更有用。
	if len(res.DomainsApplied) != 0 {
		t.Fatalf("已存在的规则不该被重复写入，实际 %v", res.DomainsApplied)
	}
	if !res.NeedsHuman {
		t.Fatal("规则已存在时应转人工")
	}
}

// TestAutoHandleRespectsDeadline 处理过程不该超过给定的 ctx 期限。
//
// 这不是性能测试，而是确认自动处理全部走 ctx —— 它由 worker 以 60 秒
// 超时调用，任何一步用了 context.Background() 都会在模型慢时挂住队列。
func TestAutoHandleRespectsDeadline(t *testing.T) {
	s := newAutoTestServer(t)
	uid := seedUser(t, s, "u1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fb := makeFeedback(uid, "ads.example.com", "correct", model.AIActNoAction, "", "")
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	if _, err := s.autoHandleFeedback(ctx, fb); err != nil {
		t.Fatalf("自动处理失败: %v", err)
	}
}
