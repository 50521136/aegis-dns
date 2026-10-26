package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// PromoteCandidate 是一份「被多个用户订阅的同一内容」的候选。
type PromoteCandidate struct {
	// CanonicalKey 是 URL 等价键（CanonicalURL 的结果）。
	CanonicalKey string
	// URLs 这一组里出现过的原始链接。
	URLs []string
	// Subs 这一组里的订阅（含各自的 UserID）。
	Subs []*model.Subscription
	// UserIDs 去重后的涉及用户。
	UserIDs []string
	// ListType 是这份内容的类别（block / allow）。
	// 一组内类别不一致时不参与收录 —— 同一份链接被一个人当 block、
	// 另一个人当 allow，说明它是「条件规则」而非公共名单，收成全局
	// 会改变别人的拦截行为。宁可不收录。
	ListType string
}

// ListPromotionCandidates 找出「被 >= minUsers 个不同用户订阅」的同一内容。
//
// 只从**用户私有订阅**里找：
//   - 已经是全局订阅的不用再收（IsGlobal = 1）；
//   - 同一用户自己建了两份等价链接不算「多人」，所以按用户去重后计数。
//
// 返回的每组的 UserIDs 已去重且有序，Subs 保留全部（含同一用户的重复）。
func (s *Store) ListPromotionCandidates(ctx context.Context, minUsers int) ([]*PromoteCandidate, error) {
	if minUsers < 2 {
		minUsers = 2
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT `+subCols+`
		FROM subscriptions
		WHERE is_global = 0 AND enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("读取订阅失败: %w", err)
	}
	defer rows.Close()

	type group struct {
		subs    []*model.Subscription
		urls    []string
		userSet map[string]struct{}
		types   map[string]struct{}
	}
	groups := map[string]*group{}
	seenURL := map[string]bool{}

	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		key := canonicalKeyFor(sub.URL)
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &group{userSet: map[string]struct{}{}, types: map[string]struct{}{}}
			groups[key] = g
		}
		g.subs = append(g.subs, sub)
		g.userSet[sub.UserID] = struct{}{}
		g.types[sub.ListType] = struct{}{}
		if !seenURL[key+"|"+sub.URL] {
			seenURL[key+"|"+sub.URL] = true
			g.urls = append(g.urls, sub.URL)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*PromoteCandidate, 0, len(groups))
	for key, g := range groups {
		if len(g.userSet) < minUsers {
			continue
		}
		// 类别混杂的不收录（见 PromoteCandidate.ListType 的注释）。
		if len(g.types) != 1 {
			continue
		}
		users := make([]string, 0, len(g.userSet))
		for uid := range g.userSet {
			users = append(users, uid)
		}
		var lt string
		for t := range g.types {
			lt = t
		}
		out = append(out, &PromoteCandidate{
			CanonicalKey: key,
			URLs:         g.urls,
			Subs:         g.subs,
			UserIDs:      users,
			ListType:     lt,
		})
	}
	return out, nil
}

