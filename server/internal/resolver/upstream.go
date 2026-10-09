// Package resolver 封装上游 DNS 转发。
//
// 设计目标（文档 4.1 Stage 5 / 风险 R9）：
//   - 多上游：优先用第一个（主），失败时并发竞速其余作为 fallback；
//   - 超时可控：单上游 3s、整体受调用方 ctx 约束；
//   - 池化：同一组上游地址复用同一个 Pool，避免每次查询重建连接。
package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// PerUpstreamTimeout 是单个上游的超时（文档 4.2）。
const PerUpstreamTimeout = 3 * time.Second

// Pool 是一组上游服务器。
type Pool struct {
	addrs []string
	ups   []upstream.Upstream
	mu    sync.RWMutex
	// closed 标记池是否已关闭（Registry 热更新时会替换掉旧池）。
	closed bool
}

// NewPool 按地址列表创建一个上游池。
//
// 地址支持 "1.1.1.1"、"udp://1.1.1.1:53"、"tcp://..."、"tls://dns.google:853"、
// "https://dns.google/dns-query" —— 由 dnsproxy 的 upstream 包统一解析。
func NewPool(addrs []string) (*Pool, error) {
	opts := &upstream.Options{
		Timeout: PerUpstreamTimeout,
	}
	p := &Pool{addrs: normalizeAddrs(addrs)}
	for _, a := range p.addrs {
		u, err := upstream.AddressToUpstream(a, opts)
		if err != nil {
			// 单个上游不可用（例如域名解析不了）不应让整池失败：
			// 保留其余可用上游，符合文档 P4 优雅降级。
			continue
		}
		p.ups = append(p.ups, u)
	}
	if len(p.ups) == 0 {
		return nil, fmt.Errorf("没有任何可用的上游 DNS（尝试过 %v）", p.addrs)
	}
	return p, nil
}

// normalizeAddrs 补全裸 IP 为 udp:// 形式，去重去空。
func normalizeAddrs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// 裸 IP（含 IPv6）默认按 UDP 53 处理。
		if !strings.Contains(a, "://") {
			a = "udp://" + a
		}
		// udp://1.1.1.1 补默认端口；dnsproxy 的 AddressToUpstream 要求显式端口。
		if host := strings.TrimPrefix(strings.TrimPrefix(a, "udp://"), "tcp://"); !strings.Contains(host, ":") {
			a = a + ":53"
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// Addrs 返回池内配置的上游地址。
func (p *Pool) Addrs() []string { return p.addrs }

// HedgeDelay 是「主上游多久没答就把其余上游并发拉起来」的阈值。
//
// 取 700ms 的理由：国内到公共 DNS 的正常 RTT 在 10–100ms，
// 700ms 已经远超正常范围，说明主上游有问题；同时又远小于单上游超时（3s），
// 所以故障切换的额外延迟几乎不可感知。
const HedgeDelay = 700 * time.Millisecond

// Exchange 向池内上游发起查询。
//
// 策略（Happy Eyeballs 思路）：
//  1. 先只问主上游 —— 正常情况下只有一次上游请求，省流量；
//  2. 700ms 内没答，或主上游直接报错，就把其余上游全部并发拉起来竞速；
//  3. 第一个成功的应答胜出，其余请求立即取消。
//
// 不用「主上游等满 3s 超时再切」的原因：那会让每一次查询在主上游故障时
// 都白等 3 秒，客户端侧表现为整体解析变慢甚至超时。
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if p == nil || len(p.ups) == 0 {
		return nil, errors.New("上游池为空")
	}
	p.mu.RLock()
	closed := p.closed
	ups := p.ups
	p.mu.RUnlock()
	if closed {
		return nil, errors.New("上游池已关闭")
	}

	// 单个上游：直接查，没有可竞速的对象。
	if len(ups) == 1 {
		return p.exchangeOne(ctx, ups[0], req)
	}

	type result struct {
		resp *dns.Msg
		err  error
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan result, len(ups))
	launched := 0
	pending := 0

	launch := func(u upstream.Upstream) {
		launched++
		pending++
		go func() {
			r, e := p.exchangeOne(raceCtx, u, req)
			ch <- result{r, e}
		}()
	}
	launchRemaining := func() {
		for launched < len(ups) {
			launch(ups[launched])
		}
	}

	launch(ups[0])

	hedge := time.NewTimer(HedgeDelay)
	defer hedge.Stop()
	hedged := false

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-hedge.C:
			if !hedged {
				hedged = true
				launchRemaining()
			}

		case r := <-ch:
			pending--
			if r.err == nil && r.resp != nil {
				cancel() // 拿到结果立刻取消其余在途请求
				return r.resp, nil
			}
			if r.err != nil {
				lastErr = r.err
			}
			// 主上游失败：不必等 hedge 计时器，立刻把其余上游拉起来。
			if !hedged {
				hedged = true
				launchRemaining()
			}
			if pending == 0 {
				if lastErr == nil {
					lastErr = errors.New("所有上游均失败")
				}
				return nil, lastErr
			}
		}
	}
}

