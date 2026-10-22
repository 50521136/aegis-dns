package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// === 邀请码（管理员）===

// handleListInviteCodes 列出所有邀请码。
func (s *Server) handleListInviteCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := s.store.ListInviteCodes(r.Context(), "")
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if codes == nil {
		codes = []model.InviteCode{}
	}
	now := time.Now().Unix()
	items := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		items = append(items, inviteCodeJSON(c, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// inviteCodeJSON 补上「还能不能用」这类派生状态。
//
// 让前端自己判断 max_uses/expires_at/enabled 三个条件很容易漏掉一个，
// 表现就是后台显示「可用」但注册时被拒。
func inviteCodeJSON(c model.InviteCode, now int64) map[string]any {
	usable, reason := true, ""
	switch {
	case !c.Enabled:
		usable, reason = false, "已停用"
	case c.ExpiresAt > 0 && now > c.ExpiresAt:
		usable, reason = false, "已过期"
	case c.MaxUses > 0 && c.UsedCount >= c.MaxUses:
		usable, reason = false, "次数已用完"
	}
	out := map[string]any{
		"code":        c.Code,
		"owner_id":    c.OwnerID,
		"note":        c.Note,
		"reward_days": c.RewardDays,
		"reward_tier": c.RewardTier,
		"max_uses":    c.MaxUses,
		"used_count":  c.UsedCount,
		"expires_at":  c.ExpiresAt,
		"enabled":     c.Enabled,
		"created_at":  c.CreatedAt,
		"usable":      usable,
	}
	if reason != "" {
		out["unusable_reason"] = reason
	}
	return out
}

type inviteCodeReq struct {
	Code       string `json:"code"`
	Note       string `json:"note"`
	RewardDays *int   `json:"reward_days"`
	// RewardTier 是核销后发给邀请人的会员种类；不传 = 用全局设置。
	RewardTier *string `json:"reward_tier"`
	MaxUses    *int    `json:"max_uses"`
	ExpiresAt  *int64  `json:"expires_at"`
	Enabled    *bool   `json:"enabled"`
}

// handleCreateInviteCode 新建邀请码。不填 code 时自动生成。
func (s *Server) handleCreateInviteCode(w http.ResponseWriter, r *http.Request) {
	var req inviteCodeReq
	if !decodeJSON(w, r, &req) {
		return
	}
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		code = randomCode(8)
	}
	// 只允许字母数字：邀请码经常被口头转述或手抄，
	// 带符号和空格会制造大量「明明输对了却提示错误」的客服问题。
	for _, c := range code {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "邀请码只能包含字母和数字", nil)
			return
		}
	}
	if len(code) < 4 || len(code) > 32 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "邀请码长度需在 4 到 32 位之间", nil)
		return
	}

	reward := set.MemberRewardDays
	if req.RewardDays != nil {
		reward = *req.RewardDays
	}
	// 奖励种类：码上指定的优先，否则用全局的「邀请奖励会员种类」。
	rewardTier := set.MemberRewardTier
	if req.RewardTier != nil {
		rewardTier = strings.TrimSpace(*req.RewardTier)
	}
	if rewardTier != "" {
		if _, err := s.store.GetTier(r.Context(), rewardTier); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "会员种类不存在", nil)
			return
		}
	}
	if reward < 0 || reward > 3650 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "奖励天数需在 0 到 3650 之间", nil)
		return
	}
	maxUses := 0
	if req.MaxUses != nil {
		maxUses = *req.MaxUses
	}
	if maxUses < 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "使用次数不能为负（0 = 不限）", nil)
		return
	}
	var expires int64
	if req.ExpiresAt != nil {
		expires = *req.ExpiresAt
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	owner := currentUser(r)
	ownerID := ""
	if owner != nil {
		ownerID = owner.ID
	}
	c := &model.InviteCode{
		Code: code, OwnerID: ownerID, Note: strings.TrimSpace(req.Note),
		RewardDays: reward, RewardTier: rewardTier, MaxUses: maxUses, ExpiresAt: expires,
		Enabled: enabled,
	}
	if err := s.store.CreateInviteCode(r.Context(), c); err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, inviteCodeJSON(*c, time.Now().Unix()))
}

// handleUpdateInviteCode 修改邀请码的可变字段。
func (s *Server) handleUpdateInviteCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	var req inviteCodeReq
	if !decodeJSON(w, r, &req) {
		return
	}
	var note *string
	if req.Note != "" {
		n := strings.TrimSpace(req.Note)
		note = &n
	}
	var expires *int64
	if req.ExpiresAt != nil {
		expires = req.ExpiresAt
	}
	if req.RewardTier != nil {
		if t := strings.TrimSpace(*req.RewardTier); t != "" {
			if _, err := s.store.GetTier(r.Context(), t); err != nil {
				writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "会员种类不存在", nil)
				return
			}
		}
	}
	if err := s.store.UpdateInviteCode(r.Context(), strings.ToUpper(code), note,
		req.RewardDays, req.RewardTier, req.MaxUses, expires, req.Enabled); err != nil {
		writeStoreError(w, err, "邀请码不存在")
		return
	}
	c, err := s.store.GetInviteCode(r.Context(), code)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "邀请码不存在", nil)
		return
	}
	writeJSON(w, http.StatusOK, inviteCodeJSON(*c, time.Now().Unix()))
}

