package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

const metaSchemaKey = "schema_version"

// GetMeta 读取一个 meta 键，不存在返回空串。
func (s *Store) GetMeta(ctx context.Context, k string) (string, error) {
	var v string
	err := s.read.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 meta.%s 失败: %w", k, err)
	}
	return v, nil
}

// SetMeta 写入一个 meta 键。
func (s *Store) SetMeta(ctx context.Context, k, v string) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	if err != nil {
		return fmt.Errorf("写入 meta.%s 失败: %w", k, err)
	}
	return nil
}

// ConfigVersion 返回当前的配置版本号（不存在视为 0）。
func (s *Store) ConfigVersion(ctx context.Context) (int64, error) {
	v, err := s.GetMeta(ctx, "config_version")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("meta.config_version 值 %q 不是整数: %w", v, err)
	}
	return n, nil
}

// BumpConfigVersion 递增配置版本号并返回新值。
//
// 放在事务里做「读-改-写」，避免两个并发请求拿到同一个版本号 ——
// 版本号是 dnsd 判断「快照是否需要重载」的唯一依据，重复会导致丢更新。
func (s *Store) BumpConfigVersion(ctx context.Context) (int64, error) {
	var next int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var cur int64
		err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'config_version'`).Scan(&cur)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			cur = 0
		case err != nil:
			return err
		}
		next = cur + 1
		_, err = tx.ExecContext(ctx,
			`INSERT INTO meta (k, v) VALUES ('config_version', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
			strconv.FormatInt(next, 10))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("递增配置版本失败: %w", err)
	}
	return next, nil
}

// === 全局设置 ===

