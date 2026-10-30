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

// semaphore 是一代不可变的并发信号量。重载时以指针换代，已取得名额的请求
// 持有原通道的闭包释放，避免更新期间对新旧通道交叉收发。
type semaphore chan struct{}

func acquireSemaphore(current *atomic.Pointer[semaphore]) (func(), bool) {
	sem := current.Load()
	if sem == nil {
		return func() {}, false
	}
	select {
	case *sem <- struct{}{}:
		return func() { <-*sem }, true
	default:
		return func() {}, false
	}
}

// Handler 实现查询处理管线（文档第四章）。
type Handler struct {
	registry *Registry
	// cache 用 atomic.Pointer 持有：管理员改缓存容量时需要整体替换，
	// 而查询路径上可能有成百上千个 goroutine 正在读它。
	cache     atomic.Pointer[Cache]
	collector *stats.Collector
	log       *slog.Logger

	sem atomic.Pointer[semaphore]
	// inflight 是当前在处理中的查询数，用于系统状态展示。
	inflight atomic.Int64

	// flight 合并「同一域名并发查询」，避免冷启动时一批相同请求
	// 各自打上游（详见 inflight.go）。
	flight *flightGroup

	// limiters 是每租户的令牌桶，按需创建。
	limiters sync.Map

	// 运行时可变参数从快照读取，这里缓存一份以便热路径少一次接口调用。
	defaults atomic.Pointer[model.Defaults]

	// 指标
	rejected atomic.Int64
	panics   atomic.Int64
	// protosRejected 统计因协议被关闭而拒绝的查询数
	protosRejected atomic.Int64

	// 缓存未命中的分类计数。
	//
	// 只知道「命中率 55%」无法判断该动什么：冷启动多说明域名长尾（容量/TTL），
	// 过期多说明上游 TTL 短（该调 minTTL），被并发合并的多说明是并发击穿
	// （singleflight 已经挡住上游压力，不该再拿命中率当目标）。
	// 三者对应的动作完全不同，所以分开记。
	cacheMissAbsent  atomic.Int64
	cacheMissExpired atomic.Int64
	cacheMerged      atomic.Int64
	// quotaRefused 统计因超出每日请求量上限被拒的次数
	quotaRefused atomic.Int64
	// fallbackRefused 统计「识别不出租户」而被兜底策略拒绝的查询数。
	//
	// 这类请求不写查询日志（见下方说明），但仍要能看出「有没有人在乱打」，
	// 所以留一个纯计数器：面板/排查时能拿到数字，而不会污染用户排行榜。
	fallbackRefused atomic.Int64
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
		quota:     newDailyQuota(),
		flight:    newFlightGroup(),
	}
	if cache == nil {
		cache = NewCache(10000, 60, 86400)
	}
	h.cache.Store(cache)
	d := model.Defaults{MaxInflight: 300, QueryTimeout: 10000, BlockingMode: model.BlockingNullIP}
	h.defaults.Store(&d)
	sem := semaphore(make(chan struct{}, d.MaxInflight))
	h.sem.Store(&sem)
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

	// 不可变信号量通过原子指针换代。请求 acquire 时固定持有该代，release
	// 也归还同一代，避免热更新期间错误释放到新通道。
	if sem := h.sem.Load(); sem == nil || cap(*sem) != d.MaxInflight {
		next := semaphore(make(chan struct{}, d.MaxInflight))
		h.sem.Store(&next)
	}
	if c := h.cache.Load(); c != nil {
		c.minTTL = d.CacheMinTTL
		c.maxTTL = d.CacheMaxTTL
	}
	if h.collector != nil {
		h.collector.SetQueryLogEnabled(d.EnableQueryLog)
	}
}

// 查询状态只有三种。缓存命中不是状态，是 qdetail.CacheHit 这个标记 ——
// 否则「允许项」和「已处理」会被「缓存」这个伪状态挤掉，
// 用户看不出某条到底是放行规则放过的、还是普通解析的。
const (
	actionBlocked = "blocked" // 已拦截
	actionAllowed = "allowed" // 允许项（命中放行规则）
	actionHandled = "handled" // 已处理（正常解析 / 改写 / 缓存命中）
)

// ServeDNS 实现 dns.Handler 接口（UDP/TCP 路径）。
func (h *Handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	h.Handle(w, req, nil)
}

