package api

import (
	"context"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
	"github.com/50521136/aegis-dns/server/internal/subsub"
)

// 这个文件锁住「同一份订阅被多人订阅 → 自动收录为全局规则」的契约（需求 2）。
//
// 这个功能会**删掉用户的订阅**，判错的代价比 AI 自动放行还直观：
// 用户打开订阅页发现东西没了。所以每条边界都要钉住。

func init() {
	// 与 apid 启动时一致：收录判定需要「两个链接是否同一份内容」。
	// 不注入时只认完全相同的字符串，GitHub raw / jsDelivr 变体会漏判
	// （TestPromotionGroupsEquivalentURLs 正是钉这一点）。
	store.SetCanonicalURL(subsub.CanonicalURL)
}

// globalSubs 返回全部全局订阅（is_global = 1）。
//
// 不用 ListSubs：那个函数按 user_id 过滤，而全局订阅的 user_id 是
// 创建者 uid，传空永远查不到。
func globalSubs(t *testing.T, s *Server) []*model.Subscription {
	t.Helper()
	all, err := s.store.AllEnabledSubs(context.Background())
	if err != nil {
		t.Fatalf("读订阅失败: %v", err)
	}
	out := make([]*model.Subscription, 0, len(all))
	for _, sub := range all {
		if sub.IsGlobal {
			out = append(out, sub)
		}
	}
	return out
}

// makeSub 建一条私有订阅。
func makeSub(t *testing.T, s *Server, uid, url, listType string) *model.Subscription {
	t.Helper()
	sub := &model.Subscription{
		UserID:   uid,
		ListType: listType,
		Name:     "测试订阅",
		URL:      url,
		Format:   "auto",
		Enabled:  true,
	}
	if err := s.store.CreateSub(context.Background(), sub); err != nil {
		t.Fatalf("建订阅失败: %v", err)
	}
	return sub
}

// TestPromotionTriggersAtThreshold 达到 3 个不同用户 → 收录成全局并删掉私有。
func TestPromotionTriggersAtThreshold(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "p1")
	u2 := seedUser(t, s, "p2")
	u3 := seedUser(t, s, "p3")

	url := "https://raw.githubusercontent.com/x/y/main/rules.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)
	makeSub(t, s, u3, url, model.ListBlock)

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 1 {
		t.Fatalf("应收录 1 组，实际 %d", res.Promoted)
	}

	// 三个人的私有订阅都该没了。
	for _, uid := range []string{u1, u2, u3} {
		subs, err := s.store.ListSubs(ctx, uid, false)
		if err != nil {
			t.Fatalf("读订阅失败: %v", err)
		}
		if len(subs) != 0 {
			t.Fatalf("用户 %s 的私有订阅应被删除，实际还剩 %d 条", uid, len(subs))
		}
	}
	// 全局层应有一条。
	// 注意不能用 ListSubs(ctx, "", true)：那个函数的语义是
	// 「某用户在全局层的订阅」，内部按 user_id = '' 过滤，永远查不到 ——
	// 全局订阅的 user_id 保留的是创建者 uid（外键不允许空）。
	global := globalSubs(t, s)
	if len(global) != 1 {
		t.Fatalf("全局层应有 1 条订阅，实际 %d", len(global))
	}
	if !global[0].IsGlobal {
		t.Fatal("收录后的订阅必须标记 IsGlobal")
	}
	// user_id 保留原创建者：它是 NOT NULL + 外键，不能清空；
	// 「全局」的判据是 is_global，不是 user_id 为空。
	if global[0].UserID != u1 {
		t.Fatalf("收录后的全局订阅应保留模板订阅的创建者 %s，实际 %q", u1, global[0].UserID)
	}
}

// TestPromotionBelowThresholdDoesNothing 只有 2 个人 → 不动。
func TestPromotionBelowThresholdDoesNothing(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "q1")
	u2 := seedUser(t, s, "q2")

	url := "https://example.com/two-users.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 0 {
		t.Fatalf("2 个用户不该触发收录，实际收录 %d 组", res.Promoted)
	}

	subs, _ := s.store.ListSubs(ctx, u1, false)
	if len(subs) != 1 {
		t.Fatalf("不该删除任何订阅，用户 1 还剩 %d 条", len(subs))
	}
}

// TestPromotionCountsUsersNotSubscriptions 同一个人建三份不算「3 个人」。
//
// 这是需求里最容易搞错的一条：「相同数量超过 3 个」说的是**人**，
// 不是订阅条数。按条数算的话，一个人自己刷三份就会触发收录 ——
// 等于替一个人把规则推给全站。
func TestPromotionCountsUsersNotSubscriptions(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "r1")

	// 同一个人的三份**等价**链接（GitHub raw / jsDelivr / github.com raw）：
	// 数据库对 (user_id, url) 有唯一约束，直接插三条一模一样的会被拒；
	// 而等价链接正是最容易凑成「看起来三份」的形态，用它来测更贴近现实。
	makeSub(t, s, u1, "https://raw.githubusercontent.com/a/b/main/one.txt", model.ListBlock)
	makeSub(t, s, u1, "https://cdn.jsdelivr.net/gh/a/b@main/one.txt", model.ListBlock)
	makeSub(t, s, u1, "https://github.com/a/b/raw/main/one.txt", model.ListBlock)

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 0 {
		t.Fatalf("同一个人建三份不该触发收录，实际收录 %d 组", res.Promoted)
	}
}

