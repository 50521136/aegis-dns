package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/50521136/aegis-dns/server/internal/auth"
	"github.com/50521136/aegis-dns/server/internal/model"
)

// ctxKey 是中间件向 handler 传值的私有键类型。
//
// 用自定义类型而不是字符串常量：字符串键可能被别的包用同样的字面量
// 覆盖（vet 也会告警）。
type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyRequestID
)

// currentUser 取出当前登录用户。
func currentUser(r *http.Request) *model.User {
	v, _ := r.Context().Value(ctxKeyUser).(*model.User)
	return v
}

// requestID 取出请求 ID。
func requestID(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyRequestID).(string)
	return v
}

// === 中间件 ===

// middlewareRecover 捕获 panic 并返回 500（文档 9.9 第 1 层）。
//
// 没有这一层的话，任何一个 handler 的空指针都会让 apid 进程退出，
// 而 apid 同时托管前端 —— 页面直接 404。
func (s *Server) middlewareRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("API panic", "panic", rec, "path", r.URL.Path, "request_id", requestID(r))
				writeError(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// middlewareRequestID 生成或透传 X-Request-ID。
func (s *Server) middlewareRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err == nil {
				id = hex.EncodeToString(b)
			} else {
				id = "unknown"
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder 记录响应状态码与字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// middlewareLogger 输出结构化访问日志。
func (s *Server) middlewareLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		// 静态资源不记日志：前端一次加载几十个 chunk，会把日志冲掉。
		if strings.HasPrefix(r.URL.Path, "/api/") || rec.status >= 400 {
			s.log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"took_ms", time.Since(start).Milliseconds(),
				"ip", clientIP(r, s.cfg.API.TrustedProxies),
				"request_id", requestID(r),
			)
		}
	})
}

// middlewareCORS 处理跨域（文档 9.9 第 4 层）。
//
// 默认同源（前端由 apid 内嵌托管，本就不跨域）。只有显式配置了
// api.cors_origins 才放开 —— 这是「默认安全」的写法。
func (s *Server) middlewareCORS(next http.Handler) http.Handler {
	allowed := s.cfg.API.CORSOrigins
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		ok := false
		for _, a := range allowed {
			if a == "*" || strings.EqualFold(a, origin) {
				ok = true
				break
			}
		}
		if ok && origin != "" {
			if allowed[0] == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// 带 Origin 的响应必须声明 Vary，否则会被缓存串味。
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ipLimiter 是单 IP 令牌桶集合。
type ipLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipEntry
	rate     rate.Limit
	burst    int
}

type ipEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(perMinute, burst int) *ipLimiter {
	if perMinute <= 0 {
		perMinute = 600
	}
	if burst <= 0 {
		burst = 120
	}
	l := &ipLimiter{
		limiters: map[string]*ipEntry{},
		rate:     rate.Limit(float64(perMinute) / 60.0),
		burst:    burst,
	}
	go l.gcLoop()
	return l
}

// gcLoop 定期清理闲置的限流器，避免 map 随访问 IP 无限增长。
func (l *ipLimiter) gcLoop() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-30 * time.Minute)
		l.mu.Lock()
		for k, v := range l.limiters {
			if v.seen.Before(cutoff) {
				delete(l.limiters, k)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	e, ok := l.limiters[ip]
	if !ok {
		e = &ipEntry{lim: rate.NewLimiter(l.rate, l.burst)}
		l.limiters[ip] = e
	}
	e.seen = time.Now()
	lim := e.lim
	l.mu.Unlock()
	return lim.Allow()
}

// middlewareRateLimit 是 API 层按 IP 的限流（文档 9.9 第 5 层）。
func (s *Server) middlewareRateLimit(next http.Handler) http.Handler {
	if s.ipLimiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 健康检查不受限流影响：监控系统必须能随时探活。
		if r.URL.Path == "/api/v1/healthz" || r.URL.Path == "/api/v1/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.ipLimiter.allow(clientIP(r, s.cfg.API.TrustedProxies)) {
			writeError(w, http.StatusTooManyRequests, CodeRateLimited, "请求过于频繁，请稍后再试", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// middlewareAuth 校验 Authorization 头（文档 9.9 第 6 层）。
//
// 同时接受 JWT access token 与 PAT：两者都是 Bearer，
// 通过前缀区分 —— JWT 是三段点分结构，PAT 以 agx_ 开头。
func (s *Server) middlewareAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "缺少 Authorization: Bearer 令牌", nil)
			return
		}

		var (
			user *model.User
			err  error
		)
		if strings.HasPrefix(token, "agx_") {
			// 长期 API Token 路径。
			uid, lerr := s.store.LookupPAT(r.Context(), auth.HashPAT(token))
			if lerr != nil {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "API Token 无效或已撤销", nil)
				return
			}
			user, err = s.store.GetUserByID(r.Context(), uid)
		} else {
			claims, cerr := s.tokens.ParseAccess(token)
			if cerr != nil {
				// 区分「过期」与「无效」：前端只对过期触发静默刷新。
				code := CodeUnauthorized
				msg := "访问令牌无效"
				if strings.Contains(cerr.Error(), "expired") || strings.Contains(cerr.Error(), "exp") {
					code = CodeTokenExpired
					msg = "访问令牌已过期"
				}
				writeError(w, http.StatusUnauthorized, code, msg, nil)
				return
			}
			user, err = s.store.GetUserByID(r.Context(), claims.UserID)
		}

		if err != nil {
			writeStoreError(w, err, "用户不存在")
			return
		}
		if !user.Enabled {
			writeError(w, http.StatusForbidden, CodeForbidden, "账号已被禁用", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user)))
	})
}

// middlewareAdmin 要求管理员角色（文档 9.9 第 7 层）。
//
// 非 admin 访问 /admin/* 会被记审计日志：这类访问往往是权限探测的前兆。
func (s *Server) middlewareAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil || u.Role != model.RoleAdmin {
			uid := ""
			if u != nil {
				uid = u.ID
			}
			s.store.LogAudit(r.Context(), uid, "admin_forbidden", r.Method+" "+r.URL.Path, clientIP(r, s.cfg.API.TrustedProxies))
			writeError(w, http.StatusForbidden, CodeForbidden, "需要管理员权限", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken 从 Authorization 头提取令牌。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// clientIP 还原客户端 IP。
//
// 只有在来源地址命中 trusted_proxies 时才采信 X-Forwarded-For ——
// 否则任何客户端都能伪造该头来绕过按 IP 的限流。
func clientIP(r *http.Request, trusted []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return host
	}
	if !ipInCIDRs(net.ParseIP(host), trusted) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	// 取最左侧的原始客户端地址。
	first := strings.TrimSpace(strings.Split(xff, ",")[0])
	if net.ParseIP(first) == nil {
		return host
	}
	return first
}

func ipInCIDRs(ip net.IP, cidrs []string) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			if net.ParseIP(c) != nil && net.ParseIP(c).Equal(ip) {
				return true
			}
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}
