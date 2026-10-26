package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这个文件实现「同一份订阅被多个用户订阅 → 自动收录为全局规则」。
//
// 需求原文：「当用户订阅相同数量超过 3 个，直接改为全局规则，并提醒这几个
// 用户对应的订阅提醒，该订阅超过 3 人订阅已被收录，以删除您的订阅」。
//
// 关键设计取舍：
//
//  1. **判定按「不同用户数」，不按「订阅条数」**。同一个人把同一个链接建了
//     三份，不是「3 个人在订」，收成全局等于替一个人做决定。所以阈值比的是
//     去重后的用户数（默认 3，可配）。
//
//  2. **只收 block/allow 类别一致的组**。同一份链接被一个人当拦截列表、
//     另一个当放行列表，说明它是条件规则；收成全局会把 A 的规则套到 B 身上，
//     造成真实的拦截错误。这类宁可不收，留给人工判断。
//
//  3. **收录后删除原私有订阅**，这是需求明确要的（「以删除您的订阅」）。
//     因为不收掉，全局规则和私有规则会同时生效，同一份域名被拦两次 ——
//     不致命，但用户会看到「我的订阅还在，为什么说被收录了」。
//
//  4. **收录前复核**。候选是在定时刷新后算出来的，到真正执行可能过了很久，
//     期间用户可能已经删了订阅或改了类别。执行前重查一遍，条件不满足就跳过。

// promoteThreshold 是「几个人订阅同一份内容」才自动收录。
//
// 需求写的是「超过 3 个」，取 3 作为默认阈值（>= 3）。
// 做成可配是为了能关掉（设成很大的值等于关闭）。
const promoteThreshold = 3

// PromoteResult 是一次自动收录的结果。
type PromoteResult struct {
	// Promoted 本次新收录的组数。
	Promoted int `json:"promoted"`
	// RemovedSubs 因收录而被删除的私有订阅数。
	RemovedSubs int `json:"removed_subs"`
	// Notified 收到「订阅被收录」通知的用户数。
	Notified int `json:"notified"`
	// Groups 每组的具体信息（供管理端展示）。
	Groups []PromoteGroupInfo `json:"groups"`
}

// PromoteGroupInfo 是一组被收录内容的概要。
type PromoteGroupInfo struct {
	URLs     []string `json:"urls"`
	Users    int      `json:"users"`
	Subs     int      `json:"subs"`
	ListType string   `json:"list_type"`
	SubID    string   `json:"sub_id"`
}

