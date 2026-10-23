package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/netproxy"
)

// handleTestProxy 实测出口代理是否可用，并同时给出直连的对照结果。
//
// 为什么必须两条路都测：订阅失败时管理员只知道「失败了」，无法判断是代理写错、
// 代理本身不通、代理通了但源站挡它，还是压根该走直连。单独测代理只能得到
// 「代理不通」这一半结论，而「代理不通但直连能通」和「两条都不通」的处置
// 方式完全不同。
//
// 请求体：
//
//	{"proxy_url": "socks5://1.2.3.4:1080", "proxy_enabled": true}
//
// proxy_url 不传时用已保存的设置 —— 这样「保存后到底通不通」可以直接复验，
// 不用重新输一遍地址。
func (s *Server) handleTestProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyURL     *string `json:"proxy_url"`
		ProxyEnabled *bool   `json:"proxy_enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	raw := ""
	enabled := true
	if req.ProxyURL != nil {
		raw = strings.TrimSpace(*req.ProxyURL)
	} else if cur, err := s.store.GetSettings(r.Context()); err == nil {
		raw = strings.TrimSpace(cur.ProxyURL)
	}
	if req.ProxyEnabled != nil {
		enabled = *req.ProxyEnabled
	} else if req.ProxyURL == nil {
		if cur, err := s.store.GetSettings(r.Context()); err == nil {
			enabled = cur.ProxyEnabled
		}
	}

	if enabled && raw == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"reason": "已启用代理但没有填写代理地址",
		})
		return
	}
	// 先校验格式：地址写错时立刻给出明确提示，不必等 8 秒超时。
	if raw != "" {
		if _, err := netproxy.Parse(raw); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     false,
				"reason": err.Error(),
			})
			return
		}
	}

	// 两条路各测两个目标，并行跑：串行最坏要 4×8 秒，管理员会以为页面卡死。
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	type probeOut struct {
		key string
		res netproxy.ProbeResult
	}
	targets := []struct {
		name string
		url  string
	}{
		{"connectivity", netproxy.ProbeTargetConnectivity},
		{"subscribe", netproxy.ProbeTargetSubscribe},
	}

	outCh := make(chan probeOut, 4)
	for _, t := range targets {
		// 直连对照
		go func(name, url string) {
			outCh <- probeOut{"direct:" + name, netproxy.Probe(ctx, "", url, 0)}
		}(t.name, t.url)
		if raw != "" {
			go func(name, url string) {
				outCh <- probeOut{"proxy:" + name, netproxy.Probe(ctx, raw, url, 0)}
			}(t.name, t.url)
		}
	}

	results := make(map[string]netproxy.ProbeResult, 4)
	want := len(targets)
	if raw != "" {
		want = len(targets) * 2
	}
	for i := 0; i < want; i++ {
		o := <-outCh
		results[o.key] = o.res
	}

	proxyOK := false
	if raw != "" {
		// 只要有一个目标通，就说明代理这条链路是活的。
		for _, name := range []string{"connectivity", "subscribe"} {
			if results["proxy:"+name].OK {
				proxyOK = true
			}
		}
	}
	directOK := results["direct:connectivity"].OK || results["direct:subscribe"].OK

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"proxy_url":  raw,
		"enabled":    enabled,
		"proxy_ok":   proxyOK,
		"direct_ok":  directOK,
		"probes":     results,
		"advice":     proxyAdvice(enabled, raw, proxyOK, directOK),
		"tested_at":  time.Now().Format(time.RFC3339),
		"proxy_used": raw != "" && enabled,
	})
}

// proxyAdvice 把探测结果翻成一句能直接照做的结论。
//
// 这是整个功能的核心价值：管理员不想看四个探针的原始结果，只想知道
// 「问题出在哪、下一步做什么」。
func proxyAdvice(enabled bool, raw string, proxyOK, directOK bool) string {
	switch {
	case raw == "" && directOK:
		return "未配置代理，直连可用。无需代理。"
	case raw == "" && !directOK:
		return "未配置代理，且直连不通。建议配置一个可用代理（填好后点「测试代理」验证）。"
	case !enabled && proxyOK:
		return "代理地址可用，但当前未启用。开启后订阅拉取会优先走代理。"
	case !enabled && !proxyOK:
		return "代理未启用，且代理地址本身不通。订阅走直连" + map[bool]string{true: "，目前可用。", false: "，目前也不通，请先修代理。"}[directOK]
	case enabled && proxyOK && directOK:
		return "代理与直连都可用。订阅会优先走代理，代理失败时自动回退直连。"
	case enabled && proxyOK && !directOK:
		return "代理可用、直连不通。这正是代理该发挥作用的场景，保持启用即可。"
	case enabled && !proxyOK && directOK:
		return "代理不通，但直连可用。订阅会自动回退到直连，暂不影响使用；建议修正代理地址或关闭代理开关。"
	default:
		return "代理与直连都不通。请检查代理地址/端口，以及服务器本身的出网能力（DNS、防火墙、安全组）。"
	}
}
