package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件锁住站内通知与「未读水位」的契约（需求 3、4）。

func newNotifyTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "ntf.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedNotifyUser(t *testing.T, st *Store, name string) string {
	t.Helper()
	u, err := st.CreateUser(context.Background(), name, name+"@test.local", "x", model.RoleUser)
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	return u.ID
}

// TestNotificationCRUDAndUnreadCount 通知的写入 / 列表 / 未读计数 / 标记已读。
func TestNotificationCRUDAndUnreadCount(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "n1")

	for _, title := range []string{"第一条", "第二条", "第三条"} {
		ok, err := st.CreateNotification(ctx, &model.Notification{
			UserID: uid, Kind: model.NotifySystem, Title: title, Body: "内容",
		})
		if err != nil {
			t.Fatalf("写通知失败: %v", err)
		}
		if !ok {
			t.Fatal("写通知应返回 true（未被去重挡下）")
		}
	}

	list, unread, err := st.ListNotifications(ctx, uid, 50, 0)
	if err != nil {
		t.Fatalf("读通知失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 条通知，实际 %d", len(list))
	}
	if unread != 3 {
		t.Fatalf("未读应为 3，实际 %d", unread)
	}
	// 倒序：最新在前。
	if list[0].Title != "第三条" {
		t.Fatalf("应按时间倒序，首条是 %q", list[0].Title)
	}

	// 标记一条已读。
	if err := st.MarkNotificationRead(ctx, uid, list[0].ID); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	n, err := st.CountUnreadNotifications(ctx, uid)
	if err != nil {
		t.Fatalf("读未读数失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("标记一条后未读应为 2，实际 %d", n)
	}

	// 全部已读。
	marked, err := st.MarkAllNotificationsRead(ctx, uid)
	if err != nil {
		t.Fatalf("全部标记失败: %v", err)
	}
	if marked != 2 {
		t.Fatalf("应标记 2 条，实际 %d", marked)
	}
	if n, _ := st.CountUnreadNotifications(ctx, uid); n != 0 {
		t.Fatalf("全部标记后未读应为 0，实际 %d", n)
	}
}

// TestNotificationDedupeKey 同用户同 dedupe_key 只允许一条。
//
// 这个约束是「订阅被收录」「签到奖励」这类触发式通知不刷屏的唯一保障。
func TestNotificationDedupeKey(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "n2")

	for i := 0; i < 3; i++ {
		_, err := st.CreateNotification(ctx, &model.Notification{
			UserID: uid, Kind: model.NotifySubPromoted,
			Title: "订阅被收录", DedupeKey: "promote:sub_x",
		})
		if err != nil {
			t.Fatalf("第 %d 次写通知失败: %v", i+1, err)
		}
	}
	list, _, _ := st.ListNotifications(ctx, uid, 50, 0)
	if len(list) != 1 {
		t.Fatalf("同 dedupe_key 应只有 1 条，实际 %d 条", len(list))
	}
}

// TestNotificationDedupeIsPerUser 同一个 key 对不同用户互不影响。
func TestNotificationDedupeIsPerUser(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	u1 := seedNotifyUser(t, st, "d1")
	u2 := seedNotifyUser(t, st, "d2")

	for _, uid := range []string{u1, u2} {
		ok, err := st.CreateNotification(ctx, &model.Notification{
			UserID: uid, Kind: model.NotifySubPromoted,
			Title: "订阅被收录", DedupeKey: "promote:sub_same",
		})
		if err != nil {
			t.Fatalf("写通知失败: %v", err)
		}
		if !ok {
			t.Fatalf("用户 %s 的第一条通知不该被去重", uid)
		}
	}
}

