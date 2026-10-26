package subsub

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// refreshBudget 是单个订阅在一次定时刷新里的总预算。
//
// FetchTimeout（300s）是**单次网络尝试**的上限，而一轮刷新里还有
// 镜像回退、重试、解析、写库。给一个粗于单次请求、细于整个窗口的
// 上限，避免某个源卡死把整个摊开窗口拖过界。
//
// 取 6 分钟而不是更大：一轮刷新摊在 60 分钟内，单订阅上限 6 分钟时
// 即使最坏情况也不会把窗口挤爆。
const refreshBudget = 6 * time.Minute

// RefreshResult 是一次订阅刷新的结果。
type RefreshResult struct {
	SubID       string `json:"id"`
	LastCount   int    `json:"last_count"`
	LastStatus  string `json:"last_status"`
	FetchedAt   int64  `json:"last_fetched"`
	DurationMS  int64  `json:"duration_ms"`
	NotModified bool   `json:"not_modified"`
}

// Manager 负责订阅的拉取与入库。
//
// 它不直接写快照：刷新完成后通过 onChanged 回调通知 coord 重建，
// 这样「谁改了数据」和「怎么发布配置」两件事保持解耦。
type Manager struct {
	st      *store.Store
	fetcher *Fetcher
	log     *slog.Logger
	// onChanged 在条目真正写库之后通知上层重建快照。
	//
	// global 来自订阅自身（sub.IsGlobal），不是来自「谁调用了刷新」：
	// 全局订阅的条目对所有启用全局层的用户生效，只按创建者重建会让
	// 其他人一直停留在旧规则上。
	onChanged func(ctx context.Context, userID string, global bool)

	// locks 按 sub_id 串行化刷新，避免用户连点两次「立即刷新」
	// 导致同一订阅的两个事务互相覆盖（文档 6.3 并发控制）。
	locks sync.Map
	// sem 限制跨订阅的并发拉取数。
	sem chan struct{}
}

// NewManager 创建订阅管理器。
func NewManager(st *store.Store, log *slog.Logger, onChanged func(context.Context, string, bool)) *Manager {
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
	return m.refresh(ctx, sub, false, nil)
}

// RefreshForce 忽略 ETag / Last-Modified，强制完整重新拉取并重新解析。
//
// 存在的理由：条目上限、解析规则这类设置改完之后，已经缓存的订阅会一直走
// 304，永远不会用新设置重新解析 —— 管理员改了配置却看不到任何变化，
// 还以为是设置没生效。强制刷新给一个明确的「现在就按新规则重算」入口。
func (m *Manager) RefreshForce(ctx context.Context, sub *model.Subscription) (*RefreshResult, error) {
	return m.refresh(ctx, sub, true, nil)
}

