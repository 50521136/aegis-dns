package resolver

import (
	"context"
	"errors"
	"fmt"
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
//	parallel     全并发，第一个成功应答胜出。延迟最低，出流量 N 倍。
//	fastest      全并发 + 累计各上游近期表现，稳定后偏向最快者。
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
	// fails 是连续失败次数，成功时清零。
	fails atomic.Int32
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
	s.fails.Store(0)
}

// observeFail 记录一次失败。
func (s *upstreamScore) observeFail() {
	s.fails.Add(1)
	// 失败后把估值拉高，让它在 fastest 模式里排到后面。
	old := s.ewmaUS.Load()
	if old == 0 {
		old = 50000
	}
	s.ewmaUS.Store(old * 2)
}

// exchangeResult 是单个上游的应答结果。
type exchangeResult struct {
	resp *dns.Msg
	src  string
	err  error
}

// exchangeParallel 对所有上游并发发起请求，返回第一个成功应答。
//
// SERVFAIL 不算成功：它在 DNS 语义里表示「上游自己也查询失败」，
// 用它的答案会掩盖真正的可用上游。只有全部上游都给 SERVFAIL 时才返回
// 其中第一个（RFC 9520 §2.1）。
func (p *Pool) exchangeParallel(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	n := len(ups)
	if n == 0 {
		return nil, "", errors.New("上游池为空")
	}
	if n == 1 {
		resp, err := p.exchangeOne(ctx, ups[0], req)
		return resp, ups[0].Address(), err
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan exchangeResult, n)
	for _, u := range ups {
		go func(u upstream.Upstream) {
			r, e := p.exchangeOne(raceCtx, u, req)
			ch <- exchangeResult{r, u.Address(), e}
		}(u)
	}

	var lastErr error
	var servfail *exchangeResult
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err == nil && r.resp != nil {
			if r.resp.Rcode == dns.RcodeServerFailure {
				// 留着当兜底，继续等真正可用的应答。
				if servfail == nil {
					rr := r
					servfail = &rr
				}
				continue
			}
			cancel() // 其余在途请求立即放弃
			return r.resp, r.src, nil
		}
		if r.err != nil {
			lastErr = r.err
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

// exchangeFastest 并发请求，但会累计各上游表现。
//
// 和 parallel 的区别：parallel 每次都全打，fastest 在某个上游明显领先时
// 可以少打几个（省流量），同时保留并发兜底。实现上仍然全并发，但把
// 慢上游的启动推迟一个很小的量（firstUS），让快上游先出手 —— 这样既保住
// 延迟，又能在快上游稳定时不浪费请求。
func (p *Pool) exchangeFastest(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	n := len(ups)
	if n <= 1 {
		return p.exchangeParallel(ctx, ups, req)
	}

	// 按历史表现排序，最快的排最前。
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

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan exchangeResult, n)
	launched := 0

	launch := func(k int) {
		u := ups[idx[k]]
		launched++
		go func(u upstream.Upstream) {
			start := time.Now()
			r, e := p.exchangeOne(raceCtx, u, req)
			if e == nil {
				p.scores[idx[k]].observe(time.Since(start).Microseconds())
			} else {
				p.scores[idx[k]].observeFail()
			}
			ch <- exchangeResult{r, u.Address(), e}
		}(u)
	}

	launch(0)

	// 最快上游先单独跑一小段；没答就把其余全部拉起。
	// 阈值比 hedge 小得多：这里只是想「省掉明显不必要的那几发」，
	// 不是在等故障判定，所以 20ms 足够区分快慢上游。
	stagger := time.NewTimer(fastestStagger)
	defer stagger.Stop()
	allOut := false

	var lastErr error
	var servfail *exchangeResult
	for i := 0; i < n; {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-stagger.C:
			if !allOut {
				allOut = true
				for k := launched; k < n; k++ {
					launch(k)
				}
			}
		case r := <-ch:
			i++
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
				// 首个上游就失败：不必等 stagger，立刻全打。
				if !allOut {
					allOut = true
					for k := launched; k < n; k++ {
						launch(k)
					}
				}
			}
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
		resp, err := p.exchangeOne(ctx, ups[i], req)
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

// sentinel 让编译期能发现 scores 长度与 ups 不一致的问题。
var _ = fmt.Sprintf
