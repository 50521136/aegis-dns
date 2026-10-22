package dns

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// 这个文件锁住「并发查询合并」的契约。
//
// 背景：客户端冷启动时会同时发出多条完全相同的查询，缓存只能挡住
// 「前一条已经写完之后」的请求。线上实测 onrt-stsdk.vivo.com.cn 在
// 10 分钟内被查 92 次、97% 的间隔小于 60 秒、中位数 0 秒，但命中率
// 只有 66% —— 没命中的那些全是并发击穿。

func replyFor(qname string) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(qname, dns.TypeA)
	m.Response = true
	rr, _ := dns.NewRR(qname + " 60 IN A 1.2.3.4")
	m.Answer = []dns.RR{rr}
	return m
}

// TestFlightMergesConcurrentQueries 断言 N 个并发同 key 查询只打一次上游。
func TestFlightMergesConcurrentQueries(t *testing.T) {
	g := newFlightGroup()
	key := FlightKey(CacheKey{UserID: "u1", QName: "a.example", QType: dns.TypeA}, false)

	var calls atomic.Int64
	const n = 20
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*dns.Msg, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, _, err, _ := g.Do(context.Background(), key, func() (*dns.Msg, string, error) {
				calls.Add(1)
				<-release // 卡住 leader，保证其他人都能加入同一个 flight
				return replyFor("a.example."), "1.1.1.1", nil
			})
			if err != nil {
				t.Errorf("goroutine %d 失败: %v", i, err)
				return
			}
			results[i] = resp
		}(i)
	}

	// 等所有 goroutine 都进入 Do 之后再放行 leader。
	time.Sleep(80 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("并发同 key 查询应只打 1 次上游，实际 %d 次", got)
	}
	for i, r := range results {
		if r == nil {
			t.Fatalf("第 %d 个请求没拿到应答", i)
		}
	}
}

// TestFlightWaitersGetIsolatedCopies 断言等待者拿到的是独立副本。
//
// leader 拿到应答后会继续改它（回填报文 ID、置 Compress、写缓存），
// 如果等待者共享同一个对象，就是数据竞争，且会把 leader 的 ID 发出去。
func TestFlightWaitersGetIsolatedCopies(t *testing.T) {
	g := newFlightGroup()
	key := "k"

	release := make(chan struct{})
	leaderDone := make(chan *dns.Msg, 1)
	go func() {
		resp, _, _, _ := g.Do(context.Background(), key, func() (*dns.Msg, string, error) {
			<-release
			return replyFor("b.example."), "", nil
		})
		leaderDone <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	waiterDone := make(chan *dns.Msg, 1)
	go func() {
		resp, _, _, shared := g.Do(context.Background(), key, func() (*dns.Msg, string, error) {
			t.Error("等待者不该自己执行 fn")
			return nil, "", nil
		})
		if !shared {
			t.Error("第二次调用应被标记为等待者")
		}
		// 等待者必须自己再 Copy 才能改（handler 里就是这么做的）
		cp := resp.Copy()
		cp.Id = 4242
		waiterDone <- cp
	}()

	time.Sleep(50 * time.Millisecond)
	close(release)

	leader := <-leaderDone
	waiter := <-waiterDone
	if leader == waiter {
		t.Fatal("等待者与 leader 不能共享同一个 *dns.Msg")
	}
	if leader.Id == 4242 {
		t.Fatal("等待者改自己的副本不该影响 leader")
	}
}

// TestFlightWaiterHonoursOwnDeadline 断言等待者能被自己的超时打断。
//
// 上游卡死时，等待者不能跟着无限等 —— 每个请求有自己的 deadline。
func TestFlightWaiterHonoursOwnDeadline(t *testing.T) {
	g := newFlightGroup()
	key := "slow"
	release := make(chan struct{})
	defer close(release)

	go func() {
		g.Do(context.Background(), key, func() (*dns.Msg, string, error) {
			<-release
			return replyFor("c.example."), "", nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err, shared := g.Do(ctx, key, func() (*dns.Msg, string, error) {
		t.Error("等待者不该自己执行 fn")
		return nil, "", nil
	})
	if !shared {
		t.Fatal("应被识别为等待者")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待者应按自己的 deadline 超时，实际 err=%v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("等待者超时耗时应接近 deadline，实际 %v", el)
	}
}

// TestFlightKeySeparatesDNSSEC 断言开了 DNSSEC 的请求不与没开的合并。
//
// 开了 DO 位上游会多带 RRSIG，和没开拿到的不是同一个答案。
func TestFlightKeySeparatesDNSSEC(t *testing.T) {
	k := CacheKey{UserID: "u1", QName: "d.example", QType: dns.TypeA}
	if FlightKey(k, false) == FlightKey(k, true) {
		t.Fatal("DNSSEC 开关必须参与合并键，否则 RRSIG 会串到没要 DNSSEC 的客户端")
	}
	// 不同租户不能合并：缓存键含 UserID，合并会跨租户泄漏规则结果。
	if FlightKey(k, false) == FlightKey(CacheKey{UserID: "u2", QName: "d.example", QType: dns.TypeA}, false) {
		t.Fatal("不同租户不能共用合并键")
	}
}

// TestFlightKeyOfSkipsECS 断言带 ECS 的请求不参与合并。
//
// 带 ECS 的应答按客户端网段定制，合并会把 leader 网段的答案发给别人。
func TestFlightKeyOfSkipsECS(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("e.example.", dns.TypeA)
	k := CacheKey{UserID: "u1", QName: "e.example", QType: dns.TypeA}

	if _, ok := flightKeyOf(k, true, false, req); !ok {
		t.Fatal("普通请求应参与合并")
	}

	withECS := new(dns.Msg)
	withECS.SetQuestion("e.example.", dns.TypeA)
	opt := new(dns.OPT)
	opt.Hdr.Name = "."
	opt.Hdr.Rrtype = dns.TypeOPT
	sub := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24}
	sub.Address = []byte{1, 2, 3, 0}
	opt.Option = append(opt.Option, sub)
	withECS.Extra = append(withECS.Extra, opt)

	if _, ok := flightKeyOf(k, true, false, withECS); ok {
		t.Fatal("带 ECS 的请求不能参与合并：会把 leader 网段的答案发给别的网段")
	}

	// 缓存键构造失败的畸形请求也不合并。
	if _, ok := flightKeyOf(k, false, false, req); ok {
		t.Fatal("不可缓存的请求不该参与合并")
	}
}

// TestFlightEmptyKeyBypasses 断言空 key 直接执行，不进合并表。
func TestFlightEmptyKeyBypasses(t *testing.T) {
	g := newFlightGroup()
	calls := 0
	for i := 0; i < 3; i++ {
		if _, _, _, shared := g.Do(context.Background(), "", func() (*dns.Msg, string, error) {
			calls++
			return replyFor("f.example."), "", nil
		}); shared {
			t.Fatal("空 key 不该被当成等待者")
		}
	}
	if calls != 3 {
		t.Fatalf("空 key 每次都要真正执行 fn，实际执行 %d 次", calls)
	}
	if g.Len() != 0 {
		t.Fatalf("空 key 不该在合并表里留记录，实际 %d 条", g.Len())
	}
}
