// Command apid 是 API + 内嵌前端进程。
//
// 与 dnsd 的关系（文档 2.3 架构红线）：
//   - 两者之间没有任何 RPC，只通过 data/runtime/config.json 单向通信；
//   - apid 挂了，dnsd 照常解析（它已经把配置载入内存）；
//   - 前端由本进程 go:embed 内嵌托管，因此「前端部署」不是一个独立步骤。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/50521136/aegis-dns/server/internal/api"
	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/coord"
	"github.com/50521136/aegis-dns/server/internal/selfupdate"
	"github.com/50521136/aegis-dns/server/internal/store"
	"github.com/50521136/aegis-dns/server/internal/subsub"
	"github.com/50521136/aegis-dns/server/internal/version"
)

func main() {
	var (
		cfgPath     = flag.String("config", "config.yaml", "配置文件路径")
		showVersion = flag.Bool("version", false, "打印版本并退出")
		checkOnly   = flag.Bool("check", false, "只做启动前检查后退出")
		genSecret   = flag.Bool("gen-secret", false, "生成一个 JWT 密钥并退出")
	)
	flag.Parse()

	version.Component = "apid"

	if *genSecret {
		fmt.Println(auth.GenerateSecret())
		return
	}

	if *showVersion {
		v := version.Get()
		fmt.Printf("aegis-apid %s (%s, %s, %s)\n", v.Version, v.Commit, v.BuildTime, v.Platform)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置加载失败:", err)
		os.Exit(1)
	}
	log := newLogger(cfg.Log)

	if err := cfg.ValidateForAPI(); err != nil {
		log.Error("配置校验失败", "err", err)
		fmt.Fprintln(os.Stderr, "\n提示：用 ./aegis-apid -gen-secret 生成一个 JWT 密钥，")
		fmt.Fprintln(os.Stderr, "      写入 .env 的 JWT_SECRET，并在配置里写 api.jwt_secret: ${JWT_SECRET}")
		os.Exit(1)
	}
	if err := cfg.EnsureDirs(); err != nil {
		log.Error("初始化数据目录失败", "err", err)
		os.Exit(1)
	}

	if *checkOnly {
		os.Exit(runChecks(cfg))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- 数据库 ---
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		log.Error("打开数据库失败", "path", cfg.DBPath(), "err", err)
		os.Exit(1)
	}
	defer st.Close()
	log.Info("数据库已就绪", "path", cfg.DBPath())

	// --- 初始管理员 ---
	adminHash, err := auth.HashPassword(cfg.API.Admin.Password)
	if err != nil {
		log.Error("生成管理员密码哈希失败", "err", err)
		os.Exit(1)
	}
	created, err := st.EnsureAdmin(ctx, cfg.API.Admin.Username, cfg.API.Admin.Email, adminHash)
	if err != nil {
		log.Error("创建管理员失败", "err", err)
		os.Exit(1)
	}
	if created {
		log.Warn("已创建初始管理员账号，请登录后立即修改密码",
			"username", cfg.API.Admin.Username)
		// 提示从配置里移除明文密码：密码已经入库，配置里再留一份就是多余的风险面。
		log.Warn("密码已入库，建议从 config.yaml 中删除 api.admin.password，并保留 .env 里的 ADMIN_PASSWORD 作为应急")
	}

	// --- 用配置文件播种全局设置 ---
	if err := seedSettingsFromConfig(ctx, st, cfg, log); err != nil {
		log.Warn("播种全局设置失败（不影响启动）", "err", err)
	}

	// --- 证书 ---
	certFile, keyFile := cfg.ResolveCert()
	certs := certmgr.New(certFile, keyFile, cfg.DNS.DevTLS, log)
	if err := certs.Ensure(); err != nil {
		log.Warn("证书不可用：HTTPS 面板与证书指纹功能将不可用（HTTP 端口不受影响）", "cert", certFile, "err", err)
	} else {
		go certs.Watch(ctx.Done())
	}

	// --- 快照协调器 ---
	cd := coord.New(st, cfg.SnapshotPath(), log)
	if err := cd.EnsureInitial(ctx); err != nil {
		log.Error("初始化配置快照失败", "err", err)
		// 不退出：dnsd 会以空配置启动，apid 恢复后写入快照即可。
	}

	// --- 订阅管理 ---
	subsMgr := subsub.NewManager(st, log, func(c context.Context, userID string) {
		if _, err := cd.Changed(c, userID); err != nil {
			log.Error("订阅刷新后重建快照失败", "user", userID, "err", err)
		}
	})
	settings, err := st.GetSettings(ctx)
	if err != nil {
		log.Warn("读取全局设置失败，使用默认值", "err", err)
		settings = ptrSettings()
	}
	go subsub.NewScheduler(subsMgr, settings.SubIntervalH, log).Run(ctx)

	// --- API 服务 ---
	srv := api.New(cfg, st, cd, subsMgr, certs, log)

	// 后台维护任务。
	go maintenanceLoop(ctx, st, settings, log)
	go updateCheckLoop(ctx, st, cfg, log)

	log.Info("aegis-apid 已启动",
		"version", version.Version,
		"listen", cfg.API.Listen,
		"tls_listen", cfg.API.TLSListen,
		"domain", settings.Domain,
		"admin", cfg.API.Admin.Username,
	)

	if err := srv.Run(ctx); err != nil {
		log.Error("API 服务异常退出", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-apid 已退出")
}

func ptrSettings() *modelSettings {
	s := defaultSettings()
	return &s
}

// seedSettingsFromConfig 用 config.yaml 的值播种数据库里的全局设置。
//
// 为什么需要这一步：配置快照里的 defaults（含上游 DNS）完全来自数据库的
// global settings，而数据库的出厂默认是 1.1.1.1 / 8.8.8.8 / dns.google ——
// 在国内服务器上这几个上游常常不可达，表现为「服务起来了但解析全超时」。
// 部署者在 config.yaml 里写的 upstreams 必须能生效。
//
// 播种规则：只在数据库里还没有该键时写入，之后以数据库（管理后台）为准。
// 这样既能用配置文件做初始值，又不会在管理员改过设置后被重启覆盖掉。
func seedSettingsFromConfig(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	set, err := st.GetSettings(ctx)
	if err != nil {
		return err
	}
	changed := false

	if set.Domain == "" && cfg.Domain != "" {
		set.Domain = cfg.Domain
		changed = true
	}

	// upstreams 在 meta 里以逗号分隔存一行，直接查该键是否存在。
	if v, err := st.GetMeta(ctx, "setting.upstreams"); err == nil && v == "" && len(cfg.Upstreams) > 0 {
		set.Upstreams = cfg.Upstreams
		changed = true
		log.Info("已用 config.yaml 的 upstreams 初始化全局上游", "upstreams", cfg.Upstreams)
	}

	if set.UpdateRepo == "" && cfg.Update.Repo != "" {
		set.UpdateRepo = cfg.Update.Repo
		changed = true
	}
	if cfg.Update.Channel != "" {
		if v, err := st.GetMeta(ctx, "setting.update_channel"); err == nil && v == "" {
			set.UpdateChannel = cfg.Update.Channel
			changed = true
		}
	}

	if !changed {
		return nil
	}
	return st.SaveSettings(ctx, set)
}

// maintenanceLoop 每天清理过期统计与日志（文档 8.4）。
func maintenanceLoop(ctx context.Context, st *store.Store, settings *modelSettings, log *slog.Logger) {
	// 启动 5 分钟后先跑一次，之后每 24 小时一次。
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s, err := st.GetSettings(ctx)
			if err != nil {
				s = settings
			}
			// query_log 总量上限按「每条日志约 200 字节」估算，
			// 默认 100 万条 ≈ 200MB，对小型部署足够。
			rows, err := st.Cleanup(ctx, 90, s.LogRetentionD, 1000000)
			if err != nil {
				log.Warn("清理过期数据失败", "err", err)
			} else {
				log.Info("清理过期数据完成", "deleted", rows)
			}
			timer.Reset(24 * time.Hour)
		}
	}
}

