package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这个文件锁住「API 响应不许被缓存」这条规则。
//
// 线上真实事故：源站 /api/v1/me 没有任何 Cache-Control，前置 CDN
// 按默认规则缓存了 1 小时，未登录访客拿到了上一个登录用户的完整
// 资料。这类问题不会报错、不会 5xx，只会「随机看到别人的账号」，
// 所以必须有断言守着。

// noStoreServer 造一个只挂了 no-store 中间件的测试 handler。
func noStoreServer(h http.HandlerFunc) http.Handler {
	s := &Server{}
	return s.middlewareNoStore(h)
}

// TestNoStoreOnAPI 断言 API 路径带齐 no-store 三件套。
func TestNoStoreOnAPI(t *testing.T) {
	h := noStoreServer(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"username": "admin"})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me", nil))

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Fatalf("API 响应必须含 no-store，实际 Cache-Control=%q", cc)
	}
	if !strings.Contains(cc, "private") {
		t.Fatalf("API 响应必须含 private（禁止共享缓存），实际 %q", cc)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("HTTP/1.0 代理只看 Pragma，实际 %q", got)
	}
	if got := rec.Header().Get("Expires"); got != "0" {
		t.Fatalf("Expires 必须为 0，实际 %q", got)
	}
}

// TestNoStoreVaryCredentials 断言 Vary 按凭证分桶。
//
// no-store 是第一道防线；万一某层缓存忽略它，Vary 保证 A 的响应
// 不会被喂给 B。
func TestNoStoreVaryCredentials(t *testing.T) {
	h := noStoreServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me", nil))

	vary := strings.Join(rec.Header().Values("Vary"), ",")
	for _, want := range []string{"Authorization", "Cookie"} {
		if !strings.Contains(vary, want) {
			t.Fatalf("Vary 必须含 %s，实际 %q", want, vary)
		}
	}
}

// TestNoStoreCoversEarlyErrors 断言早期返回（401/429/404）也带 no-store。
//
// 401 恰恰是最容易被缓存的那种响应 —— 体积小、无凭证差异时看起来
// 「人人一样」，CDN 很乐意存它。
func TestNoStoreCoversEarlyErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusNotFound} {
		code := code
		h := noStoreServer(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, code, CodeUnauthorized, "x", nil)
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me", nil))
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("状态码 %d 也必须 no-store，实际 %q", code, rec.Header().Get("Cache-Control"))
		}
	}
}

// TestNoStoreSkipsStatic 断言静态资源不受影响。
//
// index.html 需要 no-cache（可缓存但每次校验），assets/* 需要长缓存；
// 如果被 no-store 覆盖，前端每次刷新都要重新下载，白屏问题会复发。
func TestNoStoreSkipsStatic(t *testing.T) {
	h := noStoreServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/index-abc123.js", nil))

	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("静态资源不应被 no-store 覆盖，实际 %q", got)
	}
	if rec.Header().Get("Vary") != "" {
		t.Fatalf("静态资源不该被塞 Vary，实际 %q", rec.Header().Get("Vary"))
	}
}

// TestAddVaryIdempotent 断言 Vary 不重复追加。
func TestAddVaryIdempotent(t *testing.T) {
	hdr := http.Header{}
	addVary(hdr, "Origin")
	addVary(hdr, "origin") // 大小写不同也算重复
	addVary(hdr, "Accept-Encoding")
	addVary(hdr, "Accept-Encoding")

	joined := strings.Join(hdr.Values("Vary"), ",")
	if n := strings.Count(strings.ToLower(joined), "accept-encoding"); n != 1 {
		t.Fatalf("Vary 重复追加了 %d 次：%q", n, joined)
	}
	if n := strings.Count(strings.ToLower(joined), "origin"); n != 1 {
		t.Fatalf("Vary 大小写不同也应去重，实际 %q", joined)
	}
}
