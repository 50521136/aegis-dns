package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/50521136/aegis-dns/server/internal/api/webdist"
	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/certmgr"
	"github.com/50521136/aegis-dns/server/internal/config"
	"github.com/50521136/aegis-dns/server/internal/coord"
	"github.com/50521136/aegis-dns/server/internal/store"
	"github.com/50521136/aegis-dns/server/internal/subsub"
	"github.com/50521136/aegis-dns/server/internal/version"
)

// Server 是 apid 的 HTTP 服务。
type Server struct {
	cfg    *config.Config
	store  *store.Store
	coord  *coord.Coordinator
	subs   *subsub.Manager
	tokens *auth.TokenService
	certs  *certmgr.Manager
	log    *slog.Logger

	startedAt time.Time
	ipLimiter *ipLimiter

	httpSrv  *http.Server
	tlsSrv   *http.Server
	staticFS fs.FS
}

// New 创建 API 服务。
func New(
	cfg *config.Config,
	st *store.Store,
	cd *coord.Coordinator,
	subs *subsub.Manager,
	certs *certmgr.Manager,
	log *slog.Logger,
) *Server {
	s := &Server{
		cfg:       cfg,
		store:     st,
		coord:     cd,
		subs:      subs,
		tokens:    auth.NewTokenService(cfg.API.JWTSecret, "aegis-dns"),
		certs:     certs,
		log:       log,
		startedAt: time.Now(),
		ipLimiter: newIPLimiter(600, 120),
	}
	if sub, err := webdist.FS(); err == nil {
		s.staticFS = sub
	} else {
		log.Warn("前端静态资源不可用，/ 将返回提示页", "err", err)
	}
	return s
}

// Handler 构建路由。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// 中间件链顺序见文档 9.9：Recover 必须最外层，
	// 否则内层中间件自己的 panic 无人接管。
	r.Use(s.middlewareRecover)
	r.Use(s.middlewareRequestID)
	r.Use(s.middlewareLogger)
	r.Use(s.middlewareCORS)
	r.Use(s.middlewareRateLimit)

	r.Route("/api/v1", func(r chi.Router) {
		// --- 公开接口 ---
		r.Get("/healthz", s.handleHealthz)
		r.Get("/readyz", s.handleReadyz)
		r.Get("/version", s.handleVersion)
		r.Get("/docs", s.handleDocs)

		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", s.handleRegister)
			r.Post("/login", s.handleLogin)
			r.Post("/refresh", s.handleRefresh)
			r.Post("/logout", s.handleLogout)
		})

		// --- 需要认证 ---
		r.Group(func(r chi.Router) {
			r.Use(s.middlewareAuth)

			r.Get("/me", s.handleMe)
			r.Put("/me/password", s.handleChangePassword)
			r.Get("/me/dns-config", s.handleDNSConfig)
			r.Get("/me/export", s.handleExport)
			r.Post("/me/import", s.handleImport)
			r.Get("/me/tokens", s.handleListTokens)
			r.Post("/me/tokens", s.handleCreateToken)
			r.Delete("/me/tokens/{id}", s.handleDeleteToken)
			r.Get("/me/ips", s.handleListMyIPs)
			r.Post("/me/ips", s.handleAddMyIP)
			r.Delete("/me/ips", s.handleDeleteMyIP)

			r.Get("/rules", s.handleListRules)
			r.Post("/rules", s.handleCreateRule)
			r.Post("/rules/import", s.handleImportRules)
			r.Post("/rules/validate", s.handleValidateRule)
			r.Delete("/rules", s.handleBulkDeleteRules)
			r.Put("/rules/{id}", s.handleUpdateRule)
			r.Delete("/rules/{id}", s.handleDeleteRule)

			r.Get("/subscriptions", s.handleListSubs)
			r.Post("/subscriptions", s.handleCreateSub)
			r.Get("/subscriptions/presets", s.handlePresets)
			r.Post("/subscriptions/refresh-all", s.handleRefreshAllSubs)
			r.Put("/subscriptions/{id}", s.handleUpdateSub)
			r.Delete("/subscriptions/{id}", s.handleDeleteSub)
			r.Post("/subscriptions/{id}/refresh", s.handleRefreshSub)

			r.Get("/stats/summary", s.handleStatsSummary)
			r.Get("/stats/timeseries", s.handleStatsTimeseries)
			r.Get("/stats/top", s.handleStatsTop)
			r.Get("/stats/querylog", s.handleQueryLog)

			r.Get("/update/check", s.handleUpdateCheck)

			// --- 管理员 ---
			r.Route("/admin", func(r chi.Router) {
				r.Use(s.middlewareAdmin)
				r.Get("/users", s.handleAdminListUsers)
				r.Put("/users/{id}", s.handleAdminUpdateUser)
				r.Delete("/users/{id}", s.handleAdminDeleteUser)
				r.Get("/settings", s.handleAdminGetSettings)
				r.Put("/settings", s.handleAdminUpdateSettings)
				r.Post("/snapshot/rebuild", s.handleAdminRebuildSnapshot)
				r.Get("/system", s.handleAdminSystem)
				r.Post("/update/apply", s.handleAdminApplyUpdate)
				r.Get("/audit", s.handleAdminAudit)
			})
		})
	})

	// 其余路径交给内嵌前端（SPA）。
	r.NotFound(s.serveSPA)

	return r
}

