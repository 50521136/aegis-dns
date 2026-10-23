package netproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 这个文件提供「代理到底通不通」的实测能力。
//
// 为什么需要：管理员填了代理之后，订阅仍然失败时完全无法判断是哪一边的问题 ——
// 代理地址写错、代理本身不通、代理通了但订阅源挡它、还是压根该走直连。
// 以前只能靠猜，或者去服务器上手工 curl。
//
// 探测同时测两条路径（走代理 / 直连），并给出可读结论：只有把两边结果放在
// 一起看，才能区分「代理坏了」和「代理没问题、是订阅源的事」。

// ProbeResult 是一条链路的探测结果。
type ProbeResult struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	// Via 标明这次探测实际走的路径：proxy 或 direct。
	Via string `json:"via"`
}

// 探测用的目标。
//
// 选两个而不是一个，是因为它们失败的含义不同：
//   - generate_204 只证明「这条链路能出网」，最轻量；
//   - GitHub raw 是订阅源最典型的形态，能通就说明拉订阅大概率没问题。
//
// 不用 HEAD：部分 CDN 对 HEAD 返回 405，会把「能通」误判成「不通」。
var (
	// ProbeTargetConnectivity 只验证链路能否出网。
	ProbeTargetConnectivity = "https://www.gstatic.com/generate_204"
	// ProbeTargetSubscribe 是订阅源典型地址（GitHub raw，内容极小）。
	ProbeTargetSubscribe = "https://raw.githubusercontent.com/github/gitignore/main/Go.gitignore"
)

// probeTimeout 是单条探测的超时。
//
// 8 秒是权衡：直连被墙的地址通常要等到 TCP 超时才报错（默认可能 30 秒以上），
// 但管理员在设置页点「测试」时不愿意等那么久。8 秒足够判断一条链路可用性，
// 又不会让按钮转太久。
const probeTimeout = 8 * time.Second

// Probe 用指定代理（raw 为空表示直连）请求 target，返回耗时与结果。
//
// 不返回 error：探测失败本身就是一种结果（要展示给管理员），
// 用 error 表达会让调用方每次都要把 err 转成字符串。
func Probe(ctx context.Context, raw, target string, timeout time.Duration) ProbeResult {
	via := "direct"
	if raw != "" {
		via = "proxy"
	}
	res := ProbeResult{Via: via}

	tr, err := Transport(raw, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if timeout <= 0 {
		timeout = probeTimeout
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: tr,
		// 探测不跟重定向：跳转次数多通常意味着被劫持或认证页，
		// 那对「这条链路能不能用」的判断是干扰项。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		res.Error = fmt.Sprintf("构造请求失败: %v", err)
		return res
	}
	req.Header.Set("User-Agent", "aegis-dns/probe")

	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = humanizeProbeError(err)
		return res
	}
	defer resp.Body.Close()
	// 读掉一点点内容，确保连接真的建立了（有些代理会先回响应头再卡住）。
	_, _ = io.CopyN(io.Discard, resp.Body, 512)

	res.Status = resp.StatusCode
	// 204 与 2xx/3xx 都算通：能拿到 HTTP 响应就说明链路可用。
	// 4xx 也算通 —— 例如订阅源对探测 UA 回 403，那是源站策略，不是代理坏了。
	if resp.StatusCode < 500 {
		res.OK = true
		return res
	}
	res.Error = fmt.Sprintf("目标返回 HTTP %d", resp.StatusCode)
	return res
}

// humanizeProbeError 把底层错误换成人能判断的说法。
//
// 原始错误（dial tcp: lookup xxx: i/o timeout / socks connect tcp ...: connection
// refused）对排查有用，但管理员第一眼需要知道的是「哪一类问题」。
func humanizeProbeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "i/o timeout", "context deadline exceeded", "Timeout"):
		return "连接超时（地址不可达或被拦截）：" + msg
	case containsAny(msg, "connection refused"):
		return "连接被拒绝（代理端口没在监听，或地址/端口写错）：" + msg
	case containsAny(msg, "no such host"):
		return "域名解析失败（代理主机名写错，或本机 DNS 有问题）：" + msg
	case containsAny(msg, "connection reset"):
		return "连接被重置（链路被中断，常见于中间设备干扰）：" + msg
	case containsAny(msg, "authentication", "407", "401"):
		return "代理需要认证（用户名密码不对或未填）：" + msg
	case containsAny(msg, "socks"):
		return "SOCKS 握手失败（协议不匹配，例如把 socks5 端口当 http 用）：" + msg
	}
	return msg
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
