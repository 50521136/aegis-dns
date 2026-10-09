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

const subCols = `id, user_id, list_type, name, url, format, enabled, last_etag, last_modified,
	last_fetched, last_count, last_status, fail_count, created_at`

func scanSub(sc interface{ Scan(...any) error }) (*model.Subscription, error) {
	var sub model.Subscription
	var enabled int
	err := sc.Scan(&sub.ID, &sub.UserID, &sub.ListType, &sub.Name, &sub.URL, &sub.Format,
		&enabled, &sub.LastETag, &sub.LastModified, &sub.LastFetched, &sub.LastCount,
		&sub.LastStatus, &sub.FailCount, &sub.CreatedAt)
	if err != nil {
		return nil, err
	}
	sub.Enabled = enabled != 0
	return &sub, nil
}

// ListSubs 返回某用户的全部订阅。
func (s *Store) ListSubs(ctx context.Context, uid string) ([]*model.Subscription, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE user_id = ? ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("列出订阅失败: %w", err)
	}
	defer rows.Close()
	var out []*model.Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// AllEnabledSubs 返回所有启用中的订阅（调度器刷新用）。
func (s *Store) AllEnabledSubs(ctx context.Context) ([]*model.Subscription, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE enabled = 1 ORDER BY user_id, created_at`)
	if err != nil {
		return nil, fmt.Errorf("列出启用订阅失败: %w", err)
	}
	defer rows.Close()
	var out []*model.Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// GetSub 按 ID 查询订阅（带归属校验）。
func (s *Store) GetSub(ctx context.Context, uid, sid string) (*model.Subscription, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = ? AND user_id = ?`, sid, uid)
	sub, err := scanSub(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询订阅失败: %w", err)
	}
	return sub, nil
}

// CreateSub 新建订阅。
func (s *Store) CreateSub(ctx context.Context, sub *model.Subscription) error {
	sub.ID = id.New(id.PrefixSub)
	sub.CreatedAt = time.Now().Unix()
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO subscriptions (`+subCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sub.ID, sub.UserID, sub.ListType, sub.Name, sub.URL, sub.Format, boolInt(sub.Enabled),
		sub.LastETag, sub.LastModified, sub.LastFetched, sub.LastCount, sub.LastStatus, sub.FailCount, sub.CreatedAt)
	if err != nil {
		if isUniqueViolation(err, "subscriptions.user_id") || isUniqueViolation(err, "subscriptions.url") {
			return ErrSubExists
		}
		return fmt.Errorf("创建订阅失败: %w", err)
	}
	return nil
}

// ErrSubExists 表示同一用户已添加过该 URL。
var ErrSubExists = errors.New("该订阅 URL 已存在")

// UpdateSub 更新订阅的可变字段。
func (s *Store) UpdateSub(ctx context.Context, sub *model.Subscription) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET name=?, url=?, list_type=?, format=?, enabled=? WHERE id=? AND user_id=?`,
		sub.Name, sub.URL, sub.ListType, sub.Format, boolInt(sub.Enabled), sub.ID, sub.UserID)
	if err != nil {
		return fmt.Errorf("更新订阅失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSub 删除订阅（外键 CASCADE 清掉其条目）。
func (s *Store) DeleteSub(ctx context.Context, uid, sid string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ? AND user_id = ?`, sid, uid)
	if err != nil {
		return fmt.Errorf("删除订阅失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSubStatus 更新订阅的抓取元信息。
func (s *Store) UpdateSubStatus(ctx context.Context, sid, etag, lastMod, status string, fetchedAt int64, count, failCount int) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET last_etag=?, last_modified=?, last_fetched=?, last_count=?, last_status=?, fail_count=?
		 WHERE id=?`,
		etag, lastMod, fetchedAt, count, status, failCount, sid)
	if err != nil {
		return fmt.Errorf("更新订阅状态失败: %w", err)
	}
	return nil
}

// ReplaceSubEntries 用新解析出的域名集合替换某订阅的全部条目。
//
// 用临时表做集合差运算，而不是「DELETE 全部再 INSERT」：
//   - 前者只动真正变化的行，10 万条里改 3 条就只写 3 行；
//   - 后者每次刷新都会把整张表重写一遍，WAL 文件瞬间膨胀。
//
// 整个过程在一个事务内，保证 dnsd 侧看到的快照不会出现「半空」状态。
func (s *Store) ReplaceSubEntries(ctx context.Context, sid, kind string, domains []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS new_entries (domain TEXT PRIMARY KEY) WITHOUT ROWID`); err != nil {
			return err
		}
		// 临时表是连接级的，但事务内复用前必须清空。
		if _, err := tx.ExecContext(ctx, `DELETE FROM new_entries`); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO new_entries (domain) VALUES (?) ON CONFLICT(domain) DO NOTHING`)
		if err != nil {
			return err
		}
		for _, d := range domains {
			if _, err := stmt.ExecContext(ctx, d); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sub_entries WHERE sub_id = ? AND domain NOT IN (SELECT domain FROM new_entries)`, sid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sub_entries (sub_id, domain, kind)
			 SELECT ?, domain, ? FROM new_entries
			 WHERE domain NOT IN (SELECT domain FROM sub_entries WHERE sub_id = ?)`,
			sid, kind, sid); err != nil {
			return err
		}
		// kind 可能因用户改了 list_type 而需要就地修正。
		if _, err := tx.ExecContext(ctx, `UPDATE sub_entries SET kind = ? WHERE sub_id = ?`, kind, sid); err != nil {
			return err
		}
		return nil
	})
}

