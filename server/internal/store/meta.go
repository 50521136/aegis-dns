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
	set := model.DefaultSettings()
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
	set.LogRetentionD = intOr(kv["query_log_retention_days"], set.LogRetentionD)
	if v, ok := kv["enable_query_log"]; ok {
		set.EnableQueryLog = v == "1" || strings.EqualFold(v, "true")
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
		"domain":                   set.Domain,
		"upstreams":                strings.Join(set.Upstreams, ","),
		"blocking_mode":            set.BlockingMode,
		"custom_ip":                set.CustomIP,
		"fallback_policy":          set.FallbackPolicy,
		"cache_size":               strconv.Itoa(set.CacheSize),
		"cache_min_ttl":            strconv.Itoa(set.CacheMinTTL),
		"cache_max_ttl":            strconv.Itoa(set.CacheMaxTTL),
		"rate_limit_qps":           strconv.Itoa(set.RateLimitQPS),
		"max_inflight":             strconv.Itoa(set.MaxInflight),
		"query_timeout_ms":         strconv.Itoa(set.QueryTimeoutMS),
		"query_log_size":           strconv.Itoa(set.QueryLogSize),
		"query_log_retention_days": strconv.Itoa(set.LogRetentionD),
		"enable_query_log":         boolStr(set.EnableQueryLog),
		"allow_register":           boolStr(set.AllowRegister),
		"invite_code":              set.InviteCode,
		"update_repo":              set.UpdateRepo,
		"update_channel":           set.UpdateChannel,
		"sub_interval_hours":       strconv.Itoa(set.SubIntervalH),
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
