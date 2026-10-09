package dns

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/snapshot"
)

// Loader 负责把磁盘上的配置快照加载进 Registry，并持续监听变化。
//
// 双通道变更检测（文档 7.3）：
//
//	通道 A  fsnotify 监听快照所在目录（主，延迟毫秒级）
//	通道 B  每 30s 轮询 version 字段（兜底，覆盖 inotify 失效的场景）
//
// 为什么监听目录而不是文件：apid 用 rename 原子替换快照，
// 直接 watch 文件会在第一次替换后就失效（inode 被换掉了）。
type Loader struct {
	path     string
	registry *Registry
	handler  *Handler
	log      *slog.Logger

	// PollInterval 是兜底轮询间隔。
	PollInterval time.Duration
	// Debounce 是 fsnotify 事件的去抖窗口。
	Debounce time.Duration

	mu        sync.Mutex
	lastMTime time.Time
	lastSize  int64
	lastVer   int64
	// onReload 在每次成功重载后回调（用于刷新 handler 的运行时参数）。
	onReload func(*model.Snapshot)
}

// NewLoader 创建快照加载器。
func NewLoader(path string, reg *Registry, h *Handler, log *slog.Logger) *Loader {
	return &Loader{
		path:         path,
		registry:     reg,
		handler:      h,
		log:          log,
		PollInterval: 30 * time.Second,
		Debounce:     500 * time.Millisecond,
	}
}

// OnReload 注册重载回调。
func (l *Loader) OnReload(fn func(*model.Snapshot)) { l.onReload = fn }

// invalidateCache 在规则变更后清掉受影响的缓存条目。
//
// 缓存键是 {UserID, QName, QType, DO}，不含规则版本 —— 规则改了键不变，
// 旧应答会一直被命中。不主动清的话，新加的拦截规则对该域名在 TTL 内
// 完全不生效（cache_max_ttl 默认 86400，最长一天），用户看到的是
// 「加了规则没用，过一会儿才生效」。设计文档承诺的「配置生效延迟 < 2s」
// 就落在这上面。
//
// 定向失效的依据是快照里的 ChangedUsers：
//   - 为空表示全量快照，影响面未知，整体清空；
//   - 非空表示增量，只清列出的租户。
//
// 无论哪种情况都要顺带清掉 fallback（UserID 为空）的条目：全局设置
// （上游、拦截模式、缓存 TTL）变了会影响未识别请求的应答，而那些条目
// 的键里没有租户 id，只清 ChangedUsers 是覆盖不到的。
func (l *Loader) invalidateCache(snap *model.Snapshot) {
	if l.handler == nil {
		return
	}
	c := l.handler.Cache()
	if c == nil {
		return
	}
	if len(snap.ChangedUsers) == 0 {
		c.Flush()
		l.log.Info("配置全量重载，缓存已整体清空")
		return
	}
	ids := make([]string, 0, len(snap.ChangedUsers)+1)
	ids = append(ids, snap.ChangedUsers...)
	ids = append(ids, "") // fallback
	c.FlushUsers(ids)
	l.log.Info("配置增量重载，已定向失效缓存", "users", len(snap.ChangedUsers))
}

// LoadOnce 读取并应用一次快照。
//
// 任何失败都只记录日志并保留旧配置（文档 P4 / 7.4）：
// 「快照解析失败，保留旧配置」比「清空规则继续跑」安全得多。
func (l *Loader) LoadOnce() error {
	snap, err := snapshot.Read(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.log.Warn("快照文件尚不存在，dnsd 以空配置启动（等待 apid 首次写入）", "path", l.path)
			return nil
		}
		return err
	}

	verBefore := l.registry.Version()

	if err := l.registry.Build(snap); err != nil {
		return err
	}

	// Build 在「版本没前进」时会直接跳过（例如文件被 touch 了一下），
	// 这种情况下什么都没变，不该清缓存 —— 否则空转重载会把缓存打光。
	// 用版本号是否推进来判断「这次真的应用了新配置」。
	if l.registry.Version() != verBefore {
		l.invalidateCache(snap)
	}

	if l.onReload != nil {
		l.onReload(snap)
	}

	st, err := os.Stat(l.path)
	if err == nil {
		l.mu.Lock()
		l.lastMTime = st.ModTime()
		l.lastSize = st.Size()
		l.lastVer = snap.Version
		l.mu.Unlock()
	}

	l.log.Info("配置快照已加载",
		"version", snap.Version,
		"users", len(snap.Users),
		"changed_users", len(snap.ChangedUsers),
		"generated_at", snap.GeneratedAt.Format(time.RFC3339),
	)
	return nil
}

// Run 启动双通道监听，直到 ctx 取消。
func (l *Loader) Run(ctx context.Context) {
	// 先同步加载一次，保证服务对外可用时规则已经就绪。
	if err := l.LoadOnce(); err != nil {
		l.log.Error("首次加载快照失败，将以空配置提供服务", "err", err)
	}

	go l.watchFSNotify(ctx)
	go l.pollLoop(ctx)
}

// watchFSNotify 是通道 A。
func (l *Loader) watchFSNotify(ctx context.Context) {
	dir := filepath.Dir(l.path)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		l.log.Warn("fsnotify 不可用，仅依赖 30s 轮询", "err", err)
		return
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		l.log.Warn("无法监听快照目录，仅依赖轮询", "dir", dir, "err", err)
		return
	}
	l.log.Debug("已开始监听快照目录", "dir", dir)

	base := filepath.Base(l.path)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != base {
				continue
			}
			// rename 替换会以 CREATE 事件出现（新文件出现），
			// 所以 CREATE / WRITE / RENAME 都要处理。
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if !pending {
				pending = true
				timer.Reset(l.Debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			l.log.Warn("快照监听出错", "err", err)
		case <-timer.C:
			pending = false
			// 校验文件确实变了再重载，避免自己的日志写入等噪声触发空转。
			if !l.fileChanged() {
				continue
			}
			if err := l.LoadOnce(); err != nil {
				l.log.Error("快照重载失败，继续使用旧配置", "err", err)
			}
		}
	}
}

// pollLoop 是通道 B。
func (l *Loader) pollLoop(ctx context.Context) {
	t := time.NewTicker(l.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !l.fileChanged() {
				continue
			}
			l.log.Info("轮询发现快照变化，触发重载")
			if err := l.LoadOnce(); err != nil {
				l.log.Error("轮询重载失败，继续使用旧配置", "err", err)
			}
		}
	}
}

// fileChanged 判断快照文件是否与上次加载时不同。
//
// 先用 version 字段比对（便宜且语义准确），version 取不到时退回 mtime+size。
// 只比 mtime 会在某些文件系统（秒级精度）下漏判，只比 version 又依赖
// 快照格式不变，两者结合最稳。
func (l *Loader) fileChanged() bool {
	st, err := os.Stat(l.path)
	if err != nil {
		return false
	}
	l.mu.Lock()
	sameTime := st.ModTime().Equal(l.lastMTime)
	sameSize := st.Size() == l.lastSize
	lastVer := l.lastVer
	l.mu.Unlock()

	if sameTime && sameSize {
		return false
	}

	ver, err := snapshot.ReadVersion(l.path)
	if err == nil && ver != 0 && ver <= lastVer {
		// version 没前进：可能只是文件被 touch 了，不重载。
		return false
	}
	return true
}
