package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 反馈正文与域名的长度上限，防止有人把整篇文档贴进来。
const (
	maxFeedbackMessage = 2000
	maxFeedbackDomain  = 253
)

func feedbackJSON(f *model.Feedback) map[string]any {
	m := map[string]any{
		"id":          f.ID,
		"user_id":     f.UserID,
		"username":    f.Username,
		"domain":      f.Domain,
		"kind":        f.Kind,
		"message":     f.Message,
		"status":      f.Status,
		"admin_reply": f.AdminReply,
		"created_at":  rfc3339OrEmpty(f.CreatedAt),
		"updated_at":  rfc3339OrEmpty(f.UpdatedAt),
	}
	// AI 诊断字段必须一起输出。
	//
	// 漏掉它们的后果不是「少显示一块」，而是**整个功能看起来坏了**：
	// 前端按 ai_status 决定渲染「AI 诊断」按钮还是结论卡片，
	// 字段缺失时永远读到空值，于是点完按钮界面纹丝不动 ——
	// 而模型其实已经跑完、结论就在库里。
	// （这个坑真实发生过：诊断成功 3 次，界面上一次都没反应。）
	m["ai_status"] = f.AIStatus
	m["ai_verdict"] = f.AIVerdict
	m["ai_reason"] = f.AIReason
	m["ai_action"] = f.AIAction
	m["ai_reply"] = f.AIReply
	m["ai_domains"] = f.AIDomains
	m["ai_guard_notes"] = f.AIGuardNotes
	m["ai_model"] = f.AIModel
	m["ai_ran_at"] = f.AIRanAt
	// ai_evidence 是给审计看的原始证据，体积可能几百字节到几 KB，
	// 列表里不需要，只在需要时通过诊断详情获取，避免把列表撑大。

	// 时间线：一条反馈一串消息，用户与管理员往返都在里面。
	//
	// 此前只有单个 admin_reply 字段，用户想追问**只能再提交一条新反馈** ——
	// 列表里于是出现「同一个问题的第 2 条、第 3 条」，谁看都乱。
	// 现在追问走 POST /me/feedback/{id}/reply，追加到同一条时间线上。
	msgs := make([]map[string]any, 0, len(f.Messages))
	for _, msg := range f.Messages {
		msgs = append(msgs, map[string]any{
			"id":          msg.ID,
			"feedback_id": msg.FeedbackID,
			"role":        msg.Role,
			"author":      msg.Author,
			"content":     msg.Content,
			"created_at":  rfc3339OrEmpty(msg.CreatedAt),
		})
	}
	m["messages"] = msgs
	return m
}

// attachFeedbackMessages 批量把时间线挂到反馈上（避免逐条查库的 N+1）。
func (s *Server) attachFeedbackMessages(r *http.Request, list []*model.Feedback) {
	if len(list) == 0 {
		return
	}
	ids := make([]string, 0, len(list))
	for _, f := range list {
		ids = append(ids, f.ID)
	}
	byID, err := s.store.ListFeedbackMessagesBatch(r.Context(), ids)
	if err != nil {
		// 时间线读失败不该让整个列表 500：正文与状态已经拿到了，
		// 少一条时间线比看不到反馈列表轻得多。
		s.log.Warn("读取反馈时间线失败", "err", err, "count", len(ids))
		return
	}
	for _, f := range list {
		msgs := byID[f.ID]
		if len(msgs) == 0 {
			continue
		}
		f.Messages = make([]model.FeedbackMessage, 0, len(msgs))
		for _, m := range msgs {
			f.Messages = append(f.Messages, *m)
		}
	}
}