// GetSettings 读取全局设置，缺失的键用出厂默认值补齐。
func (s *Store) GetSettings(ctx context.Context) (*model.GlobalSettings, error) {
	// 命中进程内缓存就直接返回深拷贝。见 store.go 里 settings 字段的说明。
	s.settingsMu.RLock()
	cached := s.settings
	s.settingsMu.RUnlock()
	if cached != nil {
		return cloneSettings(cached), nil
	}

	set := model.DefaultSettings()
	// 记下「读库开始时的代际」：读库期间若有 SaveSettings 提交，代际会变，
	// 这份结果就是过期的，绝不能写回缓存（否则一次并发写之后所有请求都拿到旧设置）。
	s.settingsMu.RLock()
	gen := s.settingsGen
	s.settingsMu.RUnlock()
	rows, err := s.read.QueryContext(ctx, `SELECT k, v FROM meta WHERE k LIKE 'setting.%'`)
	if err != nil {
		return nil, fmt.Errorf("读取全局设置失败: %w", err)
	}
	defer rows.Close()

	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		kv[strings.TrimPrefix(k, "setting.")] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 逐字段覆盖。用显式赋值而不是反射/JSON，是为了让「设置项改名」在编译期暴露。
	if v, ok := kv["domain"]; ok {
		set.Domain = v
	}
	if v, ok := kv["upstreams"]; ok && v != "" {
		set.Upstreams = strings.Split(v, ",")
	}
	if v, ok := kv["proxy_url"]; ok {
		set.ProxyURL = v
	}
	if v, ok := kv["proxy_enabled"]; ok {
		set.ProxyEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	// AI 诊断配置。空串合法（例如清掉密钥），所以用 if v, ok 而非 if v != ""。
	if v, ok := kv["ai_enabled"]; ok {
		set.AIEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["ai_base_url"]; ok {
		set.AIBaseURL = v
	}
	if v, ok := kv["ai_model"]; ok {
		set.AIModel = v
	}
	if v, ok := kv["ai_key_encrypted"]; ok {
		set.AIKeyEncrypted = v
	}
	if v, ok := kv["ai_timeout_sec"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			set.AITimeoutSec = n
		}
	}
	if v, ok := kv["ai_max_per_hour"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			set.AIMaxPerHour = n
		}
	}
	if v, ok := kv["ai_trigger_mode"]; ok && v != "" {
		set.AITriggerMode = v
	}
	if v, ok := kv["ai_auto_handle"]; ok {
		// 用 boolStr 的反向解析：空串视为未配置，保持默认（false）。
		set.AIAutoHandle = v == "1" || v == "true"
	}
	if v, ok := kv["bootstrap_dns"]; ok {
		// 允许显式清空（空串 → 空切片），此时回退到系统解析器。
		set.BootstrapDNS = splitList(v)
	}
	if v, ok := kv["blocking_mode"]; ok && v != "" {
		set.BlockingMode = v
	}
	if v, ok := kv["custom_ip"]; ok {
		set.CustomIP = v
	}
	if v, ok := kv["fallback_policy"]; ok && v != "" {
		set.FallbackPolicy = v
	}
	set.CacheSize = intOr(kv["cache_size"], set.CacheSize)
	set.CacheMinTTL = intOr(kv["cache_min_ttl"], set.CacheMinTTL)
	set.CacheMaxTTL = intOr(kv["cache_max_ttl"], set.CacheMaxTTL)
	set.RateLimitQPS = intOr(kv["rate_limit_qps"], set.RateLimitQPS)
	set.MaxInflight = intOr(kv["max_inflight"], set.MaxInflight)
	set.QueryTimeoutMS = intOr(kv["query_timeout_ms"], set.QueryTimeoutMS)
	set.QueryLogSize = intOr(kv["query_log_size"], set.QueryLogSize)
	set.SubIntervalH = intOr(kv["sub_interval_hours"], set.SubIntervalH)
	set.SubRefreshHour = intOr(kv["sub_refresh_hour"], set.SubRefreshHour)
	set.SubRefreshMinute = intOr(kv["sub_refresh_minute"], set.SubRefreshMinute)
	set.SubSpreadMinutes = intOr(kv["sub_spread_minutes"], set.SubSpreadMinutes)
	set.SubFetchTimeoutSec = intOr(kv["sub_fetch_timeout_sec"], set.SubFetchTimeoutSec)
	// 日志保留改成按分钟。老库里是 query_log_retention_days，按天换算过来。
	set.LogRetentionMin = intOr(kv["query_log_retention_min"],
		intOr(kv["query_log_retention_days"], 0)*1440)
	if set.LogRetentionMin <= 0 {
		set.LogRetentionMin = 60
	}
	if v, ok := kv["enable_query_log"]; ok {
		set.EnableQueryLog = v == "1" || strings.EqualFold(v, "true")
	}
	// 协议开关：没存过就沿用默认（全开），不能让老库升级后协议全关
	if v, ok := kv["enable_doh"]; ok {
		set.EnableDoH = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["enable_dot"]; ok {
		set.EnableDoT = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["enable_plain_dns"]; ok {
		set.EnablePlainDNS = v == "1" || strings.EqualFold(v, "true")
	}
	// 描述文件开关。老库里没有这三个键，缺省时保留 DefaultSettings 的全开值，
	// 否则升级后 iOS 用户的描述文件入口会全部消失。
	for _, kvp := range []struct {
		key string
		dst *bool
	}{
		{"enable_ios_profile", &set.EnableProfile},
		{"profile_doh", &set.ProfileDoH},
		{"profile_dot", &set.ProfileDoT},
	} {
		if v, ok := kv[kvp.key]; ok {
			*kvp.dst = v == "1" || strings.EqualFold(v, "true")
		}
	}
	// 用户协议。正文与标题都允许被显式清空（用 ok 判断而不是非空判断），
	// 否则管理员想删掉正文时，会被「空串 = 没存过」的写法悄悄还原成默认稿。
	if v, ok := kv["terms_enabled"]; ok {
		set.TermsEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["terms_title"]; ok {
		set.TermsTitle = v
	}
	if v, ok := kv["terms_version"]; ok {
		set.TermsVersion = v
	}
	if v, ok := kv["terms_content"]; ok {
		set.TermsContent = v
	}
	if v, ok := kv["allow_register"]; ok {
		set.AllowRegister = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["invite_code"]; ok {
		set.InviteCode = v
	}
	if v, ok := kv["update_repo"]; ok {
		set.UpdateRepo = v
	}
	if v, ok := kv["update_channel"]; ok && v != "" {
		set.UpdateChannel = v
	}

	// 配额之前只写不读 —— 后台改了也不生效，一直用的是 DefaultSettings 的值。
	set.MaxSubEntries = intOr(kv["max_sub_entries"], set.MaxSubEntries)
	set.FreeMaxRules = intOr(kv["free_max_rules"], set.FreeMaxRules)
	set.FreeMaxSubs = intOr(kv["free_max_subs"], set.FreeMaxSubs)
	set.FreeDailyQueries = int64Or(kv["free_daily_queries"], set.FreeDailyQueries)
	set.VipMaxRules = intOr(kv["vip_max_rules"], set.VipMaxRules)
	set.VipMaxSubs = intOr(kv["vip_max_subs"], set.VipMaxSubs)
	set.VipDailyQueries = int64Or(kv["vip_daily_queries"], set.VipDailyQueries)
	set.MemberRewardDays = intOr(kv["member_reward_days"], set.MemberRewardDays)
	// 奖励发放的会员种类：注册赠送 / 签到 / 邀请各一个。
	// 空串是合法值（= 默认会员档），所以不能只在非空时覆盖。
	if v, ok := kv["signup_member_tier"]; ok {
		set.SignupMemberTier = v
	}
	if v, ok := kv["checkin_tier"]; ok {
		set.CheckinTier = v
	}
	if v, ok := kv["member_reward_tier"]; ok {
		set.MemberRewardTier = v
	}
	// 日报的两个设置要单独读：intOr/boolOr 在键不存在时保留默认值，
	// 而默认是「开 + 9 点」，所以老库升级上来不用迁移也不会失效。
	// 键不存在时保留默认（开 + 9 点），老库升级上来不迁移也能用。
	if v, ok := kv["daily_report_enabled"]; ok {
		set.DailyReportEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	set.DailyReportHour = intOr(kv["daily_report_hour"], set.DailyReportHour)
	set.UserInviteMaxCodes = intOr(kv["user_invite_max_codes"], set.UserInviteMaxCodes)
	set.SignupMemberDays = intOr(kv["signup_member_days"], set.SignupMemberDays)
	set.CheckinDays = intOr(kv["checkin_days"], set.CheckinDays)
	// 累计签到奖励。老库里存的是 checkin_streak_days / checkin_streak_bonus
	//（按连续签到计），升级后必须继续生效，否则管理员配好的 7/7 会变成 0/0。
	set.CheckinBonusEvery = intOr(kv["checkin_bonus_every"], intOr(kv["checkin_streak_days"], set.CheckinBonusEvery))
	set.CheckinBonusDays = intOr(kv["checkin_bonus_days"], intOr(kv["checkin_streak_bonus"], set.CheckinBonusDays))
	// 一组布尔开关：逐个写 if 太啰嗦且容易漏，用表驱动。
	for _, kvp := range []struct {
		key string
		dst *bool
	}{
		{"allow_user_invite", &set.AllowUserInvite},
		{"checkin_enabled", &set.CheckinEnabled},
		{"email_verify_register", &set.EmailVerifyRegister},
		{"allow_email_change", &set.AllowEmailChange},
		{"feedback_notify_enabled", &set.FeedbackNotifyEnabled},
		{"feedback_notify_user_reply", &set.FeedbackNotifyUserReply},
		{"geetest_enabled", &set.GeetestEnabled},
		{"geetest_on_register", &set.GeetestOnRegister},
		{"geetest_on_login", &set.GeetestOnLogin},
		{"geetest_on_forgot", &set.GeetestOnForgot},
		{"geetest_on_feedback", &set.GeetestOnFeedback},
		{"enable_ecs", &set.EnableECS},
		{"enable_dnssec", &set.EnableDNSSEC},
	} {
		if v, ok := kv[kvp.key]; ok {
			*kvp.dst = v == "1" || strings.EqualFold(v, "true")
		}
	}
	set.EmailDomainWhitelist = strOr(kv["email_domain_whitelist"], set.EmailDomainWhitelist)

	set.SMTPHost = strOr(kv["smtp_host"], set.SMTPHost)
	set.SMTPPort = intOr(kv["smtp_port"], set.SMTPPort)
	set.SMTPUser = strOr(kv["smtp_user"], set.SMTPUser)
	set.SMTPPass = strOr(kv["smtp_pass"], set.SMTPPass)
	set.FeedbackNotifyEmail = strOr(kv["feedback_notify_email"], set.FeedbackNotifyEmail)
	set.GeetestID = strOr(kv["geetest_id"], set.GeetestID)
	set.GeetestKeyEncrypted = strOr(kv["geetest_key"], set.GeetestKeyEncrypted)
	set.SMTPFrom = strOr(kv["smtp_from"], set.SMTPFrom)
	set.SMTPFromName = strOr(kv["smtp_from_name"], set.SMTPFromName)
	set.SMTPTLS = strOr(kv["smtp_tls"], set.SMTPTLS)
	if v, ok := kv["smtp_enabled"]; ok {
		set.SMTPEnabled = v == "1" || strings.EqualFold(v, "true")
	}

	set.StatsRetentionD = intOr(kv["stats_retention_days"], set.StatsRetentionD)
	set.MaxIDResets = intOr(kv["max_id_resets"], set.MaxIDResets)
	if v, ok := kv["root_domain_reserved"]; ok {
		set.RootDomainReserved = v == "1"
	}
	if v, ok := kv["allow_root_domain"]; ok {
		set.AllowRootDomain = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["allow_user_rules"]; ok {
		set.AllowUserRules = v == "1" || strings.EqualFold(v, "true")
	}
	if v, ok := kv["allow_user_subs"]; ok {
		set.AllowUserSubs = v == "1" || strings.EqualFold(v, "true")
	}

	// 回填缓存。这里存的是即将返回的那一份的深拷贝，调用方后续就地改动
	// 不会污染缓存（返回 &set 本身，所以必须拷一次存进去）。
	// 只在读库期间没有并发提交的前提下回填，见上面 gen 的说明。
	s.settingsMu.Lock()
	if s.settingsGen == gen {
		s.settings = cloneSettings(&set)
	}
	s.settingsMu.Unlock()

	return &set, nil
}

func intOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// SaveSettings 保存全局设置（全量覆盖）。
func (s *Store) SaveSettings(ctx context.Context, set *model.GlobalSettings) error {
	pairs := map[string]string{
		"domain":                  set.Domain,
		"upstreams":               strings.Join(set.Upstreams, ","),
		"bootstrap_dns":           strings.Join(set.BootstrapDNS, ","),
		"proxy_url":               set.ProxyURL,
		"proxy_enabled":           boolStr(set.ProxyEnabled),
		"blocking_mode":           set.BlockingMode,
		"custom_ip":               set.CustomIP,
		"fallback_policy":         set.FallbackPolicy,
		"cache_size":              strconv.Itoa(set.CacheSize),
		"cache_min_ttl":           strconv.Itoa(set.CacheMinTTL),
		"cache_max_ttl":           strconv.Itoa(set.CacheMaxTTL),
		"rate_limit_qps":          strconv.Itoa(set.RateLimitQPS),
		"max_inflight":            strconv.Itoa(set.MaxInflight),
		"query_timeout_ms":        strconv.Itoa(set.QueryTimeoutMS),
		"query_log_size":          strconv.Itoa(set.QueryLogSize),
		"query_log_retention_min": strconv.Itoa(set.LogRetentionMin),
		"enable_query_log":        boolStr(set.EnableQueryLog),
		"enable_doh":              boolStr(set.EnableDoH),
		"enable_dot":              boolStr(set.EnableDoT),
		"enable_plain_dns":        boolStr(set.EnablePlainDNS),
		"enable_ios_profile":      boolStr(set.EnableProfile),
		"profile_doh":             boolStr(set.ProfileDoH),
		"profile_dot":             boolStr(set.ProfileDoT),
		"terms_enabled":           boolStr(set.TermsEnabled),
		"terms_title":             set.TermsTitle,
		"terms_version":           set.TermsVersion,
		"terms_content":           set.TermsContent,
		"allow_register":          boolStr(set.AllowRegister),
		"invite_code":             set.InviteCode,
		"update_repo":             set.UpdateRepo,
		"update_channel":          set.UpdateChannel,
		"sub_interval_hours":      strconv.Itoa(set.SubIntervalH),
		"sub_refresh_hour":        strconv.Itoa(set.SubRefreshHour),
		"sub_refresh_minute":      strconv.Itoa(set.SubRefreshMinute),
		"sub_spread_minutes":      strconv.Itoa(set.SubSpreadMinutes),
		"sub_fetch_timeout_sec":   strconv.Itoa(set.SubFetchTimeoutSec),

		// AI 诊断
		"ai_enabled":       boolStr(set.AIEnabled),
		"ai_base_url":      set.AIBaseURL,
		"ai_model":         set.AIModel,
		"ai_key_encrypted": set.AIKeyEncrypted,
		"ai_timeout_sec":   strconv.Itoa(set.AITimeoutSec),
		"ai_max_per_hour":  strconv.Itoa(set.AIMaxPerHour),
		"ai_trigger_mode":  set.AITriggerMode,
		"ai_auto_handle":   boolStr(set.AIAutoHandle),

		// 配额与新增开关：之前没写进 pairs，等于后台永远存不住。
		"max_sub_entries":      strconv.Itoa(set.MaxSubEntries),
		"free_max_rules":       strconv.Itoa(set.FreeMaxRules),
		"free_max_subs":        strconv.Itoa(set.FreeMaxSubs),
		"free_daily_queries":   strconv.FormatInt(set.FreeDailyQueries, 10),
		"vip_max_rules":        strconv.Itoa(set.VipMaxRules),
		"vip_max_subs":         strconv.Itoa(set.VipMaxSubs),
		"vip_daily_queries":    strconv.FormatInt(set.VipDailyQueries, 10),
		"member_reward_days":   strconv.Itoa(set.MemberRewardDays),
		"signup_member_tier":   set.SignupMemberTier,
		"checkin_tier":         set.CheckinTier,
		"member_reward_tier":   set.MemberRewardTier,
		"daily_report_enabled": boolStr(set.DailyReportEnabled),
		"daily_report_hour":    strconv.Itoa(set.DailyReportHour),
		"signup_member_days":   strconv.Itoa(set.SignupMemberDays),
		"checkin_enabled":      boolStr(set.CheckinEnabled),
		"checkin_days":         strconv.Itoa(set.CheckinDays),
		"checkin_bonus_every":  strconv.Itoa(set.CheckinBonusEvery),
		"checkin_bonus_days":   strconv.Itoa(set.CheckinBonusDays),
		// 老键继续写：万一回滚到旧二进制，奖励设置不会丢
		"checkin_streak_days":   strconv.Itoa(set.CheckinBonusEvery),
		"checkin_streak_bonus":  strconv.Itoa(set.CheckinBonusDays),
		"allow_user_invite":     boolStr(set.AllowUserInvite),
		"user_invite_max_codes": strconv.Itoa(set.UserInviteMaxCodes),

		"smtp_enabled":               boolStr(set.SMTPEnabled),
		"smtp_host":                  set.SMTPHost,
		"smtp_port":                  strconv.Itoa(set.SMTPPort),
		"smtp_user":                  set.SMTPUser,
		"smtp_pass":                  set.SMTPPass,
		"smtp_from":                  set.SMTPFrom,
		"smtp_from_name":             set.SMTPFromName,
		"smtp_tls":                   set.SMTPTLS,
		"email_verify_register":      boolStr(set.EmailVerifyRegister),
		"email_domain_whitelist":     set.EmailDomainWhitelist,
		"allow_email_change":         boolStr(set.AllowEmailChange),
		"feedback_notify_enabled":    boolStr(set.FeedbackNotifyEnabled),
		"feedback_notify_email":      set.FeedbackNotifyEmail,
		"feedback_notify_user_reply": boolStr(set.FeedbackNotifyUserReply),
		"geetest_enabled":            boolStr(set.GeetestEnabled),
		"geetest_id":                 set.GeetestID,
		"geetest_key":                set.GeetestKeyEncrypted,
		"geetest_on_register":        boolStr(set.GeetestOnRegister),
		"geetest_on_login":           boolStr(set.GeetestOnLogin),
		"geetest_on_forgot":          boolStr(set.GeetestOnForgot),
		"geetest_on_feedback":        boolStr(set.GeetestOnFeedback),
		"enable_ecs":                 boolStr(set.EnableECS),
		"enable_dnssec":              boolStr(set.EnableDNSSEC),

		"stats_retention_days": strconv.Itoa(set.StatsRetentionD),
		"max_id_resets":        strconv.Itoa(set.MaxIDResets),
		"allow_root_domain":    boolStr(set.AllowRootDomain),
		"root_domain_reserved": boolStr(set.RootDomainReserved),
		"allow_user_rules":     boolStr(set.AllowUserRules),
		"allow_user_subs":      boolStr(set.AllowUserSubs),
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for k, v := range pairs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
				"setting."+k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveSettingsAndInvalidate 保存设置并丢弃进程内缓存。
//
// 单独一个方法而不是把 invalidateSettings 塞进 SaveSettings 的 defer：
// s.tx 失败时事务已回滚，此刻丢缓存只会白读一次库，但更重要的是——
// 调用方必须显式表达「我这次写成功了」，缓存失效才有确定语义。
func (s *Store) SaveSettingsAndInvalidate(ctx context.Context, set *model.GlobalSettings) error {
	if err := s.SaveSettings(ctx, set); err != nil {
		return err
	}
	s.invalidateSettings()
	return nil
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// === 在线更新清单 ===

// SaveAppVersion 写入某渠道的最新版本信息。
func (s *Store) SaveAppVersion(ctx context.Context, v *model.AppVersion) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO app_versions (channel, version, url, sha256, notes, released_at)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(channel) DO UPDATE SET
		   version=excluded.version, url=excluded.url, sha256=excluded.sha256,
		   notes=excluded.notes, released_at=excluded.released_at`,
		v.Channel, v.Version, v.URL, v.SHA256, v.Notes, v.ReleasedAt)
	if err != nil {
		return fmt.Errorf("保存版本信息失败: %w", err)
	}
	return nil
}

// GetAppVersion 读取某渠道的版本信息。
func (s *Store) GetAppVersion(ctx context.Context, channel string) (*model.AppVersion, error) {
	var v model.AppVersion
	err := s.read.QueryRowContext(ctx,
		`SELECT channel, version, url, sha256, notes, released_at FROM app_versions WHERE channel = ?`,
		channel).Scan(&v.Channel, &v.Version, &v.URL, &v.SHA256, &v.Notes, &v.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取版本信息失败: %w", err)
	}
	return &v, nil
}

// LogAudit 写一条审计记录。失败不影响主流程，仅记录错误。
func (s *Store) LogAudit(ctx context.Context, userID, action, detail, ip string) {
	_, _ = s.write.ExecContext(ctx,
		`INSERT INTO audit_log (ts, user_id, action, detail, ip) VALUES (?,?,?,?,?)`,
		time.Now().Unix(), userID, action, detail, ip)
}

// splitList 把逗号分隔的字符串拆成切片，去空白与空项。
func splitList(v string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// int64Or 解析 int64 配置项，缺失或非法时返回默认值。
func int64Or(raw string, def int64) int64 {
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// strOr 返回配置值，缺失时用默认值。
func strOr(raw, def string) string {
	if raw == "" {
		return def
	}
	return raw
}
