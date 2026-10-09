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
	log.Info("aegis-dnsd 已就绪",
		"version", version.Version,
		"domain", cfg.Domain,
		"data_dir", cfg.DataDir,
		"snapshot_version", registry.Version(),
	)

	// --- 等待退出信号 ---
	<-ctx.Done()
	log.Info("收到退出信号，开始优雅关闭")

	// 关闭前把统计刷一次，避免丢掉最后一批数据。
	if storeHandle != nil {
		flushOnce(context.Background(), storeHandle, collector, cfg, log)
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

// flushLoop 按配置的间隔把内存统计批量写入 SQLite（文档 8.3 / R4 对策）。
func flushLoop(ctx context.Context, st *store.Store, c *stats.Collector, cfg *config.Config, log *slog.Logger) {
	interval := time.Duration(cfg.DNS.StatsFlushSeconds) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	log.Info("统计落库循环已启动", "interval", interval.String())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			flushOnce(ctx, st, c, cfg, log)
		}
	}
}

func flushOnce(ctx context.Context, st *store.Store, c *stats.Collector, cfg *config.Config, log *slog.Logger) {
	rollups, tops, logs := c.Flush()
	if len(rollups) == 0 && len(tops) == 0 && len(logs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

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
	if err := st.WriteStats(ctx, sr, stTop, logs); err != nil {
		// 落库失败不回滚内存计数：这些计数已经丢了，但至少不要拖累查询路径。
		log.Error("统计落库失败", "err", err, "rollups", len(sr), "tops", len(stTop), "logs", len(logs))
		return
	}
	log.Debug("统计已落库", "rollups", len(sr), "tops", len(stTop), "logs", len(logs))
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
	fmt.Printf("%-34s %s\n", "主域名", orDash(cfg.Domain))
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
