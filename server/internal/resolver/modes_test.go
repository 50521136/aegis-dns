package resolver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// fakeUpstream 是可编程的假上游：指定延迟、是否失败、返回什么 rcode。
type fakeUpstream struct {
	addr   string
	delay  time.Duration
	fail   bool
	servfail bool
	calls  atomic.Int64
}

func (f *fakeUpstream) Address() string { return f.addr }
func (f *fakeUpstream) Close() error    { return nil }

func (f *fakeUpstream) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.fail {
		return nil, errors.New("假上游故障")
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	if f.servfail {
		resp.Rcode = dns.RcodeServerFailure
		return resp, nil
	}
	rr, _ := dns.NewRR(req.Question[0].Name + " 60 IN A 1.2.3.4")
	resp.Answer = append(resp.Answer, rr)
	return resp, nil
}

func newTestPool(mode string, ups ...upstream.Upstream) *Pool {
	p := &Pool{timeout: time.Second, mode: mode}
	p.ups = ups
	p.scores = make([]*upstreamScore, len(ups))
	for i := range p.scores {
		p.scores[i] = &upstreamScore{}
	}
	for _, u := range ups {
		p.addrs = append(p.addrs, u.Address())
	}
	return p
}

func testReq() *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	return m
}

// parallel 必须并发：三个上游分别 100ms，总耗时应当接近 100ms 而不是 300ms。
func TestParallelIsConcurrent(t *testing.T) {
	a := &fakeUpstream{addr: "a", delay: 100 * time.Millisecond}
	b := &fakeUpstream{addr: "b", delay: 100 * time.Millisecond}
	c := &fakeUpstream{addr: "c", delay: 100 * time.Millisecond}
	p := newTestPool("parallel", a, b, c)

	start := time.Now()
	_, _, err := p.ExchangeWithSource(context.Background(), testReq())
	el := time.Since(start)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if el > 200*time.Millisecond {
		t.Errorf("耗时 %v，看起来没并发（串行应为 300ms）", el)
	}
	t.Logf("parallel 三个100ms上游总耗时 %v", el.Round(time.Millisecond))
}

// parallel 必须取最快的那个。
func TestParallelTakesFastest(t *testing.T) {
	slow := &fakeUpstream{addr: "slow", delay: 200 * time.Millisecond}
	fast := &fakeUpstream{addr: "fast", delay: 10 * time.Millisecond}
	p := newTestPool("parallel", slow, fast)

	start := time.Now()
	_, src, err := p.ExchangeWithSource(context.Background(), testReq())
	el := time.Since(start)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if src != "fast" {
		t.Errorf("应答来自 %s，期望 fast", src)
	}
	if el > 100*time.Millisecond {
		t.Errorf("耗时 %v，没有走最快的上游", el)
	}
	t.Logf("parallel 取到 %s，耗时 %v", src, el.Round(time.Millisecond))
}

// SERVFAIL 不能算成功：它表示上游自己查挂了。
// 只要还有别的上游能给正常应答，就必须用那个。
func TestParallelRejectsServfail(t *testing.T) {
	sf := &fakeUpstream{addr: "servfail", delay: 5 * time.Millisecond, servfail: true}
	ok := &fakeUpstream{addr: "ok", delay: 80 * time.Millisecond}
	p := newTestPool("parallel", sf, ok)

	_, src, err := p.ExchangeWithSource(context.Background(), testReq())
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if src != "ok" {
		t.Errorf("应答来自 %s，SERVFAIL 被当成成功了", src)
	}
}

