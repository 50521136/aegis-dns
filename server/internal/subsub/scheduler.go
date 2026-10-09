package subsub

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// RefreshResult 是一次订阅刷新的结果。
type RefreshResult struct {
	SubID      string `json:"id"`
	LastCount  int    `json:"last_count"`
	LastStatus string `json:"last_status"`
	FetchedAt  int64  `json:"last_fetched"`
	DurationMS int64  `json:"duration_ms"`
	NotModified bool  `json:"not_modified"`
}

// Manager 负责订阅的拉取与入库。
//
// 它不直接写快照：刷新完成后通过 onChanged 回调通知 coord 重建，
// 这样「谁改了数据」和「怎么发布配置」两件事保持解耦。
type Manager struct {
	st        *store.Store
	fetcher   *Fetcher
	log       *slog.Logger
	onChanged func(ctx context.Context, userID string)

	// locks 按 sub_id 串行化刷新，避免用户连点两次「立即刷新」
	// 导致同一订阅的两个事务互相覆盖（文档 6.3 并发控制）。
	locks sync.Map
	// sem 限制跨订阅的并发拉取数。
	sem chan struct{}
}

// NewManager 创建订阅管理器。
func NewManager(st *store.Store, log *slog.Logger, onChanged func(context.Context, string)) *Manager {
	return &Manager{
		st:        st,
		fetcher:   NewFetcher(),
		log:       log,
		onChanged: onChanged,
		sem:       make(chan struct{}, 4),
	}
}

// subLock 取得某个订阅的互斥锁。
func (m *Manager) subLock(subID string) *sync.Mutex {
	v, _ := m.locks.LoadOrStore(subID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Refresh 刷新单个订阅。
func (m *Manager) Refresh(ctx context.Context, sub *model.Subscription) (*RefreshResult, error) {
	mu := m.subLock(sub.ID)
	mu.Lock()
	defer mu.Unlock()

	// 全局并发上限。
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	start := time.Now()
	res := &RefreshResult{SubID: sub.ID}

	fr, err := m.fetcher.Fetch(ctx, sub.URL, sub.LastETag, sub.LastModified)
	if err != nil {
		status := DescribeStatus(err)
		// 保留旧条目：只更新状态与失败计数（文档 6.4 核心原则）。
		failCount := sub.FailCount + 1
		if uerr := m.st.UpdateSubStatus(ctx, sub.ID, sub.LastETag, sub.LastModified, status, time.Now().Unix(), sub.LastCount, failCount); uerr != nil {
			m.log.Warn("更新订阅状态失败", "sub", sub.ID, "err", uerr)
		}
		res.LastStatus = status
		res.LastCount = sub.LastCount
		res.DurationMS = time.Since(start).Milliseconds()
		m.log.Warn("订阅刷新失败，保留旧规则", "sub", sub.ID, "url", sub.URL, "err", err, "fail_count", failCount)
		return res, fmt.Errorf("刷新订阅失败: %w", err)
	}

	if fr.NotModified {
		res.NotModified = true
		res.LastStatus = "ok"
		res.LastCount = sub.LastCount
		res.FetchedAt = time.Now().Unix()
		res.DurationMS = time.Since(start).Milliseconds()
		if uerr := m.st.UpdateSubStatus(ctx, sub.ID, pick(fr.ETag, sub.LastETag), pick(fr.LastModified, sub.LastModified), "ok", res.FetchedAt, sub.LastCount, 0); uerr != nil {
			m.log.Warn("更新订阅状态失败", "sub", sub.ID, "err", uerr)
		}
		m.log.Debug("订阅未变更（304）", "sub", sub.ID)
		return res, nil
	}

	parsed, err := Parse(fr.Content, Format(sub.Format))
	if err != nil {
		status := DescribeStatus(err)
		failCount := sub.FailCount + 1
		if uerr := m.st.UpdateSubStatus(ctx, sub.ID, sub.LastETag, sub.LastModified, status, time.Now().Unix(), sub.LastCount, failCount); uerr != nil {
			m.log.Warn("更新订阅状态失败", "sub", sub.ID, "err", uerr)
		}
		res.LastStatus = status
		res.LastCount = sub.LastCount
		res.DurationMS = time.Since(start).Milliseconds()
		m.log.Warn("订阅解析失败，保留旧规则", "sub", sub.ID, "err", err)
		return res, err
	}

	block, allow := SplitByKind(parsed.Entries)
	kind := "block"
	domains := block
	if sub.ListType == model.ListAllow {
		kind = "allow"
		domains = allow
	}

	if err := m.st.ReplaceSubEntries(ctx, sub.ID, kind, domains); err != nil {
		res.LastStatus = DescribeStatus(err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res, fmt.Errorf("写入订阅条目失败: %w", err)
	}

	count, _ := m.st.CountSubEntries(ctx, sub.ID)
	now := time.Now().Unix()
	if err := m.st.UpdateSubStatus(ctx, sub.ID, fr.ETag, fr.LastModified, "ok", now, count, 0); err != nil {
		m.log.Warn("更新订阅状态失败", "sub", sub.ID, "err", err)
	}

	res.LastCount = count
	res.LastStatus = "ok"
	res.FetchedAt = now
	res.DurationMS = time.Since(start).Milliseconds()

	m.log.Info("订阅已刷新",
		"sub", sub.ID,
		"url", sub.URL,
		"entries", count,
		"skipped", parsed.Skipped,
		"format", parsed.Detected,
		"truncated", fr.Truncated,
		"took_ms", res.DurationMS,
	)

	if m.onChanged != nil {
		m.onChanged(ctx, sub.UserID)
	}
	return res, nil
}

// RefreshAll 刷新全部启用订阅，返回成功与失败计数。
func (m *Manager) RefreshAll(ctx context.Context) (refreshed, failed int) {
	subs, err := m.st.AllEnabledSubs(ctx)
	if err != nil {
		m.log.Error("读取启用订阅失败", "err", err)
		return 0, 0
	}
	for _, sub := range subs {
		select {
		case <-ctx.Done():
			return refreshed, failed
		default:
		}
		if _, err := m.Refresh(ctx, sub); err != nil {
			failed++
			continue
		}
		refreshed++
	}
	return refreshed, failed
}

// Scheduler 周期性刷新订阅（文档 6.3）。
type Scheduler struct {
	mgr      *Manager
	interval time.Duration
	jitter   time.Duration
	log      *slog.Logger
}

// NewScheduler 创建调度器。
//
// interval 为 0 时默认 24 小时；jitter 默认 30 分钟。
// 抖动的作用是避免多实例部署时所有实例在同一秒冲击订阅源。
func NewScheduler(mgr *Manager, intervalHours int, log *slog.Logger) *Scheduler {
	if intervalHours <= 0 {
		intervalHours = 24
	}
	return &Scheduler{
		mgr:      mgr,
		interval: time.Duration(intervalHours) * time.Hour,
		jitter:   30 * time.Minute,
		log:      log,
	}
}

// Run 启动调度循环直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	// 启动时不立即刷新：apid 重启频率远高于订阅变化频率，
	// 每次重启都全量拉一遍会把公共规则源当成压测目标。
	s.log.Info("订阅调度器已启动", "interval", s.interval.String(), "jitter", s.jitter.String())

	for {
		delay := s.interval
		if s.jitter > 0 {
			delay += time.Duration(rand.Int63n(int64(s.jitter)))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			refreshed, failed := s.mgr.RefreshAll(ctx)
			s.log.Info("订阅定时刷新完成", "refreshed", refreshed, "failed", failed)
		}
	}
}

// pick 返回第一个非空值。
func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
