package dns

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// 这个文件锁住「ECS 与缓存共存」的契约。
//
// 背景：v1.0.78 为了解决「带 ECS 的答案不能跨网段复用」，直接把带 ECS 的
// 请求踢出缓存（CacheKeyFrom 返回 cacheable=false，put 里 hasECS 早退）。
// 正确性问题解决了，代价是**开了 ECS 就等于关掉全部缓存** —— 线上命中率
// 从 50% 掉到 1%，缓存条目数从打满 30000 掉到 42。
//
// 现在的形态：网段进缓存的分层键，而不是把请求踢出缓存。
// 下面每组断言对应一条容易改错的性质。

func mkReq(t *testing.T, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	return m
}

func mkResp(t *testing.T, req *dns.Msg, ip string, ttl uint32) *dns.Msg {
	t.Helper()
	resp := new(dns.Msg)
	resp.SetReply(req)
	rr, err := dns.NewRR(req.Question[0].Name + " " + itoa(int(ttl)) + " IN A " + ip)
	if err != nil {
		t.Fatalf("构造 RR 失败: %v", err)
	}
	resp.Answer = []dns.RR{rr}
	return resp
}

func mustSubnet(t *testing.T, cidr string) ecsKey {
	t.Helper()
	k, ok := ecsKeyFromPrefix(netip.MustParsePrefix(cidr))
	if !ok {
		t.Fatalf("构造网段键失败: %s", cidr)
	}
	return k
}

// TestCacheECSSubnetIsolation 是这次修复的核心断言：
// 不同网段的同名查询拿到各自的答案，绝不互相命中。
//
// 这条一旦写坏，表现是「A 省的客户端拿到 B 省的 CDN 节点」——
// 功能不报错、延迟悄悄变差，很难发现。
func TestCacheECSSubnetIsolation(t *testing.T) {
	c := NewCache(256, 60, 3600)
	req := mkReq(t, "cdn.example.", dns.TypeA)
	key, ok := CacheKeyFrom("u1", req)
	if !ok {
		t.Fatal("构造缓存键失败")
	}

	subA := mustSubnet(t, "36.1.94.0/24")
	subB := mustSubnet(t, "183.219.232.0/24")

	c.PutWithStampECS(key, mkResp(t, req, "59.36.46.63", 300), req, false, c.Stamp("u1"), &subA)
	c.PutWithStampECS(key, mkResp(t, req, "117.162.52.192", 300), req, false, c.Stamp("u1"), &subB)

	gotA, hit, _, _, _ := c.GetWithECS(key, req, &subA)
	if !hit {
		t.Fatal("A 网段应命中自己写进去的条目")
	}
	if ip := gotA.Answer[0].(*dns.A).A.String(); ip != "59.36.46.63" {
		t.Fatalf("A 网段拿到了别人的答案: %s", ip)
	}

	gotB, hit, _, _, _ := c.GetWithECS(key, req, &subB)
	if !hit {
		t.Fatal("B 网段应命中自己写进去的条目")
	}
	if ip := gotB.Answer[0].(*dns.A).A.String(); ip != "117.162.52.192" {
		t.Fatalf("B 网段拿到了别人的答案: %s", ip)
	}

	// 第三个网段不该命中任何人的条目。
	subC := mustSubnet(t, "117.61.103.0/24")
	if _, hit, _, _, reason := c.GetWithECS(key, req, &subC); hit {
		t.Fatal("未查询过的网段不能命中别人网段的答案")
	} else if reason != MissExpired {
		// 这个域名我们缓存过、只是别的网段先问的：归成「过期/网段不匹配」
		// 而不是「冷启动」，否则会被误判成容量不足。
		t.Fatalf("网段不匹配应归类为已缓存过，实际 %q", reason)
	}
}

