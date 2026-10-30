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
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// PerUpstreamTimeout 是单个上游的超时（文档 4.2）。
//
// 3s → 1.5s：国内公共 DNS 的正常 RTT 在 5–60ms，1.5s 已经宽出两个数量级。
// 原来的 3s 只在一种情况下有意义 —— 上游真的挂了，而此时用户已经等了 3 秒，
// 再多给 1.5 秒也救不回来，只是把「慢」拖成「更慢」。
//
// 实测依据：华为云上 24 小时有 365 次查询各耗时 3701ms，
// 正好等于 HedgeDelay(700) + 本值(3000) —— 就是两个上游双双超时的尾巴。
const PerUpstreamTimeout = 1500 * time.Millisecond

// Pool 是一组上游服务器。
type Pool struct {
	addrs []string
	ups   []upstream.Upstream
	// timeout 是单次上游交换的超时。查询路径用 PerUpstreamTimeout；
	// 诊断（管理后台的上游自测）要更长，见 NewPoolForTest。
	timeout time.Duration
	mu      sync.RWMutex
	// closed 标记池是否已关闭（Registry 热更新时会替换掉旧池）。
	closed bool

	// mode 是上游选择策略，见 model.UpstreamMode*。空串按 hedge 处理。
	mode string
	// scores 与 ups 一一对应，只给 fastest 模式用。
	scores []*upstreamScore
	// rr 是 load_balance 模式的轮询游标。
	rr atomic.Uint64
	// fallback 是备用上游池：只有主池全部失败时才启用。
	// 与 AdGuard Home 的 fallback_dns 同义。
	fallback *Pool
}

// NewPool 按地址列表创建一个上游池。
//
// 地址支持 "1.1.1.1"、"udp://1.1.1.1:53"、"tcp://..."、"tls://dns.google:853"、
// "https://dns.google/dns-query" —— 由 dnsproxy 的 upstream 包统一解析。
func NewPool(addrs []string, bootstrap []string) (*Pool, error) {
	return newPool(addrs, bootstrap, PerUpstreamTimeout)
}

// NewPoolForTest 创建一个用于连通性诊断的池，单上游超时放宽到 10 秒。
//
// 为什么不能沿用 3 秒：DoH/DoT 的首次交换包含 TLS 握手 + 证书校验，
// 跨境链路上经常超过 3 秒。查询路径上 3 秒是对的（有缓存、有并发竞速、
// 超时要尽快回退），但诊断场景用它会把「慢但能用」误报成「不可用」——
// 实测 AliDNS 的 DoH 在 3 秒超时下报失败，而线上查询一直是通的。
func NewPoolForTest(addrs []string, bootstrap []string) (*Pool, error) {
	return newPool(addrs, bootstrap, 10*time.Second)
}

func newPool(addrs []string, bootstrap []string, timeout time.Duration) (*Pool, error) {
	opts := &upstream.Options{
		Timeout: timeout,
		// 过滤「客户端主动取消」类日志（详情见 logger.go）。
		// 不设的话 dnsproxy 用 slog.Default，会把 context canceled 记成 ERROR。
		Logger: upstreamLogger(nil),
	}
	// 引导解析器：DoH/DoT 上游要先解析主机名才能连。
	// 不给的话 dnsproxy 会走系统解析器，容器/最小化系统里常常没有可用解析器，
	// 表现就是「上游配好了却全部超时」，而且日志里只说解析失败，看不出缺引导。
	if br := bootstrapResolver(bootstrap); br != nil {
		opts.Bootstrap = br
	}
	p := &Pool{addrs: normalizeAddrs(addrs), timeout: timeout}
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
	// scores 必须与 ups 一一对应，fastest 模式按下标取用。
	p.scores = make([]*upstreamScore, len(p.ups))
	for i := range p.scores {
		p.scores[i] = &upstreamScore{}
	}
	return p, nil
}

