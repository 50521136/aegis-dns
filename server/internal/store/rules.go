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

const ruleCols = `id, user_id, kind, pattern, value, qtypes, enabled, created_at`

func scanRule(sc interface{ Scan(...any) error }) (*model.Rule, error) {
	var r model.Rule
	var qtypes string
	var enabled int
	if err := sc.Scan(&r.ID, &r.UserID, &r.Kind, &r.Pattern, &r.Value, &qtypes, &enabled, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.QTypes = splitQTypes(qtypes)
	r.Enabled = enabled != 0
	return &r, nil
}

// splitQTypes 把 "A,AAAA" 拆成切片，空串返回 nil。
func splitQTypes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func joinQTypes(q []string) string {
	if len(q) == 0 {
		return ""
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(q))
	for _, v := range q {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

// ListRules 分页列出某个用户的规则。
//
// kind 为空表示不筛选；q 为关键字模糊匹配 pattern 与 value。
func (s *Store) ListRules(ctx context.Context, uid, kind, q string, enabled *bool, limit, offset int) ([]*model.Rule, int, error) {
	where := []string{"user_id = ?"}
	args := []any{uid}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if q != "" {
		where = append(where, "(pattern LIKE ? OR value LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	if enabled != nil {
		where = append(where, "enabled = ?")
		args = append(args, boolInt(*enabled))
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计规则数失败: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+ruleCols+` FROM rules`+clause+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出规则失败: %w", err)
	}
	defer rows.Close()

	var out []*model.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ListAllRules 返回某用户的全部启用规则（构建快照用）。
func (s *Store) ListAllRules(ctx context.Context, uid string) ([]model.Rule, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+ruleCols+` FROM rules WHERE user_id = ? AND enabled = 1 ORDER BY created_at`, uid)
	if err != nil {
		return nil, fmt.Errorf("读取规则失败: %w", err)
	}
	defer rows.Close()
	var out []model.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// AllRules 返回所有启用规则，按 user_id 分组（全量快照构建用）。
func (s *Store) AllRules(ctx context.Context) (map[string][]model.Rule, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+ruleCols+` FROM rules WHERE enabled = 1 ORDER BY user_id, created_at`)
	if err != nil {
		return nil, fmt.Errorf("读取全部规则失败: %w", err)
	}
	defer rows.Close()
	out := map[string][]model.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out[r.UserID] = append(out[r.UserID], *r)
	}
	return out, rows.Err()
}

// CreateRule 新建一条规则。
func (s *Store) CreateRule(ctx context.Context, r *model.Rule) error {
	r.ID = id.New(id.PrefixRule)
	r.CreatedAt = time.Now().Unix()
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO rules (`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.UserID, r.Kind, r.Pattern, r.Value, joinQTypes(r.QTypes), boolInt(r.Enabled), r.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建规则失败: %w", err)
	}
	return nil
}

// BulkCreateRules 在单个事务里批量插入规则，返回成功条数。
//
// 大批量导入（如几万条 hosts）时逐条 INSERT 会因为每次一个隐式事务而极慢，
// 这里统一走一个事务 + 预编译语句。
func (s *Store) BulkCreateRules(ctx context.Context, rules []*model.Rule) (int, error) {
	if len(rules) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	created := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO rules (`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rules {
			r.ID = id.New(id.PrefixRule)
			r.CreatedAt = now
			if _, err := stmt.ExecContext(ctx,
				r.ID, r.UserID, r.Kind, r.Pattern, r.Value, joinQTypes(r.QTypes), boolInt(r.Enabled), r.CreatedAt); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("批量创建规则失败: %w", err)
	}
	return created, nil
}

// GetRule 按 ID 查询（带归属校验，防止越权访问他人规则）。
func (s *Store) GetRule(ctx context.Context, uid, rid string) (*model.Rule, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM rules WHERE id = ? AND user_id = ?`, rid, uid)
	r, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询规则失败: %w", err)
	}
	return r, nil
}

// UpdateRule 全量更新一条规则的字段。
func (s *Store) UpdateRule(ctx context.Context, r *model.Rule) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE rules SET kind=?, pattern=?, value=?, qtypes=?, enabled=? WHERE id=? AND user_id=?`,
		r.Kind, r.Pattern, r.Value, joinQTypes(r.QTypes), boolInt(r.Enabled), r.ID, r.UserID)
	if err != nil {
		return fmt.Errorf("更新规则失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRule 删除一条规则。
func (s *Store) DeleteRule(ctx context.Context, uid, rid string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM rules WHERE id = ? AND user_id = ?`, rid, uid)
	if err != nil {
		return fmt.Errorf("删除规则失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRules 批量删除规则，返回实际删除条数。
func (s *Store) DeleteRules(ctx context.Context, uid string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	deleted := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `DELETE FROM rules WHERE id = ? AND user_id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, rid := range ids {
			res, err := stmt.ExecContext(ctx, rid, uid)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				deleted++
			}
		}
		return nil
	})
	return deleted, err
}

// CountRules 返回某用户的规则总数。
func (s *Store) CountRules(ctx context.Context, uid string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules WHERE user_id = ?`, uid).Scan(&n)
	return n, err
}
