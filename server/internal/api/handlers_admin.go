package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/selfupdate"
	"github.com/50521136/aegis-dns/server/internal/version"
)

// === 用户管理 ===

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	page, size, offset := pagination(r, 20)
	q := r.URL.Query().Get("q")

	users, total, err := s.store.ListUsers(r.Context(), q, size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		ruleCount, _ := s.store.CountRules(r.Context(), u.ID, false)
		ips, _ := s.store.ListUserIPs(r.Context(), u.ID)
		items = append(items, map[string]any{
			"id":         u.ID,
			"username":   u.Username,
			"email":      u.Email,
			"role":       u.Role,
			"client_id":  u.ClientID,
			"enabled":    u.Enabled,
			"created_at": rfc3339OrEmpty(u.CreatedAt),
			"rule_count": ruleCount,
			"ip_count":   len(ips),
			// 后台用户列表要显示并控制这几个
			"use_global_rules": u.UseGlobalRules,
			"id_reset_count":   u.IDResetCount,
			"rules_enabled":    u.RulesEnabled,
			"subs_enabled":     u.SubsEnabled,
			"custom_id_used":   u.CustomIDUsed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
	})
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool  `json:"enabled"`
		Role    string `json:"role"`
		Email   string `json:"email"`
		// UseGlobalRules 允许管理员为某个用户关闭全局规则层
		// （典型场景：全局规则误伤了某个用户，先给他摘掉应急）。
		UseGlobalRules *bool `json:"use_global_rules"`
		// RulesEnabled / SubsEnabled 是管理员对单个用户的开关：
		// 关掉后该用户不能再新建个人规则 / 订阅（已有的照常生效）。
		RulesEnabled *bool `json:"rules_enabled"`
		SubsEnabled  *bool `json:"subs_enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	uid := chi.URLParam(r, "id")
	actor := currentUser(r)

	if req.Role != "" && req.Role != model.RoleAdmin && req.Role != model.RoleUser {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "role 只能是 admin 或 user", nil)
		return
	}
	// 防止管理员把自己降级或禁用，把自己锁在门外。
	if uid == actor.ID {
		if req.Role != "" && req.Role != model.RoleAdmin {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "不能修改自己的角色", nil)
			return
		}
		if req.Enabled != nil && !*req.Enabled {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "不能禁用自己", nil)
			return
		}
	}
	// 至少保留一个启用的管理员。
	if req.Enabled != nil && !*req.Enabled || (req.Role != "" && req.Role != model.RoleAdmin) {
		if err := s.ensureAnotherAdmin(r.Context(), uid); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
			return
		}
	}

	var rolePtr, emailPtr *string
	if req.Role != "" {
		rolePtr = &req.Role
	}
	if req.Email != "" {
		emailPtr = &req.Email
	}
	if err := s.store.UpdateUserFields(r.Context(), uid, req.Enabled, rolePtr, emailPtr, req.UseGlobalRules, req.RulesEnabled, req.SubsEnabled); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}

	// 用户被禁用后，dnsd 必须立刻停止为他解析（enabled=false 会写进快照）。
	if _, err := s.coord.Changed(r.Context()); err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.store.LogAudit(r.Context(), actor.ID, "admin_update_user", uid, clientIP(r, s.cfg.API.TrustedProxies))

	u, err := s.store.GetUserByID(r.Context(), uid)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

// ensureAnotherAdmin 确认除了 uid 之外还有别的启用管理员。
func (s *Server) ensureAnotherAdmin(ctx context.Context, uid string) error {
	users, _, err := s.store.ListUsers(ctx, "", 1000, 0)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.ID == uid {
			continue
		}
		if u.Role == model.RoleAdmin && u.Enabled {
			return nil
		}
	}
	return errValidation("系统必须至少保留一个启用的管理员账号")
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	actor := currentUser(r)
	if uid == actor.ID {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "不能删除自己", nil)
		return
	}
	// 被删的用户可能是最后一个管理员。
	target, err := s.store.GetUserByID(r.Context(), uid)
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	if target.Role == model.RoleAdmin {
		if err := s.ensureAnotherAdmin(r.Context(), uid); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), uid); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	if _, err := s.coord.Changed(r.Context()); err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.store.LogAudit(r.Context(), actor.ID, "admin_delete_user", target.Username, clientIP(r, s.cfg.API.TrustedProxies))
	w.WriteHeader(http.StatusNoContent)
}

// === 全局设置 ===

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, s.settingsJSON(set))
}

// settingsJSON 补充只读的派生字段。
func (s *Server) settingsJSON(set *model.GlobalSettings) map[string]any {
	out := map[string]any{
		"domain":                   set.Domain,
		"upstreams":                set.Upstreams,
		"bootstrap_dns":            set.BootstrapDNS,
		"blocking_mode":            set.BlockingMode,
		"custom_ip":                set.CustomIP,
		"fallback_policy":          set.FallbackPolicy,
		"cache_size":               set.CacheSize,
		"cache_min_ttl":            set.CacheMinTTL,
		"cache_max_ttl":            set.CacheMaxTTL,
		"rate_limit_qps":           set.RateLimitQPS,
		"max_inflight":             set.MaxInflight,
		"query_timeout_ms":         set.QueryTimeoutMS,
		"query_log_size":           set.QueryLogSize,
		"enable_query_log":         set.EnableQueryLog,
		"sub_interval_hours":       set.SubIntervalH,
		"query_log_retention_days": set.LogRetentionD,
		"allow_register":           set.AllowRegister,
		"invite_code":              set.InviteCode,
		"update_repo":              set.UpdateRepo,
		"update_channel":           set.UpdateChannel,

		// 配额：前端据此显示「已用 x / 上限 y」并在新建时提前拦。
		"max_rules_per_user": set.MaxRulesPerUser,
		"max_subs_per_user":  set.MaxSubsPerUser,
		"max_sub_entries":    set.MaxSubEntries,
		"max_rules_global":   set.MaxRulesGlobal,
		"max_subs_global":    set.MaxSubsGlobal,

		// 主域名开关、清理周期、自助重置上限、租户侧总开关。
		// 这个 map 是手写的，加字段时这里必须同步 —— 漏了的话设置存进去了
		// 但前端读不到，表现为「改了没生效」。
		"allow_root_domain":    set.AllowRootDomain,
		"stats_retention_days": set.StatsRetentionD,
		"max_id_resets":        set.MaxIDResets,
		"allow_user_rules":     set.AllowUserRules,
		"allow_user_subs":      set.AllowUserSubs,
	}
	// 配置文件的 domain 作为兜底提示：管理员可能改了设置却忘了改配置，
	// 或反之。两个值不一致时前端要能看出来。
	out["config_domain"] = s.cfg.Domain
	out["dns_listen"] = map[string]any{
		"udp": s.cfg.DNS.ListenUDP,
		"tcp": s.cfg.DNS.ListenTCP,
		"dot": s.cfg.DNS.ListenDOT,
		"doh": s.cfg.DNS.ListenDOH,
	}
	return out
}

// settingsPatch 是 PUT /admin/settings 的请求体。
//
// 所有字段都用指针：只有「请求里真的出现过」才覆盖当前值。
//
// 这是必须的，不是洁癖 —— 用值类型时无法区分「没传」和「传了 false」，
// 于是任何部分更新（例如只想改 fallback_policy）都会把没提到的开关
// 全部按零值写回去：enable_query_log 被关掉、allow_register 被关掉……
// 实测踩到过：改完兜底策略后查询日志再也不更新，从任何日志里都看不出
// 是「被同一次设置请求顺手关掉了」。
type settingsPatch struct {
	Domain         *string   `json:"domain"`
	Upstreams      *[]string `json:"upstreams"`
	BootstrapDNS   *[]string `json:"bootstrap_dns"`
	BlockingMode   *string   `json:"blocking_mode"`
	CustomIP       *string   `json:"custom_ip"`
	FallbackPolicy *string   `json:"fallback_policy"`
	CacheSize      *int      `json:"cache_size"`
	CacheMinTTL    *int      `json:"cache_min_ttl"`
	CacheMaxTTL    *int      `json:"cache_max_ttl"`
	RateLimitQPS   *int      `json:"rate_limit_qps"`
	MaxInflight    *int      `json:"max_inflight"`
	QueryTimeoutMS *int      `json:"query_timeout_ms"`
	QueryLogSize   *int      `json:"query_log_size"`
	EnableQueryLog *bool     `json:"enable_query_log"`
	SubIntervalH   *int      `json:"sub_interval_hours"`
	LogRetentionD  *int      `json:"query_log_retention_days"`
	AllowRegister  *bool     `json:"allow_register"`
	InviteCode     *string   `json:"invite_code"`
	UpdateRepo     *string   `json:"update_repo"`
	UpdateChannel  *string   `json:"update_channel"`

	MaxRulesPerUser *int `json:"max_rules_per_user"`
	MaxSubsPerUser  *int `json:"max_subs_per_user"`
	MaxSubEntries   *int `json:"max_sub_entries"`
	MaxRulesGlobal  *int `json:"max_rules_global"`
	MaxSubsGlobal   *int `json:"max_subs_global"`

	AllowRootDomain *bool `json:"allow_root_domain"`
	StatsRetentionD *int  `json:"stats_retention_days"`
	MaxIDResets     *int  `json:"max_id_resets"`
	AllowUserRules  *bool `json:"allow_user_rules"`
	AllowUserSubs   *bool `json:"allow_user_subs"`
}

// applySettingsPatch 把请求里出现过的字段写到 dst 上。
func applySettingsPatch(dst *model.GlobalSettings, p *settingsPatch) {
	if p.Domain != nil {
		dst.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(*p.Domain), "."))
	}
	if p.Upstreams != nil {
		dst.Upstreams = cleanList(*p.Upstreams)
	}
	if p.BootstrapDNS != nil {
		dst.BootstrapDNS = cleanList(*p.BootstrapDNS)
	}
	if p.BlockingMode != nil {
		dst.BlockingMode = *p.BlockingMode
	}
	if p.CustomIP != nil {
		dst.CustomIP = strings.TrimSpace(*p.CustomIP)
	}
	if p.FallbackPolicy != nil {
		dst.FallbackPolicy = *p.FallbackPolicy
	}
	setInt(&dst.CacheSize, p.CacheSize)
	setInt(&dst.CacheMinTTL, p.CacheMinTTL)
	setInt(&dst.CacheMaxTTL, p.CacheMaxTTL)
	setInt(&dst.RateLimitQPS, p.RateLimitQPS)
	setInt(&dst.MaxInflight, p.MaxInflight)
	setInt(&dst.QueryTimeoutMS, p.QueryTimeoutMS)
	setInt(&dst.QueryLogSize, p.QueryLogSize)
	setInt(&dst.SubIntervalH, p.SubIntervalH)
	setInt(&dst.LogRetentionD, p.LogRetentionD)
	setInt(&dst.MaxRulesPerUser, p.MaxRulesPerUser)
	setInt(&dst.MaxSubsPerUser, p.MaxSubsPerUser)
	setInt(&dst.MaxSubEntries, p.MaxSubEntries)
	setInt(&dst.MaxRulesGlobal, p.MaxRulesGlobal)
	setInt(&dst.MaxSubsGlobal, p.MaxSubsGlobal)
	setInt(&dst.StatsRetentionD, p.StatsRetentionD)
	setInt(&dst.MaxIDResets, p.MaxIDResets)
	setBool(&dst.EnableQueryLog, p.EnableQueryLog)
	setBool(&dst.AllowRegister, p.AllowRegister)
	setBool(&dst.AllowRootDomain, p.AllowRootDomain)
	setBool(&dst.AllowUserRules, p.AllowUserRules)
	setBool(&dst.AllowUserSubs, p.AllowUserSubs)
	if p.InviteCode != nil {
		dst.InviteCode = strings.TrimSpace(*p.InviteCode)
	}
	if p.UpdateRepo != nil {
		dst.UpdateRepo = *p.UpdateRepo
	}
	if p.UpdateChannel != nil {
		dst.UpdateChannel = *p.UpdateChannel
	}
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

// cleanList 去空白、去空项；返回非 nil 空切片表示「显式清空」。
func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	// 先取当前值，只覆盖请求里出现过的字段。
	cur, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	applySettingsPatch(cur, &req)

	if err := validateSettings(cur); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.SaveSettings(r.Context(), cur); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 全局设置影响所有租户，必须全量重建快照。
	if _, err := s.coord.Changed(r.Context()); err != nil {
		writeStoreError(w, err, "")
		return
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_update_settings", cur.Domain, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, s.settingsJSON(cur))
}

// mergeSettings 用非零值覆盖当前设置。
func mergeSettings(dst *model.GlobalSettings, src *model.GlobalSettings) {
	if src.Domain != "" {
		dst.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(src.Domain), "."))
	}
	if len(src.Upstreams) > 0 {
		clean := make([]string, 0, len(src.Upstreams))
		for _, u := range src.Upstreams {
			if u = strings.TrimSpace(u); u != "" {
				clean = append(clean, u)
			}
		}
		if len(clean) > 0 {
			dst.Upstreams = clean
		}
	}
	if src.BlockingMode != "" {
		dst.BlockingMode = src.BlockingMode
	}
	// custom_ip 允许被清空（切回 null_ip 时需要），因此不做非零判断，
	// 但只在 blocking_mode 是 custom_ip 时才要求它非空（见 validateSettings）。
	dst.CustomIP = strings.TrimSpace(src.CustomIP)
	if src.FallbackPolicy != "" {
		dst.FallbackPolicy = src.FallbackPolicy
	}
	if src.CacheSize > 0 {
		dst.CacheSize = src.CacheSize
	}
	if src.CacheMinTTL > 0 {
		dst.CacheMinTTL = src.CacheMinTTL
	}
	if src.CacheMaxTTL > 0 {
		dst.CacheMaxTTL = src.CacheMaxTTL
	}
	if src.RateLimitQPS >= 0 {
		dst.RateLimitQPS = src.RateLimitQPS
	}
	if src.MaxInflight > 0 {
		dst.MaxInflight = src.MaxInflight
	}
	if src.QueryTimeoutMS > 0 {
		dst.QueryTimeoutMS = src.QueryTimeoutMS
	}
	if src.QueryLogSize > 0 {
		dst.QueryLogSize = src.QueryLogSize
	}
	if src.SubIntervalH > 0 {
		dst.SubIntervalH = src.SubIntervalH
	}
	if src.LogRetentionD > 0 {
		dst.LogRetentionD = src.LogRetentionD
	}
	if src.UpdateRepo != "" {
		dst.UpdateRepo = src.UpdateRepo
	}
	if src.UpdateChannel != "" {
		dst.UpdateChannel = src.UpdateChannel
	}
	// 配额：允许显式设成 0 表示「不限制」，因此不做非零判断。
	// 但负数没有意义，直接忽略。
	if src.MaxRulesPerUser >= 0 {
		dst.MaxRulesPerUser = src.MaxRulesPerUser
	}
	if src.MaxSubsPerUser >= 0 {
		dst.MaxSubsPerUser = src.MaxSubsPerUser
	}
	if src.MaxSubEntries >= 0 {
		dst.MaxSubEntries = src.MaxSubEntries
	}
	if src.MaxRulesGlobal >= 0 {
		dst.MaxRulesGlobal = src.MaxRulesGlobal
	}
	if src.MaxSubsGlobal >= 0 {
		dst.MaxSubsGlobal = src.MaxSubsGlobal
	}
	// 统计保留天数允许设成 0（不自动清理），所以用 >= 0。
	if src.StatsRetentionD >= 0 {
		dst.StatsRetentionD = src.StatsRetentionD
	}
	if src.MaxIDResets >= 0 {
		dst.MaxIDResets = src.MaxIDResets
	}
	// 布尔与字符串可能被显式设成零值，这里直接采用请求值。
	// 注意：前端必须每次都把这些字段传全，漏传等于把它们关掉。
	dst.EnableQueryLog = src.EnableQueryLog
	dst.AllowRegister = src.AllowRegister
	dst.AllowRootDomain = src.AllowRootDomain
	dst.AllowUserRules = src.AllowUserRules
	dst.AllowUserSubs = src.AllowUserSubs
	dst.InviteCode = strings.TrimSpace(src.InviteCode)
}

// handleTestUpstream 试解析一次，验证上游（含引导 DNS）是否可用。
//
// 为什么要这个接口：DoH/DoT 上游配错、或引导 DNS 没配导致主机名解析不了时，
// 现象是「所有查询都超时」，而设置页保存本身是成功的 —— 管理员只能靠猜。
// 这里直接把「连不上 / 解析不出 / 正常返回」摊开给出来。
func (s *Server) handleTestUpstream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Upstream     string   `json:"upstream"`
		BootstrapDNS []string `json:"bootstrap_dns"`
		Domain       string   `json:"domain"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	up := strings.TrimSpace(req.Upstream)
	if up == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "请填写要测试的上游地址", nil)
		return
	}
	qname := strings.TrimSpace(req.Domain)
	if qname == "" {
		qname = "www.baidu.com"
	}
	// 没传 bootstrap_dns 就用当前已保存的设置，测的才是「保存后实际会用到的组合」。
	bootstrap := req.BootstrapDNS
	if bootstrap == nil {
		if cur, err := s.store.GetSettings(r.Context()); err == nil {
			bootstrap = cur.BootstrapDNS
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pool, err := resolver.NewPoolForTest([]string{up}, bootstrap)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "upstream": up, "error": "上游不可用: " + err.Error(),
		})
		return
	}
	defer pool.Close()

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(qname), dns.TypeA)
	start := time.Now()
	resp, err := pool.Exchange(ctx, msg)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "upstream": up, "domain": qname,
			"elapsed_ms": elapsed, "error": "解析失败: " + err.Error(),
		})
		return
	}

	answers := make([]string, 0, 8)
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			answers = append(answers, a.A.String())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "upstream": up, "domain": qname,
		"elapsed_ms": elapsed, "answers": answers,
		"bootstrap_dns": bootstrap,
	})
}

