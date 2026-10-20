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
	// Host 是请求实际带的完整主机名（DoT 的 SNI / DoH 的 Host，已去掉端口）。
	// 用来判断「这次请求打的是裸域名还是子域名」—— 裸域名能否使用由
	// 全局设置 AllowRootDomain 决定。
	Host string
	// Proto 是接入协议：udp | tcp | dot | doh。为空时按 RemoteAddr 的网络类型推断。
	Proto string
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
	rejected atomic.Int64
	panics   atomic.Int64
	// quotaRefused 统计因超出每日请求量上限被拒的次数
	quotaRefused atomic.Int64
	// quota 是每日请求量计数器（会员档位）
	quota        *dailyQuota
	upstreamErrs atomic.Int64
}

// NewHandler 创建查询处理器。
func NewHandler(reg *Registry, cache *Cache, collector *stats.Collector, log *slog.Logger) *Handler {
	h := &Handler{
		registry:  reg,
		collector: collector,
		log:       log,
		sem:       make(chan struct{}, 300),
		quota:     newDailyQuota(),
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
	// det 承载日志详情，贯穿整条管线逐步填充。
	det := qdetail{Proto: protoOf(hint, w), ECS: ecsFrom(req)}

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
	applyECS := d.EnableECS
	applyDNSSEC := d.EnableDNSSEC

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

	// Stage 2 前置校验：每日请求量上限（会员档位）。
	//
	// 判定放在「识别出用户之后、任何解析动作之前」：
	// 超额请求不该消耗上游、缓存与规则匹配的任何资源。
	// 回 REFUSED 而不是 SERVFAIL —— 前者是「明确拒绝」，客户端会展示为
	// 解析失败而不是重试；后者会诱发客户端反复重试，把上限越撞越死。
	if !isFallback && user.DailyQueries > 0 {
		if !h.quota.allow(user.UserID, user.DailyQueries, start) {
			h.quotaRefused.Add(1)
			h.record(user.UserID, q, "blocked", "REFUSED", time.Since(start), w, qdetail{
				Proto: det.Proto, ECS: det.ECS,
			})
			_ = w.WriteMsg(refusedResponse(req))
			return
		}
	}

	// 裸域名开关。
	//
	// 判断依据必须是「主机名是不是裸域名」，不能挂在 isFallback 上：
	// 客户端 IP 登记过的话，裸域名请求会被来源 IP 认成租户，isFallback 为假，
	// 开关就等于失效了。
	//
	// 但 DoH 路径式（https://<域名>/dns-query/<client_id>）的 Host 天生就是裸域名，
	// 那种请求带了路径段线索，属于合法用法，不能拦 —— 所以要求 ClientID 与
	// AltClientID 都为空才算「真的没带身份」。
	if hint != nil && hint.Host != "" && d.Domain != "" &&
		strings.EqualFold(hint.Host, d.Domain) &&
		hint.ClientID == "" && hint.AltClientID == "" && !d.AllowRootDomain {
		h.record("", q, "blocked", "REFUSED", time.Since(start), w)
		_ = w.WriteMsg(refusedResponse(req))
		return
	}

	// 带了 client_id 但查不到对应租户：直接拒绝。
	//
	// 判断必须独立于来源 IP 兜底。之前挂在 isFallback 上，结果客户端 IP
	// 登记过的话会被 IP 兜底认成租户、绕过这条检查 —— 用户拿着写错的
	// 子域名照样能用，永远发现不了自己配错了。
	//
	// 语义上也该拒绝：这不是「没带身份」，而是「带了一个不存在的身份」
	// （用户被删、地址抄错、旧二维码）。放行等于让陌生人白嫖解析服务。
	if hint != nil && (hint.ClientID != "" || hint.AltClientID != "") {
		matched := false
		if hint.ClientID != "" && snap.LookupClientID(hint.ClientID) != nil {
			matched = true
		}
		if !matched && hint.AltClientID != "" && snap.LookupClientID(hint.AltClientID) != nil {
			matched = true
		}
		if !matched {
			h.record("", q, "blocked", "REFUSED", time.Since(start), w)
			_ = w.WriteMsg(refusedResponse(req))
			return
		}
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
		det.Answer = answerFrom(resp)
		h.record(user.UserID, q, "allowed", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
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
			det.Answer = answerFrom(msg)
			h.record(user.UserID, q, "cached", dns.RcodeToString[msg.Rcode], time.Since(start), w, det)
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
			det.Answer = answerFrom(resp)
			h.record(user.UserID, q, "blocked", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
			_ = w.WriteMsg(resp)
			return
		case rules.ActionRewrite:
			resp := rewriteResponse(req, decision.Rewrite)
			det.Answer = answerFrom(resp)
			h.record(user.UserID, q, "rewritten", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
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

	// EDNS / DNSSEC：这两个开关只在「要发出去的报文」上动手，
	// 不影响缓存键 —— ECS 开了以后缓存仍按域名共享，
	// 所以严格来说 ECS 的 CDN 就近效果会被缓存削弱（第一个用户的网段
	// 决定了这次应答，后续用户直接命中）。要彻底生效得把网段并进缓存键，
	// 那会让命中率再掉一大截，这里选择「先不破坏命中率」。
	upReq := req
	if applyDNSSEC || applyECS {
		upReq = req.Copy()
		if applyDNSSEC {
			// 置 DO 位：让上游把 DNSSEC 记录一起回来，并把验证结果放进 AD 位。
			upReq.SetEdns0(4096, true)
		}
		if applyECS {
			// 只在客户端没自带 ECS 时注入，避免覆盖客户端自己的意图。
			if !hasECS(upReq) {
				if sub := clientSubnet(w); sub != nil && upReq.IsEdns0() != nil {
					upReq.IsEdns0().Option = append(upReq.IsEdns0().Option, sub)
				}
			}
		}
	}
	resp, upSrc, err := pool.ExchangeWithSource(ctx, upReq)
	det.Upstream = upSrc
	if err != nil {
		h.upstreamErrs.Add(1)
		h.log.Warn("上游解析失败", "qname", q.Name, "qtype", dns.TypeToString[q.Qtype], "err", err)
		h.record(user.UserID, q, "upstream", "SERVFAIL", time.Since(start), w, det)
		_ = w.WriteMsg(servfailResponse(req))
		return
	}

	// 上游返回的 ID 可能被改写，统一回填成请求 ID。
	resp.Id = req.Id
	resp.Compress = true

	// AD 位只在客户端自己要了 DNSSEC 时才透传。
	//
	// 我们可能替客户端置了 DO 位去问上游，上游因此做了验证并置 AD；
	// 但客户端没要过 DNSSEC，把这个 AD 转发过去等于凭空给了一个
	// 「已校验」的承诺 —— 而客户端根本没有能力也没有意愿去用它。
	if applyDNSSEC && !clientWantsDNSSEC(req) {
		resp.AuthenticatedData = false
	}

	// Stage 6 响应后处理：写缓存 + 统计。
	if key, ok := CacheKeyFrom(user.UserID, req); ok && cache != nil {
		cache.Put(key, resp, req)
	}
	det.Answer = answerFrom(resp)
	h.record(user.UserID, q, "allowed", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
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
// qdetail 是一条查询日志的补充信息。
//
// 列表页只显示 domain/action，这些字段只在「点开单条」时才看得到 ——
// 但它们必须在解析时采集，事后没有任何地方能补回来。
type qdetail struct {
	Proto      string // udp|tcp|dot|doh
	ECS        string // EDNS Client Subnet
	Upstream   string // 实际应答的上游
	Answer     string // 应答内容（截断）
	ClientName string // 客户端备注名
}

// maxAnswerLen 是应答内容落库前的截断长度。
//
// 一条 A 记录几十字节，但 ANY 或大 TXT 能到几 KB；日志表按天增长，
// 不截断会让库体积失控。详情面板也不需要完整应答。
const maxAnswerLen = 512

func (h *Handler) record(userID string, q dns.Question, action, rcode string, dur time.Duration, w dns.ResponseWriter, extra ...qdetail) {
	if h.collector == nil {
		return
	}
	client := ""
	if w != nil {
		client = maskIP(remoteIP(w.RemoteAddr()))
	}
	domain := strings.ToLower(strings.TrimSuffix(q.Name, "."))

	var det qdetail
	if len(extra) > 0 {
		det = extra[0]
	}
	// 协议没显式给就按网络类型推断；DoT/DoH 都跑在 TCP 上，
	// 只能由调用方（SNI/Host 那一层）标出来。
	if det.Proto == "" && w != nil {
		det.Proto = w.RemoteAddr().Network()
	}
	if len(det.Answer) > maxAnswerLen {
		det.Answer = det.Answer[:maxAnswerLen] + "…"
	}

	h.collector.Record(stats.Event{
		UserID:     userID,
		Domain:     domain,
		QType:      dns.TypeToString[q.Qtype],
		RCode:      rcode,
		Action:     action,
		Client:     client,
		LatencyMS:  dur.Milliseconds(),
		Proto:      det.Proto,
		ECS:        det.ECS,
		Upstream:   det.Upstream,
		Answer:     det.Answer,
		Tracker:    matchTracker(domain),
		ClientName: det.ClientName,
	})
}

// ecsFrom 提取 EDNS Client Subnet 选项，形如 "1.2.3.0/24"。
func ecsFrom(req *dns.Msg) string {
	if req == nil {
		return ""
	}
	opt := req.IsEdns0()
	if opt == nil {
		return ""
	}
	for _, o := range opt.Option {
		if sub, ok := o.(*dns.EDNS0_SUBNET); ok {
			return sub.Address.String() + "/" + itoa(int(sub.SourceNetmask))
		}
	}
	return ""
}

// answerFrom 把应答记录压成一行，供详情面板显示。
func answerFrom(resp *dns.Msg) string {
	if resp == nil || len(resp.Answer) == 0 {
		return ""
	}
	parts := make([]string, 0, len(resp.Answer))
	for i, rr := range resp.Answer {
		if i >= 8 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, rr.String())
	}
	return strings.Join(parts, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
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

// protoOf 判断接入协议。
//
// DoT 与 DoH 都跑在 TCP 上，光看 RemoteAddr 只能得到 tcp ——
// 必须靠 identify 阶段标出来的线索来源区分：SNI 是 DoT，
// Host/路径是 DoH（面板、移动端配置都走 DoH）。
func protoOf(hint *IdentifyHint, w dns.ResponseWriter) string {
	if hint != nil {
		if hint.Proto != "" {
			return hint.Proto
		}
		switch hint.Source {
		case "sni":
			return "dot"
		case "host", "path":
			return "doh"
		}
	}
	if w != nil {
		return w.RemoteAddr().Network()
	}
	return ""
}

// clientSubnet 按客户端来源 IP 构造 /24（IPv4）或 /56（IPv6）的 ECS 选项。
//
// 只给到 /24 而不是精确 IP：ECS 的目的只是让 CDN 判断大致区域，
// 精确到单 IP 会把用户地址暴露得更细，且缓存分片会碎到几乎没用。
//
// 私有地址不注入 —— 上游拿到 192.168.x.x 只会当成无效数据丢掉，
// 还可能让某些权威服务器返回错误结果。
func clientSubnet(w dns.ResponseWriter) *dns.EDNS0_SUBNET {
	ip := remoteIP(w.RemoteAddr())
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return nil
	}
	sub := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceScope: 0}
	if v4 := ip.To4(); v4 != nil {
		sub.Family = 1
		sub.SourceNetmask = 24
		sub.Address = v4.Mask(net.CIDRMask(24, 32))
		return sub
	}
	sub.Family = 2
	sub.SourceNetmask = 56
	sub.Address = ip.Mask(net.CIDRMask(56, 128))
	return sub
}

// clientWantsDNSSEC 判断客户端自己是否请求了 DNSSEC。
func clientWantsDNSSEC(req *dns.Msg) bool {
	if req.AuthenticatedData || req.CheckingDisabled {
		return true
	}
	opt := req.IsEdns0()
	return opt != nil && opt.Do()
}
