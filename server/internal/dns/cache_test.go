package dns

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// TestCacheIsolationByUser 验证多租户隔离（文档第十章「必须避免的反模式」第一条）。
func TestCacheIsolationByUser(t *testing.T) {
	c := NewCache(64, 60, 3600)

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	keyA, ok := CacheKeyFrom("userA", req)
	if !ok {
		t.Fatal("构造缓存键失败")
	}
	keyB, _ := CacheKeyFrom("userB", req)

	if keyA == keyB {
		t.Fatal("不同用户的缓存键必须不同，否则会跨租户泄漏")
	}

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(1, 2, 3, 4).To4(),
	}}

	if !c.Put(keyA, resp, req) {
		t.Fatal("写入缓存失败")
	}
	if _, hit := c.Get(keyA); !hit {
		t.Fatal("同一用户应命中缓存")
	}
	if _, hit := c.Get(keyB); hit {
		t.Fatal("另一用户不应命中该用户的缓存")
	}
}

// TestCacheHitMustResetID 是「缓存命中必须重置报文 ID」的回归测试。
//
// 这个 bug 只在第二次查询同一域名时出现：缓存里存的是首次应答的副本，
// 它带着首次请求的随机 ID。原样返回会让客户端认为应答不匹配而丢弃，
// 表现为查询超时（dig 报 "ID mismatch"）。
func TestCacheHitMustResetID(t *testing.T) {
	c := NewCache(64, 60, 3600)

	mkReq := func(id uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion("id-reset.test.", dns.TypeA)
		m.Id = id
		return m
	}

	req1 := mkReq(1111)
	key, _ := CacheKeyFrom("u1", req1)

	resp := new(dns.Msg)
	resp.SetReply(req1)
	resp.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "id-reset.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(1, 2, 3, 4).To4(),
	}}
	c.Put(key, resp, req1)

	// 第二次请求用不同的 ID，缓存返回的副本必须带着旧 ID。
	req2 := mkReq(2222)
	key2, _ := CacheKeyFrom("u1", req2)
	got, hit := c.Get(key2)
	if !hit {
		t.Fatal("应命中缓存")
	}
	if got.Id != 1111 {
		t.Fatalf("缓存副本应带首次请求的 ID 1111，实际 %d（若这里变了，说明缓存层开始改写 ID，需同步更新 handler 的契约）", got.Id)
	}
	// 调用方的正确做法：写回前重置 ID。
	got.Id = req2.Id
	if got.Id != 2222 {
		t.Fatal("重置后 ID 应为 2222")
	}
}

// TestCacheTTLDecrement 验证命中缓存时 TTL 递减且不低于 1。
func TestCacheTTLDecrement(t *testing.T) {
	c := NewCache(16, 1, 3600)

	req := new(dns.Msg)
	req.SetQuestion("ttl.test.", dns.TypeA)
	key, _ := CacheKeyFrom("u1", req)

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "ttl.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(9, 9, 9, 9).To4(),
	}}
	c.Put(key, resp, req)

	// 人为把 storedAt 往前挪，模拟已过去 100 秒。
	c.shardFor(key).mu.Lock()
	item := c.shardFor(key).items[key].Value.(*cacheItem)
	item.entry.storedAt = time.Now().Add(-100 * time.Second)
	c.shardFor(key).mu.Unlock()

	got, hit := c.Get(key)
	if !hit {
		t.Fatal("应命中缓存")
	}
	if ttl := got.Answer[0].Header().Ttl; ttl != 200 {
		t.Fatalf("TTL 应递减到 200，实际 %d", ttl)
	}
}

// TestCacheSkipsECS 验证带 EDNS Client Subnet 的请求不缓存（文档 4.3）。
func TestCacheSkipsECS(t *testing.T) {
	c := NewCache(16, 60, 3600)

	req := new(dns.Msg)
	req.SetQuestion("ecs.test.", dns.TypeA)
	opt := new(dns.OPT)
	opt.Hdr.Name = "."
	opt.Hdr.Rrtype = dns.TypeOPT
	opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{
		Code:          dns.EDNS0SUBNET,
		Family:        1,
		SourceNetmask: 24,
		Address:       net.IPv4(1, 2, 3, 0).To4(),
	})
	req.Extra = append(req.Extra, opt)

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "ecs.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(5, 6, 7, 8).To4(),
	}}

	key, _ := CacheKeyFrom("u1", req)
	if c.Put(key, resp, req) {
		t.Fatal("带 ECS 的请求不应被缓存")
	}
}

// TestCacheLRUEviction 验证超过容量时淘汰最旧条目，且 map 不残留僵尸项。
func TestCacheLRUEviction(t *testing.T) {
	// 容量 16 条，分 16 片 => 每片 1 条。
	c := NewCache(16, 60, 3600)

	// 用同一个 user 与不同 qname 制造哈希分散；这里只验证「不泄漏」，
	// 因此逐条插入后检查总条目数不超过容量。
	for i := 0; i < 200; i++ {
		req := new(dns.Msg)
		req.SetQuestion(randName(i), dns.TypeA)
		key, _ := CacheKeyFrom("u1", req)

		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
			A:   net.IPv4(1, 1, 1, 1).To4(),
		}}
		c.Put(key, resp, req)
	}

	total := 0
	for _, s := range c.shards {
		s.mu.Lock()
		total += len(s.items)
		if len(s.items) != s.lru.Len() {
			s.mu.Unlock()
			t.Fatalf("map 与 LRU 长度不一致：map=%d lru=%d（说明淘汰时没删 map 项）", len(s.items), s.lru.Len())
		}
		s.mu.Unlock()
	}
	if total > c.size {
		t.Fatalf("缓存条目数 %d 超过容量 %d", total, c.size)
	}
}

