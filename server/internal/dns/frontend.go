package dns

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// frontendProxy 是 443 上「/dns-query 以外」的请求去往控制台的通道。
//
// 为什么需要它：443 由 dnsd 独占（DoH 自己终止 TLS，架构红线 C4），
// 而控制台跑在 apid 的另一个端口上。想让 https://<domain> 直接打开控制台，
// 只有两条路 —— 要么前置一个反代抢走 443（多一个组件、DoH3 会废），
// 要么由 dnsd 自己按路径分流。AdGuard Home 用的是后者：同一个端口、
// 同一张证书、同一个 mux，/dns-query 归 DoH，其余归 Web UI。
//
// 与 AdGuard Home 的唯一差别在 Host 判定：AGH 是「任意 Host 都能打开后台」，
// 这里改成**只认白名单里的 Host**。原因是本系统有租户子域
// （<client_id>.<domain>），那些名字是给 DoH 客户端用的，让它们也能打开
// 管理后台既没有意义，又把控制台入口摊到每个租户名下。
type frontendProxy struct {
	proxy *httputil.ReverseProxy
	// hosts 是允许打开前端的 Host 白名单（已归一：小写、无尾点）。
	hosts map[string]struct{}
}

// newFrontendProxy 按配置构造反代。
//
// upstream 为空时返回 (nil, nil)：调用方据此保持 443 根路径原来的
// 「这是 DoH 端点」提示，行为与未引入本特性时完全一致。
func newFrontendProxy(upstream string, hosts []string, log *slog.Logger) (*frontendProxy, error) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return nil, nil
	}

	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("frontend_upstream 不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("frontend_upstream 只支持 http/https，当前是 %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("frontend_upstream 缺少主机名: %q", upstream)
	}

	allowed := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = normalizeHost(h)
		if h != "" {
			allowed[h] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("启用了 frontend_upstream 但 frontend_hosts 为空，且 domain 未配置")
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			// SetURL 会把 Out.Host 改成上游的 host:port，这里改回客户端原本的
			// Host —— apid 按 Host 判定同源、生成 CSP 与 Cookie 域，看到
			// 127.0.0.1:3000 会让会话与安全头全部错位。
			pr.Out.Host = pr.In.Host
			// SetXForwarded 是「覆盖」而不是「追加」，正好把客户端自带的
			// X-Forwarded-* 顶掉：dnsd 是链路最前端，这些头只能由它写。
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// 控制台挂了不能影响解析：只记日志 + 502，DoH 那条路完全独立。
			log.Error("前端反代失败", "path", r.URL.Path, "upstream", u.String(), "err", err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "控制台暂时不可用。\n")
		},
	}

	return &frontendProxy{proxy: rp, hosts: allowed}, nil
}

// allows 判断这个请求是否应当转给控制台。
//
// 两道关都要过：
//   - Host 必须命中白名单（浏览器地址栏里的域名）
//   - 有 SNI 时 SNI 也必须命中（TLS 握手时协商的名字）
//
// 只查 Host 是不够的：Host 是请求头，客户端想写什么就写什么 —— 用租户子域
// 的 SNI 建连、再把 Host 改成裸域名，就能绕过「只有指定域名能打开前端」。
// SNI 在握手阶段就固定了，改不了，所以它才是真正的判据。
//
// SNI 为空（直接按 IP 建连、或极老的客户端）时只认 Host：那种情况下
// Host 就是 IP，本身过不了白名单，不需要额外分支。
func (f *frontendProxy) allows(r *http.Request) bool {
	if f == nil {
		return false
	}
	host := normalizeHost(r.Host)
	if host == "" {
		return false
	}
	if _, ok := f.hosts[host]; !ok {
		return false
	}
	if r.TLS != nil && r.TLS.ServerName != "" {
		if _, ok := f.hosts[normalizeHost(r.TLS.ServerName)]; !ok {
			return false
		}
	}
	return true
}
