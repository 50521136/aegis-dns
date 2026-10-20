package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)


// refreshSubAsync 在后台拉取订阅，成功后重建快照。
//
// 关键点：不能用 r.Context()。拉取在这条线路上要 60~90 秒，浏览器/代理先断开时
// 请求上下文被取消，下载直接中断（日志里是 context canceled），订阅就永远停在
// 「0 条目 / 未同步」。拉取是用户已经确认过的后台任务，应当脱离请求生命周期。
func (s *Server) refreshSubAsync(userID string, sub *model.Subscription, global bool) {
	go func() {
		// 比单次拉取超时多留一截，给解析和入库用。
		fctx, cancel := context.WithTimeout(context.Background(), subsub.FetchTimeout+60*time.Second)
		defer cancel()

		res, err := s.subs.Refresh(fctx, sub)
		if err != nil {
			s.log.Warn("订阅刷新失败（后台）", "sub", sub.ID, "url", sub.URL, "err", err)
			return
		}
		s.log.Info("订阅刷新完成（后台）", "sub", sub.ID, "entries", res.LastCount, "status", res.LastStatus)

		// 条目变了必须重建快照，否则新拉下来的规则不会生效。
		tctx, tcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer tcancel()
		if err := s.touched(tctx, userID, global); err != nil {
			s.log.Warn("刷新订阅后重建快照失败", "sub", sub.ID, "err", err)
		}
	}()
}

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
		"is_global":    sub.IsGlobal,
		"last_count":   sub.LastCount,
		"last_status":  sub.LastStatus,
		"fail_count":   sub.FailCount,
		"last_fetched": rfc3339OrEmpty(sub.LastFetched),
		"created_at":   rfc3339OrEmpty(sub.CreatedAt),
	}
}

// subQuota 返回指定 scope 下该用户的订阅配额上限。
func (s *Server) subQuota(ctx context.Context, global bool) int {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return 0
	}
	if global {
		return set.MaxSubsGlobal
	}
	return set.MaxSubsPerUser
}

// checkSubQuota 在新增订阅前检查配额。
func (s *Server) checkSubQuota(ctx context.Context, uid string, global bool) error {
	limit := s.subQuota(ctx, global)
	if limit <= 0 {
		return nil
	}
	cur, err := s.store.CountSubs(ctx, uid, global)
	if err != nil {
		return err
	}
	if cur+1 > limit {
		scope := "个人"
		if global {
			scope = "全局"
		}
		return errQuota(scope + "订阅已达上限 " + itoa(limit) + " 个（当前 " + itoa(cur) +
			"）。请先删除部分订阅，或让管理员调整配额。")
	}
	return nil
}

func (s *Server) handleListSubs(w http.ResponseWriter, r *http.Request) {
	s.listSubs(w, r, false)
}

func (s *Server) handleAdminListSubs(w http.ResponseWriter, r *http.Request) {
	s.listSubs(w, r, true)
}

func (s *Server) listSubs(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)
	list, err := s.store.ListSubs(r.Context(), u.ID, global)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, sub := range list {
		items = append(items, subToJSON(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "limit": s.subQuota(r.Context(), global),
	})
}

func (s *Server) handleCreateSub(w http.ResponseWriter, r *http.Request) {
	s.createSub(w, r, false)
}

func (s *Server) handleAdminCreateSub(w http.ResponseWriter, r *http.Request) {
	s.createSub(w, r, true)
}

func (s *Server) createSub(w http.ResponseWriter, r *http.Request, global bool) {
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
		IsGlobal: global,
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
	if !global {
		if err := s.checkUserFeature(r.Context(), u, "subs"); err != nil {
			writeFeatureError(w, err)
			return
		}
	}
	if err := s.checkSubQuota(r.Context(), u.ID, global); err != nil {
		writeQuotaError(w, err)
		return
	}
	if err := s.store.CreateSub(r.Context(), sub); err != nil {
		writeStoreError(w, err, "")
		return
	}

	// 新建后拉一次，但不能同步跑在请求里：
	// 这条线路上公共规则源要 60~90 秒才下完，浏览器/代理往往先断开，
	// r.Context() 被取消后下载直接中断（日志里表现为 context canceled），
	// 订阅就永远停在「0 条目 / 未同步」。改成后台任务 + 独立上下文，
	// 请求立刻返回，前端靠轮询看进度。
	if sub.Enabled {
		sub.LastStatus = model.StatusPending
		// 必须落库：前端创建完会重新拉列表，列表读数据库，
		// 只改内存的话读回来是空状态，界面上显示成「未同步」。
		if err := s.store.SetSubStatus(r.Context(), sub.ID, model.StatusPending); err != nil {
			s.log.Warn("写入同步中状态失败", "sub", sub.ID, "err", err)
		}
		s.refreshSubAsync(u.ID, sub, global)
	}
	writeJSON(w, http.StatusCreated, subToJSON(sub))
}

