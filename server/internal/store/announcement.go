package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// === 公告 ===

// CreateAnnouncement 新建公告。
func (s *Store) CreateAnnouncement(ctx context.Context, a *model.Announcement) (int64, error) {
	res, err := s.write.ExecContext(ctx,
		`INSERT INTO announcements (title, body, level, mode, dismissible, enabled, pinned, starts_at, ends_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.Title, a.Body, a.Level, a.Mode, boolInt(a.Dismissible), boolInt(a.Enabled),
		boolInt(a.Pinned), a.StartsAt, a.EndsAt, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("创建公告失败: %w", err)
	}
	return res.LastInsertId()
}

// UpdateAnnouncement 更新公告。
func (s *Store) UpdateAnnouncement(ctx context.Context, a *model.Announcement) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE announcements SET title=?, body=?, level=?, mode=?, dismissible=?, enabled=?, pinned=?, starts_at=?, ends_at=?
		 WHERE id=?`,
		a.Title, a.Body, a.Level, a.Mode, boolInt(a.Dismissible), boolInt(a.Enabled),
		boolInt(a.Pinned), a.StartsAt, a.EndsAt, a.ID)
	if err != nil {
		return fmt.Errorf("更新公告失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteAnnouncement 删除公告及其已读记录。
func (s *Store) DeleteAnnouncement(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM announcement_reads WHERE ann_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM announcements WHERE id = ?`, id)
		return err
	})
}

// ListAnnouncements 列出全部公告（管理员视图，含已停用与已过期）。
func (s *Store) ListAnnouncements(ctx context.Context) ([]model.Announcement, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, title, body, level, mode, dismissible, enabled, pinned, starts_at, ends_at, created_at
		 FROM announcements ORDER BY pinned DESC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询公告失败: %w", err)
	}
	defer rows.Close()
	var out []model.Announcement
	for rows.Next() {
		a, err := scanAnn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActiveAnnouncements 返回当前对用户可见的公告，并带上该用户的已读/已静音状态。
//
// 时间窗口在 SQL 里判断（starts_at <= now <= ends_at，0 视为不限制），
// 不在应用层过滤：漏判一条就会让已下线的公告一直挂着。
func (s *Store) ActiveAnnouncements(ctx context.Context, uid string) ([]model.AnnouncementView, error) {
	now := time.Now().Unix()
	rows, err := s.read.QueryContext(ctx,
		`SELECT a.id, a.title, a.body, a.level, a.mode, a.dismissible, a.enabled, a.pinned,
		        a.starts_at, a.ends_at, a.created_at,
		        COALESCE(r.read_at, 0), COALESCE(r.dismissed_at, 0)
		 FROM announcements a
		 LEFT JOIN announcement_reads r ON r.ann_id = a.id AND r.user_id = ?
		 WHERE a.enabled = 1
		   AND (a.starts_at = 0 OR a.starts_at <= ?)
		   AND (a.ends_at = 0 OR a.ends_at >= ?)
		 ORDER BY a.pinned DESC, a.created_at DESC`, uid, now, now)
	if err != nil {
		return nil, fmt.Errorf("查询公告失败: %w", err)
	}
	defer rows.Close()
	var out []model.AnnouncementView
	for rows.Next() {
		var v model.AnnouncementView
		var dismissible, enabled, pinned int
		if err := rows.Scan(&v.ID, &v.Title, &v.Body, &v.Level, &v.Mode, &dismissible, &enabled, &pinned,
			&v.StartsAt, &v.EndsAt, &v.CreatedAt, &v.ReadAt, &v.DismissedAt); err != nil {
			return nil, err
		}
		v.Dismissible = dismissible == 1
		v.Enabled = enabled == 1
		v.Pinned = pinned == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkAnnouncementRead 标记已读。
func (s *Store) MarkAnnouncementRead(ctx context.Context, uid string, id int64) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO announcement_reads (user_id, ann_id, read_at, dismissed_at) VALUES (?,?,?,0)
		 ON CONFLICT(user_id, ann_id) DO UPDATE SET read_at = excluded.read_at
		 WHERE announcement_reads.read_at = 0`, uid, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("标记公告已读失败: %w", err)
	}
	return nil
}

// DismissAnnouncement 标记「不再弹出」。
func (s *Store) DismissAnnouncement(ctx context.Context, uid string, id int64) error {
	now := time.Now().Unix()
	// dismissed 隐含已读：用户都点「不再弹出」了，公告栏还留个红点说不过去。
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO announcement_reads (user_id, ann_id, read_at, dismissed_at) VALUES (?,?,?,?)
		 ON CONFLICT(user_id, ann_id) DO UPDATE SET dismissed_at = excluded.dismissed_at,
		   read_at = CASE WHEN announcement_reads.read_at = 0 THEN excluded.read_at ELSE announcement_reads.read_at END`,
		uid, id, now, now)
	if err != nil {
		return fmt.Errorf("设置公告不再弹出失败: %w", err)
	}
	return nil
}

// UnreadAnnouncementCount 返回未读公告数（红点/角标用）。
func (s *Store) UnreadAnnouncementCount(ctx context.Context, uid string) (int, error) {
	now := time.Now().Unix()
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM announcements a
		 LEFT JOIN announcement_reads r ON r.ann_id = a.id AND r.user_id = ?
		 WHERE a.enabled = 1 AND COALESCE(r.read_at, 0) = 0
		   AND (a.starts_at = 0 OR a.starts_at <= ?)
		   AND (a.ends_at = 0 OR a.ends_at >= ?)`, uid, now, now).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("统计未读公告失败: %w", err)
	}
	return n, nil
}

func scanAnn(rows *sql.Rows) (model.Announcement, error) {
	var a model.Announcement
	var dismissible, enabled, pinned int
	err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Level, &a.Mode, &dismissible, &enabled, &pinned,
		&a.StartsAt, &a.EndsAt, &a.CreatedAt)
	a.Dismissible = dismissible == 1
	a.Enabled = enabled == 1
	a.Pinned = pinned == 1
	return a, err
}
