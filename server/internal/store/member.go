package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 会员发放的统一入口。
//
// 「发会员」= 设定会员种类 + 延长有效期，两件事必须一起做：
// 只加天数会让奖励落到用户上一次的种类上，只改种类则会出现
// 「永久指定了档位但其实不是会员」这种说不清的状态。
//
// 三条规则：
//  1. **不降级**：用户当前还是会员、且新档位等级更低时，只延长有效期、
//     不改种类。低档奖励不该把高档冲掉。
//  2. 天数从「现在」与「原到期时间」中较晚者起算，连续发放不冲掉剩余时间。
//  3. 负数天数照常扣减，扣到过去就是立即到期，不会出现负数时间戳。

// sqlExecer 是 *sql.DB 与 *sql.Tx 的公共子集，让同一段发放逻辑
// 既能独立执行、也能挂进注册/签到/核销邀请码的事务里。
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// grantMember 给用户发放一段会员期，返回新的到期时间。
//
// tierID 为空（或指向已删除的档位）时保持用户现有种类不变；
// 用户从来没有过种类时，由 model.GrantableTierID 落到默认会员档。
func grantMember(ctx context.Context, q sqlExecer, uid, tierID string, days int,
	tiers map[string]model.TierDef) (int64, error) {
	var cur int64
	var curTier string
	err := q.QueryRowContext(ctx, `SELECT member_until, tier_id FROM users WHERE id = ?`, uid).
		Scan(&cur, &curTier)
	if err != nil {
		return 0, err
	}

	now := time.Now().Unix()
	base := now
	if cur > base {
		base = cur
	}
	next := base + int64(days)*86400
	if next < 0 {
		next = 0
	}

	applied := strings.TrimSpace(curTier)
	want := strings.TrimSpace(tierID)
	if want != "" {
		if _, ok := tiers[want]; ok {
			// 只有「还在会员期内」才谈得上降级；已过期的用户
			// 从任何一个奖励重新开始时，就该按奖励的档位来。
			if cur <= now || applied == "" || model.TierRank(tiers, want) >= model.TierRank(tiers, applied) {
				applied = want
			}
		}
	}
	if applied == "" {
		applied = model.GrantableTierID(tiers, "")
	}

	if _, err := q.ExecContext(ctx,
		`UPDATE users SET member_until = ?, tier_id = ? WHERE id = ?`, next, applied, uid); err != nil {
		return 0, fmt.Errorf("更新会员期失败: %w", err)
	}
	return next, nil
}

// GrantMembership 给用户发放一段会员期（天数可正可负）。
//
// 注册赠送、每日签到、邀请奖励、管理员加天数都走它，避免四处各写一套加法
// 而漏掉「种类也要跟着变」。
func (s *Store) GrantMembership(ctx context.Context, uid, tierID string, days int) (int64, error) {
	tiers, err := s.TierMap(ctx)
	if err != nil {
		return 0, err
	}
	return grantMember(ctx, s.write, uid, tierID, days, tiers)
}

// SetMembership 直接设定会员种类与到期时间（管理员手动改档用）。
//
// 与 GrantMembership 的区别：这里**无条件覆盖**，包括降级与永久。
// 管理员说「改成普通用户」就该改成普通用户，不该被「不降级」挡住。
func (s *Store) SetMembership(ctx context.Context, uid, tierID string, until int64) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE users SET member_until = ?, tier_id = ? WHERE id = ?`, until, strings.TrimSpace(tierID), uid)
	if err != nil {
		return fmt.Errorf("更新会员期失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RevokeMembership 取消会员：到期时间归零，种类一并清掉。
//
// 只清到期时间是不够的：种类留在库里，下次一发放低档奖励
// 就会因为「不降级」而直接继承上一次的高档。
func (s *Store) RevokeMembership(ctx context.Context, uid string) error {
	return s.SetMembership(ctx, uid, "", 0)
}

// UserTierID 读取用户当前的会员种类 id（不看有效期）。
func (s *Store) UserTierID(ctx context.Context, uid string) (string, error) {
	var id string
	if err := s.read.QueryRowContext(ctx,
		`SELECT tier_id FROM users WHERE id = ?`, uid).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}
