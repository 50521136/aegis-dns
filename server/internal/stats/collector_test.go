package stats

import "testing"

// 这个文件锁住一条真实的统计口径缺陷：
//
//	「是否命中缓存」与「拦截 / 放行」是两个正交维度。
//
// 早先 Record 把它们写成一个 switch 的并列分支：
//
//	switch {
//	case ev.Action == "blocked": c.blocked.Add(1)
//	case ev.CacheHit:            c.cached.Add(1)   // ← blocked 先匹配，永远走不到
//	default:                     c.allowed.Add(1)
//	}
//
// 后果：被拦截的查询命中缓存时只进 blocked、永远进不了 cached。而拦截类
// 占线上 36% 流量、命中率 77%，这批全被漏算 —— 面板命中率被系统性低估到
// 6.8%，真实值 53%。命中率失真之后，所有围绕缓存的调优判断都跟着错。

// TestCachedCountsBlockedHits 断言「被拦截 + 命中缓存」的查询同时计入两项。
func TestCachedCountsBlockedHits(t *testing.T) {
	c := New(16, false)

	// 一条被拦截的查询，命中缓存。
	c.Record(Event{UserID: "u1", Domain: "ad.example", Action: "blocked", CacheHit: true})
	// 一条正常放行、命中缓存。
	c.Record(Event{UserID: "u1", Domain: "ok.example", Action: "handled", CacheHit: true})
	// 一条未命中缓存的上游解析。
	c.Record(Event{UserID: "u1", Domain: "new.example", Action: "handled", CacheHit: false})

	got := c.Counters()
	if got.Total != 3 {
		t.Fatalf("total 期望 3，实际 %d", got.Total)
	}
	if got.Cached != 2 {
		t.Fatalf("cached 期望 2（含被拦截那条），实际 %d —— 命中率会被系统性低估", got.Cached)
	}
	if got.Blocked != 1 {
		t.Fatalf("blocked 期望 1，实际 %d", got.Blocked)
	}
	if got.Allowed != 2 {
		t.Fatalf("allowed 期望 2（handled 也算放行），实际 %d", got.Allowed)
	}
	if got.Blocked+got.Allowed != got.Total {
		t.Fatalf("blocked(%d)+allowed(%d) 必须等于 total(%d)，否则面板上的比例对不上",
			got.Blocked, got.Allowed, got.Total)
	}
}

// TestRollupCountsBlockedCacheHits 断言落库的聚合行同样不漏算。
//
// 面板读的是 query_rollup，不是进程计数器 —— 只改进程计数器的话，
// 页面上看到的仍然是错的。
func TestRollupCountsBlockedCacheHits(t *testing.T) {
	c := New(16, false)
	c.Record(Event{UserID: "u1", Domain: "ad.example", Action: "blocked", CacheHit: true})
	c.Record(Event{UserID: "u1", Domain: "ad2.example", Action: "blocked", CacheHit: true})
	c.Record(Event{UserID: "u1", Domain: "ok.example", Action: "handled", CacheHit: false})

	rollups, _, _ := c.Flush()
	if len(rollups) != 1 {
		t.Fatalf("同一租户同一桶应只有 1 行，实际 %d", len(rollups))
	}
	r := rollups[0]
	if r.Total != 3 {
		t.Fatalf("rollup total 期望 3，实际 %d", r.Total)
	}
	if r.Cached != 2 {
		t.Fatalf("rollup cached 期望 2（含被拦截的），实际 %d", r.Cached)
	}
	if r.Blocked != 2 {
		t.Fatalf("rollup blocked 期望 2，实际 %d", r.Blocked)
	}
	if r.Allowed != 1 {
		t.Fatalf("rollup allowed 期望 1，实际 %d", r.Allowed)
	}
}