// Handle 执行完整查询管线。
func (h *Handler) Handle(w dns.ResponseWriter, req *dns.Msg, hint *IdentifyHint) {
	start := time.Now()
	// 整条管线耗时：所有 return 分支都会经过这个 defer。
	defer func() { observe(stageTotal, time.Since(start)) }()
	// det 承载日志详情，贯穿整条管线逐步填充。
	det := qdetail{Proto: protoOf(hint, w), ECS: ecsFrom(req)}

	// 协议开关：管理员关掉的协议直接拒绝。
	// 放在最前面是因为它和用户无关，没有任何后续处理的意义；
	// 用 REFUSED 而不是丢包，客户端能立刻知道是「不让用」而不是「网络不通」。
	if !h.protoEnabled(det.Proto) {
		h.protosRejected.Add(1)
		if w != nil {
			writeMsg(w, req, refusedResponse(req))
		}
		// 协议被关掉的查询也要记一条：否则用户在日志里完全看不到
		// 「我的明文请求去哪了」，只会以为程序漏记。
		// 这里还没走到解析问题段，q 尚未定义，直接从 req 里取。
		if req != nil && len(req.Question) > 0 {
			h.record("", req.Question[0], "blocked", "REFUSED", time.Since(start), w, qdetail{
				Proto: det.Proto,
			})
		}
		return
	}

	// Panic 保护：任何一个查询 panic 都不能让 dnsd 进程退出。
	// 这是「DNS 可用性优先」的兜底（文档 4.2）。
	defer func() {
		if r := recover(); r != nil {
			h.panics.Add(1)
			h.log.Error("查询处理 panic", "panic", r, "qname", firstQuestion(req))
			if w != nil {
				writeMsg(w, req, servfailResponse(req))
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
	gateStart := time.Now()
	releaseSem, ok := acquireSemaphore(&h.sem)
	observe(stageGate, time.Since(gateStart))
	if !ok {
		// 并发已满：立刻回 SERVFAIL 而不是排队。
		// 排队会让延迟雪崩，客户端更可能超时后重试，形成正反馈。
		h.rejected.Add(1)
		writeMsg(w, req, servfailResponse(req))
		return
	}
	defer releaseSem()
	h.inflight.Add(1)
	defer h.inflight.Add(-1)

	if len(req.Question) == 0 {
		writeMsg(w, req, servfailResponse(req))
		return
	}
	q := req.Question[0]

	// Stage 1 用户识别。
	//
	// 打点要细分：之前把「识别 + 全部前置校验」合成一个 identify 打点，
	// 结果 identify 的 P50 是 34ms —— 但里面全是内存查表，不可能这么慢。
	// 说明时间花在了别处（排在同一个 defer 区间里的并发闸门/配额/限流），
	// 混在一起就把真正的原因掩盖了。现在拆成 identify / 闸门 / 校验三段。
	identifyStart := time.Now()
	snap := h.registry.Load()
	user := h.identify(snap, w, req, hint)
	observe(stageIdentify, time.Since(identifyStart))
	if user == nil {
		user = snap.Fallback()
	}
	isFallback := user.UserID == ""

	// Stage 2 前置校验：租户是否启用。
	if !isFallback && !user.Enabled {
		h.record(user.UserID, q, "blocked", "REFUSED", time.Since(start), w)
		writeMsg(w, req, refusedResponse(req))
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
			writeMsg(w, req, refusedResponse(req))
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
		writeMsg(w, req, refusedResponse(req))
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
			writeMsg(w, req, refusedResponse(req))
			return
		}
	}

	// 未识别请求的 refused 策略。
	//
	// 这里**故意不写查询日志**。
	//
	// 原因：识别不出租户的请求（全网扫描器的 hostname.bind / version.bind、
	// 直接拿 IP 打 53 的探测、没绑来源 IP 的设备）全部会落到 user_id="" 这个
	// 兜底桶里。它们写进日志后，管理员在「用户排行榜」上会看到一行没有名字
	// 的幽灵用户，拦截率接近 100%、缓存命中 0 —— 看起来像数据坏了，
	// 实际是一堆扫描器。排行榜是给人看用量排行的，不该被这类噪音占位。
	//
	// 保留一个纯计数器：既避免污染榜单，又还能看出「有没有人在乱打」。
	if isFallback && d.FallbackPolicy == model.FallbackRefused {
		h.fallbackRefused.Add(1)
		writeMsg(w, req, refusedResponse(req))
		return
	}

	// 限流（按租户）。
	if d.RateLimitQPS > 0 {
		if !h.allow(user.UserID, d.RateLimitQPS) {
			h.record(user.UserID, q, "blocked", "REFUSED", time.Since(start), w)
			writeMsg(w, req, refusedResponse(req))
			return
		}
	}

	// 特殊域名：本地回环、DoH 探针。
	if resp, handled := h.specialDomain(req); handled {
		det.Answer = h.detail(resp)
		h.record(user.UserID, q, "allowed", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
		writeMsg(w, req, resp)
		return
	}

	// Stage 3 缓存查询。
	cache := h.cache.Load()
	// cacheKey 在 Stage 5 的并发合并里还要用（同一域名合并成一次上游查询），
	// 所以在这里算一次存下来，避免热路径上重复做小写与去尾点。
	cacheKey, cacheable := CacheKeyFrom(user.UserID, req)

	// 这条查询的「网段身份」：决定它去网段层的哪一格查/写。
	//
	// 三种来源，优先级就是可信度：
	//  1. 客户端自带的 ECS —— 它自己声明了属于哪个网段，直接采信；
	//  2. 服务端要注入的 ECS —— 开 EnableECS 且客户端带了 EDNS（或开了
	//     DNSSEC 会顺带 SetEdns0）时，按来源地址算 /24 或 /56；
	//  3. 都没有 —— nil，只走主键层。
	//
	// 注意 ecsKey 与「要不要真的注入报文」是两件事：这里算出来的是缓存
	// 维度，注入与否还要看下面 applyECS 的分支。两者必须用同一份网段，
	// 否则会往错误的格子里写（拿 A 网段的答案存到 B 网段名下）。
	var (
		ecsSub   *dns.EDNS0_SUBNET
		ecsCache *ecsKey
	)
	if sub := subnetFromMsg(req); sub != nil {
		ecsSub = sub
		if k, ok := ecsKeyFromSUBNET(sub); ok {
			ecsCache = &k
		}
	} else if d.EnableECS && (req.IsEdns0() != nil || d.EnableDNSSEC) {
		if sub := subnetFromWriter(w); sub != nil {
			ecsSub = sub
			if k, ok := ecsKeyFromSUBNET(sub); ok {
				ecsCache = &k
			}
		}
	}
	// 发给上游的那份网段：只有管理员开了 ECS 才注入，且不覆盖客户端自带的。
	// 客户端自带 ECS 时即使 EnableECS 关着，缓存仍需按那个网段分层 ——
	// 上游照样会按它返回就近答案。
	if !d.EnableECS {
		if own := subnetFromMsg(req); own == nil {
			ecsSub = nil
		}
	}
	// 记进日志：之前 det.ECS 只在 ecsFrom(req) 里填了「客户端自带的」，
	// 服务端注入的那条从来没回填过，于是 query_log.ecs 永远是空字段，
	// 谁也没法从面板核实 ECS 到底有没有生效。
	//
	// 用 formatECSForLog 而不是 formatECS：后者返回注入上游用的网段地址
	// （主机位已抹），而日志要的是精确地址，否则同一个 /24 里的设备
	// 在排查时无法区分。
	var clientIP net.IP
	if w != nil {
		clientIP = remoteIP(w.RemoteAddr())
	}
	det.ECS = formatECSForLog(ecsSub, clientIP)

	cacheStart := time.Now()
	if cacheable && cache != nil {
		msg, hit, allowRule, cachedAction, missReason := cache.GetWithECS(cacheKey, req, ecsCache)
		observe(stageCache, time.Since(cacheStart))
		if !hit {
			// 记下未命中的成因（仅当这条请求本来会进缓存时才有意义）。
			switch missReason {
			case MissAbsent:
				h.cacheMissAbsent.Add(1)
			case MissExpired:
				h.cacheMissExpired.Add(1)
			}
		}
		if hit {
			// 必须把报文 ID 改回本次请求的 ID。
			//
			// 缓存里存的是首次应答的副本，它带着「当时那个请求」的随机 ID。
			// 原样返回会让客户端认为应答与请求不匹配而直接丢弃，
			// 表现就是查询超时（dig 会明确提示 "ID mismatch"）。
			// 这个 bug 只在第二次查同一域名时才出现，所以极易漏测。
			msg.Id = req.Id
			det.Answer = h.detail(msg)
			// 缓存命中不是一种状态：状态看这条应答当初是怎么产生的，
			// 「缓存」由 det.CacheHit 这个标记表达（列表显示成「已拦截（缓存）」）。
			det.CacheHit = true
			status := actionHandled
			switch {
			case cachedAction != "":
				// 拦截/改写的应答：还原当初的状态，否则拦截率会失真。
				status = cachedAction
			case allowRule:
				status = actionAllowed
			}
			h.record(user.UserID, q, status, dns.RcodeToString[msg.Rcode], time.Since(start), w, det)
			writeMsg(w, req, msg)
			return
		}
	}

	// 取失效代际快照：必须在打上游/构造拦截应答**之前**。
	//
	// 缓存键不含规则版本，规则改了键不变。若这条查询在规则变更**之前**出发、
	// 在 FlushUsers 之后才回来，它带的是旧规则下的答案 —— 不校验就会把旧答案
	// 写进缓存（TTL 最长 86400），用户看到「规则加了没用，过一阵自己好」。
	// stamp 与写入时的当前代际不一致，这次写入直接丢弃。
	var cacheStamp CacheStamp
	if cacheable && cache != nil {
		cacheStamp = cache.Stamp(user.UserID)
	}

	// Stage 4 规则匹配。
	// allowRule 表示「这次放行是规则明确允许的」，用于日志状态分类：
	// 命中放行规则 → 允许项；其余正常解析/改写 → 已处理。
	allowRule := false
	rs := user.Rules.Load()
	rulesStart := time.Now()
	if rs != nil && !rs.Empty() {
		decision := rs.Match(q.Name, q.Qtype)
		observe(stageRules, time.Since(rulesStart))
		switch decision.Action {
		case rules.ActionAllow:
			// 白名单：放行到上游，跳过后续拦截判断。
			allowRule = true
		case rules.ActionBlock:
			resp := buildBlockResponse(req, d.BlockingMode, d.CustomIP)
			// 拦截结果必须进缓存。
			//
			// 被拦的域名恰恰是重复率最高的那一类（遥测/广告端点每秒都在
			// 重试）：线上实测 aeventlog.beacon.qq.com 一天 3277 次、全部
			// 被拦，平均 26 秒一次。不缓存的话每一条都要重跑「识别用户 +
			// 几十万条后缀规则匹配 + 构造拦截响应」，而这些结果只取决于
			// 规则本身，与时间无关。
			//
			// 规则变更时缓存会被定向失效（FlushUsers），所以不会出现
			// 「规则删了但还被拦」的残留。
			if cacheable && cache != nil {
				cache.PutWithActionStamp(cacheKey, resp, req, "blocked", cacheStamp)
			}
			det.Answer = h.detail(resp)
			h.record(user.UserID, q, "blocked", dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
			writeMsg(w, req, resp)
			return
		case rules.ActionRewrite:
			resp := rewriteResponse(req, decision.Rewrite)
			// 改写结果同理：只由规则决定，可以缓存。
			if cacheable && cache != nil {
				cache.PutWithActionStamp(cacheKey, resp, req, actionHandled, cacheStamp)
			}
			det.Answer = h.detail(resp)
			h.record(user.UserID, q, actionHandled, dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
			writeMsg(w, req, resp)
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
		writeMsg(w, req, servfailResponse(req))
		return
	}

	timeout := time.Duration(d.QueryTimeout) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// EDNS / DNSSEC：这两个开关只在「要发出去的报文」上动手。
	//
	// 缓存维度（ecsCache / ecsSub）已经在 Stage 3 算好了，这里必须复用
	// 同一份网段 —— 两处各算一次的话，一旦条件判断有差异（例如 DNSSEC
	// 分支会先 SetEdns0），就会出现「按 A 网段注入、往 B 网段名下写缓存」。
	upReq := req
	if applyDNSSEC || applyECS {
		upReq = req.Copy()
		if applyDNSSEC {
			// 置 DO 位：让上游把 DNSSEC 记录一起回来，并把验证结果放进 AD 位。
			upReq.SetEdns0(4096, true)
		}
		if applyECS {
			// 只在客户端没自带 ECS 时注入，避免覆盖客户端自己的意图。
			if !hasECS(upReq) && ecsSub != nil && upReq.IsEdns0() != nil {
				upReq.IsEdns0().Option = append(upReq.IsEdns0().Option, ecsSub)
			}
		}
	}
	// Stage 5 上游解析。
	//
	// 并发合并：同一租户 + 同一域名 + 同一类型 + 同一 DO 位 + **同一网段**的
	// 请求只放一条去问上游。
	//
	// 网段必须进合并键：把 A 网段的查询合并到 B 网段那条上，拿回来的是
	// A 的就近答案 —— 那是错答案而不是性能优化。以前的做法是干脆不合并
	// 带 ECS 的请求，等于把击穿保护也一起关掉了。
	fkey, mergeable := flightKeyOf(cacheKey, cacheable, applyDNSSEC, ecsCache)
	if !mergeable {
		fkey = "" // 不参与合并，直接问上游
	}
	upstreamStart := time.Now()
	resp, upSrc, err, shared := h.flight.Do(ctx, fkey,
		func() (*dns.Msg, string, error) {
			return pool.ExchangeWithSource(ctx, upReq)
		})
	// 这里量的是「等到上游答完」，包含被并发合并时的等待时间 ——
	// 那正是用户感知的延迟，单独剔除反而失真。
	observe(stageUpstream, time.Since(upstreamStart))
	if shared && resp != nil {
		// 等待者拿到的是别人那次的共享副本：leader 还会继续改它
		// （回填 ID、置 Compress、写缓存），这里必须先复制。
		resp = resp.Copy()
		// 这次查询没有真正打上游，是被同域名的并发请求合并了。
		// 它既不是缓存命中也不是一次真实的上游查询，单独记一类。
		h.cacheMerged.Add(1)
	}
	det.Upstream = upSrc
	if err != nil {
		h.upstreamErrs.Add(1)
		h.log.Warn("上游解析失败", "qname", q.Name, "qtype", dns.TypeToString[q.Qtype], "err", err)
		h.record(user.UserID, q, "upstream", "SERVFAIL", time.Since(start), w, det)
		writeMsg(w, req, servfailResponse(req))
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
	if cacheable && cache != nil {
		cache.PutWithStampECS(cacheKey, resp, req, allowRule, cacheStamp, sharedSubnet(resp, ecsSub, ecsCache))
	}
	det.Answer = h.detail(resp)
	status := actionHandled
	if allowRule {
		status = actionAllowed
	}
	h.record(user.UserID, q, status, dns.RcodeToString[resp.Rcode], time.Since(start), w, det)
	writeMsg(w, req, resp)
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
	Proto string // udp|tcp|dot|doh
	// CacheHit 标记这次应答来自缓存。它是「状态」的修饰，不是状态本身。
	CacheHit   bool
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
		// 迁就用精确 IP：脱敏改成在对外出站那一层做（见 redact.go），
		// 而不是在落库前就把信息抹掉。
		//
		// 原因：日志一旦以 /24 存进来，就再也还原不回单个客户端 ——
		// 而排障时「是这一台设备还是整个网段都在报错」是完全不同的问题。
		// 脱敏是「给谁看」的问题，应该在出站时按角色决定，不该在采集时
		// 一刀切。
		client = ipString(remoteIP(w.RemoteAddr()))
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
		CacheHit:   det.CacheHit,
		Client:     client,
		LatencyMS:  dur.Milliseconds(),
		Proto:      det.Proto,
		ECS:        det.ECS,
		Upstream:   det.Upstream,
		Answer:     det.Answer,
		Tracker:    trackerLabel(domain),
		ClientName: det.ClientName,
	})
}

// trackerLabel 把追踪器整理成日志里显示的标签：「名称（类别）」。
//
// 单独包一层是为了让日志里永远只有一种格式：
// 命中就写「QQ International（客户交互）」，没命中就空着。
// 前端不需要再判断有没有类别、要不要拼括号。
func trackerLabel(domain string) string {
	name, category := matchTracker(domain)
	if name == "" {
		return ""
	}
	if category == "" {
		return name
	}
	return name + "（" + category + "）"
}

// ecsFrom 提取 EDNS Client Subnet 选项。
//
// 输出形如 "112.111.55.37/24"：前面是精确地址，后面保留客户端声明的掩码。
// 过去这里只写网段本身（"112.111.55.0/24"），把主机位抹掉了 —— 而 ECS 的
// 地址本来就是客户端自己上报的网络信息，不是我们采集到的终端地址，
// 保留精确值既不影响真实脱敏（那是对 client 字段做的），又能让
// 「同一个 /24 里哪台设备更近 CDN」这类问题查得出来。
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
//
// 只取前 8 条：日志详情是给人看一眼「解析到了什么」，不是完整报文导出。
//
// 走轻量格式而不是 rr.String()：后者做完整的 DNS 记录格式化（域名 + TTL +
// 类 + 类型 + 全部字段，还对 TTL 做宽度对齐），单条就要几百纳秒、十几次分配。
// 基准测试实测 answerFrom 单次 5070ns / 23 allocs，而整条缓存命中路径才
// 9244ns —— 也就是说「为了记一条日志」，一半的 CPU 花在了格式化上。
// 常见类型（A/AAAA/CNAME/MX/TXT/NS/PTR/SRV）改用直接取值拼接后，
// 分配数与耗时都成倍下降；其余少见的类型仍回退到 rr.String()，保证信息不丢。
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
		parts = append(parts, formatRR(rr))
	}
	return strings.Join(parts, "\n")
}

// formatRR 用最少的分配拼出一条记录的「类型 值」。
func formatRR(rr dns.RR) string {
	switch v := rr.(type) {
	case *dns.A:
		return "A " + v.A.String()
	case *dns.AAAA:
		return "AAAA " + v.AAAA.String()
	case *dns.CNAME:
		return "CNAME " + v.Target
	case *dns.NS:
		return "NS " + v.Ns
	case *dns.PTR:
		return "PTR " + v.Ptr
	case *dns.MX:
		return "MX " + itoa(int(v.Preference)) + " " + v.Mx
	case *dns.TXT:
		return "TXT " + strings.Join(v.Txt, " ")
	case *dns.SRV:
		return "SRV " + itoa(int(v.Priority)) + " " + itoa(int(v.Weight)) + " " +
			itoa(int(v.Port)) + " " + v.Target
	case *dns.HTTPS:
		return "HTTPS " + v.Hdr.Name + " " + itoa(int(v.Priority))
	case nil:
		return ""
	}
	// 少见类型回退到完整格式化，保证日志里不会出现空值。
	return rr.String()
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
	maxInflight := 0
	if sem := h.sem.Load(); sem != nil {
		maxInflight = cap(*sem)
	}
	return map[string]any{
		"inflight":        h.inflight.Load(),
		"max_inflight":    maxInflight,
		"rejected":        h.rejected.Load(),
		"panics":          h.panics.Load(),
		"upstream_errors": h.upstreamErrs.Load(),
		// 缓存未命中分类：判断「命中率为什么低」的三个方向
		"cache_miss_absent":  h.cacheMissAbsent.Load(),
		"cache_miss_expired": h.cacheMissExpired.Load(),
		"cache_merged":       h.cacheMerged.Load(),
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

// ipString 输出精确 IP 字符串。
//
// 和 maskIP 的区别：maskIP 把地址抹成 /24 网段，这里保留完整地址。
// 落库用精确值，对外出站时再按查看者角色决定是否脱敏（见 api/redact.go）。
// 这样「采集」与「展示」分开：采集时保留全量信息，展示时按权限裁剪。
func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	if v6 := ip.To16(); v6 != nil {
		return v6.String()
	}
	return ""
}

// maskIP 把客户端 IP 脱敏到 /24（IPv4）或 /48（IPv6）。
//
// 日志脱敏是文档 10.3 的硬要求：查询日志用于排障与统计，
// 不需要精确到单个设备，保留网段即可定位来源。
//
// 手工拼字节而不是 fmt.Sprintf：这条路径每条查询都要走一次，而
// Sprintf 的反射开销实测 532ns / 1 次分配，占了缓存命中路径的 6%。
// 掩码结果只有几十种（同一个网段的客户端反复出现），所以再加一层
// sync.Map 缓存，把重复计算的成本也省掉。
func maskIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		// 先查缓存：线上绝大多数查询来自同一个 /24。
		key := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8
		if s, ok := maskCache.Load(key); ok {
			return s.(string)
		}
		// 最长 "255.255.255.0/24" = 16 字节。
		var b [16]byte
		n := 0
		n += putUint8(b[n:], v4[0])
		b[n] = '.'
		n++
		n += putUint8(b[n:], v4[1])
		b[n] = '.'
		n++
		n += putUint8(b[n:], v4[2])
		b[n] = '.'
		n++
		b[n] = '0'
		n++
		b[n] = '/'
		n++
		b[n] = '2'
		n++
		b[n] = '4'
		n++
		s := string(b[:n])
		maskCache.Store(key, s)
		return s
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	return fmt.Sprintf("%02x%02x:%02x%02x:%02x%02x::/48", v6[0], v6[1], v6[2], v6[3], v6[4], v6[5])
}

// maskCache 缓存 v4 的 /24 掩码结果，键是把前三段压进的 uint32。
var maskCache sync.Map

// putUint8 把 0-255 写成十进制，返回写入的字节数。
func putUint8(dst []byte, v byte) int {
	switch {
	case v >= 100:
		dst[0] = '0' + v/100
		dst[1] = '0' + (v/10)%10
		dst[2] = '0' + v%10
		return 3
	case v >= 10:
		dst[0] = '0' + v/10
		dst[1] = '0' + v%10
		return 2
	default:
		dst[0] = '0' + v
		return 1
	}
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

// clientWantsDNSSEC 判断客户端自己是否请求了 DNSSEC。
func clientWantsDNSSEC(req *dns.Msg) bool {
	if req.AuthenticatedData || req.CheckingDisabled {
		return true
	}
	opt := req.IsEdns0()
	return opt != nil && opt.Do()
}

// protoEnabled 判断某个接入协议是否被管理员开启。
//
// 快照里没写（老快照）时一律按开启处理，避免升级瞬间把服务关掉。
func (h *Handler) protoEnabled(proto string) bool {
	d := h.registry.Load().Defaults()
	// 三个都是 false 说明这份快照里根本没有这些字段（老版本写出来的），
	// 此时按「全部开启」处理。否则升级瞬间 dnsd 会先把所有查询拒掉，
	// 直到 apid 写出新快照为止 —— 那几秒整个服务是不可用的。
	// apid 侧校验保证合法配置不可能三个全关，所以这个判据不会误伤。
	if !d.EnableDoH && !d.EnableDoT && !d.EnablePlainDNS {
		return true
	}
	switch proto {
	case "doh":
		return d.EnableDoH
	case "doh3":
		// HTTP/3 只是 DoH 的另一种传输，跟随 DoH 开关：
		// 单独给一个开关会让「DoH 关了但 H3 还开着」这种组合出现，
		// 而客户端的 h3 地址与 DoH 完全相同，用户会以为是随机故障。
		return d.EnableDoH
	case "dot":
		return d.EnableDoT
	case "doq":
		// DoQ 与 DoT 共用 853 端口号、同一张证书、同一条 SNI 识别路径，
		// 语义上就是「853 上的加密 DNS」，所以跟随 DoT 开关。
		return d.EnableDoT
	case "udp", "tcp", "":
		return d.EnablePlainDNS
	default:
		return true
	}
}

// writeMsg 是写应答的唯一出口。
//
// 只有 UDP 需要截断：TCP/DoT/DoH 走的是流，长度不受 512 字节限制。
// 不截断的后果是大应答（DNSSEC 带 RRSIG、长 TXT、多个 A 记录）整包塞进
// UDP 发出去，客户端缓冲装不下就直接丢 —— 表现为「偶尔查不到」，
// 而服务端这边看起来一切正常，极难排查。
//
// RFC 1035/6891：无 EDNS 时上限 512，有 EDNS 时按客户端声明的值。
func writeMsg(w dns.ResponseWriter, req, resp *dns.Msg) {
	if w == nil || resp == nil {
		return
	}
	if ra := w.RemoteAddr(); ra != nil && ra.Network() == "udp" {
		resp.Truncate(udpPayloadSize(req))
	}
	_ = w.WriteMsg(resp)
}

// udpPayloadSize 返回本次请求允许的 UDP 应答上限。
//
// 客户端声明的值小于 512 一律按 512 处理（RFC 6891 的要求），
// 不带 EDNS 时就是 512。
func udpPayloadSize(req *dns.Msg) int {
	if req != nil {
		if opt := req.IsEdns0(); opt != nil {
			if s := int(opt.UDPSize()); s > dns.MinMsgSize {
				return s
			}
		}
	}
	return dns.MinMsgSize
}

// detail 按需组装应答文本。
//
// 关了查询日志就返回空串：answerFrom 一次要 ~4.9µs、30 次分配，
// 而这些只服务于日志详情面板。之前无条件调用，等于给每个查询
// 白加了一次字符串拼接的成本。
func (h *Handler) detail(resp *dns.Msg) string {
	if h.collector == nil || !h.collector.QueryLogEnabled() {
		return ""
	}
	return answerFrom(resp)
}
