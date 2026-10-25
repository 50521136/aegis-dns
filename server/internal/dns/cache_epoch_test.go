package dns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

// 这组测试锁住「规则变更期间的在途查询不能把旧答案写回缓存」这条。
//
// 缺陷形态（只在生产并发下出现，本地单请求测试永远复现不出来）：
//
//	查询 Q 带着旧规则出发 → 管理员改规则 → 快照重载 → FlushUsers 清掉该租户缓存
//	→ Q 才从上游回来，把**旧规则下的应答**写进缓存（TTL 最长 86400）。
//
// 用户看到的是「规则加了，这个域名还是照常解析」，而且过一阵子自己好
// （缓存过期），这种「自己会好的故障」最难自查。
//
// 判据必须是「写入被丢弃」，不能只看「flush 之后缓存是空的」——
// 后者在写入发生在 flush 之后时照样通过。

func testCacheKey(uid, name string) CacheKey {
	return CacheKey{UserID: uid, QName: name, QType: dns.TypeA}
}

// answerFor 造一条带指定 IP 的 A 应答。
func answerFor(t *testing.T, name, ip string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	rr, err := dns.NewRR(name + ". 60 IN A " + ip)
	if err != nil {
		t.Fatalf("构造 RR 失败: %v", err)
	}
	m.Answer = []dns.RR{rr}
	return m
}

// TestStaleStampRejectedAfterUserFlush 是核心用例：
// 查询出发后该租户规则变了（FlushUsers），这次写入必须被丢弃。
func TestStaleStampRejectedAfterUserFlush(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("ads.example.com.", dns.TypeA)
	k := testCacheKey("usr_a", "ads.example.com")

	// 查询出发，取 stamp（此时缓存里还是旧规则的结果）
	stamp := c.Stamp("usr_a")

	// 规则变更：该租户缓存被定向失效
	c.FlushUsers([]string{"usr_a"})

	// 在途查询回来了，带着旧规则下的应答（这里用「未拦截的正常答案」代表）
	wrote := c.PutWithStamp(k, answerFor(t, "ads.example.com", "1.2.3.4"), req, false, stamp)
	if wrote {
		t.Fatal("规则变更后，出发于变更之前的在途查询仍被写进了缓存 —— 这正是要修的缺陷")
	}
	if _, hit := c.Get(k, req); hit {
		t.Fatal("陈旧应答进了缓存，后续查询会命中旧规则的结果")
	}
}

// TestStaleStampRejectedAfterFullFlush 同上，但走全量 Flush（管理员手动清缓存、
// 或设置变更导致整机重载）。
func TestStaleStampRejectedAfterFullFlush(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	k := testCacheKey("usr_b", "example.com")

	stamp := c.Stamp("usr_b")
	c.Flush()

	if c.PutWithStamp(k, answerFor(t, "example.com", "5.6.7.8"), req, false, stamp) {
		t.Fatal("全量 Flush 之后，陈旧 stamp 的写入必须被丢弃")
	}
}

// TestFreshStampStillWrites 是对照组：没有发生失效时写入必须照常成功。
//
// 没有这条，「所有写入都被丢弃」也能让上面两条通过 —— 那是把缓存彻底关掉，
// 不是修好。
func TestFreshStampStillWrites(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("ok.example.com.", dns.TypeA)
	k := testCacheKey("usr_c", "ok.example.com")

	stamp := c.Stamp("usr_c")
	if !c.PutWithStamp(k, answerFor(t, "ok.example.com", "9.9.9.9"), req, false, stamp) {
		t.Fatal("没有失效时写入应当成功（否则等于把缓存关了）")
	}
	msg, hit := c.Get(k, req)
	if !hit {
		t.Fatal("刚写入的条目应当能命中")
	}
	if len(msg.Answer) != 1 {
		t.Fatalf("应答内容不对: %v", msg.Answer)
	}
}

// TestOtherUserFlushDoesNotInvalidateMyStamp 验证定向失效的范围：
// 别人改了规则不该让我的在途查询作废（否则一次规则变更会波及全站写入）。
func TestOtherUserFlushDoesNotInvalidateMyStamp(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("mine.example.com.", dns.TypeA)
	k := testCacheKey("usr_me", "mine.example.com")

	stamp := c.Stamp("usr_me")
	c.FlushUsers([]string{"usr_other"}) // 只影响别人

	if !c.PutWithStamp(k, answerFor(t, "mine.example.com", "3.3.3.3"), req, false, stamp) {
		t.Fatal("别的租户失效不该作废我的写入 —— 定向失效必须只影响目标租户")
	}
}

// TestStampAdvancesAcrossFlushes 断言代际确实在推进（而不是靠某种巧合通过）。
func TestStampAdvancesAcrossFlushes(t *testing.T) {
	c := NewCache(64, 60, 3600)
	s0 := c.Stamp("u")
	c.FlushUsers([]string{"u"})
	s1 := c.Stamp("u")
	if s1.User == s0.User {
		t.Fatalf("FlushUsers 之后该租户代际应当递增：before=%d after=%d", s0.User, s1.User)
	}
	c.Flush()
	s2 := c.Stamp("u")
	if s2.Gen == s1.Gen {
		t.Fatalf("Flush 之后全局代际应当递增：before=%d after=%d", s1.Gen, s2.Gen)
	}
}

// TestBlockedAnswerStampGuardsRuleFlips 模拟真实缺陷场景：
// 一条域名原本放行（正常解析），管理员加了拦截规则，而在途的那条「放行答案」
// 不能覆盖掉新的拦截结果。
func TestBlockedAnswerStampGuardsRuleFlips(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("track.example.net.", dns.TypeA)
	k := testCacheKey("usr_d", "track.example.net")

	// 在途查询出发时规则还没加（stamp 取在规则变更之前）
	stale := c.Stamp("usr_d")

	// 管理员加了拦截规则 → 快照重载 → 定向失效
	c.FlushUsers([]string{"usr_d"})

	// 旧的「正常解析」答案回来，不能写进缓存
	if c.PutWithStamp(k, answerFor(t, "track.example.net", "8.8.4.4"), req, false, stale) {
		t.Fatal("旧的放行答案被写进缓存，新加的拦截规则对该域名会失效到 TTL 到期")
	}

	// 之后新的拦截结果（stamp 取在变更之后）必须能写进去
	fresh := c.Stamp("usr_d")
	blocked := new(dns.Msg)
	blocked.SetQuestion("track.example.net.", dns.TypeA)
	blocked.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: "track.example.net.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
	}}
	if !c.PutWithActionStamp(k, blocked, req, "blocked", fresh) {
		t.Fatal("变更之后的拦截结果应当能写进缓存")
	}
	if _, hit := c.Get(k, req); !hit {
		t.Fatal("拦截结果没进缓存，拦截类流量会全部重跑规则匹配")
	}
}

// TestPutWithoutStampSkipsCheck 保证不带 stamp 的老入口行为不变
// （测试与不关心代际的调用方仍可用，不会被静默丢弃）。
func TestPutWithoutStampSkipsCheck(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("legacy.example.com.", dns.TypeA)
	k := testCacheKey("usr_e", "legacy.example.com")

	c.Flush() // 让代际前进，制造「看起来像陈旧」的状态
	if !c.Put(k, answerFor(t, "legacy.example.com", "4.4.4.4"), req) {
		t.Fatal("不带 stamp 的 Put 不做代际校验，flush 之后也应能写入")
	}
}

// TestStampConcurrentFlushNoStaleWrite 是并发版本：一边反复 flush，
// 一边用旧 stamp 写，任何一次写成功都算失败。
func TestStampConcurrentFlushNoStaleWrite(t *testing.T) {
	c := NewCache(256, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("race.example.com.", dns.TypeA)
	k := testCacheKey("usr_race", "race.example.com")
	stale := c.Stamp("usr_race")

	// 必须先确认「第一次 flush 已经发生」再开始断言。
	//
	// 直接一边起 flush 协程一边断言是错的：两者在赛跑，第一轮写入完全可能
	// 先于第一次 flush 到达 —— 那时 stamp 仍新鲜，写进去是**正确行为**。
	// 断言从这个点上开始才有意义（实测这条写成赛跑会假红一次）。
	flushed := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.FlushUsers([]string{"usr_race"})
		close(flushed)
		for i := 0; i < 200; i++ {
			c.FlushUsers([]string{"usr_race"})
			time.Sleep(time.Millisecond)
		}
	}()

	<-flushed
	for i := 0; i < 200; i++ {
		if c.PutWithStamp(k, answerFor(t, "race.example.com", "7.7.7.7"), req, false, stale) {
			t.Fatal("并发的 flush 期间，陈旧 stamp 的写入被接受了")
		}
	}
	<-done
}