// RunSubscriptionPromotion 扫描并收录被多人订阅的内容。
//
// 幂等：同一步骤重复执行不会重复收录（已有全局订阅时只做清理与通知），
// 通知本身靠 dedupe_key 去重。
func (s *Server) RunSubscriptionPromotion(ctx context.Context) (*PromoteResult, error) {
	cands, err := s.store.ListPromotionCandidates(ctx, promoteThreshold)
	if err != nil {
		return nil, err
	}

	res := &PromoteResult{Groups: []PromoteGroupInfo{}}
	notifiedUsers := map[string]struct{}{}

	for _, c := range cands {
		if len(c.UserIDs) < promoteThreshold {
			continue
		}
		// 复核：这一组里的订阅必须全部还在、且都还是私有的。
		// 候选是定时任务算的，可能已经过去很久。
		fresh, err := s.recheckPromotionCandidate(ctx, c)
		if err != nil || !fresh {
			s.log.Debug("收录候选复核未通过，跳过", "key", c.CanonicalKey)
			continue
		}

		subID, affected, err := s.store.PromoteSubscriptions(ctx, c)
		if err != nil {
			s.log.Warn("自动收录订阅失败", "key", c.CanonicalKey, "err", err)
			continue
		}

		res.Promoted++
		res.RemovedSubs += len(affected)
		res.Groups = append(res.Groups, PromoteGroupInfo{
			URLs: c.URLs, Users: len(c.UserIDs), Subs: len(c.Subs),
			ListType: c.ListType, SubID: subID,
		})

		// 给每个受影响的人发通知。同一个人可能在这一组里有多个订阅，
		// 只发一条（用 dedupe_key 按用户 + 订阅内容去重）。
		users := map[string]struct{}{}
		for _, a := range affected {
			users[a.UserID] = struct{}{}
		}
		for uid := range users {
			n := &model.Notification{
				UserID:    uid,
				Kind:      model.NotifySubPromoted,
				Title:     "订阅已被收录为全局规则",
				Body:      fmt.Sprintf("您的订阅《%s》已有 %d 人订阅，已自动收录为全站共享规则。为避免重复生效，您的这份订阅已删除，拦截效果由全局规则继续提供，无需重新添加。", firstNonEmpty(c.URLs, ""), len(c.UserIDs)),
				Link:      "/app/subscriptions",
				RefID:     subID,
				DedupeKey: "promote:" + subID + ":" + uid,
			}
			if _, err := s.store.CreateNotification(ctx, n); err != nil {
				s.log.Warn("发送订阅收录通知失败", "user", uid, "err", err)
				continue
			}
			notifiedUsers[uid] = struct{}{}
		}
	}

	res.Notified = len(notifiedUsers)

	if res.Promoted > 0 {
		// 规则集变了必须重建快照，否则 dnsd 还在用旧规则。
		// 全局层影响所有人，所以 global=true，uid 传空。
		if err := s.touched(ctx, "", true); err != nil {
			s.log.Warn("收录后重建快照失败", "err", err)
		}
		s.log.Info("订阅自动收录完成",
			"组数", res.Promoted, "删除私有订阅", res.RemovedSubs, "通知用户", res.Notified)
	}
	return res, nil
}

// recheckPromotionCandidate 在执行收录前复核候选是否仍然成立。
func (s *Server) recheckPromotionCandidate(ctx context.Context, c *store.PromoteCandidate) (bool, error) {
	users := map[string]struct{}{}
	for _, sub := range c.Subs {
		cur, err := s.store.GetSub(ctx, sub.UserID, sub.ID)
		if err != nil || cur == nil {
			// 订阅已被删除：这一组的状态变了，本轮跳过。
			return false, nil
		}
		if cur.IsGlobal || !cur.Enabled {
			return false, nil
		}
		if cur.ListType != c.ListType {
			// 类别被改过，不再是一致的组。
			return false, nil
		}
		users[cur.UserID] = struct{}{}
	}
	return len(users) >= promoteThreshold, nil
}

// handleAdminRunPromotion 手动触发一次自动收录（管理端按钮）。
func (s *Server) handleAdminRunPromotion(w http.ResponseWriter, r *http.Request) {
	res, err := s.RunSubscriptionPromotion(r.Context())
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAdminPromotionPreview 预览「如果现在执行，会收录哪些」。
//
// 与执行分开是因为这个动作会删用户订阅 —— 管理员该先看到清单再决定。
func (s *Server) handleAdminPromotionPreview(w http.ResponseWriter, r *http.Request) {
	cands, err := s.store.ListPromotionCandidates(r.Context(), promoteThreshold)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		users := make([]string, 0, len(c.UserIDs))
		for _, uid := range c.UserIDs {
			name := uid
			if u, err := s.store.GetUserByID(r.Context(), uid); err == nil && u != nil && u.Username != "" {
				name = u.Username
			}
			users = append(users, name)
		}
		items = append(items, map[string]any{
			"urls":      c.URLs,
			"users":     users,
			"user_count": len(c.UserIDs),
			"subs":      len(c.Subs),
			"list_type": c.ListType,
			"threshold": promoteThreshold,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"threshold": promoteThreshold,
		"groups":    items,
	})
}

func firstNonEmpty(ss []string, def string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return def
}