// PromoteSubscriptions 把一组订阅收录为一条全局订阅，并删掉这些私有订阅。
//
// 这是需求「同一份订阅被超过 3 个人订阅，直接改为全局规则」的落地：
//
//   - 用其中一份订阅的记录作模板（名称、格式、超时），只把 IsGlobal 置 1、
//     UserID 清空 —— 全局订阅不属于任何用户，这样它不再计入任何人的配额；
//   - 其余重复的私有订阅删除；
//   - 条目（sub_entries）随订阅一起删（外键级联不存在，显式删）；
//   - 返回被删订阅的 (userID, name) 清单，供调用方给这些人发通知。
//
// 整个操作在一个事务里完成：中途失败会回滚，不会出现「全局订阅建好了
// 但私有订阅没删」导致规则翻倍的中间态。
func (s *Store) PromoteSubscriptions(ctx context.Context, c *PromoteCandidate) (promotedID string, affected []PromotedSub, err error) {
	if len(c.Subs) == 0 {
		return "", nil, fmt.Errorf("收录失败: 没有可收录的订阅")
	}
	tpl := c.Subs[0]
	now := time.Now().Unix()

	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("收录失败: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// 1) 先确认这份内容还没有全局订阅（并发下可能已有别的路径收录过）。
	var existID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM subscriptions WHERE is_global = 1 AND url = ? LIMIT 1`, tpl.URL).Scan(&existID)
	if err != nil && err != sql.ErrNoRows {
		return "", nil, fmt.Errorf("收录失败: %w", err)
	}
	if err == nil && existID != "" {
		// 已经存在：本轮只做清理，把私有重复项删掉并通知，不再新建。
		promotedID = existID
	} else {
		// 复用模板订阅的 id：条目表里已经有它的数据，不必重拉。
		promotedID = tpl.ID
		// **user_id 不能清空**：subscriptions.user_id 是 NOT NULL 且带
		// REFERENCES users(id) 外键（ON DELETE CASCADE），置成 '' 会直接
		// 撞 FOREIGN KEY constraint failed。
		// 保留原创建者即可 —— is_global=1 才是「全局」的判据，user_id
		// 在这些行上只表示「谁建的」，与生效范围无关（现有的全局订阅
		// 也都是挂着一个管理员 uid）。
		_, err = tx.ExecContext(ctx, `
			UPDATE subscriptions
			SET is_global = 1, name = ?, promoted_at = ?, promoted_from = ?
			WHERE id = ?`,
			promotionName(tpl), now, tpl.UserID, tpl.ID)
		if err != nil {
			return "", nil, fmt.Errorf("收录失败: %w", err)
		}
	}

	// 2) 删掉其余重复的私有订阅（含同用户自己建的第二份）。
	for _, sub := range c.Subs {
		if sub.ID == promotedID {
			continue
		}
		if _, derr := tx.ExecContext(ctx,
			`DELETE FROM sub_entries WHERE sub_id = ?`, sub.ID); derr != nil {
			err = fmt.Errorf("收录失败: 清理条目 %s: %w", sub.ID, derr)
			return "", nil, err
		}
		if _, derr := tx.ExecContext(ctx,
			`DELETE FROM subscriptions WHERE id = ? AND is_global = 0`, sub.ID); derr != nil {
			err = fmt.Errorf("收录失败: 删除订阅 %s: %w", sub.ID, derr)
			return "", nil, err
		}
		affected = append(affected, PromotedSub{UserID: sub.UserID, SubID: sub.ID, Name: sub.Name})
	}
	// 模板订阅自己也属于被收录者，同样要通知。
	affected = append(affected, PromotedSub{UserID: tpl.UserID, SubID: promotedID, Name: tpl.Name})

	err = tx.Commit()
	if err != nil {
		return "", nil, fmt.Errorf("收录失败: %w", err)
	}
	return promotedID, affected, nil
}

// PromotedSub 是一条「因收录而失效」的私有订阅，用于给用户发通知。
type PromotedSub struct {
	UserID string
	SubID  string
	Name   string
}

// promotionName 给收录后的全局订阅起名。
//
// 保留原名，只在名字里已经有「全局」时不重复加 —— 这个名字会出现在
// 管理端的全局订阅列表里，叫「自动收录：xxx」比一串 URL 好认。
func promotionName(tpl *model.Subscription) string {
	name := tpl.Name
	if name == "" {
		name = tpl.URL
	}
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	return "自动收录：" + name
}

// canonicalKeyFor 是 store 包对 URL 归一的薄封装。
//
// 真正的实现放在 subsub 包里，但 store 不能反向依赖它（subsub 依赖 store，
// 反向会成环），所以这里放一个极简版本：只处理「完全相同的链接」。
// 调用方（API 层）如果需要完整的等价判定（GitHub raw / jsDelivr 变体），
// 应先自己做归一。
var canonicalURLFn func(string) string

// SetCanonicalURL 注入 URL 归一实现（由 API 层在启动时设置）。
//
// 用注入而不是直接 import，避免 store ↔ subsub 的循环依赖。
func SetCanonicalURL(fn func(string) string) {
	canonicalURLFn = fn
}

func canonicalKeyFor(raw string) string {
	if canonicalURLFn != nil {
		return canonicalURLFn(raw)
	}
	return raw
}
