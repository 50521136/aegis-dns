package api

import (
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
	return m
}

// handleCreateFeedback 提交一条反馈（普通用户）。
func (s *Server) handleCreateFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain  string `json:"domain"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &req) {
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
