package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件是站内通知的 HTTP 接口。
//
// 通知是「按人产生」的轻量消息（订阅被收录、签到奖励、AI 自动回复），
// 与管理员运营的公告（announcements）是两套东西：
// 公告一个 id 对所有人可见，通知一条只属于一个人。

func notificationJSON(n *model.Notification) map[string]any {
	return map[string]any{
		"id":         n.ID,
		"kind":       n.Kind,
		"title":      n.Title,
		"body":       n.Body,
		"link":       n.Link,
		"ref_id":     n.RefID,
		"read_at":    rfc3339OrEmpty(n.ReadAt),
		"created_at": rfc3339OrEmpty(n.CreatedAt),
	}
}

// handleListMyNotifications 返回当前用户的通知列表 + 未读数。
func (s *Server) handleListMyNotifications(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 50)
	offset := atoiDefault(q.Get("offset"), 0)

	list, unread, err := s.store.ListNotifications(r.Context(), u.ID, limit, offset)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, n := range list {
		items = append(items, notificationJSON(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "unread": unread,
	})
}

// handleUnreadCounts 返回导航红点需要的各项未读数。
//
// 一次返回全部（通知 + 反馈），避免前端为两个小数字发两次请求 ——
// 这个接口会被前端在每次路由切换时调用，多一次往返是白费。
func (s *Server) handleUnreadCounts(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	notif, err := s.store.CountUnreadNotifications(r.Context(), u.ID)
	if err != nil {
		s.log.Warn("统计未读通知失败", "user", u.ID, "err", err)
	}

	// 反馈红点：
	//   用户端 —— 管理员有回复但我还没看（admin 消息时间 > user_read_at）；
	//   管理员端 —— 用户有追问而我没看（user 消息时间 > admin_read_at），
	//              再加上「AI 判定需要人工」的条目。
	fbCount := 0
	if u.Role == model.RoleAdmin {
		if n, err := s.store.CountFeedbackNeedingAdmin(r.Context()); err == nil {
			fbCount = n
		}
	} else {
		if n, err := s.store.CountFeedbackNeedingUser(r.Context(), u.ID); err == nil {
			fbCount = n
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": notif,
		"feedback":      fbCount,
	})
}

// handleMarkNotificationRead 把一条通知标记为已读。
func (s *Server) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := s.store.MarkNotificationRead(r.Context(), u.ID, chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err, "通知不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMarkAllNotificationsRead 全部标记已读。
func (s *Server) handleMarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	n, err := s.store.MarkAllNotificationsRead(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "marked": n})
}

// handleDeleteNotification 删除一条通知。
func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := s.store.DeleteNotification(r.Context(), u.ID, chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err, "通知不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// itoa 小工具（同包内已被多处使用，这里保持一致的写法）。
var _ = strconv.Itoa
