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
	return map[string]any{
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