func validateSettings(s *model.GlobalSettings) error {
	switch s.BlockingMode {
	case model.BlockingNXDOMAIN, model.BlockingNullIP, model.BlockingRefused, model.BlockingCustomIP:
	default:
		return errValidation("blocking_mode 只能是 nxdomain / null_ip / refused / custom_ip")
	}
	if s.BlockingMode == model.BlockingCustomIP && s.CustomIP == "" {
		return errValidation("blocking_mode 为 custom_ip 时必须填写 custom_ip")
	}
	switch s.FallbackPolicy {
	case model.FallbackPassthrough, model.FallbackRefused, model.FallbackBlocklistOnly:
	default:
		return errValidation("fallback_policy 只能是 passthrough / refused / blocklist_only")
	}
	if s.Domain != "" {
		if strings.ContainsAny(s.Domain, " /:\\") {
			return errValidation("domain 不能包含空格、斜杠或冒号")
		}
	}
	if len(s.Upstreams) == 0 {
		return errValidation("至少需要一个上游 DNS")
	}
	if s.CacheMaxTTL < s.CacheMinTTL {
		return errValidation("cache_max_ttl 不能小于 cache_min_ttl")
	}
	if s.MaxInflight < 10 || s.MaxInflight > 100000 {
		return errValidation("max_inflight 需在 10 到 100000 之间")
	}
	if s.QueryTimeoutMS < 500 || s.QueryTimeoutMS > 60000 {
		return errValidation("query_timeout_ms 需在 500 到 60000 之间")
	}
	if s.SubIntervalH < 1 || s.SubIntervalH > 720 {
		return errValidation("sub_interval_hours 需在 1 到 720 之间")
	}
	// 配额上限：0 = 不限制，其余必须落在合理区间。
	// 不设上限的话一个管理员把 max_sub_entries 写成 1000 万，
	// 一次订阅就能把进程内存打爆 —— 这正是这个功能要防的事。
	if s.MaxRulesPerUser < 0 || s.MaxRulesPerUser > 100000 {
		return errValidation("max_rules_per_user 需在 0 到 100000 之间（0 = 不限制）")
	}
	if s.MaxSubsPerUser < 0 || s.MaxSubsPerUser > 200 {
		return errValidation("max_subs_per_user 需在 0 到 200 之间（0 = 不限制）")
	}
	if s.MaxSubEntries < 0 || s.MaxSubEntries > 2000000 {
		return errValidation("max_sub_entries 需在 0 到 2000000 之间（0 = 不限制）")
	}
	if s.MaxRulesGlobal < 0 || s.MaxRulesGlobal > 100000 {
		return errValidation("max_rules_global 需在 0 到 100000 之间（0 = 不限制）")
	}
	if s.MaxSubsGlobal < 0 || s.MaxSubsGlobal > 200 {
		return errValidation("max_subs_global 需在 0 到 200 之间（0 = 不限制）")
	}
	return nil
}

