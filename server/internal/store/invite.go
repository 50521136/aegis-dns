package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// === 邀请码 ===

// CreateInviteCode 新建邀请码。
func (s *Store) CreateInviteCode(ctx context.Context, c *model.InviteCode) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO invite_codes (code, owner_id, note, reward_days, max_uses, used_count, expires_at, enabled, created_at)
		 VALUES (?,?,?,?,?,0,?,?,?)`,
		c.Code, c.OwnerID, c.Note, c.RewardDays, c.MaxUses, c.ExpiresAt, boolInt(c.Enabled), time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("邀请码 %s 已存在", c.Code)
		}
		return fmt.Errorf("创建邀请码失败: %w", err)
	}
	return nil
}

// ListInviteCodes 列出邀请码，ownerID 非空时只看该用户名下的。
func (s *Store) ListInviteCodes(ctx context.Context, ownerID string) ([]model.InviteCode, error) {
	q := `SELECT code, owner_id, note, reward_days, max_uses, used_count, expires_at, enabled, created_at FROM invite_codes`
	var args []any
	if ownerID != "" {
		q += ` WHERE owner_id = ?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询邀请码失败: %w", err)
	}
	defer rows.Close()
	var out []model.InviteCode
	for rows.Next() {
		var c model.InviteCode
		var enabled int
		if err := rows.Scan(&c.Code, &c.OwnerID, &c.Note, &c.RewardDays, &c.MaxUses,
			&c.UsedCount, &c.ExpiresAt, &enabled, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateInviteCode 更新邀请码的可变字段（码本身不可改，它是被分发出去的凭据）。
func (s *Store) UpdateInviteCode(ctx context.Context, code string, note *string, rewardDays, maxUses *int, expiresAt *int64, enabled *bool) error {
	sets := []string{}
	args := []any{}
	if note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *note)
	}
	if rewardDays != nil {
		sets = append(sets, "reward_days = ?")
		args = append(args, *rewardDays)
	}
	if maxUses != nil {
		sets = append(sets, "max_uses = ?")
		args = append(args, *maxUses)
	}
	if expiresAt != nil {
		sets = append(sets, "expires_at = ?")
		args = append(args, *expiresAt)
	}
	if enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolInt(*enabled))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, code)
	res, err := s.write.ExecContext(ctx, `UPDATE invite_codes SET `+strings.Join(sets, ", ")+` WHERE code = ?`, args...)
	if err != nil {
		return fmt.Errorf("更新邀请码失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteInviteCode 删除邀请码。已产生的使用记录保留（历史不可篡改）。
func (s *Store) DeleteInviteCode(ctx context.Context, code string) error {
	_, err := s.write.ExecContext(ctx, `DELETE FROM invite_codes WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("删除邀请码失败: %w", err)
	}
	return nil
}

// GetInviteCode 取单个邀请码。
func (s *Store) GetInviteCode(ctx context.Context, code string) (*model.InviteCode, error) {
	var c model.InviteCode
	var enabled int
	err := s.read.QueryRowContext(ctx,
		`SELECT code, owner_id, note, reward_days, max_uses, used_count, expires_at, enabled, created_at
		 FROM invite_codes WHERE code = ?`, strings.ToUpper(strings.TrimSpace(code))).
		Scan(&c.Code, &c.OwnerID, &c.Note, &c.RewardDays, &c.MaxUses, &c.UsedCount, &c.ExpiresAt, &enabled, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询邀请码失败: %w", err)
	}
	c.Enabled = enabled == 1
	return &c, nil
}

// RedeemInvite 在注册成功后核销邀请码，并给邀请人发奖励。
//
// 整个过程在一个事务里：核销、记录、加会员期必须同时成立。
// 分开写会出现「码核销了但奖励没发」或「奖励发了但码还能再用」——
// 前者让邀请人白干，后者能被刷。
//
// 返回邀请人获得的奖励天数（0 表示码没有归属人，只做核销）。
func (s *Store) RedeemInvite(ctx context.Context, code, inviteeID string) (int, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return 0, nil
	}
	var reward int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var ownerID, note string
		var rewardDays, maxUses, usedCount, enabled int
		var expiresAt, createdAt int64
		err := tx.QueryRowContext(ctx,
			`SELECT owner_id, note, reward_days, max_uses, used_count, expires_at, enabled, created_at
			 FROM invite_codes WHERE code = ?`, code).
			Scan(&ownerID, &note, &rewardDays, &maxUses, &usedCount, &expiresAt, &enabled, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("邀请码不存在")
		}
		if err != nil {
			return err
		}
		if enabled != 1 {
			return fmt.Errorf("邀请码已停用")
		}
		if expiresAt > 0 && time.Now().Unix() > expiresAt {
			return fmt.Errorf("邀请码已过期")
		}
		if maxUses > 0 && usedCount >= maxUses {
			return fmt.Errorf("邀请码使用次数已用完")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE invite_codes SET used_count = used_count + 1 WHERE code = ?`, code); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO invite_uses (code, invitee_id, inviter_id, reward_days, created_at)
			 VALUES (?,?,?,?,?)`, code, inviteeID, ownerID, rewardDays, time.Now().Unix()); err != nil {
			return err
		}
		// 给被邀请人打上 invited_by，便于后台看拉新来源
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET invited_by = ? WHERE id = ?`, ownerID, inviteeID); err != nil {
			return err
		}
		// 奖励发给码的归属人。从「现在」与「原到期时间」中取较晚者作为起点，
		// 连续兑换不会把前一段剩余时间冲掉。
		if ownerID != "" && rewardDays > 0 {
			var cur int64
			if err := tx.QueryRowContext(ctx, `SELECT member_until FROM users WHERE id = ?`, ownerID).Scan(&cur); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					// 码的归属人已被删：核销照做，奖励无处可发
					reward = 0
					return nil
				}
				return err
			}
			base := time.Now().Unix()
			if cur > base {
				base = cur
			}
			next := base + int64(rewardDays)*86400
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET member_until = ? WHERE id = ?`, next, ownerID); err != nil {
				return err
			}
			reward = rewardDays
		}
		return nil
	})
	return reward, err
}

// InviteStats 是某个用户名下的拉新统计。
type InviteStats struct {
	TotalUses   int   `json:"total_uses"`
	RewardDays  int   `json:"reward_days"`
	MemberUntil int64 `json:"member_until"`
}

// GetInviteStats 统计 ownerID 名下邀请码带来的有效使用次数与累计奖励天数。
func (s *Store) GetInviteStats(ctx context.Context, ownerID string) (*InviteStats, error) {
	st := &InviteStats{}
	row := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(reward_days), 0) FROM invite_uses WHERE inviter_id = ?`, ownerID)
	if err := row.Scan(&st.TotalUses, &st.RewardDays); err != nil {
		return nil, fmt.Errorf("统计邀请失败: %w", err)
	}
	// 会员到期时间以用户表为准（可能还有管理员手动加的天数）
	_ = s.read.QueryRowContext(ctx, `SELECT member_until FROM users WHERE id = ?`, ownerID).Scan(&st.MemberUntil)
	return st, nil
}

// SetMemberUntil 直接设置会员到期时间（管理员手动调整用）。
func (s *Store) SetMemberUntil(ctx context.Context, uid string, until int64) error {
	res, err := s.write.ExecContext(ctx, `UPDATE users SET member_until = ? WHERE id = ?`, until, uid)
	if err != nil {
		return fmt.Errorf("更新会员到期时间失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AddMemberDays 在现有会员期基础上增减天数（负数为扣减）。
//
// 与 RedeemInvite 里的加法不同，这里允许扣减，用于纠正误发。
// 扣到过去就是立即到期，不会出现负数时间戳。
func (s *Store) AddMemberDays(ctx context.Context, uid string, days int) (int64, error) {
	var cur int64
	if err := s.read.QueryRowContext(ctx, `SELECT member_until FROM users WHERE id = ?`, uid).Scan(&cur); err != nil {
		return 0, err
	}
	base := time.Now().Unix()
	if cur > base {
		base = cur
	}
	next := base + int64(days)*86400
	if next < 0 {
		next = 0
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE users SET member_until = ? WHERE id = ?`, next, uid); err != nil {
		return 0, fmt.Errorf("更新会员到期时间失败: %w", err)
	}
	return next, nil
}