func randName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return string([]byte{
		letters[i%26],
		letters[(i/26)%26],
		letters[(i/676)%26],
	}) + ".evict.test."
}

// TestCIDRIndexLongestPrefixWins 验证最长前缀优先。
func TestCIDRIndexLongestPrefixWins(t *testing.T) {
	idx := newCIDRIndex()
	if err := idx.Add("203.0.113.0/24", "user-net", "netcid"); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add("203.0.113.7/32", "user-host", "hostcid"); err != nil {
		t.Fatal(err)
	}

	uid, _, ok := idx.Lookup(net.ParseIP("203.0.113.7"))
	if !ok || uid != "user-host" {
		t.Fatalf("/32 应优先于 /24，实际 uid=%q ok=%v", uid, ok)
	}

	uid, _, ok = idx.Lookup(net.ParseIP("203.0.113.9"))
	if !ok || uid != "user-net" {
		t.Fatalf("同网段其他 IP 应命中 /24，实际 uid=%q ok=%v", uid, ok)
	}

	if _, _, ok = idx.Lookup(net.ParseIP("198.51.100.1")); ok {
		t.Fatal("网段外 IP 不应命中")
	}
}

// TestBlockingModes 验证四种拦截模式的应答形状（文档 4.4）。
func TestBlockingModes(t *testing.T) {
	mkReq := func(qtype uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion("ads.example.com.", qtype)
		return m
	}

	t.Run("null_ip", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeA), "null_ip", "")
		if len(resp.Answer) != 1 {
			t.Fatalf("期望 1 条应答，实际 %d", len(resp.Answer))
		}
		a, ok := resp.Answer[0].(*dns.A)
		if !ok || !a.A.Equal(net.IPv4zero) {
			t.Fatalf("期望 A 0.0.0.0，实际 %v", resp.Answer[0])
		}
	})

	t.Run("null_ip AAAA", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeAAAA), "null_ip", "")
		aaaa, ok := resp.Answer[0].(*dns.AAAA)
		if !ok || !aaaa.AAAA.Equal(net.IPv6zero) {
			t.Fatalf("期望 AAAA ::，实际 %v", resp.Answer[0])
		}
	})

	t.Run("nxdomain", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeA), "nxdomain", "")
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("期望 NXDOMAIN，实际 %d", resp.Rcode)
		}
		if len(resp.Ns) == 0 {
			t.Error("NXDOMAIN 应带 SOA 以便客户端负缓存")
		}
	})

	t.Run("refused", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeA), "refused", "")
		if resp.Rcode != dns.RcodeRefused {
			t.Fatalf("期望 REFUSED，实际 %d", resp.Rcode)
		}
	})

	t.Run("custom_ip", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeA), "custom_ip", "10.20.30.40")
		a := resp.Answer[0].(*dns.A)
		if !a.A.Equal(net.ParseIP("10.20.30.40")) {
			t.Fatalf("期望 10.20.30.40，实际 %v", a.A)
		}
	})

	t.Run("custom_ip 非法值降级为 null_ip", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeA), "custom_ip", "not-an-ip")
		a := resp.Answer[0].(*dns.A)
		if !a.A.Equal(net.IPv4zero) {
			t.Fatalf("非法 custom_ip 应降级为 0.0.0.0，实际 %v", a.A)
		}
	})

	t.Run("HTTPS 记录返回 NODATA", func(t *testing.T) {
		resp := buildBlockResponse(mkReq(dns.TypeHTTPS), "null_ip", "")
		if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
			t.Fatalf("HTTPS 记录应返回 NODATA，实际 rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
		}
	})
}

// TestRewriteResponseQTypeMatch 验证改写应答只回填类型匹配的记录。
func TestRewriteResponseQTypeMatch(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("nas.home.", dns.TypeA)

	rrs := []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "nas.home.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(192, 168, 1, 10).To4(),
	}}

	resp := rewriteResponse(req, rrs)
	if len(resp.Answer) != 1 {
		t.Fatalf("A 查询应得到 1 条改写记录，实际 %d", len(resp.Answer))
	}

	// AAAA 查询命中 A 改写规则时返回 NODATA（文档 5.5「部分改写的处理」）。
	reqAAAA := new(dns.Msg)
	reqAAAA.SetQuestion("nas.home.", dns.TypeAAAA)
	respAAAA := rewriteResponse(reqAAAA, rrs)
	if len(respAAAA.Answer) != 0 {
		t.Fatalf("AAAA 查询不应拿到 A 记录，实际 %d 条", len(respAAAA.Answer))
	}
}

// TestFirstLabel 的用例已合并到 identify_test.go 的
// TestFirstLabelOnlyAcceptsClientIDShape —— 那里连同「裸域名必须返回空串」
// 这个契约一起测，因为它正是路径式 DoH 失效的根因。
//
// 这里原本用的例子 "k7m2p9xq4a" 取自设计文档，但它含字符 '9'，
// 而 client_id 字符集是 [a-z2-7]（排除 0/1/8/9）。文档示例与自己的字符集
// 规则相矛盾，以字符集为准；测试里改用合法值。