func (s *Server) handleAdminRebuildSnapshot(w http.ResponseWriter, r *http.Request) {
	v, err := s.coord.Rebuild(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "message": "快照已重建，dnsd 将在 2 秒内生效"})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	rows, err := s.store.RecentAudit(r.Context(), limit)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// === 系统状态 ===

func (s *Server) handleAdminSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userCount, _ := s.store.CountUsers(ctx)
	configVersion, _ := s.store.ConfigVersion(ctx)
	snapVer, snapAt, snapErr := s.coord.Stats()

	var snapUsers, snapRules int
	if snap, err := s.coord.Snapshot(); err == nil && snap != nil {
		snapUsers = len(snap.Users)
		for _, u := range snap.Users {
			snapRules += len(u.Rules) + len(u.SubBlock) + len(u.SubAllow)
		}
	}
	entries, _ := s.store.CountSubEntriesAll(ctx)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	certDaysLeft := -1
	if s.certs != nil && s.certs.NotAfter() > 0 {
		certDaysLeft = int(time.Until(time.Unix(s.certs.NotAfter(), 0)).Hours() / 24)
	}

	snapErrText := ""
	if snapErr != nil {
		snapErrText = snapErr.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"component":        "apid",
		"version":          version.Version,
		"commit":           version.Commit,
		"build_time":       version.BuildTime,
		"go_version":       runtime.Version(),
		"platform":         runtime.GOOS + "/" + runtime.GOARCH,
		"uptime_seconds":   s.uptimeSeconds(),
		"started_at":       s.startedAt.UTC().Format(time.RFC3339),
		"users":            userCount,
		"config_version":   configVersion,
		"snapshot_version": snapVer,
		"snapshot_at":      rfc3339OrEmpty(snapAt.Unix()),
		"snapshot_error":   snapErrText,
		"snapshot_users":   snapUsers,
		"snapshot_rules":   snapRules,
		"sub_entries":      entries,
		"memory_alloc_mb":  float64(m.Alloc) / 1024 / 1024,
		"memory_sys_mb":    float64(m.Sys) / 1024 / 1024,
		"goroutines":       runtime.NumGoroutine(),
		"cert_days_left":   certDaysLeft,
		"cert_fingerprint": s.fingerprint(),
		"domain":           s.domain(r),
		"listen": map[string]any{
			"api":     s.cfg.API.Listen,
			"api_tls": s.cfg.API.TLSListen,
			"dns_udp": s.cfg.DNS.ListenUDP,
			"dns_tcp": s.cfg.DNS.ListenTCP,
			"dns_dot": s.cfg.DNS.ListenDOT,
			"dns_doh": s.cfg.DNS.ListenDOH,
		},
	})
}

