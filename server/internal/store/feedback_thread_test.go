package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 反馈时间线：一条反馈一串消息，用户与管理员往返都在里面。
//
// 缺陷形态（本组测试守的就是它）：此前只有单个 admin_reply 字段，
// 用户想追问**只能再提交一条新反馈** —— 列表里于是出现「同一个问题的
// 第 2 条、第 3 条」，谁也看不出它们是一件事。

func newFeedbackTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mkFeedback(t *testing.T, st *Store, uid, username, msg string) *model.Feedback {
	t.Helper()
	f := &model.Feedback{
		UserID: uid, Username: username, Kind: model.FeedbackOther,
		Message: msg, Status: model.FeedbackOpen,
	}
	if err := st.CreateFeedback(context.Background(), f); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	return f
}

// 追加的消息必须按时间顺序读回来，且 role / author 保持原样。
func TestFeedbackMessagesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newFeedbackTestStore(t)
	f := mkFeedback(t, st, "u1", "alice", "广告域名没拦住")

	for _, m := range []*model.FeedbackMessage{
		{FeedbackID: f.ID, Role: model.FeedbackRoleUser, Author: "alice", Content: "补充：是 app 内弹窗", CreatedAt: 100},
		{FeedbackID: f.ID, Role: model.FeedbackRoleAdmin, Author: "管理员", Content: "已加全局放行", CreatedAt: 200},
	} {
		if err := st.AppendFeedbackMessage(ctx, m); err != nil {
			t.Fatalf("追加消息失败: %v", err)
		}
	}

	msgs, err := st.ListFeedbackMessages(ctx, f.ID)
	if err != nil {
		t.Fatalf("读取时间线失败: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("应有 2 条消息，实际 %d", len(msgs))
	}
	if msgs[0].Role != model.FeedbackRoleUser || msgs[0].Content != "补充：是 app 内弹窗" {
		t.Errorf("第一条不对: %+v", msgs[0])
	}
	if msgs[1].Role != model.FeedbackRoleAdmin {
		t.Errorf("第二条应是管理员消息: %+v", msgs[1])
	}
}

// 同一秒内连续两条消息也必须稳定排序。
//
// 只按 created_at 排的话，同一秒的两条顺序不确定，读两次可能不同 ——
// 前端会看到消息「跳来跳去」。
func TestFeedbackMessagesStableOrderWithinSameSecond(t *testing.T) {
	ctx := context.Background()
	st := newFeedbackTestStore(t)
	f := mkFeedback(t, st, "u1", "alice", "x")

	const ts = 500
	for _, c := range []string{"第一条", "第二条", "第三条"} {
		if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
			FeedbackID: f.ID, Role: model.FeedbackRoleUser, Content: c, CreatedAt: ts,
		}); err != nil {
			t.Fatalf("追加失败: %v", err)
		}
	}

	first, _ := st.ListFeedbackMessages(ctx, f.ID)
	second, _ := st.ListFeedbackMessages(ctx, f.ID)
	if len(first) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(first))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("两次读取顺序不一致（第 %d 条）：%s vs %s", i, first[i].ID, second[i].ID)
		}
	}
}

// 非法 role / 空内容必须拒绝，而不是静默写入一条脏数据。
func TestAppendFeedbackMessageRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	st := newFeedbackTestStore(t)
	f := mkFeedback(t, st, "u1", "alice", "x")

	if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		FeedbackID: f.ID, Role: "system", Content: "x",
	}); err == nil {
		t.Error("非法 role 应报错")
	}
	if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		FeedbackID: f.ID, Role: model.FeedbackRoleUser, Content: "   ",
	}); err == nil {
		t.Error("空内容应报错")
	}
	if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		Role: model.FeedbackRoleUser, Content: "x",
	}); err == nil {
		t.Error("缺 feedback_id 应报错")
	}
}

// 批量读取必须把消息挂到正确的反馈上（不能串台）。
func TestListFeedbackMessagesBatchGroupsCorrectly(t *testing.T) {
	ctx := context.Background()
	st := newFeedbackTestStore(t)
	f1 := mkFeedback(t, st, "u1", "alice", "问题一")
	f2 := mkFeedback(t, st, "u1", "alice", "问题二")

	_ = st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{FeedbackID: f1.ID, Role: model.FeedbackRoleUser, Content: "A1"})
	_ = st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{FeedbackID: f2.ID, Role: model.FeedbackRoleUser, Content: "B1"})
	_ = st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{FeedbackID: f1.ID, Role: model.FeedbackRoleAdmin, Content: "A2"})

	got, err := st.ListFeedbackMessagesBatch(ctx, []string{f1.ID, f2.ID})
	if err != nil {
		t.Fatalf("批量读取失败: %v", err)
	}
	if len(got[f1.ID]) != 2 {
		t.Errorf("f1 应有 2 条，实际 %d", len(got[f1.ID]))
	}
	if len(got[f2.ID]) != 1 {
		t.Errorf("f2 应有 1 条，实际 %d", len(got[f2.ID]))
	}
	for _, m := range got[f2.ID] {
		if m.Content != "B1" {
			t.Errorf("f2 混进了别的反馈的消息: %q", m.Content)
		}
	}
}

// 空入参不该发查询，也不该报错。
func TestListFeedbackMessagesBatchEmpty(t *testing.T) {
	st := newFeedbackTestStore(t)
	got, err := st.ListFeedbackMessagesBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("空入参不该报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空入参应返回空 map，实际 %d 项", len(got))
	}
}
