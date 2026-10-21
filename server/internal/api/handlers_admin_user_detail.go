package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/bjtime"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// handleAdminGetUser 返回单个用户的完整信息。
//
// 列表接口只给「用户名 + 规则数」这类概览，管理员要判断某个用户
// 到底是会员、配额还剩多少、今天用了多少，得进到详情里看。
// 这些字段分散在 users / query_rollup 两处，在这里拼成一份响应，
// 前端就不用为了一个弹窗连打三个接口。
func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	now := time.Now()

	// 档位默认与生效配额都给：前端要能显示「会员档 200，你手填了 500」，
	// 只给生效值的话管理员看不出这个数是他自己改过的。
	def := model.QuotaFor(model.TierOf(u, now), mustSettings(s, r))
	eff := model.QuotaForUser(u, mustSettings(s, r), now)

	usage := map[string]any{}
	if ruleCount, err := s.store.CountRules(r.Context(), u.ID, false); err == nil {
		usage["rules"] = ruleCount
	}
	if subs, err := s.store.ListSubs(r.Context(), u.ID, false); err == nil {
		usage["subs"] = len(subs)
	}
	// 今日请求量取自 query_rollup 而不是 query_log：
	// query_log 只保留很短时间（用户可能设成 10 分钟），
	// 拿它统计「今日」会得到一个明显偏小的数。
	if n, err := s.store.CountUserQueriesSince(r.Context(), u.ID, bjtime.StartOfDay(now.Unix())); err == nil {
		usage["today_queries"] = n
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":               u.ID,
		"username":         u.Username,
		"email":            u.Email,
		"role":             u.Role,
		"client_id":        u.ClientID,
		"enabled":          u.Enabled,
		"use_global_rules": u.UseGlobalRules,
		"rules_enabled":    u.RulesEnabled,
		"subs_enabled":     u.SubsEnabled,
		"id_reset_count":   u.IDResetCount,
		"custom_id_used":   u.CustomIDUsed,
		"created_at":       rfc3339OrEmpty(u.CreatedAt),
		"member_until":     rfc3339OrEmpty(u.MemberUntil),
		"is_member":        u.IsMember(now),
		"member_days":      u.MemberRemainingDays(now),
		"tier":             string(model.TierOf(u, now)),
		"quota": map[string]any{
			"max_rules":     eff.MaxRules,
			"max_subs":      eff.MaxSubs,
			"daily_queries": eff.DailyQueries,
		},
		"quota_default": map[string]any{
			"max_rules":     def.MaxRules,
			"max_subs":      def.MaxSubs,
			"daily_queries": def.DailyQueries,
		},
		// 0 = 跟随档位，-1 = 不限，其他 = 手填上限。
		"custom": map[string]any{
			"max_rules":     u.CustomMaxRules,
			"max_subs":      u.CustomMaxSubs,
			"daily_queries": u.CustomDailyQueries,
		},
		"usage": usage,
	})
}

// handleAdminSetUserQuota 设置单个用户的配额覆盖。
//
// 三个字段都是可选的：只传一个就只改一个。
// 0 表示恢复按档位默认，负数表示不限 —— 之所以要区分，
// 是因为 Quota 里 0 本身就代表「不限制」，不借负号就分不出
// 「没设过」和「故意设成不限」。
func (s *Server) handleAdminSetUserQuota(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	var body struct {
		MaxRules     *int `json:"max_rules"`
		MaxSubs      *int `json:"max_subs"`
		DailyQueries *int `json:"daily_queries"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	rules := u.CustomMaxRules
	subs := u.CustomMaxSubs
	daily := u.CustomDailyQueries
	if body.MaxRules != nil {
		rules = *body.MaxRules
	}
	if body.MaxSubs != nil {
		subs = *body.MaxSubs
	}
	if body.DailyQueries != nil {
		daily = *body.DailyQueries
	}
	// -1 是「不限」，比 -1 更小的值没有意义，统一钳到 -1，
	// 免得库里存出 -999 这种看不懂的数。
	for _, v := range []*int{&rules, &subs, &daily} {
		if *v < -1 {
			*v = -1
		}
	}
	if err := s.store.UpdateUserQuota(r.Context(), u.ID, rules, subs, daily); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 配额进了快照（每日请求量要下发给 dnsd），必须重建才会生效。
	if _, err := s.coord.Rebuild(r.Context()); err != nil {
		s.log.Warn("配额已改但快照重建失败", "user", u.ID, "err", err)
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_set_user_quota", u.Username, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, map[string]any{
		"custom": map[string]any{
			"max_rules":     rules,
			"max_subs":      subs,
			"daily_queries": daily,
		},
	})
}

// handleClearMyLogs 让用户清空自己看到的查询日志。
//
// 只把 users.log_cleared_at 推到当前时间，一条数据都不删：
// 管理员那边的审计视图必须留着，否则用户清一下，出了问题就查无实据。
// 真正的物理删除仍然只由保留策略按时间做。
func (s *Server) handleClearMyLogs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	now := time.Now().Unix()
	if err := s.store.ClearUserLogs(r.Context(), u.ID, now); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared_at": rfc3339OrEmpty(now),
	})
}

// mustSettings 取全局设置，失败时返回 nil（配额换算会退化成 0 = 不限）。
// 详情接口宁可少显示一个数，也不该因为设置表读失败就整个 500。
func mustSettings(s *Server, r *http.Request) *model.GlobalSettings {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		return nil
	}
	return set
}
