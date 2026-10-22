package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleAdminUserRules 跨租户列出「用户自己加的规则」。
//
// 管理员的 /admin/rules 只有全局层，普通用户的规则在后台一直不可见：
// 用户反馈「某个域名被拦了」，管理员在后台翻不到那条规则，只能让他自己
// 截图。这里补上只读视图，支持按用户、关键字、类型筛选。
func (s *Server) handleAdminUserRules(w http.ResponseWriter, r *http.Request) {
	_, size, offset := pagination(r, 50)
	q := r.URL.Query()
	items, total, err := s.store.ListAllUserRules(
		r.Context(), q.Get("user_id"), q.Get("q"), q.Get("kind"), size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  offset/size + 1,
		"size":  size,
	})
}

// handleAdminUserSubs 跨租户列出用户自己的订阅。
func (s *Server) handleAdminUserSubs(w http.ResponseWriter, r *http.Request) {
	_, size, offset := pagination(r, 50)
	q := r.URL.Query()
	items, total, err := s.store.ListAllUserSubs(
		r.Context(), q.Get("user_id"), q.Get("q"), size, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  offset/size + 1,
		"size":  size,
	})
}

// handleAdminDeleteUserRule 删除某个用户的一条规则。
//
// 管理员的规则接口一直只能改全局层，遇到用户加了一条把整站搞挂的规则
// （比如把自家域名拦了），只能让用户自己去删。这里给一条运维出口。
// 删的是别人的东西，必须落审计。
func (s *Server) handleAdminDeleteUserRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// 先查出来拿到 owner，用于通知快照重建与审计描述。
	rule, err := s.store.GetRuleByID(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "规则不存在")
		return
	}
	// 只允许删用户规则：全局规则走 /admin/rules/{id}，两条路径各管各的，
	// 免得一次手误把全局拦截清掉。
	if rule.IsGlobal {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed,
			"这是全局规则，请到全局规则页删除", nil)
		return
	}
	if err := s.store.DeleteRule(r.Context(), rule.UserID, id); err != nil {
		writeStoreError(w, err, "")
		return
	}
	// 规则变更必须让「这个用户」的缓存失效，否则旧应答在 TTL 内继续命中，
	// 用户看到的是「删了规则还照样拦」。
	// 只清他的：全站清空会让所有租户命中率归零并向上游打一波真实查询。
	key := "cache_flush_user_seq:" + rule.UserID
	if cur, err := s.store.GetMeta(r.Context(), key); err == nil {
		seq, _ := strconv.ParseInt(strings.TrimSpace(cur), 10, 64)
		_ = s.store.SetMeta(r.Context(), key, strconv.FormatInt(seq+1, 10))
	}
	// 只标记这个用户变更：增量快照即可，不必重建全站。
	if _, err := s.coord.Changed(r.Context(), rule.UserID); err != nil {
		s.log.Warn("用户规则已删但快照重建失败", "rule", id, "err", err)
	}
	s.store.LogAudit(r.Context(), currentUser(r).ID, "admin_delete_user_rule",
		rule.Pattern, clientIP(r, s.cfg.API.TrustedProxies))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