// TestCacheECSDoesNotLeakToNoECSClients 断言网段层的答案不会给到
// 「没声明网段」的客户端。
//
// 那是按别人网段算出来的答案，给一个没网段身份的查询等于凭空假设它
// 属于那个网段 —— CDN 就近失效，某些场景直接是错的。
func TestCacheECSDoesNotLeakToNoECSClients(t *testing.T) {
	c := NewCache(256, 60, 3600)
	req := mkReq(t, "cdn2.example.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)

	sub := mustSubnet(t, "36.1.94.0/24")
	c.PutWithStampECS(key, mkResp(t, req, "59.36.46.63", 300), req, false, c.Stamp("u1"), &sub)

	if _, hit, _, _, _ := c.GetWithECS(key, req, nil); hit {
		t.Fatal("无网段的查询不能命中网段层的条目")
	}
	// 反过来：主键层（与网段无关）的答案对所有网段都该命中。
	c.PutWithStampECS(key, mkResp(t, req, "1.1.1.1", 300), req, false, c.Stamp("u1"), nil)
	if _, hit, _, _, _ := c.GetWithECS(key, req, &sub); !hit {
		t.Fatal("主键层的通用答案对任何网段都该命中")
	}
	if _, hit, _, _, _ := c.GetWithECS(key, req, nil); !hit {
		t.Fatal("主键层的通用答案对无网段查询也该命中")
	}
}

// TestCacheBlockedStaysSharedUnderECS 是这次修复的另一半：
// 拦截应答必须留在主键层，不被网段分裂。
//
// 拦截应答是本地构造的 0.0.0.0，从没问过上游，与网段毫无关系。
// 按网段分裂等于「开 ECS 就把重复率最高的一类流量也逐出缓存」——
// 线上被拦的查询占 32~48%，全部重复率极高。
func TestCacheBlockedStaysSharedUnderECS(t *testing.T) {
	c := NewCache(256, 60, 3600)
	req := mkReq(t, "ad.example.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)
	stamp := c.Stamp("u1")

	blockedMsg := new(dns.Msg)
	blockedMsg.SetReply(req)
	rr, _ := dns.NewRR("ad.example. 300 IN A 0.0.0.0")
	blockedMsg.Answer = []dns.RR{rr}

	// 即使调用方以后不小心传了网段，规则决定型应答也必须留在主键层。
	if !c.PutWithActionStamp(key, blockedMsg, req, "blocked", stamp) {
		t.Fatal("拦截应答应写入缓存")
	}

	// 换一个完全不同的网段，仍必须命中 —— 说明它没有按网段分裂。
	other := mustSubnet(t, "1.2.3.0/24")
	got, hit, _, action, _ := c.GetWithECS(key, req, &other)
	if !hit {
		t.Fatal("拦截应答不能按网段分裂：不同网段的客户端该共享同一条缓存")
	}
	if action != "blocked" {
		t.Fatalf("命中拦截应答时必须还原状态，否则拦截率会失真：得到 %q", action)
	}
	if ip := got.Answer[0].(*dns.A).A.String(); ip != "0.0.0.0" {
		t.Fatalf("拦截应答内容不对: %s", ip)
	}
}

// TestCacheECSCapacityIsShared 断言网段条目也占容量、也会被淘汰。
//
// 网段层用嵌套 map 存储，淘汰时如果只看主键层，它们会变成永不失效的
// 僵尸条目 —— 占着内存却查不到，表现为「内存涨了但命中率没涨」。
func TestCacheECSCapacityIsShared(t *testing.T) {
	c := NewCache(16, 60, 3600) // 16 条 → 每分片 1 条

	req := mkReq(t, "cap.example.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)

	for i := 0; i < 40; i++ {
		sub := mustSubnet(t, netip.PrefixFrom(
			netip.AddrFrom4([4]byte{10, byte(i), 0, 0}), 24).String())
		c.PutWithStampECS(key, mkResp(t, req, "1.1.1.1", 300), req, false, c.Stamp("u1"), &sub)
	}
	// 容量上限是 16，网段条目必须一起被淘汰掉，不能无限增长。
	if n := c.Len(); n > 16 {
		t.Fatalf("缓存条目数超过容量上限：%d > 16（网段条目没参与淘汰）", n)
	}
}

// TestCacheFlushUsersDropsSubnetLayer 断言定向失效会连网段层一起清。
//
// 规则改了却只清主键层的话，某个域名的旧网段答案会继续被命中 ——
// 用户看到的是「加了规则没用，过一阵子才好」。
func TestCacheFlushUsersDropsSubnetLayer(t *testing.T) {
	c := NewCache(256, 60, 3600)
	req := mkReq(t, "flush.example.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)
	sub := mustSubnet(t, "36.1.94.0/24")

	c.PutWithStampECS(key, mkResp(t, req, "59.36.46.63", 300), req, false, c.Stamp("u1"), &sub)
	if _, hit, _, _, _ := c.GetWithECS(key, req, &sub); !hit {
		t.Fatal("前置：应能命中")
	}

	c.FlushUsers([]string{"u1"})

	if _, hit, _, _, _ := c.GetWithECS(key, req, &sub); hit {
		t.Fatal("FlushUsers 必须清掉网段层，否则规则变更后旧答案继续被命中")
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("FlushUsers 后应清空，实际剩 %d 条", n)
	}
}

// TestEcsKeyFromSUBNETIPv4AndIPv6 断言网段键的规范化：
// 掩码必须真的生效（10.1.2.3/24 与 10.1.2.9/24 是同一个网段），
// 且 v4/v6 不能撞在一起。
func TestEcsKeyFromSUBNETIPv4AndIPv6(t *testing.T) {
	a := &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24,
		Address: net.IPv4(10, 1, 2, 3),
	}
	b := &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24,
		Address: net.IPv4(10, 1, 2, 9),
	}
	ka, ok := ecsKeyFromSUBNET(a)
	if !ok {
		t.Fatal("解析 IPv4 网段失败")
	}
	kb, _ := ecsKeyFromSUBNET(b)
	if ka != kb {
		t.Fatal("同一 /24 内的不同 IP 必须归一成同一个网段键")
	}

	c := &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 16,
		Address: net.IPv4(10, 1, 0, 0),
	}
	kc, _ := ecsKeyFromSUBNET(c)
	if ka == kc {
		t.Fatal("不同掩码长度必须是不同的键")
	}

	v6 := &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 2, SourceNetmask: 56,
		Address: net.ParseIP("2001:db8:1234:5600::"),
	}
	k6, ok := ecsKeyFromSUBNET(v6)
	if !ok {
		t.Fatal("解析 IPv6 网段失败")
	}
	if !k6.ipv6 {
		t.Fatal("IPv6 网段必须标记地址族")
	}
	if k6 == ka {
		t.Fatal("IPv6 与 IPv4 网段键不能相同")
	}
}

