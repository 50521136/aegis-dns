// Command dnsd 是 DNS 引擎进程。
//
// 设计要点（文档 P1/P2/C1-C5）：
//   - 启动时把配置全部载入内存，运行期不查询数据库（只批量写统计）；
//   - 与 apid 之间没有任何 RPC，只通过 data/runtime/config.json 单向通信；
//   - 53 / 853 / 443 由本进程直接监听，不依赖任何反向代理。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/dns"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/snapshot"
	"github.com/50521136/aegis-dns/server/internal/stats"
	"github.com/50521136/aegis-dns/server/internal/store"
	"github.com/50521136/aegis-dns/server/internal/version"
)

func main() {
	var (
		cfgPath     = flag.String("config", "config.yaml", "配置文件路径")
		showVersion = flag.Bool("version", false, "打印版本并退出")
		checkOnly   = flag.Bool("check", false, "只做启动前检查（端口/证书/快照）后退出")
	)
	flag.Parse()

	version.Component = "dnsd"

	if *showVersion {
		v := version.Get()
		fmt.Printf("aegis-dnsd %s (%s, %s, %s)\n", v.Version, v.Commit, v.BuildTime, v.Platform)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置加载失败:", err)
		os.Exit(1)
	}

	log := newLogger(cfg.Log)

	if err := cfg.ValidateForDNS(); err != nil {
		log.Error("配置校验失败", "err", err)
		os.Exit(1)
	}
	if err := cfg.EnsureDirs(); err != nil {
		log.Error("初始化数据目录失败", "err", err)
		os.Exit(1)
	}

	if *checkOnly {
		os.Exit(runChecks(cfg, log))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- 证书 ---
	certFile, keyFile := cfg.ResolveCert()
	certs := certmgr.New(certFile, keyFile, cfg.DNS.DevTLS, log)
	if err := certs.Ensure(); err != nil {
		// 没有证书不是致命错误：UDP/TCP 仍然要能用（文档 3.4 兜底）。
		log.Warn("没有可用证书，DoT/DoH 将不可用，仅提供 UDP/TCP", "cert", certFile, "key", keyFile, "err", err)
	} else {
		log.Info("证书已加载",
			"cert", certFile,
			"not_after", time.Unix(certs.NotAfter(), 0).Format("2006-01-02"),
			"days_left", int(time.Until(time.Unix(certs.NotAfter(), 0)).Hours()/24),
		)
		go certs.Watch(ctx.Done())
	}

	// --- 统计收集器 ---
	collector := stats.New(cfg.DNS.QueryLogRing, true)

	// --- 注册表与处理器 ---
	pools := resolver.NewCache()
	registry := dns.NewRegistry(pools)
	handler := dns.NewHandler(registry, dns.NewCache(10000, 60, 86400), collector, log)

	// --- 快照加载（先加载再监听，保证对外服务时规则已就绪）---
	loader := dns.NewLoader(cfg.SnapshotPath(), registry, handler, log)
	loader.OnReload(func(snap *model.Snapshot) {
		handler.ApplyDefaults(snap.Defaults)
		// 缓存容量变化时重建缓存，避免旧容量一直生效。
		handler.ResizeCache(snap.Defaults.CacheSize, snap.Defaults.CacheMinTTL, snap.Defaults.CacheMaxTTL)
	})
	loader.Run(ctx)

	// --- DNS 服务 ---
	srv := dns.NewServer(cfg, handler, certs, log)
	if err := srv.Start(ctx); err != nil {
		log.Error("DNS 服务启动失败", "err", err)
		os.Exit(1)
	}

	// --- 统计落库（唯一允许 dnsd 写数据库的地方）---
	storeHandle, storeErr := store.Open(cfg.DBPath())
	if storeErr != nil {
		log.Warn("无法打开数据库，统计将不会持久化（解析功能不受影响）", "err", storeErr)
	} else {
		defer storeHandle.Close()
		go flushLoop(ctx, storeHandle, collector, cfg, log)
	}
	// 缓存未命中分类指标：dnsd 与 apid 零 RPC，apid 的 /admin/system 读不到
	// dnsd 的内存计数器，所以这些数字只能在 dnsd 侧落地。
	// 用周期性结构化日志（而不是新增端点或写库）的好处：不引入新的传输通道、
	// 不动表结构，运维 grep 一次就能看，也能被 journal 留存回溯。
	go metricsLoop(ctx, handler, log)
	// 打印生效域名而不是 cfg.Domain：config.yaml 的 domain 只是首次初始化的兜底，
	// 后台改过域名后它就不再更新，直接打印会报出一个早就不用的域名。
	effDomain := registry.Domain()
	if effDomain == "" {
		effDomain = cfg.Domain
	}
	log.Info("aegis-dnsd 已就绪",
		"version", version.Version,
		"domain", effDomain,
		"data_dir", cfg.DataDir,
		"snapshot_version", registry.Version(),
	)
	if cfg.Domain != "" && effDomain != "" && !strings.EqualFold(cfg.Domain, effDomain) {
		log.Warn("config.yaml 的 domain 与后台设置不一致，以后台设置为准",
			"config_domain", cfg.Domain,
			"effective_domain", effDomain,
			"hint", "建议把 config.yaml 的 domain 改成一致，避免自检和横幅误报",
		)
	}

	// --- 等待退出信号 ---
	<-ctx.Done()
	log.Info("收到退出信号，开始优雅关闭")

	// 关闭前把统计刷一次，避免丢掉最后一批数据。
	// 这里用一次性调度器：退避与缓冲在退出路径上没有意义，只求尽力写完。
	if storeHandle != nil {
		flushOnce(context.Background(), storeHandle, collector, newFlushScheduler(defaultFlushBufferRows), log)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("关闭 DNS 监听时出错", "err", err)
	}
	pools.Close()
	log.Info("aegis-dnsd 已退出")
}

// snapshotModel 别名已移除：main 直接使用 model.Snapshot，避免类型别名
// 在跨包回调里制造不必要的间接层。

// metricsLoop 每 5 分钟打一条缓存指标快照。
//
// 存在的理由：命中率低的时候，光看「命中率 55%」无法判断该动什么 ——
// 冷启动多说明域名长尾（容量/TTL），过期多说明上游 TTL 短（该调 minTTL），
// 被并发合并的多说明是并发击穿（singleflight 已挡住上游压力，不该再拿命中率
// 当目标）。三者动作完全不同，所以把分类计数打出来，让判断有据可依。
func metricsLoop(ctx context.Context, h *dns.Handler, log *slog.Logger) {
	const interval = 5 * time.Minute
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m := h.Metrics()
			st := dns.StageMetrics()
			absent, _ := m["cache_miss_absent"].(int64)
			expired, _ := m["cache_miss_expired"].(int64)
			merged, _ := m["cache_merged"].(int64)
			totalMiss := absent + expired
			// 占比用 ‰ 表示（整数，避免浮点日志在不同版本里格式不一致）
			var absentPermille, expiredPermille int64
			if totalMiss > 0 {
				absentPermille = absent * 1000 / totalMiss
				expiredPermille = expired * 1000 / totalMiss
			}
			log.Info("缓存指标快照",
				"miss_absent", absent,
				"miss_expired", expired,
				"merged", merged,
				"absent_permille", absentPermille,
				"expired_permille", expiredPermille,
				"inflight", m["inflight"],
				"rejected", m["rejected"],
				"upstream_errors", m["upstream_errors"],
			)

			// 分阶段耗时快照。
			//
			// 单独一条日志，不要和缓存指标混在一起：缓存那条是固定字段
			// 的业务指标，这条是排查用的诊断数据，字段多、涨落大。
			//
			// 输出 P50/P90/P99 而不是只给均值：尾部延迟问题的特征就是
			// 均值正常、P90 开始发散，只看 avg 会得出「一切正常」的错误结论
			// （线上 P50=6ms 但 P90=64ms 就是这个形状）。
			for _, name := range []string{"identify", "gate", "rules", "cache", "upstream", "total"} {
				ss := st[name]
				if ss.Count == 0 {
					continue
				}
				log.Info("阶段耗时快照",
					"stage", name,
					"samples", ss.Count,
					"avg_us", ss.AvgUS,
					"p50_us", ss.P50US,
					"p90_us", ss.P90US,
					"p99_us", ss.P99US,
					"max_us", ss.MaxUS,
				)
			}
		}
	}
}