// handleDeleteInviteCode 删除邀请码。
func (s *Server) handleDeleteInviteCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if err := s.store.DeleteInviteCode(r.Context(), strings.ToUpper(code)); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// === 会员（管理员）===

type memberReq struct {
	// Days 是要增减的天数；负数表示扣减。
	Days int `json:"days"`
	// TierID 是这次发放的会员种类；空串 = 保持用户现有种类
	// （从来没有过种类时落到默认会员档）。
	TierID string `json:"tier_id"`
	// Permanent 为真时直接给永久会员（member_until 置成 9999-12-31），
	// 此时 Days 被忽略。用时间戳而不是额外字段表示永久，
	// 免得每处判断会员的地方都要多看一眼布尔值。
	Permanent bool `json:"permanent"`
}

// handleSetMember 调整某个用户的会员期（可同时指定会员种类）。
//
// 正数是加、负数是扣，都从「现在与原到期时间中较晚者」起算，
// 这样连续加不会冲掉剩余时间，扣减也不会把已过期的用户扣成负数。
//
// 这是**唯一**的管理员改会员入口：种类和有效期一起改。
// 之前还有一个独立的「指定档位」接口，两个入口能分别改同一个用户的
// 两个字段，界面上就出现了「会员档」和「超级会员」并存的状态。
func (s *Server) handleSetMember(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	var req memberReq
	if !decodeJSON(w, r, &req) {
		return
	}
	tierID := strings.TrimSpace(req.TierID)
	if tierID != "" {
		if _, err := s.store.GetTier(r.Context(), tierID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "会员种类不存在", nil)
			return
		}
	}

	var until int64
	var err error
	switch {
	case req.Permanent:
		// 永久会员：种类必须给定（否则「永久」会落在哪个档上说不清）。
		if tierID == "" {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "设为永久会员时必须选择会员种类", nil)
			return
		}
		err = s.store.SetMembership(r.Context(), uid, tierID, model.MemberForever)
		until = model.MemberForever
	default:
		if req.Days == 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "days 不能为 0", nil)
			return
		}
		if req.Days > 36500 || req.Days < -36500 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "天数超出合理范围", nil)
			return
		}
		until, err = s.store.GrantMembership(r.Context(), uid, tierID, req.Days)
	}
	if err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	s.coord.Changed(r.Context())
	u, _ := s.store.GetUserByID(r.Context(), uid)
	tiers, _ := s.store.TierMap(r.Context())
	st := model.MemberStateOf(u, tiers, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"member_until":   until,
		"tier":           st.Tier,
		"tier_name":      st.TierName,
		"member_forever": st.Forever,
		"member_days":    st.Days,
	})
}

// handleRevokeMember 直接取消会员（到期时间置 0，会员种类一并清掉）。
func (s *Server) handleRevokeMember(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	if err := s.store.RevokeMembership(r.Context(), uid); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	s.coord.Changed(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"member_until": 0, "tier": string(model.TierFree)})
}

// === 会员 / 邀请（用户自己）===