// SetMode 设置上游选择策略。非法值按 hedge 处理（保持旧行为）。
//
// 在池建好之后单独设置而不是塞进构造函数：调用方（Registry）拿到的
// 配置是热更新的，模式可能在没有变化上游地址的情况下单独改变，
// 这时不该重建整个池（会丢掉 DoH 的 TLS 会话复用）。
func (p *Pool) SetMode(mode string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

// Mode 返回当前策略，供日志与诊断使用。
func (p *Pool) Mode() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetFallback 挂上备用池：主池全部失败时才启用。
func (p *Pool) SetFallback(fb *Pool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.fallback = fb
	p.mu.Unlock()
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
// 取 300ms 的理由：国内到公共 DNS 的正常 RTT 在 5–60ms，
// 300ms 已经是正常值的 5 倍以上，足以说明主上游有问题；
// 同时又足够大，不会因为一次抖动就把备用上游白白拉起来。
//
// 原来是 700ms：故障时「主上游慢 + 对冲晚」叠加，最坏路径变成
// 700 + 3000 = 3.7s。两个值一起收紧后最坏路径降到 300 + 1500 = 1.8s。
const HedgeDelay = 300 * time.Millisecond

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
	resp, _, err := p.ExchangeWithSource(ctx, req)
	return resp, err
}

// ExchangeWithSource 与 Exchange 相同，额外返回实际应答的上游地址。
//
// 多上游竞速时「到底是谁答的」只能在这里拿到 —— 出问题要判断是主上游
// 挂了还是 fallback 在顶，日志里没有这个信息就只能靠猜。
func (p *Pool) ExchangeWithSource(ctx context.Context, req *dns.Msg) (*dns.Msg, string, error) {
	if p == nil || len(p.ups) == 0 {
		return nil, "", errors.New("上游池为空")
	}
	p.mu.RLock()
	closed := p.closed
	ups := p.ups
	p.mu.RUnlock()
	if closed {
		return nil, "", errors.New("上游池已关闭")
	}

	// 按配置的策略分发。备用池只在主池彻底失败后才启用，
	// 而不是参与首选竞速 —— 它的用途是「主上游全挂」时的兜底。
	resp, src, err := p.dispatch(ctx, ups, req)
	if err == nil {
		return resp, src, nil
	}
	if fb := p.fallback; fb != nil {
		if r2, s2, e2 := fb.ExchangeWithSource(ctx, req); e2 == nil {
			return r2, s2, nil
		}
	}
	return nil, "", err
}

// dispatch 按 mode 选择具体策略。
func (p *Pool) dispatch(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	// 单个上游：直接查，没有可竞速的对象。
	if len(ups) == 1 {
		resp, err := p.exchangeOne(ctx, ups[0], req)
		return resp, ups[0].Address(), err
	}
	switch p.mode {
	case "parallel":
		return p.exchangeParallel(ctx, ups, req)
	case "fastest":
		return p.exchangeFastest(ctx, ups, req)
	case "load_balance":
		return p.exchangeLoadBalance(ctx, ups, req)
	}
	// 默认 hedge：保持旧行为。
	return p.exchangeHedge(ctx, ups, req)
}

// exchangeHedge 是旧策略：只问主上游，HedgeDelay 后才拉起其余上游竞速。
//
// 保留它的原因：出流量最省。但在「主上游够快但答案不对」的场景下，
// 备用上游永远不会上场 —— 线上实测 4830 次查询备用只接到 8 次。
// 想真正用上备用请选 parallel 或 load_balance。
func (p *Pool) exchangeHedge(ctx context.Context, ups []upstream.Upstream, req *dns.Msg) (*dns.Msg, string, error) {
	type result struct {
		resp *dns.Msg
		src  string
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
		go func(u upstream.Upstream) {
			r, e := p.exchangeOne(raceCtx, u, req)
			ch <- result{r, u.Address(), e}
		}(u)
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
			return nil, "", ctx.Err()

		case <-hedge.C:
			if !hedged {
				hedged = true
				launchRemaining()
			}

		case r := <-ch:
			pending--
			if r.err == nil && r.resp != nil {
				cancel() // 拿到结果立刻取消其余在途请求
				return r.resp, r.src, nil
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
				return nil, "", lastErr
			}
		}
	}
}

