package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/50521136/aegis-dns/server/internal/bjtime"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// handleMyCheckin 返回签到状态，POST 时执行签到。
func (s *Server) handleGetCheckin(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	st, err := s.store.GetCheckinState(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 会员状态顺带给出：总览页头部要显示「当前是什么种类、还剩多久」，
	// 只为这个再打一次 /me/membership 不值当。
	tiers, _ := s.store.TierMap(r.Context())
	mem := model.MemberStateOf(u, tiers, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        set.CheckinEnabled,
		"daily_days":     set.CheckinDays,
		"daily_tier":     model.GrantableTierID(tiers, set.CheckinTier),
		"streak_days":    set.CheckinBonusEvery,
		"streak_bonus":   set.CheckinBonusDays,
		"checked_today":  st.CheckedToday,
		"streak":         st.Streak,
		"total_days":     st.TotalDays,
		"granted_today":  st.GrantedToday,
		"member_until":   mem.Until,
		"member_days":    mem.Days,
		"member_forever": mem.Forever,
		"is_member":      mem.IsMember,
		"tier":           mem.Tier,
		"tier_name":      mem.TierName,
	})
}

// handleCheckin 执行当日签到。
func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if !set.CheckinEnabled {
		writeError(w, http.StatusForbidden, CodeFeatureDisabled, "本站未开启签到", nil)
		return
	}
	res, err := s.store.Checkin(r.Context(), u.ID, set.CheckinDays, set.CheckinBonusEvery, set.CheckinBonusDays, set.CheckinTier)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if res.AlreadyDone {
		writeError(w, http.StatusConflict, CodeValidationFailed, "今天已经签到过了，明天再来", nil)
		return
	}
	// 会员期变了，配额要跟着更新（快照里的 DailyQueries 与档位都取自它）
	if res.Granted > 0 {
		if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
			s.log.Warn("签到后重建快照失败", "user", u.ID, "err", err)
		}
	}
	msg := ""
	if res.Bonus > 0 {
		msg = "连续签到奖励触发，额外多得 " + itoa(res.Bonus) + " 天"
	}

	// 签到奖励发一条站内通知（需求 3）。
	//
	// 只在真的拿到天数时发：
	//   - 普通签到（granted=0）每天都发生，发通知等于把通知页变成打卡流水；
	//   - 奖励（连签满 N 天、或者按档位发会员）是低频事件，用户值得知道。
	//
	// 去重键按「用户 + 北京时间日期」：同一天重复调用接口（并发两次）
	// 不会发出两条。
	if res.Bonus > 0 || res.Granted > 0 {
		title := "签到奖励已到账"
		body := ""
		switch {
		case res.Bonus > 0 && res.Granted > 0:
			body = "连续签到 " + itoa(res.Streak) + " 天，获得 " + itoa(res.Granted) + " 天会员（含连签奖励 " + itoa(res.Bonus) + " 天）。"
		case res.Bonus > 0:
			body = "连续签到 " + itoa(res.Streak) + " 天，额外获得 " + itoa(res.Bonus) + " 天会员。"
		default:
			body = "今日签到获得 " + itoa(res.Granted) + " 天会员。"
		}
		n := &model.Notification{
			UserID:    u.ID,
			Kind:      model.NotifyCheckinBonus,
			Title:     title,
			Body:      body,
			Link:      "/app/account",
			DedupeKey: "checkin:" + u.ID + ":" + strconv.FormatInt(bjtime.Today(), 10),
		}
		if _, err := s.store.CreateNotification(r.Context(), n); err != nil {
			// 通知失败不该让签到整体失败：天数已经加上了，用户重试会被告知
			// 「今天已签到」反而更困惑。
			s.log.Warn("发送签到奖励通知失败", "user", u.ID, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"granted":       res.Granted,
		"bonus":         res.Bonus,
		"streak":        res.Streak,
		"total_days":    res.CheckinState.TotalDays,
		"checked_today": true,
		"member_until":  res.MemberUntil,
		"member_days":   (&model.User{MemberUntil: res.MemberUntil}).MemberRemainingDays(time.Now()),
		"bonus_message": msg,
	})
}
