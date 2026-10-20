package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/auth"
)

/* ---------------- 重置 client_id ---------------- */

// handleResetMyClientID 让用户自助重置自己的 client_id。
//
// 换 id 等于换掉整个接入地址（旧子域立刻失效），所以必须限次：
// 上限来自全局设置 MaxIDResets，0 表示管理员关掉了这个功能。
// 次数用完之后只能找管理员，管理员那边不受这个限制。
func (s *Server) handleResetMyClientID(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if set.MaxIDResets <= 0 {
		writeError(w, http.StatusForbidden, CodeFeatureDisabled,
			"管理员已关闭自助重置 ID 功能，请联系管理员", nil)
		return
	}
	if u.IDResetCount >= set.MaxIDResets {
		writeError(w, http.StatusForbidden, CodeFeatureDisabled,
			"重置次数已用完（"+itoa(u.IDResetCount)+"/"+itoa(set.MaxIDResets)+"），请联系管理员", nil)
		return
	}

	old := u.ClientID
	cid, err := s.store.ResetClientID(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 换 id 必须重建快照：否则 dnsd 还认旧子域，新地址解析不了。
	if err := s.touched(r.Context(), u.ID, false); err != nil {
		s.log.Warn("重置 client_id 后重建快照失败", "user", u.ID, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":      cid,
		"old_client_id":  old,
		"used":           u.IDResetCount + 1,
		"limit":          set.MaxIDResets,
		"remaining":      set.MaxIDResets - u.IDResetCount - 1,
		"dns_config":     buildDNSConfig(s.domain(r), cid, s.fingerprint(), s.dotPort()),
	})
}

// handleAdminResetClientID 管理员替某个用户重置 client_id（不受次数限制）。
func (s *Server) handleAdminResetClientID(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	target, err := s.store.GetUserByID(r.Context(), uid)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	cid, err := s.store.ResetClientID(r.Context(), uid)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.touched(r.Context(), uid, false); err != nil {
		s.log.Warn("重置 client_id 后重建快照失败", "user", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            target.ID,
		"username":      target.Username,
		"client_id":     cid,
		"old_client_id": target.ClientID,
	})
}

/* ---------------- 批量删除用户 ---------------- */

// handleAdminBulkDeleteUsers 批量删除用户。
//
// 与单个删除走同一套级联（rules/subscriptions 靠外键 ON DELETE CASCADE），
// 但放在一个事务里，避免「删了 3 个报错」这种说不清的状态。
// 操作者自己会被跳过：管理员不该能一键把自己删掉。
func (s *Server) handleAdminBulkDeleteUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "ids 不能为空", nil)
		return
	}
	if len(req.IDs) > 200 {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "一次最多删除 200 个用户", nil)
		return
	}

	actor := currentUser(r)
	deleted, err := s.store.BulkDeleteUsers(r.Context(), req.IDs, actor.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 用户没了，快照必须重建，否则他们的子域在 dnsd 里还挂着。
	if _, err := s.coord.Changed(r.Context()); err != nil {
		s.log.Warn("批量删除用户后重建快照失败", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"skipped": len(req.IDs) - deleted,
	})
}

/* ---------------- 管理员改密码 ---------------- */

// handleAdminSetPassword 管理员直接给某个用户设置新密码。
//
// 用途是用户忘记密码且没有邮箱可走找回流程时的兜底。
// 不要求旧密码（管理员本来也不知道），但强度校验与用户自助改密一致。
func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	uid := chi.URLParam(r, "id")

	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), uid, hash); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	// 改密后踢掉该用户所有 refresh token，否则旧会话还能继续换发新令牌。
	if err := s.store.RevokeAllUserTokens(r.Context(), uid); err != nil {
		s.log.Warn("改密后吊销令牌失败", "user", uid, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

/* ---------------- 手动清理 ---------------- */

// handleAdminCleanup 立即执行一次日志与统计清理。
//
// 参数留空则用后台配置的保留天数；显式传 0 表示「只按条数上限清理，不按时间」。
// 定时任务每天跑一次，这个接口给管理员「现在就想腾空间」时用。
func (s *Server) handleAdminCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StatsDays *int `json:"stats_retention_days"`
		LogDays   *int `json:"query_log_retention_days"`
		MaxLogRows *int `json:"max_log_rows"`
	}
	// 允许空 body
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	statsDays := set.StatsRetentionD
	if req.StatsDays != nil {
		statsDays = *req.StatsDays
	}
	logDays := set.LogRetentionD
	if req.LogDays != nil {
		logDays = *req.LogDays
	}
	maxRows := 1000000
	if req.MaxLogRows != nil {
		maxRows = *req.MaxLogRows
	}

	start := time.Now()
	rows, err := s.store.Cleanup(r.Context(), statsDays, logDays, maxRows)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	var total int64
	for _, n := range rows {
		total += n
	}
	s.log.Info("管理员手动清理完成", "deleted", rows, "total", total,
		"stats_days", statsDays, "log_days", logDays)

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":              rows,
		"total":                total,
		"stats_retention_days": statsDays,
		"log_retention_days":   logDays,
		"duration_ms":          time.Since(start).Milliseconds(),
	})
}

// handleAdminUsersQuota 返回各用户的规则/订阅占用情况，供后台列表展示。
func (s *Server) handleAdminUsersQuota(w http.ResponseWriter, r *http.Request) {
	usage, err := s.store.RuleSubUsage(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": usage,
		"limits": map[string]any{
			"max_rules_per_user": set.MaxRulesPerUser,
			"max_subs_per_user":  set.MaxSubsPerUser,
		},
	})
}
