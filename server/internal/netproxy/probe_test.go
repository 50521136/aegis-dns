package netproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 这个文件锁住「代理探测」的判定口径。
//
// 探测结果会被翻成「下一步做什么」给管理员看，判错方向比不显示更糟：
// 把「源站 403」说成「代理坏了」，管理员会去换一个本来没问题的代理。

// TestProbeDirectOK 断言直连可用时能被判为可用。
func TestProbeDirectOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res := Probe(context.Background(), "", srv.URL, 3*time.Second)
	if !res.OK {
		t.Fatalf("直连应判为可用，实际 error=%q", res.Error)
	}
	if res.Via != "direct" {
		t.Fatalf("via 应为 direct，实际 %q", res.Via)
	}
	if res.Status != http.StatusNoContent {
		t.Fatalf("状态码应为 204，实际 %d", res.Status)
	}
}

// TestProbeTreats4xxAsReachable 断言 4xx 算「链路通」。
//
// 订阅源对探测 UA 回 403 是源站策略，不是代理坏了。把这种情况判成「代理不通」
// 会让管理员去换代理，问题永远解决不了。
func TestProbeTreats4xxAsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	res := Probe(context.Background(), "", srv.URL, 3*time.Second)
	if !res.OK {
		t.Fatalf("4xx 说明链路是通的，应判为可用，实际 error=%q", res.Error)
	}
	if res.Status != http.StatusForbidden {
		t.Fatalf("状态码应为 403，实际 %d", res.Status)
	}
}

// TestProbe5xxIsNotOK 断言 5xx 判为不可用（源站自己挂了）。
func TestProbe5xxIsNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	res := Probe(context.Background(), "", srv.URL, 3*time.Second)
	if res.OK {
		t.Fatal("5xx 应判为不可用")
	}
	if !strings.Contains(res.Error, "502") {
		t.Fatalf("错误信息应带状态码，实际 %q", res.Error)
	}
}

// TestProbeInvalidProxy 断言代理地址写错时立刻给出可读原因，而不是等超时。
func TestProbeInvalidProxy(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"1.2.3.4:7890", "协议前缀"},
		{"ftp://1.2.3.4:21", "不支持"},
		{"http://", "主机名"},
	}
	for _, c := range cases {
		res := Probe(context.Background(), c.raw, ProbeTargetConnectivity, time.Second)
		if res.OK {
			t.Errorf("%q 不该判为可用", c.raw)
		}
		if !strings.Contains(res.Error, c.want) {
			t.Errorf("%q 的错误应包含 %q，实际 %q", c.raw, c.want, res.Error)
		}
		if res.Via != "proxy" {
			t.Errorf("%q 的 via 应为 proxy，实际 %q", c.raw, res.Via)
		}
	}
}

// TestProbeRefusedProxy 断言「端口没开」被翻译成人话。
//
// 这是最常见的一种配置错误：代理地址对，但服务没起或端口写错。
func TestProbeRefusedProxy(t *testing.T) {
	// 关掉的 httptest server 的地址，连接必然被拒。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	res := Probe(context.Background(), addr, ProbeTargetConnectivity, 2*time.Second)
	if res.OK {
		t.Fatal("连接被拒时不该判为可用")
	}
	if !strings.Contains(res.Error, "连接被拒绝") && !strings.Contains(res.Error, "连接超时") {
		t.Fatalf("应翻译成可读原因，实际 %q", res.Error)
	}
}

// TestProbeTimeout 断言超时被翻译成人话，且不会拖过 deadline。
func TestProbeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	start := time.Now()
	res := Probe(context.Background(), "", srv.URL, 300*time.Millisecond)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatal("超时不该判为可用")
	}
	if !strings.Contains(res.Error, "连接超时") {
		t.Fatalf("应翻译成超时原因，实际 %q", res.Error)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("探测应受 timeout 约束，实际耗时 %v", elapsed)
	}
}
