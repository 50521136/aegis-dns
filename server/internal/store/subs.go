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

const subCols = `id, user_id, list_type, name, url, format, enabled, is_global, last_etag, last_modified,
	last_fetched, last_count, last_status, fail_count, created_at, timeout_sec,
	promoted_at, promoted_from`

// subInsertCols 是 INSERT 用的列名。
//
// **不能复用 subCols**：给 SELECT 加一个字段就会让 INSERT 变成
// 「N values for M columns」，后果是建订阅直接失败、apid 起不来。
// 这两个常量必须各自独立维护 —— 它们碰巧顺序一致，但不是同一个东西。
const subInsertCols = `id, user_id, list_type, name, url, format, enabled, is_global, last_etag, last_modified,
	last_fetched, last_count, last_status, fail_count, created_at, timeout_sec`

func scanSub(sc interface{ Scan(...any) error }) (*model.Subscription, error) {
	var sub model.Subscription
	var enabled, global int
	err := sc.Scan(&sub.ID, &sub.UserID, &sub.ListType, &sub.Name, &sub.URL, &sub.Format,
		&enabled, &global, &sub.LastETag, &sub.LastModified, &sub.LastFetched, &sub.LastCount,
		&sub.LastStatus, &sub.FailCount, &sub.CreatedAt, &sub.TimeoutSec,
		&sub.PromotedAt, &sub.PromotedFrom)
	if err != nil {
		return nil, err
	}
	sub.Enabled = enabled != 0
	sub.IsGlobal = global != 0
	return &sub, nil
}

