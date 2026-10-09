package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// === 统计写入（dnsd 侧批量落库）===

// RollupBucket 是一个 5 分钟聚合桶，dnsd 每 60s 批量提交一次。
type RollupBucket struct {
	UserID     string
	TS         int64
	Total      int64
	Blocked    int64
	Cached     int64
	Allowed    int64
	LatencySum int64
}

// TopBucket 是一个 TOP 域名聚合桶。
type TopBucket struct {
	UserID  string
	TS      int64
	Domain  string
	Hits    int64
	Blocked int64
}

// WriteStats 在一个事务里批量写入 rollup、top 域名与查询日志。
//
// 单事务的意义：dnsd 每 60s 攒一批，一次提交。如果逐条提交，SQLite 每秒
// 都要 fsync，几百 QPS 就会把磁盘打满（文档 R4 的对策之一）。
func (s *Store) WriteStats(ctx context.Context, rollups []RollupBucket, tops []TopBucket, logs []model.QueryLogRow) error {
	if len(rollups) == 0 && len(tops) == 0 && len(logs) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if len(rollups) > 0 {
			stmt, err := tx.PrepareContext(ctx,
				`INSERT INTO query_rollup (user_id, ts, total, blocked, cached, allowed, latency_sum)
				 VALUES (?,?,?,?,?,?,?)
				 ON CONFLICT(user_id, ts) DO UPDATE SET
				   total = total + excluded.total,
				   blocked = blocked + excluded.blocked,
				   cached = cached + excluded.cached,
				   allowed = allowed + excluded.allowed,
				   latency_sum = latency_sum + excluded.latency_sum`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, r := range rollups {
				if _, err := stmt.ExecContext(ctx, r.UserID, r.TS, r.Total, r.Blocked, r.Cached, r.Allowed, r.LatencySum); err != nil {
					return err
				}
			}
		}
		if len(tops) > 0 {
			stmt, err := tx.PrepareContext(ctx,
				`INSERT INTO top_domains (user_id, ts, domain, hits, blocked)
				 VALUES (?,?,?,?,?)
				 ON CONFLICT(user_id, ts, domain) DO UPDATE SET
				   hits = hits + excluded.hits,
				   blocked = blocked + excluded.blocked`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, t := range tops {
				if _, err := stmt.ExecContext(ctx, t.UserID, t.TS, t.Domain, t.Hits, t.Blocked); err != nil {
					return err
				}
			}
		}
		if len(logs) > 0 {
			stmt, err := tx.PrepareContext(ctx,
				`INSERT INTO query_log (user_id, ts, domain, qtype, rcode, action, client, latency_ms)
				 VALUES (?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, l := range logs {
				if _, err := stmt.ExecContext(ctx, l.UserID, l.TS, l.Domain, l.QType, l.RCode, l.Action, l.Client, l.LatencyMS); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// === 统计读取（apid 侧）===

// StatsSummary 是总览统计。
type StatsSummary struct {
	Total      int64   `json:"total"`
	Blocked    int64   `json:"blocked"`
	Cached     int64   `json:"cached"`
	Allowed    int64   `json:"allowed"`
	BlockRate  float64 `json:"block_rate"`
	CacheRate  float64 `json:"cache_hit_rate"`
	AvgLatency float64 `json:"avg_latency_ms"`
}

// Summary 汇总某用户在某时间区间内的统计。
//
// uid 为空表示全局（管理员视角）；否则只统计该用户。
func (s *Store) Summary(ctx context.Context, uid string, since int64) (*StatsSummary, error) {
	where := []string{"ts >= ?"}
	args := []any{since}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	var out StatsSummary
	var latencySum int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total),0), COALESCE(SUM(blocked),0), COALESCE(SUM(cached),0),
		        COALESCE(SUM(allowed),0), COALESCE(SUM(latency_sum),0)
		 FROM query_rollup WHERE `+strings.Join(where, " AND "), args...).
		Scan(&out.Total, &out.Blocked, &out.Cached, &out.Allowed, &latencySum)
	if err != nil {
		return nil, fmt.Errorf("汇总统计失败: %w", err)
	}
	if out.Total > 0 {
		out.BlockRate = float64(out.Blocked) / float64(out.Total)
		out.CacheRate = float64(out.Cached) / float64(out.Total)
		out.AvgLatency = float64(latencySum) / float64(out.Total)
	}
	return &out, nil
}

// TimePoint 是时间序列上的一个点。
type TimePoint struct {
	TS      int64 `json:"ts"`
	Total   int64 `json:"total"`
	Blocked int64 `json:"blocked"`
	Cached  int64 `json:"cached"`
	Allowed int64 `json:"allowed"`
}

// Timeseries 返回按 interval 秒对齐的时间序列。
func (s *Store) Timeseries(ctx context.Context, uid string, since, interval int64) ([]TimePoint, error) {
	if interval <= 0 {
		interval = 3600
	}
	where := []string{"ts >= ?"}
	args := []any{since}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	// 用整数除法对齐到 interval 边界，避免 Go 侧再做一轮聚合。
	rows, err := s.read.QueryContext(ctx,
		`SELECT (ts / ?) * ? AS bucket,
		        COALESCE(SUM(total),0), COALESCE(SUM(blocked),0),
		        COALESCE(SUM(cached),0), COALESCE(SUM(allowed),0)
		 FROM query_rollup WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY bucket ORDER BY bucket`, append([]any{interval, interval}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("查询时间序列失败: %w", err)
	}
	defer rows.Close()
	var out []TimePoint
	for rows.Next() {
		var p TimePoint
		if err := rows.Scan(&p.TS, &p.Total, &p.Blocked, &p.Cached, &p.Allowed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TopDomain 是 TOP 域名条目。
type TopDomain struct {
	Domain  string `json:"domain"`
	Hits    int64  `json:"hits"`
	Blocked int64  `json:"blocked"`
}

// TopDomains 返回某区间内的 TOP 域名。
//
// blockedOnly 为 true 时只统计被拦截的域名（对应 ?type=blocked）。
func (s *Store) TopDomains(ctx context.Context, uid string, since int64, limit int, blockedOnly bool) ([]TopDomain, error) {
	where := []string{"ts >= ?"}
	args := []any{since}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	having := ""
	if blockedOnly {
		having = " HAVING SUM(blocked) > 0"
	}
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx,
		`SELECT domain, COALESCE(SUM(hits),0) AS h, COALESCE(SUM(blocked),0) AS b
		 FROM top_domains WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY domain`+having+`
		 ORDER BY `+orderByForTop(blockedOnly)+` DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 TOP 域名失败: %w", err)
	}
	defer rows.Close()
	var out []TopDomain
	for rows.Next() {
		var t TopDomain
		if err := rows.Scan(&t.Domain, &t.Hits, &t.Blocked); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func orderByForTop(blockedOnly bool) string {
	if blockedOnly {
		return "b"
	}
	return "h"
}

// UniqueDomains 返回区间内的唯一域名数。
func (s *Store) UniqueDomains(ctx context.Context, uid string, since int64) (int64, error) {
	where := []string{"ts >= ?"}
	args := []any{since}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	var n int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT domain) FROM top_domains WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	return n, err
}

// QueryLogFilter 是查询日志的筛选条件。
type QueryLogFilter struct {
	UserID string
	Domain string
	Action string
	Limit  int
	Offset int
}

// QueryLogs 分页查询日志。
func (s *Store) QueryLogs(ctx context.Context, f QueryLogFilter) ([]model.QueryLogRow, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.Domain != "" {
		where = append(where, "domain LIKE ?")
		args = append(args, "%"+f.Domain+"%")
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_log`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计查询日志失败: %w", err)
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, user_id, ts, domain, qtype, rcode, action, client, latency_ms
		 FROM query_log`+clause+` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询日志失败: %w", err)
	}
	defer rows.Close()
	var out []model.QueryLogRow
	for rows.Next() {
		var l model.QueryLogRow
		if err := rows.Scan(&l.ID, &l.UserID, &l.TS, &l.Domain, &l.QType, &l.RCode, &l.Action, &l.Client, &l.LatencyMS); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// Cleanup 按保留期清理统计与日志，返回删除行数。
func (s *Store) Cleanup(ctx context.Context, retentionDays, logRetentionDays, maxLogRows int) (map[string]int64, error) {
	out := map[string]int64{}
	now := time.Now().Unix()
	statCutoff := now - int64(retentionDays)*86400
	logCutoff := now - int64(logRetentionDays)*86400

	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []struct {
			name string
			sql  string
			args []any
		}{
			{"query_rollup", `DELETE FROM query_rollup WHERE ts < ?`, []any{statCutoff}},
			{"top_domains", `DELETE FROM top_domains WHERE ts < ?`, []any{statCutoff}},
			{"query_log", `DELETE FROM query_log WHERE ts < ?`, []any{logCutoff}},
			{"refresh_tokens", `DELETE FROM refresh_tokens WHERE expires_at < ?`, []any{now}},
		} {
			res, err := tx.ExecContext(ctx, q.sql, q.args...)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			out[q.name] += n
		}
		// 查询日志再叠一层总量上限，防止「保留期没到但已经几百万条」。
		if maxLogRows > 0 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_log`).Scan(&n); err != nil {
				return err
			}
			if n > maxLogRows {
				excess := n - maxLogRows
				res, err := tx.ExecContext(ctx,
					`DELETE FROM query_log WHERE id IN (SELECT id FROM query_log ORDER BY id ASC LIMIT ?)`, excess)
				if err != nil {
					return err
				}
				d, _ := res.RowsAffected()
				out["query_log"] += d
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("清理过期数据失败: %w", err)
	}
	return out, nil
}

// === 令牌 ===

// SaveRefreshToken 记录一个 refresh token 的 jti，用于支持撤销。
func (s *Store) SaveRefreshToken(ctx context.Context, jti, uid string, expiresAt int64) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO refresh_tokens (jti, user_id, expires_at, revoked) VALUES (?,?,?,0)
		 ON CONFLICT(jti) DO NOTHING`, jti, uid, expiresAt)
	if err != nil {
		return fmt.Errorf("保存 refresh token 失败: %w", err)
	}
	return nil
}

// IsRefreshTokenValid 判断 jti 是否仍然有效（存在、未撤销、未过期）。
func (s *Store) IsRefreshTokenValid(ctx context.Context, jti string) (bool, error) {
	var revoked int
	var expires int64
	err := s.read.QueryRowContext(ctx, `SELECT revoked, expires_at FROM refresh_tokens WHERE jti = ?`, jti).
		Scan(&revoked, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return revoked == 0 && expires > time.Now().Unix(), nil
}

// RevokeRefreshToken 撤销单个 refresh token。
func (s *Store) RevokeRefreshToken(ctx context.Context, jti string) error {
	_, err := s.write.ExecContext(ctx, `UPDATE refresh_tokens SET revoked = 1 WHERE jti = ?`, jti)
	return err
}

// RevokeAllUserTokens 撤销某用户的全部 refresh token（改密码后调用）。
func (s *Store) RevokeAllUserTokens(ctx context.Context, uid string) error {
	_, err := s.write.ExecContext(ctx, `UPDATE refresh_tokens SET revoked = 1 WHERE user_id = ?`, uid)
	return err
}

// PATToken 是一条长期 API Token 的元信息（不含原文）。
type PATToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	CreatedAt int64  `json:"-"`
	LastUsed  int64  `json:"-"`
	ExpiresAt int64  `json:"-"`
}

// CreatePAT 保存一个 PAT 的哈希。
func (s *Store) CreatePAT(ctx context.Context, uid, name, hash, prefix string, expiresAt int64) (string, error) {
	pid := id.New("pat_")
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO pat_tokens (id, user_id, name, token_hash, prefix, created_at, expires_at)
		 VALUES (?,?,?,?,?,?,?)`, pid, uid, name, hash, prefix, time.Now().Unix(), expiresAt)
	if err != nil {
		return "", fmt.Errorf("创建 API Token 失败: %w", err)
	}
	return pid, nil
}

// ListPATs 列出某用户的 PAT 元信息。
func (s *Store) ListPATs(ctx context.Context, uid string) ([]*PATToken, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, name, prefix, created_at, last_used, expires_at FROM pat_tokens WHERE user_id = ? ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("列出 API Token 失败: %w", err)
	}
	defer rows.Close()
	var out []*PATToken
	for rows.Next() {
		var p PATToken
		if err := rows.Scan(&p.ID, &p.Name, &p.Prefix, &p.CreatedAt, &p.LastUsed, &p.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// DeletePAT 删除一个 PAT。
func (s *Store) DeletePAT(ctx context.Context, uid, pid string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM pat_tokens WHERE id = ? AND user_id = ?`, pid, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupPAT 按哈希查出归属用户，并刷新 last_used。
func (s *Store) LookupPAT(ctx context.Context, hash string) (string, error) {
	var uid string
	var expires int64
	err := s.read.QueryRowContext(ctx, `SELECT user_id, expires_at FROM pat_tokens WHERE token_hash = ?`, hash).
		Scan(&uid, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if expires > 0 && expires < time.Now().Unix() {
		return "", ErrNotFound
	}
	// 更新使用时间是「尽力而为」：失败不影响鉴权结果。
	_, _ = s.write.ExecContext(ctx, `UPDATE pat_tokens SET last_used = ? WHERE token_hash = ?`, time.Now().Unix(), hash)
	return uid, nil
}