// TestNotificationIsolation 一个用户看不到别人的通知，也改不了别人的。
func TestNotificationIsolation(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	u1 := seedNotifyUser(t, st, "iso1")
	u2 := seedNotifyUser(t, st, "iso2")

	if _, err := st.CreateNotification(ctx, &model.Notification{
		UserID: u1, Kind: model.NotifySystem, Title: "给 u1 的",
	}); err != nil {
		t.Fatalf("写通知失败: %v", err)
	}

	list2, unread2, _ := st.ListNotifications(ctx, u2, 50, 0)
	if len(list2) != 0 || unread2 != 0 {
		t.Fatalf("u2 不该看到 u1 的通知，实际 %d 条 / 未读 %d", len(list2), unread2)
	}

	// u2 去标记 / 删除 u1 的通知：不该有任何效果。
	list1, _, _ := st.ListNotifications(ctx, u1, 50, 0)
	if err := st.MarkNotificationRead(ctx, u2, list1[0].ID); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	if n, _ := st.CountUnreadNotifications(ctx, u1); n != 1 {
		t.Fatalf("u2 的标记不该影响 u1 的未读，实际未读 %d", n)
	}
	if err := st.DeleteNotification(ctx, u2, list1[0].ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	list1b, _, _ := st.ListNotifications(ctx, u1, 50, 0)
	if len(list1b) != 1 {
		t.Fatal("u2 的删除不该删掉 u1 的通知")
	}
}

// TestPurgeOldNotificationsKeepsUnread 清理只删已读的旧通知。
//
// 未读是「用户还没看到的内容」，过期就丢等于消息被吞。
func TestPurgeOldNotificationsKeepsUnread(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "p1")

	old := int64(1000) // 很久以前
	// 一条已读的旧通知（该被删）+ 一条未读的旧通知（该留着）。
	if _, err := st.CreateNotification(ctx, &model.Notification{
		UserID: uid, Kind: model.NotifySystem, Title: "已读旧通知", ReadAt: old, CreatedAt: old,
	}); err != nil {
		t.Fatalf("写通知失败: %v", err)
	}
	if _, err := st.CreateNotification(ctx, &model.Notification{
		UserID: uid, Kind: model.NotifySystem, Title: "未读旧通知", CreatedAt: old,
	}); err != nil {
		t.Fatalf("写通知失败: %v", err)
	}

	n, err := st.PurgeOldNotifications(ctx, 2000)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应删 1 条已读旧通知，实际删了 %d 条", n)
	}
	list, _, _ := st.ListNotifications(ctx, uid, 50, 0)
	if len(list) != 1 || list[0].Title != "未读旧通知" {
		t.Fatalf("应只剩未读那条，实际 %d 条", len(list))
	}
}