// handleCreateFeedback 提交一条反馈（普通用户）。
func (s *Server) handleCreateFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain  string       `json:"domain"`
		Kind    string       `json:"kind"`
		Message string       `json:"message"`
		Captcha *captchaBody `json:"captcha"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if ok, msg := s.verifyCaptchaByScene(r, CaptchaSceneFeedback, req.Captcha); !ok {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, msg, nil)
		return
	}

	u := currentUser(r)

	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "请填写反馈内容", nil)
		return
	}
	if len([]rune(msg)) > maxFeedbackMessage {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"反馈内容最多 "+itoa(maxFeedbackMessage)+" 个字符", nil)
		return
	}
	if len(domain) > maxFeedbackDomain {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "域名过长", nil)
		return
	}
	kind := req.Kind
	switch kind {
	case model.FeedbackWrongBlock, model.FeedbackWrongAllow, model.FeedbackOther:
	case "":
		kind = model.FeedbackOther
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"kind 只能是 wrong_block / wrong_allow / other", nil)
		return
	}

	f := &model.Feedback{
		UserID:   u.ID,
		Username: u.Username,
		Domain:   domain,
		Kind:     kind,
		Message:  msg,
		Status:   model.FeedbackOpen,
	}
	if err := s.store.CreateFeedback(r.Context(), f); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 首条正文同时进时间线：前端只按时间线渲染，不再为「第一条」写特例。
	first := &model.FeedbackMessage{
		FeedbackID: f.ID,
		Role:       model.FeedbackRoleUser,
		Author:     f.Username,
		Content:    f.Message,
		CreatedAt:  f.CreatedAt,
	}
	if err := s.store.AppendFeedbackMessage(r.Context(), first); err != nil {
		// 时间线写失败不该让提交失败：正文已经落在 feedback.message 里，
		// 管理员照样能处理，只是这次对话少一条。
		s.log.Warn("写入反馈首条消息失败", "feedback", f.ID, "err", err)
	} else {
		f.Messages = []model.FeedbackMessage{*first}
	}

	// 新反馈提醒管理员。异步发信：SMTP 抖动不能让提交卡住或失败。
	go s.notifyFeedbackNew(context.Background(), f)

	// on_submit 模式下立刻排一次 AI 诊断（异步，不阻塞本次响应）。
	//
	// 只对「误杀申诉」触发：wrong_allow（该拦没拦）与 other 的处置
	// 方式差别太大，交给管理员判断更稳。模型对这两类也基本给不出
	// 可执行建议，跑了纯属浪费额度。
	if f.Kind == model.FeedbackWrongBlock && s.diagnoser != nil && s.diagnoser.ShouldAutoRun() {
		if err := s.store.ResetAIDiagnosis(r.Context(), f.ID); err != nil {
			s.log.Warn("初始化诊断状态失败", "feedback", f.ID, "err", err)
		} else {
			f.AIStatus = model.AIDiagPending
			s.diagnoser.Enqueue(*f)
		}
	}

	writeJSON(w, http.StatusCreated, feedbackJSON(f))
}

// handleListMyFeedback 返回当前用户自己的反馈。
func (s *Server) handleListMyFeedback(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	page, size, offset := pagination(r, 20)
	status := r.URL.Query().Get("status")

	list, total, err := s.store.ListFeedback(r.Context(), u.ID, status, size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.attachFeedbackMessages(r, list)
	items := make([]map[string]any, 0, len(list))
	for _, f := range list {
		items = append(items, feedbackJSON(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
	})
}

// handleAdminListFeedback 返回全部反馈（管理员）。
func (s *Server) handleAdminListFeedback(w http.ResponseWriter, r *http.Request) {
	page, size, offset := pagination(r, 50)
	status := r.URL.Query().Get("status")

	list, total, err := s.store.ListFeedback(r.Context(), "", status, size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.attachFeedbackMessages(r, list)
	items := make([]map[string]any, 0, len(list))
	for _, f := range list {
		items = append(items, feedbackJSON(f))
	}
	open, _ := s.store.CountFeedback(r.Context(), model.FeedbackOpen)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
		"open_count": open,
	})
}

// handleAdminReplyFeedback 回复或结单。
func (s *Server) handleAdminReplyFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
		Reply  string `json:"reply"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	fid := chi.URLParam(r, "id")

	status := req.Status
	switch status {
	case "", model.FeedbackOpen, model.FeedbackReplied, model.FeedbackClosed:
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"status 只能是 open / replied / closed", nil)
		return
	}
	reply := strings.TrimSpace(req.Reply)
	if len([]rune(reply)) > maxFeedbackMessage {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"回复最多 "+itoa(maxFeedbackMessage)+" 个字符", nil)
		return
	}
	// 回复了但没显式给状态，就自动置为已回复 —— 否则管理员要改两次。
	if status == "" && reply != "" {
		status = model.FeedbackReplied
	}

	if err := s.store.UpdateFeedback(r.Context(), fid, status, reply); err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}
	f, err := s.store.GetFeedback(r.Context(), fid)
	if err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}

	// 回复正文同时进时间线。admin_reply 保留为「最近一次管理员回复」的冗余快照 ——
	// AI 诊断与邮件通知都还在读它，一次性全改会把改动面放大好几倍。
	if reply != "" {
		msg := &model.FeedbackMessage{
			FeedbackID: f.ID,
			Role:       model.FeedbackRoleAdmin,
			Author:     "管理员",
			Content:    reply,
		}
		if u := currentUser(r); u != nil && u.Username != "" {
			msg.Author = u.Username
		}
		if err := s.store.AppendFeedbackMessage(r.Context(), msg); err != nil {
			s.log.Warn("写入管理员回复失败", "feedback", f.ID, "err", err)
		}
	}

	msgs, err := s.store.ListFeedbackMessages(r.Context(), f.ID)
	if err != nil {
		s.log.Warn("读取反馈时间线失败", "feedback", f.ID, "err", err)
	} else {
		for _, m := range msgs {
			f.Messages = append(f.Messages, *m)
		}
	}

	// 回复后给用户发一封邮件（可选，管理员没配 SMTP 就静默跳过）。
	s.notifyFeedbackReply(r.Context(), f, reply)

	writeJSON(w, http.StatusOK, feedbackJSON(f))
}

