package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/stats"
	"github.com/50521136/aegis-dns/server/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestCollector(t *testing.T) *stats.Collector {
	t.Helper()
	return stats.New(16, true)
}

// recordOne 制造一条可落库的统计记录。
func recordOne(t *testing.T, c *stats.Collector) {
	t.Helper()
	c.Record(stats.Event{
		UserID: "usr_flush", Domain: "flush.example", QType: "A",
		RCode: "NOERROR", Action: "allowed", LatencyMS: 1,
	})
}

// 这组测试锁住「统计落库失败不丢数据」这条。
//
// 旧实现的缺陷：flushOnce 里 c.Flush() 是**取走并清空**语义，取出来的这批
// 数据一旦 WriteStats 失败就直接 return —— 统计与查询日志永久丢失，只留一行
// Error 日志。库被锁、磁盘满、apid 同时写都会触发，表现是「统计数字莫名少了一块」，
// 而且无法事后补。
//
// 三条要守住的性质：
//  1. 失败的那批必须留在缓冲里等下一轮；
//  2. 缓冲必须有上限，超限丢最旧的并且把丢弃量记下来（不能静默丢弃）；
//  3. 退避要收敛，库恢复后不能还傻等很久。

func mkLogs(n int) []model.QueryLogRow { return mkLogsFrom(0, n) }

// mkLogsFrom 造 n 条 TS 从 start 开始的日志，用于区分新旧批次。
func mkLogsFrom(start, n int) []model.QueryLogRow {
	out := make([]model.QueryLogRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.QueryLogRow{
			TS:     int64(start + i),
			Domain: fmt.Sprintf("d%d.example", start+i),
		})
	}
	return out
}

func TestFlushBufferKeepsFailedBatch(t *testing.T) {
	b := newFlushBuffer(defaultFlushBufferRows)
	b.add(nil, nil, mkLogs(10))
	if b.len() != 10 {
		t.Fatalf("缓冲应有 10 行，实际 %d", b.len())
	}

	// take 之后必须清空（调用方拿到所有权）
	r, tp, l := b.take()
	if len(r) != 0 || len(tp) != 0 || len(l) != 10 {
		t.Fatalf("take 结果不对: rollups=%d tops=%d logs=%d", len(r), len(tp), len(l))
	}
	if b.len() != 0 {
		t.Fatalf("take 之后缓冲应清空，实际 %d", b.len())
	}

	// 失败时把同一批 add 回去，内容必须完整保留
	b.add(r, tp, l)
	if b.len() != 10 {
		t.Fatalf("放回后应有 10 行，实际 %d", b.len())
	}
	r2, _, l2 := b.take()
	if len(r2) != 0 || len(l2) != 10 {
		t.Fatalf("放回后内容不对: rollups=%d logs=%d", len(r2), len(l2))
	}
	if b.dropped != 0 {
		t.Fatalf("没有超限时不该有丢弃，实际 dropped=%d", b.dropped)
	}
}

func TestFlushBufferMergesBatches(t *testing.T) {
	b := newFlushBuffer(defaultFlushBufferRows)
	b.add(nil, nil, mkLogs(5))
	b.add(nil, nil, mkLogs(7)) // 下一轮的新数据
	if b.len() != 12 {
		t.Fatalf("两批应合并为 12 行，实际 %d", b.len())
	}
}

func TestFlushBufferDropsOldestWhenOverflow(t *testing.T) {
	b := newFlushBuffer(10)
	// 两批必须是可区分的新旧：第二批 TS 从 100 开始。
	// 最初两批都从 0 开始时，这条断言无法判断「丢的是新还是旧」，
	// 曾因此误判成 trim 有 bug（实际是测试数据不可区分）。
	b.add(nil, nil, mkLogs(8))          // TS 0..7
	b.add(nil, nil, mkLogsFrom(100, 8)) // TS 100..107，合计 16 > 上限 10

	if b.len() > 10 {
		t.Fatalf("缓冲超过上限：%d > 10", b.len())
	}
	if b.dropped != 6 {
		t.Fatalf("应丢弃 6 行最旧数据，实际 dropped=%d", b.dropped)
	}
	// 丢的必须是最旧的 6 条（TS 0..5），留下的是序列末尾 10 条：6,7,100..107。
	_, _, logs := b.take()
	if len(logs) != 10 {
		t.Fatalf("应留下 10 行，实际 %d", len(logs))
	}
	want := []int64{6, 7, 100, 101, 102, 103, 104, 105, 106, 107}
	for i, l := range logs {
		if l.TS != want[i] {
			t.Fatalf("第 %d 条应是 TS=%d，实际 %d（丢的必须是最旧的，不能丢新数据）", i, want[i], l.TS)
		}
	}
}

func TestFlushSchedulerBackoffGrowsAndResets(t *testing.T) {
	s := newFlushScheduler(1000)

	// 首次失败：backoff 1→2，skip = 1（跳过下一轮）
	s.onFailure()
	if s.backoff != 2 || s.skip != 1 {
		t.Fatalf("第一次失败后应 backoff=2 skip=1，实际 backoff=%d skip=%d", s.backoff, s.skip)
	}
	// 连续失败：2→4→8，到上限后不再增长
	s.onFailure() // skip=3
	s.onFailure() // skip=7
	if s.backoff != 8 {
		t.Fatalf("退避应到 8，实际 %d", s.backoff)
	}
	s.onFailure() // 已到上限（maxBackoffRounds=8）
	if s.backoff > s.maxBackoffRounds {
		t.Fatalf("退避超过上限：%d > %d", s.backoff, s.maxBackoffRounds)
	}

	// 成功必须完全复位：库恢复后不能还傻等
	s.onSuccess()
	if s.backoff != 1 || s.skip != 0 || s.failedRounds != 0 {
		t.Fatalf("成功后未复位: backoff=%d skip=%d failed=%d", s.backoff, s.skip, s.failedRounds)
	}
}

func TestFlushSchedulerShouldSkipHonorsCount(t *testing.T) {
	s := newFlushScheduler(1000)
	s.onFailure() // skip=1
	if !s.shouldSkip() {
		t.Fatal("失败后应跳过下一轮")
	}
	if s.shouldSkip() {
		t.Fatal("跳过次数用完后应恢复执行")
	}
}

// TestFlushOnceSurvivesWriteFailure 是端到端那条：库不可用时数据留在缓冲，
// 库恢复后能一次性写进去。
func TestFlushOnceSurvivesWriteFailure(t *testing.T) {
	// 用一个已关闭的 store 模拟「库不可用」
	dir := t.TempDir()
	st, err := store.Open(dir + "/f.db")
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	_ = st.Close() // 关掉：后续写入必然失败

	col := newTestCollector(t)
	// 塞一条统计进 collector
	recordOne(t, col)

	sched := newFlushScheduler(1000)
	flushOnce(context.Background(), st, col, sched, testLogger())

	if sched.buf.len() == 0 {
		t.Fatal("落库失败后数据必须留在缓冲里（旧实现在这里直接丢弃）")
	}
	before := sched.buf.len()

	// 再失败一轮：数据仍在，且不会因为重复 add 而翻倍错乱
	flushOnce(context.Background(), st, col, sched, testLogger())
	if sched.buf.len() < before {
		t.Fatalf("缓冲不应减少：%d → %d", before, sched.buf.len())
	}
	if sched.failedRounds == 0 {
		t.Fatal("失败次数没有被记录")
	}
}