// exchangeOne 向单个上游发起一次查询，强制单上游超时。
//
// dnsproxy v0.86 的 Upstream.Exchange 接受 context，因此超时直接交给它处理，
// 不需要再套一层 goroutine + select（那会留下无法回收的 goroutine）。
func (p *Pool) exchangeOne(ctx context.Context, u upstream.Upstream, req *dns.Msg) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, PerUpstreamTimeout)
	defer cancel()

	// 复制一份请求：并发竞速时多个 goroutine 共用同一个 *dns.Msg
	// 会产生数据竞争（Exchange 会改写 Msg.Id）。
	q := req.Copy()

	resp, err := u.Exchange(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("上游 %s 失败: %w", u.Address(), err)
	}
	return resp, nil
}

// Close 关闭池内全部上游连接。
//
// 用读写锁保护：热更新时旧池可能在查询中被关闭，
// 关闭后 Exchange 会返回错误而不是 panic。
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var errs []error
	for _, u := range p.ups {
		if c, ok := u.(interface{ Close() error }); ok {
			errs = append(errs, c.Close())
		}
	}
	return errors.Join(errs...)
}

// Cache 按上游地址集合缓存 Pool，避免每个用户各建一套连接。
type Cache struct {
	mu    sync.Mutex
	pools map[string]*Pool
	// refs 记录每个池被多少个用户引用，用于热更新时安全回收。
	refs map[string]int
}

// NewCache 创建一个上游池缓存。
func NewCache() *Cache {
	return &Cache{pools: map[string]*Pool{}, refs: map[string]int{}}
}

// Get 取得（必要时创建）某组上游地址对应的池。
func (c *Cache) Get(addrs []string) (*Pool, error) {
	key := strings.Join(normalizeAddrs(addrs), ",")
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pools[key]; ok {
		c.refs[key]++
		return p, nil
	}
	p, err := NewPool(addrs)
	if err != nil {
		return nil, err
	}
	c.pools[key] = p
	c.refs[key] = 1
	return p, nil
}

// Sweep 关闭不再被引用的池。
//
// 每次快照重载后调用：新快照已经把所有用户指到新池上，
// 旧池的引用计数在本次重载的 Get 阶段被重新累计，未被累计到的即可安全关闭。
func (c *Cache) Sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, p := range c.pools {
		if c.refs[key] == 0 {
			p.Close()
			delete(c.pools, key)
		}
	}
}

// BeginGeneration 在构建新 Registry 前调用，把引用计数清零以便 Sweep 判断。
func (c *Cache) BeginGeneration() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.refs {
		c.refs[k] = 0
	}
}

// Close 关闭全部池。
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, p := range c.pools {
		errs = append(errs, p.Close())
	}
	c.pools = map[string]*Pool{}
	c.refs = map[string]int{}
	return errors.Join(errs...)
}
