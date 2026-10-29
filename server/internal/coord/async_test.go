package coord

import (
	"context"
	"log/slog"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/store"
)

// 锁住 TouchedAsync 的两个关键性质：
//   1. 立刻返回，不等重建（否则批量操作还是 N 次串行等待）
//   2. 不丢更新 —— 返回之后那次变更最终一定会进快照
//
// 第 2 条是安全性底线：规则已经落库了，如果重建没带上它，dnsd 会一直
// 用旧规则，表现为「后台显示规则已保存，但解析行为没变」。
func TestTouchedAsyncReturnsImmediatelyAndDoesNotLoseUpdates(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := New(st, filepath.Join(t.TempDir(), "snap.json"), log)

	// 记录重建次数：把 debounce 调大一点，确保连续调用能落进同一批。
	c.debounce = 200 * time.Millisecond

	var rebuilds int64
	c.onRebuild = func() { atomic.AddInt64(&rebuilds, 1) }

	// 连续 8 次异步标记，应当几乎瞬间返回。
	start := time.Now()
	for i := 0; i < 8; i++ {
		c.TouchedAsync()
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("8 次 TouchedAsync 耗时 %v，没有立即返回", elapsed)
	}

	// 等重建跑完（debounce 200ms + 重建本身）。
	deadline := time.Now().Add(15 * time.Second)
	for atomic.LoadInt64(&rebuilds) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt64(&rebuilds) == 0 {
		t.Fatal("TouchedAsync 之后始终没有触发重建 —— 更新丢了")
	}

	// 合并效果：8 次调用不应该产生 8 次重建。
	time.Sleep(300 * time.Millisecond)
	n := atomic.LoadInt64(&rebuilds)
	if n > 2 {
		t.Fatalf("8 次连续 TouchedAsync 触发了 %d 次重建，合并没生效", n)
	}
	t.Logf("8 次调用 -> %d 次重建，总耗时 %v", n, elapsed)

	// 快照必须真的写出来了，且版本号有效。
	snap, err := c.Snapshot()
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap == nil || snap.Version == 0 {
		t.Fatal("快照没有正确生成")
	}
	t.Logf("快照版本 %d", snap.Version)
}

// 对照：同步的 Changed 仍然要等重建完成，语义不能被这次改动带偏。
func TestChangedStillBlocksUntilRebuilt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "coord2.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := New(st, filepath.Join(t.TempDir(), "snap.json"), log)
	c.debounce = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	v, err := c.Changed(ctx)
	if err != nil {
		t.Fatalf("Changed 失败: %v", err)
	}
	if v == 0 {
		t.Fatal("Changed 返回的版本号为 0")
	}

	// Changed 返回时快照必须已经可见 —— 这是它的契约。
	snap, err := c.Snapshot()
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap == nil || snap.Version != v {
		t.Fatalf("Changed 返回版本 %d，但快照版本是 %v —— 契约被破坏", v, snap)
	}
}

var _ = sync.Mutex{}
