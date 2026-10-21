package api

import (
	"net/http"
	"strconv"
	"strings"
)

// handleFlushCache 让管理员手动清空解析缓存。
//
// 为什么不直接调 Cache.Flush：缓存在 dnsd 进程里，apid 碰不到它
// （两个进程只通过快照文件通信，这是「API 挂了不影响解析」的前提）。
// 所以这里只做一件事 —— 把「清除缓存」的序号 +1 并重建快照，
// dnsd 发现序号变了就自己清。副作用是清除不会立刻生效，
// 要等 dnsd 下一次轮询（默认 30 秒），接口返回值里会说明这一点。
func (s *Server) handleFlushCache(w http.ResponseWriter, r *http.Request) {
	cur, err := s.store.GetMeta(r.Context(), "cache_flush_seq")
	if err != nil {
		writeStoreError(w, err, "读取清除序号失败")
		return
	}
	seq, _ := strconv.ParseInt(strings.TrimSpace(cur), 10, 64)
	seq++
	if err := s.store.SetMeta(r.Context(), "cache_flush_seq", strconv.FormatInt(seq, 10)); err != nil {
		writeStoreError(w, err, "写入清除序号失败")
		return
	}

	// 立刻重建快照，不然要等到下一次配置变更才会带上新序号
	if _, err := s.coord.Rebuild(r.Context()); err != nil {
		writeStoreError(w, err, "重建快照失败")
		return
	}

	actor := currentUser(r)
	if actor != nil {
		s.store.LogAudit(r.Context(), actor.ID, "admin_flush_cache",
			"手动清除解析缓存 seq="+strconv.FormatInt(seq, 10), clientIP(r, s.cfg.API.TrustedProxies))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"seq":     seq,
		"message": "清除指令已下发，dnsd 会在下次轮询（通常 30 秒内）清空缓存",
	})
}

// handleFlushMyCache 让普通用户清空「自己那份」缓存。
//
// 为什么要单独一个序号：全局 CacheFlushSeq 一改，全站所有租户的缓存都会被清掉。
// 普通用户点一下清除不该影响别人，所以用 cache_flush_user_seq:<uid> 单独计数，
// dnsd 只对该用户调 FlushUsers。
func (s *Server) handleFlushMyCache(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "请先登录", nil)
		return
	}
	key := "cache_flush_user_seq:" + u.ID
	cur, err := s.store.GetMeta(r.Context(), key)
	if err != nil {
		writeStoreError(w, err, "读取清除序号失败")
		return
	}
	seq, _ := strconv.ParseInt(strings.TrimSpace(cur), 10, 64)
	seq++
	if err := s.store.SetMeta(r.Context(), key, strconv.FormatInt(seq, 10)); err != nil {
		writeStoreError(w, err, "写入清除序号失败")
		return
	}
	// 只标记这个用户变更，走增量快照（不必重建全站）
	if _, err := s.coord.Changed(r.Context(), u.ID); err != nil {
		writeStoreError(w, err, "重建快照失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"seq":     seq,
		"message": "已提交，dnsd 会在下次轮询（通常 30 秒内）清空你的缓存条目",
	})
}
