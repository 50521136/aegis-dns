package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/id"
	"github.com/50521136/aegis-dns/server/internal/model"
)

const feedbackCols = `id, user_id, username, domain, kind, message, status, admin_reply, created_at, updated_at`

func scanFeedback(sc interface{ Scan(...any) error }) (*model.Feedback, error) {
	var f model.Feedback
	if err := sc.Scan(&f.ID, &f.UserID, &f.Username, &f.Domain, &f.Kind, &f.Message,
		&f.Status, &f.AdminReply, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

// CreateFeedback 新建一条反馈。
func (s *Store) CreateFeedback(ctx context.Context, f *model.Feedback) error {
	f.ID = id.New(id.PrefixFeedback)
	now := time.Now().Unix()
	f.CreatedAt = now
	f.UpdatedAt = now
	if f.Status == "" {
		f.Status = model.FeedbackOpen
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO feedback (`+feedbackCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.UserID, f.Username, f.Domain, f.Kind, f.Message, f.Status, f.AdminReply,
		f.CreatedAt, f.UpdatedAt)
	if err != nil {
		return fmt.Errorf("提交反馈失败: %w", err)
	}
	return nil
}

// ListFeedback 分页列出反馈。
//
// uid 非空时只列该用户的（普通用户视角），为空则列全部（管理员视角）。
// status 为空表示不筛选。
func (s *Store) ListFeedback(ctx context.Context, uid, status string, limit, offset int) ([]*model.Feedback, int, error) {
	where := []string{}
	args := []any{}
	if uid != "" {
		where = append(where, "user_id = ?")
		args = append(args, uid)
	}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计反馈数失败: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+feedbackCols+` FROM feedback`+clause+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出反馈失败: %w", err)
	}
	defer rows.Close()

	var out []*model.Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// GetFeedback 按 ID 查询一条反馈。
func (s *Store) GetFeedback(ctx context.Context, fid string) (*model.Feedback, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+feedbackCols+` FROM feedback WHERE id = ?`, fid)
	f, err := scanFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询反馈失败: %w", err)
	}
	return f, nil
}

// UpdateFeedback 更新反馈状态与管理员回复。
//
// 只允许改 status 与 admin_reply：正文属于提交者，管理员不应改写。
func (s *Store) UpdateFeedback(ctx context.Context, fid, status, reply string) error {
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().Unix()}
	if status != "" {
		sets = append(sets, "status = ?")
		args = append(args, status)
	}
	if reply != "" {
		sets = append(sets, "admin_reply = ?")
		args = append(args, reply)
	}
	args = append(args, fid)
	res, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("更新反馈失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountFeedback 返回反馈总数，status 非空时只数该状态（管理员概览用）。
func (s *Store) CountFeedback(ctx context.Context, status string) (int, error) {
	var n int
	var err error
	if status == "" {
		err = s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback`).Scan(&n)
	} else {
		err = s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback WHERE status = ?`, status).Scan(&n)
	}
	return n, err
}