// ListSubs 返回某用户在指定 scope 下的订阅。
//
// global=false 返回个人订阅，global=true 返回全局订阅（管理员的全局层）。
func (s *Store) ListSubs(ctx context.Context, uid string, global bool) ([]*model.Subscription, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE user_id = ? AND is_global = ? ORDER BY created_at DESC`, uid, boolInt(global))
	if err != nil {
		return nil, fmt.Errorf("列出订阅失败: %w", err)
	}
	defer rows.Close()
	var out []*model.Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ListAllGlobalSubs 返回**全部**全局订阅，不按创建者过滤。
//
// 与 ListSubs(uid, true) 的区别：后者按 user_id = uid 过滤，只返回
// 「这个人创建的」全局订阅。全局订阅的 user_id 只表示「谁建的」，
// 生效范围是全站 —— 被自动收录或手动合并上来的订阅，user_id 保留的是
// 原租户（可能是普通用户），用 ListSubs 查会让管理员在后台看不到它们。
//
// 管理端的全局订阅页必须用这个函数：它对全站在生效，
// 看不到就等于无法管理。
func (s *Store) ListAllGlobalSubs(ctx context.Context) ([]*model.Subscription, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE is_global = 1 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("列出全局订阅失败: %w", err)
	}
	defer rows.Close()
	out := make([]*model.Subscription, 0, 16)
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// UpdateSubByID 按 id 更新订阅（不校验归属），供管理端改全局订阅。
//
// 与 DeleteSubByID 同理：全局订阅的 user_id 可能是普通租户，
// UpdateSub 的 `WHERE ... AND user_id = ?` 会让管理员的修改静默失败
// （RowsAffected = 0 → ErrNotFound），而界面上只觉得「点了保存没反应」。
func (s *Store) UpdateSubByID(ctx context.Context, sub *model.Subscription) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET name=?, url=?, list_type=?, format=?, enabled=? WHERE id=? AND is_global=1`,
		sub.Name, sub.URL, sub.ListType, sub.Format, boolInt(sub.Enabled), sub.ID)
	if err != nil {
		return fmt.Errorf("更新订阅失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSubByID 按 id 删除订阅（不校验归属），供管理端删全局订阅。
//
// 全局订阅的 user_id 可能是普通租户（合并/收录保留原主人），
// DeleteSub(uid, sid) 会因归属不符而删不掉 —— 管理员点了删除却报
// 「订阅不存在」，而它确实在那里。
//
// 只给管理端全局路径用：普通用户路径仍然走 DeleteSub 的归属校验。
func (s *Store) DeleteSubByID(ctx context.Context, sid string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sub_entries WHERE sub_id = ?`, sid); err != nil {
			return fmt.Errorf("删除订阅条目失败: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?`, sid)
		if err != nil {
			return fmt.Errorf("删除订阅失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AllEnabledSubs 返回所有启用中的订阅（调度器刷新用）。
func (s *Store) AllEnabledSubs(ctx context.Context) ([]*model.Subscription, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+subCols+` FROM subscriptions WHERE enabled = 1 ORDER BY user_id, created_at`)
	if err != nil {
		return nil, fmt.Errorf("列出启用订阅失败: %w", err)
	}
	defer rows.Close()
	var out []*model.Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// GetSub 按 ID 查询订阅（带归属校验）。
func (s *Store) GetSub(ctx context.Context, uid, sid string) (*model.Subscription, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = ? AND user_id = ?`, sid, uid)
	sub, err := scanSub(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询订阅失败: %w", err)
	}
	return sub, nil
}

// CreateSub 新建订阅。
func (s *Store) CreateSub(ctx context.Context, sub *model.Subscription) error {
	sub.ID = id.New(id.PrefixSub)
	sub.CreatedAt = time.Now().Unix()
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO subscriptions (`+subInsertCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sub.ID, sub.UserID, sub.ListType, sub.Name, sub.URL, sub.Format, boolInt(sub.Enabled), boolInt(sub.IsGlobal),
		sub.LastETag, sub.LastModified, sub.LastFetched, sub.LastCount, sub.LastStatus, sub.FailCount, sub.CreatedAt,
		sub.TimeoutSec)
	if err != nil {
		if isUniqueViolation(err, "subscriptions.user_id") || isUniqueViolation(err, "subscriptions.url") {
			return ErrSubExists
		}
		return fmt.Errorf("创建订阅失败: %w", err)
	}
	return nil
}

// ErrSubExists 表示同一用户已添加过该 URL。
var ErrSubExists = errors.New("该订阅 URL 已存在")

// UpdateSub 更新订阅的可变字段。
func (s *Store) UpdateSub(ctx context.Context, sub *model.Subscription) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET name=?, url=?, list_type=?, format=?, enabled=? WHERE id=? AND user_id=? AND is_global=?`,
		sub.Name, sub.URL, sub.ListType, sub.Format, boolInt(sub.Enabled), sub.ID, sub.UserID, boolInt(sub.IsGlobal))
	if err != nil {
		return fmt.Errorf("更新订阅失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSub 删除订阅（外键 CASCADE 清掉其条目）。
func (s *Store) DeleteSub(ctx context.Context, uid, sid string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ? AND user_id = ?`, sid, uid)
	if err != nil {
		return fmt.Errorf("删除订阅失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSubStatus 更新订阅的抓取元信息。
// SetSubStatus 只更新 last_status，不动 etag/条目数。
//
// 用途：把「同步中」这个瞬时状态落库。只放在内存里没用 —— 前端创建完会重新拉列表，
// 列表读的是数据库，读回来是空状态就会显示成「未同步」，看着像坏了。
func (s *Store) SetSubStatus(ctx context.Context, sid, status string) error {
	// 顺带把 last_fetched 刷成当前时间：pending 是有起点的，
	// 没有起点就没法判断「同步中」到底是刚发起，还是已经卡死了。
	// 进程被重启、后台 goroutine 被掐掉时不会有人来清这个状态，
	// 只能靠读取侧按起点算超时。
	if _, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET last_status=?, last_fetched=? WHERE id=?`,
		status, time.Now().Unix(), sid); err != nil {
		return fmt.Errorf("更新订阅状态失败: %w", err)
	}
	return nil
}

func (s *Store) UpdateSubStatus(ctx context.Context, sid, etag, lastMod, status string, fetchedAt int64, count, failCount int) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE subscriptions SET last_etag=?, last_modified=?, last_fetched=?, last_count=?, last_status=?, fail_count=?
		 WHERE id=?`,
		etag, lastMod, fetchedAt, count, status, failCount, sid)
	if err != nil {
		return fmt.Errorf("更新订阅状态失败: %w", err)
	}
	return nil
}

// SubEntry 是一条订阅条目的最小描述：域名 + 类别（block / allow）。
type SubEntry struct {
	Domain string
	Kind   string
}

// ReplaceSubEntriesMixed 用一批**可混合类别**的条目替换某订阅的全部条目。
//
// 为什么需要它：一份订阅里 block 与 allow 可以同时存在，而且很常见 ——
// AdGuard/AdBlock 语法的列表用 `@@||domain^` 表达白名单例外，
// 用户报障的那份 rules.txt 里就有 3002 条 `@@` 放行条目。
//
// 旧的 ReplaceSubEntries 只接受**一个** kind，于是 blocklist 订阅里的
// `@@` 条目被整批丢弃：白名单例外静默失效，而订阅状态照样显示 ok ——
// 用户完全看不出「我明明写了放行，为什么还是被拦」。
//
// 读侧（AllSubEntries / AllGlobalSubEntries / SubEntriesByUser）本来就是
// 按行读 kind 的，快照也分 block/allow 两个数组，所以只需把写侧放开。
func (s *Store) ReplaceSubEntriesMixed(ctx context.Context, sid string, entries []SubEntry) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS new_entries2 (domain TEXT PRIMARY KEY, kind TEXT NOT NULL) WITHOUT ROWID`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM new_entries2`); err != nil {
			return err
		}
		// 同一个域名出现两次时以**最后一条**为准：列表里「先拉黑后放行」的
		// 写法要靠后面的 allow 覆盖前面的 block（解析层已经做过这层去重，
		// 这里再兜一次，避免上游顺序变化导致放行失效）。
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO new_entries2 (domain, kind) VALUES (?, ?)
			 ON CONFLICT(domain) DO UPDATE SET kind = excluded.kind`)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Domain == "" {
				continue
			}
			k := e.Kind
			if k != "allow" {
				k = "block"
			}
			if _, err := stmt.ExecContext(ctx, e.Domain, k); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()

		// 先删掉这次没出现的（含类别变化后要重写的），再插入。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sub_entries WHERE sub_id = ? AND domain NOT IN (SELECT domain FROM new_entries2)`, sid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sub_entries (sub_id, domain, kind)
			 SELECT ?, domain, kind FROM new_entries2
			 WHERE domain NOT IN (SELECT domain FROM sub_entries WHERE sub_id = ?)`,
			sid, sid); err != nil {
			return err
		}
		// 类别可能变了（同一域名从 block 变 allow），统一刷一遍。
		if _, err := tx.ExecContext(ctx,
			`UPDATE sub_entries SET kind = (SELECT kind FROM new_entries2 n WHERE n.domain = sub_entries.domain)
			 WHERE sub_id = ? AND domain IN (SELECT domain FROM new_entries2)`, sid); err != nil {
			return err
		}
		return nil
	})
}

// ReplaceSubEntries 用新解析出的域名集合替换某订阅的全部条目。
//
// 用临时表做集合差运算，而不是「DELETE 全部再 INSERT」：
//   - 前者只动真正变化的行，10 万条里改 3 条就只写 3 行；
//   - 后者每次刷新都会把整张表重写一遍，WAL 文件瞬间膨胀。
//
// 整个过程在一个事务内，保证 dnsd 侧看到的快照不会出现「半空」状态。
func (s *Store) ReplaceSubEntries(ctx context.Context, sid, kind string, domains []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS new_entries (domain TEXT PRIMARY KEY) WITHOUT ROWID`); err != nil {
			return err
		}
		// 临时表是连接级的，但事务内复用前必须清空。
		if _, err := tx.ExecContext(ctx, `DELETE FROM new_entries`); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO new_entries (domain) VALUES (?) ON CONFLICT(domain) DO NOTHING`)
		if err != nil {
			return err
		}
		for _, d := range domains {
			if _, err := stmt.ExecContext(ctx, d); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sub_entries WHERE sub_id = ? AND domain NOT IN (SELECT domain FROM new_entries)`, sid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sub_entries (sub_id, domain, kind)
			 SELECT ?, domain, ? FROM new_entries
			 WHERE domain NOT IN (SELECT domain FROM sub_entries WHERE sub_id = ?)`,
			sid, kind, sid); err != nil {
			return err
		}
		// kind 可能因用户改了 list_type 而需要就地修正。
		if _, err := tx.ExecContext(ctx, `UPDATE sub_entries SET kind = ? WHERE sub_id = ?`, kind, sid); err != nil {
			return err
		}
		return nil
	})
}

// CountSubEntries 返回某订阅的条目数。
func (s *Store) CountSubEntries(ctx context.Context, sid string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM sub_entries WHERE sub_id = ?`, sid).Scan(&n)
	return n, err
}

