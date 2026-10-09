package dns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/time/rate"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/rules"
	"github.com/50521136/aegis-dns/server/internal/stats"
)

// IdentifyHint 是协议层已经识别出的租户线索。
//
// DoT 的 SNI、DoH 的 Host / 路径段在建立连接或解析请求头时就已经拿到，
// 不必等到 DNS 报文解析。把它显式传下来，比让 handler 去猜更可靠。
//
// 线索是**有序**的：ClientID 优先，取不到再退到 AltClientID。
// 两者都在来源 IP 之前 —— 它们是显式指定的租户，而来源 IP 只是环境推断。
type IdentifyHint struct {
	// ClientID 来自 SNI（DoT）或 Host 首段（DoH）。
	ClientID string
	// Source 标识线索来源，仅用于日志与调试：sni | host | path | edns。
	Source string
	// AltClientID 是次级线索，目前只有 DoH 会填：Host 首段不是合法 client_id
	// 时（例如客户端只能访问 https://<域名>/dns-query/<client_id> 这种路径式
	// 地址，此时 Host 是裸域名），用路径段兜底。
	//
	// 必须有这一级，否则路径式 DoH 会静默失效：裸域名 Host 的首段会被当成
	// client_id 查一次、查不到，然后直接掉到来源 IP，路径段永远轮不到。
	AltClientID string
	// AltSource 是 AltClientID 的来源标识：path | edns。
	AltSource string
}

// Handler 实现 DNS 查询管线（文档第四章）。
type Handler struct {
	registry *Registry
	// cache 用 atomic.Pointer 持有：管理员改缓存容量时需要整体替换，
	// 而查询路径上可能有成百上千个 goroutine 正在读它。
	cache     atomic.Pointer[Cache]
	collector *stats.Collector
	log       *slog.Logger

	// sem 限制并发处理的查询数（文档 4.2 max_goroutines）。
	sem chan struct{}
	// inflight 是当前在处理中的查询数，用于系统状态展示。
	inflight atomic.Int64

	// limiters 是每租户的令牌桶，按需创建。
	limiters sync.Map

	// 运行时可变参数从快照读取，这里缓存一份以便热路径少一次接口调用。
	defaults atomic.Pointer[model.Defaults]

	// 指标
	rejected     atomic.Int64
	panics       atomic.Int64
	upstreamErrs atomic.Int64
}

// NewHandler 创建查询处理器。
func NewHandler(reg *Registry, cache *Cache, collector *stats.Collector, log *slog.Logger) *Handler {
	h := &Handler{
		registry:  reg,
		collector: collector,
		log:       log,
		sem:       make(chan struct{}, 300),
	}
	if cache == nil {
		cache = NewCache(10000, 60, 86400)
	}
	h.cache.Store(cache)
	d := model.Defaults{MaxInflight: 300, QueryTimeout: 10000, BlockingMode: model.BlockingNullIP}
	h.defaults.Store(&d)
	return h
}

// Cache 返回当前缓存（系统状态展示用）。
func (h *Handler) Cache() *Cache { return h.cache.Load() }

// ResizeCache 在缓存容量或 TTL 区间变化时重建缓存。
//
// 重建而不是原地调整：分片 LRU 的容量是构造时按分片均分的，
// 原地改会让某些分片超限、另一些饿死。重建的代价只是丢一次缓存热度。
func (h *Handler) ResizeCache(size, minTTL, maxTTL int) {
	cur := h.cache.Load()
	if cur == nil {
		h.cache.Store(NewCache(size, minTTL, maxTTL))
		return
	}
	if cur.size == size && cur.minTTL == minTTL && cur.maxTTL == maxTTL {
		return
	}
	h.cache.Store(NewCache(size, minTTL, maxTTL))
}

// ApplyDefaults 在快照重载后刷新运行时参数。
func (h *Handler) ApplyDefaults(d model.Defaults) {
	if d.MaxInflight <= 0 {
		d.MaxInflight = 300
	}
	if d.QueryTimeout <= 0 {
		d.QueryTimeout = 10000
	}
	h.defaults.Store(&d)

	// 并发信号量容量变化时重建（旧信号量在途查询仍能用，只是不再被使用）。
	if cap(h.sem) != d.MaxInflight {
		h.sem = make(chan struct{}, d.MaxInflight)
	}
	if c := h.cache.Load(); c != nil {
		c.minTTL = d.CacheMinTTL
		c.maxTTL = d.CacheMaxTTL
	}
	if h.collector != nil {
		h.collector.SetQueryLogEnabled(d.EnableQueryLog)
	}
}

// ServeDNS 实现 dns.Handler 接口（UDP/TCP 路径）。
func (h *Handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	h.Handle(w, req, nil)
}

