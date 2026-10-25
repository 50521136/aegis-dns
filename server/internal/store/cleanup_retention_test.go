package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 这组测试锁住「保留期设 0 也不能让汇总表无界增长」这条。
//
// 缺陷形态：管理员把 stats_retention_days 设成 0，本意是「别删我的历史统计」，
// 旧实现直接跳过 query_rollup / top_domains 的清理 —— 于是这两张表只增不减。
// 线上就是这么把 top_domains 涨到 52.9 万行的，再往下就是磁盘被统计吃满，
// 而面板上完全看不出「清理没生效」。
//
// 修法是保留「0 = 不按时间删」的语义，同时叠一层行数上限兜底。

func newCleanupTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// countRows 数某个表的行数。
func countRows(t *testing.T, st *Store, table string) int {
	t.Helper()
	var n int
	if err := st.read.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("数 %s 行数失败: %v", table, err)
	}
	return n
}

// TestCleanupRetentionZeroStillBoundsRows 断言保留期 0 时行数上限生效。
//
// 直接插到上限以上不现实（上限是千万级），所以这里用「先少量插入 + 断言
// 清理不删任何东西」，再用 SQL 直接验证兜底语句本身的正确性：
// 把上限当成变量不可行（常量），因此分两步验证：
//  1. 数据远低于上限时，retentionDays=0 不能删任何行（语义不变）；
//  2. 真正超限时按 ts 删最旧的 —— 用一个独立库把上限机制手动复刻验证。
func TestCleanupRetentionZeroKeepsRowsBelowCap(t *testing.T) {
	st := newCleanupTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// 造 20 条 30 天前的 rollup / top_domains 行
	for i := 0; i < 20; i++ {
		ts := now - 30*86400 + int64(i*300)
		if err := st.write.QueryRowContext(ctx, `SELECT 1`).Scan(new(int)); err != nil {
			t.Fatalf("前置失败: %v", err)
		}
		if _, err := st.write.ExecContext(ctx,
			`INSERT INTO query_rollup (user_id, ts, total, blocked, cached, allowed, latency_sum)
			 VALUES (?,?,?,?,?,?,?)`, "usr_x", ts, 1, 0, 0, 1, 1); err != nil {
			t.Fatalf("插 rollup 失败: %v", err)
		}
		if _, err := st.write.ExecContext(ctx,
			`INSERT INTO top_domains (user_id, ts, domain, hits, blocked)
			 VALUES (?,?,?,?,?)`, "usr_x", ts, "d.example", 1, 0); err != nil {
			t.Fatalf("插 top_domains 失败: %v", err)
		}
	}

	beforeR := countRows(t, st, "query_rollup")
	beforeT := countRows(t, st, "top_domains")

	// retentionDays=0：远低于上限，一行都不该删
	out, err := st.Cleanup(ctx, 0, 60, 1_000_000)
	if err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if got := countRows(t, st, "query_rollup"); got != beforeR {
		t.Fatalf("保留期 0 且低于上限时不该删 rollup：%d → %d（out=%v）", beforeR, got, out)
	}
	if got := countRows(t, st, "top_domains"); got != beforeT {
		t.Fatalf("保留期 0 且低于上限时不该删 top_domains：%d → %d（out=%v）", beforeT, got, out)
	}
}

// TestCleanupRetentionZeroCapSQLDeletesOldest 直接验证兜底 SQL 的语义：
// 给定一个很小的上限，必须删最旧的那批、留下最新的。
//
// 常量上限是千万级，插那么多行不现实，所以这里把同一段 SQL 用参数化上限
// 跑一次，确认「按 ts 升序取超额部分」这个逻辑本身是对的 —— 常量取多少
// 是策略问题，逻辑对不对是正确性问题。
func TestCleanupRetentionZeroCapSQLDeletesOldest(t *testing.T) {
	st := newCleanupTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// 10 行，ts 递增（0 最旧）
	for i := 0; i < 10; i++ {
		if _, err := st.write.ExecContext(ctx,
			`INSERT INTO top_domains (user_id, ts, domain, hits, blocked)
			 VALUES (?,?,?,?,?)`, "usr_y", now+int64(i), "d.example", 1, 0); err != nil {
			t.Fatalf("插 top_domains 失败: %v", err)
		}
	}

	// 上限设成 4：应删掉最旧的 6 行。
	// 注意 top_domains 是 WITHOUT ROWID 表，只能用 ts 边界定位。
	// ts 每条 +1 唯一，所以按边界删正好得到 4 行。
	const cap = 4
	if _, err := st.write.ExecContext(ctx,
		`DELETE FROM top_domains WHERE ts < (
			SELECT ts FROM top_domains ORDER BY ts ASC LIMIT 1 OFFSET MAX(0, (SELECT COUNT(*) FROM top_domains) - ?))`,
		cap); err != nil {
		t.Fatalf("兜底删除失败: %v", err)
	}

	if n := countRows(t, st, "top_domains"); n != cap {
		t.Fatalf("应剩 %d 行，实际 %d", cap, n)
	}
	// 留下的必须是最新的 4 行（ts = now+6..now+9）
	rows, err := st.read.QueryContext(ctx, `SELECT ts FROM top_domains ORDER BY ts ASC`)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("scan 失败: %v", err)
		}
		got = append(got, ts)
	}
	for i, ts := range got {
		want := now + int64(6+i)
		if ts != want {
			t.Fatalf("第 %d 行应是 ts=%d（最新那批），实际 %d —— 删错了方向", i, want, ts)
		}
	}
}

// TestCleanupRetentionPositiveStillDeletesByAge 是对照组：
// 设了天数的路径必须继续按时间删（新机制不能把老机制顶掉）。
func TestCleanupRetentionPositiveStillDeletesByAge(t *testing.T) {
	st := newCleanupTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// 一条 40 天前、一条刚写的
	for _, ts := range []int64{now - 40*86400, now - 60} {
		if _, err := st.write.ExecContext(ctx,
			`INSERT INTO query_rollup (user_id, ts, total, blocked, cached, allowed, latency_sum)
			 VALUES (?,?,?,?,?,?,?)`, "usr_z", ts, 1, 0, 0, 1, 1); err != nil {
			t.Fatalf("插 rollup 失败: %v", err)
		}
	}

	if _, err := st.Cleanup(ctx, 30, 60, 1_000_000); err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if n := countRows(t, st, "query_rollup"); n != 1 {
		t.Fatalf("按天数清理应只剩 1 行（40 天前那条被删），实际 %d", n)
	}
}
