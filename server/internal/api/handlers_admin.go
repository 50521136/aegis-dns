package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/mail"
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
	// 档位表一次取出：列表里每个用户都要把种类 id 翻成显示名，
	// 逐个查库就是 N+1。
	tiers, _ := s.store.TierMap(r.Context())
	now := time.Now()
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		ruleCount, _ := s.store.CountRules(r.Context(), u.ID, false)
		ips, _ := s.store.ListUserIPs(r.Context(), u.ID)
		// 会员状态由后端算好（种类 + 有效期），前端只负责显示。
		// 漏掉这几个字段的表现是「列表里永远看不到会员徽章」——
		// 前端按 member_days/member_forever 判断，拿不到就当成非会员。
		mem := model.MemberStateOf(u, tiers, now)
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
			// 会员状态
			"member_until":   mem.Until,
			"member_days":    mem.Days,
			"member_forever": mem.Forever,
			"is_member":      mem.IsMember,
			"tier":           mem.Tier,
			"tier_name":      mem.TierName,
			"tier_id":        mem.AssignedTier,
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
	tiers, _ := s.store.TierMap(r.Context())
	writeJSON(w, http.StatusOK, toUserJSON(u, tiers))
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
		"domain":             set.Domain,
		"upstreams":          set.Upstreams,
		"upstream_mode":      upstreamModeOrDefault(set.UpstreamMode),
		"fallback_upstreams": orEmptyStrings(set.FallbackUpstreams),
		"bootstrap_dns":      set.BootstrapDNS,
		"proxy_url":          set.ProxyURL,
		"proxy_enabled":      set.ProxyEnabled,
		"blocking_mode":      set.BlockingMode,
		"custom_ip":          set.CustomIP,
		"fallback_policy":    set.FallbackPolicy,
		"cache_size":         set.CacheSize,
		"cache_min_ttl":      set.CacheMinTTL,
		"cache_max_ttl":      set.CacheMaxTTL,
		"rate_limit_qps":     set.RateLimitQPS,
		"max_inflight":       set.MaxInflight,
		"query_timeout_ms":   set.QueryTimeoutMS,
		"query_log_size":     set.QueryLogSize,
		"enable_query_log":   set.EnableQueryLog,
		"enable_doh":         set.EnableDoH,
		"enable_dot":         set.EnableDoT,
		"enable_plain_dns":   set.EnablePlainDNS,
		// 描述文件（iOS / macOS）开关。后两个是「描述文件内部允许哪种载荷」，
		// 受全局协议开关约束：生效值 = 全局 && 本开关（前端据此把开关置灰）。
		// 生效值的计算只发生在后端 protos() / 前端的表单联动各一处，
		// 不再额外下发一个「全局是否开着」的派生字段 —— 那就是 enable_doh 的副本，
		// 两处来源必然有一天对不上。
		"enable_ios_profile": set.EnableProfile,
		"profile_doh":        set.ProfileDoH,
		"profile_dot":        set.ProfileDoT,
		// 用户协议：正文可能很长，但后台表单要能编辑它，所以整段回传。
		"terms_enabled": set.TermsEnabled,
		"terms_title":   set.TermsTitle,
		"terms_version": set.TermsVersion,
		"terms_content": set.TermsContent,
		// 只读：默认正文，供后台「恢复默认」按钮使用。
		// 让前端自己存一份默认稿的话，改一次措辞就要两个地方同步。
		"terms_content_default":   model.DefaultTermsContent,
		"sub_interval_hours":      set.SubIntervalH,
		"sub_refresh_hour":        set.SubRefreshHour,
		"sub_refresh_minute":      set.SubRefreshMinute,
		"sub_spread_minutes":      set.SubSpreadMinutes,
		"sub_fetch_timeout_sec":   set.SubFetchTimeoutSec,
		"query_log_retention_min": set.LogRetentionMin,
		"allow_register":          set.AllowRegister,
		"invite_code":             set.InviteCode,
		"update_repo":             set.UpdateRepo,
		"update_channel":          set.UpdateChannel,

		// 配额：前端据此显示「已用 x / 上限 y」并在新建时提前拦。
		"max_sub_entries": set.MaxSubEntries,
		// 会员两档配额
		"free_max_rules":        set.FreeMaxRules,
		"free_max_subs":         set.FreeMaxSubs,
		"free_daily_queries":    set.FreeDailyQueries,
		"vip_max_rules":         set.VipMaxRules,
		"vip_max_subs":          set.VipMaxSubs,
		"vip_daily_queries":     set.VipDailyQueries,
		"member_reward_days":    set.MemberRewardDays,
		"daily_report_enabled":  set.DailyReportEnabled,
		"daily_report_hour":     set.DailyReportHour,
		"allow_user_invite":     set.AllowUserInvite,
		"user_invite_max_codes": set.UserInviteMaxCodes,
		"signup_member_days":    set.SignupMemberDays,
		// 奖励发放的会员种类（空串 = 默认会员档）。三处奖励各一个，
		// 前端据此渲染「赠送会员种类」下拉。
		"signup_member_tier":  set.SignupMemberTier,
		"checkin_tier":        set.CheckinTier,
		"member_reward_tier":  set.MemberRewardTier,
		"checkin_enabled":     set.CheckinEnabled,
		"checkin_days":        set.CheckinDays,
		"checkin_bonus_every": set.CheckinBonusEvery,
		"checkin_bonus_days":  set.CheckinBonusDays,
		"enable_ecs":          set.EnableECS,
		"enable_dnssec":       set.EnableDNSSEC,

		// 邮件服务。密码不回传：后台表单只需要知道「有没有配」，
		// 把明文密码发到浏览器等于把它暴露在任何一次截图/日志里。
		"smtp_enabled":           set.SMTPEnabled,
		"smtp_host":              set.SMTPHost,
		"smtp_port":              set.SMTPPort,
		"smtp_user":              set.SMTPUser,
		"smtp_pass_set":          set.SMTPPass != "",
		"smtp_from":              set.SMTPFrom,
		"smtp_from_name":         set.SMTPFromName,
		"smtp_tls":               set.SMTPTLS,
		"email_verify_register":  set.EmailVerifyRegister,
		"email_domain_whitelist": set.EmailDomainWhitelist,
		"allow_email_change":     set.AllowEmailChange,

		// 反馈邮件通知。
		"feedback_notify_enabled":    set.FeedbackNotifyEnabled,
		"feedback_notify_email":      set.FeedbackNotifyEmail,
		"feedback_notify_user_reply": set.FeedbackNotifyUserReply,

		// 极验 v4：私钥只回「是否已配置」，永不回显明文。
		"geetest_enabled":     set.GeetestEnabled,
		"geetest_id":          set.GeetestID,
		"geetest_key_set":     set.GeetestKeyEncrypted != "",
		"geetest_on_register": set.GeetestOnRegister,
		"geetest_on_login":    set.GeetestOnLogin,
		"geetest_on_forgot":   set.GeetestOnForgot,
		"geetest_on_feedback": set.GeetestOnFeedback,

		// 主域名开关、清理周期、自助重置上限、租户侧总开关。
		// 这个 map 是手写的，加字段时这里必须同步 —— 漏了的话设置存进去了
		// 但前端读不到，表现为「改了没生效」。
		"allow_root_domain":    set.AllowRootDomain,
		"root_domain_reserved": set.RootDomainReserved,
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
	Domain            *string   `json:"domain"`
	Upstreams         *[]string `json:"upstreams"`
	UpstreamMode      *string   `json:"upstream_mode"`
	FallbackUpstreams *[]string `json:"fallback_upstreams"`
	BootstrapDNS      *[]string `json:"bootstrap_dns"`
	ProxyURL          *string   `json:"proxy_url"`
	ProxyEnabled      *bool     `json:"proxy_enabled"`
	BlockingMode      *string   `json:"blocking_mode"`
	CustomIP          *string   `json:"custom_ip"`
	FallbackPolicy    *string   `json:"fallback_policy"`
	CacheSize         *int      `json:"cache_size"`
	CacheMinTTL       *int      `json:"cache_min_ttl"`
	CacheMaxTTL       *int      `json:"cache_max_ttl"`
	RateLimitQPS      *int      `json:"rate_limit_qps"`
	MaxInflight       *int      `json:"max_inflight"`
	QueryTimeoutMS    *int      `json:"query_timeout_ms"`
	QueryLogSize      *int      `json:"query_log_size"`
	EnableQueryLog    *bool     `json:"enable_query_log"`
	// 协议开关用指针：漏传 = 不变，不会因为前端没带字段就把协议关掉
	EnableDoH      *bool `json:"enable_doh"`
	EnableDoT      *bool `json:"enable_dot"`
	EnablePlainDNS *bool `json:"enable_plain_dns"`
	// 描述文件（iOS / macOS）的三个开关，同样是「漏传 = 不变」
	EnableProfile *bool `json:"enable_ios_profile"`
	ProfileDoH    *bool `json:"profile_doh"`
	ProfileDoT    *bool `json:"profile_dot"`
	// 用户协议。正文用指针区分「没传」和「显式清空成空串」。
	TermsEnabled       *bool   `json:"terms_enabled"`
	TermsTitle         *string `json:"terms_title"`
	TermsVersion       *string `json:"terms_version"`
	TermsContent       *string `json:"terms_content"`
	SubIntervalH       *int    `json:"sub_interval_hours"`
	SubRefreshHour     *int    `json:"sub_refresh_hour"`
	SubRefreshMinute   *int    `json:"sub_refresh_minute"`
	SubSpreadMinutes   *int    `json:"sub_spread_minutes"`
	SubFetchTimeoutSec *int    `json:"sub_fetch_timeout_sec"`
	LogRetentionMin    *int    `json:"query_log_retention_min"`
	AllowRegister      *bool   `json:"allow_register"`
	InviteCode         *string `json:"invite_code"`
	UpdateRepo         *string `json:"update_repo"`
	UpdateChannel      *string `json:"update_channel"`

	MaxSubEntries      *int   `json:"max_sub_entries"`
	FreeMaxRules       *int   `json:"free_max_rules"`
	FreeMaxSubs        *int   `json:"free_max_subs"`
	FreeDailyQueries   *int64 `json:"free_daily_queries"`
	VipMaxRules        *int   `json:"vip_max_rules"`
	VipMaxSubs         *int   `json:"vip_max_subs"`
	VipDailyQueries    *int64 `json:"vip_daily_queries"`
	MemberRewardDays   *int   `json:"member_reward_days"`
	DailyReportEnabled *bool  `json:"daily_report_enabled"`
	DailyReportHour    *int   `json:"daily_report_hour"`
	AllowUserInvite    *bool  `json:"allow_user_invite"`
	UserInviteMaxCodes *int   `json:"user_invite_max_codes"`
	SignupMemberDays   *int   `json:"signup_member_days"`
	// 奖励发放的会员种类。用指针区分「没传」和「显式清空成默认档」。
	SignupMemberTier  *string `json:"signup_member_tier"`
	CheckinTier       *string `json:"checkin_tier"`
	MemberRewardTier  *string `json:"member_reward_tier"`
	CheckinEnabled    *bool   `json:"checkin_enabled"`
	CheckinDays       *int    `json:"checkin_days"`
	CheckinBonusEvery *int    `json:"checkin_bonus_every"`
	CheckinBonusDays  *int    `json:"checkin_bonus_days"`
	// 旧字段：老客户端/老脚本仍可能发这两个，收下来搬到新字段
	CheckinStreakDays    *int    `json:"checkin_streak_days"`
	CheckinStreakBonus   *int    `json:"checkin_streak_bonus"`
	EnableECS            *bool   `json:"enable_ecs"`
	EnableDNSSEC         *bool   `json:"enable_dnssec"`
	EmailDomainWhitelist *string `json:"email_domain_whitelist"`
	AllowEmailChange     *bool   `json:"allow_email_change"`

	FeedbackNotifyEnabled   *bool   `json:"feedback_notify_enabled"`
	FeedbackNotifyEmail     *string `json:"feedback_notify_email"`
	FeedbackNotifyUserReply *bool   `json:"feedback_notify_user_reply"`

	GeetestEnabled    *bool   `json:"geetest_enabled"`
	GeetestID         *string `json:"geetest_id"`
	GeetestKey        *string `json:"geetest_key"`
	GeetestKeyClear   *bool   `json:"geetest_key_clear"`
	GeetestOnRegister *bool   `json:"geetest_on_register"`
	GeetestOnLogin    *bool   `json:"geetest_on_login"`
	GeetestOnForgot   *bool   `json:"geetest_on_forgot"`
	GeetestOnFeedback *bool   `json:"geetest_on_feedback"`

	SMTPEnabled         *bool   `json:"smtp_enabled"`
	SMTPHost            *string `json:"smtp_host"`
	SMTPPort            *int    `json:"smtp_port"`
	SMTPUser            *string `json:"smtp_user"`
	SMTPPass            *string `json:"smtp_pass"`
	SMTPFrom            *string `json:"smtp_from"`
	SMTPFromName        *string `json:"smtp_from_name"`
	SMTPTLS             *string `json:"smtp_tls"`
	EmailVerifyRegister *bool   `json:"email_verify_register"`

	AllowRootDomain    *bool `json:"allow_root_domain"`
	RootDomainReserved *bool `json:"root_domain_reserved"`
	StatsRetentionD    *int  `json:"stats_retention_days"`
	MaxIDResets        *int  `json:"max_id_resets"`
	AllowUserRules     *bool `json:"allow_user_rules"`
	AllowUserSubs      *bool `json:"allow_user_subs"`
}

