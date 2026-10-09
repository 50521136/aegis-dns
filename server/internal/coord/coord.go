// Package coord 协调「配置变更 → 版本递增 → 快照重建」这条写入路径。
//
// 为什么单独成包：apid 的规则/订阅/设置/用户四类接口都会触发快照重建，
// 订阅调度器也会。把这套逻辑收敛到一处，才能保证
//   - 版本号一定递增（dnsd 判断重载的唯一依据）；
//   - 快照写入一定是原子的（snapshot.AtomicWrite）；
//   - 并发变更不会互相覆盖（互斥 + 合并）。
package coord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/snapshot"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// Coordinator 串行化快照重建。
type Coordinator struct {
	st   *store.Store
	path string
	log  *slog.Logger

	mu sync.Mutex
	// pending 记录等待合并的变更用户集合；nil 表示需要全量重建。
	pending map[string]bool
	// full 表示有请求要求全量重建（例如改了全局设置）。
	full bool
	// timer 用于把短时间内的多次变更合并成一次写盘。
	timer *time.Timer
	// lastVersion / lastAt 记录最近一次成功重建的信息，供系统状态展示。
	lastVersion int64
	lastAt      time.Time
	lastErr     error
	// notify 在重建完成后触发（测试与调度器用）。
	notify chan struct{}
	// debounce 是合并窗口。
	debounce time.Duration
}

// New 创建协调器。
func New(st *store.Store, snapshotPath string, log *slog.Logger) *Coordinator {
	return &Coordinator{
		st:       st,
		path:     snapshotPath,
		log:      log,
		debounce: 120 * time.Millisecond,
		notify:   make(chan struct{}, 1),
	}
}

// Changed 标记某些用户配置发生变化并触发快照重建。
//
// 空参数表示需要全量重建（全局设置、用户增删等场景）。
// 返回值是重建后的快照版本号。
func (c *Coordinator) Changed(ctx context.Context, userIDs ...string) (int64, error) {
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]bool{}
	}
	if len(userIDs) == 0 {
		c.full = true
	} else {
		for _, u := range userIDs {
			if u == "" {
				c.full = true
				continue
			}
			c.pending[u] = true
		}
	}
	c.mu.Unlock()

	return c.rebuild(ctx)
}

// Rebuild 强制全量重建（管理员手动触发）。
func (c *Coordinator) Rebuild(ctx context.Context) (int64, error) {
	return c.Changed(ctx)
}

// rebuild 执行一次重建。
//
// 用互斥锁串行化整个「递增版本 + 写快照」过程：
// 并发写同一个文件会让后写者覆盖先写者，而版本号已经递增过，
// dnsd 会看到版本跳跃但内容缺了一块 —— 这是最难排查的一类丢更新。
func (c *Coordinator) rebuild(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	changed := make([]string, 0, len(c.pending))
	for u := range c.pending {
		changed = append(changed, u)
	}
	full := c.full
	c.pending = map[string]bool{}
	c.full = false

	version, err := c.st.BumpConfigVersion(ctx)
	if err != nil {
		c.lastErr = err
		return 0, err
	}

	// full 为 true 时传 nil，让快照不带 changed_users（dnsd 会全量重建）。
	var changedUsers []string
	if !full {
		changedUsers = changed
	}

	start := time.Now()
	snap, err := snapshot.Write(ctx, c.st, c.path, version, changedUsers)
	if err != nil {
		c.lastErr = err
		return 0, fmt.Errorf("写入配置快照失败: %w", err)
	}

	c.lastVersion = version
	c.lastAt = time.Now()
	c.lastErr = nil

	c.log.Info("配置快照已重建",
		"version", version,
		"users", len(snap.Users),
		"changed_users", len(changedUsers),
		"full", full,
		"took_ms", time.Since(start).Milliseconds(),
	)

	select {
	case c.notify <- struct{}{}:
	default:
	}
	return version, nil
}

// Stats 返回最近一次重建的信息。
func (c *Coordinator) Stats() (version int64, at time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastVersion, c.lastAt, c.lastErr
}

// Notify 返回重建完成的通知通道。
func (c *Coordinator) Notify() <-chan struct{} { return c.notify }

// CurrentVersion 返回数据库里的当前配置版本。
func (c *Coordinator) CurrentVersion(ctx context.Context) (int64, error) {
	return c.st.ConfigVersion(ctx)
}

// EnsureInitial 在 apid 启动时确保快照存在。
//
// 首次部署时数据库是空的，dnsd 可能比 apid 先启动。这里主动写一份
// 快照，让 dnsd 不用等到第一次配置变更才有配置可读。
func (c *Coordinator) EnsureInitial(ctx context.Context) error {
	version, err := c.st.ConfigVersion(ctx)
	if err != nil {
		return err
	}
	// 已经有版本号且快照文件存在时不动它。
	if version > 0 {
		if _, err := snapshot.ReadVersion(c.path); err == nil {
			return nil
		}
	}
	_, err = c.rebuild(ctx)
	return err
}

// Snapshot 返回当前快照（供 /admin/system 展示规模）。
func (c *Coordinator) Snapshot() (*model.Snapshot, error) {
	return snapshot.Read(c.path)
}