// CountSubEntries 返回某订阅的条目数。
func (s *Store) CountSubEntries(ctx context.Context, sid string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM sub_entries WHERE sub_id = ?`, sid).Scan(&n)
	return n, err
}

// SubEntriesByUser 返回某用户的 block / allow 域名集合（增量快照用）。
func (s *Store) SubEntriesByUser(ctx context.Context, uid string) (block, allow []string, err error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT e.kind, e.domain FROM sub_entries e
		 JOIN subscriptions s ON s.id = e.sub_id
		 WHERE s.user_id = ? AND s.enabled = 1`, uid)
	if err != nil {
		return nil, nil, fmt.Errorf("读取订阅条目失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return nil, nil, err
		}
		if kind == "allow" {
			allow = append(allow, domain)
		} else {
			block = append(block, domain)
		}
	}
	return block, allow, rows.Err()
}

// AllSubEntries 返回全部启用订阅的条目，按 user_id 分组（全量快照用）。
func (s *Store) AllSubEntries(ctx context.Context) (map[string][2][]string, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT s.user_id, e.kind, e.domain FROM sub_entries e
		 JOIN subscriptions s ON s.id = e.sub_id
		 WHERE s.enabled = 1
		 ORDER BY s.user_id`)
	if err != nil {
		return nil, fmt.Errorf("读取全部订阅条目失败: %w", err)
	}
	defer rows.Close()

	out := map[string][2][]string{}
	for rows.Next() {
		var uid, kind, domain string
		if err := rows.Scan(&uid, &kind, &domain); err != nil {
			return nil, err
		}
		v := out[uid]
		if kind == "allow" {
			v[1] = append(v[1], domain)
		} else {
			v[0] = append(v[0], domain)
		}
		out[uid] = v
	}
	return out, rows.Err()
}

// UserIDsWithSubs 返回所有拥有启用订阅的用户 ID（增量快照需要知道哪些用户受影响）。
func (s *Store) UserIDsWithSubs(ctx context.Context, sid string) (string, error) {
	var uid string
	err := s.read.QueryRowContext(ctx, `SELECT user_id FROM subscriptions WHERE id = ?`, sid).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return uid, err
}

// CountSubEntriesAll 返回全库订阅条目总数（容量监控用）。
func (s *Store) CountSubEntriesAll(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM sub_entries`).Scan(&n)
	return n, err
}

// AuditRow 是一条审计日志。
type AuditRow struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	Time   string `json:"time"`
	UserID string `json:"user_id"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}

// RecentAudit 返回最近的审计记录。
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, ts, user_id, action, detail, ip FROM audit_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取审计日志失败: %w", err)
	}
	defer rows.Close()
	out := make([]AuditRow, 0, limit)
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.ID, &a.TS, &a.UserID, &a.Action, &a.Detail, &a.IP); err != nil {
			return nil, err
		}
		a.Time = time.Unix(a.TS, 0).UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	return out, rows.Err()
}

// trimDomains 去重并规范化域名列表（保持稳定顺序，便于 diff 与快照可比）。
func trimDomains(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
