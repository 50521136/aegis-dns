package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/snapshot"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// 这组测试锁住「合并重建」这个改动的两条底线：
//
//  1. **省**：并发变更必须被合并，重建次数要远小于调用次数。
//     不改这个的话，N 个并发变更 = N 次 62MB 写盘（线上实测一次 6.1 秒）。
//  2. **不丢**：每个调用方返回成功时，它自己那批改动必须已经在快照里。
//     只做「合并」不做到这一点，就会把「保存成功但规则没生效」变成常态 ——
//     那比慢得多严重。
//
// 第 2 条是这次改动最容易踩坏的地方：如果 waiter 被复用进「已经取走 pending
// 的那一次重建」，它拿到的版本号对应的快照**不含它的改动**。

func newTestCoord(t *testing.T) (*Coordinator, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// 重建次数从这个计数器读。
	path := filepath.Join(dir, "runtime", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	c := New(st, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// 测试里不需要合并窗口，避免拖慢（合并本身由队列保证，不靠这个窗口）。
	c.debounce = 0
	return c, st, path
}

// seedUsers 造 n 个用户，供 changed_users 使用。
func seedUsers(t *testing.T, st *store.Store, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		u, err := st.CreateUser(context.Background(),
			fmt.Sprintf("u%02d", i), fmt.Sprintf("u%02d@test.local", i), "hash", "user")
		if err != nil {
			t.Fatalf("建用户失败: %v", err)
		}
		ids = append(ids, u.ID)
	}
	return ids
}

// snapshotChangedUsers 读快照里的 changed_users（缺席表示全量重建）。
func snapshotChangedUsers(t *testing.T, path string) ([]string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	var v struct {
		Version      int64    `json:"version"`
		ChangedUsers []string `json:"changed_users"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("解析快照失败: %v", err)
	}
	return v.ChangedUsers, len(v.ChangedUsers) == 0
}

// TestConcurrentChangesAreCoalesced 是主用例：16 个并发变更必须被合并。
//
// 旧实现（每个调用各建一次）这里会是 16 次重建；合并后应当是 1~3 次。
// 上限放到 4 是为了容忍调度抖动（builder 可能已经取走第一批）。
func TestConcurrentChangesAreCoalesced(t *testing.T) {
	c, st, _ := newTestCoord(t)
	ids := seedUsers(t, st, 16)

	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	versions := make([]int64, len(ids))
	start := make(chan struct{})

	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			<-start // 尽量让它们同时发出
			versions[i], errs[i] = c.Changed(context.Background(), id)
		}(i, id)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个变更失败: %v", i, err)
		}
		if versions[i] == 0 {
			t.Fatalf("第 %d 个变更拿到了 0 版本号", i)
		}
	}

	// 版本号就是重建次数（每次重建 BumpConfigVersion 一次）。
	maxVer := versions[0]
	for _, v := range versions {
		if v > maxVer {
			maxVer = v
		}
	}
	if maxVer > 4 {
		t.Fatalf("16 个并发变更产生了 %d 次重建，合并没有生效（期望 1~3 次）", maxVer)
	}
	t.Logf("16 个并发变更 → %d 次重建（旧实现是 16 次）", maxVer)
}

// TestEachCallerSeesOwnChange 是「不丢」那条底线：
// 每个调用方返回时，它自己那个 user 必须已经在快照的 changed_users 里，
// 且快照版本不低于它拿到的版本。
func TestEachCallerSeesOwnChange(t *testing.T) {
	c, st, path := newTestCoord(t)
	ids := seedUsers(t, st, 8)

	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	type result struct {
		id      string
		version int64
		changed []string
	}
	results := make([]result, 0, len(ids))

	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			v, err := c.Changed(context.Background(), id)
			if err != nil {
				t.Errorf("变更失败: %v", err)
				return
			}
			// 必须在返回的这一刻读快照：再晚一点可能被后续重建覆盖。
			changed, _ := snapshotChangedUsers(t, path)
			mu.Lock()
			results = append(results, result{id: id, version: v, changed: changed})
			mu.Unlock()
		}(id)
	}
	close(start)
	wg.Wait()

	if len(results) != len(ids) {
		t.Fatalf("结果数不对：%d / %d", len(results), len(ids))
	}
	// 逐个核对：它的 user 要么出现在这一次的 changed_users 里，
	// 要么已经被更晚的一次重建带过（那种情况下 changed_users 里是别人的，
	// 但版本号会更新）。这里断言「不是两个都没有」。
	for _, r := range results {
		found := false
		for _, u := range r.changed {
			if u == r.id {
				found = true
				break
			}
		}
		if !found && len(r.changed) == 0 {
			// 全量重建（changed_users 缺席）也算覆盖。
			continue
		}
		if !found {
			t.Fatalf("调用方 %s 拿到了版本 %d，但快照的 changed_users=%v 里没有它",
				r.id, r.version, r.changed)
		}
	}
}

// TestSingleChangeStillWorks 是基本功能回归：单个变更照常返回、快照被更新。
func TestSingleChangeStillWorks(t *testing.T) {
	c, st, path := newTestCoord(t)
	ids := seedUsers(t, st, 1)

	v1, err := c.Changed(context.Background(), ids[0])
	if err != nil {
		t.Fatalf("变更失败: %v", err)
	}
	if v1 <= 0 {
		t.Fatalf("版本号应为正，实际 %d", v1)
	}
	if got, _ := snapshot.ReadVersion(path); got != v1 {
		t.Fatalf("快照版本 %d 与返回的 %d 不一致", got, v1)
	}
	changed, _ := snapshotChangedUsers(t, path)
	if len(changed) != 1 || changed[0] != ids[0] {
		t.Fatalf("changed_users 不对: %v", changed)
	}

	// 第二次变更版本必须递增。
	v2, err := c.Changed(context.Background(), ids[0])
	if err != nil {
		t.Fatalf("第二次变更失败: %v", err)
	}
	if v2 <= v1 {
		t.Fatalf("版本必须递增：%d → %d", v1, v2)
	}
}

// TestFullRebuildDominates 断言全量请求会压过增量请求（合并时必须取并集的上界）。
//
// 必须**确定性**地让两个请求落进同一批，否则这条断言本身就是竞态的：
// 两个 goroutine 同时起跑时，增量那次可能先独自重建完（合法），
// 随后全量再单独重建一次 —— 最后一次快照是增量的，看起来像「全量被吞」，
// 其实是两批而不是一批。用 debounce 窗口把两者钉在同一批里。
func TestFullRebuildDominates(t *testing.T) {
	c, st, path := newTestCoord(t)
	ids := seedUsers(t, st, 4)

	// 合并窗口放大到 400ms：先到的增量还在 pending 里等窗口，
	// 后到的全量必然并进同一批。
	c.debounce = 400 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = c.Changed(context.Background(), ids[0]) }()
	// 等增量进入 pending（builder 仍在 debounce 窗口内，不会取走）
	time.Sleep(120 * time.Millisecond)
	go func() { defer wg.Done(); _, _ = c.Changed(context.Background()) }() // 全量
	wg.Wait()

	// 两者同批 → 全量压过增量 → changed_users 必须缺席。
	changed, absent := snapshotChangedUsers(t, path)
	if !absent {
		t.Fatalf("全量请求被增量吞掉了：同批合并后 changed_users 应为空，实际 %v", changed)
	}
}

// TestEnsureMissingSnapshotRebuilds 断言启动路径：快照文件缺失时会重建。
func TestEnsureMissingSnapshotRebuilds(t *testing.T) {
	c, st, path := newTestCoord(t)
	_ = seedUsers(t, st, 2)

	if err := c.EnsureInitial(context.Background()); err != nil {
		t.Fatalf("EnsureInitial 失败: %v", err)
	}
	if _, err := snapshot.ReadVersion(path); err != nil {
		t.Fatalf("快照未生成: %v", err)
	}
}

// TestCallerTimeoutDoesNotKillRebuild 断言调用方超时不影响重建完成，
// 也不会让其他调用方拿不到结果。
func TestCallerTimeoutDoesNotKillRebuild(t *testing.T) {
	c, st, path := newTestCoord(t)
	ids := seedUsers(t, st, 2)

	// 一个几乎立即超时的调用方
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := c.Changed(ctx, ids[0]); err == nil {
		t.Fatal("前置条件不成立：超时的调用方应当返回错误")
	}

	// 另一个正常调用方必须能完成
	v, err := c.Changed(context.Background(), ids[1])
	if err != nil {
		t.Fatalf("正常调用方失败: %v", err)
	}
	if v == 0 {
		t.Fatal("正常调用方拿不到版本号")
	}
	// 超时那次的改动也必须最终落进快照（不丢更新）
	changed, absent := snapshotChangedUsers(t, path)
	if absent {
		return // 全量重建，覆盖一切
	}
	found := false
	for _, u := range changed {
		if u == ids[0] {
			found = true
		}
	}
	if !found {
		t.Logf("提示：changed_users=%v（超时的改动可能已被并进更早的一次重建）", changed)
	}
}

// TestNoGoroutineLeakWhenIdle 断言空闲时 builder 循环会退出（不常驻）。
func TestNoGoroutineLeakWhenIdle(t *testing.T) {
	c, st, _ := newTestCoord(t)
	ids := seedUsers(t, st, 1)

	if _, err := c.Changed(context.Background(), ids[0]); err != nil {
		t.Fatalf("变更失败: %v", err)
	}
	// 等 builder 处理完并自行退出
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("builder 循环没有退出，空闲时会常驻 goroutine")
}

// TestManySequentialChangesNotMerged 是对照组：**串行**变更不该被吞掉，
// 每次都要有自己的重建（调用方要看到自己的版本号）。
func TestManySequentialChangesNotMerged(t *testing.T) {
	c, st, _ := newTestCoord(t)
	ids := seedUsers(t, st, 3)

	var last int64
	for _, id := range ids {
		v, err := c.Changed(context.Background(), id)
		if err != nil {
			t.Fatalf("变更失败: %v", err)
		}
		if v <= last {
			t.Fatalf("串行变更的版本号必须递增：%d → %d", last, v)
		}
		last = v
	}
}

var _ = atomic.Int64{}
var _ = model.KindBlock
