package subsub

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一等价键并发请求时，底层函数只能执行一次。
//
// 这是去重的核心保证：如果并发下跑了两次，那「省掉重复拉取」就是假的，
// 而这种事在单元测试里不写并发就永远发现不了。
func TestFetchCacheCoalescesConcurrent(t *testing.T) {
	c := newFetchCache()
	var calls int32

	const n = 20
	var wg sync.WaitGroup
	results := make([]*FetchResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.do("same-key", func() (*FetchResult, error) {
				atomic.AddInt32(&calls, 1)
				// 留一点时间让其它 goroutine 一起挤进 inflight 分支
				time.Sleep(20 * time.Millisecond)
				return &FetchResult{ETag: "etag-1", Content: []byte("hello")}, nil
			})
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发下底层只该执行 1 次，实际 %d 次", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个调用返回错误: %v", i, errs[i])
		}
		if results[i] == nil || results[i].ETag != "etag-1" {
			t.Fatalf("第 %d 个调用拿到错误结果: %+v", i, results[i])
		}
	}
	// 省掉的次数 = n - 1（1 次真正执行，其余 19 次命中）
	if c.savedFetches != n-1 {
		t.Fatalf("savedFetches 应为 %d，实际 %d", n-1, c.savedFetches)
	}
}

// 不同键不能互相合并 —— 那会把两份不同内容串在一起。
func TestFetchCacheSeparatesKeys(t *testing.T) {
	c := newFetchCache()
	var calls int32
	f := func() (*FetchResult, error) {
		atomic.AddInt32(&calls, 1)
		return &FetchResult{}, nil
	}
	if _, err := c.do("k1", f); err != nil {
		t.Fatal(err)
	}
	if _, err := c.do("k2", f); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("不同键应各执行 1 次（共 2 次），实际 %d", got)
	}
}

// 完成后的重复请求走 done 缓存，不再执行。
func TestFetchCacheReusesCompleted(t *testing.T) {
	c := newFetchCache()
	var calls int32
	f := func() (*FetchResult, error) {
		atomic.AddInt32(&calls, 1)
		return &FetchResult{ETag: "x"}, nil
	}
	if _, err := c.do("k", f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := c.do("k", f); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("应只执行 1 次，实际 %d", got)
	}
}

// 错误也要共享：同组第二个订阅不该为了拿同样的错误再拉一次。
func TestFetchCacheSharesErrors(t *testing.T) {
	c := newFetchCache()
	var calls int32
	f := func() (*FetchResult, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(10 * time.Millisecond)
		return nil, errTestBoom
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.do("bad", f); err == nil {
				t.Error("期望拿到错误")
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("错误也应共享，底层执行 %d 次", got)
	}
}

// 解析缓存的键必须带列表类型：同一份内容按 blocklist / allowlist
// 解析结果不同，混用会串类别。
func TestSessionKeyIncludesListType(t *testing.T) {
	if sessionKey("gh:a/b@main/x.txt", "blocklist") == sessionKey("gh:a/b@main/x.txt", "allowlist") {
		t.Fatal("不同 list_type 的解析缓存键不能相同")
	}
}

// nextRun 必须落在配置的整点上，且始终是「未来」。
func TestSchedulerNextRun(t *testing.T) {
	s := &Scheduler{hour: 3, minute: 30}
	// 当天 01:00 → 应等到当天 03:30
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.Local)
	next := s.nextRun(now)
	want := time.Date(2026, 10, 26, 3, 30, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("01:00 时下一次应为 %v，实际 %v", want, next)
	}
	// 当天 05:00（已过）→ 应等到次日 03:30
	now = time.Date(2026, 10, 26, 5, 0, 0, 0, time.Local)
	next = s.nextRun(now)
	want = time.Date(2026, 10, 27, 3, 30, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("05:00 时下一次应为 %v，实际 %v", want, next)
	}
	// 恰好等于刷新时刻时，必须推到明天（不能立刻触发，
	// 否则 24 小时周期里会连跑两次）
	now = time.Date(2026, 10, 26, 3, 30, 0, 0, time.Local)
	next = s.nextRun(now)
	if !next.After(now) {
		t.Fatalf("恰好整点时应推到明天，实际 %v", next)
	}
}

// 选项的默认值与边界收敛。
func TestSchedulerOptionsDefaults(t *testing.T) {
	s := NewSchedulerWithOptions(nil, SchedulerOptions{}, nil)
	if s.hour != 3 || s.minute != 0 {
		t.Fatalf("默认时刻应为 03:00，实际 %02d:%02d", s.hour, s.minute)
	}
	// 0 点是合法值：显式指定时必须生效，不能被默认值顶掉
	s = NewSchedulerWithOptions(nil, SchedulerOptions{Hour: 0, HourSet: true, Minute: 0, MinuteSet: true}, nil)
	if s.hour != 0 || s.minute != 0 {
		t.Fatalf("显式 00:00 应生效，实际 %02d:%02d", s.hour, s.minute)
	}
	// 显式指定凌晨 4:15
	s = NewSchedulerWithOptions(nil, SchedulerOptions{Hour: 4, HourSet: true, Minute: 15, MinuteSet: true}, nil)
	if s.hour != 4 || s.minute != 15 {
		t.Fatalf("显式 04:15 应生效，实际 %02d:%02d", s.hour, s.minute)
	}
	if s.jitter != time.Hour {
		t.Fatalf("默认摊开窗口应为 1 小时，实际 %v", s.jitter)
	}
	// 非法值要被收敛而不是透传
	s = NewSchedulerWithOptions(nil, SchedulerOptions{Hour: 99, Minute: -5, SpreadMinutes: 99999}, nil)
	if s.hour != 3 {
		t.Fatalf("非法小时应回落到 3，实际 %d", s.hour)
	}
	if s.minute != 0 {
		t.Fatalf("非法分钟应回落到 0，实际 %d", s.minute)
	}
	if s.jitter != 24*time.Hour {
		t.Fatalf("超大窗口应被压到 24h，实际 %v", s.jitter)
	}
}

var errTestBoom = &testErr{"拉取失败"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }
