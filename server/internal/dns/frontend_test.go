package dns

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testLogger 返回一个写到 io.Discard 的 logger，避免测试输出噪音。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestFrontend 建一个指向 h 的反代，白名单为 hosts。
func newTestFrontend(t *testing.T, h http.Handler, hosts ...string) *frontendProxy {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	f, err := newFrontendProxy(srv.URL, hosts, testLogger())
	if err != nil {
		t.Fatalf("newFrontendProxy: %v", err)
	}
	if f == nil {
		t.Fatal("newFrontendProxy 返回 nil，期望构造成功")
	}
	return f
}

// TestFrontendProxyDisabled 确认 upstream 为空时完全不启用（行为与引入本特性前一致）。
func TestFrontendProxyDisabled(t *testing.T) {
	f, err := newFrontendProxy("", []string{"adguardhome.lv10.ren"}, testLogger())
	if err != nil {
		t.Fatalf("期望无错误，得到 %v", err)
	}
	if f != nil {
		t.Fatal("upstream 为空时应返回 nil")
	}
}

// TestFrontendProxyConfigErrors 覆盖几种明显写错的配置。
func TestFrontendProxyConfigErrors(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		hosts    []string
	}{
		{"协议不支持", "ftp://127.0.0.1:3000", []string{"a.example"}},
		{"缺主机名", "http://", []string{"a.example"}},
		{"没有白名单", "http://127.0.0.1:3000", nil},
		{"白名单全是空串", "http://127.0.0.1:3000", []string{"", "  "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := newFrontendProxy(c.upstream, c.hosts, testLogger()); err == nil {
				t.Fatal("期望返回错误，得到 nil")
			}
		})
	}
}

// TestFrontendAllowsHost 是本特性的核心：**只有白名单里的域名能打开前端**。
//
// 租户子域 <client_id>.<domain> 必须被拒 —— 那些名字是给 DoH 客户端用的，
// 让它们也能打开管理后台等于把控制台入口摊到每个租户名下。
func TestFrontendAllowsHost(t *testing.T) {
	f, err := newFrontendProxy("http://127.0.0.1:3000", []string{"adguardhome.lv10.ren"}, testLogger())
	if err != nil {
		t.Fatalf("newFrontendProxy: %v", err)
	}

	cases := []struct {
		name  string
		host  string
		sni   string
		allow bool
	}{
		{"裸域名", "adguardhome.lv10.ren", "", true},
		{"大写", "AdGuardHome.LV10.REN", "", true},
		{"带端口", "adguardhome.lv10.ren:443", "", true},
		{"结尾带点", "adguardhome.lv10.ren.", "", true},
		{"租户子域", "lz0423.adguardhome.lv10.ren", "", false},
		{"租户子域大写", "LZ0423.AdGuardHome.lv10.ren", "", false},
		{"无关域名", "evil.example.com", "", false},
		{"裸 IP", "123.60.82.151", "", false},
		{"空 Host", "", "", false},
		{"SNI 命中", "adguardhome.lv10.ren", "adguardhome.lv10.ren", true},
		// Host 写成裸域名、但握手用的是租户 SNI：Host 头可以随便伪造，
		// SNI 才是握手阶段固定下来的判据，这种情况必须拒。
		{"Host 伪造 + 租户 SNI", "adguardhome.lv10.ren", "lz0423.adguardhome.lv10.ren", false},
		{"Host 命中 + 无关 SNI", "adguardhome.lv10.ren", "evil.example.com", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = c.host
			if c.sni != "" {
				r.TLS = &tls.ConnectionState{ServerName: c.sni}
			}
			if got := f.allows(r); got != c.allow {
				t.Fatalf("allows() = %v，期望 %v（Host=%q SNI=%q）", got, c.allow, c.host, c.sni)
			}
		})
	}
}

// TestFrontendAllowsNilProxy 确认未启用时 allows 安全返回 false（不 panic）。
func TestFrontendAllowsNilProxy(t *testing.T) {
	var f *frontendProxy
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "adguardhome.lv10.ren"
	if f.allows(r) {
		t.Fatal("nil frontendProxy 不应放行")
	}
}