// TestSharedSubnetScopePolicy 断言「写哪一层」的判据。
//
// 上游明确回 scope=0 才进主键层共享；其余一律只按客户端实际网段存。
// 特别是不采信上游声称的非零 scope —— 实测 223.5.5.5 对不带 ECS 的请求
// 会合成一个网段并回显非零 scope，照抄它等于把 A 网段的答案发给 B 网段。
func TestSharedSubnetScopePolicy(t *testing.T) {
	req := mkReq(t, "scope.example.", dns.TypeA)
	req.SetEdns0(1232, false)
	sent := &dns.EDNS0_SUBNET{
		Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24,
		Address: net.IPv4(36, 1, 94, 0),
	}
	cacheECS := mustSubnet(t, "36.1.94.0/24")

	mkEcho := func(scope uint8) *dns.Msg {
		resp := mkResp(t, req, "59.36.46.63", 300)
		opt := new(dns.OPT)
		opt.Hdr.Name = "."
		opt.Hdr.Rrtype = dns.TypeOPT
		opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{
			Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24,
			SourceScope: scope, Address: net.IPv4(36, 1, 94, 0),
		})
		resp.Extra = append(resp.Extra, opt)
		return resp
	}

	// scope=0 → 全网共享，写主键层。
	if got := sharedSubnet(mkEcho(0), sent, &cacheECS); got != nil {
		t.Fatal("上游声明 scope=0 时应写主键层（与网段无关）")
	}
	// scope=24 → 只按客户端实际网段存，不采信它声明的那份粗网段。
	if got := sharedSubnet(mkEcho(24), sent, &cacheECS); got == nil || *got != cacheECS {
		t.Fatal("非零 scope 应按客户端实际网段存，不能放大适用范围")
	}
	// 上游没回显 ECS → 最保守，只按实际网段存。
	if got := sharedSubnet(mkResp(t, req, "59.36.46.63", 300), sent, &cacheECS); got == nil {
		t.Fatal("上游没回显 ECS 时不能共享给别的网段")
	}
	// 没有网段身份 → 必然主键层。
	if got := sharedSubnet(mkEcho(24), sent, nil); got != nil {
		t.Fatal("无网段身份时应写主键层")
	}
}

// TestEcsEntryExpires 断言网段层条目也会按 TTL 过期并清掉。
func TestEcsEntryExpires(t *testing.T) {
	c := NewCache(64, 1, 1) // minTTL=maxTTL=1 秒
	req := mkReq(t, "ttl.example.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)
	sub := mustSubnet(t, "36.1.94.0/24")

	c.PutWithStampECS(key, mkResp(t, req, "1.1.1.1", 1), req, false, c.Stamp("u1"), &sub)
	if _, hit, _, _, _ := c.GetWithECS(key, req, &sub); !hit {
		t.Fatal("前置：刚写入应命中")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, hit, _, _, reason := c.GetWithECS(key, req, &sub); hit {
		t.Fatal("过期条目不该命中")
	} else if reason != MissExpired {
		t.Fatalf("过期应返回 MissExpired，实际 %q", reason)
	}
}