// exchangeOne 向单个上游发起一次查询，强制单上游超时。
//
// dnsproxy v0.86 的 Upstream.Exchange 接受 context，因此超时直接交给它处理，
// 不需要再套一层 goroutine + select（那会留下无法回收的 goroutine）。
func (p *Pool) exchangeOne(ctx context.Context, u upstream.Upstream, req *dns.Msg) (*dns.Msg, error) {
	tmo := p.timeout
	if tmo <= 0 {
		tmo = PerUpstreamTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, tmo)
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
func (c *Cache) Get(addrs []string, bootstrap []string) (*Pool, error) {
	// 池的 key 必须包含引导地址：同一组上游配不同引导是不同行为，
	// 只按上游做 key 会让后配的引导被先建的池静默复用。
	//
	// 不把 mode 放进 key：mode 可以在不改上游地址的情况下单独变化，
	// 那时应当就地更新已有池（保住 DoH 的 TLS 会话复用），
	// 而不是重建一个新池把旧连接全丢掉。见 SetMode。
	key := strings.Join(normalizeAddrs(addrs), ",") + "|" + strings.Join(bootstrap, ",")
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pools[key]; ok {
		c.refs[key]++
		return p, nil
	}
	p, err := NewPool(addrs, bootstrap)
	if err != nil {
		return nil, err
	}
	c.pools[key] = p
	c.refs[key] = 1
	return p, nil
}

// GetWithMode 与 Get 相同，额外套用上游选择策略与备用服务器。
//
// 备用池单独走一次 Get，这样它能被多个用户共享（引用计数在 Sweep 里
// 统一回收），而不是每个用户各建一份。
func (c *Cache) GetWithMode(addrs, fallback []string, mode string, bootstrap []string) (*Pool, error) {
	p, err := c.Get(addrs, bootstrap)
	if err != nil {
		return nil, err
	}
	p.SetMode(mode)
	if len(fallback) > 0 {
		// 备用池建失败不应让主池也不可用：主上游能用就还能解析。
		if fb, ferr := c.Get(fallback, bootstrap); ferr == nil {
			p.SetFallback(fb)
		}
	} else {
		p.SetFallback(nil)
	}
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

// bootstrapResolver 用配置里的引导 DNS 服务器解析上游主机名。
//
// 这里绝不能图省事用 upstream.StaticResolver —— 那个类型不是「要去问的
// DNS 服务器」，而是**静态的主机名→IP 映射**：它会把任意主机名都解析成
// 列表里给的那几个地址。实测踩过这个坑：配 [223.5.5.5] 时「能通」纯属巧合
// （223.5.5.5 恰好就是 dns.alidns.com 的 IP），配 [119.29.29.29] 时就去连
// 119.29.29.29:443 然后超时，表现为「上游配好了却全部失败」。
//
// 正确做法是给一个真正的解析器：net.Resolver 配上自定义 Dial，把查询打到
// 引导服务器上。net.Resolver 本身实现了 dnsproxy 的 Resolver 接口。
//
// 只接受 IP 字面量：用域名或 DoH 地址做引导是循环依赖。
func bootstrapResolver(addrs []string) upstream.Resolver {
	server := ""
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if host, _, err := net.SplitHostPort(a); err == nil {
			a = host
		}
		if ip, err := netip.ParseAddr(a); err == nil && !ip.IsUnspecified() {
			server = ip.String()
			break
		}
	}
	if server == "" {
		return nil
	}
	addr := net.JoinHostPort(server, "53")
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// 固定走 udp：引导只查 A/AAAA，TCP 回退由 Go 解析器自己处理。
			d := &net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", addr)
		},
	}
}
