package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/diagnose"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件是「AI 自动处理反馈」的执行器。
//
// 需求原文：「优化一下 ai 反馈处理功能，误拦截或者没有拦截的，ai 如果识别出
// 没有问题，则直接回复并结单，如果确实是误拦截则进行添加白名单，并回复结单，
// 如果未带域名的回复或者无法处理的，则等待我手动处理。自动回复处理的，加个 ai 标识」
//
// 对应到诊断结论上的三条路径：
//
//	verdict=correct   → 拦截是对的、或用户报的「该拦没拦」其实没问题
//	                    → 直接回复「经核查无需调整」并结单
//	verdict=wrong_block + action=add_*_allow
//	                    → 真的是误拦截 → 写白名单规则 + 回复 + 结单
//	其余（unclear / need_human / 无域名 / 闸门全否决）
//	                    → 不动，转人工，打上 needs_human 标记
//
// **为什么必须有闸门而不是直接信模型**：
// 放行规则一旦写进去就是全站或该用户的长期行为，模型判错一次就是一次
// 真实的拦截失效。diagnose.Guard 会挡掉公共后缀、非法域名、以及「证据里
// 根本没被拦」的域名 —— 这些校验不通过就绝不落规则，宁可转人工。

// autoHandleResult 是一次自动处理的结果，用于日志与测试断言。
type autoHandleResult struct {
	// Handled 是否已经自动结单（回复已发给用户）。
	Handled bool
	// Reply 发给用户的回复正文。
	Reply string
	// DomainsApplied 实际写入放行规则的域名。
	DomainsApplied []string
	// Global 白名单是否写在全局层。
	Global bool
	// NeedsHuman 是否转人工。
	NeedsHuman bool
	// Reason 决策依据（写审计日志）。
	Reason string
}

// autoHandleFeedback 尝试自动处理一条已诊断完的反馈。
//
// 调用前 fb 必须已经带有 AI 诊断结果（AIVerdict / AIAction / AIReply / AIDomains）。
// 返回 Handled=false 时调用方应把这条反馈留给人工。
func (s *Server) autoHandleFeedback(ctx context.Context, fb *model.Feedback) (*autoHandleResult, error) {
	res := &autoHandleResult{}

	// ── 门槛 1：没有结论，一定转人工 ──
	if fb.AIStatus != model.AIDiagDone {
		res.NeedsHuman = true
		res.Reason = "AI 诊断未完成"
		return res, nil
	}

	// ── 门槛 2：模型自己说不确定 / 要求人工 ──
	if fb.AIVerdict == "unclear" || fb.AIAction == model.AIActNeedHuman {
		res.NeedsHuman = true
		res.Reason = "AI 判断需要人工确认（verdict=" + fb.AIVerdict + "）"
		return res, nil
	}

	// ── 门槛 3：没有带域名，无法定位问题 ──
	// 用户只说「上不了网」而不给域名，AI 即使给了结论也无从验证 ——
	// 这种情况下回复用户「请提供具体域名」比结单更有用，所以也转人工。
	if strings.TrimSpace(fb.Domain) == "" {
		res.NeedsHuman = true
		res.Reason = "反馈未带域名，无法定位"
		return res, nil
	}

	switch {
	// ── 路径 A：核查无问题（拦截是对的 / 该拦的确实拦了）──
	case fb.AIVerdict == "correct":
		reply := strings.TrimSpace(fb.AIReply)
		if reply == "" {
			reply = defaultCorrectReply(fb.Domain)
		}
		res.Handled = true
		res.Reply = reply
		res.Reason = "AI 核查后确认当前拦截行为正确"
		return s.finishAutoHandle(ctx, fb, res)

	// ── 路径 B：确实是误拦截，需要放行 ──
	case fb.AIVerdict == "wrong_block":
		if fb.AIAction != model.AIActGlobalAllow && fb.AIAction != model.AIActUserAllow {
			// 误拦截但 AI 没给可执行动作（闸门可能全否决了）→ 转人工。
			res.NeedsHuman = true
			res.Reason = "AI 判定误拦截但没有可执行的放行动作（action=" + fb.AIAction + "）"
			return res, nil
		}
		global := fb.AIAction == model.AIActGlobalAllow
		applied, err := s.applyAllowRules(ctx, fb, global)
		if err != nil {
			return nil, err
		}
		if len(applied) == 0 {
			// 域名全被安全校验挡下 → 转人工，绝不硬写。
			res.NeedsHuman = true
			res.Reason = "建议的域名未通过安全校验"
			return res, nil
		}
		reply := strings.TrimSpace(fb.AIReply)
		if reply == "" {
			reply = defaultWrongBlockReply(applied)
		}
		res.Handled = true
		res.Reply = reply
		res.DomainsApplied = applied
		res.Global = global
		res.Reason = fmt.Sprintf("AI 判定误拦截，已放行 %s", strings.Join(applied, ", "))
		return s.finishAutoHandle(ctx, fb, res)

	// ── 路径 C：该拦没拦（wrong_allow）──
	// 「没有拦截」的反馈，AI 判定 correct 表示「本来就该拦，已拦住了」；
	// 如果判定确实漏拦，需要新增拦截规则 —— 但这会直接影响该用户之外的
	// 判断，且误加一条拦截的后果比误放行更烦人，所以一律转人工。
	case fb.AIVerdict == "wrong_allow":
		res.NeedsHuman = true
		res.Reason = "AI 判定确实漏拦，拦截规则需人工确认"
		return res, nil

	default:
		res.NeedsHuman = true
		res.Reason = "未知 verdict: " + fb.AIVerdict
		return res, nil
	}
}

