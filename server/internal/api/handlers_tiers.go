package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// tierIDPattern 限制档位 id 的字符集。
//
// id 会进数据库、进快照、出现在接口路径里，允许中文或空格会带来一堆转义问题。
// 显示名（name）随便写中文，id 只留小写字母数字和短横线。
var tierIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// handleListTiers 列出全部会员种类。
func (s *Server) handleListTiers(w http.ResponseWriter, r *http.Request) {
	tiers, err := s.store.ListTiers(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tiers})
}

// handleSaveTier 新建或更新一个会员种类。
func (s *Server) handleSaveTier(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Sort         int    `json:"sort"`
		MaxRules     int    `json:"max_rules"`
		MaxSubs      int    `json:"max_subs"`
		DailyQueries int64  `json:"daily_queries"`
		IsDefault    bool   `json:"is_default"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ID = strings.ToLower(strings.TrimSpace(body.ID))
	body.Name = strings.TrimSpace(body.Name)
	if !tierIDPattern.MatchString(body.ID) {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "档位标识只能用小写字母、数字、短横线，最长 32 位", nil)
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "档位名称不能为空", nil)
		return
	}
	if body.MaxRules < 0 || body.MaxSubs < 0 || body.DailyQueries < 0 {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "配额不能是负数，0 表示不限", nil)
		return
	}
	t := model.TierDef{
		ID:           body.ID,
		Name:         body.Name,
		Sort:         body.Sort,
		MaxRules:     body.MaxRules,
		MaxSubs:      body.MaxSubs,
		DailyQueries: body.DailyQueries,
		IsDefault:    body.IsDefault,
	}
	if err := s.store.UpsertTier(r.Context(), t); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// free / vip 同时回写 settings 里的同名字段。
	// 「会员与邀请」页面上还摆着两档配额的输入框，两边不同步的话
	// 管理员改完这边、那边还显示旧数字，等于骗人。
	if t.ID == "free" || t.ID == "vip" {
		if err := s.syncTierToSettings(r.Context(), t); err != nil {
			s.log.Warn("档位回写设置失败", "tier", t.ID, "err", err)
		}
	}
	// 档位进了快照（每个用户的每日请求量由它算出来），必须重建才生效。
	if _, err := s.coord.Rebuild(r.Context()); err != nil {
		s.log.Warn("档位已存但快照重建失败", "tier", t.ID, "err", err)
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_save_tier", t.ID, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTier 删除一个会员种类。
func (s *Server) handleDeleteTier(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteTier(r.Context(), id); err != nil {
		// 内置档位、还有用户在用的档位都会走到这里，
		// 用 422 把原因原样带回去，管理员才知道该先做什么。
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if _, err := s.coord.Rebuild(r.Context()); err != nil {
		s.log.Warn("档位已删但快照重建失败", "tier", id, "err", err)
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_delete_tier", id, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetUserTier 给用户指定档位。tier_id 传空串 = 按会员状态自动判定。
func (s *Server) handleSetUserTier(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		TierID string `json:"tier_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.TierID = strings.TrimSpace(body.TierID)
	if body.TierID != "" {
		// 必须指向一个真实存在的档位，否则配额会静默退化成默认档。
		if _, err := s.store.GetTier(r.Context(), body.TierID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, "档位不存在", nil)
			return
		}
	}
	if err := s.store.SetUserTier(r.Context(), id, body.TierID); err != nil {
		writeStoreError(w, err, "用户不存在")
		return
	}
	if _, err := s.coord.Rebuild(r.Context()); err != nil {
		s.log.Warn("用户档位已改但快照重建失败", "user", id, "err", err)
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_set_user_tier", id+"→"+body.TierID, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, map[string]any{"tier_id": body.TierID})
}

// syncTierToSettings 把 free/vip 档位的配额回写到全局设置。
//
// 两个地方都存了这两档的配额：settings 给「会员与邀请」页面显示，
// tiers 给配额换算用。不双向同步就会出现「改了这边那边还是旧数字」，
// 管理员根本不知道哪个在生效。
func (s *Server) syncTierToSettings(ctx context.Context, t model.TierDef) error {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if t.ID == "free" {
		set.FreeMaxRules = t.MaxRules
		set.FreeMaxSubs = t.MaxSubs
		set.FreeDailyQueries = t.DailyQueries
	} else {
		set.VipMaxRules = t.MaxRules
		set.VipMaxSubs = t.MaxSubs
		set.VipDailyQueries = t.DailyQueries
	}
	return s.store.SaveSettings(ctx, set)
}

// syncSettingsToTiers 是反方向：改设置时把 free/vip 同步进 tiers 表。
func (s *Server) syncSettingsToTiers(ctx context.Context) {
	set, err := s.store.GetSettings(ctx)
	if err != nil {
		return
	}
	tiers, err := s.store.ListTiers(ctx)
	if err != nil {
		return
	}
	for _, t := range tiers {
		switch t.ID {
		case "free":
			t.MaxRules, t.MaxSubs, t.DailyQueries = set.FreeMaxRules, set.FreeMaxSubs, set.FreeDailyQueries
		case "vip":
			t.MaxRules, t.MaxSubs, t.DailyQueries = set.VipMaxRules, set.VipMaxSubs, set.VipDailyQueries
		default:
			continue
		}
		if err := s.store.UpsertTier(ctx, t); err != nil {
			s.log.Warn("设置同步到档位失败", "tier", t.ID, "err", err)
		}
	}
}
