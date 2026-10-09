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

	if err := l.registry.Build(snap); err != nil {
		return err
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