// updateCheckLoop 每 6 小时检查一次新版本（文档 14.1）。
//
// 只写库不自动升级：升级必须由管理员在面板上点确认，
// 静默自动升级会让「服务突然变了行为」变得无法归因。
func updateCheckLoop(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) {
	run := func() {
		s, err := st.GetSettings(ctx)
		if err != nil {
			return
		}
		repo := s.UpdateRepo
		if repo == "" {
			repo = cfg.Update.Repo
		}
		if repo == "" {
			return
		}
		channel := s.UpdateChannel
		if channel == "" {
			channel = cfg.Update.Channel
		}
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()

		res, err := selfupdate.Check(cctx, repo, "aegis-apid", channel, version.Version)
		if err != nil {
			log.Debug("检查更新失败", "err", err)
			return
		}
		if res.UpdateAvailable {
			log.Info("发现新版本", "current", res.Current, "latest", res.Latest)
		}
	}

	// 启动 1 分钟后检查一次，然后每 6 小时一次。
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
		run()
	}
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// runChecks 做启动前自检。
func runChecks(cfg *config.Config) int {
	fmt.Println("aegis-apid 启动前自检")
	fmt.Println(strings.Repeat("-", 52))

	fail := 0

	fmt.Printf("%-30s %s\n", "数据目录", cfg.DataDir)
	fmt.Printf("%-30s %s\n", "数据库", cfg.DBPath())
	fmt.Printf("%-30s %s\n", "配置快照", cfg.SnapshotPath())

	if cfg.Domain == "" {
		fmt.Printf("%-30s ⚠ 未配置（需在管理后台填写主域名）\n", "主域名")
	} else {
		fmt.Printf("%-30s %s\n", "主域名", cfg.Domain)
	}

	if err := cfg.ValidateForAPI(); err != nil {
		fmt.Printf("%-30s ✗ %v\n", "API 配置", err)
		fail++
	} else {
		fmt.Printf("%-30s ✓\n", "API 配置")
	}

	certFile, keyFile := cfg.ResolveCert()
	cm := certmgr.New(certFile, keyFile, cfg.DNS.DevTLS, slog.Default())
	if err := cm.Load(); err != nil {
		fmt.Printf("%-30s ⚠ %v\n", "证书", err)
		fmt.Println("     HTTPS 面板将不可用；HTTP 端口与 API 不受影响")
	} else {
		days := int(time.Until(time.Unix(cm.NotAfter(), 0)).Hours() / 24)
		mark := "✓"
		if days < 14 {
			mark = "⚠"
		}
		fmt.Printf("%-30s %s 剩余 %d 天\n", "证书", mark, days)
	}

	// 端口冲突检查：最常见的是把 api.tls_listen 配成 443，与 dnsd 的 DoH 撞车。
	if strings.HasSuffix(cfg.API.TLSListen, ":443") && cfg.API.TLSListen != "" {
		fmt.Printf("%-30s ✗ api.tls_listen=%s 与 dnsd 的 DoH 端口冲突\n", "端口冲突", cfg.API.TLSListen)
		fmt.Println("     建议改用 :8443")
		fail++
	} else if cfg.API.TLSListen != "" {
		fmt.Printf("%-30s ✓ %s\n", "HTTPS 面板端口", cfg.API.TLSListen)
	}

	fmt.Println(strings.Repeat("-", 52))
	if fail > 0 {
		fmt.Printf("结果：%d 项失败\n", fail)
		return 1
	}
	fmt.Println("结果：全部通过")
	return 0
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
