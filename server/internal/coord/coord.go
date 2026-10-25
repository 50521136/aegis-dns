// Package coord 协调「配置变更 → 版本递增 → 快照重建」这条写入路径。
//
// 为什么单独成包：apid 的规则/订阅/设置/用户四类接口都会触发快照重建，
// 订阅调度器也会。把这套逻辑收敛到一处，才能保证
//   - 版本号一定递增（dnsd 判断重载的唯一依据）；
//   - 快照写入一定是原子的（snapshot.AtomicWrite）；
//   - 并发变更不会互相覆盖（合并 + 单 builder）。
//
// 关于「合并」：早期实现是每个调用方各自触发一次全量重建，用一把大锁串行化。
// 线上实测（56 用户、3M 订阅条目、62MB 快照）24 小时重建 72 次、中位耗时
// 6.1 秒，而其中 69 次增量重建里 changed_users 的中位数/最大值**都是 1** ——
// 改一个用户的规则要重写整份 62MB，且同一分钟里能连着重建 6 次。
//
// 现在的做法是「合并队列」：一个 builder 循环负责实际重建，重建**进行中**
// 到达的变更全部并进下一次重建，而不是排队各建一次。对调用方的契约完全不变
// —— Changed 仍然阻塞到「自己的改动已写进快照」才返回，只是这份快照
// 顺带把别人的改动也带上了。
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

// rebuildTimeout 是单次重建的独立超时。
//
// 不能用调用方的 ctx：重建由后台 builder 执行，一个调用方超时/断开不该
// 让整次重建半途而废 —— 那会让等在同一批里的**其他**调用方永远拿不到结果，
// 而版本号已经递增、快照却没写完。
const rebuildTimeout = 3 * time.Minute

// Coordinator 串行化并合并快照重建。
type Coordinator struct {
	st   *store.Store
	path string
	log  *slog.Logger

	mu sync.Mutex
	// pending 记录等待合并的变更用户集合；full 表示需要全量重建。
	pending map[string]bool
	// full 表示有请求要求全量重建（例如改了全局设置）。
	full bool
	// waiters 是等着「自己那批变更写进快照」的调用方。
	waiters []*waiter
	// running 表示 builder 循环在跑（避免重复起循环）。
	running bool
	// lastVersion / lastAt 记录最近一次成功重建的信息，供系统状态展示。
	lastVersion int64
	lastAt      time.Time
	lastErr     error
	// notify 在重建完成后触发（测试与调度器用）。
	notify chan struct{}
	// debounce 是合并窗口：把紧接着到来的变更并进同一批。
	//
	// 相对 6 秒的重建耗时，120ms 可以忽略；它的作用是让「一次用户操作产生的
	// 多个写请求」落进同一批，而不是各触发一次。
	debounce time.Duration
}

// waiter 是一个等待重建结果的调用方。
//
// done 带缓冲 1：调用方可能已经超时离开，builder 仍要能无阻塞地投递结果。
type waiter struct {
	done chan rebuildResult
}

type rebuildResult struct {
	version int64
	err     error
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

// Changed 标记某些用户配置发生变化，并在**这些变更写进快照之后**返回版本号。
//
// 空参数表示需要全量重建（全局设置、用户增删等场景）。
//
// 关键契约：返回成功就意味着「dnsd 已经能看到这次改动」—— 调用方靠它回
// 「已生效」。所以这里会一直等到包含本次变更的那次重建完成。
// 期间若发生 ctx 取消/超时，返回 ctx.Err()，但**重建会继续跑完**
// （变更已经在 pending 里，丢弃它等于丢更新）。
func (c *Coordinator) Changed(ctx context.Context, userIDs ...string) (int64, error) {
	w := &waiter{done: make(chan rebuildResult, 1)}

	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]bool{}
	}
	if len(userIDs) == 0 {
		c.full = true
	} else {
		for _, u := range userIDs {
			if u == "" {
				// 空 userID 语义上等于「不知道是谁改了」，只能全量重建。
				c.full = true
				continue
			}
			c.pending[u] = true
		}
	}
	c.waiters = append(c.waiters, w)
	if !c.running {
		c.running = true
		go c.run()
	}
	c.mu.Unlock()

	select {
	case r := <-w.done:
		return r.version, r.err
	case <-ctx.Done():
		// 调用方先走了。它的变更仍在 pending 里，会被下一次重建带上，
		// 所以不存在丢更新；只是它自己拿不到版本号。
		return 0, ctx.Err()
	}
}

// Rebuild 强制全量重建（管理员手动触发）。
func (c *Coordinator) Rebuild(ctx context.Context) (int64, error) {
	return c.Changed(ctx)
}

// run 是唯一的 builder 循环。
//
// 由 Changed 按需启动、pending 清空后自行退出（running 复位），
// 所以空闲时没有常驻 goroutine。
func (c *Coordinator) run() {
	for {
		// 先看有没有活可干；没有就退出循环。
		c.mu.Lock()
		if len(c.pending) == 0 && !c.full {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		// 合并窗口：放掉锁睡一会儿，让「同一操作产生的相邻变更」并进来。
		if c.debounce > 0 {
			time.Sleep(c.debounce)
		}

		// 取走这一批。
		//
		// 取走**之后**到达的变更会进到新的 pending，等下一次重建 ——
		// 这也是每个 waiter 必须等到「自己加入之后开始的那次重建」的原因：
		// 复用正在进行的那次会让它拿到「不含自己改动」的版本号。
		c.mu.Lock()
		if len(c.pending) == 0 && !c.full {
			c.running = false
			c.mu.Unlock()
			return
		}
		changed := make([]string, 0, len(c.pending))
		for u := range c.pending {
			changed = append(changed, u)
		}
		full := c.full
		c.pending = map[string]bool{}
		c.full = false
		batch := c.waiters
		c.waiters = nil
		c.mu.Unlock()

		version, err := c.writeSnapshot(changed, full)

		// 无论成功失败都要唤醒这一批，否则调用方会一直挂着。
		for _, w := range batch {
			w.done <- rebuildResult{version: version, err: err}
		}
	}
}

// writeSnapshot 是实际重建：递增版本 + 原子写快照。
//
// 不持 c.mu —— 串行性由「只有一个 builder 循环」保证。原来把 62MB 写盘
// 放在大锁里，所有 Changed 调用都被它挡住，也没有合并的空间。
func (c *Coordinator) writeSnapshot(changed []string, full bool) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rebuildTimeout)
	defer cancel()

	version, err := c.st.BumpConfigVersion(ctx)
	if err != nil {
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
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
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		return 0, fmt.Errorf("写入配置快照失败: %w", err)
	}

	c.mu.Lock()
	c.lastVersion = version
	c.lastAt = time.Now()
	c.lastErr = nil
	c.mu.Unlock()

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
//
// 走 Changed 而不是内部直接重建：启动时可能有并发的初始化写入，
// 单点入口才不会出现两份快照互相覆盖。
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
	_, err = c.Changed(ctx)
	return err
}

// Snapshot 返回当前快照（供 /admin/system 展示规模）。
func (c *Coordinator) Snapshot() (*model.Snapshot, error) {
	return snapshot.Read(c.path)
}
