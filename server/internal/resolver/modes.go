package resolver

import (
	"context"
	"errors"

	"sync/atomic"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// 这个文件实现上游池的四种选择策略。
//
// 为什么需要它：之前只有一种 hedge 策略（先问主上游，300ms 后才拉起其余
// 上游竞速）。线上实测 4830 次查询里备用上游只接到 8 次 —— 因为主上游
// 61ms 就返回了，永远够不到 hedge 阈值。结果是「配了三个上游，实际只有
// 一个在干活」，故障切换能力形同虚设。
//
// AdGuard Home 的默认策略是 parallel（全并发、谁快用谁），实测平均处理
// 时间 3.6ms。这里补齐四种模式让调用方选：
//
//	parallel     有界并发，第一个成功应答胜出。
//	fastest      优先近期最快者，延迟后并发兜底。
//	load_balance 轮询分散，不并发。出流量最小，延迟取决于轮到的上游。
//	hedge        旧行为，只问主上游，超时才对冲。

// upstreamScore 记录单个上游的近期表现，用于 fastest 模式。
//
// 用指数滑动平均（EWMA）而不是窗口平均：窗口需要保存历史样本，而这里
// 每条查询都要更新，分配会成为热点。EWMA 只要一次乘加，且对近期变化
// 更敏感 —— 上游突然变慢时能快速调低权重。
type upstreamScore struct {
	// ewmaUS 是最近应答耗时的 EWMA，单位微秒。
	ewmaUS atomic.Int64

	// cooldownUntil 是临时抑制截止时间（Unix 纳秒）。
	cooldownUntil atomic.Int64
	failures      atomic.Int32
}

// observe 记录一次应答耗时。
func (s *upstreamScore) observe(us int64) {
	const alpha = 8 // 权重 1/8，约等于最近 8 次的有效窗口
	old := s.ewmaUS.Load()
	if old == 0 {
		s.ewmaUS.Store(us)
	} else {
		s.ewmaUS.Store(old + (us-old)/alpha)
	}

	s.failures.Store(0)
	s.cooldownUntil.Store(0)
}

// exchangeResult 是单个上游的应答结果。
type exchangeResult struct {
	resp *dns.Msg
	src  string
	err  error

	started bool
}

// exchangeParallel 查询上游池，最多同时运行 maxParallelUpstreams 个尝试。
//
// SERVFAIL 不算成功：它在 DNS 语义里表示「上游自己也查询失败」，
// 只有没有更好的应答时才作为兜底返回。限制并发既控制出流量，也避免
// 一个配置了很多上游的查询把客户端的单次预算耗在无意义的竞速上。
func (p *Pool) exchangeParallel(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	order := make([]int, len(ups))
	for i := range order {
		order[i] = i
	}
	return p.exchangeParallelOrdered(ctx, ups, req, order, 0)
}

func (p *Pool) exchangeParallelOrdered(ctx context.Context, ups []upstream.Upstream, req *dns.Msg, order []int, stagger time.Duration) (*dns.Msg, string, error) {
	if len(order) == 0 {
		return nil, "", errors.New("上游池为空")
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan exchangeResult, len(order))
	launched, completed, active := 0, 0, 0
	limit := min(maxParallelUpstreams, len(order))
	var staggerC <-chan time.Time
	if stagger > 0 && limit > 1 {
		limit = 1
		timer := time.NewTimer(stagger)
		defer timer.Stop()
		staggerC = timer.C
	}

	launch := func() {
		for active < limit && launched < len(order) {
			idx := order[launched]
			launched++
			if p.inCooldown(idx) {
				ch <- exchangeResult{src: ups[idx].Address(), err: cooldownError}
				continue
			}
			active++
			go func(idx int, u upstream.Upstream) {
				r, e := p.exchangeOneTracked(raceCtx, idx, u, req)
				ch <- exchangeResult{resp: r, src: u.Address(), err: e, started: true}
			}(idx, ups[idx])
		}
	}
	launch()

	var lastErr error
	var servfail *exchangeResult
	for completed < len(order) {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-staggerC:
			staggerC = nil
			limit = min(maxParallelUpstreams, len(order))
			launch()
		case r := <-ch:
			completed++
			if r.started {
				active--
			}
			if r.err == nil && r.resp != nil {
				if r.resp.Rcode == dns.RcodeServerFailure {
					if servfail == nil {
						rr := r
						servfail = &rr
					}
				} else {
					cancel()
					return r.resp, r.src, nil
				}
			} else if r.err != nil {
				lastErr = r.err
			}
			if r.started && staggerC != nil {
				staggerC = nil
				limit = min(maxParallelUpstreams, len(order))
			}
			launch()
		}
	}
	if servfail != nil {
		return servfail.resp, servfail.src, nil
	}
	if lastErr == nil {
		lastErr = errors.New("所有上游均未应答")
	}
	return nil, "", lastErr
}

// exchangeFastest orders candidates by recent EWMA and applies the same
// concurrency budget as parallel mode. Failed or cooling candidates are
// advanced without starting an unbounded fan-out.
func (p *Pool) exchangeFastest(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	n := len(ups)
	if n <= 1 {
		return p.exchangeParallel(ctx, ups, req)
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	scores := p.scores
	for i := 1; i < n; i++ {
		for j := i; j > 0 && scores[idx[j]].ewmaUS.Load() < scores[idx[j-1]].ewmaUS.Load(); j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
	return p.exchangeParallelOrdered(ctx, ups, req, idx, fastestStagger)
}

// exchangeLoadBalance 按轮询选一个上游，不并发。
//
// 出流量最省，但单次延迟完全取决于轮到的那个上游 —— 混用了快慢上游时
// 会一半快一半慢。适合「上游之间质量接近」的场景。
func (p *Pool) exchangeLoadBalance(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	n := uint64(len(ups))
	if n == 0 {
		return nil, "", errors.New("上游池为空")
	}
	start := p.rr.Add(1) - 1
	var lastErr error
	// 轮到的那个失败时顺延，保证「轮询」不等于「不重试」。
	for k := uint64(0); k < n; k++ {
		i := (start + k) % n
		resp, err := p.exchangeOneTracked(ctx, int(i), ups[i], req)
		if err == nil && resp != nil {
			return resp, ups[i].Address(), nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("所有上游均未应答")
	}
	return nil, "", lastErr
}

// fastestStagger 是 fastest 模式下「先只打最快上游」的等待窗口。
//
// 取值理由：实测国内到公共 DNS 的 RTT 在 6ms 量级，20ms 已经足够让快
// 上游答完。再大就会把「快上游刚好抖动」变成真实延迟；再小则省不下请求。
const fastestStagger = 20 * time.Millisecond