// 全部 SERVFAIL 时仍然要返回一个答案，而不是报错 ——
// 客户端拿到 SERVFAIL 比拿不到回包更容易重试成功。
func TestParallelAllServfail(t *testing.T) {
	a := &fakeUpstream{addr: "a", delay: 5 * time.Millisecond, servfail: true}
	b := &fakeUpstream{addr: "b", delay: 10 * time.Millisecond, servfail: true}
	p := newTestPool("parallel", a, b)

	resp, _, err := p.ExchangeWithSource(context.Background(), testReq())
	if err != nil {
		t.Fatalf("全 SERVFAIL 应返回应答而不是错误: %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Errorf("rcode = %d, 期望 SERVFAIL", resp.Rcode)
	}
}

// load_balance 不并发：只打一个上游。
func TestLoadBalanceSingleUpstream(t *testing.T) {
	a := &fakeUpstream{addr: "a", delay: 10 * time.Millisecond}
	b := &fakeUpstream{addr: "b", delay: 10 * time.Millisecond}
	p := newTestPool("load_balance", a, b)

	for i := 0; i < 4; i++ {
		if _, _, err := p.ExchangeWithSource(context.Background(), testReq()); err != nil {
			t.Fatalf("第 %d 次查询失败: %v", i, err)
		}
	}
	total := a.calls.Load() + b.calls.Load()
	if total != 4 {
		t.Errorf("4 次查询产生了 %d 次上游请求，load_balance 应当每次只打一个", total)
	}
	// 轮询要真的分流，不能永远打第一个。
	if a.calls.Load() == 0 || b.calls.Load() == 0 {
		t.Errorf("没有轮询分流: a=%d b=%d", a.calls.Load(), b.calls.Load())
	}
	t.Logf("轮询分发 a=%d b=%d", a.calls.Load(), b.calls.Load())
}

// hedge 只打主上游就返回，备用不该被拉起。
func TestHedgeOnlyUsesPrimary(t *testing.T) {
	prim := &fakeUpstream{addr: "primary", delay: 10 * time.Millisecond}
	back := &fakeUpstream{addr: "backup", delay: 5 * time.Millisecond}
	p := newTestPool("hedge", prim, back)

	_, src, err := p.ExchangeWithSource(context.Background(), testReq())
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if src != "primary" {
		t.Errorf("应答来自 %s，hedge 模式应当用主上游", src)
	}
	if back.calls.Load() != 0 {
		t.Errorf("备用上游被调用了 %d 次，hedge 模式不该拉起它", back.calls.Load())
	}
}

// 这也是线上那个问题的复现：主上游够快时，备用永远不上场。
// 4830 次查询备用只接到 8 次，就是这个原因。
func TestHedgeStarvesBackup(t *testing.T) {
	prim := &fakeUpstream{addr: "primary", delay: 61 * time.Millisecond}
	back := &fakeUpstream{addr: "backup", delay: 10 * time.Millisecond}
	p := newTestPool("hedge", prim, back)

	for i := 0; i < 10; i++ {
		if _, _, err := p.ExchangeWithSource(context.Background(), testReq()); err != nil {
			t.Fatalf("查询失败: %v", err)
		}
	}
	if back.calls.Load() != 0 {
		t.Errorf("备用被调用 %d 次，期望 0（主上游 61ms < HedgeDelay 300ms）", back.calls.Load())
	}
	t.Logf("确认：主上游 61ms 时备用调用 %d 次 —— 这就是备用形同虚设的原因", back.calls.Load())
}

// 备用池只在主池全部失败时才接管。
func TestFallbackActivatesOnTotalFailure(t *testing.T) {
	dead1 := &fakeUpstream{addr: "dead1", delay: time.Millisecond, fail: true}
	dead2 := &fakeUpstream{addr: "dead2", delay: time.Millisecond, fail: true}
	main := newTestPool("parallel", dead1, dead2)

	good := &fakeUpstream{addr: "fallback", delay: 5 * time.Millisecond}
	fb := newTestPool("parallel", good)
	main.SetFallback(fb)

	_, src, err := main.ExchangeWithSource(context.Background(), testReq())
	if err != nil {
		t.Fatalf("备用应接管，却报错: %v", err)
	}
	if src != "fallback" {
		t.Errorf("应答来自 %s，期望 fallback", src)
	}
	if good.calls.Load() == 0 {
		t.Error("备用池没有被调用")
	}
}

// 主池能用时，备用不该被打扰。
func TestFallbackUntouchedWhenMainWorks(t *testing.T) {
	mainUp := &fakeUpstream{addr: "main", delay: 5 * time.Millisecond}
	main := newTestPool("parallel", mainUp)
	fbUp := &fakeUpstream{addr: "fallback", delay: 5 * time.Millisecond}
	main.SetFallback(newTestPool("parallel", fbUp))

	if _, _, err := main.ExchangeWithSource(context.Background(), testReq()); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if fbUp.calls.Load() != 0 {
		t.Errorf("备用被调用 %d 次，主池正常时不该启用", fbUp.calls.Load())
	}
}

// fastest 模式：同样并发、取最快，并且要更新 score。
func TestFastestUpdatesScores(t *testing.T) {
	slow := &fakeUpstream{addr: "slow", delay: 120 * time.Millisecond}
	fast := &fakeUpstream{addr: "fast", delay: 10 * time.Millisecond}
	p := newTestPool("fastest", slow, fast)

	for i := 0; i < 3; i++ {
		if _, _, err := p.ExchangeWithSource(context.Background(), testReq()); err != nil {
			t.Fatalf("第 %d 次查询失败: %v", i, err)
		}
	}
	// 至少有一个上游的 score 被记录（快的那条路径一定记录过）。
	var got int64
	for _, sc := range p.scores {
		got += sc.ewmaUS.Load()
	}
	if got == 0 {
		t.Error("fastest 模式没有记录任何上游表现")
	}
	t.Logf("scores: fast=%dus slow=%dus", p.scores[1].ewmaUS.Load(), p.scores[0].ewmaUS.Load())
}

// 未配置模式时按 hedge 走，保证旧快照行为不变。
func TestEmptyModeFallsBackToHedge(t *testing.T) {
	prim := &fakeUpstream{addr: "primary", delay: 10 * time.Millisecond}
	back := &fakeUpstream{addr: "backup", delay: 5 * time.Millisecond}
	p := newTestPool("", prim, back)

	_, src, err := p.ExchangeWithSource(context.Background(), testReq())
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if src != "primary" {
		t.Errorf("空模式应答来自 %s，期望 primary（按 hedge 处理）", src)
	}
}
