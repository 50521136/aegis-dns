package api

import (
	"strings"
)

// 日志对外展示的脱敏层。
//
// 背景：query_log.upstream 存的是上游的完整地址，而阿里/腾讯的个人版 DoH
// 地址里带账号标识（例如 https://909111-m2tdhgkjlcpife47.alidns.com/dns-query
// 里的 909111-m2tdhgkjlcpife47 就是订阅 ID）。这串东西直接暴露给用户，
// 等于把上游接入方的账号信息交出去 —— 它不属于用户自己的数据。
//
// 处理原则：按「谁在看」决定粒度，而不是在采集时一刀切。
//
//	用户看自己的日志  → 只给协议类型（"DoH" / "DoT" / "DoQ" / "UDP"）
//	管理员看任意日志  → 给完整地址（他要判断的是「主上游还是备用在答」）
//
// 为什么不在落库时就抹掉：日志一旦脱敏就不可逆，而排障经常需要定位
// 「是哪个上游出了问题」。存全量、展示时裁剪，两种需求都能满足。

// upstreamKindOf 把上游地址归类成对用户友好的协议名。
//
// 只暴露协议类型：用户需要知道「走的是加密的还是明文的」，
// 但不需要知道具体是哪台服务器，更不该看到接入账号。
func upstreamKindOf(addr string) string {
	a := strings.TrimSpace(addr)
	if a == "" {
		return ""
	}
	low := strings.ToLower(a)
	switch {
	case strings.HasPrefix(low, "quic://"):
		return "DoQ"
	case strings.HasPrefix(low, "tls://"):
		return "DoT"
	case strings.HasPrefix(low, "https://"):
		return "DoH"
	case strings.HasPrefix(low, "h3://"), strings.HasPrefix(low, "doh3://"):
		return "DoH(HTTP/3)"
	case strings.HasPrefix(low, "tcp://"):
		return "TCP"
	case strings.HasPrefix(low, "udp://"):
		return "UDP"
	}
	// 裸 IP / 裸域名都按 UDP 处理 —— 见 resolver.normalizeAddrs 的同款规则。
	if !strings.Contains(low, "://") {
		return "UDP"
	}
	return "其他"
}

// redactUpstream 返回给普通用户看的上游表示。
//
// 保留协议类型，丢掉主机与端口。空值原样返回，前端按空处理。
func redactUpstream(addr string) string {
	if strings.TrimSpace(addr) == "" {
		return ""
	}
	return upstreamKindOf(addr)
}

// visibleUpstream 按查看者是否为管理员决定返回完整地址还是脱敏值。
func visibleUpstream(addr string, isAdmin bool) string {
	if isAdmin {
		return addr
	}
	return redactUpstream(addr)
}
