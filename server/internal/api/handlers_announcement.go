package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// === 公告（管理员）===

// handleListAnnouncements 列出全部公告（含停用/过期）。
func (s *Server) handleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAnnouncements(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if list == nil {
		list = []model.Announcement{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "total": len(list)})
}

type announcementReq struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	Level       string `json:"level"`
	Mode        string `json:"mode"`
	Dismissible *bool  `json:"dismissible"`
	Enabled     *bool  `json:"enabled"`
	Pinned      *bool  `json:"pinned"`
	StartsAt    *int64 `json:"starts_at"`
	EndsAt      *int64 `json:"ends_at"`
}

// normalizeAnnouncement 校验并补齐默认值。
func normalizeAnnouncement(a *model.Announcement) string {
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		return "标题不能为空"
	}
	if len([]rune(a.Title)) > 120 {
		return "标题不能超过 120 字"
	}
	if len([]rune(a.Body)) > 8000 {
		return "正文不能超过 8000 字"
	}
	switch a.Level {
	case model.AnnLevelInfo, model.AnnLevelWarning, model.AnnLevelDanger:
	case "":
		a.Level = model.AnnLevelInfo
	default:
		return "level 只能是 info / warning / danger"
	}
	switch a.Mode {
	case model.AnnModeSilent, model.AnnModePopup:
	case "":
		a.Mode = model.AnnModeSilent
	default:
		return "mode 只能是 silent / popup"
	}
	if a.EndsAt > 0 && a.StartsAt > 0 && a.EndsAt < a.StartsAt {
		return "结束时间不能早于开始时间"
	}
	return ""
}

// handleCreateAnnouncement 新建公告。
func (s *Server) handleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req announcementReq
	if !decodeJSON(w, r, &req) {
		return
	}
	a := model.Announcement{
		Title: req.Title, Body: req.Body, Level: req.Level, Mode: req.Mode,
		Dismissible: true, Enabled: true,
	}
	if req.Dismissible != nil {
		a.Dismissible = *req.Dismissible
	}
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if req.Pinned != nil {
		a.Pinned = *req.Pinned
	}
	if req.StartsAt != nil {
		a.StartsAt = *req.StartsAt
	}
	if req.EndsAt != nil {
		a.EndsAt = *req.EndsAt
	}
	if msg := normalizeAnnouncement(&a); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg, nil)
		return
	}
	id, err := s.store.CreateAnnouncement(r.Context(), &a)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	a.ID = id
	writeJSON(w, http.StatusCreated, a)
}

// handleUpdateAnnouncement 更新公告。
func (s *Server) handleUpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "公告 id 不合法", nil)
		return
	}
	var req announcementReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// 先读出原值再覆盖请求里出现的字段：
	// 公告是「改一条」而不是「换一条」，没传的字段必须保持原样。
	list, err := s.store.ListAnnouncements(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	var cur *model.Announcement
	for i := range list {
		if list[i].ID == id {
			cur = &list[i]
			break
		}
	}
	if cur == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "公告不存在", nil)
		return
	}
	a := *cur
	if req.Title != "" {
		a.Title = req.Title
	}
	if req.Body != "" {
		a.Body = req.Body
	}
	if req.Level != "" {
		a.Level = req.Level
	}
	if req.Mode != "" {
		a.Mode = req.Mode
	}
	if req.Dismissible != nil {
		a.Dismissible = *req.Dismissible
	}
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if req.Pinned != nil {
		a.Pinned = *req.Pinned
	}
	if req.StartsAt != nil {
		a.StartsAt = *req.StartsAt
	}
	if req.EndsAt != nil {
		a.EndsAt = *req.EndsAt
	}
	if msg := normalizeAnnouncement(&a); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg, nil)
		return
	}
	if err := s.store.UpdateAnnouncement(r.Context(), &a); err != nil {
		writeStoreError(w, err, "公告不存在")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// handleDeleteAnnouncement 删除公告。
func (s *Server) handleDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "公告 id 不合法", nil)
		return
	}
	if err := s.store.DeleteAnnouncement(r.Context(), id); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// === 公告（用户）===

// handleMyAnnouncements 返回当前用户可见的公告与未读数。
func (s *Server) handleMyAnnouncements(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	list, err := s.store.ActiveAnnouncements(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if list == nil {
		list = []model.AnnouncementView{}
	}
	unread := 0
	for _, a := range list {
		if a.ReadAt == 0 {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  list,
		"unread": unread,
	})
}

// handleReadAnnouncement 标记已读。
func (s *Server) handleReadAnnouncement(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "公告 id 不合法", nil)
		return
	}
	if err := s.store.MarkAnnouncementRead(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDismissAnnouncement 标记「不再弹出」。
func (s *Server) handleDismissAnnouncement(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "公告 id 不合法", nil)
		return
	}
	if err := s.store.DismissAnnouncement(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