// === 在线更新 ===

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	repo := set.UpdateRepo
	if repo == "" {
		repo = s.cfg.Update.Repo
	}
	channel := set.UpdateChannel
	if channel == "" {
		channel = s.cfg.Update.Channel
	}
	component := "apid"
	if c := r.URL.Query().Get("component"); c != "" {
		component = c
	}

	res, err := selfupdate.Check(ctx, repo, selfupdate.ComponentBinaryName(component), channel, version.Version)
	if err != nil {
		// 检查更新失败不是致命错误：网络受限的部署环境很常见。
		writeJSON(w, http.StatusOK, map[string]any{
			"current":          version.Version,
			"update_available": false,
			"reason":           "检查更新失败：" + err.Error(),
		})
		return
	}

	// 把结果落库，便于前端在检查失败时展示上一次已知版本。
	if res.UpdateAvailable {
		_ = s.store.SaveAppVersion(r.Context(), &model.AppVersion{
			Channel: channel, Version: res.Latest, URL: res.URL,
			SHA256: res.SHA256, Notes: res.Notes, ReleasedAt: time.Now().Unix(),
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAdminApplyUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Component string `json:"component"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Component == "" {
		req.Component = "apid"
	}
	if req.Component != "apid" && req.Component != "dnsd" && req.Component != "all" {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "component 只能是 apid / dnsd / all", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	repo := set.UpdateRepo
	if repo == "" {
		repo = s.cfg.Update.Repo
	}
	channel := set.UpdateChannel
	if channel == "" {
		channel = s.cfg.Update.Channel
	}
	if repo == "" {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "未配置更新仓库（update.repo）", nil)
		return
	}

	applied := []string{}
	messages := []string{}

	if req.Component == "dnsd" || req.Component == "all" {
		res, err := selfupdate.Check(ctx, repo, "aegis-dnsd", channel, version.Version)
		if err != nil {
			messages = append(messages, "检查 dnsd 更新失败："+err.Error())
		} else if !res.UpdateAvailable {
			messages = append(messages, "dnsd 已是最新（"+version.Version+"）")
		} else {
			target := s.dnsdBinaryPath()
			if err := selfupdate.ApplyFile(ctx, res.URL, res.SHA256, target); err != nil {
				messages = append(messages, "dnsd 更新失败："+err.Error())
			} else {
				applied = append(applied, "dnsd")
				if err := selfupdate.RestartService(ctx, "aegis-dnsd"); err != nil {
					messages = append(messages, "dnsd 二进制已替换，但自动重启失败："+err.Error())
				} else {
					messages = append(messages, "dnsd 已更新到 "+res.Latest+" 并重启")
				}
			}
		}
	}

	if req.Component == "apid" || req.Component == "all" {
		res, err := selfupdate.Check(ctx, repo, "aegis-apid", channel, version.Version)
		if err != nil {
			messages = append(messages, "检查 apid 更新失败："+err.Error())
		} else if !res.UpdateAvailable {
			messages = append(messages, "apid 已是最新（"+version.Version+"）")
		} else {
			if err := selfupdate.Apply(ctx, res.URL, res.SHA256); err != nil {
				messages = append(messages, "apid 更新失败："+err.Error())
			} else {
				applied = append(applied, "apid")
				messages = append(messages, "apid 已更新到 "+res.Latest+"，进程即将重启")
				// 先把响应写出去，再退出进程 —— 否则前端拿到的是连接中断。
				writeJSON(w, http.StatusOK, map[string]any{
					"applied": applied, "message": strings.Join(messages, "；"), "restarting": true,
				})
				go func() {
					time.Sleep(1500 * time.Millisecond)
					os.Exit(0)
				}()
				return
			}
		}
	}

	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_apply_update", strings.Join(applied, ","), clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": applied, "message": strings.Join(messages, "；"), "restarting": false,
	})
}

// dnsdBinaryPath 推断 dnsd 二进制路径。
//
// 约定：与 apid 二进制同目录、文件名把 apid 换成 dnsd。
// 部署脚本（deploy/install.sh）就是这么放的。
func (s *Server) dnsdBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/opt/aegis/aegis-dnsd"
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	if strings.Contains(name, "apid") {
		return filepath.Join(dir, strings.Replace(name, "apid", "dnsd", 1))
	}
	return filepath.Join(dir, "aegis-dnsd")
}