// handleMyMembership 返回当前用户的档位、配额与邀请战绩。
func (s *Server) handleMyMembership(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	now := time.Now()
	tiers, _ := s.store.TierMap(r.Context())
	q := model.QuotaForUser(u, set, tiers, now)
	stats, err := s.store.GetInviteStats(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 会员状态只给一份（种类 + 有效期），前端不再自己拼。
	// tiers 给全部档位：用户页那个「各会员种类说明」的小弹窗要用，
	// 前端自己拼列表的话，管理员新加一档就得到处补映射。
	st := model.MemberStateOf(u, tiers, now)
	allTiers, _ := s.store.ListTiers(r.Context())
	if allTiers == nil {
		allTiers = []model.TierDef{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tier":           st.Tier,
		"tier_name":      st.TierName,
		"tier_id":        st.AssignedTier,
		"is_member":      st.IsMember,
		"member_forever": st.Forever,
		"member_until":   st.Until,
		"member_days":    st.Days,
		"quota": map[string]any{
			"max_rules":     q.MaxRules,
			"max_subs":      q.MaxSubs,
			"daily_queries": q.DailyQueries,
		},
		// 全部会员种类（含配额），用于「我的账户」里的小弹窗说明
		"tier_list": allTiers,
		"invite": map[string]any{
			"total_uses":  stats.TotalUses,
			"reward_days": stats.RewardDays,
		},
		// 自助重置次数上限：接入页要显示「还剩几次」，
		// 让用户自己再发一个请求只为拿这个数字没必要。
		"max_id_resets":  set.MaxIDResets,
		"id_reset_count": u.IDResetCount,
		"custom_id_used": u.CustomIDUsed,
	})
}

// randomCode 生成一个 base32 风格的邀请码（去掉易混淆的 0/1/O/I）。
func randomCode(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 熵源不可用是极端情况，退化成时间派生的码也比 panic 好
		return strings.ToUpper(time.Now().Format("150405"))
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// === 邀请码（普通用户自建）===

// handleMyInviteCodes 返回当前用户名下的邀请码与分享链接。
//
// 普通用户能自建邀请码，这是拉新激励的关键：只有管理员能发码的话，
// 这套东西就退化成「管理员发福利」，用户没有动力去分享。
// 但奖励天数不给用户填 —— 能自选天数等于能给自己无限续会员。
func (s *Server) handleMyInviteCodes(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	codes, err := s.store.ListInviteCodes(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	stats, err := s.store.GetInviteStats(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	now := time.Now().Unix()
	items := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		item := inviteCodeJSON(c, now)
		item["link"] = inviteLink(s.domain(r), c.Code)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
		// 还能建几个：前端据此决定按钮是否可点，避免点了才报错
		"can_create":  set.AllowUserInvite && set.UserInviteMaxCodes > len(codes),
		"enabled":     set.AllowUserInvite,
		"max_codes":   set.UserInviteMaxCodes,
		"reward_days": set.MemberRewardDays,
		// 奖励会员种类的显示名：邀请卡上要写清「+30 天高级会员」，
		// 否则多档位之后用户不知道奖励发的是哪一种。
		"reward_tier_name": tierDisplayName(r.Context(), s, set.MemberRewardTier),
		"invited":          stats.TotalUses,
		"earned_days":      stats.RewardDays,
		"member_until":     u.MemberUntil,
		"member_days":      u.MemberRemainingDays(time.Now()),
	})
}

// tierDisplayName 把会员种类 id 翻成显示名，查不到时给出可读兜底。
func tierDisplayName(ctx context.Context, s *Server, tierID string) string {
	if strings.TrimSpace(tierID) == "" {
		return ""
	}
	if t, err := s.store.GetTier(ctx, tierID); err == nil && t != nil {
		return t.Name
	}
	return tierID
}

// inviteLink 拼出可直接分享的注册链接。
func inviteLink(domain, code string) string {
	return "https://" + domain + "/register?invite=" + code
}

// handleCreateMyInviteCode 普通用户自建邀请码。
func (s *Server) handleCreateMyInviteCode(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	set, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if !set.AllowUserInvite {
		writeError(w, http.StatusForbidden, CodeFeatureDisabled, "本站未开放用户自建邀请码", nil)
		return
	}
	if !set.AllowRegister {
		// 注册都关着，码发出去也用不了
		writeError(w, http.StatusForbidden, CodeFeatureDisabled, "当前已关闭公开注册，邀请码无法使用", nil)
		return
	}
	mine, err := s.store.ListInviteCodes(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if set.UserInviteMaxCodes > 0 && len(mine) >= set.UserInviteMaxCodes {
		writeError(w, http.StatusForbidden, CodeQuotaExceeded,
			fmt.Sprintf("你最多持有 %d 个邀请码，先删掉不用的再建", set.UserInviteMaxCodes), nil)
		return
	}
	// 奖励天数固定用全局值；次数不限（一个码可以分享给多人）。
	c := &model.InviteCode{
		Code:       randomCode(8),
		OwnerID:    u.ID,
		Note:       "用户自建",
		RewardDays: set.MemberRewardDays,
		MaxUses:    0,
		Enabled:    true,
	}
	if err := s.store.CreateInviteCode(r.Context(), c); err != nil {
		writeStoreError(w, err, "")
		return
	}
	out := inviteCodeJSON(*c, time.Now().Unix())
	out["link"] = inviteLink(s.domain(r), c.Code)
	writeJSON(w, http.StatusCreated, out)
}

// handleDeleteMyInviteCode 删除自己的邀请码。
func (s *Server) handleDeleteMyInviteCode(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	code := strings.ToUpper(chi.URLParam(r, "code"))
	c, err := s.store.GetInviteCode(r.Context(), code)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if c == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "邀请码不存在", nil)
		return
	}
	// 只能删自己的：不校验归属的话，任何用户都能把别人正在推广的码删掉
	if c.OwnerID != u.ID {
		writeError(w, http.StatusForbidden, CodeForbidden, "只能删除自己的邀请码", nil)
		return
	}
	if err := s.store.DeleteInviteCode(r.Context(), code); err != nil {
		writeStoreError(w, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
