package api

import (
	"context"
	"errors"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这个文件锁住「删除反馈」的契约（需求：管理员反馈已结单的可以删除）。
//
// 删除是不可逆操作，而且删掉的是用户提的问题 —— 边界必须钉死：
// 只有已结单的能删，未结单的一律拒绝。

// seedFeedbackWithStatus 建一条指定状态的反馈（可带时间线消息）。
func seedFeedbackWithStatus(t *testing.T, s *Server, uid, status string, withMsgs bool) *model.Feedback {
	t.Helper()
	ctx := context.Background()
	fb := &model.Feedback{
		UserID: uid, Username: "tester", Domain: "del.example.com",
		Kind: model.FeedbackWrongBlock, Message: "被拦了",
	}
	if err := s.store.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	if status != fb.Status {
		if err := s.store.UpdateFeedback(ctx, fb.ID, status, "已处理"); err != nil {
			t.Fatalf("改状态失败: %v", err)
		}
	}
	if withMsgs {
		for _, m := range []struct{ role, content string }{
			{model.FeedbackRoleUser, "被拦了"},
			{model.FeedbackRoleAdmin, "已处理"},
		} {
			if err := s.store.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
				FeedbackID: fb.ID, Role: m.role, Author: "t", Content: m.content,
			}); err != nil {
				t.Fatalf("写消息失败: %v", err)
			}
		}
	}
	return fb
}

// TestDeleteClosedFeedback 已结单的可以删除，时间线一起清掉。
func TestDeleteClosedFeedback(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	uid := seedUser(t, s, "dc1")
	fb := seedFeedbackWithStatus(t, s, uid, model.FeedbackClosed, true)

	// 删除前确认时间线有内容。
	msgs, _ := s.store.ListFeedbackMessages(ctx, fb.ID)
	if len(msgs) == 0 {
		t.Fatal("测试前置：时间线应有多条消息")
	}

	if err := s.store.DeleteFeedback(ctx, fb.ID); err != nil {
		t.Fatalf("删除已结单反馈失败: %v", err)
	}

	// 反馈本身没了。
	if _, err := s.store.GetFeedback(ctx, fb.ID); err == nil {
		t.Fatal("删除后不该还能读到这条反馈")
	}
	// 时间线也必须清干净：feedback_messages 没有外键级联，
	// 不显式删就会留下永远查不到的孤儿消息占库。
	if left, _ := s.store.ListFeedbackMessages(ctx, fb.ID); len(left) != 0 {
		t.Fatalf("时间线应一并删除，实际还剩 %d 条", len(left))
	}
}

// TestDeleteOpenFeedbackRefused 未结单的一律拒绝删除。
//
// 这是最重要的一条：未结单的反馈是用户还在等答复的事，
// 删掉等于把问题静默丢掉。
func TestDeleteOpenFeedbackRefused(t *testing.T) {
	for _, status := range []string{model.FeedbackOpen, model.FeedbackReplied} {
		t.Run(status, func(t *testing.T) {
			s := newAutoTestServer(t)
			ctx := context.Background()
			uid := seedUser(t, s, "do_"+status)
			fb := seedFeedbackWithStatus(t, s, uid, status, false)

			err := s.store.DeleteFeedback(ctx, fb.ID)
			if err == nil {
				t.Fatalf("%s 状态的反馈不该允许删除", status)
			}
			if !errors.Is(err, store.ErrFeedbackNotClosed) {
				t.Fatalf("应返回 ErrFeedbackNotClosed，实际 %v", err)
			}
			// 反馈还在。
			if _, err := s.store.GetFeedback(ctx, fb.ID); err != nil {
				t.Fatalf("被拒绝后反馈不该消失: %v", err)
			}
		})
	}
}

// TestDeleteFeedbackNotFound 删不存在的反馈返回 ErrNotFound。
func TestDeleteFeedbackNotFound(t *testing.T) {
	s := newAutoTestServer(t)
	err := s.store.DeleteFeedback(context.Background(), "fb_not_exist")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，实际 %v", err)
	}
}

// TestDeleteFeedbackKeepsOthers 删一条不影响其它反馈。
func TestDeleteFeedbackKeepsOthers(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	uid := seedUser(t, s, "dk1")

	keep := seedFeedbackWithStatus(t, s, uid, model.FeedbackOpen, false)
	gone := seedFeedbackWithStatus(t, s, uid, model.FeedbackClosed, false)

	if err := s.store.DeleteFeedback(ctx, gone.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := s.store.GetFeedback(ctx, keep.ID); err != nil {
		t.Fatalf("其它反馈不该被牵连: %v", err)
	}
	// 列表里只剩一条。
	list, total, err := s.store.ListFeedback(ctx, uid, "", 50, 0)
	if err != nil {
		t.Fatalf("列反馈失败: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("应只剩 1 条，实际 total=%d len=%d", total, len(list))
	}
}

// TestCountClosedFeedback 统计已结单数量（管理端「可清理」提示用）。
func TestCountClosedFeedback(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	uid := seedUser(t, s, "cc1")

	seedFeedbackWithStatus(t, s, uid, model.FeedbackOpen, false)
	seedFeedbackWithStatus(t, s, uid, model.FeedbackClosed, false)
	seedFeedbackWithStatus(t, s, uid, model.FeedbackClosed, false)

	n, err := s.store.CountClosedFeedback(ctx)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("已结单应为 2 条，实际 %d", n)
	}
}