// SubEntriesByUser 返回某用户的 block / allow 域名集合（增量快照用）。
func (s *Store) SubEntriesByUser(ctx context.Context, uid string) (block, allow []string, err error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT e.kind, e.domain FROM sub_entries e
		 JOIN subscriptions s ON s.id = e.sub_id
		 WHERE s.user_id = ? AND s.enabled = 1 AND s.is_global = 0`, uid)
	if err != nil {
		return nil, nil, fmt.Errorf("读取订阅条目失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return nil, nil, err
		}
		if kind == "allow" {
			allow = append(allow, domain)
		} else {
			block = append(block, domain)
		}
	}
	return block, allow, rows.Err()
}

// AllSubEntries 返回全部启用订阅的条目，按 user_id 分组（全量快照用）。
func (s *Store) AllSubEntries(ctx context.Context) (map[string][2][]string, error) {
	// 不写 ORDER BY。
	//
	// 结果本来就按 user_id 塞进 map，顺序对下游没有任何意义 —— 但 SQLite
	// 为了满足 ORDER BY user_id 会建一颗临时 B 树把 183 万行全排一遍。
	// 线上实测（执行计划里能直接看到 `USE TEMP B-TREE FOR ORDER BY`）：
	//
	//	ORDER BY user_id   2597ms
	//	去掉 ORDER BY      1542ms   <- 省 41%
	//
	// 这 1 秒直接落在每次快照重建上，而重建期间全站缓存会被清空、请求
	// 全部回源，用户感知就是「改完规则之后一阵子特别慢」。
	rows, err := s.read.QueryContext(ctx,
		`SELECT s.user_id, e.kind, e.domain FROM sub_entries e
		 JOIN subscriptions s ON s.id = e.sub_id
		 WHERE s.enabled = 1 AND s.is_global = 0`)
	if err != nil {
		return nil, fmt.Errorf("读取全部订阅条目失败: %w", err)
	}
	defer rows.Close()

	out := map[string][2][]string{}
	for rows.Next() {
		var uid, kind, domain string
		if err := rows.Scan(&uid, &kind, &domain); err != nil {
			return nil, err
		}
		v := out[uid]
		if kind == "allow" {
			v[1] = append(v[1], domain)
		} else {
			v[0] = append(v[0], domain)
		}
		out[uid] = v
	}
	return out, rows.Err()
}

// AllGlobalSubEntries 返回所有启用中的全局订阅条目，压平成 block / allow 两个数组。
//
// 全局层是全体用户共享的一份，不按用户分组 —— 这正是它能把内存压下来的原因：
// 订阅条目在快照里只出现一次，而不是每个用户各存一份。
//
// 去重是必须的，且已在 SQL 里做（DISTINCT）：
//   线上全局层有 113.9 万条，唯一域名只有 69.3 万 —— **39.2% 是重复**。
//   不同订阅源大量互相收录（同一条广告域名出现在三四个列表里很常见）。
//   不去重的话这 44.6 万条冗余会：
//     1. 让快照 JSON 白白大出三分之一（63MB -> 实际只需 ~39MB），每次
//        全量重建都要多序列化、多写盘、多让 dnsd 解析一遍；
//     2. 让 buildSnapshot 里 6 个 map 各多插 44.6 万次（去重后才发现这些
//        插入是纯浪费）；
//     3. 让 suffixSet 的排序数组变大，二分多走一层。
//   语义上完全等价：suffixSet 本来就会在 finish() 里去重，这里提前做掉
//   只是把浪费从「构建期 + 每次重建」挪到源头一次性解决。
func (s *Store) AllGlobalSubEntries(ctx context.Context) (block, allow []string, err error) {
	// 同理不写 ORDER BY：调用方只是把结果按 kind 分进两个切片，
	// 顺序无关，而 SQLite 排序要建临时 B 树（实测 1502ms -> 1360ms）。
	rows, err := s.read.QueryContext(ctx,
		`SELECT DISTINCT e.kind, e.domain FROM sub_entries e
		 JOIN subscriptions s ON s.id = e.sub_id
		 WHERE s.enabled = 1 AND s.is_global = 1`)
	if err != nil {
		return nil, nil, fmt.Errorf("读取全局订阅条目失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return nil, nil, err
		}
		if kind == "allow" {
			allow = append(allow, domain)
		} else {
			block = append(block, domain)
		}
	}
	return block, allow, rows.Err()
}

// CountSubs 返回某用户在指定 scope 下的订阅数（配额判定用）。
func (s *Store) CountSubs(ctx context.Context, uid string, global bool) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND is_global = ?`, uid, boolInt(global)).Scan(&n)
	return n, err
}

