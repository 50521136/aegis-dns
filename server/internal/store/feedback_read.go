package store

import (
	"context"
	"fmt"
	"time"
)

// 这个文件是反馈的「未读水位」与「待处理计数」。
//
// 需求是两端都要红点：
//   - 用户端：管理员（或 AI）回复了我还没看；
//   - 管理员端：用户追问了、或 AI 判定需要人工介入，而我还没处理。
//
// 为什么用时间戳水位而不是布尔位：
// 一条反馈可能往返多轮（用户追问 → 管理员回复 → 用户再追问）。布尔位在
// 第一轮之后就被置位了，第二轮再也亮不起来；而水位只需要和「对方最后
// 一条消息的时间」比大小，轮次多少都成立。

// MarkFeedbackReadByUser 记录用户看到了这条反馈的最新回复。
//
// 水位取「最后一条非用户消息的时间」而不是 time.Now()：
//   - 用 now() 时，如果消息时间在将来（测试构造、或服务器时钟回拨过），
//     水位追不上消息时间，红点永远消不掉；
//   - 取消息时间则天然对齐 —— 「我看过了」的语义就是「我看到这条消息了」，
//     水位等于那条消息的时间即可，比它更晚的消息才是新的未读。
//
// 没有任何对方消息时退回 now()：此时没有「未读」可言，取哪个值都一样。
func (s *Store) MarkFeedbackReadByUser(ctx context.Context, uid, fid string) error {
	var lastPeer int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(created_at), 0) FROM feedback_messages
		 WHERE feedback_id = ? AND role <> 'user'`, fid).Scan(&lastPeer)
	if err != nil {
		return fmt.Errorf("标记反馈已读失败: %w", err)
	}
	watermark := lastPeer
	if watermark == 0 {
		watermark = time.Now().Unix()
	}
	_, err = s.write.ExecContext(ctx,
		`UPDATE feedback SET user_read_at = ? WHERE id = ? AND user_id = ?`,
		watermark, fid, uid)
	if err != nil {
		return fmt.Errorf("标记反馈已读失败: %w", err)
	}
	return nil
}

// MarkFeedbackReadByAdmin 记录管理员看到了这条反馈的最新用户发言。
//
// 水位口径同 MarkFeedbackReadByUser：取最后一条用户消息的时间，
// 而不是 now()。理由见上。
func (s *Store) MarkFeedbackReadByAdmin(ctx context.Context, fid string) error {
	var lastUser int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(created_at), 0) FROM feedback_messages
		 WHERE feedback_id = ? AND role = 'user'`, fid).Scan(&lastUser)
	if err != nil {
		return fmt.Errorf("标记反馈已读失败: %w", err)
	}
	watermark := lastUser
	if watermark == 0 {
		watermark = time.Now().Unix()
	}
	_, err = s.write.ExecContext(ctx,
		`UPDATE feedback SET admin_read_at = ? WHERE id = ?`, watermark, fid)
	if err != nil {
		return fmt.Errorf("标记反馈已读失败: %w", err)
	}
	return nil
}

// CountFeedbackNeedingUser 返回「有回复但我还没看」的反馈数（用户端红点）。
//
// 判定口径：存在一条非用户发言（admin，含 AI 自动回复写入的 admin 消息）
// 且其时间晚于 user_read_at，并且这条反馈还没有被我亲自看过最后一条。
//
// 用 EXISTS 子查询而不是 JOIN，是为了让 'open' 状态之外的历史反馈也能正确
// 判定 —— 已结单的反馈如果后来又被管理员改了口径，用户仍然该看到。
func (s *Store) CountFeedbackNeedingUser(ctx context.Context, uid string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM feedback f
		WHERE f.user_id = ?
		  AND EXISTS (
		    SELECT 1 FROM feedback_messages m
		    WHERE m.feedback_id = f.id
		      AND m.role <> 'user'
		      AND m.created_at > f.user_read_at
		  )`, uid).Scan(&n)
	return n, err
}

// CountFeedbackNeedingAdmin 返回「需要管理员处理」的反馈数（管理端红点）。
//
// 三种情况：
//  1. 从未被管理员看过的未结单反馈（admin_read_at = 0）—— 用户刚提的、
//     或提了从没人打开过的。**这一条不能靠时间线判定**：新建反馈时
//     时间线是空的（正文存在 feedback.message 里，不是 messages 表），
//     只查消息表会让「刚提的反馈」永远不亮红点，等于红点对新反馈失效。
//  2. 用户追问了而管理员还没看（user 消息晚于 admin_read_at）。
//  3. AI 明确判定「需要人工」（needs_human_at > 0）。
//
// 刻意不把「AI 已自动结单」的算进来：那些不需要人动手，算进红点
// 只会让管理员点进去发现没事可做，红点就失去意义了。
func (s *Store) CountFeedbackNeedingAdmin(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM feedback f
		WHERE f.status <> 'closed'
		  AND (
		    f.needs_human_at > 0
		    OR f.admin_read_at = 0
		    OR EXISTS (
		      SELECT 1 FROM feedback_messages m
		      WHERE m.feedback_id = f.id
		        AND m.role = 'user'
		        AND m.created_at > f.admin_read_at
		    )
		  )`).Scan(&n)
	return n, err
}

// ClearNeedsHuman 清除「需要人工」标记（管理员处理完这条反馈后调用）。
func (s *Store) ClearNeedsHuman(ctx context.Context, fid string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET needs_human_at = 0 WHERE id = ?`, fid)
	return err
}

// MarkFeedbackAIAnswered 记录这条反馈已被 AI 自动处理并回复。
func (s *Store) MarkFeedbackAIAnswered(ctx context.Context, fid string, at int64) error {
	if at == 0 {
		at = time.Now().Unix()
	}
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET ai_answered_at = ? WHERE id = ?`, at, fid)
	return err
}

// MarkFeedbackNeedsHuman 标记这条反馈需要人工介入。
func (s *Store) MarkFeedbackNeedsHuman(ctx context.Context, fid string, at int64) error {
	if at == 0 {
		at = time.Now().Unix()
	}
	_, err := s.write.ExecContext(ctx,
		`UPDATE feedback SET needs_human_at = ? WHERE id = ?`, at, fid)
	return err
}