// finishAutoHandle 把自动回复写进时间线、置为已回复，并通知用户。
//
// 回复以 **admin 角色**写入时间线，而不是新造一个 role=ai：
//   - 前端的时间线气泡只有 user / admin 两种样式，多一个角色要改渲染层；
//   - Email 通知、列表摘要等现有逻辑都按 admin 取「最近一次回复」；
//   - 「这是 AI 发的」通过 AISender 标记体现（见下），不必靠 role 区分。
//
// 但**必须留下 AI 标识**（需求明确要求）：这里同时
//   - 把 AIReply 落进 admin_reply（老接口、邮件通知都读它）；
//   - 设置 ai_answered_at（前端据此渲染「AI 自动处理」徽章）。
func (s *Server) finishAutoHandle(ctx context.Context, fb *model.Feedback, res *autoHandleResult) (*autoHandleResult, error) {
	now := time.Now().Unix()

	if err := s.store.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		FeedbackID: fb.ID,
		Role:       model.FeedbackRoleAdmin,
		Author:     aiAuthorName,
		Content:    res.Reply,
	}); err != nil {
		return nil, err
	}

	if err := s.store.UpdateFeedback(ctx, fb.ID, model.FeedbackClosed, res.Reply); err != nil {
		return nil, err
	}
	if err := s.store.MarkFeedbackAIAnswered(ctx, fb.ID, now); err != nil {
		s.log.Warn("标记 AI 已回复失败", "feedback", fb.ID, "err", err)
	}
	// 自动结单了就不该再占着人工队列。
	if err := s.store.ClearNeedsHuman(ctx, fb.ID); err != nil {
		s.log.Warn("清除需人工标记失败", "feedback", fb.ID, "err", err)
	}

	// 用户端通知：让他在「通知」页看到处理结果，而不是只能翻反馈列表。
	n := &model.Notification{
		UserID:    fb.UserID,
		Kind:      model.NotifyFeedbackAI,
		Title:     "反馈已由 AI 自动处理",
		Body:      res.Reply,
		Link:      "/app/feedback",
		RefID:     fb.ID,
		DedupeKey: "fb-ai:" + fb.ID,
	}
	if _, err := s.store.CreateNotification(ctx, n); err != nil {
		s.log.Warn("发送反馈处理通知失败", "feedback", fb.ID, "err", err)
	}

	s.log.Info("反馈已自动处理",
		"feedback", fb.ID, "verdict", fb.AIVerdict,
		"action", fb.AIAction, "domains", res.DomainsApplied, "reason", res.Reason)
	return res, nil
}

// aiAuthorName 是 AI 自动回复在时间线里显示的发言人。
//
// 用「AI 助手」而不是「管理员」：用户看到是 AI 判的要清楚，
// 否则他追问时不知道该问谁、也不理解为什么几秒钟就有回复。
const aiAuthorName = "AI 助手"

// applyAllowRules 把 AI 建议的域名写成放行规则，返回真正写入的域名。
//
// 三重校验，任一不通过就跳过该域名：
//  1. 形态校验（diagnose.IsSafeAllowTarget）：挡公共后缀、非法域名；
//  2. 不重复写：已有同名同层的放行规则就跳过，避免规则越堆越多；
//  3. 逐条独立成败：一条失败不影响其余，全失败由调用方转人工。
func (s *Server) applyAllowRules(ctx context.Context, fb *model.Feedback, global bool) ([]string, error) {
	domains := splitCommaList(fb.AIDomains)
	if len(domains) == 0 {
		// AI 没给域名时，退回到用户报的那个域名 —— 但只在它通得过校验时用。
		if d := strings.TrimSpace(fb.Domain); d != "" && diagnose.IsSafeAllowTarget(d) {
			domains = []string{d}
		}
	}

	applied := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !diagnose.IsSafeAllowTarget(d) {
			s.log.Warn("拒绝自动放行的域名", "feedback", fb.ID, "domain", d)
			continue
		}
		pattern := "||" + d + "^"

		// 已有等价放行规则就不重复写。
		if exists, err := s.store.RuleExists(ctx, fb.UserID, "allow", pattern, global); err == nil && exists {
			s.log.Info("放行规则已存在，跳过", "feedback", fb.ID, "domain", d, "global", global)
			continue
		}

		rule := &model.Rule{
			UserID:   fb.UserID,
			Kind:     "allow",
			Pattern:  pattern,
			Enabled:  true,
			IsGlobal: global,
		}
		if err := s.store.CreateRule(ctx, rule); err != nil {
			s.log.Warn("写入自动放行规则失败", "feedback", fb.ID, "domain", d, "err", err)
			continue
		}
		applied = append(applied, d)
	}

	if len(applied) > 0 {
		// 规则变了要重建快照，否则 dnsd 不会生效。
		if err := s.touched(ctx, fb.UserID, global); err != nil {
			s.log.Warn("自动放行后重建快照失败", "feedback", fb.ID, "err", err)
		}
	}
	return applied, nil
}

// defaultCorrectReply 是「核查后没问题」的兜底回复。
func defaultCorrectReply(domain string) string {
	return fmt.Sprintf("已核查 %s 的拦截记录：当前拦截行为是正确的，属于该域名本身的追踪/广告用途，不需要调整。如仍有访问问题，请补充具体现象（哪个 App、哪一步失败）后再反馈。", domain)
}

// defaultWrongBlockReply 是「已放行」的兜底回复。
func defaultWrongBlockReply(domains []string) string {
	return fmt.Sprintf("已确认这是误拦截，已为 %s 添加放行规则，请重连网络后重试（若无效可清除本地 DNS 缓存或等待几分钟生效）。", strings.Join(domains, "、"))
}
