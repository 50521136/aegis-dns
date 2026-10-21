package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/store"
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
		"client_id":     cid,
		"old_client_id": old,
		"used":          u.IDResetCount + 1,
		"limit":         set.MaxIDResets,
		"remaining":     set.MaxIDResets - u.IDResetCount - 1,
		"dns_config":    buildDNSConfig(s.domain(r), cid, s.fingerprint(), s.dotPort(), s.coversSubdomains(r), s.protos(r.Context())),
	})
}

/* ---------------- 自定义 client_id ---------------- */

// reservedClientID 判断某个 client_id 是否和主域名首段撞车。
//
// 为什么要拦：dnsd 识别租户是先取 SNI 首段再查 client_id。主域名
// adguard.seven-cloud.cn 的首段是 "adguard"，如果有人把 client_id 设成
// "adguard"，那么**裸域名**的请求就会被认成他的租户、套上他的规则 ——
// 等于把主域名认领走了。这类冲突必须在写入前拦掉。
func (s *Server) reservedClientID(r *http.Request, cid string) bool {
	domain := s.domain(r)
	first := domain
	if i := strings.IndexByte(domain, '.'); i > 0 {
		first = domain[:i]
	}
	return cid == strings.ToLower(first)
}

// handleSetMyClientID 让用户把自己的 client_id 改成自定义值。
//
// 需求是「只能一次」：这跟重置不同 —— 重置是系统发一个新的随机 id，
// 自定义是用户自己挑，挑完就定下来，之后想换只能找管理员。
//
// 校验分三层，顺序有讲究：
//  1. 形状（长度/字符集）先判，纯本地、最便宜，也让报错最具体
//  2. 「机会是否已用」再判，避免用户白填一遍才发现没机会了
//  3. 唯一性最后判，要查库
func (s *Server) handleSetMyClientID(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	var body struct {
		ClientID string `json:"client_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	// 统一小写再校验：用户多半会输入大写，直接报错太苛刻，
	// 而 client_id 本身是 DNS 标签，大小写不敏感，转小写语义不变。
	cid := strings.ToLower(strings.TrimSpace(body.ClientID))
	if err := id.CheckCustomClientID(cid); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
		return
	}

	if s.reservedClientID(r, cid) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"该接入 ID 与主域名首段冲突，换一个", nil)
		return
	}
	if u.CustomIDUsed {
		writeError(w, http.StatusForbidden, CodeCustomIDUsed,
			"自定义接入 ID 只能设置一次，如需再改请联系管理员", nil)
		return
	}
	if cid == u.ClientID {
		// 填了个一样的值：不该消耗掉这一次机会
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"这就是你当前的接入 ID，无需修改", nil)
		return
	}

	taken, err := s.store.ClientIDTaken(r.Context(), cid, u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if taken {
		writeError(w, http.StatusConflict, CodeClientIDTaken, "该接入 ID 已被占用，换一个试试", nil)
		return
	}

	old := u.ClientID
	if err := s.store.SetCustomClientID(r.Context(), u.ID, cid); err != nil {
		switch {
		case errors.Is(err, store.ErrClientIDTaken):
			writeError(w, http.StatusConflict, CodeClientIDTaken, "该接入 ID 已被占用，换一个试试", nil)
		case errors.Is(err, store.ErrNotFound):
			// 并发下被另一个请求抢先改掉了，这里如实说明而不是含糊报错
			writeError(w, http.StatusForbidden, CodeCustomIDUsed,
				"自定义接入 ID 只能设置一次，如需再改请联系管理员", nil)
		default:
			writeStoreError(w, err, "")
		}
		return
	}
	// 换 id 必须重建快照：否则 dnsd 还认旧子域，新地址解析不了。
	if err := s.touched(r.Context(), u.ID, false); err != nil {
		s.log.Warn("自定义 client_id 后重建快照失败", "user", u.ID, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":     cid,
		"old_client_id": old,
		"dns_config":    buildDNSConfig(s.domain(r), cid, s.fingerprint(), s.dotPort(), s.coversSubdomains(r), s.protos(r.Context())),
	})
}

// handleAdminSetClientID 管理员直接指定某个用户的 client_id。
//
// 不消耗用户那次机会：用户填错字、或者机会用完后想再换，都靠这条兜底。
func (s *Server) handleAdminSetClientID(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")

	var body struct {
		ClientID string `json:"client_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cid := strings.ToLower(strings.TrimSpace(body.ClientID))
	if err := id.CheckCustomClientID(cid); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
		return
	}

	if s.reservedClientID(r, cid) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"该接入 ID 与主域名首段冲突，换一个", nil)
		return
	}
	taken, err := s.store.ClientIDTaken(r.Context(), cid, uid)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if taken {
		writeError(w, http.StatusConflict, CodeClientIDTaken, "该接入 ID 已被占用", nil)
		return
	}

	if err := s.store.AdminSetClientID(r.Context(), uid, cid); err != nil {
		if errors.Is(err, store.ErrClientIDTaken) {
			writeError(w, http.StatusConflict, CodeClientIDTaken, "该接入 ID 已被占用", nil)
			return
		}
		writeStoreError(w, err, "")
		return
	}
	if err := s.touched(r.Context(), uid, false); err != nil {
		s.log.Warn("管理员设置 client_id 后重建快照失败", "user", uid, "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"client_id": cid})
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
		StatsDays  *int `json:"stats_retention_days"`
		LogDays    *int `json:"query_log_retention_days"`
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
		// 两档配额都给出来：前端要按每个用户自己的档位显示对应上限，
		// 而不是拿一个统一数字糊弄。
		"limits": map[string]any{
			"free": map[string]any{
				"max_rules":     set.FreeMaxRules,
				"max_subs":      set.FreeMaxSubs,
				"daily_queries": set.FreeDailyQueries,
			},
			"vip": map[string]any{
				"max_rules":     set.VipMaxRules,
				"max_subs":      set.VipMaxSubs,
				"daily_queries": set.VipDailyQueries,
			},
			"member_reward_days": set.MemberRewardDays,
		},
	})
}
