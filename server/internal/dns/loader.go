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
	// 上一次快照的指纹快照（不是快照本身）。
	//
	// 这里曾经存的是 *model.Snapshot 本体，只为比对指纹 —— 而快照里有
	// 几十万条订阅域名，等于把整份快照常驻内存一份，峰值翻倍。
	// 线上 1.7G 的机器上 dnsd 直接吃到 1.3G，被 OOM 杀了两次。
	// 只留 「全局指纹 + 每用户指纹」 就够判断，几十 KB 的量级。
	prevGlobalFP string
	// lastFlushSeq 是上次处理过的「清除缓存」序号
	lastFlushSeq int64
	prevUserFP   map[string]string
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

	// 管理员手动点了「清除缓存」：序号变了就整体清空，跟规则有没有变无关。
	// 这条要放在指纹判断之前 —— 用户点清除的场景恰恰是「规则没动但缓存有脏数据」。
	if snap.CacheFlushSeq != l.lastFlushSeq {
		before := c.Len()
		c.Flush()
		l.lastFlushSeq = snap.CacheFlushSeq
		l.log.Info("管理员清除缓存，已整体清空", "entries_dropped", before, "seq", snap.CacheFlushSeq)
		return
	}

	// 第一次加载：没有旧指纹可比，直接清空（此时缓存本来就是空的）。
	if l.prevUserFP == nil {
		c.Flush()
		return
	}

	// 全局层或影响应答的默认值变了：所有租户的缓存都不可信。
	//
	// 注意这里**不看** ChangedUsers：全量重建不等于规则变了。
	// 一次订阅刷新、一次配额调整都会触发全量重建，而绝大多数用户的规则
	// 一个字节都没动。按 ChangedUsers 判断的结果是缓存被反复整体清空
	// （线上实测 24 小时 33 次），命中率从理论 86% 掉到 12%。
	if l.prevGlobalFP != snap.GlobalFingerprint {
		before := c.Len()
		c.Flush()
		l.log.Info("全局层或默认值变更，缓存已整体清空", "entries_dropped", before)
		return
	}

	// 逐用户比对指纹，只清真的变了的那些。
	// prevUserFP 就地消费（删除已比对过的），剩下的就是「已删除的用户」。
	changed := make([]string, 0, 8)
	for _, u := range snap.Users {
		if l.prevUserFP[u.UserID] != u.Fingerprint {
			changed = append(changed, u.UserID)
			continue
		}
		delete(l.prevUserFP, u.UserID)
	}
	for id := range l.prevUserFP {
		changed = append(changed, id)
	}

	if len(changed) == 0 {
		// 什么都没变：不清缓存。这是绝大多数重建的真实情况 ——
		// 订阅刷新、配额调整、会员天数变动都会走到这里。
		l.log.Info("配置重建但规则无变化，保留缓存", "cache_entries", c.Len())
		return
	}
	changed = append(changed, "") // fallback 的条目也要一起清
	c.FlushUsers(changed)
	l.log.Info("规则变更，已定向失效缓存", "users", len(changed)-1, "cache_entries", c.Len())
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
	// 只保留指纹，快照本身用完即弃 —— 这是内存占用的关键。
	l.prevGlobalFP = snap.GlobalFingerprint
	next := make(map[string]string, len(snap.Users))
	for _, u := range snap.Users {
		next[u.UserID] = u.Fingerprint
	}
	l.prevUserFP = next

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
