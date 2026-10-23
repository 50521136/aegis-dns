// Package netproxy 按 URL 构造带代理的 HTTP Transport。
//
// 用途：在线更新（GitHub）与订阅拉取（公共规则源）。这两条链路在国内机房
// 经常完全不通，运维手里往往已经有 http/socks 代理，却没法让服务用上 ——
// 以前只能改系统级代理或 iptables，既影响其它进程又难回滚。
package netproxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Parse 校验并解析代理地址。
//
// 只接受 http / https / socks5 / socks5h 四种 scheme：
// 其它写法（例如裸 host:port）在 http.ProxyURL 里会被静默当成 http 代理，
// 而 socks 端口走 http 协议只会得到一堆看不懂的连接重置。
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// 先看有没有 scheme 再交给 url.Parse。
	//
	// 顺序很重要：`1.2.3.4:7890`（最常见的漏写协议前缀写法）在 url.Parse 里
	// 会报 "first path segment in URL cannot contain colon" —— 那句话对填表的人
	// 毫无指导意义。这里提前拦下来，直接告诉他该写成什么样。
	if !strings.Contains(raw, "://") {
		return nil, fmt.Errorf("代理地址缺少协议前缀，请写成 http://主机:端口 或 socks5://主机:端口")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("代理地址格式不正确: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	case "":
		return nil, fmt.Errorf("代理地址缺少协议前缀，请写成 http://主机:端口 或 socks5://主机:端口")
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q，仅支持 http / https / socks5", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("代理地址缺少主机名或端口")
	}
	return u, nil
}

// Transport 按代理地址返回一个 Transport；raw 为空时返回不带代理的 Transport。
func Transport(raw string, base *http.Transport) (*http.Transport, error) {
	u, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	var t *http.Transport
	if base != nil {
		t = base.Clone()
	} else {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	if u != nil {
		t.Proxy = http.ProxyURL(u)
	}
	return t, nil
}
