package api

import (
	"net/http"
	"strings"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/mail"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// mailConfig 把全局设置转成发信配置。
func mailConfig(set *model.GlobalSettings) mail.Config {
	return mail.Config{
		Enabled:  set.SMTPEnabled,
		Host:     set.SMTPHost,
		Port:     set.SMTPPort,
		User:     set.SMTPUser,
		Pass:     set.SMTPPass,
		From:     set.SMTPFrom,
		FromName: set.SMTPFromName,
		TLS:      set.SMTPTLS,
	}
}

// handleSendEmailCode 发送邮箱验证码（注册验证 / 找回密码）。
//
// 无论邮箱是否已注册，返回都是同一句话：
// 否则这个接口就成了「批量探测哪些邮箱在本站注册过」的工具。
func (s *Server) handleSendEmailCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email   string `json:"email"`
		Purpose string `json:"purpose"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "邮箱格式不正确", nil)
		return
	}
	purpose := req.Purpose
	switch purpose {
	case store.EmailPurposeRegister, store.EmailPurposeReset, store.EmailPurposeChange:
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "purpose 只能是 register / reset / change", nil)
		return
	}

	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	cfg := mailConfig(set)
	if !cfg.Enabled {
		writeError(w, http.StatusServiceUnavailable, CodeFeatureDisabled, "本站未启用邮件服务", nil)
		return
	}

	// 注册验证要先看这个功能开没开
	if purpose == store.EmailPurposeRegister {
		if !emailAllowed(email, set.EmailDomainWhitelist) {
			writeError(w, http.StatusForbidden, CodeForbidden, "该邮箱后缀不在允许范围内", nil)
			return
		}
		if !set.EmailVerifyRegister {
			writeError(w, http.StatusBadRequest, CodeFeatureDisabled, "注册邮箱验证未开启，无需验证码", nil)
			return
		}
		if !set.AllowRegister {
			writeError(w, http.StatusForbidden, CodeForbidden, "当前已关闭公开注册", nil)
			return
		}
	}
	// 更换邮箱：必须是登录用户，且目标邮箱不能被别人占用。
	// 这个用途只能由登录态发起，所以用 currentUser 判断而不是只看邮件地址。
	if purpose == store.EmailPurposeChange {
		u := currentUser(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "请先登录", nil)
			return
		}
		if !set.AllowEmailChange {
			writeError(w, http.StatusForbidden, CodeFeatureDisabled, "本站未开放自助更换邮箱", nil)
			return
		}
		if !emailAllowed(email, set.EmailDomainWhitelist) {
			writeError(w, http.StatusForbidden, CodeForbidden, "该邮箱后缀不在允许范围内", nil)
			return
		}
		if other, _ := s.store.GetUserByEmail(r.Context(), email); other != nil && other.ID != u.ID {
			writeError(w, http.StatusConflict, CodeValidationFailed, "该邮箱已被其他账号使用", nil)
			return
		}
	}

	// 找回密码：邮箱必须存在，但对外不区分
	if purpose == store.EmailPurposeReset {
		if u, _ := s.store.GetUserByEmail(r.Context(), email); u == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"sent": true, "message": "如果该邮箱已注册，验证码已发出",
			})
			return
		}
	}

	code, err := s.store.IssueEmailCode(r.Context(), email, purpose)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, err.Error(), nil)
		return
	}

	subject, body := emailCodeContent(purpose, code, s.domain(r))
	if err := mail.Send(cfg, mail.Message{To: email, Subject: subject, Body: body}); err != nil {
		s.log.Warn("发送验证码邮件失败", "email", email, "purpose", purpose, "err", err)
		writeError(w, http.StatusBadGateway, CodeInternal, "邮件发送失败: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "message": "验证码已发出"})
}

// emailCodeContent 生成验证码邮件正文。
func emailCodeContent(purpose, code, domain string) (string, string) {
	var sb strings.Builder
	switch purpose {
	case store.EmailPurposeReset:
		sb.WriteString("你在 " + domain + " 申请重置密码。\n\n")
	case store.EmailPurposeChange:
		sb.WriteString("你在 " + domain + " 申请更换邮箱。\n\n")
	default:
		sb.WriteString("你在 " + domain + " 注册账号。\n\n")
	}
	sb.WriteString("验证码：" + code + "\n\n")
	sb.WriteString("有效期 10 分钟，请勿转发给他人。\n")
	sb.WriteString("如果这不是你本人的操作，忽略本邮件即可。\n")
	subject := "【" + domain + "】邮箱验证码"
	switch purpose {
	case store.EmailPurposeReset:
		subject = "【" + domain + "】重置密码验证码"
	case store.EmailPurposeChange:
		subject = "【" + domain + "】更换邮箱验证码"
	}
	return subject, sb.String()
}

// handleForgotPassword 通过邮箱验证码重置密码。
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "邮箱格式不正确", nil)
		return
	}
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.VerifyEmailCode(r.Context(), email, store.EmailPurposeReset, strings.TrimSpace(req.Code)); err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error(), nil)
		return
	}
	u, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil || u == nil {
		// 验证码校验已经通过，说明这个邮箱确实收到了信，
		// 走到这里只可能是账号刚被删除。
		writeError(w, http.StatusNotFound, CodeNotFound, "该邮箱对应的账号不存在", nil)
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), u.ID, hash); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 密码改了，已签发的会话必须全部作废 —— 否则「改密码踢掉别人」这件事不成立。
	if err := s.store.RevokeAllUserTokens(r.Context(), u.ID); err != nil {
		s.log.Warn("重置密码后吊销会话失败", "user", u.ID, "err", err)
	}
	s.log.Info("用户通过邮箱重置了密码", "user", u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTestEmail 发送一封测试邮件（管理员用）。
func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	to := strings.ToLower(strings.TrimSpace(req.To))
	if !looksLikeEmail(to) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "收件人邮箱格式不正确", nil)
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	cfg := mailConfig(set)
	domain := s.domain(r)
	if err := mail.Send(cfg, mail.Message{
		To:      to,
		Subject: "【" + domain + "】测试邮件",
		Body:    "这是一封测试邮件。\n\n如果你收到了它，说明 " + domain + " 的邮件服务配置正确。\n",
	}); err != nil {
		writeError(w, http.StatusBadGateway, CodeInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "测试邮件已发出"})
}

// looksLikeEmail 做最基本的邮箱格式判断。
//
// 不追求 RFC 完整性：真正的验证手段是「能不能收到验证码」，
// 正则写得再复杂也只是把明显错误的输入挡在发信之前。
func looksLikeEmail(s string) bool {
	if len(s) < 5 || len(s) > 254 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.Contains(s[at+1:], ".") == false {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return true
}

// handleChangeEmail 用新邮箱收到的验证码替换当前账号邮箱。
func (s *Server) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "邮箱格式不正确", nil)
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if !set.AllowEmailChange {
		writeError(w, http.StatusForbidden, CodeFeatureDisabled, "本站未开放自助更换邮箱", nil)
		return
	}
	if !emailAllowed(email, set.EmailDomainWhitelist) {
		writeError(w, http.StatusForbidden, CodeForbidden, "该邮箱后缀不在允许范围内", nil)
		return
	}
	if other, _ := s.store.GetUserByEmail(r.Context(), email); other != nil && other.ID != u.ID {
		writeError(w, http.StatusConflict, CodeValidationFailed, "该邮箱已被其他账号使用", nil)
		return
	}
	if err := s.store.VerifyEmailCode(r.Context(), email, store.EmailPurposeChange, strings.TrimSpace(req.Code)); err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.UpdateUserFields(r.Context(), u.ID, nil, nil, &email, nil, nil, nil); err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.log.Info("用户更换了邮箱", "user", u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email})
}
