package dns

import (
	"encoding/binary"
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

// 本文件实现 EDNS Client Subnet（RFC 7871）与本地缓存的共存。
//
// # 为什么需要这一层
//
// ECS 让上游按「客户端所在网段」返回就近节点，所以同一个域名在不同网段
// 可能拿到**不同答案**。缓存键必须能区分网段，否则会把 A 网段的答案发给
// B 网段：CDN 就近失效，某些场景下答案本身就是错的（境外节点、走绕行链路）。
//
// 早期的实现是「带 ECS 的请求整个不进缓存」——正确，但代价极大：只要开了
// ECS，带 EDNS 的客户端（Android DoT、浏览器、iOS 全都带）100% 绕过缓存。
// 线上实测命中率从 50% 掉到 1%，缓存条目数从打满 30000 掉到 42。
//
// **这不是 ECS 的固有代价，是实现偷懒。** dnsproxy / AdGuard Home 用的是
// 「网段进缓存键 + 最长前缀匹配」：subnet 单独存一层，查找时从精确掩码
// 逐位回落到 /0。
//
// # 两层的分工（读写各最多两次查找，保持 O(1)）
//
//	cacheShard.items     主键层 {UserID,QName,QType,DO}：拦截、改写应答，
//	                     以及「适用范围为全网」的正常应答（上游 scope=0）
//	cacheShard.subItems  网段层 {主键, ecsKey}：答案确实依赖网段的正常应答
//
// 拦截／改写应答由本地规则构造，从没问过上游，与网段毫无关系 —— 它们必须
// 留在主键层，否则「开 ECS 就把拦截也不缓存」，等于把重复率最高的一类流量
// （占线上 32~48%）白白丢掉。
//
// # 为什么不把答案挂到祖先网段上
//
// 一个 /24 的答案往上挂到 /16，同 /16 里另一个 /24 的客户端就会命中它 ——
// 那正是「就近失效」本身。所以只存精确网段；ancestor 回落只用于「命中
// 更粗的、真正声明了粗 scope 的条目」，而那条路径由主键层承担（scope=0）。
//
// # 上游谎报 scope 的防御
//
// 实测 223.5.5.5 对**不带** ECS 的请求会自己合成一个 ECS 网段（返回上海
// 节点），且响应里的 scope 段照抄了它合成的那个非零值 —— 而 scope 本应由
// 权威服务器声明。所以「上游声称 scope 非零」不能用来放大适用范围，
// 只允许精确网段复用；只有 scope 真正为 0（明确表示答案与网段无关）时
// 才写进主键层共享。

// ecsKey 是一条 ECS 网段的紧凑表示，用作网段层的缓存键。
//
// 为什么不用 net.IPNet：缓存键要参与 map 查找，而 net.IPNet 内部是切片，
// 不能直接当 map key（转字符串则每次查询多一次分配）。这里全是定长标量，
// 可直接做 map key 且不产生堆分配 —— 每条带 ECS 的查询都要走这条路径。
type ecsKey struct {
	// addr 是掩码后的网络号（IPv4 存低 32 位，IPv6 存前 64 位）。
	addr uint64
	// bits 是掩码长度。
	bits uint8
	// ipv6 区分地址族：同一串字节在 v4/v6 下含义不同，不能混。
	ipv6 bool
}

// ecsKeyFromPrefix 由 netip.Prefix 构造缓存键。
func ecsKeyFromPrefix(p netip.Prefix) (k ecsKey, ok bool) {
	p = p.Masked()
	a := p.Addr()
	if a.Is4() || a.Is4In6() {
		b := a.As4()
		return ecsKey{
			addr: uint64(binary.BigEndian.Uint32(b[:])),
			bits: uint8(p.Bits()),
		}, true
	}
	if !a.Is6() {
		return ecsKey{}, false
	}
	b := a.As16()
	return ecsKey{
		addr: binary.BigEndian.Uint64(b[:8]),
		bits: uint8(p.Bits()),
		ipv6: true,
	}, true
}

// ecsKeyFromSUBNET 由报文里的 EDNS0_SUBNET 选项构造缓存键。
func ecsKeyFromSUBNET(sub *dns.EDNS0_SUBNET) (k ecsKey, ok bool) {
	if sub == nil {
		return ecsKey{}, false
	}
	ip := net.IP(sub.Address)
	switch sub.Family {
	case 1:
		v4 := ip.To4()
		if v4 == nil {
			return ecsKey{}, false
		}
		bits := int(sub.SourceNetmask)
		if bits < 0 || bits > 32 {
			bits = 32
		}
		return ecsKeyFromPrefix(netip.PrefixFrom(netip.AddrFrom4([4]byte(v4)), bits))
	case 2:
		v6 := ip.To16()
		if v6 == nil {
			return ecsKey{}, false
		}
		bits := int(sub.SourceNetmask)
		if bits < 0 || bits > 128 {
			bits = 128
		}
		return ecsKeyFromPrefix(netip.PrefixFrom(netip.AddrFrom16([16]byte(v6)), bits))
	}
	return ecsKey{}, false
}

// subnetFromWriter 由客户端来源地址构造要注入上游的 ECS 选项。
//
// IPv4 用 /24、IPv6 用 /56：ECS 的目的只是让 CDN 判断大致区域，不需要
// 精确定位到单个设备 —— 给得太细等于把用户的完整地址泄漏给上游。
//
// 回环、内网、未指定地址一律返回 nil（本地机器和局域网没有「就近」概念，
// 注入一个假网段只会让上游返回无关节点）。
func subnetFromWriter(w dns.ResponseWriter) *dns.EDNS0_SUBNET {
	if w == nil {
		return nil
	}
	ip := remoteIP(w.RemoteAddr())
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return nil
	}
	sub := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceScope: 0}
	if v4 := ip.To4(); v4 != nil {
		sub.Family = 1
		sub.SourceNetmask = 24
		sub.Address = v4.Mask(net.CIDRMask(24, 32))
		return sub
	}
	sub.Family = 2
	sub.SourceNetmask = 56
	sub.Address = ip.Mask(net.CIDRMask(56, 128))
	return sub
}