// TestPromotionRefusesMixedListTypes 同一链接被当 block 与 allow 混用 → 不收录。
//
// 类别不一致说明它是「条件规则」而不是公共名单，收成全局会把
// A 的拦截规则套到 B 身上，造成真实拦截错误。
func TestPromotionRefusesMixedListTypes(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "m1")
	u2 := seedUser(t, s, "m2")
	u3 := seedUser(t, s, "m3")

	url := "https://example.com/mixed.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)
	makeSub(t, s, u3, url, model.ListAllow) // 类别不一致

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 0 {
		t.Fatalf("类别混杂不该收录，实际收录 %d 组", res.Promoted)
	}
	// 一份都不能删。
	subs, _ := s.store.ListSubs(ctx, u1, false)
	if len(subs) != 1 {
		t.Fatal("类别混杂时不该删除任何订阅")
	}
}

// TestPromotionNotifiesAffectedUsers 收录后每个受影响用户都收到通知。
func TestPromotionNotifiesAffectedUsers(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "n1")
	u2 := seedUser(t, s, "n2")
	u3 := seedUser(t, s, "n3")

	url := "https://example.com/notify.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)
	makeSub(t, s, u3, url, model.ListBlock)

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Notified != 3 {
		t.Fatalf("应通知 3 个人，实际 %d", res.Notified)
	}
	for _, uid := range []string{u1, u2, u3} {
		list, unread, err := s.store.ListNotifications(ctx, uid, 10, 0)
		if err != nil {
			t.Fatalf("读通知失败: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("用户 %s 应收到 1 条通知，实际 %d 条", uid, len(list))
		}
		if unread != 1 {
			t.Fatalf("用户 %s 未读应为 1，实际 %d", uid, unread)
		}
		if list[0].Kind != model.NotifySubPromoted {
			t.Fatalf("通知类别应为 %q，实际 %q", model.NotifySubPromoted, list[0].Kind)
		}
	}
}

// TestPromotionIsIdempotent 重复执行不会造出第二条全局订阅。
func TestPromotionIsIdempotent(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "i1")
	u2 := seedUser(t, s, "i2")
	u3 := seedUser(t, s, "i3")

	url := "https://example.com/idem.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)
	makeSub(t, s, u3, url, model.ListBlock)

	if _, err := s.RunSubscriptionPromotion(ctx); err != nil {
		t.Fatalf("第一次收录失败: %v", err)
	}
	// 第二轮：候选里已经没有私有订阅了，但不该报错，也不该再造一条。
	if _, err := s.RunSubscriptionPromotion(ctx); err != nil {
		t.Fatalf("第二次收录失败: %v", err)
	}
	global := globalSubs(t, s)
	if len(global) != 1 {
		t.Fatalf("全局层应始终只有 1 条订阅，实际 %d", len(global))
	}
	// 通知也不该重复。
	list, _, _ := s.store.ListNotifications(ctx, u1, 10, 0)
	if len(list) != 1 {
		t.Fatalf("重复执行不该产生重复通知，实际 %d 条", len(list))
	}
}

// TestPromotionIgnoresDisabledSubs 停用的订阅不参与判定。
//
// 用户把订阅关掉了，说明他现在不用这份内容；把它算进「有人在订」
// 会让一份实际只有两个人在用的名单变成全局规则。
func TestPromotionIgnoresDisabledSubs(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "d1")
	u2 := seedUser(t, s, "d2")
	u3 := seedUser(t, s, "d3")

	url := "https://example.com/disabled.txt"
	makeSub(t, s, u1, url, model.ListBlock)
	makeSub(t, s, u2, url, model.ListBlock)
	sub3 := makeSub(t, s, u3, url, model.ListBlock)
	// 关掉第三个人的。
	if err := s.store.UpdateSub(ctx, &model.Subscription{
		ID: sub3.ID, UserID: u3, ListType: model.ListBlock, Name: sub3.Name,
		URL: sub3.URL, Format: sub3.Format, Enabled: false,
	}); err != nil {
		t.Fatalf("停用订阅失败: %v", err)
	}

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 0 {
		t.Fatalf("停用的订阅不该计入人数，实际收录 %d 组", res.Promoted)
	}
}

// TestPromotionGroupsEquivalentURLs 同一内容的等价链接（GitHub raw / jsDelivr）
// 应被识别成同一份。
//
// 这是收录功能真正的价值：用户抄的链接形态各不相同，不归一就永远凑不满
// 三个人，同一份名单会被重复拉取、重复压进每个人的快照。
func TestPromotionGroupsEquivalentURLs(t *testing.T) {
	s := newAutoTestServer(t)
	ctx := context.Background()
	u1 := seedUser(t, s, "e1")
	u2 := seedUser(t, s, "e2")
	u3 := seedUser(t, s, "e3")

	makeSub(t, s, u1, "https://raw.githubusercontent.com/owner/repo/main/list.txt", model.ListBlock)
	makeSub(t, s, u2, "https://cdn.jsdelivr.net/gh/owner/repo@main/list.txt", model.ListBlock)
	makeSub(t, s, u3, "https://github.com/owner/repo/raw/main/list.txt", model.ListBlock)

	res, err := s.RunSubscriptionPromotion(ctx)
	if err != nil {
		t.Fatalf("收录失败: %v", err)
	}
	if res.Promoted != 1 {
		t.Fatalf("三种等价链接应被收成 1 组，实际 %d 组", res.Promoted)
	}
}
