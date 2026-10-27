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

const feedbackCols = `id, user_id, username, domain, kind, message, status, admin_reply, created_at, updated_at,
	ai_status, ai_verdict, ai_reason, ai_action, ai_reply, ai_domains, ai_guard_notes, ai_evidence, ai_model, ai_ran_at,
	ai_answered_at, needs_human_at, user_read_at, admin_read_at`

func scanFeedback(sc interface{ Scan(...any) error }) (*model.Feedback, error) {
	var f model.Feedback
	if err := sc.Scan(&f.ID, &f.UserID, &f.Username, &f.Domain, &f.Kind, &f.Message,
		&f.Status, &f.AdminReply, &f.CreatedAt, &f.UpdatedAt,
		&f.AIStatus, &f.AIVerdict, &f.AIReason, &f.AIAction, &f.AIReply,
		&f.AIDomains, &f.AIGuardNotes, &f.AIEvidence, &f.AIModel, &f.AIRanAt,
		&f.AIAnsweredAt, &f.NeedsHumanAt, &f.UserReadAt, &f.AdminReadAt); err != nil {
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
	// 提交者当然看过自己写的内容：把用户端水位顶到创建时刻，
	// 否则自己刚提的反馈会立刻在「反馈」入口亮起红点。
	if f.UserReadAt == 0 {
		f.UserReadAt = now
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO feedback (`+feedbackCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.UserID, f.Username, f.Domain, f.Kind, f.Message, f.Status, f.AdminReply,
		f.CreatedAt, f.UpdatedAt,
		f.AIStatus, f.AIVerdict, f.AIReason, f.AIAction, f.AIReply,
		f.AIDomains, f.AIGuardNotes, f.AIEvidence, f.AIModel, f.AIRanAt,
		f.AIAnsweredAt, f.NeedsHumanAt, f.UserReadAt, f.AdminReadAt)
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

// ── 反馈时间线（feedback_messages）──

// AppendFeedbackMessage 往某条反馈的时间线追加一条消息。
//
// 调用方负责填 FeedbackID / Role / Content；ID 与 CreatedAt 由这里生成，
// 保证同一毫秒内连续两条也有稳定的先后（ID 单调，前端按 (created_at, id) 排）。
func (s *Store) AppendFeedbackMessage(ctx context.Context, m *model.FeedbackMessage) error {
	if m.FeedbackID == "" {
		return fmt.Errorf("追加反馈消息失败: feedback_id 为空")
	}
	if m.Role != model.FeedbackRoleUser && m.Role != model.FeedbackRoleAdmin {
		return fmt.Errorf("追加反馈消息失败: role 只能是 user / admin，实际 %q", m.Role)
	}
	if strings.TrimSpace(m.Content) == "" {
		return fmt.Errorf("追加反馈消息失败: 内容为空")
	}
	if m.ID == "" {
		m.ID = id.New(id.PrefixFeedbackMsg)
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().Unix()
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO feedback_messages (id, feedback_id, role, author, content, created_at)
		 VALUES (?,?,?,?,?,?)`,
		m.ID, m.FeedbackID, m.Role, m.Author, m.Content, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("追加反馈消息失败: %w", err)
	}
	return nil
}

// ListFeedbackMessages 按时间顺序返回某条反馈的完整时间线。
//
// 排序用 (created_at, id)：同一秒内管理员连发两条时，只按 created_at 排
// 顺序不稳定，读两次可能得到不同结果，前端会看到消息「跳来跳去」。
func (s *Store) ListFeedbackMessages(ctx context.Context, feedbackID string) ([]*model.FeedbackMessage, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, feedback_id, role, author, content, created_at
		 FROM feedback_messages WHERE feedback_id = ?
		 ORDER BY created_at, id`, feedbackID)
	if err != nil {
		return nil, fmt.Errorf("读取反馈消息失败: %w", err)
	}
	defer rows.Close()

	var out []*model.FeedbackMessage
	for rows.Next() {
		var m model.FeedbackMessage
		if err := rows.Scan(&m.ID, &m.FeedbackID, &m.Role, &m.Author, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ListFeedbackMessagesBatch 一次取多条反馈的时间线，返回 feedback_id → 消息列表。
//
// 列表页一屏几十条反馈，逐条 ListFeedbackMessages 就是 N+1 次查询；
// 这里用一次 IN 查询取回来在内存里分组。
func (s *Store) ListFeedbackMessagesBatch(ctx context.Context, feedbackIDs []string) (map[string][]*model.FeedbackMessage, error) {
	out := map[string][]*model.FeedbackMessage{}
	if len(feedbackIDs) == 0 {
		return out, nil
	}
	// SQLite 的变量上限是 999，分批处理避免超限（列表页最多 50~100 条，
	// 但详情导出这类场景可能一次要几百条）。
	const chunk = 400
	for start := 0; start < len(feedbackIDs); start += chunk {
		end := start + chunk
		if end > len(feedbackIDs) {
			end = len(feedbackIDs)
		}
		batch := feedbackIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch))
		for _, v := range batch {
			args = append(args, v)
		}
		rows, err := s.read.QueryContext(ctx,
			`SELECT id, feedback_id, role, author, content, created_at
			 FROM feedback_messages WHERE feedback_id IN (`+placeholders+`)
			 ORDER BY feedback_id, created_at, id`, args...)
		if err != nil {
			return nil, fmt.Errorf("批量读取反馈消息失败: %w", err)
		}
		for rows.Next() {
			var m model.FeedbackMessage
			if err := rows.Scan(&m.ID, &m.FeedbackID, &m.Role, &m.Author, &m.Content, &m.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out[m.FeedbackID] = append(out[m.FeedbackID], &m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountFeedbackMessages 返回某条反馈的消息数（列表上显示「N 条对话」用）。
func (s *Store) CountFeedbackMessages(ctx context.Context, feedbackID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM feedback_messages WHERE feedback_id = ?`, feedbackID).Scan(&n)
	return n, err
}

// DeleteFeedback 删除一条**已结单**的反馈及其时间线。
//
// 只允许删 closed 的：未结单的反馈是用户还在等答复的事，删掉等于把问题
// 丢掉；而结单的反馈留在库里只会让列表越翻越长，管理员需要能清理。
// 这个约束写在 SQL 的 WHERE 里而不是只靠调用方判断 —— 调用方漏判一次
// 就是一条真实问题被静默删除。
//
// feedback_messages 没有外键级联（0020 建表时没加 REFERENCES），
// 必须显式删，否则会留下一堆永远查不到的孤儿消息占库。
//
// 事务内完成：两步之间失败会回滚，不会出现「消息删了反馈还在」。
func (s *Store) DeleteFeedback(ctx context.Context, fid string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// 先确认状态，避免删到未结单的。
		var status string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM feedback WHERE id = ?`, fid).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != model.FeedbackClosed {
			return ErrFeedbackNotClosed
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM feedback_messages WHERE feedback_id = ?`, fid); err != nil {
			return fmt.Errorf("删除反馈时间线失败: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM feedback WHERE id = ? AND status = ?`, fid, model.FeedbackClosed)
		if err != nil {
			return fmt.Errorf("删除反馈失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// CountClosedFeedback 返回已结单反馈数（管理端「可清理」提示用）。
func (s *Store) CountClosedFeedback(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM feedback WHERE status = ?`, model.FeedbackClosed).Scan(&n)
	return n, err
}