// flushLoop 按配置的间隔把内存统计批量写入 SQLite（文档 8.3 / R4 对策）。
func flushLoop(ctx context.Context, st *store.Store, c *stats.Collector, cfg *config.Config, log *slog.Logger) {
	interval := time.Duration(cfg.DNS.StatsFlushSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	log.Info("统计落库循环已启动", "interval", interval.String())

	// 带重试的落库调度器：失败的那批不再丢弃，放回缓冲与下一轮合并重试。
	// 详见 flushretry.go —— 旧实现 flush 失败即丢，只留一行 Error 日志。
	sched := newFlushScheduler(defaultFlushBufferRows)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// 退出前再冲一次，尽量少丢最后几秒的数据。
			flushOnce(ctx, st, c, sched, log)
			return
		case <-t.C:
			if sched.shouldSkip() {
				continue
			}
			flushOnce(ctx, st, c, sched, log)
		}
	}
}

func flushOnce(ctx context.Context, st *store.Store, c *stats.Collector, sched *flushScheduler, log *slog.Logger) {
	rollups, tops, logs := c.Flush()

	// 把这一轮的新数据并进（可能非空的）待重试缓冲，然后整批写一次。
	// 合并而不是分别写：库恢复慢时会积累几十个小事务，合并后只有一个。
	sr := make([]store.RollupBucket, 0, len(rollups))
	for _, r := range rollups {
		sr = append(sr, store.RollupBucket{
			UserID: r.UserID, TS: r.TS, Total: r.Total,
			Blocked: r.Blocked, Cached: r.Cached, Allowed: r.Allowed, LatencySum: r.LatencySum,
		})
	}
	stTop := make([]store.TopBucket, 0, len(tops))
	for _, t := range tops {
		stTop = append(stTop, store.TopBucket{
			UserID: t.UserID, TS: t.TS, Domain: t.Domain, Hits: t.Hits, Blocked: t.Blocked,
		})
	}
	sched.buf.add(sr, stTop, logs)
	total := sched.buf.len()
	if total == 0 {
		sched.onSuccess() // 没数据也算一轮健康
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	pendingR, pendingT, pendingL := sched.buf.take()
	if err := st.WriteStats(ctx, pendingR, pendingT, pendingL); err != nil {
		// 失败：把这一批放回缓冲，下一轮（退避后）重试。
		// 缓冲有上限，超限时丢最旧的并累计 dropped —— 宁可丢统计，也不能把
		// dnsd 的内存吃光（它是解析进程，被统计拖垮等于 DNS 全断）。
		sched.buf.add(pendingR, pendingT, pendingL)
		sched.onFailure()
		log.Error("统计落库失败，已放回缓冲待重试",
			"err", err,
			"rollups", len(pendingR), "tops", len(pendingT), "logs", len(pendingL),
			"buffered", sched.buf.len(), "dropped_total", sched.buf.dropped,
			"failed_rounds", sched.failedRounds, "skip_rounds", sched.skip)
		return
	}
	sched.onSuccess()
	log.Debug("统计已落库",
		"rollups", len(pendingR), "tops", len(pendingT), "logs", len(pendingL),
		"dropped_total", sched.buf.dropped)
}

// runChecks 做启动前自检，返回进程退出码。
//
// 覆盖文档风险 R2（53 被 systemd-resolved 占用）与 R1（证书缺失）：
// 这两类问题在真实部署里出现频率最高，且表现是「服务起不来但日志看不出原因」。
func runChecks(cfg *config.Config, log *slog.Logger) int {
	fail := 0

	fmt.Println("aegis-dnsd 启动前自检")
	fmt.Println(strings.Repeat("-", 52))

	// 1. 配置
	// 这里只能看 config.yaml，因为自检跑在加载数据库之前。
	// 明确标注「兜底值」，免得和后台设置的真实域名混淆。
	fmt.Printf("%-34s %s\n", "主域名(config.yaml 兜底)", orDash(cfg.Domain))
	fmt.Printf("%-34s %s\n", "数据目录", cfg.DataDir)

	// 2. 端口占用
	for _, p := range []struct{ name, addr string }{
		{"UDP DNS", cfg.DNS.ListenUDP},
		{"TCP DNS", cfg.DNS.ListenTCP},
		{"DoT", cfg.DNS.ListenDOT},
		{"DoH", cfg.DNS.ListenDOH},
	} {
		if p.addr == "" {
			continue
		}
		// UDP 必须用 ListenPacket：net.Listen("udp", ...) 会报
		// "unexpected address type"，那是个假阴性，会让人以为端口被占用。
		var (
			closer interface{ Close() error }
			err    error
		)
		if strings.HasPrefix(p.name, "UDP") {
			var pc net.PacketConn
			pc, err = net.ListenPacket("udp", p.addr)
			closer = pc
		} else {
			var ln net.Listener
			ln, err = net.Listen("tcp", p.addr)
			closer = ln
		}
		if err != nil {
			fmt.Printf("%-34s ✗ %v\n", p.name+" "+p.addr, err)
			if strings.Contains(err.Error(), "address already in use") {
				fmt.Println("     提示：53 端口常被 systemd-resolved 占用。最小改动的修法是")
				fmt.Println("     只关掉它的 stub listener，而不是停用整个服务：")
				fmt.Println("       mkdir -p /etc/systemd/resolved.conf.d")
				fmt.Println("       printf '[Resolve]\\nDNSStubListener=no\\n' > /etc/systemd/resolved.conf.d/99-aegis.conf")
				fmt.Println("       systemctl restart systemd-resolved")
			}
			fail++
			continue
		}
		if closer != nil {
			closer.Close()
		}
		fmt.Printf("%-34s ✓\n", p.name+" "+p.addr)
	}

	// 3. 证书
	certFile, keyFile := cfg.ResolveCert()
	cm := certmgr.New(certFile, keyFile, cfg.DNS.DevTLS, log)
	if err := cm.Load(); err != nil {
		fmt.Printf("%-34s ✗ %v\n", "证书", err)
		fmt.Println("     提示：无证书时 DoT/DoH 会被跳过，UDP/TCP 仍可用")
	} else {
		days := int(time.Until(time.Unix(cm.NotAfter(), 0)).Hours() / 24)
		mark := "✓"
		if days < 14 {
			mark = "⚠"
		}
		fmt.Printf("%-34s %s 剩余 %d 天\n", "证书", mark, days)
	}

	// 4. 快照
	if _, err := os.Stat(cfg.SnapshotPath()); err != nil {
		fmt.Printf("%-34s ⚠ 不存在（等待 apid 首次写入）\n", "配置快照")
	} else {
		ver, _ := snapshot.ReadVersion(cfg.SnapshotPath())
		fmt.Printf("%-34s ✓ version=%d\n", "配置快照", ver)
	}

	fmt.Println(strings.Repeat("-", 52))
	if fail > 0 {
		fmt.Printf("结果：%d 项失败\n", fail)
		return 1
	}
	fmt.Println("结果：全部通过")
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "(未配置)"
	}
	return s
}

func newLogger(lc config.Log) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(lc.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(lc.Format, "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	log := slog.New(h)
	slog.SetDefault(log)
	return log
}
