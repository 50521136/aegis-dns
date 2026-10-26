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

// forceRefresh 解析 ?force=1。
//
// 用途：条目上限、解析规则这类设置改完后，缓存的订阅会一直走 304 不重算，
// 管理员看不到任何变化。带 force 就忽略 ETag 完整重拉。
func forceRefresh(r *http.Request) bool {
	v := r.URL.Query().Get("force")
	return v == "1" || strings.EqualFold(v, "true")
}

// refreshSubAsync 在后台拉取订阅。
//
// 关键点：不能用 r.Context()。拉取在这条线路上要 60~90 秒，浏览器/代理先断开时
// 请求上下文被取消，下载直接中断（日志里是 context canceled），订阅就永远停在
// 「0 条目 / 未同步」。拉取是用户已经确认过的后台任务，应当脱离请求生命周期。
//
// 快照重建不在这里做：条目写库成功后由 subsub.Manager 的 onChanged 回调统一触发，
// 且重建范围取自订阅自身（sub.IsGlobal）。早先这里又调了一次 s.touched，
// 于是一次手动刷新触发两次全量重建；304 未变更时 Manager 不回调，这里却照样重建一次。
func (s *Server) refreshSubAsync(sub *model.Subscription, force bool) {
	go func() {
		// 比单次拉取超时多留一截，给解析和入库用。
		fctx, cancel := context.WithTimeout(context.Background(), subsub.FetchTimeout+60*time.Second)
		defer cancel()

		var (
			res *subsub.RefreshResult
			err error
		)
		if force {
			res, err = s.subs.RefreshForce(fctx, sub)
		} else {
			res, err = s.subs.Refresh(fctx, sub)
		}
		if err != nil {
			s.log.Warn("订阅刷新失败（后台）", "sub", sub.ID, "url", sub.URL, "err", err)
			return
		}
		s.log.Info("订阅刷新完成（后台）", "sub", sub.ID, "entries", res.LastCount, "status", res.LastStatus)
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
	// 0 = 跟随全局默认；其余必须落在合理区间。
	// 上限卡住是必要的：填 100000 等于让一个卡死的源占住整个刷新窗口。
	if sub.TimeoutSec < 0 || sub.TimeoutSec > 600 {
		return errValidation("timeout_sec 需在 0 到 600 之间（0 = 跟随全局默认）")
	}
	return nil
}

func subToJSON(sub *model.Subscription) map[string]any {
	return map[string]any{
		"id":          sub.ID,
		"name":        sub.Name,
		"url":         sub.URL,
		"list_type":   sub.ListType,
		"format":      sub.Format,
		"enabled":     sub.Enabled,
		"is_global":   sub.IsGlobal,
		"last_count":  sub.LastCount,
		"last_status": sub.LastStatus,
		"fail_count":  sub.FailCount,
		// 手写 map 必须与 model.Subscription 同步：漏一个字段的表现是
		// 「存进去了但界面上看不到/改不了」，而库里其实是对的。
		"timeout_sec":  sub.TimeoutSec,
		"last_fetched": rfc3339OrEmpty(sub.LastFetched),
		"created_at":   rfc3339OrEmpty(sub.CreatedAt),
	}
}

// subQuota 返回指定 scope 下的订阅配额上限，0 表示不限制。
// 全局层不限（见 ruleQuota 的说明），个人层按会员档位算。
func (s *Server) subQuota(ctx context.Context, uid string, global bool) int {
	if global {
		return 0
	}
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return 0
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u == nil {
		return 0
	}
	tiers, _ := s.store.TierMap(ctx)
	return model.QuotaForUser(u, set, tiers, time.Now()).MaxSubs
}

// checkSubQuota 在新增订阅前检查配额。
func (s *Server) checkSubQuota(ctx context.Context, uid string, global bool) error {
	limit := s.subQuota(ctx, uid, global)
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

// stalePendingAfter 是「同步中」允许停留的最长时间。
//
// 比一次拉取的超时（subsub.FetchTimeout）多留两分钟做解析与入库。
// 超过这个时间还停在 pending，说明后台任务已经没了（进程重启、
// goroutine 被掐、下载卡死），状态不会有人来改 —— 界面上就永远转圈。
const stalePendingAfter = subsub.FetchTimeout + 2*time.Minute

// normalizeStalePending 把卡死的「同步中」改写成明确的失败。
//
// 只改内存里的展示值，不动数据库：真正卡住的原因（进程重启）无法在这里修复，
// 但至少要让用户看到「超时了，点刷新重试」而不是无限转圈。
func normalizeStalePending(sub *model.Subscription) {
	if sub == nil || !strings.HasPrefix(sub.LastStatus, "pending") {
		return
	}
	if sub.LastFetched <= 0 {
		return
	}
	if time.Since(time.Unix(sub.LastFetched, 0)) > stalePendingAfter {
		sub.LastStatus = "error: 同步超时，请点刷新重试"
	}
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
		normalizeStalePending(sub)
		items = append(items, subToJSON(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "limit": s.subQuota(r.Context(), currentUser(r).ID, global),
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
		// TimeoutSec 是该订阅自己的拉取超时，0 = 跟随全局默认。
		// 用指针区分「没传」与「显式设为 0」：更新时没传就不该覆盖现有值。
		TimeoutSec *int `json:"timeout_sec"`
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
	if req.TimeoutSec != nil {
		sub.TimeoutSec = *req.TimeoutSec
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
		s.refreshSubAsync(sub, false) // 新建没有缓存，无需 force
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
		// TimeoutSec 是该订阅自己的拉取超时，0 = 跟随全局默认。
		// 用指针区分「没传」与「显式设为 0」：更新时没传就不该覆盖现有值。
		TimeoutSec *int `json:"timeout_sec"`
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
	// 先记下旧值再改：下面的「是否需要重拉」要跟旧值比。
	oldURL, oldListType := sub.URL, sub.ListType

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
	// 没传就保留原值：这是部分更新，不能把已有的超时清成 0。
	if req.TimeoutSec != nil {
		sub.TimeoutSec = *req.TimeoutSec
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

	// 列表类型或地址变了必须重新拉取条目。
	//
	// 条目是按 list_type 的口径筛出来存的（blocklist 只存 block 那批），
	// 只改字段不重拉的话，界面会显示「同步成功但 0 条」—— 用户看到的是
	// 「订阅明明好了却一条规则都没有」，无从理解。换地址同理：旧条目属于
	// 旧源，留着就是错的。
	//
	// 这里用 force 重拉：ETag 对应的是旧内容，不 force 会拿到 304 而跳过解析。
	if (sub.ListType != oldListType || sub.URL != oldURL) && sub.Enabled {
		s.refreshSubAsync(sub, true)
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
	s.refreshSubAsync(sub, forceRefresh(r))
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
		s.refreshSubAsync(sub, forceRefresh(r))
		pending++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending": pending, "queued": true})
}

// handleAdminRefreshAllTenants 触发**全站所有启用订阅**的重拉，含各用户的订阅。
//
// 为什么需要它：现有 /admin/subscriptions/refresh-all 走 ListSubs(u.ID, true)，
// 只覆盖管理员自己那一层（全局订阅）。而 refreshSub 用 GetSub(u.ID, sid)
// 按当前用户查，管理员拿别的用户的 sub id 只会得到 404。
// 结果是管理员**看得见某用户订阅条目数异常，却没有任何入口让它重拉** ——
// 只能去催用户自己点刷新，或者等 24 小时后的定时刷新。
//
// 典型场景：解析器修了一个丢条目的 bug（混合格式订阅 32448 行只剩 6171 条），
// 代码上线后存量订阅仍是旧数据，全站都需要重拉一次才生效。
//
// 同步契约：这里只排队、立刻返回 202；实际拉取在后台任务里跑，
// 几十份订阅串行拉要好几十分钟，不能挂在请求上。
func (s *Server) handleAdminRefreshAllTenants(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.AllEnabledSubs(r.Context())
	if err != nil {
		writeStoreError(w, err, "读取订阅失败")
		return
	}
	force := forceRefresh(r)
	queued, skipped := 0, 0
	for _, sub := range subs {
		if !sub.Enabled {
			skipped++
			continue
		}
		if err := s.store.SetSubStatus(r.Context(), sub.ID, model.StatusPending); err != nil {
			s.log.Warn("写入同步中状态失败", "sub", sub.ID, "err", err)
		}
		s.refreshSubAsync(sub, force)
		queued++
	}
	s.log.Info("管理员触发全站订阅重拉", "queued", queued, "skipped", skipped, "force", force)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":  queued,
		"skipped": skipped,
		"force":   force,
	})
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": subsub.PresetList()})
}
