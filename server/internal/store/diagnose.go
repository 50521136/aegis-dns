package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件是 AI 诊断需要的两类数据访问：
//   1. 把诊断结果写回 feedback（WriteAIDiagnosis / ResetAIDiagnosis）；
//   2. 为证据收集器提供查询能力（implement diagnose.Source 的一部分）。
//
// 单独成文件而不是塞进 feedback.go：诊断是可选特性，
// 出问题时能整块摘掉而不影响反馈主流程。

// WriteAIDiagnosis 把诊断结果写入反馈行。
//
// evidenceJSON 只在入库时写一次，之后不再更新 —— 它是「当时模型看到了什么」
// 的快照，用来复盘判断是否可信。覆盖它就失去了审计意义。
func (s *Store) WriteAIDiagnosis(ctx context.Context, f *model.Feedback) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET ai_status = ?, ai_verdict = ?, ai_reason = ?, ai_action = ?,
			ai_reply = ?, ai_domains = ?, ai_guard_notes = ?, ai_evidence = ?,
			ai_model = ?, ai_ran_at = ?
		 WHERE id = ?`,
		f.AIStatus, f.AIVerdict, f.AIReason, f.AIAction,
		f.AIReply, f.AIDomains, f.AIGuardNotes, f.AIEvidence,
		f.AIModel, time.Now().Unix(), f.ID)
	if err != nil {
		return fmt.Errorf("写入诊断结果失败: %w", err)
	}
	return nil
}

// MarkAIDiagnosisFailed 把诊断标记为失败，reason 写进 ai_reason 供排查。
func (s *Store) MarkAIDiagnosisFailed(ctx context.Context, fid, reason string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET ai_status = ?, ai_reason = ?, ai_ran_at = ? WHERE id = ?`,
		model.AIDiagFailed, reason, time.Now().Unix(), fid)
	if err != nil {
		return fmt.Errorf("标记诊断失败: %w", err)
	}
	return nil
}

// ResetAIDiagnosis 清空诊断字段（重新诊断前调用，避免旧结论残留）。
func (s *Store) ResetAIDiagnosis(ctx context.Context, fid string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET ai_status = ?, ai_verdict = '', ai_reason = '', ai_action = '',
			ai_reply = '', ai_domains = '', ai_guard_notes = '', ai_model = ''
		 WHERE id = ?`, model.AIDiagPending, fid)
	if err != nil {
		return fmt.Errorf("重置诊断状态失败: %w", err)
	}
	return nil
}

// GlobalRulesForDiagnose 返回全局规则（诊断用）。
//
// 与 ListRules 的区别：这里不分页、不看 enabled 过滤参数，一次取全 ——
// 诊断需要完整规则集才能复现 dnsd 的匹配结果。
func (s *Store) GlobalRulesForDiagnose(ctx context.Context) ([]model.Rule, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, user_id, kind, pattern, value, qtypes, enabled, created_at, is_global
		 FROM rules WHERE is_global = 1 ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("读取全局规则失败: %w", err)
	}
	defer rows.Close()
	var out []model.Rule
	for rows.Next() {
		var r model.Rule
		var qtypes string
		var enabled int
		if err := rows.Scan(&r.ID, &r.UserID, &r.Kind, &r.Pattern, &r.Value,
			&qtypes, &enabled, &r.CreatedAt, &r.IsGlobal); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if qtypes != "" {
			r.QTypes = strings.Split(qtypes, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserRulesForDiagnose 返回某用户的全部规则（诊断用，不分页）。
func (s *Store) UserRulesForDiagnose(ctx context.Context, uid string) ([]model.Rule, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, user_id, kind, pattern, value, qtypes, enabled, created_at, is_global
		 FROM rules WHERE user_id = ? ORDER BY created_at`, uid)
	if err != nil {
		return nil, fmt.Errorf("读取用户规则失败: %w", err)
	}
	defer rows.Close()
	var out []model.Rule
	for rows.Next() {
		var r model.Rule
		var qtypes string
		var enabled int
		if err := rows.Scan(&r.ID, &r.UserID, &r.Kind, &r.Pattern, &r.Value,
			&qtypes, &enabled, &r.CreatedAt, &r.IsGlobal); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		if qtypes != "" {
			r.QTypes = strings.Split(qtypes, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SubSourcesOf 反查这些域名分别来自哪些订阅。
//
// 一个域名可能同时出现在多个订阅里（常见：好几份公共列表互相抄），
// 所以返回的是列表。诊断里「来自哪个订阅」比「来自几个订阅」更有用，
// 但全部列出能让管理员判断要不要换源。
func (s *Store) SubSourcesOf(ctx context.Context, domains []string) (map[string][]SubSourceRow, error) {
	out := make(map[string][]SubSourceRow, len(domains))
	if len(domains) == 0 {
		return out, nil
	}
	// 分批查，避免 IN (...) 参数过多触发 SQLite 变量上限。
	const batch = 200
	for start := 0; start < len(domains); start += batch {
		end := start + batch
		if end > len(domains) {
			end = len(domains)
		}
		chunk := domains[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, d := range chunk {
			args = append(args, d)
		}
		rows, err := s.read.QueryContext(ctx,
			`SELECT e.domain, e.sub_id, e.kind, sb.name, sb.url, sb.is_global
			 FROM sub_entries e JOIN subscriptions sb ON sb.id = e.sub_id
			 WHERE e.domain IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("反查订阅来源失败: %w", err)
		}
		for rows.Next() {
			var r SubSourceRow
			var isGlobal int
			if err := rows.Scan(&r.Domain, &r.SubID, &r.Kind, &r.Name, &r.URL, &isGlobal); err != nil {
				rows.Close()
				return nil, err
			}
			r.Global = isGlobal == 1
			out[r.Domain] = append(out[r.Domain], r)
		}
		rows.Close()
	}
	return out, nil
}

// SubSourceRow 是「域名 → 订阅」的一行。
type SubSourceRow struct {
	Domain string
	SubID  string
	Kind   string // block / allow
	Name   string
	URL    string
	Global bool
}

// RecentQueriesOfDomain 返回某用户某域名的近期查询记录（倒序）。
//
// 返回查询样本与被拦截次数两个值：诊断需要区分「用户确实被拦了」和
// 「用户以为被拦但实际放行了」—— 后者是客户端缓存或应用层问题，
// 不该动规则。
func (s *Store) RecentQueriesOfDomain(ctx context.Context, uid, domain string, since int64, limit int) ([]QueryRow, int, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT action, rcode, cache_hit, latency_ms, proto, ts
		 FROM query_log WHERE user_id = ? AND domain = ? AND ts >= ?
		 ORDER BY ts DESC LIMIT ?`, uid, domain, since, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("查询近期记录失败: %w", err)
	}
	defer rows.Close()
	var out []QueryRow
	for rows.Next() {
		var q QueryRow
		var cacheHit int
		if err := rows.Scan(&q.Action, &q.RCode, &cacheHit, &q.LatencyMS, &q.Proto, &q.TS); err != nil {
			return nil, 0, err
		}
		q.CacheHit = cacheHit == 1
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var blocked int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM query_log WHERE user_id = ? AND domain = ? AND ts >= ? AND action = 'blocked'`,
		uid, domain, since).Scan(&blocked); err != nil {
		return out, 0, nil // 计数失败不影响主流程
	}
	return out, blocked, nil
}

// QueryRow 是一条查询日志（脱敏后）。
type QueryRow struct {
	Action    string
	RCode     string
	CacheHit  bool
	LatencyMS int64
	Proto     string
	TS        int64
}
