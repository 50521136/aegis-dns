package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// validateSub 校验订阅字段。
func validateSub(sub *model.Subscription) error {
	sub.Name = strings.TrimSpace(sub.Name)
	sub.URL = strings.TrimSpace(sub.URL)
	if sub.Name == "" {
		sub.Name = sub.URL
	}
	if len([]rune(sub.Name)) > 120 {
		return errValidation("订阅名称最多 120 个字符")
	}
	if !strings.HasPrefix(sub.URL, "http://") && !strings.HasPrefix(sub.URL, "https://") {
		return errValidation("订阅地址必须以 http:// 或 https:// 开头")
	}
	switch sub.ListType {
	case model.ListBlock, model.ListAllow:
	default:
		return errValidation("list_type 只能是 blocklist 或 allowlist")
	}
	switch sub.Format {
	case "", "auto":
		sub.Format = "auto"
	case "adblock", "hosts", "dnsmasq", "domain":
	default:
		return errValidation("format 只能是 auto / adblock / hosts / dnsmasq / domain")
	}
	return nil
}

func subToJSON(sub *model.Subscription) map[string]any {
	return map[string]any{
		"id":           sub.ID,
		"name":         sub.Name,
		"url":          sub.URL,
		"list_type":    sub.ListType,
		"format":       sub.Format,
		"enabled":      sub.Enabled,
		"last_count":   sub.LastCount,
		"last_status":  sub.LastStatus,
		"fail_count":   sub.FailCount,
		"last_fetched": rfc3339OrEmpty(sub.LastFetched),
		"created_at":   rfc3339OrEmpty(sub.CreatedAt),
	}
}

func (s *Server) handleListSubs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	list, err := s.store.ListSubs(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, sub := range list {
		items = append(items, subToJSON(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) handleCreateSub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		ListType string `json:"list_type"`
		Format   string `json:"format"`
		Enabled  *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)

	sub := &model.Subscription{
		UserID:   u.ID,
		Name:     req.Name,
		URL:      req.URL,
		ListType: req.ListType,
		Format:   req.Format,
		Enabled:  true,
	}
	if sub.ListType == "" {
		sub.ListType = model.ListBlock
	}
	if req.Enabled != nil {
		sub.Enabled = *req.Enabled
	}
	if err := validateSub(sub); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.CreateSub(r.Context(), sub); err != nil {
		writeStoreError(w, err, "")
		return
	}

	// 新建后立即拉一次，让用户马上看到条目数，而不是等 24 小时。
	// 拉取失败不算创建失败：订阅已经建好了，状态里会写明原因。
	if sub.Enabled {
		if res, err := s.subs.Refresh(r.Context(), sub); err != nil {
			s.log.Warn("新建订阅首次拉取失败", "sub", sub.ID, "err", err)
			sub.LastStatus = subsub.DescribeStatus(err)
		} else {
			sub.LastCount = res.LastCount
			sub.LastStatus = res.LastStatus
			sub.LastFetched = res.FetchedAt
		}
	}
	writeJSON(w, http.StatusCreated, subToJSON(sub))
}

func (s *Server) handleUpdateSub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		ListType string `json:"list_type"`
		Format   string `json:"format"`
		Enabled  *bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := currentUser(r)
	sid := chi.URLParam(r, "id")

	sub, err := s.store.GetSub(r.Context(), u.ID, sid)
	if err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	if req.Name != "" {
		sub.Name = req.Name
	}
	if req.URL != "" {
		sub.URL = req.URL
	}
	if req.ListType != "" {
		sub.ListType = req.ListType
	}
	if req.Format != "" {
		sub.Format = req.Format
	}
	if req.Enabled != nil {
		sub.Enabled = *req.Enabled
	}
	if err := validateSub(sub); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.UpdateSub(r.Context(), sub); err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}

	// 停用订阅必须立刻把它的条目从快照里摘掉，否则规则还在生效。
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, subToJSON(sub))
}

func (s *Server) handleDeleteSub(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	sid := chi.URLParam(r, "id")
	if err := s.store.DeleteSub(r.Context(), u.ID, sid); err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshSub(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	sid := chi.URLParam(r, "id")

	sub, err := s.store.GetSub(r.Context(), u.ID, sid)
	if err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	res, err := s.subs.Refresh(r.Context(), sub)
	if err != nil {
		// 刷新失败仍然返回 200 + last_status：这是「操作已执行但结果失败」，
		// 前端要展示的是订阅状态而不是一个红色 toast。
		writeJSON(w, http.StatusOK, map[string]any{
			"id":           sub.ID,
			"last_count":   res.LastCount,
			"last_fetched": rfc3339OrEmpty(res.FetchedAt),
			"last_status":  res.LastStatus,
			"duration_ms":  res.DurationMS,
			"error":        err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           sub.ID,
		"last_count":   res.LastCount,
		"last_fetched": rfc3339OrEmpty(res.FetchedAt),
		"last_status":  res.LastStatus,
		"duration_ms":  res.DurationMS,
		"not_modified": res.NotModified,
	})
}

func (s *Server) handleRefreshAllSubs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	// 只刷新当前用户自己的订阅：refresh-all 是用户级操作，
	// 不能让任何登录用户触发全站拉取（那是放大器）。
	subs, err := s.store.ListSubs(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	refreshed, failed := 0, 0
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		if _, err := s.subs.Refresh(r.Context(), sub); err != nil {
			failed++
			continue
		}
		refreshed++
	}
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": refreshed, "failed": failed})
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": subsub.PresetList()})
}