func (m *Manager) refresh(ctx context.Context, sub *model.Subscription, force bool, sess *refreshSession) (*RefreshResult, error) {
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

	etag, lastMod := sub.LastETag, sub.LastModified
	if force {
		// 传空值让 Fetch 不带 If-None-Match / If-Modified-Since
		etag, lastMod = "", ""
	}

	// 一轮刷新内按内容等价键共享下载与解析。
	//
	// 只在 sess 非 nil（定时批量刷新）时生效：用户手动点「立即刷新」
	// 有明确的目标订阅，跟别的订阅共享结果没有收益，反而容易让人
	// 疑惑「我刷的是这个，为什么状态里显示的是另一个的耗时」。
	var canonKey string
	if sess != nil && !force {
		canonKey = CanonicalURL(sub.URL)
	}

	var fr *FetchResult
	var err error
	// 订阅自己配了超时就用它，否则走全局默认（0 会让 FetchTimeout 用默认值）。
	var subTimeout time.Duration
	if sub.TimeoutSec > 0 {
		subTimeout = time.Duration(sub.TimeoutSec) * time.Second
	}
	if canonKey != "" {
		fr, err = sess.fetch.do(canonKey, func() (*FetchResult, error) {
			return m.fetcher.FetchTimeout(ctx, sub.URL, etag, lastMod, subTimeout)
		})
	} else {
		fr, err = m.fetcher.FetchTimeout(ctx, sub.URL, etag, lastMod, subTimeout)
	}
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

	// 解析结果同样按「等价键 + 列表类型」共享：24 万条列表里跑一遍
	// `||domain^` 正则并不便宜，同组第二份没必要重跑。
	var parsed *ParseResult
	if canonKey != "" {
		if cached, ok := sess.parse.get(sessionKey(canonKey, sub.ListType)); ok {
			parsed = cached
		} else {
			parsed, err = Parse(fr.Content, Format(sub.Format), sub.ListType)
			if err == nil {
				sess.parse.put(sessionKey(canonKey, sub.ListType), parsed)
			}
		}
	} else {
		parsed, err = Parse(fr.Content, Format(sub.Format), sub.ListType)
	}
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

	// 条目上限：一次订阅拉进来 23 万条是常态，而这些条目会被**按用户压平**进
	// 快照，几个用户订同一份就成倍膨胀。超限时截断并告警，而不是拒绝整份订阅 ——
	// 截断至少还能提供前 N 条防护，拒绝等于用户什么都没得到。
	//
	// 截断只作用在「主导类别」上；例外条目（另一种类别）全部保留 ——
	// 例外通常只有几十上百条，砍掉它们会让本该放行的域名被拦，
	// 比多占几 KB 严重得多。
	if set, serr := m.st.GetSettings(ctx); serr == nil && set.MaxSubEntries > 0 && len(domains) > set.MaxSubEntries {
		m.log.Warn("订阅条目超过上限，已截断",
			"sub", sub.ID, "url", sub.URL,
			"got", len(domains), "limit", set.MaxSubEntries)
		domains = domains[:set.MaxSubEntries]
	}

	// 一份订阅里可以同时有 block 与 allow：AdBlock 语法用 `@@||domain^`
	// 表达白名单例外，公共列表里很常见（报障的那份有 3002 条）。
	// 旧实现按单一 kind 整批写入，`@@` 条目被静默丢弃 —— 用户写了放行却
	// 仍被拦，而状态显示 ok。这里改成混合写入，两种类别都保留。
	primary, secondary := domains, allow
	primaryKind, secondaryKind := "block", "allow"
	if kind == "allow" {
		primary, secondary = domains, block
		primaryKind, secondaryKind = "allow", "block"
	}
	mixed := make([]store.SubEntry, 0, len(primary)+len(secondary))
	for _, d := range primary {
		mixed = append(mixed, store.SubEntry{Domain: d, Kind: primaryKind})
	}
	for _, d := range secondary {
		mixed = append(mixed, store.SubEntry{Domain: d, Kind: secondaryKind})
	}

	if err := m.st.ReplaceSubEntriesMixed(ctx, sub.ID, mixed); err != nil {
		res.LastStatus = DescribeStatus(err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res, fmt.Errorf("写入订阅条目失败: %w", err)
	}

	count, _ := m.st.CountSubEntries(ctx, sub.ID)
	now := time.Now().Unix()
	// 有降级时把数字写进状态：这份源是靠把 $dnsrewrite 降级成拦/放才生效的，
	// 状态里不体现的话，排查「为什么某条规则没按改写生效」时无从下手。
	status := "ok"
	if parsed.RewriteDowngraded > 0 {
		status = fmt.Sprintf("ok: 含 %d 条改写降级为拦截", parsed.RewriteDowngraded)
	}
	if err := m.st.UpdateSubStatus(ctx, sub.ID, fr.ETag, fr.LastModified, status, now, count, 0); err != nil {
		m.log.Warn("更新订阅状态失败", "sub", sub.ID, "err", err)
	}

	res.LastCount = count
	res.LastStatus = status
	res.FetchedAt = now
	res.DurationMS = time.Since(start).Milliseconds()

	m.log.Info("订阅已刷新",
		"sub", sub.ID,
		"url", sub.URL,
		"entries", count,
		"skipped", parsed.Skipped,
		"rewrite_downgraded", parsed.RewriteDowngraded,
		"format", parsed.Detected,
		"truncated", fr.Truncated,
		"took_ms", res.DurationMS,
	)

	if m.onChanged != nil {
		m.onChanged(ctx, sub.UserID, sub.IsGlobal)
	}
	return res, nil
}

// RefreshAll 刷新全部启用订阅，返回成功与失败计数。
//
// 一轮刷新共享一个 session：内容等价的订阅只下载、只解析一次。
// 顺序按等价键排序，让同组订阅挨在一起 —— 否则第一份拉完之后
// 缓存还在，但第二份要等几百个订阅之后才轮到，结果一样，只是
// 内存里多留一会儿。
func (m *Manager) RefreshAll(ctx context.Context) (refreshed, failed int) {
	subs, err := m.st.AllEnabledSubs(ctx)
	if err != nil {
		m.log.Error("读取启用订阅失败", "err", err)
		return 0, 0
	}
	sort.SliceStable(subs, func(i, j int) bool {
		return CanonicalURL(subs[i].URL) < CanonicalURL(subs[j].URL)
	})

	sess := newRefreshSession()
	for _, sub := range subs {
		select {
		case <-ctx.Done():
			return refreshed, failed
		default:
		}
		if _, err := m.refresh(ctx, sub, false, sess); err != nil {
			failed++
			continue
		}
		refreshed++
	}
	if shared := sess.fetch.savedFetches; shared > 0 {
		m.log.Info("订阅刷新去重生效", "省掉的重复拉取", shared,
			"耗时", time.Since(sess.start).String())
	}
	return refreshed, failed
}

// Scheduler 周期性刷新订阅（文档 6.3）。
type Scheduler struct {
	mgr      *Manager
	interval time.Duration
	jitter   time.Duration
	log      *slog.Logger
	hour     int
	minute   int
}

// SchedulerOptions 是调度器的可配置项。
type SchedulerOptions struct {
	// IntervalHours 是刷新周期（小时），<=0 时用 24。
	IntervalHours int
	// Hour 是每天几点开始刷新（本地时区 0-23）。
	//
	// 为什么要按「整点」而不是纯粹按 interval 轮转：公共规则源大多在
	// 固定时段更新，深夜刷新对用户影响最小，也让 CPU 尖峰避开业务高峰。
	//
	// **0 是合法值**（0 点刷新是常见需求），所以能不能用 0 取决于
	// HourSet —— 不能只看 Hour >= 0，那会让「没配置」和「设为 0 点」
	// 无法区分，结果默认值永远生效（实测就是这样把默认 3 点变成 0 点、
	// 又把显式配置的 0 点顶成 3 点）。调用方从设置里取值时必须同时置
	// HourSet=true。
	Hour    int
	HourSet bool
	// Minute 是起始分钟（0-59），同样用 MinuteSet 区分未配置与 0。
	Minute    int
	MinuteSet bool
	// SpreadMinutes 是「把这一批订阅摊开的窗口」（分钟）。
	//
	// 原来所有订阅在同一个 tick 里挤着刷新，实测一小时 64 次拉取、
	// 47 次快照重建，每次重建 9-14 秒 —— 2 核机器直接被顶满。
	// 摊开之后同样的工作量分散到整个窗口，CPU 变成平滑的。
	SpreadMinutes int
}

// NewScheduler 用默认选项创建调度器（兼容旧调用）。
func NewScheduler(mgr *Manager, intervalHours int, log *slog.Logger) *Scheduler {
	return NewSchedulerWithOptions(mgr, SchedulerOptions{IntervalHours: intervalHours}, log)
}

// NewSchedulerWithOptions 创建带完整选项的调度器。
func NewSchedulerWithOptions(mgr *Manager, opt SchedulerOptions, log *slog.Logger) *Scheduler {
	if opt.IntervalHours <= 0 {
		opt.IntervalHours = 24
	}
	// 时刻：未显式指定时用凌晨 3 点；指定了就必须落在合法区间，
	// 越界同样回落默认（0 点在这里是合法值）。
	if !opt.HourSet || opt.Hour < 0 || opt.Hour > 23 {
		opt.Hour = 3
	}
	if !opt.MinuteSet || opt.Minute < 0 || opt.Minute > 59 {
		opt.Minute = 0
	}
	if opt.SpreadMinutes <= 0 {
		opt.SpreadMinutes = 60 // 默认摊开一小时
	}
	if opt.SpreadMinutes > 24*60 {
		opt.SpreadMinutes = 24 * 60
	}
	return &Scheduler{
		mgr:      mgr,
		interval: time.Duration(opt.IntervalHours) * time.Hour,
		jitter:   time.Duration(opt.SpreadMinutes) * time.Minute,
		log:      log,
		hour:     opt.Hour,
		minute:   opt.Minute,
	}
}

// nextRun 计算距下一个刷新时刻的等待时间。
//
// 按「每天 hour:minute（北京时间）」对齐，而不是「启动后 +24h」：
// apid 重启很频繁（每次升级、每次改配置重建都可能重启），
// 用启动时刻做基准会让刷新时间点漂移到任意时刻 —— 管理员明明设了
// 凌晨 3 点，实际可能中午才跑。对齐到固定整点才符合「可配置」的语义。
func (s *Scheduler) nextRun(now time.Time) time.Time {
	loc := now.Location()
	next := time.Date(now.Year(), now.Month(), now.Day(), s.hour, s.minute, 0, 0, loc)
	for !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// Run 启动调度循环直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	// 启动时不立即刷新：apid 重启频率远高于订阅变化频率，
	// 每次重启都全量拉一遍会把公共规则源当成压测目标。
	s.log.Info("订阅调度器已启动",
		"周期", s.interval.String(),
		"每日时刻", fmt.Sprintf("%02d:%02d", s.hour, s.minute),
		"摊开窗口", s.jitter.String())

	for {
		now := time.Now()
		next := s.nextRun(now)
		wait := next.Sub(now)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.log.Info("开始订阅定时刷新", "摊开窗口", s.jitter.String())
		s.runSpread(ctx)
	}
}

// runSpread 把这一轮的所有订阅摊到 jitter 窗口内陆续刷新。
//
// 关键点：**不能**简单地「每个订阅 sleep 固定间隔」然后串行跑 ——
// 单次刷新本身可能耗时几十秒（大列表），固定间隔完全无法保证摊开效果。
// 这里的做法是按窗口把启动时刻预先算好，每个订阅在自己的时刻由一个
// 独立的 goroutine 发起，并发度由 Manager.sem（4）兜住。
func (s *Scheduler) runSpread(ctx context.Context) {
	subs, err := s.mgr.st.AllEnabledSubs(ctx)
	if err != nil {
		s.log.Error("读取启用订阅失败", "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	// 按等价键排序，让同组订阅的启动时刻相邻 —— 同组第二份大概率
	// 还能命中拉取缓存（缓存只在一轮内有效）。
	sort.SliceStable(subs, func(i, j int) bool {
		return CanonicalURL(subs[i].URL) < CanonicalURL(subs[j].URL)
	})

	window := s.jitter
	if window <= 0 {
		window = time.Minute
	}
	step := window / time.Duration(len(subs))
	if step < time.Second {
		step = time.Second
	}

	sess := newRefreshSession()
	var (
		wg                sync.WaitGroup
		mu                sync.Mutex
		refreshed, failed int
	)

	for i, sub := range subs {
		// 第 i 个订阅在 i*step 之后开始。
		delay := time.Duration(i) * step
		wg.Add(1)
		go func(sub *model.Subscription, delay time.Duration) {
			defer wg.Done()
			if delay > 0 {
				t := time.NewTimer(delay)
				defer t.Stop()
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
			// 每个订阅用自己的超时预算，互不影响。
			rctx, cancel := context.WithTimeout(ctx, refreshBudget)
			defer cancel()
			_, err := s.mgr.refresh(rctx, sub, false, sess)
			mu.Lock()
			if err != nil {
				failed++
			} else {
				refreshed++
			}
			mu.Unlock()
		}(sub, delay)
	}
	wg.Wait()

	s.log.Info("订阅定时刷新完成",
		"refreshed", refreshed, "failed", failed,
		"摊开窗口", window.String(),
		"省掉的重复拉取", sess.fetch.savedFetches)
}

// pick 返回第一个非空值。
func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SetProxy 更新订阅拉取的出口代理，立即对所有后续拉取生效。
func (m *Manager) SetProxy(raw string) error {
	if m.fetcher == nil {
		return nil
	}
	return m.fetcher.SetProxy(raw)
}

// SetDefaultTimeout 更新订阅拉取的默认超时。
//
// 面板上改了 sub_fetch_timeout_sec 之后由调用方重新调一次即可生效，
// 不需要重启 apid。订阅自己配了 timeout_sec 时仍然优先用自己的。
func (m *Manager) SetDefaultTimeout(d time.Duration) {
	if m.fetcher == nil || d <= 0 {
		return
	}
	m.fetcher.SetTimeout(d)
	m.log.Info("订阅默认拉取超时已更新", "timeout", d.String())
}