// validateSettingsPatch 校验请求里出现的字段，返回第一条错误。
//
// 单独抽出来而不是让 applySettingsPatch 返回 error：后者有几十个字段的
// 赋值，改成带返回值会牵动所有调用点，而真正需要提前拒绝的只有少数几个
// 枚举型字段（上游模式就属于这类 —— 写进库里再发现不认识就晚了）。
func validateSettingsPatch(p *settingsPatch) error {
	if p.UpstreamMode != nil {
		m := strings.TrimSpace(*p.UpstreamMode)
		if m != "" && !model.ValidUpstreamMode(m) {
			return fmt.Errorf("未知的上游选择模式 %q，可选：parallel / fastest / load_balance / hedge", m)
		}
	}
	return nil
}

// applySettingsPatch 把请求里出现过的字段写到 dst 上。
func applySettingsPatch(dst *model.GlobalSettings, p *settingsPatch) {
	if p.Domain != nil {
		dst.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(*p.Domain), "."))
	}
	if p.Upstreams != nil {
		dst.Upstreams = cleanList(*p.Upstreams)
	}
	if p.UpstreamMode != nil {
		// 合法性在 validateSettingsPatch 里先行校验，这里只做归一化。
		m := strings.TrimSpace(*p.UpstreamMode)
		if m == "" {
			m = model.UpstreamModeHedge
		}
		dst.UpstreamMode = m
	}
	if p.FallbackUpstreams != nil {
		dst.FallbackUpstreams = cleanList(*p.FallbackUpstreams)
	}
	if p.BootstrapDNS != nil {
		dst.BootstrapDNS = cleanList(*p.BootstrapDNS)
	}
	if p.ProxyURL != nil {
		dst.ProxyURL = strings.TrimSpace(*p.ProxyURL)
	}
	setBool(&dst.ProxyEnabled, p.ProxyEnabled)
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
	// 时刻允许 0（0 点刷新是合法需求），所以不能用「非零才覆盖」的 setInt。
	//
	// **不要在这里做区间判断后静默丢弃**：那样 validateSettings 永远看不到
	// 非法值，保存会返回 200，而管理员的 99 点被悄悄忽略 —— 他以为设上了。
	// 写进去，让 validateSettings 明确回 422。
	if p.SubRefreshHour != nil {
		dst.SubRefreshHour = *p.SubRefreshHour
	}
	if p.SubRefreshMinute != nil {
		dst.SubRefreshMinute = *p.SubRefreshMinute
	}
	// 摊开窗口与拉取超时：0 不是合法业务值，用 setInt 即可。
	setInt(&dst.SubSpreadMinutes, p.SubSpreadMinutes)
	setInt(&dst.SubFetchTimeoutSec, p.SubFetchTimeoutSec)
	setInt(&dst.LogRetentionMin, p.LogRetentionMin)
	setInt(&dst.MaxSubEntries, p.MaxSubEntries)
	setInt(&dst.FreeMaxRules, p.FreeMaxRules)
	setInt(&dst.FreeMaxSubs, p.FreeMaxSubs)
	setInt64(&dst.FreeDailyQueries, p.FreeDailyQueries)
	setInt(&dst.VipMaxRules, p.VipMaxRules)
	setInt(&dst.VipMaxSubs, p.VipMaxSubs)
	setInt64(&dst.VipDailyQueries, p.VipDailyQueries)
	setInt(&dst.MemberRewardDays, p.MemberRewardDays)
	setBool(&dst.DailyReportEnabled, p.DailyReportEnabled)
	// 小时要允许 0：0 点发日报是合法需求，
	// 所以不能用「非零才覆盖」的 setInt，单独判断指针。
	if p.DailyReportHour != nil && *p.DailyReportHour >= 0 && *p.DailyReportHour <= 23 {
		dst.DailyReportHour = *p.DailyReportHour
	}
	setBool(&dst.AllowUserInvite, p.AllowUserInvite)
	setInt(&dst.UserInviteMaxCodes, p.UserInviteMaxCodes)
	setInt(&dst.SignupMemberDays, p.SignupMemberDays)
	// 会员种类允许空串（= 默认会员档），所以不能用 setStr 之外的判断：
	// setStr 只在指针非 nil 时覆盖，正好符合「没传就不动」。
	setStr(&dst.SignupMemberTier, p.SignupMemberTier)
	setStr(&dst.CheckinTier, p.CheckinTier)
	setStr(&dst.MemberRewardTier, p.MemberRewardTier)
	setBool(&dst.CheckinEnabled, p.CheckinEnabled)
	setInt(&dst.CheckinDays, p.CheckinDays)
	setInt(&dst.CheckinBonusEvery, p.CheckinBonusEvery)
	setInt(&dst.CheckinBonusDays, p.CheckinBonusDays)
	// 兼容老字段名（按连续签到计奖励的年代）
	setInt(&dst.CheckinBonusEvery, p.CheckinStreakDays)
	setInt(&dst.CheckinBonusDays, p.CheckinStreakBonus)
	setBool(&dst.EnableECS, p.EnableECS)
	setBool(&dst.EnableDNSSEC, p.EnableDNSSEC)
	setBool(&dst.AllowEmailChange, p.AllowEmailChange)

	setBool(&dst.FeedbackNotifyEnabled, p.FeedbackNotifyEnabled)
	setStr(&dst.FeedbackNotifyEmail, p.FeedbackNotifyEmail)
	setBool(&dst.FeedbackNotifyUserReply, p.FeedbackNotifyUserReply)

	setBool(&dst.GeetestEnabled, p.GeetestEnabled)
	setStr(&dst.GeetestID, p.GeetestID)
	setBool(&dst.GeetestOnRegister, p.GeetestOnRegister)
	setBool(&dst.GeetestOnLogin, p.GeetestOnLogin)
	setBool(&dst.GeetestOnForgot, p.GeetestOnForgot)
	setBool(&dst.GeetestOnFeedback, p.GeetestOnFeedback)
	// 极验私钥不在这里处理：applySettingsPatch 是纯函数，拿不到加密器。
	// 由调用方（handleUpdateSettings）在保存前用 s.encryptSecret 写入，
	// 见 geetestKeyAction 的用法。
	if p.EmailDomainWhitelist != nil {
		dst.EmailDomainWhitelist = normalizeDomainList(*p.EmailDomainWhitelist)
	}
	setStr(&dst.SMTPHost, p.SMTPHost)
	setInt(&dst.SMTPPort, p.SMTPPort)
	setStr(&dst.SMTPUser, p.SMTPUser)
	// 密码留空表示「不修改」：前端拿不到原密码，回传的永远是空串，
	// 如果按空串覆盖，用户改任何别的设置都会把密码清掉。
	if p.SMTPPass != nil && *p.SMTPPass != "" {
		dst.SMTPPass = *p.SMTPPass
	}
	setStr(&dst.SMTPFrom, p.SMTPFrom)
	setStr(&dst.SMTPFromName, p.SMTPFromName)
	setStr(&dst.SMTPTLS, p.SMTPTLS)
	setBool(&dst.SMTPEnabled, p.SMTPEnabled)
	setBool(&dst.EmailVerifyRegister, p.EmailVerifyRegister)
	setInt(&dst.StatsRetentionD, p.StatsRetentionD)
	setInt(&dst.MaxIDResets, p.MaxIDResets)
	setBool(&dst.EnableQueryLog, p.EnableQueryLog)
	setBool(&dst.EnableDoH, p.EnableDoH)
	setBool(&dst.EnableDoT, p.EnableDoT)
	setBool(&dst.EnablePlainDNS, p.EnablePlainDNS)
	setBool(&dst.EnableProfile, p.EnableProfile)
	setBool(&dst.ProfileDoH, p.ProfileDoH)
	setBool(&dst.ProfileDoT, p.ProfileDoT)
	setBool(&dst.TermsEnabled, p.TermsEnabled)
	// 协议正文不 TrimSpace 掉换行：正文是排版好的多段文本，
	// 只去掉首尾空白，保留段内缩进与空行。
	if p.TermsTitle != nil {
		dst.TermsTitle = strings.TrimSpace(*p.TermsTitle)
	}
	if p.TermsVersion != nil {
		dst.TermsVersion = strings.TrimSpace(*p.TermsVersion)
	}
	if p.TermsContent != nil {
		dst.TermsContent = strings.Trim(*p.TermsContent, "\r\n 	")
	}
	setBool(&dst.AllowRegister, p.AllowRegister)
	setBool(&dst.AllowRootDomain, p.AllowRootDomain)
	setBool(&dst.RootDomainReserved, p.RootDomainReserved)
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

