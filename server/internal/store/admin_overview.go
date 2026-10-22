package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// OwnedRule 是「规则 + 它属于谁」，供管理员跨租户查看。
//
// 管理员的 /admin/rules 一直是全局层，普通用户自己加的规则在后台完全
// 不可见 —— 排查「某个人为什么被拦了」时没法直接看，只能逐个用户去问。
type OwnedRule struct {
	*model.Rule
	Username string `json:"username"`
	// CreatedAtText 是给前端显示的创建时间。
	// model.Rule.CreatedAt 标了 json:"-"，这里单独补一个可读字段。
	CreatedAtText string `json:"created_at"`
}

// ListAllUserRules 跨租户分页列出「用户自己加的规则」（is_global = 0）。
//
// userID 非空时只看某个用户；q 模糊匹配 pattern / value / 用户名。
// 排序按用户分组再按创建时间，同一租户的规则连在一起，方便扫。
func (s *Store) ListAllUserRules(ctx context.Context, userID, q, kind string, limit, offset int) ([]OwnedRule, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	where := []string{"r.is_global = 0"}
	args := []any{}
	if userID != "" {
		where = append(where, "r.user_id = ?")
		args = append(args, userID)
	}
	if kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, kind)
	}
	if q != "" {
		where = append(where, "(r.pattern LIKE ? OR r.value LIKE ? OR u.username LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	clause := " WHERE " + strings.Join(where, " AND ")
	from := " FROM rules r LEFT JOIN users u ON u.id = r.user_id" + clause

	var total int
	if err := s.read.QueryRowContext(ctx, "SELECT COUNT(*)"+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计用户规则数失败: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.read.QueryContext(ctx,
		`SELECT r.id, r.user_id, COALESCE(u.username,''), r.kind, r.pattern, r.value,
		        r.qtypes, r.enabled, r.is_global, r.created_at`+from+
			` ORDER BY r.user_id, r.created_at DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出用户规则失败: %w", err)
	}
	defer rows.Close()

	out := make([]OwnedRule, 0, limit)
	for rows.Next() {
		var r model.Rule
		var owned OwnedRule
		var qtypes string
		var enabled int
		if err := rows.Scan(&r.ID, &r.UserID, &owned.Username, &r.Kind, &r.Pattern, &r.Value,
			&qtypes, &enabled, &r.IsGlobal, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		r.Enabled = enabled != 0
		r.QTypes = splitCSV(qtypes)
		owned.Rule = &r
		owned.CreatedAtText = time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339)
		out = append(out, owned)
	}
	return out, total, rows.Err()
}

// OwnedSub 是「订阅 + 它属于谁」。
type OwnedSub struct {
	*model.Subscription
	Username string `json:"username"`
	// CreatedAtText 同上。
	CreatedAtText string `json:"created_at"`
	// EntryCount 是该订阅已解析出的条目数，列表里直接展示。
	EntryCount int `json:"entry_count"`
}

// ListAllUserSubs 跨租户分页列出用户自己的订阅。
func (s *Store) ListAllUserSubs(ctx context.Context, userID, q string, limit, offset int) ([]OwnedSub, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	where := []string{"s.is_global = 0"}
	args := []any{}
	if userID != "" {
		where = append(where, "s.user_id = ?")
		args = append(args, userID)
	}
	if q != "" {
		where = append(where, "(s.name LIKE ? OR s.url LIKE ? OR u.username LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	clause := " WHERE " + strings.Join(where, " AND ")
	from := " FROM subscriptions s LEFT JOIN users u ON u.id = s.user_id" + clause

	var total int
	if err := s.read.QueryRowContext(ctx, "SELECT COUNT(*)"+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计用户订阅数失败: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit, offset)
	// 条目数用子查询而不是 JOIN + GROUP BY：订阅数量本来就少，
	// 子查询读起来清楚，也不需要额外的分组逻辑。
	rows, err := s.read.QueryContext(ctx,
		`SELECT s.id, s.user_id, COALESCE(u.username,''), s.name, s.url, s.list_type,
		        s.format, s.enabled, s.is_global, s.last_count, s.last_status, s.fail_count,
		        s.created_at,
		        (SELECT COUNT(*) FROM sub_entries e WHERE e.sub_id = s.id)`+from+
			` ORDER BY s.user_id, s.created_at DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出用户订阅失败: %w", err)
	}
	defer rows.Close()

	out := make([]OwnedSub, 0, limit)
	for rows.Next() {
		var sub model.Subscription
		var owned OwnedSub
		var enabled int
		if err := rows.Scan(&sub.ID, &sub.UserID, &owned.Username, &sub.Name, &sub.URL, &sub.ListType,
			&sub.Format, &enabled, &sub.IsGlobal, &sub.LastCount, &sub.LastStatus, &sub.FailCount,
			&sub.CreatedAt, &owned.EntryCount); err != nil {
			return nil, 0, err
		}
		sub.Enabled = enabled != 0
		owned.Subscription = &sub
		owned.CreatedAtText = time.Unix(sub.CreatedAt, 0).UTC().Format(time.RFC3339)
		out = append(out, owned)
	}
	return out, total, rows.Err()
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
