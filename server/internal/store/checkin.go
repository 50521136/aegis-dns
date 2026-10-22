package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/bjtime"
)

// CheckinState 是某用户的签到状态。
type CheckinState struct {
	// CheckedToday 今天是否已签到。
	CheckedToday bool `json:"checked_today"`
	// Streak 当前连续签到天数（今天签过则含今天）。
	Streak int `json:"streak"`
	// TotalDays 累计签到天数。
	TotalDays int `json:"total_days"`
	// GrantedToday 今天实际发放的会员天数。
	GrantedToday int `json:"granted_today"`
	// LastDay 最后一次签到的自然日序号。
	LastDay int64 `json:"last_day"`
}

// dayIndex 把时间换算成北京自然日序号。
//
// 曾经用 UTC（t.Unix()/86400），结果签到要到北京时间早上 8 点才更新，
// 凌晨签到会被算进前一天。统一走 bjtime，全站一个口径。
func dayIndex(t time.Time) int64 { return bjtime.Day(t) }

// GetCheckinState 读取签到状态。
func (s *Store) GetCheckinState(ctx context.Context, uid string) (*CheckinState, error) {
	today := bjtime.Today()
	st := &CheckinState{}
	var lastDay int64
	var streak, granted int
	err := s.read.QueryRowContext(ctx,
		`SELECT day, streak, days_granted FROM checkins WHERE user_id = ? ORDER BY day DESC LIMIT 1`, uid).
		Scan(&lastDay, &streak, &granted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("查询签到状态失败: %w", err)
	}
	var total int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM checkins WHERE user_id = ?`, uid).Scan(&total); err != nil {
		return nil, fmt.Errorf("统计签到次数失败: %w", err)
	}
	st.TotalDays = total
	st.LastDay = lastDay
	st.CheckedToday = lastDay == today
	// 连续天数只在「昨天或今天」时才算数：断了就归零，
	// 否则界面上会一直显示一个早已断掉的历史记录。
	switch lastDay {
	case today:
		st.Streak = streak
		st.GrantedToday = granted
	case today - 1:
		st.Streak = streak
	default:
		st.Streak = 0
	}
	return st, nil
}

// CheckinResult 是签到的结果。
type CheckinResult struct {
	CheckinState
	// AlreadyDone 表示今天已经签过（此时 Granted=0）。
	AlreadyDone bool `json:"already_done"`
	// Granted 是本次发放的天数。
	Granted int `json:"granted"`
	// Bonus 是其中来自连签奖励的天数。
	Bonus int `json:"bonus"`
	// MemberUntil 是签到后的会员到期时间。
	MemberUntil int64 `json:"member_until"`
}

// Checkin 执行签到：记录、计算连签、发放会员天数。
//
// 全过程在一个事务里：签到记录与会员天数必须同时成立。
// 分开写会出现「签到成功但天数没加」或「天数加了还能再签一次」。
// bonusEvery / bonusDays：每累计签到 bonusEvery 天，额外赠送 bonusDays 天。
//
// 原来按「连续签到」算奖励，断一天就归零，对偶尔漏签的用户惩罚过重；
// 改成累计口径后，签满 7 天就一定有奖励，只是不再要求连续。
// tierID 是签到发放的会员种类（全局设置 checkin_tier）；空串 = 默认会员档。
func (s *Store) Checkin(ctx context.Context, uid string, dailyDays, bonusEvery, bonusDays int, tierID string) (*CheckinResult, error) {
	now := time.Now()
	today := bjtime.Today()
	res := &CheckinResult{}
	tiers, err := s.TierMap(ctx)
	if err != nil {
		return nil, err
	}

	err = s.tx(ctx, func(tx *sql.Tx) error {
		// 锁内再查一次当天记录：并发连点两次不能都成功。
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM checkins WHERE user_id = ? AND day = ?`, uid, today).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			res.AlreadyDone = true
			return nil
		}

		// 昨天签过就 +1，否则重新从 1 开始
		var prevStreak int
		err := tx.QueryRowContext(ctx,
			`SELECT streak FROM checkins WHERE user_id = ? AND day = ?`, uid, today-1).Scan(&prevStreak)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		streak := prevStreak + 1

		// 累计签到天数 = 已签过的不重复天数（含本次）
		var totalBefore int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM checkins WHERE user_id = ?`, uid).Scan(&totalBefore); err != nil {
			return err
		}
		totalAfter := totalBefore + 1

		granted := dailyDays
		bonus := 0
		// 累计奖励：每签满 bonusEvery 天给一次，避免第 8 天又给一次第 7 天的奖励
		if bonusEvery > 0 && bonusDays > 0 && totalAfter%bonusEvery == 0 {
			bonus = bonusDays
			granted += bonus
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO checkins (user_id, day, streak, days_granted, created_at) VALUES (?,?,?,?,?)`,
			uid, today, streak, granted, now.Unix()); err != nil {
			return err
		}
		if granted > 0 {
			// 天数和会员种类一起发：只加天数的话，管理员配了
			// 「签到送超级会员」也不会生效，签到永远落在内置 vip 档上。
			next, err := grantMember(ctx, tx, uid, tierID, granted, tiers)
			if err != nil {
				return err
			}
			res.MemberUntil = next
		}
		res.Streak = streak
		res.TotalDays = totalAfter
		res.Granted = granted
		res.Bonus = bonus
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 事务结束再读一次完整状态，避免事务内多查一次
	st, err := s.GetCheckinState(ctx, uid)
	if err == nil && st != nil {
		st.Streak = res.Streak
		if res.Granted > 0 {
			st.CheckedToday = true
			st.GrantedToday = res.Granted
		}
		res.CheckinState = *st
	}
	if res.MemberUntil == 0 {
		_ = s.read.QueryRowContext(ctx, `SELECT member_until FROM users WHERE id = ?`, uid).Scan(&res.MemberUntil)
	}
	return res, nil
}