// TestFeedbackUnreadWatermarks 反馈两端未读水位的语义（需求 4 的数据基础）。
func TestFeedbackUnreadWatermarks(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "fb1")

	fb := &model.Feedback{
		UserID: uid, Username: "fb1", Domain: "x.example.com",
		Kind: model.FeedbackWrongBlock, Message: "被拦了",
	}
	if err := st.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}

	// 刚提交：用户自己看过，管理员还没看。
	got, _ := st.GetFeedback(ctx, fb.ID)
	if got.UserReadAt == 0 {
		t.Fatal("提交后 user_read_at 应被顶到创建时刻（否则自己提的会立刻亮红点）")
	}
	if got.AdminReadAt != 0 {
		t.Fatal("新建反馈的 admin_read_at 应为 0（管理员还没看）")
	}

	// 管理员还没回复：用户端不该有红点。
	if n, err := st.CountFeedbackNeedingUser(ctx, uid); err != nil || n != 0 {
		t.Fatalf("无回复时用户端红点应为 0，实际 %d（err=%v）", n, err)
	}
	// 管理员端：用户提了反馈就该亮。
	if n, err := st.CountFeedbackNeedingAdmin(ctx); err != nil || n == 0 {
		t.Fatalf("新反馈应让管理端亮红点，实际 %d（err=%v）", n, err)
	}

	// 管理员回复（以 admin 角色写时间线）。
	if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		FeedbackID: fb.ID, Role: model.FeedbackRoleAdmin, Author: "管理员",
		Content: "已处理", CreatedAt: got.CreatedAt + 10,
	}); err != nil {
		t.Fatalf("写回复失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingUser(ctx, uid); n == 0 {
		t.Fatal("管理员回复后用户端应亮红点")
	}

	// 用户看过 → 用户端红点消失。
	if err := st.MarkFeedbackReadByUser(ctx, uid, fb.ID); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingUser(ctx, uid); n != 0 {
		t.Fatalf("用户读过之后红点应消失，实际 %d", n)
	}
	// 管理员已回复，管理端也不该再亮（除非用户又追问）。
	if err := st.MarkFeedbackReadByAdmin(ctx, fb.ID); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n != 0 {
		t.Fatalf("管理员处理完红点应消失，实际 %d", n)
	}

	// 用户追问（第二条用户消息，时间更晚）→ 管理端重新亮。
	if err := st.AppendFeedbackMessage(ctx, &model.FeedbackMessage{
		FeedbackID: fb.ID, Role: model.FeedbackRoleUser, Author: "fb1",
		Content: "还是不行", CreatedAt: got.CreatedAt + 100,
	}); err != nil {
		t.Fatalf("写追问失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n == 0 {
		t.Fatal("用户追问后管理端应重新亮红点（水位机制的核心用处）")
	}
}

// TestFeedbackNeedsHumanCounts 标记为「需要人工」的反馈计入管理端红点。
func TestFeedbackNeedsHumanCounts(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "nh1")

	fb := &model.Feedback{
		UserID: uid, Username: "nh1", Domain: "y.example.com",
		Kind: model.FeedbackWrongBlock, Message: "说不清",
	}
	if err := st.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	// 管理员看过了，正常情况下红点该灭。
	if err := st.MarkFeedbackReadByAdmin(ctx, fb.ID); err != nil {
		t.Fatalf("标记已读失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n != 0 {
		t.Fatalf("看过之后红点应为 0，实际 %d", n)
	}

	// 但 AI 判定要人工 → 即使看过了也该继续提示。
	if err := st.MarkFeedbackNeedsHuman(ctx, fb.ID, 0); err != nil {
		t.Fatalf("标记需人工失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n == 0 {
		t.Fatal("标记为需人工后管理端应亮红点")
	}

	// 清除标记 → 红点消失。
	if err := st.ClearNeedsHuman(ctx, fb.ID); err != nil {
		t.Fatalf("清除标记失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n != 0 {
		t.Fatalf("清除后红点应为 0，实际 %d", n)
	}
}

// TestFeedbackClosedNotCountedForAdmin 已结单的反馈不再占管理端红点。
func TestFeedbackClosedNotCountedForAdmin(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "cl1")

	fb := &model.Feedback{
		UserID: uid, Username: "cl1", Domain: "z.example.com",
		Kind: model.FeedbackWrongBlock, Message: "被拦",
	}
	if err := st.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n == 0 {
		t.Fatal("新建反馈应计入管理端红点")
	}

	if err := st.UpdateFeedback(ctx, fb.ID, model.FeedbackClosed, "已处理"); err != nil {
		t.Fatalf("结单失败: %v", err)
	}
	if n, _ := st.CountFeedbackNeedingAdmin(ctx); n != 0 {
		t.Fatalf("结单后不该再占管理端红点，实际 %d", n)
	}
}

// TestMarkFeedbackAIAnswered 记录 AI 自动处理时间（前端 AI 标识的数据来源）。
func TestMarkFeedbackAIAnswered(t *testing.T) {
	st := newNotifyTestStore(t)
	ctx := context.Background()
	uid := seedNotifyUser(t, st, "ai1")

	fb := &model.Feedback{
		UserID: uid, Username: "ai1", Domain: "a.example.com",
		Kind: model.FeedbackWrongBlock, Message: "被拦",
	}
	if err := st.CreateFeedback(ctx, fb); err != nil {
		t.Fatalf("建反馈失败: %v", err)
	}
	got, _ := st.GetFeedback(ctx, fb.ID)
	if got.AIAnsweredAt != 0 {
		t.Fatal("新建反馈的 ai_answered_at 应为 0")
	}

	if err := st.MarkFeedbackAIAnswered(ctx, fb.ID, 0); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	got2, _ := st.GetFeedback(ctx, fb.ID)
	if got2.AIAnsweredAt == 0 {
		t.Fatal("标记后 ai_answered_at 必须非零（前端靠它显示 AI 标识）")
	}
}