func (s *Server) handleUpdateSub(w http.ResponseWriter, r *http.Request) {
	s.updateSub(w, r, false)
}

func (s *Server) handleAdminUpdateSub(w http.ResponseWriter, r *http.Request) {
	s.updateSub(w, r, true)
}

func (s *Server) updateSub(w http.ResponseWriter, r *http.Request, global bool) {
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
	// 作用域必须与路由一致，否则普通用户能用个人路由改掉全局订阅。
	if sub.IsGlobal != global {
		writeError(w, http.StatusNotFound, CodeNotFound, "订阅不存在", nil)
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
	if err := s.touched(r.Context(), u.ID, global); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, subToJSON(sub))
}

func (s *Server) handleDeleteSub(w http.ResponseWriter, r *http.Request) {
	s.deleteSub(w, r, false)
}

func (s *Server) handleAdminDeleteSub(w http.ResponseWriter, r *http.Request) {
	s.deleteSub(w, r, true)
}

func (s *Server) deleteSub(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)
	sid := chi.URLParam(r, "id")

	sub, err := s.store.GetSub(r.Context(), u.ID, sid)
	if err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	if sub.IsGlobal != global {
		writeError(w, http.StatusNotFound, CodeNotFound, "订阅不存在", nil)
		return
	}
	if err := s.store.DeleteSub(r.Context(), u.ID, sid); err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	if err := s.touched(r.Context(), u.ID, global); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshSub(w http.ResponseWriter, r *http.Request) {
	s.refreshSub(w, r, false)
}

func (s *Server) handleAdminRefreshSub(w http.ResponseWriter, r *http.Request) {
	s.refreshSub(w, r, true)
}

func (s *Server) refreshSub(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)
	sid := chi.URLParam(r, "id")

	sub, err := s.store.GetSub(r.Context(), u.ID, sid)
	if err != nil {
		writeStoreError(w, err, "订阅不存在")
		return
	}
	if sub.IsGlobal != global {
		writeError(w, http.StatusNotFound, CodeNotFound, "订阅不存在", nil)
		return
	}
	// 与新建同理：拉取慢且易被客户端断开，交给后台任务，接口立刻回 202。
	if err := s.store.SetSubStatus(r.Context(), sub.ID, model.StatusPending); err != nil {
		s.log.Warn("写入同步中状态失败", "sub", sub.ID, "err", err)
	}
	s.refreshSubAsync(u.ID, sub, global)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":          sub.ID,
		"last_status": model.StatusPending,
		"pending":     true,
	})
}

func (s *Server) handleRefreshAllSubs(w http.ResponseWriter, r *http.Request) {
	s.refreshAllSubs(w, r, false)
}

func (s *Server) handleAdminRefreshAllSubs(w http.ResponseWriter, r *http.Request) {
	s.refreshAllSubs(w, r, true)
}

func (s *Server) refreshAllSubs(w http.ResponseWriter, r *http.Request, global bool) {
	u := currentUser(r)

	// 只刷新本作用域内的订阅：refresh-all 是用户级操作，
	// 不能让任何登录用户触发全站拉取（那是放大器）。
	subs, err := s.store.ListSubs(r.Context(), u.ID, global)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 批量刷新同样不能跑在请求里：几份订阅串起来要好几十分钟，
	// 请求早就超时了，而每次客户端断开都会把正在下的那份砍掉。
	// 这里全部交给后台任务，接口立刻返回，用户去列表看进度。
	pending := 0
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		sub.LastStatus = model.StatusPending
		if err := s.store.SetSubStatus(r.Context(), sub.ID, model.StatusPending); err != nil {
			s.log.Warn("写入同步中状态失败", "sub", sub.ID, "err", err)
		}
		s.refreshSubAsync(u.ID, sub, global)
		pending++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending": pending, "queued": true})
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": subsub.PresetList()})
}
