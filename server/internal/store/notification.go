package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// CreateNotification 写入一条站内通知。
//
// 当 n.DedupeKey 非空且该用户已有同 key 的通知时，**静默跳过**并返回
// (false, nil)：调用方（订阅收录、签到奖励）都是「触发即发」的场景，
// 重复触发不是错误，不该让整个业务流程因为一条通知发重而失败。
func (s *Store) CreateNotification(ctx context.Context, n *model.Notification) (bool, error) {
	if n.UserID == "" {
		return false, errors.New("写入通知失败: user_id 为空")
	}
	if n.Kind == "" {
		n.Kind = model.NotifySystem
	}
	if n.ID == "" {
		n.ID = id.New(id.PrefixNotification)
	}
	if n.CreatedAt == 0 {
		n.CreatedAt = time.Now().Unix()
	}

	var dedupe any
	if n.DedupeKey != "" {
		dedupe = n.DedupeKey
	}
	res, err := s.write.ExecContext(ctx,
		`INSERT OR IGNORE INTO notifications
		   (id, user_id, kind, title, body, link, ref_id, dedupe_key, read_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.UserID, n.Kind, n.Title, n.Body, n.Link, n.RefID, dedupe, n.ReadAt, n.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("写入通知失败: %w", err)
	}
	if n1, _ := res.RowsAffected(); n1 == 0 {
		return false, nil // 被 dedupe 唯一索引挡下
	}
	return true, nil
}

// ListNotifications 按时间倒序返回某用户的通知，并返回未读总数。
//
// unread 单独算而不是让前端数当前页：未读数要用在导航红点上，
// 而列表可能是分页的，数当前页会偏小。
func (s *Store) ListNotifications(ctx context.Context, uid string, limit, offset int) ([]*model.Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, user_id, kind, title, body, link, ref_id, read_at, created_at
		 FROM notifications WHERE user_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, uid, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("读取通知失败: %w", err)
	}
	defer rows.Close()

	out := make([]*model.Notification, 0, limit)
	for rows.Next() {
		var n model.Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.Kind, &n.Title, &n.Body,
			&n.Link, &n.RefID, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var unread int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at = 0`, uid).Scan(&unread); err != nil {
		return nil, 0, err
	}
	return out, unread, nil
}

// CountUnreadNotifications 只返回未读数（导航红点用，最轻的查询）。
func (s *Store) CountUnreadNotifications(ctx context.Context, uid string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at = 0`, uid).Scan(&n)
	return n, err
}

// MarkNotificationRead 把一条通知标记为已读（只对本人生效）。
func (s *Store) MarkNotificationRead(ctx context.Context, uid, nid string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at = 0`,
		time.Now().Unix(), nid, uid)
	if err != nil {
		return fmt.Errorf("标记通知已读失败: %w", err)
	}
	return nil
}

// MarkAllNotificationsRead 把某用户所有未读通知标记为已读，返回受影响条数。
func (s *Store) MarkAllNotificationsRead(ctx context.Context, uid string) (int, error) {
	res, err := s.write.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at = 0`,
		time.Now().Unix(), uid)
	if err != nil {
		return 0, fmt.Errorf("全部标记已读失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteNotification 删除一条通知（只对本人生效）。
func (s *Store) DeleteNotification(ctx context.Context, uid, nid string) error {
	_, err := s.write.ExecContext(ctx,
		`DELETE FROM notifications WHERE id = ? AND user_id = ?`, nid, uid)
	if err != nil {
		return fmt.Errorf("删除通知失败: %w", err)
	}
	return nil
}

// PurgeOldNotifications 清理超过保留期的已读通知，返回删除条数。
//
// 只删已读的：未读是「用户还没看到的内容」，过期就丢等于消息被吞。
// 通知表会随签到、订阅收录持续增长，这条清理挂在定时任务里跑。
func (s *Store) PurgeOldNotifications(ctx context.Context, olderThan int64) (int, error) {
	res, err := s.write.ExecContext(ctx,
		`DELETE FROM notifications WHERE read_at > 0 AND created_at < ?`, olderThan)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// 保证 database/sql 被引用（部分构建标签下用不到）。
var _ = sql.ErrNoRows