// Run 启动 HTTP 与 HTTPS 监听。
//
// 两个监听都要起：HTTP 供本机/内网与反代使用，HTTPS（默认 :8443）
// 给面板用 —— 因为 443 已经被 dnsd 的 DoH 占用。
func (s *Server) Run(ctx context.Context) error {
	handler := s.Handler()

	s.httpSrv = &http.Server{
		Addr:              s.cfg.API.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		s.log.Info("API 已启动（HTTP）", "addr", s.cfg.API.Listen, "version", version.Version)
		if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP 监听失败: %w", err)
		}
	}()

	if s.cfg.API.TLSListen != "" {
		if !s.certs.Available() {
			s.log.Warn("没有可用证书，跳过 HTTPS 面板监听", "addr", s.cfg.API.TLSListen)
		} else {
			tlsCfg := s.certs.TLSConfig()
			// 面板不需要 ALPN 的 dot 协议。
			tlsCfg.NextProtos = []string{"h2", "http/1.1"}
			s.tlsSrv = &http.Server{
				Addr:              s.cfg.API.TLSListen,
				Handler:           handler,
				TLSConfig:         tlsCfg,
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
				WriteTimeout:      120 * time.Second,
			}
			go func() {
				s.log.Info("API 已启动（HTTPS 面板）", "addr", s.cfg.API.TLSListen)
				if err := s.tlsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errCh <- fmt.Errorf("HTTPS 监听失败: %w", err)
				}
			}()
		}
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var errs []error
		if s.httpSrv != nil {
			errs = append(errs, s.httpSrv.Shutdown(shutdownCtx))
		}
		if s.tlsSrv != nil {
			errs = append(errs, s.tlsSrv.Shutdown(shutdownCtx))
		}
		return errors.Join(errs...)
	case err := <-errCh:
		return err
	}
}

// serveSPA 提供内嵌前端。
//
// 单页应用的核心：任何未命中静态文件的路径都回落到 index.html，
// 否则用户刷新 /app/rules 会拿到 404（浏览器直接请求该路径，
// 服务端并不知道这是前端路由）。
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	// API 路径落到这里说明路由没匹配上，返回标准 404 而不是 HTML。
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, CodeNotFound, "接口不存在", nil)
		return
	}
	// 只处理 GET/HEAD。
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	if s.staticFS == nil {
		s.writeNoFrontend(w)
		return
	}

	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	rel := strings.TrimPrefix(clean, "/")

	// 命中真实文件就直接返回（带强缓存，因为文件名含内容哈希）。
	if rel != "" {
		if f, err := s.staticFS.Open(rel); err == nil {
			st, serr := f.Stat()
			f.Close()
			if serr == nil && !st.IsDir() {
				if strings.HasPrefix(rel, "assets/") {
					// 内容哈希命名的资源：一年不可变。
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
				return
			}
		}
	}

	// 回落 index.html。必须 no-cache，否则用户会一直拿到旧版本引用的
	// 已删除 chunk，表现为白屏。
	data, err := fs.ReadFile(s.staticFS, "index.html")
	if err != nil {
		s.writeNoFrontend(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 安全头：CSP 限制脚本来源为自身，挡住注入的第三方脚本。
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// writeNoFrontend 在二进制没有内嵌前端时给出可操作的提示。
func (s *Server) writeNoFrontend(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<title>DNSForge</title><style>body{background:#0B1020;color:#E5E7EB;font:15px/1.7 Inter,"PingFang SC",system-ui,sans-serif;
display:grid;place-items:center;min-height:100vh;margin:0}div{max-width:560px;padding:32px;background:#141A2E;
border:1px solid #1E293B;border-radius:16px}code{background:#0B1020;padding:2px 6px;border-radius:6px;color:#A5B4FC}</style>
</head><body><div><h2 style="margin:0 0 8px">前端未内嵌</h2>
<p>apid 正在运行，但当前二进制里没有前端构建产物。</p>
<p>API 可用：<code>/api/v1/healthz</code>、<code>/api/v1/version</code></p>
<p>要带上前端：<code>cd web &amp;&amp; pnpm install &amp;&amp; pnpm build</code>，把 <code>web/dist</code> 内容拷到
<code>server/internal/api/webdist/dist/</code> 后重新编译 apid。</p>
<p style="color:#94A3B8;font-size:13px">DNS 解析（dnsd）与此无关，不受影响。</p></div></body></html>`))
}

// ListenAddrs 返回实际监听的地址（自检用）。
func (s *Server) ListenAddrs() []string {
	out := []string{s.cfg.API.Listen}
	if s.cfg.API.TLSListen != "" {
		out = append(out, s.cfg.API.TLSListen)
	}
	return out
}

// tlsAvailable 判断能否提供 HTTPS 面板。
func (s *Server) tlsAvailable() bool { return s.certs != nil && s.certs.Available() }

// ensureNoConflict 检查监听地址是否与 DNS 端口冲突。
//
// 这是部署时最容易踩的坑：把 api.tls_listen 也配成 :443，
// 结果 dnsd 先占了端口，apid 起不来，日志只显示 bind: address already in use。
func (s *Server) ensureNoConflict() error {
	dnsPorts := map[string]string{
		portOf(s.cfg.DNS.ListenDOH): "dnsd 的 DoH",
		portOf(s.cfg.DNS.ListenDOT): "dnsd 的 DoT",
		portOf(s.cfg.DNS.ListenUDP): "dnsd 的 UDP DNS",
	}
	for _, addr := range s.ListenAddrs() {
		if who, ok := dnsPorts[portOf(addr)]; ok && who != "" {
			return fmt.Errorf("api 监听地址 %s 与 %s 端口冲突，请改用其它端口", addr, who)
		}
	}
	return nil
}

func portOf(addr string) string {
	if addr == "" {
		return ""
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return p
}

// unusedTLSImport 保证 crypto/tls 被引用（TLSConfig 来自 certmgr，
// 但显式保留导入以便未来在此直接构造 tls.Config）。
var _ = tls.VersionTLS12