// TestHandleRootRoutes 验证根路径的分流：白名单 Host 转控制台，其余回 DoH 提示。
func TestHandleRootRoutes(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 反代必须保留原始 Host：apid 按 Host 判定同源、生成 CSP 与 Cookie 域。
		_, _ = io.WriteString(w, "PANEL host="+r.Host+
			" xff="+r.Header.Get("X-Forwarded-For")+
			" xfproto="+r.Header.Get("X-Forwarded-Proto")+
			" path="+r.URL.Path)
	})
	f := newTestFrontend(t, backend, "adguardhome.lv10.ren")

	s := &Server{log: testLogger(), frontend: f}

	t.Run("白名单 Host 走控制台", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/some/page", nil)
		r.Host = "adguardhome.lv10.ren"
		w := httptest.NewRecorder()
		s.handleRoot(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", w.Code)
		}
		body := w.Body.String()
		if !strings.HasPrefix(body, "PANEL") {
			t.Fatalf("响应体 = %q，期望来自后端", body)
		}
		if !strings.Contains(body, "host=adguardhome.lv10.ren") {
			t.Fatalf("Host 未透传：%q", body)
		}
		if !strings.Contains(body, "path=/some/page") {
			t.Fatalf("路径未透传：%q", body)
		}
		if !strings.Contains(body, "xff=") || strings.Contains(body, "xff= ") {
			t.Fatalf("X-Forwarded-For 未设置：%q", body)
		}
	})

	t.Run("租户子域只做 DoH", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "lz0423.adguardhome.lv10.ren"
		w := httptest.NewRecorder()
		s.handleRoot(w, r)

		if w.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404", w.Code)
		}
		if !strings.Contains(w.Body.String(), "这是 DoH 端点") {
			t.Fatalf("响应体 = %q，期望 DoH 提示", w.Body.String())
		}
	})

	t.Run("未启用反代时维持原提示", func(t *testing.T) {
		plain := &Server{log: testLogger()}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "adguardhome.lv10.ren"
		w := httptest.NewRecorder()
		plain.handleRoot(w, r)

		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "这是 DoH 端点") {
			t.Fatalf("状态码=%d 响应体=%q，期望 404 + DoH 提示", w.Code, w.Body.String())
		}
	})
}

// TestHandleRootProxyError 确认控制台挂掉时返回 502 而不是把 dnsd 拖垮。
func TestHandleRootProxyError(t *testing.T) {
	// 起一个立刻关掉的上游，制造连接被拒。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	f, err := newFrontendProxy(addr, []string{"adguardhome.lv10.ren"}, testLogger())
	if err != nil {
		t.Fatalf("newFrontendProxy: %v", err)
	}

	s := &Server{log: testLogger(), frontend: f}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "adguardhome.lv10.ren"
	w := httptest.NewRecorder()
	s.handleRoot(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", w.Code)
	}
}

// TestDoHQueryNotProxied 确认 /dns-query 仍然归 DoH，不会被根 handler 抢走。
//
// 这是整个方案的前提：路径分流一旦把 /dns-query 也转给控制台，
// 所有 DoH 客户端立刻全挂。
func TestDoHQueryNotProxied(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "PANEL")
	})
	f := newTestFrontend(t, backend, "adguardhome.lv10.ren")
	s := &Server{log: testLogger(), frontend: f}

	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", s.handleDoH)
	mux.HandleFunc("/dns-query/", s.handleDoH)
	mux.HandleFunc("/", s.handleRoot)

	r := httptest.NewRequest(http.MethodGet, "/dns-query", nil)
	r.Host = "adguardhome.lv10.ren"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	body := w.Body.String()
	if strings.Contains(body, "PANEL") {
		t.Fatal("/dns-query 被反代到了控制台，DoH 会全挂")
	}
	// 没带 dns 参数时 DoH handler 回 400，足以证明请求落到了 DoH 而不是控制台。
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（DoH handler 的缺参数响应）", w.Code)
	}
}
