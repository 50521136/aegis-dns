package subsub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这个文件锁住「代理优先、直连兜底」的行为。
//
// 背景：代理和直连各自能通一部分订阅源。只走代理时，代理一挂订阅就彻底拉不动，
// 而很多源直连其实能通；只走直连时，GitHub 这类源在国内机房完全不通。两条路
// 都握着、按顺序尝试，才不会被单点拖死。

// TestFetchFallsBackToDirectWhenProxyFails 断言代理网络层失败时回退直连。
func TestFetchFallsBackToDirectWhenProxyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("example.com\nads.example.net\n"))
	}))
	defer srv.Close()

	f := NewFetcherWithTimeout(3 * time.Second)
	// 指向一个必然连不上的代理端口：代理这条路网络层失败。
	if err := f.SetProxy("http://127.0.0.1:1"); err != nil {
		t.Fatalf("设置代理失败: %v", err)
	}

	res, err := f.Fetch(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("代理不通时应回退直连并成功，实际失败: %v", err)
	}
	if !strings.Contains(string(res.Content), "ads.example.net") {
		t.Fatalf("回退直连后应拿到内容，实际 %q", string(res.Content))
	}
}

// TestFetchProxyFirstWhenWorking 断言代理可用时优先走代理。
//
// 用 httptest 起一个极简 HTTP 代理，确认请求确实经过了它 —— 只断言「拉取成功」
// 无法区分是走了代理还是走了兜底。
func TestFetchProxyFirstWhenWorking(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct-content\n"))
	}))
	defer target.Close()

	var proxied atomic.Int64
	// 极简代理：收到任何请求就返回代理侧的内容（不真的转发），
	// 这样只要拿到 proxy-content 就证明请求走了代理。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		_, _ = w.Write([]byte("proxy-content\n"))
	}))
	defer proxy.Close()

	f := NewFetcherWithTimeout(3 * time.Second)
	if err := f.SetProxy(proxy.URL); err != nil {
		t.Fatalf("设置代理失败: %v", err)
	}

	res, err := f.Fetch(context.Background(), target.URL, "", "")
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if proxied.Load() == 0 {
		t.Fatal("代理可用时必须优先走代理")
	}
	if !strings.Contains(string(res.Content), "proxy-content") {
		t.Fatalf("应拿到代理侧内容，实际 %q", string(res.Content))
	}
}

// TestFetchNoFallbackOnHTTPError 断言 4xx/5xx 不回退。
//
// 拿到 HTTP 状态码说明链路是通的，换条路不会变好；而且回退会让真实原因
// （源站 403/404）被后一条路的错误盖掉，排查方向直接被带偏。
func TestFetchNoFallbackOnHTTPError(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer target.Close()

	var proxyHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer proxy.Close()

	f := NewFetcherWithTimeout(3 * time.Second)
	if err := f.SetProxy(proxy.URL); err != nil {
		t.Fatalf("设置代理失败: %v", err)
	}

	_, err := f.Fetch(context.Background(), target.URL, "", "")
	if err == nil {
		t.Fatal("404 应报错")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("错误里应保留真实状态码，实际 %q", err.Error())
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("404 只该请求代理一次（不回退、不重试），实际 %d 次", proxyHits.Load())
	}
}

// TestProxyRoundCannotStarveFallback 断言代理卡死时直连仍能拿到时间。
//
// 这是实测踩到的坑：代理轮如果把整个超时耗尽，直连兜底就永远轮不上 ——
// 表现出来是订阅一直显示「同步中」，直到 300 秒超时才报错。所以开代理时
// 把预算对半分，代理最多用一半。
func TestProxyRoundCannotStarveFallback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct-content\n"))
	}))
	defer target.Close()

	// 一个只接受连接但永不响应的「黑洞代理」。
	blackhole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blackhole.Close()

	// 总预算 2 秒：对半分后代理最多用 1 秒，直连还有 1 秒可用。
	f := NewFetcherWithTimeout(2 * time.Second)
	if err := f.SetProxy(blackhole.URL); err != nil {
		t.Fatalf("设置代理失败: %v", err)
	}

	start := time.Now()
	res, err := f.Fetch(context.Background(), target.URL, "", "")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("代理黑洞时直连兜底应成功，实际失败: %v（耗时 %v）", err, elapsed)
	}
	if !strings.Contains(string(res.Content), "direct-content") {
		t.Fatalf("应拿到直连内容，实际 %q", string(res.Content))
	}
	if elapsed > 1900*time.Millisecond {
		t.Fatalf("兜底必须留出时间，总耗时不该逼近预算上限，实际 %v", elapsed)
	}
}

// TestSetProxyEmptyRestoresDirect 断言清空代理后彻底回到直连。
func TestSetProxyEmptyRestoresDirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct-content\n"))
	}))
	defer target.Close()

	f := NewFetcherWithTimeout(3 * time.Second)
	if err := f.SetProxy("http://127.0.0.1:1"); err != nil {
		t.Fatalf("设置代理失败: %v", err)
	}
	if err := f.SetProxy(""); err != nil {
		t.Fatalf("清空代理失败: %v", err)
	}
	if f.Proxy() != "" {
		t.Fatalf("清空后 Proxy() 应为空，实际 %q", f.Proxy())
	}
	if f.proxyClient != nil {
		t.Fatal("清空后不该保留代理客户端，否则仍会先试代理白等一轮超时")
	}

	res, err := f.Fetch(context.Background(), target.URL, "", "")
	if err != nil {
		t.Fatalf("直连拉取失败: %v", err)
	}
	if !strings.Contains(string(res.Content), "direct-content") {
		t.Fatalf("应拿到直连内容，实际 %q", string(res.Content))
	}
}

// TestSetProxyRejectsBadURL 断言非法代理地址被拒绝且不改动现有设置。
func TestSetProxyRejectsBadURL(t *testing.T) {
	f := NewFetcherWithTimeout(3 * time.Second)
	if err := f.SetProxy("http://127.0.0.1:1080"); err != nil {
		t.Fatalf("设置合法代理失败: %v", err)
	}
	if err := f.SetProxy("1.2.3.4:7890"); err == nil {
		t.Fatal("缺少协议前缀应被拒绝")
	}
	// 拒绝之后原设置必须保持不变，否则一次手误就把可用配置弄没了。
	if f.Proxy() != "http://127.0.0.1:1080" {
		t.Fatalf("非法地址不该覆盖原有代理，实际 %q", f.Proxy())
	}
}
