package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// MergeResult 是一次手动合并的结果。
type MergeResult struct {
	// KeepID 被保留并转成全局的订阅 id。
	KeepID string
	// KeepOwnerID 这条订阅原来的归属用户（保留原创建者，不做转移）。
	KeepOwnerID string
	// KeepOwnerName 这条订阅原来的名称（通知里要用）。
	KeepOwnerName string
	// RemovedSubs 被删除的订阅（含原归属用户与名称，用于发通知）。
	RemovedSubs []MergedSub
	// AffectedUsers 本次操作涉及的全部用户（含保留者）—— 他们的快照都要重建。
	AffectedUsers []string
}

// MergedSub 是一条因合并而消失的订阅。
type MergedSub struct {
	SubID  string
	UserID string
	Name   string
}

// GetSubByID 按 id 取订阅（不校验归属）。
//
// 与 GetSub 的区别：GetSub 需要 (uid, sid) 两个参数、按归属过滤，
// 管理端的跨租户操作（合并、收录）拿不到用户的 uid，用它查不到东西。
// 这里只给管理路径用，不要暴露给普通用户接口 —— 那会让人能读别人的订阅。
func (s *Store) GetSubByID(ctx context.Context, sid string) (*model.Subscription, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE id = ?`, sid)
	sub, err := scanSub(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取订阅失败: %w", err)
	}
	return sub, nil
}

// MergeSubscriptions 把一组等价订阅合并成**一条全局订阅**（方案 1）。
//
// 语义：保留那一条转成 is_global = 1，其余删除。
//
// 为什么是转全局而不是就地保留成私有：
// 合并会删掉其余几个人的订阅，如果保留项仍是私有，那几个人从此不再受
// 这份名单保护 —— 一条「整理」动作就悄悄降低了他们的防护。转成全局后
// 它对这些人生效，谁都不丢规则。
//
// 副作用要说清楚：保留者的订阅会从「我的订阅」移到管理员的「全局订阅」
// （is_global = 1 之后不再属于个人列表）。调用方必须给**所有**受影响用户
// 发通知，包括保留者 —— 否则他会以为订阅莫名其妙消失了。
//
// 调用方必须先做等价性校验 —— 这个函数只保证「删得干净、保留项转全局」，
// 不判断「该不该删」。判断放在 api 层是因为它需要 CanonicalURL，
// 而 store 不能反向依赖 subsub（会成环）。
//
// 一个事务内完成：删条目、删订阅、把保留项转全局。
// 中途失败会回滚，不会出现「删了条目但订阅还在」的中间态
// （那种状态下定时刷新会把它当成 0 条规则的空订阅）。
func (s *Store) MergeSubscriptions(ctx context.Context, subIDs []string, keepID string) (*MergeResult, error) {
	if len(subIDs) < 2 {
		return nil, fmt.Errorf("合并失败: 至少需要两条订阅")
	}

	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("合并失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res := &MergeResult{KeepID: keepID}
	userSet := map[string]struct{}{}

	for _, sid := range subIDs {
		if sid == keepID {
			// 保留项：读出归属信息（通知要用），并把它转成全局订阅。
			var uid, name string
			if err := tx.QueryRowContext(ctx,
				`SELECT user_id, name FROM subscriptions WHERE id = ?`, sid).Scan(&uid, &name); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("合并失败: 订阅 %s 不存在", sid)
				}
				return nil, fmt.Errorf("合并失败: %w", err)
			}

			// **转全局**。
			//
			// user_id 保留原创建者（不能置空：该列 NOT NULL 且带
			// REFERENCES users(id) 外键），is_global = 1 才是「全局」的判据。
			//
			// promoted_at / promoted_from 记下这是一条被合并收录的订阅：
			// 之后在全局订阅页上能区分「管理员自己建的」与「合并上来的」，
			// 排查「这条全站生效的规则是什么时候冒出来的」时有用。
			if _, err := tx.ExecContext(ctx, `
				UPDATE subscriptions
				SET is_global = 1, promoted_at = ?, promoted_from = ?
				WHERE id = ?`, time.Now().Unix(), uid, sid); err != nil {
				return nil, fmt.Errorf("合并失败: 转为全局订阅: %w", err)
			}

			res.KeepOwnerID = uid
			res.KeepOwnerName = name
			userSet[uid] = struct{}{}
			continue
		}

		// 读出待删订阅的归属信息（删完就查不到了）。
		var uid, name string
		if err := tx.QueryRowContext(ctx,
			`SELECT user_id, name FROM subscriptions WHERE id = ?`, sid).Scan(&uid, &name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// 已被别的路径删掉了，跳过而不是整体失败 ——
				// 合并是幂等的整理动作，不该因为一条已经不在而报错。
				continue
			}
			return nil, fmt.Errorf("合并失败: %w", err)
		}

		// 先删条目再删订阅：反过来的话中间态里会出现「订阅在、条目空」，
		// 虽在事务内不可见，但保持「依赖项先删」的顺序更清楚。
		if _, err := tx.ExecContext(ctx, `DELETE FROM sub_entries WHERE sub_id = ?`, sid); err != nil {
			return nil, fmt.Errorf("合并失败: 清理条目 %s: %w", sid, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM subscriptions WHERE id = ? AND is_global = 0`, sid); err != nil {
			return nil, fmt.Errorf("合并失败: 删除订阅 %s: %w", sid, err)
		}
		res.RemovedSubs = append(res.RemovedSubs, MergedSub{SubID: sid, UserID: uid, Name: name})
		userSet[uid] = struct{}{}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("合并失败: %w", err)
	}

	res.AffectedUsers = make([]string, 0, len(userSet))
	for uid := range userSet {
		res.AffectedUsers = append(res.AffectedUsers, uid)
	}
	return res, nil
}