// handleReplyMyFeedback 让用户在自己的反馈下追问。
//
// 这是「回复又是一条」的根治手段：此前用户看完管理员回复想补充信息，
// 唯一能做的是再提交一条新反馈，于是同一个人在列表里占好几条，
// 管理员结单时还得挨个翻。现在追问追加到同一条时间线，状态回到待处理。
func (s *Server) handleReplyMyFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "请填写追问内容", nil)
		return
	}
	if len([]rune(msg)) > maxFeedbackMessage {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"内容最多 "+itoa(maxFeedbackMessage)+" 个字符", nil)
		return
	}

	u := currentUser(r)
	fid := chi.URLParam(r, "id")

	// 先确认这条反馈确实属于当前用户：GetFeedback 不带 user_id 条件，
	// 直接用它会让任何登录用户能往别人的反馈里插话。
	f, err := s.store.GetFeedback(r.Context(), fid)
	if err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}
	if f.UserID != u.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "反馈不存在", nil)
		return
	}
	if f.Status == model.FeedbackClosed {
		writeError(w, http.StatusConflict, CodeValidationFailed,
			"这条反馈已结单，如需继续请重新提交", nil)
		return
	}

	if err := s.store.AppendFeedbackMessage(r.Context(), &model.FeedbackMessage{
		FeedbackID: f.ID,
		Role:       model.FeedbackRoleUser,
		Author:     u.Username,
		Content:    msg,
	}); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 用户追问意味着需要管理员再看一眼：状态回到待处理。
	// 不这么做的话，管理员按「待处理」筛不到它，追问就石沉大海。
	if err := s.store.UpdateFeedback(r.Context(), f.ID, model.FeedbackOpen, ""); err != nil {
		s.log.Warn("重置反馈状态失败", "feedback", f.ID, "err", err)
	}

	f, err = s.store.GetFeedback(r.Context(), f.ID)
	if err != nil {
		writeStoreError(w, err, "反馈不存在")
		return
	}
	msgs, err := s.store.ListFeedbackMessages(r.Context(), f.ID)
	if err != nil {
		s.log.Warn("读取反馈时间线失败", "feedback", f.ID, "err", err)
	} else {
		for _, m := range msgs {
			f.Messages = append(f.Messages, *m)
		}
	}
	writeJSON(w, http.StatusOK, feedbackJSON(f))
}

// handleGetPreferences 返回当前用户的可调偏好。
func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"use_global_rules": u.UseGlobalRules,
	})
}

// handleUpdatePreferences 让用户自己开关全局规则层。
//
// 只允许改自己的偏好：这是「全局规则误伤我」时的自助逃生门，
// 不需要管理员介入。
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UseGlobalRules *bool `json:"use_global_rules"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UseGlobalRules == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"use_global_rules 不能为空", nil)
		return
	}
	u := currentUser(r)
	if err := s.store.UpdateUserFields(r.Context(), u.ID, nil, nil, nil, req.UseGlobalRules, nil, nil); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 偏好写进快照，dnsd 据此决定要不要挂全局层 —— 必须立即重建。
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"use_global_rules": *req.UseGlobalRules,
	})
}
