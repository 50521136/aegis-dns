package dns

import (
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// negativeSOA 是用于 NXDOMAIN 应答的合成 SOA。
//
// 作用有两个：一是让部分客户端（尤其是 Windows）接受这是权威的「不存在」，
// 二是提供一个负缓存 TTL，避免客户端反复重试同一个被拦域名。
const negativeSOA = "aegis-dns. invalid. 1 3600 600 604800 60"

// buildBlockResponse 按配置的拦截模式构造应答（文档 4.4）。
//
// 四种模式：
//
//	nxdomain  —— RCODE=3 且带 SOA，语义正确但某些客户端会重试
//	null_ip   —— A 返回 0.0.0.0 / AAAA 返回 ::，连接立即失败、无重试（推荐）
//	refused   —— RCODE=5，明确拒绝，但部分客户端会降级到其它 DNS
//	custom_ip —— 返回自定义 IP，可指向内网陷阱主机做拦截统计
func buildBlockResponse(req *dns.Msg, mode, customIP string) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.RecursionAvailable = true
	resp.Compress = true

	if len(req.Question) == 0 {
		resp.Rcode = dns.RcodeRefused
		return resp
	}
	q := req.Question[0]

	switch mode {
	case model.BlockingNXDOMAIN:
		resp.Rcode = dns.RcodeNameError
		resp.Ns = []dns.RR{negativeSOARR(q.Name)}

	case model.BlockingRefused:
		resp.Rcode = dns.RcodeRefused

	case model.BlockingCustomIP:
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(customIP), "[]"))
		if ip == nil {
			// 配置了 custom_ip 但值非法：降级为 null_ip，绝不返回空应答，
			// 否则客户端会看到「域名存在但没有记录」而走更长的重试路径。
			ip = net.IPv4zero
		}
		appendBlockedIP(resp, q, ip)

	default: // model.BlockingNullIP
		appendBlockedIP(resp, q, net.IPv4zero)
	}
	return resp
}

// appendBlockedIP 按查询类型写入拦截 IP。
func appendBlockedIP(resp *dns.Msg, q dns.Question, ip net.IP) {
	switch q.Qtype {
	case dns.TypeA:
		v4 := ip.To4()
		if v4 == nil {
			v4 = net.IPv4zero.To4()
		}
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   v4,
		})
	case dns.TypeAAAA:
		v16 := ip.To16()
		if ip.To4() != nil {
			// custom_ip 给的是 IPv4，但查询是 AAAA：返回 :: 而不是把
			// IPv4 硬塞进 AAAA（那会产生非法记录）。
			v16 = net.IPv6zero.To16()
		}
		resp.Answer = append(resp.Answer, &dns.AAAA{
			Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
			AAAA: v16,
		})
	case dns.TypeHTTPS, dns.TypeSVCB:
		// HTTPS/SVCB 记录若返回 0.0.0.0 会干扰浏览器连接复用；
		// 返回 NODATA（无应答）是 AdGuard Home 的做法。
		resp.Rcode = dns.RcodeSuccess
	default:
		// 其它类型（MX/TXT/PTR...）返回 NODATA。
		resp.Rcode = dns.RcodeSuccess
	}
}

// negativeSOARR 构造负缓存用的 SOA 记录。
func negativeSOARR(qname string) dns.RR {
	rr, err := dns.NewRR(qname + " 60 IN SOA " + negativeSOA)
	if err != nil {
		// 合成的 SOA 解析失败极不可能发生；返回 nil 由调用方容忍。
		return nil
	}
	return rr
}

// refusedResponse 构造未识别请求在 refused 策略下的应答。
func refusedResponse(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeRefused)
	resp.RecursionAvailable = true
	return resp
}

// servfailResponse 构造 SERVFAIL 应答（上游全挂 / panic 兜底）。
func servfailResponse(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeServerFailure)
	resp.RecursionAvailable = true
	return resp
}

// rewriteResponse 用预构建的改写记录构造应答。
//
// 改写记录在规则构建期就已生成（文档 5.5），这里只做一次拷贝，
// 查询路径上零分配。
func rewriteResponse(req *dns.Msg, rrs []dns.RR) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.RecursionAvailable = true
	resp.Compress = true
	if len(req.Question) == 0 {
		return resp
	}
	q := req.Question[0]

	for _, rr := range rrs {
		h := rr.Header()
		// 记录类型必须与查询类型匹配，否则客户端会忽略它。
		// AAAA 查询命中 A 改写规则时返回 NODATA，保持记录类型语义完整
		// （文档 5.5「部分改写的处理」）。
		if h.Rrtype != q.Qtype {
			continue
		}
		cp := dns.Copy(rr)
		cp.Header().Name = q.Name
		resp.Answer = append(resp.Answer, cp)
	}
	return resp
}
