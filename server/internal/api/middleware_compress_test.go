package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 这个文件锁住压缩中间件的边界条件。
//
// 压缩写错不会报错，只会「看起来正常但某些客户端坏了」，所以每条
// 反直觉的规则都要有断言：小响应不压、Range 不压、Content-Length 必须删、
// 不支持 gzip 的客户端拿到原文。

// compressServer 造一个只挂了压缩中间件的测试 handler。
func compressServer(h http.HandlerFunc) http.Handler {
	s := &Server{}
	return s.middlewareCompress(h)
}

// TestCompressLargeJSON 断言大 JSON 被压缩，且 Content-Length 被删掉。
//
// Content-Length 忘了删是最典型的错误：客户端按旧长度读取，
// 表现是响应被截断或一直挂住。
func TestCompressLargeJSON(t *testing.T) {
	big := map[string]string{"data": strings.Repeat("a", 4096)}
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, big)
	})

	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("大响应应被 gzip 压缩，实际 Content-Encoding=%q", got)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		t.Fatalf("压缩后必须删除 Content-Length，实际 %q", cl)
	}
	if rec.Header().Get("Vary") == "" {
		t.Fatal("必须声明 Vary: Accept-Encoding，否则中间缓存会发错版本")
	}

	// 解压后必须能还原成合法 JSON
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应不是合法 gzip: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解压后不是合法 JSON: %v", err)
	}
	if out["data"] != big["data"] {
		t.Fatal("解压后内容与原文不一致")
	}
}

// TestCompressSmallResponseUntouched 断言小响应原样返回。
//
// 短 JSON 压完反而更大（gzip 头尾约 20 字节），而且会让客户端
// 多一次解压开销。这条没有断言的话，很容易「顺手全压」。
func TestCompressSmallResponseUntouched(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("小响应不该压缩，实际 Content-Encoding=%q", got)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("小响应被破坏了: %v", err)
	}
	if out["ok"] != "1" {
		t.Fatal("小响应内容不对")
	}
}

// TestCompressHonorsAcceptEncoding 断言客户端不支持时返回原文。
func TestCompressHonorsAcceptEncoding(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("a", 4096) + `"}`))
	})
	req := httptest.NewRequest("GET", "/api/v1/x", nil) // 不带 Accept-Encoding
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("客户端不支持 gzip 时不能压缩，实际 %q", got)
	}
	if rec.Body.Len() < 4000 {
		t.Fatalf("应返回完整原文，实际只有 %d 字节", rec.Body.Len())
	}
}

// TestCompressSkipsRangeRequests 断言 Range 请求完全不碰。
//
// 一旦压缩，Content-Range / Content-Length 语义就乱了，
// 断点续传和媒体播放会坏 —— 这类故障极难归因到压缩。
func TestCompressSkipsRangeRequests(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(bytes.Repeat([]byte("a"), 4096))
	})
	req := httptest.NewRequest("GET", "/assets/x.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-4095")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Range 请求不能被压缩，实际 %q", got)
	}
}

// TestCompressSkipsImages 断言已压缩类型不压。
func TestCompressSkipsImages(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x89, 0x50}, 4000))
	})
	req := httptest.NewRequest("GET", "/x.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("PNG 不该被压缩，实际 %q", got)
	}
}

// TestCompressSkipsNoContent 断言 204/304 不被压。
func TestCompressSkipsNoContent(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		h := compressServer(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		})
		req := httptest.NewRequest("GET", "/api/v1/x", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%d 不该有 Content-Encoding，实际 %q", code, got)
		}
		if rec.Code != code {
			t.Errorf("状态码被改坏了：期望 %d，实际 %d", code, rec.Code)
		}
	}
}

// TestCompressKeepsStatus 断言压缩不改变状态码。
func TestCompressKeepsStatus(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + strings.Repeat("x", 4096) + `"}`))
	})
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码被改坏了：%d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("错误响应体够大时也该压缩")
	}
}

// TestCompressExplicitContentLengthSmall 断言声明了小体积的不压。
func TestCompressExplicitContentLengthSmall(t *testing.T) {
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(100))
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	})
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("声明 100 字节的响应不该压缩，实际 %q", got)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "100" {
		t.Fatalf("未压缩时必须保留 Content-Length，实际 %q", cl)
	}
}

// TestAcceptsGzip 断言 Accept-Encoding 解析。
func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                    false,
		"gzip":                true,
		"gzip, deflate, br":   true,
		"deflate":             false,
		"br":                  false,
		"*":                   true,
		"gzip;q=0":            false,
		"gzip;q=0.8, deflate": true,
		"identity":            false,
	}
	for ae, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if ae != "" {
			r.Header.Set("Accept-Encoding", ae)
		}
		if got := acceptsGzip(r); got != want {
			t.Errorf("Accept-Encoding=%q 期望 %v，实际 %v", ae, want, got)
		}
	}
}

// TestCompressible 断言类型白名单。
func TestCompressible(t *testing.T) {
	yes := []string{
		"text/html; charset=utf-8", "application/json; charset=utf-8",
		"text/javascript", "application/javascript", "image/svg+xml",
		"application/xml",
	}
	for _, ct := range yes {
		if !compressible(ct) {
			t.Errorf("%q 应判定为可压缩", ct)
		}
	}
	no := []string{
		"image/png", "image/jpeg", "video/mp4", "font/woff2",
		"application/zip", "application/octet-stream", "application/pdf",
	}
	for _, ct := range no {
		if compressible(ct) {
			t.Errorf("%q 不该判定为可压缩", ct)
		}
	}
}