// Handle 执行完整查询管线。
func (h *Handler) Handle(w dns.ResponseWriter, req *dns.Msg, hint *IdentifyHint) {
	start := time.Now()

	// Panic 保护：任何一个查询 panic 都不能让 dnsd 进程退出。
	// 这是「DNS 可用性优先」的兜底（文档 4.2）。
	defer func() {
		if r := recover(); r != nil {
			h.panics.Add(1)
			h.log.Error("查询处理 panic", "panic", r, "qname", firstQuestion(req))
			if w != nil {
				_ = w.WriteMsg(servfailResponse(req))
			}
		}
	}()

	if w == nil || req == nil {
		return
	}

	d := *h.defaults.Load()

	// Stage 2 前置校验：并发闸门。
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		// 并发已满：立刻回 SERVFAIL 而不是排队。
		// 排队会让延迟雪崩，客户端更可能超时后重试，形成正反馈。
		h.rejected.Add(1)
		_ = w.WriteMsg(servfailResponse(req))
		return
	}
	h.inflight.Add(1)
	defer h.inflight.Add(-1)

	if len(req.Question) == 0 {
		_ = w.WriteMsg(servfailResponse(req))
		return
	}
	q := req.Question[0]

	// Stage 1 用户识别。
	snap := h.registry.Load()
	user := h.identify(snap, w, req, hint)
	if user == nil {
		user = snap.Fallback()
	}
	isFallback := user.UserID == ""

	// Stage 2 前置校验：租户是否启用。
	if !isFallback && !user.Enabled {
		h.record(user.UserID, q, "blocked", "REFUSED", time.Since(start), w)
		_ = w.WriteMsg(refusedResponse(req))
		return
	}

	// 未识别请求的 refused 策略。
	if isFallback && d.FallbackPolicy == model.FallbackRefused {
		h.record("", q, "blocked", "REFUSED", time.Since(start), w)
		_ = w.WriteMsg(refusedResponse(req))
		return
	}

	// 限流（按租户）。
	if d.RateLimitQPS > 0 {
		if !h.allow(user.UserID, d.RateLimitQPS) {
			h.record(user.UserID, q, "blocked", "REFUSED", time.Since(start), w)
			_ = w.WriteMsg(refusedResponse(req))
			return
		}
	}

	// 特殊域名：本地回环、DoH 探针。
	if resp, handled := h.specialDomain(req); handled {
		h.record(user.UserID, q, "allowed", dns.RcodeToString[resp.Rcode], time.Since(start), w)
		_ = w.WriteMsg(resp)
		return
	}

	// Stage 3 缓存查询。
	cache := h.cache.Load()
	if key, ok := CacheKeyFrom(user.UserID, req); ok && cache != nil {
		if msg, hit := cache.Get(key); hit {
			// 必须把报文 ID 改回本次请求的 ID。
			//
			// 缓存里存的是首次应答的副本，它带着「当时那个请求」的随机 ID。
			// 原样返回会让客户端认为应答与请求不匹配而直接丢弃，
			// 表现就是查询超时（dig 会明确提示 "ID mismatch"）。
			// 这个 bug 只在第二次查同一域名时才出现，所以极易漏测。
			msg.Id = req.Id
			h.record(user.UserID, q, "cached", dns.RcodeToString[msg.Rcode], time.Since(start), w)
			_ = w.WriteMsg(msg)
			return
		}
	}

	// Stage 4 规则匹配。
	rs := user.Rules.Load()
	if rs != nil && !rs.Empty() {
		decision := rs.Match(q.Name, q.Qtype)
		switch decision.Action {
		case rules.ActionAllow:
			// 白名单：放行到上游，跳过后续拦截判断。
		case rules.ActionBlock:
			resp := buildBlockResponse(req, d.BlockingMode, d.CustomIP)
			h.record(user.UserID, q, "blocked", dns.RcodeToString[resp.Rcode], time.Since(start), w)
			_ = w.WriteMsg(resp)
			return
		case rules.ActionRewrite:
			resp := rewriteResponse(req, decision.Rewrite)
			h.record(user.UserID, q, "rewritten", dns.RcodeToString[resp.Rcode], time.Since(start), w)
			_ = w.WriteMsg(resp)
			return
		}
	}

	// Stage 5 上游解析。
	pool := user.Pool
	if pool == nil {
		pool = snap.Fallback().Pool
	}
	if pool == nil {
		h.upstreamErrs.Add(1)
		_ = w.WriteMsg(servfailResponse(req))
		return
	}

	timeout := time.Duration(d.QueryTimeout) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resp, err := pool.Exchange(ctx, req)
	if err != nil {
		h.upstreamErrs.Add(1)
		h.log.Warn("上游解析失败", "qname", q.Name, "qtype", dns.TypeToString[q.Qtype], "err", err)
		h.record(user.UserID, q, "upstream", "SERVFAIL", time.Since(start), w)
		_ = w.WriteMsg(servfailResponse(req))
		return
	}

	// 上游返回的 ID 可能被改写，统一回填成请求 ID。
	resp.Id = req.Id
	resp.Compress = true

	// Stage 6 响应后处理：写缓存 + 统计。
	if key, ok := CacheKeyFrom(user.UserID, req); ok && cache != nil {
		cache.Put(key, resp, req)
	}
	h.record(user.UserID, q, "allowed", dns.RcodeToString[resp.Rcode], time.Since(start), w)
	_ = w.WriteMsg(resp)
}

