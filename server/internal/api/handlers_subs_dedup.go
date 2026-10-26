package api

import (
	"net/http"

	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// 订阅链接重复检测。
//
// 只做「识别」，不改任何数据：把内容等价的订阅分组报给管理员，
// 由人决定要不要处理。自动合并的收益（省几次下载）远小于判错一次的
// 代价（一个人的规则套到另一个人身上，造成真实拦截错误）。
//
// 识别规则见 internal/subsub/canonical.go：只认链接形态，
// 绝不下载内容去比对 —— 为了「猜两个源是不是同一个」而把两份
// 几十万行的列表都拉下来解析，比重复拉取更贵。
//
// 注意：定时刷新本身已经会**自动共享**同组订阅的下载与解析
// （见 subsub.fetchCache），所以这里报出来的重复项不需要人工处理
// 也不会重复拉取；报出来是为了让管理员知道「哪几个人订了同一份」，
// 可以在需要时清理。

// handleAdminDuplicateSubs 列出疑似重复的订阅（覆盖全部租户）。
func (s *Server) handleAdminDuplicateSubs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subs, err := s.store.AllEnabledSubs(ctx)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	// LastCount 用库里记录的值，不重新统计 —— 一次点击触发几百万行
	// COUNT 是不可接受的。
	refs := make([]*subsub.DupInput, 0, len(subs))
	// 顺手建索引，下面补名字时就不用逐条再查库（那是 N+1）。
	bySub := make(map[string]string, len(subs)) // subID -> name
	byUser := make(map[string]string, len(subs))
	for _, sub := range subs {
		refs = append(refs, &subsub.DupInput{
			ID:        sub.ID,
			UserID:    sub.UserID,
			URL:       sub.URL,
			LastCount: sub.LastCount,
		})
		bySub[sub.ID] = sub.Name
	}
	// 用户名单独取一次，按需填充。
	for _, sub := range subs {
		if _, ok := byUser[sub.UserID]; ok {
			continue
		}
		if u, uerr := s.store.GetUser(ctx, sub.UserID); uerr == nil && u != nil {
			byUser[sub.UserID] = u.Username
		}
	}

	groups := subsub.FindDuplicateGroups(refs)

	out := make([]map[string]any, 0, len(groups))
	savedFetches := 0
	for _, g := range groups {
		items := make([]map[string]any, 0, len(g.SubIDs))
		for i, sid := range g.SubIDs {
			uid := ""
			if i < len(g.UserIDs) {
				uid = g.UserIDs[i]
			}
			items = append(items, map[string]any{
				"id":       sid,
				"user_id":  uid,
				"username": byUser[uid],
				"name":     bySub[sid],
			})
		}
		savedFetches += g.ExtraFetches
		out = append(out, map[string]any{
			"canonical_key": g.CanonicalKey,
			"urls":          g.URLs,
			"entries":       g.Entries,
			"extra_fetches": g.ExtraFetches,
			"subscriptions": items,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_enabled": len(subs),
		"groups":        out,
		// 每一轮自动刷新能省掉的重复拉取次数（已经是自动共享，
		// 不需要人工处理；报出来是为了让「优化有没有生效」可见）。
		"auto_saved_fetches": savedFetches,
	})
}
