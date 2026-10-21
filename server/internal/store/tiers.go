package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// ListTiers 返回全部会员种类，按 sort 升序。
func (s *Store) ListTiers(ctx context.Context) ([]model.TierDef, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, name, sort, max_rules, max_subs, daily_queries, is_default
		   FROM tiers ORDER BY sort, id`)
	if err != nil {
		return nil, fmt.Errorf("列出会员种类失败: %w", err)
	}
	defer rows.Close()
	out := make([]model.TierDef, 0, 8)
	for rows.Next() {
		var t model.TierDef
		var def int
		if err := rows.Scan(&t.ID, &t.Name, &t.Sort, &t.MaxRules, &t.MaxSubs, &t.DailyQueries, &def); err != nil {
			return nil, err
		}
		t.IsDefault = def != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// TierMap 返回 id → 档位定义，供配额换算使用。
//
// 每次生成快照都要按用户算配额，逐个查库会是 N+1；
// 一次取出几十条档位拼成 map，之后纯内存查。
func (s *Store) TierMap(ctx context.Context) (map[string]model.TierDef, error) {
	tiers, err := s.ListTiers(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]model.TierDef, len(tiers))
	for _, t := range tiers {
		m[t.ID] = t
	}
	return m, nil
}

// GetTier 按 id 取单个档位。
func (s *Store) GetTier(ctx context.Context, id string) (*model.TierDef, error) {
	var t model.TierDef
	var def int
	err := s.read.QueryRowContext(ctx,
		`SELECT id, name, sort, max_rules, max_subs, daily_queries, is_default
		   FROM tiers WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Sort, &t.MaxRules, &t.MaxSubs, &t.DailyQueries, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取会员种类失败: %w", err)
	}
	t.IsDefault = def != 0
	return &t, nil
}

// UpsertTier 新建或更新一个档位。
//
// free 与 vip 不允许删除、也不允许改 id：用户没有显式指定档位时，
// 配额换算要靠这两个 id 兜底，删掉之后老用户会算不出配额。
func (s *Store) UpsertTier(ctx context.Context, t model.TierDef) error {
	if t.ID == "" {
		return errors.New("档位 id 不能为空")
	}
	if t.IsDefault {
		// 默认档位全表只能有一个，先全部清零再设。
		if _, err := s.write.ExecContext(ctx, `UPDATE tiers SET is_default = 0`); err != nil {
			return fmt.Errorf("重置默认档位失败: %w", err)
		}
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO tiers (id, name, sort, max_rules, max_subs, daily_queries, is_default, created_at)
		 VALUES (?,?,?,?,?,?,?, strftime('%s','now'))
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name, sort = excluded.sort,
		   max_rules = excluded.max_rules, max_subs = excluded.max_subs,
		   daily_queries = excluded.daily_queries, is_default = excluded.is_default`,
		t.ID, t.Name, t.Sort, t.MaxRules, t.MaxSubs, t.DailyQueries, boolInt(t.IsDefault))
	if err != nil {
		return fmt.Errorf("保存会员种类失败: %w", err)
	}
	return nil
}

// DeleteTier 删除一个档位。内置的 free/vip 拒绝删除。
func (s *Store) DeleteTier(ctx context.Context, id string) error {
	if id == "free" || id == "vip" {
		return errors.New("内置档位 free / vip 不能删除")
	}
	// 还挂着用户的档位不能删：删了这些用户的配额会静默退化成默认档，
	// 用户自己看不出区别，管理员却以为新档位还在生效。
	var n int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE tier_id = ?`, id).Scan(&n); err != nil {
		return fmt.Errorf("统计档位用户失败: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("还有 %d 个用户属于这个档位，请先改到别的档位", n)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM tiers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除会员种类失败: %w", err)
	}
	return nil
}

// SetUserTier 给用户指定档位。传空串表示改回「按会员状态自动判定」。
func (s *Store) SetUserTier(ctx context.Context, uid, tierID string) error {
	res, err := s.write.ExecContext(ctx, `UPDATE users SET tier_id = ? WHERE id = ?`, tierID, uid)
	if err != nil {
		return fmt.Errorf("设置用户档位失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