// UserIDsWithSubs 返回所有拥有启用订阅的用户 ID（增量快照需要知道哪些用户受影响）。
func (s *Store) UserIDsWithSubs(ctx context.Context, sid string) (string, error) {
	var uid string
	err := s.read.QueryRowContext(ctx, `SELECT user_id FROM subscriptions WHERE id = ?`, sid).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return uid, err
}

// CountSubEntriesAll 返回全库订阅条目总数（容量监控用）。
func (s *Store) CountSubEntriesAll(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM sub_entries`).Scan(&n)
	return n, err
}

// GlobalLayerStats 是全局层的规模，用于给用户看「管理员已经替你挡了多少」。
//
// 为什么要给用户看这个：管理员开了全局规则/订阅之后，用户在自己的规则页
// 看到的是一片空白，很自然会以为「没有任何规则在生效」，于是自己再挂一份
// 大订阅 —— 既占他自己的配额，也占服务器内存（订阅条目是按用户压平进快照的）。
// 把真实条数摆出来，用户才知道不需要重复添加。
type GlobalLayerStats struct {
	// RuleCount 是启用的全局规则条数（不含订阅）。
	RuleCount int `json:"rule_count"`
	// SubCount 是启用的全局订阅份数。
	SubCount int `json:"sub_count"`
	// SubEntryCount 是这些全局订阅展开后的域名条目总数。
	SubEntryCount int `json:"sub_entry_count"`
}

// CountEnabledGlobalRules 数启用的全局规则。
func (s *Store) CountEnabledGlobalRules(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rules WHERE enabled = 1 AND is_global = 1`).Scan(&n)
	return n, err
}

