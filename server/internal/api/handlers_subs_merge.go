package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

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

	// 任何一条都不该已经是全局订阅：合并的语义是「把私有的重复项收成一条
	// 全局」，已经全局的没有「合并」的必要，重复点击也只会报错。
	for _, sub := range subs {
		if sub.IsGlobal {
			writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
				"订阅《"+sub.Name+"》已经是全局订阅，无需合并", nil)
			return
		}
	}

	res, err := s.store.MergeSubscriptions(ctx, req.SubIDs, keepID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}

	// 留痕：把「删了谁、留了谁」写进日志。
	//
	// 这是本次线上问题暴露出来的缺口：当时合并成功但通知全失败，而库里
	// 被删的订阅行已经不存在了，事后**无法反查那几个用户是谁**，
	// 也就没法补发通知。
	// 日志是唯一不随数据删除而消失的记录，必须在提交后立刻写。
	deleted := make([]string, 0, len(res.RemovedSubs))
	for _, a := range res.RemovedSubs {
		deleted = append(deleted, a.UserID+"/"+a.SubID)
	}
	s.log.Info("合并重复订阅",
		"keep", res.KeepID, "keep_owner", res.KeepOwnerID,
		"deleted", deleted, "affected_users", len(res.AffectedUsers))

	// --- 后续动作必须脱离请求上下文（这是本次线上故障的根因）---
	//
	// 实测：合并本身 200 成功、数据也对，但重建快照跑了 19 秒，浏览器/代理
	// 先断开连接，r.Context() 被取消 —— 于是建快照失败、**所有通知一条都
	// 没发出去**，日志里是 context canceled。
	//
	// 后果是「静默半成功」：用户看到报错，管理员看到合并成功但没人收到通知，
	// 而且没有任何东西会在事后补上。
	//
	// 所以合并事务提交之后就与请求解耦：后台用独立 ctx 做完重建与通知，
	// 接口立刻返回。合并已经落库，这两件事失败也不该让接口报错。
	merged := res
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		s.finishMerge(bg, merged, len(req.SubIDs))
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"keep_id":        res.KeepID,
		"removed":        len(res.RemovedSubs),
		"affected_users": len(res.AffectedUsers),
		// 通知是后台发的，此刻还没发完，所以这里不报具体人数，
		// 只说明「已安排」。前端文案据此调整，避免显示一个恒为 0 的数。
		"notify_async": true,
		// global=true 让前端能明确提示「已转为全局订阅」，
		// 而不是含糊地说「已合并」。
		"global": true,
	})
}

// finishMerge 在合并提交之后做两件事：全量重建快照、给所有受影响用户发通知。
//
// 独立于 HTTP 请求运行，理由见 handleAdminMergeDuplicateSubs 里的注释。
// 每一步独立成败，失败只记日志 —— 合并本身已经落库，不能因为发通知失败
// 就让管理员以为整次操作失败（他重试会得到「已经是全局订阅」的报错）。
func (s *Server) finishMerge(ctx context.Context, res *store.MergeResult, total int) {
	// 全量重建：合并后的订阅对所有启用全局层的用户生效，
	// 只重建这几个人会让其余用户停在旧规则上。
	if err := s.touched(ctx, "", true); err != nil {
		s.log.Warn("合并订阅后重建快照失败", "sub", res.KeepID, "err", err)
	}

	// 通知所有受影响的人（含保留者）。
	//
	// 三件事都要说清楚，否则用户会以为自己的订阅莫名其妙消失了：
	//   1. 这条内容**已转为全局规则**，仍然生效；
	//   2. 因为重复，他自己的那份订阅被删掉了；
	//   3. 保留者的那份从「我的订阅」移到了全站，所以他的列表里也会消失。
	//
	// 保留者的通知文案与其他人不同：他的订阅不是「被删」，是「升级成全局」，
	// 说成「已删除」会让他去找管理员要回来。
	notified := 0
	for _, a := range res.RemovedSubs {
		n := &model.Notification{
			UserID: a.UserID,
			Kind:   model.NotifySubMerged,
			Title:  "重复订阅已合并为全局规则",
			Body: fmt.Sprintf(
				"您的订阅《%s》与另外几位用户订阅的是同一份内容（共 %d 份重复），已合并收录为全站共享的全局规则。"+
					"您这份私有订阅已被删除，但拦截/放行效果由全局规则继续提供，无需重新添加。",
				a.Name, total),
			Link:      "/app/subscriptions",
			RefID:     res.KeepID,
			DedupeKey: "merge-removed:" + a.SubID,
		}
		if _, err := s.store.CreateNotification(ctx, n); err != nil {
			s.log.Warn("发送订阅合并通知失败", "user", a.UserID, "err", err)
			continue
		}
		notified++
	}

	// 保留者单独一条：他不是「被删」，是「自己的订阅变成了全局规则」。
	if res.KeepOwnerID != "" {
		n := &model.Notification{
			UserID: res.KeepOwnerID,
			Kind:   model.NotifySubMerged,
			Title:  "您的订阅已收录为全局规则",
			Body: fmt.Sprintf(
				"您的订阅《%s》被确认为与另外 %d 份订阅内容相同，已合并收录为全站共享的全局规则。"+
					"它已从「我的订阅」移到全局层，对所有用户生效，您自己同样继续受保护，无需重新添加。",
				res.KeepOwnerName, total-1),
			Link:      "/app/subscriptions",
			RefID:     res.KeepID,
			DedupeKey: "merge-kept:" + res.KeepID,
		}
		if _, err := s.store.CreateNotification(ctx, n); err != nil {
			s.log.Warn("发送订阅收录通知失败", "user", res.KeepOwnerID, "err", err)
		} else {
			notified++
		}
	}

	s.log.Info("订阅合并后续处理完成",
		"sub", res.KeepID, "删除", len(res.RemovedSubs), "通知", notified)
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