// subnetFromMsg 取出报文里已有的 ECS 选项（客户端自带的情形）。
func subnetFromMsg(req *dns.Msg) *dns.EDNS0_SUBNET {
	if req == nil {
		return nil
	}
	opt := req.IsEdns0()
	if opt == nil {
		return nil
	}
	for _, o := range opt.Option {
		if sub, ok := o.(*dns.EDNS0_SUBNET); ok {
			return sub
		}
	}
	return nil
}

// responseScope 读应答里回显的 ECS scope。
//
// 返回值 sent 表示上游有没有回显 ECS；scope 是它声明的适用范围
// （0 = 与网段无关，可全网共享）。
//
// 只在「回显的网段与我们发出去的一致」时才采信 —— RFC 7871 §7.3 要求
// 不一致时丢弃应答，我们这里保守地降级为「不可共享」，不做丢弃。
func responseScope(resp *dns.Msg, sent *dns.EDNS0_SUBNET) (scope int, echoed bool) {
	if resp == nil || sent == nil {
		return 0, false
	}
	sub := subnetFromMsg(resp)
	if sub == nil || sub.Family != sent.Family || sub.SourceNetmask != sent.SourceNetmask {
		return 0, false
	}
	if !net.IP(sub.Address).Equal(sent.Address) {
		return 0, false
	}
	return int(sub.SourceScope), true
}

// formatECS 把 ECS 选项整理成日志里显示的 "1.2.3.0/24"。
func formatECS(sub *dns.EDNS0_SUBNET) string {
	if sub == nil {
		return ""
	}
	addr := net.IP(sub.Address).String()
	if addr == "<nil>" {
		return ""
	}
	return addr + "/" + itoa(int(sub.SourceNetmask))
}

// sharedSubnet 决定一条正常解析的应答该写进缓存的哪一层。
//
// 返回 nil = 写主键层（与网段无关，所有客户端共享）；非 nil = 写网段层。
//
// 判据只有一条：**上游在应答里把 scope 回显成 0**。
//
// 为什么不能只看「有没有注入 ECS」：注入了不等于答案会变。同一个 CDN 后面
// 挂着全局一致的记录时，上游回 scope=0 就是明确告诉我们「这个答案对任何
// 网段都一样」，此时按网段分层纯属浪费容量与命中率。
//
// 为什么不能采信「非零 scope 就把答案挂到那个粗网段上」：实测 223.5.5.5
// 对**不带** ECS 的请求会自己合成一个 ECS 网段（返回上海节点），响应里的
// scope 段照抄了它合成的那个值 —— 而 scope 本应由权威服务器声明。
// 照抄上游的非零 scope 去放大适用范围，等于把 A 网段的答案发给 B 网段，
// 这正是本次要修的问题。所以非零 scope 一律降级成「只按客户端实际网段存」。
func sharedSubnet(resp *dns.Msg, sent *dns.EDNS0_SUBNET, cacheECS *ecsKey) *ecsKey {
	if cacheECS == nil {
		// 这次查询没有网段身份：答案必然进主键层。
		return nil
	}
	scope, echoed := responseScope(resp, sent)
	if !echoed {
		// 上游没回显 ECS（有些上游吃掉选项不回）。最保守：只让同一网段复用，
		// 绝不跨网段。
		return cacheECS
	}
	if scope == 0 {
		// 明确声明「与网段无关」→ 主键层，所有客户端共享。
		return nil
	}
	// 非零 scope：不信它声明的那份粗网段，仍按客户端实际网段存。
	return cacheECS
}