// GlobalSubStats 数启用的全局订阅份数与条目总数。
//
// 一次查询取两个数：份数用 COUNT(DISTINCT s.id)，条目数用 COUNT(e.domain)。
// 分两次查会在两次之间插入新订阅时给出自相矛盾的数字（份数 0、条目几万）。
func (s *Store) GlobalSubStats(ctx context.Context) (subs int, entries int, err error) {
	err = s.read.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT s.id), COUNT(e.domain)
		   FROM subscriptions s
		   LEFT JOIN sub_entries e ON e.sub_id = s.id
		  WHERE s.enabled = 1 AND s.is_global = 1`).Scan(&subs, &entries)
	return subs, entries, err
}

// AuditRow 是一条审计日志。
type AuditRow struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	Time   string `json:"time"`
	UserID string `json:"user_id"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}

// RecentAudit 返回最近的审计记录。
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, ts, user_id, action, detail, ip FROM audit_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取审计日志失败: %w", err)
	}
	defer rows.Close()
	out := make([]AuditRow, 0, limit)
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.ID, &a.TS, &a.UserID, &a.Action, &a.Detail, &a.IP); err != nil {
			return nil, err
		}
		a.Time = time.Unix(a.TS, 0).UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	return out, rows.Err()
}

// trimDomains 去重并规范化域名列表（保持稳定顺序，便于 diff 与快照可比）。
func trimDomains(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
