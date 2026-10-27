package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// 这个文件实现「手动合并重复订阅」（需求：重复订阅检测加一个手动合并按钮）。
//
// 与自动收录的区别（两者解决的是同一类问题，但触发方式与后果不同）：
//
//	自动收录  同一份内容被 >= 4 个**不同用户**订阅 → 自动转全局并删除私有订阅
//	手动合并  管理员在检测结果里点某一组 → 保留一条，其余删除
//
// 手动合并不自动转全局：管理员点的可能是「这两个人订的是同一份，
// 合成一个人保留就行」，也可能是「这份该进全局」。两种意图不同，
// 不该由一次点击替管理员决定。要转全局请用收录功能（或先合并再收录）。
//
// 合并时保留哪一条（keep_id）由管理员指定；不指定则保留条目数最多的那条 ——
// 那是「最完整的一份」，删除其它不会再丢内容。

// handleAdminMergeDuplicateSubs 合并一组重复订阅。
//
// 请求体：
//
//	{ "sub_ids": ["sub_a","sub_b"], "keep_id": "sub_a" }
//
// keep_id 可省略，省略时保留条目数最多的一条。
func (s *Server) handleAdminMergeDuplicateSubs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubIDs []string `json:"sub_ids"`
		KeepID string   `json:"keep_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.SubIDs) < 2 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"至少选择两条订阅才能合并", nil)
		return
	}

	ctx := r.Context()

	// 逐条读出来，确认都存在、且确实属于同一份内容。
	//
	// **必须再校验一次等价性**：前端传来的是 ID 列表，可以被手工构造。
	// 如果把两份内容不同的订阅合并了，等于删掉一个人真实的规则集。
	subs := make([]*model.Subscription, 0, len(req.SubIDs))
	for _, id := range req.SubIDs {
		sub, err := s.store.GetSubByID(ctx, id)
		if err != nil || sub == nil {
			writeError(w, http.StatusNotFound, CodeNotFound, "订阅不存在: "+id, nil)
			return
		}
		subs = append(subs, sub)
	}

	// 等价性校验：所有 URL 的归一键必须一致。
	var key string
	for i, sub := range subs {
		k := subsub.CanonicalURL(sub.URL)
		if i == 0 {
			key = k
			continue
		}
		if k != key {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
				"选中的订阅指向不同内容，不能合并（"+subs[0].URL+" 与 "+sub.URL+"）", nil)
			return
		}
	}

	// 决定保留哪一条。
	keepID := req.KeepID
	if keepID == "" {
		// 不指定时保留条目数最多的：那是最完整的一份。
		best := subs[0]
		for _, sub := range subs[1:] {
			if sub.LastCount > best.LastCount {
				best = sub
			}
		}
		keepID = best.ID
	}
	// keepID 必须在选择范围内。
	keepExists := false
	for _, sub := range subs {
		if sub.ID == keepID {
			keepExists = true
			break
		}
	}
	if !keepExists {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"keep_id 不在选中的订阅里", nil)
		return
	}

	// 任何一条都不该是全局订阅：合并私有与全局会把「谁的规则」搞乱。
	for _, sub := range subs {
		if sub.IsGlobal {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
				"全局订阅不参与合并，请先在全局订阅页单独处理", nil)
			return
		}
	}

	res, err := s.store.MergeSubscriptions(ctx, req.SubIDs, keepID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	// 规则集变了：受影响用户的快照都要重建。
	for _, uid := range res.AffectedUsers {
		if err := s.touched(ctx, uid, false); err != nil {
			s.log.Warn("合并订阅后重建快照失败", "user", uid, "err", err)
		}
	}

	// 给被删掉订阅的用户发通知 —— 他的订阅消失了，必须知道为什么。
	for _, a := range res.RemovedSubs {
		n := &model.Notification{
			UserID:    a.UserID,
			Kind:      model.NotifySubMerged,
			Title:     "重复订阅已被合并",
			Body:      fmt.Sprintf("您的订阅《%s》与管理员确认的其它订阅指向同一份内容，已合并保留一份，规则效果不受影响。", a.Name),
			Link:      "/app/subscriptions",
			RefID:     res.KeepID,
			DedupeKey: "merge:" + a.SubID,
		}
		if _, err := s.store.CreateNotification(ctx, n); err != nil {
			s.log.Warn("发送订阅合并通知失败", "user", a.UserID, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"keep_id":        res.KeepID,
		"removed":        len(res.RemovedSubs),
		"affected_users": len(res.AffectedUsers),
	})
}

// handleAdminGlobalSubByURL 按 URL 查这条内容是否已有全局订阅（需求 3）。
//
// 用户在订阅页填链接时调这个接口，命中就提示「全局订阅已存在此规则」，
// 避免他重复添加一份内容与全局层完全相同的私有订阅 ——
// 那既占自己的订阅配额，也让订阅条目在他的快照里翻倍。
func (s *Server) handleAdminGlobalSubByURL(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "缺少 url 参数", nil)
		return
	}
	ctx := r.Context()

	key := subsub.CanonicalURL(raw)

	// 全局订阅数量很少（几个到几十个），直接全量比对，
	// 不需要为它建索引或写复杂 SQL。
	all, err := s.store.AllEnabledSubs(ctx)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	for _, sub := range all {
		if !sub.IsGlobal {
			continue
		}
		if subsub.CanonicalURL(sub.URL) == key {
			writeJSON(w, http.StatusOK, map[string]any{
				"exists":    true,
				"sub_id":    sub.ID,
				"name":      sub.Name,
				"url":       sub.URL,
				"list_type": sub.ListType,
				"entries":   sub.LastCount,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exists": false})
}

// lookupGlobalSubByURL 是给 createSub 复用的内部版本。
//
// 创建订阅时也要挡一次：前端提示可以被绕过（直接调 API、旧客户端缓存），
// 服务端不拦的话「重复添加全局已有的内容」照样能发生。
func (s *Server) lookupGlobalSubByURL(ctx context.Context, raw string) *model.Subscription {
	key := subsub.CanonicalURL(raw)
	all, err := s.store.AllEnabledSubs(ctx)
	if err != nil {
		return nil
	}
	for _, sub := range all {
		if sub.IsGlobal && subsub.CanonicalURL(sub.URL) == key {
			return sub
		}
	}
	return nil
}

// 保证 chi 与 store 被引用（部分构建下未直接用到）。
var (
	_ = chi.URLParam
	_ = store.SubEntry{}
)