func setStr(dst *string, src *string) {
	if src != nil {
		*dst = strings.TrimSpace(*src)
	}
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setInt64(dst *int64, src *int64) {
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
// upstreamModeOrDefault 把空的模式字段补成 hedge（旧行为的默认值），
// 让前端拿到的永远是四个合法值之一，不必自己处理空串。
func upstreamModeOrDefault(m string) string {
	if model.ValidUpstreamMode(m) {
		return m
	}
	return model.UpstreamModeHedge
}

// orEmptyStrings 保证 JSON 里是 [] 而不是 null。
// 前端对 null 直接 .map 会抛异常，这个坑在 is_global 上踩过一次。
func orEmptyStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

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
	// 先校验再取库：非法枚举值不该等到读库之后才发现，
	// 而且能避免「读库成功、校验失败」这种半途状态。
	if err := validateSettingsPatch(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
		return
	}
	cur, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	applySettingsPatch(cur, &req)

	// 极验私钥：与 SMTPPass 同样「留空表示不修改」，否则改任何别的设置
	// 都会把它清空；显式 geetest_key_clear=true 才清空。
	// 加密必须在保存之前完成 —— 存进去的只能是密文。
	if req.GeetestKeyClear != nil && *req.GeetestKeyClear {
		cur.GeetestKeyEncrypted = ""
	} else if req.GeetestKey != nil && strings.TrimSpace(*req.GeetestKey) != "" {
		enc, encErr := s.encryptSecret(strings.TrimSpace(*req.GeetestKey))
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal,
				"保存极验私钥失败: "+encErr.Error(), nil)
			return
		}
		cur.GeetestKeyEncrypted = enc
	}

	// 奖励发放的会员种类必须指向真实存在的档位。放行的话发放时会静默
	// 回落到默认会员档 —— 管理员以为配了「签到送超级会员」，实际发的是 vip。
	for _, tid := range []string{cur.SignupMemberTier, cur.CheckinTier, cur.MemberRewardTier} {
		if strings.TrimSpace(tid) == "" {
			continue
		}
		if _, err := s.store.GetTier(r.Context(), tid); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
				"会员种类 "+tid+" 不存在，请先在「会员种类」里创建", nil)
			return
		}
	}

	if err := validateSettings(cur); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.store.SaveSettingsAndInvalidate(r.Context(), cur); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 代理改了要立刻生效，不必等重启。
	s.applyProxy(r.Context(), cur)

	// 设置里的 free/vip 配额先同步进 tiers 表，再重建快照。
	// 顺序不能反：配额换算以 tiers 为准，先重建的话快照里拿到的还是旧配额，
	// 表现就是「改了设置不生效」，要等下一次重建才偶然好。
	s.syncSettingsToTiers(r.Context())

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
	if src.LogRetentionMin > 0 {
		dst.LogRetentionMin = src.LogRetentionMin
	}
	if src.UpdateRepo != "" {
		dst.UpdateRepo = src.UpdateRepo
	}
	if src.UpdateChannel != "" {
		dst.UpdateChannel = src.UpdateChannel
	}
	// 配额：允许显式设成 0 表示「不限制」，因此不做非零判断。
	// 但负数没有意义，直接忽略。
	if src.MaxSubEntries >= 0 {
		dst.MaxSubEntries = src.MaxSubEntries
	}
	if src.FreeMaxRules >= 0 {
		dst.FreeMaxRules = src.FreeMaxRules
	}
	if src.FreeMaxSubs >= 0 {
		dst.FreeMaxSubs = src.FreeMaxSubs
	}
	if src.FreeDailyQueries >= 0 {
		dst.FreeDailyQueries = src.FreeDailyQueries
	}
	if src.VipMaxRules >= 0 {
		dst.VipMaxRules = src.VipMaxRules
	}
	if src.VipMaxSubs >= 0 {
		dst.VipMaxSubs = src.VipMaxSubs
	}
	if src.VipDailyQueries >= 0 {
		dst.VipDailyQueries = src.VipDailyQueries
	}
	if src.MemberRewardDays > 0 {
		dst.MemberRewardDays = src.MemberRewardDays
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
	dst.RootDomainReserved = src.RootDomainReserved
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

// applyProxy 把设置里的代理应用到订阅拉取与在线更新。
//
// 两处都要同步：改完代理只影响订阅、更新还在直连的话，
// 排查「订阅能拉了但更新还是超时」会非常费解。
func (s *Server) applyProxy(ctx context.Context, set *model.GlobalSettings) {
	raw := ""
	if set.ProxyEnabled {
		raw = set.ProxyURL
	}
	if s.subs != nil {
		if err := s.subs.SetProxy(raw); err != nil {
			s.log.Warn("订阅拉取的代理地址无效，保持原设置", "err", err)
		}
	}
	if err := selfupdate.SetProxy(raw); err != nil {
		s.log.Warn("在线更新的代理地址无效，保持原设置", "err", err)
	}
}

// ApplyProxyFromSettings 在启动时按已保存的设置初始化代理。
func (s *Server) ApplyProxyFromSettings(ctx context.Context) {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return
	}
	s.applyProxy(ctx, set)
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
	if s.SubRefreshHour < 0 || s.SubRefreshHour > 23 {
		return errValidation("sub_refresh_hour 需在 0 到 23 之间")
	}
	if s.SubRefreshMinute < 0 || s.SubRefreshMinute > 59 {
		return errValidation("sub_refresh_minute 需在 0 到 59 之间")
	}
	// 摊开窗口下限 1 分钟、上限一整天。窗口太小等于没摊开，
	// 太大会让一部分订阅隔天才轮到。
	if s.SubSpreadMinutes < 1 || s.SubSpreadMinutes > 1440 {
		return errValidation("sub_spread_minutes 需在 1 到 1440 之间")
	}
	// 拉取超时：下限 5 秒（再短连握手都不够），上限 600 秒。
	// 上限卡住是必要的 —— 有人填 100000 就等于让一个卡死的源占住整个窗口。
	if s.SubFetchTimeoutSec < 5 || s.SubFetchTimeoutSec > 600 {
		return errValidation("sub_fetch_timeout_sec 需在 5 到 600 之间")
	}
	// 配额上限：0 = 不限制，其余必须落在合理区间。
	// 不设上限的话一个管理员把 max_sub_entries 写成 1000 万，
	// 一次订阅就能把进程内存打爆 —— 这正是这个功能要防的事。
	if s.MaxSubEntries < 0 || s.MaxSubEntries > 2000000 {
		return errValidation("max_sub_entries 需在 0 到 2000000 之间（0 = 不限制）")
	}
	// 会员两档配额。免费档必须严格小于会员档，否则「开会员」这件事
	// 在配额上完全没有意义，运营还以为自己配错了。
	if s.FreeMaxRules < 0 || s.FreeMaxRules > 100000 {
		return errValidation("free_max_rules 需在 0 到 100000 之间（0 = 不限制）")
	}
	if s.VipMaxRules < 0 || s.VipMaxRules > 100000 {
		return errValidation("vip_max_rules 需在 0 到 100000 之间（0 = 不限制）")
	}
	if s.FreeMaxSubs < 0 || s.FreeMaxSubs > 200 {
		return errValidation("free_max_subs 需在 0 到 200 之间（0 = 不限制）")
	}
	if s.VipMaxSubs < 0 || s.VipMaxSubs > 200 {
		return errValidation("vip_max_subs 需在 0 到 200 之间（0 = 不限制）")
	}
	if s.FreeDailyQueries < 0 {
		return errValidation("free_daily_queries 不能为负（0 = 不限制）")
	}
	if s.VipDailyQueries < 0 {
		return errValidation("vip_daily_queries 不能为负（0 = 不限制）")
	}
	if s.MemberRewardDays < 1 || s.MemberRewardDays > 3650 {
		return errValidation("member_reward_days 需在 1 到 3650 之间")
	}
	if s.UserInviteMaxCodes < 0 || s.UserInviteMaxCodes > 200 {
		return errValidation("user_invite_max_codes 需在 0 到 200 之间（0 = 不允许用户自建）")
	}
	if s.SignupMemberDays < 0 || s.SignupMemberDays > 3650 {
		return errValidation("signup_member_days 需在 0 到 3650 之间（0 = 不赠送）")
	}
	if s.CheckinDays < 0 || s.CheckinDays > 365 {
		return errValidation("checkin_days 需在 0 到 365 之间")
	}
	// 三个协议全关等于服务对外不可用，直接拒绝保存
	if !s.EnableDoH && !s.EnableDoT && !s.EnablePlainDNS {
		return errValidation("DoH / DoT / 普通 DNS 至少要保留一个开启")
	}
	// 描述文件开着，但**全局还有可用协议**、描述文件却一个载荷都没开 ——
	// 这是自相矛盾的配置：用户页会出现能下载的入口，下下来却是装了不生效的文件。
	//
	// 注意不能无条件拦：管理员把 DoH / DoT 全关、只留明文 53 是合法操作，
	// 这时描述文件本来就没有可用载荷（前端也不会给入口），不该因此拒绝保存。
	if s.EnableProfile && (s.EnableDoH || s.EnableDoT) {
		if !(s.EnableDoH && s.ProfileDoH) && !(s.EnableDoT && s.ProfileDoT) {
			return errValidation("已开启描述文件下发，但 DoH / DoT 中没有任何一种对描述文件可用：" +
				"请同时打开全局协议开关与描述文件里对应的开关，或直接关闭描述文件下发")
		}
	}
	if s.CheckinBonusEvery < 0 || s.CheckinBonusEvery > 3650 {
		return errValidation("checkin_bonus_every 需在 0 到 3650 之间（0 = 不设累计奖励）")
	}
	// 开了协议勾选却没有正文：用户会被要求同意一份空白协议。
	// 这既是体验问题也是合规问题（无法证明他同意了什么）。
	if s.TermsEnabled {
		if strings.TrimSpace(s.TermsContent) == "" {
			return errValidation("已开启用户协议勾选，但协议正文为空：请填写正文或先关闭该开关")
		}
		if strings.TrimSpace(s.TermsTitle) == "" {
			return errValidation("已开启用户协议勾选，但协议标题为空")
		}
	}
	// 正文过长会撑爆 SQLite 单行与前端渲染。10 万字符足够写任何协议，
	// 超了基本是误粘贴（比如把整本法律文本贴进来）。
	if len([]rune(s.TermsContent)) > 100000 {
		return errValidation("协议正文超过 10 万字符，请精简后再保存")
	}
	if s.CheckinBonusDays < 0 || s.CheckinBonusDays > 3650 {
		return errValidation("checkin_bonus_days 需在 0 到 3650 之间")
	}
	// 邮件：只有启用时才要求配全，否则「先填一半保存」会被卡住。
	if s.SMTPEnabled {
		if strings.TrimSpace(s.SMTPHost) == "" {
			return errValidation("启用邮件服务时必须填写 SMTP 服务器")
		}
		if s.SMTPPort <= 0 || s.SMTPPort > 65535 {
			return errValidation("smtp_port 不合法")
		}
		if !strings.Contains(s.SMTPFrom, "@") {
			return errValidation("smtp_from 需为合法邮箱地址")
		}
		switch s.SMTPTLS {
		case mail.TLSNone, mail.TLSStartTLS, mail.TLSImplicit:
		default:
			return errValidation("smtp_tls 只能是 none / starttls / tls")
		}
		// 打开注册验证却没配邮件，等于把所有人挡在门外
		if s.EmailVerifyRegister {
			if err := (mail.Config{
				Host: s.SMTPHost, Port: s.SMTPPort, From: s.SMTPFrom,
				TLS: s.SMTPTLS, Enabled: true,
			}).Validate(); err != nil {
				return errValidation("邮件配置不完整: " + err.Error())
			}
		}
	} else if s.EmailVerifyRegister {
		return errValidation("开启注册邮箱验证前必须先启用并配好邮件服务")
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
