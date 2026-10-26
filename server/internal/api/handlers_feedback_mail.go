package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/mail"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// 反馈邮件通知。
//
// 两条独立的通知：
//   1. 用户提交新反馈 → 提醒管理员（反馈是报障的唯一入口，没人盯着面板
//      就会压一整天）；
//   2. 管理员回复 → 提醒提交者（用户不会主动回来看，没有提醒等于没回复）。
//
// 设计上的两条硬规则：
//   * **发信失败绝不影响业务**。反馈已经落库了，邮件只是提醒；这里所有
//     错误只写日志。反过来（发信失败就让提交返回 500）会让 SMTP 一抖动
//     整个反馈功能不可用。
//   * **没配 SMTP 就静默跳过**。多数自建实例不开邮件，每次提交打一条
//     "未启用" 的 WARN 只会把日志刷满。

// feedbackNotifyRecipients 算出新反馈提醒该发给谁。
//
// 优先用管理员显式配置的地址；留空时自动取所有 role=admin 账号的邮箱 ——
// 多数部署只有一个管理员，让他再去设置页填一遍自己的邮箱纯属多余。
func (s *Server) feedbackNotifyRecipients(ctx context.Context, set *model.GlobalSettings) []string {
	if raw := strings.TrimSpace(set.FeedbackNotifyEmail); raw != "" {
		out := make([]string, 0, 4)
		for _, part := range strings.Split(raw, ",") {
			addr := strings.TrimSpace(part)
			if looksLikeEmail(addr) {
				out = append(out, addr)
			}
		}
		if len(out) > 0 {
			return out
		}
		// 配置了但全是非法地址：继续往下走自动兜底，而不是「一条都不发」——
		// 管理员填错了不该导致彻底收不到提醒。
		s.log.Warn("反馈提醒收件地址全部非法，回退到管理员账号邮箱", "raw", raw)
	}

	admins, err := s.store.ListUsersAll(ctx)
	if err != nil {
		s.log.Warn("读取管理员邮箱失败", "err", err)
		return nil
	}
	out := make([]string, 0, 2)
	for _, u := range admins {
		if u.Role != model.RoleAdmin {
			continue
		}
		if looksLikeEmail(u.Email) {
			out = append(out, u.Email)
		}
	}
	return out
}

// notifyFeedbackNew 在新反馈提交后提醒管理员。
func (s *Server) notifyFeedbackNew(ctx context.Context, f *model.Feedback) {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		s.log.Warn("读取设置失败，跳过反馈邮件提醒", "err", err)
		return
	}
	if !set.FeedbackNotifyEnabled || !set.SMTPEnabled {
		return
	}
	emailDomain := set.Domain
	if emailDomain == "" {
		emailDomain = s.cfg.Domain
	}
	to := s.feedbackNotifyRecipients(ctx, set)
	if len(to) == 0 {
		return
	}

	kindLabel := map[string]string{
		model.FeedbackWrongBlock: "误拦了",
		model.FeedbackWrongAllow: "该拦没拦",
		model.FeedbackOther:      "其他",
	}[f.Kind]
	if kindLabel == "" {
		kindLabel = f.Kind
	}

	subject := fmt.Sprintf("【%s】新反馈：%s", emailDomain, kindLabel)
	if f.Domain != "" {
		subject = fmt.Sprintf("【%s】新反馈：%s · %s", emailDomain, kindLabel, f.Domain)
	}

	var sb strings.Builder
	sb.WriteString("收到一条新的用户反馈。\n\n")
	sb.WriteString("提交者：" + f.Username + "\n")
	sb.WriteString("类型：" + kindLabel + "\n")
	if f.Domain != "" {
		sb.WriteString("域名：" + f.Domain + "\n")
	}
	sb.WriteString("时间：" + rfc3339OrEmpty(f.CreatedAt) + "\n\n")
	sb.WriteString("正文：\n" + f.Message + "\n\n")
	sb.WriteString("在「管理后台 → 反馈处理」里回复。\n")

	htmlBody := mail.FeedbackEmail(mail.FeedbackNotice{
		Title:   "新反馈",
		Domain:  emailDomain,
		Kind:    kindLabel,
		Author:  f.Username,
		Target:  f.Domain,
		Time:    rfc3339OrEmpty(f.CreatedAt),
		Content: f.Message,
		Hint:    "在「管理后台 → 反馈处理」里回复。",
	})

	cfg := mailConfig(set)
	for _, addr := range to {
		if err := mail.Send(cfg, mail.Message{
			To: addr, Subject: subject, Body: sb.String(), HTML: htmlBody,
		}); err != nil {
			s.log.Warn("发送反馈提醒失败", "to", addr, "feedback", f.ID, "err", err)
		}
	}
}

// notifyFeedbackReply 在管理员回复后提醒提交者。
func (s *Server) notifyFeedbackReply(ctx context.Context, f *model.Feedback, reply string) {
	if strings.TrimSpace(reply) == "" {
		return
	}
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		s.log.Warn("读取设置失败，跳过反馈回复邮件", "err", err)
		return
	}
	if !set.FeedbackNotifyEnabled || !set.FeedbackNotifyUserReply || !set.SMTPEnabled {
		return
	}

	emailDomain := set.Domain
	if emailDomain == "" {
		emailDomain = s.cfg.Domain
	}
	u, err := s.store.GetUser(ctx, f.UserID)
	if err != nil || u == nil || !looksLikeEmail(u.Email) {
		// 用户没绑邮箱就不发。这里不打 WARN —— 相当一部分账号本来就没邮箱，
		// 每次都记一条会把日志噪音化。
		return
	}

	subject := fmt.Sprintf("【%s】你的反馈已回复", emailDomain)
	htmlBody := mail.FeedbackEmail(mail.FeedbackNotice{
		Title:   "你的反馈已回复",
		Domain:  emailDomain,
		Kind:    "反馈回复",
		Author:  "管理员",
		Target:  f.Domain,
		Time:    rfc3339OrEmpty(f.UpdatedAt),
		Content: reply,
		Hint:    "登录面板在「反馈」页可以看到完整对话，也可以继续追问。",
	})

	var sb strings.Builder
	sb.WriteString("你的反馈已收到回复。\n\n")
	if f.Domain != "" {
		sb.WriteString("域名：" + f.Domain + "\n")
	}
	sb.WriteString("原问题：\n" + f.Message + "\n\n")
	sb.WriteString("管理员回复：\n" + reply + "\n\n")
	sb.WriteString("登录面板在「反馈」页可以看到完整对话，也可以继续追问。\n")

	if err := mail.Send(mailConfig(set), mail.Message{
		To: u.Email, Subject: subject, Body: sb.String(), HTML: htmlBody,
	}); err != nil {
		s.log.Warn("发送反馈回复邮件失败", "to", u.Email, "feedback", f.ID, "err", err)
	}
}