// identify 按文档 3.2 的优先级识别租户。
//
// 顺序：SNI/Host/Path（协议层线索）→ 来源 IP → EDNS → 兜底。
// 识别失败一律落到 fallback，绝不猜测归属（文档第十章「识别隔离」）。
func (h *Handler) identify(snap *registrySnapshot, w dns.ResponseWriter, req *dns.Msg, hint *IdentifyHint) *UserRuntime {
	if hint != nil && hint.ClientID != "" {
		if u := snap.LookupClientID(hint.ClientID); u != nil {
			return u
		}
		// 线索给了但查不到（例如用户已删）：不猜测，继续往下走。
	}
	if hint != nil && hint.AltClientID != "" {
		if u := snap.LookupClientID(hint.AltClientID); u != nil {
			return u
		}
	}
	if w != nil {
		if ip := remoteIP(w.RemoteAddr()); ip != nil {
			if u := snap.LookupIP(ip); u != nil {
				return u
			}
		}
	}
	if cid := ednsClientID(req); cid != "" {
		if u := snap.LookupClientID(cid); u != nil {
			return u
		}
	}
	return nil
}

// specialDomain 处理必须本地应答的特殊域名。
//
// 返回 handled=true 表示已构造应答，不应再走后续管线。
func (h *Handler) specialDomain(req *dns.Msg) (*dns.Msg, bool) {
	q := req.Question[0]
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))

	switch name {
	case "localhost":
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.RecursionAvailable = true
		switch q.Qtype {
		case dns.TypeA:
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.IPv4(127, 0, 0, 1).To4(),
			})
		case dns.TypeAAAA:
			resp.Answer = append(resp.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
				AAAA: net.IPv6loopback.To16(),
			})
		}
		return resp, true

	case "use-application-dns.net":
		// Firefox 的 DoH 探针域名：返回 NXDOMAIN 是「本网络不欢迎 DoH」的
		// 标准信号，能让浏览器回落到系统 DNS（也就是我们）。
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeNameError)
		resp.RecursionAvailable = true
		return resp, true
	}

	// PTR 反查交给上游，不做本地处理（内网反查通常有专门的上游）。
	return nil, false
}

// allow 按租户做令牌桶限流。
func (h *Handler) allow(userID string, qps int) bool {
	if userID == "" {
		return true
	}
	v, ok := h.limiters.Load(userID)
	if !ok {
		// burst 取 qps 的 2 倍但不小于 10，容忍瞬时突发。
		burst := qps * 2
		if burst < 10 {
			burst = 10
		}
		lim := rate.NewLimiter(rate.Limit(qps), burst)
		v, _ = h.limiters.LoadOrStore(userID, lim)
	}
	return v.(*rate.Limiter).Allow()
}

// record 记录一次统计。
func (h *Handler) record(userID string, q dns.Question, action, rcode string, dur time.Duration, w dns.ResponseWriter) {
	if h.collector == nil {
		return
	}
	client := ""
	if w != nil {
		client = maskIP(remoteIP(w.RemoteAddr()))
	}
	h.collector.Record(stats.Event{
		UserID:    userID,
		Domain:    strings.ToLower(strings.TrimSuffix(q.Name, ".")),
		QType:     dns.TypeToString[q.Qtype],
		RCode:     rcode,
		Action:    action,
		Client:    client,
		LatencyMS: dur.Milliseconds(),
	})
}

// Metrics 返回 dnsd 的运行指标。
func (h *Handler) Metrics() map[string]any {
	return map[string]any{
		"inflight":        h.inflight.Load(),
		"max_inflight":    cap(h.sem),
		"rejected":        h.rejected.Load(),
		"panics":          h.panics.Load(),
		"upstream_errors": h.upstreamErrs.Load(),
	}
}

// remoteIP 从 net.Addr 提取 IP。
func remoteIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP
	case *net.TCPAddr:
		return a.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	return net.ParseIP(host)
}

// maskIP 把客户端 IP 脱敏到 /24（IPv4）或 /48（IPv6）。
//
// 日志脱敏是文档 10.3 的硬要求：查询日志用于排障与统计，
// 不需要精确到单个设备，保留网段即可定位来源。
func maskIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	return fmt.Sprintf("%02x%02x:%02x%02x:%02x%02x::/48", v6[0], v6[1], v6[2], v6[3], v6[4], v6[5])
}

// firstQuestion 返回第一个问题名（日志用）。
func firstQuestion(req *dns.Msg) string {
	if req == nil || len(req.Question) == 0 {
		return ""
	}
	return req.Question[0].Name
}

// ednsClientID 从 EDNS0 Option 65001 里读取 client_id（文档 3.2 的兜底识别）。
func ednsClientID(req *dns.Msg) string {
	opt := req.IsEdns0()
	if opt == nil {
		return ""
	}
	for _, o := range opt.Option {
		local, ok := o.(*dns.EDNS0_LOCAL)
		if !ok {
			continue
		}
		if local.Code != 65001 {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(string(local.Data)))
		if v != "" {
			return v
		}
	}
	return ""
}
