package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 这组测试锁住 Accept-Encoding 的 qvalue 解析。
//
// 原来的实现用子串匹配（`Contains(part,"q=0") && !Contains(part,"q=0.")`），
// 在四种真实写法上判错，其中三种会把「明确拒绝」当成「接受」：
//
//	"gzip;q=0.00"  → 含 "q=0." 被误判为接受
//	"gzip; q=0.0"  → 同上（带空格是常见写法）
//	"gzip;Q=0"     → 大小写敏感，没匹配到
//
// 判错方向是「多压缩一次」：功能不坏，但违反了客户端的明确声明。
// 这类错误不会报错、不会告警，只能靠断言守住。

func TestAcceptsGzipQValue(t *testing.T) {
	cases := []struct {
		header string
		want   bool
		why    string
	}{
		// 基本形态
		{"gzip", true, "不带 q 参数表示 q=1"},
		{"GZIP", true, "编码名大小写不敏感"},
		{" gzip ", true, "两侧空白要裁掉"},
		{"", false, "没有头就不压"},

		// 明确的拒绝：这四种是原实现的漏判
		{"gzip;q=0", false, "q=0 表示拒绝"},
		{"gzip;q=0.0", false, "q=0.0 也是拒绝（原实现误判为接受）"},
		{"gzip;q=0.00", false, "q=0.00 也是拒绝（原实现误判为接受）"},
		{"gzip; q=0.0", false, "带空格也是拒绝（原实现误判为接受）"},
		{"gzip;Q=0", false, "参数名大小写不敏感（原实现漏判）"},
		{"gzip;q=0;", false, "尾随分号不影响判定"},

		// 接受
		{"gzip;q=0.5", true, "半量接受也算接受"},
		{"gzip;q=1", true, "q=1"},
		{"gzip;q=1.0", true, "q=1.0"},

		// 多编码组合
		{"br, gzip", true, "逗号分隔"},
		{"br, gzip;q=0", false, "只有 gzip 被拒就不压"},
		{"deflate, gzip;q=0.8", true, "gzip 在中间"},
		{"identity", false, "只有 identity 不压"},
		{"br", false, "只有 br 不压（我们只支持 gzip）"},

		// wildcard 兜底
		{"*", true, "通配且 q=1 → 可压"},
		{"*;q=0", false, "通配但 q=0 → 不压"},
		{"*;q=0.5", true, "通配 q>0 → 可压"},
		{"br, *;q=0.5", true, "通配兜底"},

		// 坏值：按 0 处理，宁可不压
		{"gzip;q=abc", false, "q 值非法按 0 处理"},
		{"gzip;q=", false, "空 q 值按 0 处理"},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		if c.header != "" {
			req.Header.Set("Accept-Encoding", c.header)
		}
		if got := acceptsGzip(req); got != c.want {
			t.Errorf("acceptsGzip(%q) = %v，期望 %v（%s）", c.header, got, c.want, c.why)
		}
	}
}

// TestCompressRespectsQZero 是端到端那条：q=0.0 的客户端必须拿到未压缩的原文。
//
// 只测纯函数不够 —— 中间件里还有「小响应不压」「Range 不压」等分支，
// 要在真实的响应链路上确认 q=0 真的没被压。
func TestCompressRespectsQZero(t *testing.T) {
	payload := map[string]string{"data": string(make([]byte, 4096))}
	h := compressServer(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, payload)
	})

	for _, ae := range []string{"gzip;q=0.0", "gzip; q=0.0", "gzip;Q=0", "gzip;q=0.00"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		req.Header.Set("Accept-Encoding", ae)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got == "gzip" {
			t.Errorf("Accept-Encoding=%q 明确拒绝 gzip，却仍然压缩了响应", ae)
		}
	}
}
