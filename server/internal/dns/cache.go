package dns

import (
	"container/list"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// CacheKey 是缓存键。
//
// 必须包含 UserID —— 这是文档第十章「必须避免的反模式」第一条：
// 缓存 key 不含 user_id 会导致 A 用户命中 B 用户的改写结果，跨租户数据泄漏。
type CacheKey struct {
	UserID string
	QName  string // 已小写、无尾点
	QType  uint16
	// DO 位（DNSSEC OK）会改变响应内容（是否带 RRSIG），必须参与区分。
	DO bool
}

// ruleCacheTTL 是「规则决定型应答」（拦截 / 改写）的缓存秒数。
//
// 这类应答的 TTL 不该跟着上游走：它完全由规则决定，与 DNS 记录无关，
// 上游根本不参与。原来沿用 ttlFor() 算出来的 60 秒（cache_min_ttl），
// 结果是命中率被压在 30% 上下 —— 现代客户端会交替查 A / AAAA / HTTPS，
// 同一个 key 的相邻查询间隔被拉到 120 秒以上，60 秒的条目早就过期了。
//
// 线上实测拦截类查询占 36% 流量，把它们的 TTL 提到 300 秒后，
// 规则不变期间这批查询基本全部命中。
//
// 安全性：规则变更时 loader 会走 FlushUsers 定向失效该租户的缓存，
// 所以不会出现「规则删了但缓存还拦着」。管理员手动清缓存同样生效。
const ruleCacheTTL = 300

// CacheEntry 是一条缓存记录。
type CacheEntry struct {
	msg       *dns.Msg
	storedAt  time.Time
	expiresAt time.Time
	// allowRule 记录这条应答是「命中放行规则后去上游拿到的」。
	//
	// 为什么要存：查询日志的状态分「允许项 / 已处理」，而缓存命中时
	// 已经走不到规则匹配了，不记下来就永远只能显示「已处理（缓存）」，
	// 明明是因为放行规则才通的域名，看起来跟普通解析一样。
	allowRule bool
	// action 记录这条应答当初是哪个分支产生的，供缓存命中时还原状态。
	//
	// 取值："" 正常解析（含放行规则外的默认路径）、"blocked" 被规则拦截、
	// "handled" 被规则改写。存它的原因和 allowRule 一样 —— 拦截结果也进
	// 缓存之后，命中时如果不还原状态，拦截的查询会被记成「已处理」，
	// 拦截率会凭空掉下去，统计直接失真。
	action string
}

// cacheItem 是 LRU 链表节点载荷。
//
// 必须把 key 一起存进来：淘汰时是从链表尾部取节点，只有 entry 无法反查
// 它在 items map 里的键，也就删不掉 map 项，会留下永远无法淘汰的僵尸条目。
type cacheItem struct {
	key   CacheKey
	entry *CacheEntry
	// ecs 非零表示这条条目在网段层，key 需要再带上它才能定位与删除。
	//
	// 淘汰时是从 LRU 尾部取节点，只有 entry 无法反查它在哪一层、哪个键
	// 下 —— 不记下来的话网段条目会变成删不掉的僵尸，占着容量却永不失效。
	ecs   ecsKey
	inSub bool
}

// Cache 是分片 LRU 缓存。
//
// 分片的意义是降低锁竞争：16 个分片各自持有独立的互斥锁，
// 不同分片的读写互不阻塞。
type Cache struct {
	shards [16]*cacheShard
	// size 是总容量上限，构造时按分片均分。
	size int
	// minTTL / maxTTL 是 TTL 钳制区间。
	minTTL int
	maxTTL int
	// genMu 保护 gen 与 userGen。
	//
	// 用 RWMutex 而不是 atomic：写缓存时要在**同一个临界区**里既读代际又写
	// 条目，否则「读代际 → 失效发生 → 写条目」这三步之间会留下窗口，
	// 陈旧条目照样写得进去（见 CacheStamp 的说明）。
	genMu sync.RWMutex
	// gen 是全量失效的代际，每次 Flush() 递增。
	gen uint64
	// userGen 是各租户定向失效的代际，FlushUsers 时递增。
	userGen map[string]uint64
}

// MissReason 说明一次缓存未命中的成因。
//
// 只有「命中率掉了」的时候才会有人来看这个，而仅仅知道「命中率 55%」无法
// 判断该动什么：
//   - 绝大多数是 MissAbsent（冷启动）→ 说明域名长尾，缓存容量或 TTL 是瓶颈；
//   - 绝大多数是 MissExpired（过期）→ 说明上游 TTL 短，或者客户端查询间隔
//     太长，这时候该考虑的是 minTTL 而不是扩容；
//   - 有相当比例是 MissInflight → 那是并发击穿，靠 singleflight 已经挡住上游
//     压力，命中率的数字本身不该再拿它当目标。
//
// 三者对应的动作完全不同，所以必须分开计数。
type MissReason string

const (
	// MissNone 表示命中（或这条查询本就不参与缓存）。
	MissNone MissReason = ""
	// MissAbsent 表示缓存里从来没有过这个键（冷启动/长尾域名）。
	MissAbsent MissReason = "absent"
	// MissExpired 表示有过但已过 TTL。
	MissExpired MissReason = "expired"
)

// CacheStamp 是一次查询「开始那一刻」看到的失效代际。
//
// 解决的是一个只在生产并发下才出现的正确性问题：
//
//	查询 Q 带着旧规则出发 → 管理员改规则 → 快照重载 → FlushUsers 清掉该租户缓存
//	→ Q 才从上游回来，把**旧规则下的应答**写进缓存（TTL 最长 86400）。
//
// 用户看到的是「规则加了，这个域名还是照常解析」，而且过一阵子自己好
// （缓存过期），最难自查。
//
// 做法：查询出发前取一个 stamp，写缓存时比对当前代际，不一致就丢弃这次写入。
// 只影响「跨越了一次规则变更的在途查询」，正常查询零成本。
type CacheStamp struct {
	// Gen 是取 stamp 时的全局代际（Flush 递增）。
	Gen uint64
	// User 是该租户的代际（FlushUsers 递增）。
	User uint64
}

type cacheShard struct {
	mu    sync.Mutex
	items map[CacheKey]*list.Element
	// subItems 是「网段层」：带 ECS 且答案确实依赖网段的正常应答。
	//
	// 单独一层而不是把网段塞进 CacheKey：塞进去的话，拦截/改写应答（本地
	// 规则构造、与网段无关）也会被迫按网段分裂，等于开 ECS 就把重复率最高
	// 的那类流量也逐出缓存。分成两层之后两者互不影响。
	//
	// 与 items 共用同一把锁与同一条 LRU：网段条目也占容量，淘汰逻辑不需要
	// 写两遍。代价是容量按「主键条目 + 网段条目」合计计算，这正是想要的
	// 效果（cache_size 的语义是「一共存多少条应答」）。
	subItems map[CacheKey]map[ecsKey]*list.Element
	lru      *list.List
	cap      int
}

// NewCache 创建一个容量为 size 条的分片 LRU。
func NewCache(size, minTTL, maxTTL int) *Cache {
	if size <= 0 {
		size = 10000
	}
	if minTTL < 0 {
		minTTL = 0
	}
	if maxTTL <= 0 {
		maxTTL = 86400
	}
	if maxTTL < minTTL {
		maxTTL = minTTL
	}
	c := &Cache{size: size, minTTL: minTTL, maxTTL: maxTTL}
	c.userGen = make(map[string]uint64, 64)
	per := size / len(c.shards)
	if per < 1 {
		per = 1
	}
	for i := range c.shards {
		c.shards[i] = &cacheShard{
			items:    make(map[CacheKey]*list.Element, per),
			subItems: make(map[CacheKey]map[ecsKey]*list.Element),
			lru:      list.New(),
			cap:      per,
		}
	}
	return c
}

// shardFor 按 key 的哈希选择分片。
func (c *Cache) shardFor(k CacheKey) *cacheShard {
	h := fnv.New32a()
	h.Write([]byte(k.UserID))
	h.Write([]byte{0})
	h.Write([]byte(k.QName))
	h.Write([]byte{byte(k.QType >> 8), byte(k.QType)})
	if k.DO {
		h.Write([]byte{1})
	}
	return c.shards[h.Sum32()%uint32(len(c.shards))]
}

// Get 查缓存。
//
// 命中时返回一份消息副本，并把各 RR 的 TTL 按已过去的秒数递减 ——
// 直接返回原对象会让调用方（或下游中间件）修改到共享状态。
//
// 注意：返回的副本带着**首次请求**的报文 ID。调用方必须把 Id 改成本次
// 请求的 ID 再写回，否则客户端会丢弃应答（表现为超时）。这里不做替换，
// 是因为缓存层拿不到本次请求；把契约写在文档注释里并配了回归测试。
// Get 返回缓存应答与是否命中。
func (c *Cache) Get(k CacheKey, req *dns.Msg) (*dns.Msg, bool) {
	msg, hit, _, _ := c.GetWithAllow(k, req)
	return msg, hit
}

// GetWithAllow 额外返回「这条应答是否来自命中放行规则」和它当初的状态。
//
// 放行规则命中的应答也会缓存，命中缓存时已经走不到规则匹配了；
// 不把标记带出来，查询日志里就只能显示「已处理（缓存）」，
// 明明是靠放行规则才通的域名，看起来和普通解析一样。
//
// action 同理：拦截/改写结果进缓存之后，命中时必须还原成 blocked / handled，
// 否则拦截率会凭空掉一截。
func (c *Cache) GetWithAllow(k CacheKey, req *dns.Msg) (*dns.Msg, bool, bool, string) {
	msg, hit, allow, action, _ := c.GetWithReason(k, req)
	return msg, hit, allow, action
}

// GetWithReason 与 GetWithAllow 相同，额外返回未命中的成因。
//
// 成因只用于指标分类，不影响返回内容 —— 拆成独立方法而不是改 GetWithAllow
// 的签名，是为了不动那些只关心命中的调用点。
func (c *Cache) GetWithReason(k CacheKey, req *dns.Msg) (*dns.Msg, bool, bool, string, MissReason) {
	return c.GetWithECS(k, req, nil)
}

// GetWithECS 是带网段的查找。
//
// sub 为 nil 表示这次查询不带（也不会注入）ECS，只查主键层。
//
// 查找顺序（两层，最多 2 次 map 查找，不含遍历，热路径 O(1)）：
//
//  1. 带 sub 时先查网段层精确匹配 —— 这是大多数带 ECS 的查询的命中去处；
//  2. 再查主键层 —— 那里存的是「与网段无关」的答案（拦截/改写，
//     以及上游明确声明 scope=0 的正常应答）。
//
// 反过来（不带 sub 的查询）只查主键层：网段层的条目是按别人的网段算出来的
// 答案，给一个没声明网段的客户端用等于凭空假设它属于那个网段。
func (c *Cache) GetWithECS(k CacheKey, req *dns.Msg, sub *ecsKey) (*dns.Msg, bool, bool, string, MissReason) {
	if c == nil {
		return nil, false, false, "", MissAbsent
	}
	now := time.Now()
	s := c.shardFor(k)

	s.mu.Lock()
	var (
		el       *list.Element
		miss     = MissAbsent
		probeSub bool
	)
	if sub != nil {
		if m, ok := s.subItems[k]; ok {
			if e, ok := m[*sub]; ok {
				el = e
				probeSub = true
			}
		}
	}
	if el == nil {
		if e, ok := s.items[k]; ok {
			el = e
		} else if sub != nil {
			// 网段层有这个主键的条目、但网段对不上：那是「过期」而不是
			// 「冷启动」—— 这个域名我们缓存过，只是别的网段先问的。
			// 分类错了会让人误判成「容量不足」，实际是网段维度不够。
			if m, ok := s.subItems[k]; ok && len(m) > 0 {
				miss = MissExpired
			}
		}
	}
	if el == nil {
		s.mu.Unlock()
		return nil, false, false, "", miss
	}
	item := el.Value.(*cacheItem)
	entry := item.entry
	if now.After(entry.expiresAt) {
		// 过期即删，避免占着 LRU 位置。
		s.lru.Remove(el)
		if probeSub {
			c.deleteSubLocked(s, k, item)
		} else {
			delete(s.items, k)
		}
		s.mu.Unlock()
		return nil, false, false, "", MissExpired
	}
	s.lru.MoveToFront(el)
	msg := entry.msg.Copy()
	elapsed := uint32(now.Sub(entry.storedAt).Seconds())
	s.mu.Unlock()

	// OPT 必须按「本次请求」重新校准，不能沿用缓存里那个客户端的。
	//
	// RFC 6891 §6.1.1：请求里没有 OPT，应答里就不能有。缓存存的是首次
	// 应答的完整副本，它带着首个客户端的 OPT（含那个客户端声明的
	// UDP 缓冲大小）。原样返回会出现两种错：
	//   - 不带 EDNS 的客户端收到一个 OPT —— 严格实现会判协议错误；
	//   - 512 缓冲的客户端收到一个标着 4096 的应答 —— 路径 MTU 一卡就丢包。
	msg.Extra = alignOPT(msg.Extra, req)

	// 递减 TTL，且不低于 1（客户端拿到 0 会立刻重新查询，失去缓存意义）。
	for _, rr := range msg.Answer {
		decTTL(rr, elapsed)
	}
	for _, rr := range msg.Ns {
		decTTL(rr, elapsed)
	}
	for _, rr := range msg.Extra {
		decTTL(rr, elapsed)
	}
	return msg, true, entry.allowRule, entry.action, MissNone
}

// alignOPT 把应答里的 OPT 调整成与本次请求一致。
//
// 请求没带 EDNS → 整段丢掉；带了 → 把 UDP 大小改成客户端声明的值
// （缓冲大小是链路属性，不是答案本身的一部分，跟着缓存走没有意义）。
func alignOPT(extra []dns.RR, req *dns.Msg) []dns.RR {
	var wantUDP uint16
	hasReqOPT := false
	if req != nil {
		if o := req.IsEdns0(); o != nil {
			hasReqOPT = true
			wantUDP = o.UDPSize()
		}
	}
	out := extra[:0]
	for _, rr := range extra {
		opt, ok := rr.(*dns.OPT)
		if !ok {
			out = append(out, rr)
			continue
		}
		if !hasReqOPT {
			continue // 客户端没要 EDNS，不能凭空给它一段 OPT
		}
		opt.SetUDPSize(wantUDP)
		out = append(out, opt)
	}
	return out
}

func decTTL(rr dns.RR, elapsed uint32) {
	h := rr.Header()
	if h.Ttl > elapsed {
		h.Ttl -= elapsed
	} else {
		h.Ttl = 1
	}
}

// Stamp 取当前失效代际，供 PutWithStamp 校验。
//
// 必须在**查询出发前**取（Stage 3 之后、打上游之前），不能在写缓存时取 ——
// 那样永远等于当前代际，等于没校验。
func (c *Cache) Stamp(userID string) CacheStamp {
	if c == nil {
		return CacheStamp{}
	}
	c.genMu.RLock()
	defer c.genMu.RUnlock()
	return CacheStamp{Gen: c.gen, User: c.userGen[userID]}
}

// Put 写入缓存（不带失效代际校验，仅测试与不关心代际的调用方用）。
//
// 查询路径必须用 PutWithStamp：不带 stamp 的写入在规则变更期间会把
// 旧规则下的应答写回缓存，正是这个功能要解决的问题。
func (c *Cache) Put(k CacheKey, msg *dns.Msg, req *dns.Msg) bool {
	return c.PutWith(k, msg, req, false)
}

// PutWith 缓存一条应答，并记下它是否来自「命中放行规则」。
func (c *Cache) PutWith(k CacheKey, msg *dns.Msg, req *dns.Msg, allowRule bool) bool {
	return c.put(k, msg, req, allowRule, "", nil)
}

// PutWithStamp 与 PutWith 相同，但会校验失效代际：若查询出发后发生过
// Flush / FlushUsers，这次写入被丢弃。
func (c *Cache) PutWithStamp(k CacheKey, msg *dns.Msg, req *dns.Msg, allowRule bool, stamp CacheStamp) bool {
	return c.put(k, msg, req, allowRule, "", &stamp)
}

// PutWithActionStamp 缓存规则决定型应答（拦截/改写），并校验失效代际。
func (c *Cache) PutWithActionStamp(k CacheKey, msg *dns.Msg, req *dns.Msg, action string, stamp CacheStamp) bool {
	return c.put(k, msg, req, false, action, &stamp)
}

// PutWithAction 缓存一条由规则直接产生的应答（拦截 / 改写），并记下状态。
//
// action 会被存进缓存条目，命中时还原成查询日志里的状态 —— 否则被拦的
// 查询走缓存后会被记成「已处理」，拦截率凭空掉一截。
func (c *Cache) PutWithAction(k CacheKey, msg *dns.Msg, req *dns.Msg, action string) bool {
	return c.put(k, msg, req, false, action, nil)
}

// PutWithStampECS 缓存一条正常解析的应答，并按答案的适用范围决定写哪一层。
//
// subnet 为 nil：写主键层（与网段无关，所有网段共享）。
// subnet 非 nil：写网段层，只有同一个网段能命中。
//
// 调用方用 sharedSubnet 算出参数，别自己拼 —— 那里的注释解释了为什么
// 不能信上游声称的非零 scope。
func (c *Cache) PutWithStampECS(
	k CacheKey,
	msg *dns.Msg,
	req *dns.Msg,
	allowRule bool,
	stamp CacheStamp,
	subnet *ecsKey,
) bool {
	return c.putECS(k, msg, req, allowRule, "", &stamp, subnet)
}

// put 是写入缓存的唯一实现。
//
// stamp 为 nil 表示不做失效代际校验（测试与不关心代际的调用方）。
//
// 加锁顺序固定为「分片锁 → genMu 读锁」：写条目与读代际必须在同一个临界区内，
// 否则「读代际 → 失效发生 → 写条目」之间会留下窗口，陈旧条目照样写进去。
// 反向顺序（先 genMu 再分片锁）在这里不需要，Flush 只拿 genMu 写锁、随后单独
// 拿各分片锁，不存在交叉持有。
func (c *Cache) put(k CacheKey, msg *dns.Msg, req *dns.Msg, allowRule bool, action string, stamp *CacheStamp) bool {
	return c.putECS(k, msg, req, allowRule, action, stamp, nil)
}

// putECS 是带上网段的写入。
//
// shared=false（或 subnet 为 nil）：写主键层，代表「这条答案与网段无关」。
// shared=true：写网段层，键里带 sub，代表「这条答案只对 sub 这个网段成立」。
//
// 判断放在调用方（handler）是因为只有它知道答案是怎么来的：拦截/改写应答
// 从来没有问过上游，天然与网段无关；正常解析要看上游回显的 scope。
func (c *Cache) putECS(
	k CacheKey,
	msg *dns.Msg,
	req *dns.Msg,
	allowRule bool,
	action string,
	stamp *CacheStamp,
	subnet *ecsKey,
) bool {
	if c == nil || msg == nil {
		return false
	}
	if msg.Rcode != dns.RcodeSuccess && msg.Rcode != dns.RcodeNameError {
		// 只缓存正常应答与 NXDOMAIN；SERVFAIL/REFUSED 缓存下来会放大故障。
		//
		// 这条规则顺带决定了「refused 拦截模式」的应答不会被缓存 ——
		// 那种模式下拦截结果每条都要重新判定，属于可接受的保守取舍。
		return false
	}
	// 注意：这里不再因为「请求带 ECS」就拒绝写入。
	//
	// 旧实现一律 return false，于是开了 ECS 之后所有带 EDNS 的客户端
	// 100% 绕过缓存（线上命中率从 50% 掉到 1%）。网段维度改由 subnet
	// 参数表达：与网段无关的答案照常进主键层，依赖网段的进网段层。
	switch k.QType {
	case dns.TypeDS, dns.TypeDNSKEY, dns.TypeRRSIG, dns.TypeNSEC, dns.TypeNSEC3:
		return false
	}

	// 规则决定型应答（拦截/改写）用固定 TTL：它只取决于规则，
	// 与上游记录的 TTL 无关。规则变更会定向失效缓存，所以长 TTL 是安全的。
	ttl := c.ttlFor(msg)
	if action != "" {
		ttl = ruleCacheTTL
		// 规则决定型应答一定在主键层：它是本地构造的 0.0.0.0 / 改写值，
		// 与客户端网段没有任何关系，按网段分裂只会白丢命中率。
		subnet = nil
	}
	now := time.Now()
	entry := &CacheEntry{
		msg:       msg.Copy(),
		storedAt:  now,
		expiresAt: now.Add(time.Duration(ttl) * time.Second),
		allowRule: allowRule,
		action:    action,
	}

	s := c.shardFor(k)
	s.mu.Lock()
	defer s.mu.Unlock()

	// 失效代际校验：查询出发后规则变过，这条应答属于旧规则，不能进缓存。
	if stamp != nil {
		c.genMu.RLock()
		fresh := c.gen == stamp.Gen && c.userGen[k.UserID] == stamp.User
		c.genMu.RUnlock()
		if !fresh {
			return false
		}
	}

	if subnet == nil {
		if el, ok := s.items[k]; ok {
			el.Value.(*cacheItem).entry = entry
			s.lru.MoveToFront(el)
			return true
		}
		el := s.lru.PushFront(&cacheItem{key: k, entry: entry})
		s.items[k] = el
		return c.evictLocked(s)
	}

	m := s.subItems[k]
	if m == nil {
		m = make(map[ecsKey]*list.Element, 4)
		s.subItems[k] = m
	}
	if el, ok := m[*subnet]; ok {
		el.Value.(*cacheItem).entry = entry
		s.lru.MoveToFront(el)
		return true
	}
	el := s.lru.PushFront(&cacheItem{key: k, entry: entry, ecs: *subnet, inSub: true})
	m[*subnet] = el
	return c.evictLocked(s)
}

// evictLocked 按 LRU 淘汰到分片容量以内。调用方必须已持有 s.mu。
//
// 主键层与网段层共用一条 LRU，所以淘汰时要看条目自己记的层信息 ——
// 只按主键删的话，网段条目会留在 subItems 里变成永不失效的僵尸。
func (c *Cache) evictLocked(s *cacheShard) bool {
	for s.lru.Len() > s.cap {
		oldest := s.lru.Back()
		if oldest == nil {
			break
		}
		s.lru.Remove(oldest)
		item := oldest.Value.(*cacheItem)
		if item.inSub {
			c.deleteSubLocked(s, item.key, item)
		} else {
			delete(s.items, item.key)
		}
	}
	return true
}

// deleteSubLocked 从网段层删除一条条目。调用方必须已持有 s.mu。
func (c *Cache) deleteSubLocked(s *cacheShard, k CacheKey, item *cacheItem) {
	m, ok := s.subItems[k]
	if !ok {
		return
	}
	delete(m, item.ecs)
	if len(m) == 0 {
		// 空 map 留着会一直累积（一个域名查过几个网段就留一个空壳），
		// 而它占的内存和一次 map 分配一样多。
		delete(s.subItems, k)
	}
}

// ttlFor 计算一条应答的缓存时长。
//
// 取所有 RR 中最小的 TTL，再钳制到 [minTTL, maxTTL]；
// NXDOMAIN / NODATA（无 Answer）按固定 60s 负缓存（文档 4.3）。
func (c *Cache) ttlFor(msg *dns.Msg) int {
	if len(msg.Answer) == 0 {
		return 60
	}
	min := int(^uint32(0) >> 1)
	for _, rr := range msg.Answer {
		if t := int(rr.Header().Ttl); t < min {
			min = t
		}
	}
	if min == int(^uint32(0)>>1) {
		return c.minTTL
	}
	if min < c.minTTL {
		return c.minTTL
	}
	if min > c.maxTTL {
		return c.maxTTL
	}
	return min
}

// Len 返回当前缓存条目数（系统状态展示用）。
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	n := 0
	for _, s := range c.shards {
		s.mu.Lock()
		n += len(s.items)
		for _, m := range s.subItems {
			n += len(m)
		}
		s.mu.Unlock()
	}
	return n
}

// Flush 清空缓存（管理员操作）。
func (c *Cache) Flush() {
	if c == nil {
		return
	}
	// 先推进代际再清条目：在途查询的 stamp 会因此失效，写不回来。
	// 两步顺序不能反 —— 先清后推进的话，清理与推进之间回来的应答
	// 读到的还是旧代际，会被写进刚清空的缓存。
	c.genMu.Lock()
	c.gen++
	c.genMu.Unlock()

	for _, s := range c.shards {
		s.mu.Lock()
		s.items = make(map[CacheKey]*list.Element, s.cap)
		s.subItems = make(map[CacheKey]map[ecsKey]*list.Element)
		s.lru.Init()
		s.mu.Unlock()
	}
}

// FlushUsers 只清掉指定租户的缓存条目。
//
// 用于规则变更后的定向失效。缓存键是 {UserID, QName, QType, DO}，**不含规则
// 版本** —— 规则改了键不变，旧应答会一直被命中。不主动清的话，新加的拦截
// 规则对该域名在 TTL 内完全不生效（cache_max_ttl 默认 86400，最长一天），
// 用户看到的是「加了规则没用，过一阵子才生效」。
//
// 为什么不直接全量 Flush：一次规则改动就把整机缓存清空，会让所有租户的
// 命中率瞬间归零并向上游打一波真实查询 —— 这正是缓存要避免的事。
// 定向失效只影响真正改了规则的那几个租户。
//
// 代价是 O(分片条目数) 的遍历。这只在快照重载时发生（规则变更触发），
// 不在查询路径上，因此可以接受。
func (c *Cache) FlushUsers(userIDs []string) {
	if c == nil || len(userIDs) == 0 {
		return
	}
	drop := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		drop[id] = struct{}{}
	}

	// 先推进这些租户的代际，再清条目。
	//
	// 这一步是整个定向失效能成立的前提：缓存键不含规则版本，规则改了键不变，
	// 而**在途**的查询（带着旧规则出发、此刻还没回来）会在失效之后才写缓存 ——
	// 没有代际校验的话，它写进去的就是旧规则下的应答，TTL 最长 86400。
	// 用户看到的是「加了规则没用」，过一阵自己好，最难自查。
	c.genMu.Lock()
	for id := range drop {
		c.userGen[id]++
	}
	c.genMu.Unlock()

	for _, s := range c.shards {
		s.mu.Lock()
		for k, el := range s.items {
			if _, ok := drop[k.UserID]; ok {
				s.lru.Remove(el)
				delete(s.items, k)
			}
		}
		for k, m := range s.subItems {
			if _, ok := drop[k.UserID]; !ok {
				continue
			}
			for _, el := range m {
				s.lru.Remove(el)
			}
			delete(s.subItems, k)
		}
		s.mu.Unlock()
	}
}

// hasECS 判断请求是否携带 EDNS Client Subnet 选项。
func hasECS(req *dns.Msg) bool {
	if req == nil {
		return false
	}
	opt := req.IsEdns0()
	if opt == nil {
		return false
	}
	for _, o := range opt.Option {
		if _, ok := o.(*dns.EDNS0_SUBNET); ok {
			return true
		}
	}
	return false
}

// hasECS 已由 subnetFromMsg 取代（ecs.go）—— 那里同时返回选项本身，
// 而缓存分层需要网段值而不只是「有没有」。保留此注释说明去向，避免
// 后来者再写一份「带 ECS 就不缓存」的判断。
// CacheKeyFrom 由查询构造缓存键。带客户端 ECS 的请求不参与共享缓存，
// 因为答案可能依网段变化，而缓存键不包含网段。
func CacheKeyFrom(userID string, req *dns.Msg) (CacheKey, bool) {
	if req == nil || len(req.Question) == 0 {
		return CacheKey{}, false
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return CacheKey{}, false
	}
	// 注意：带 ECS 的请求**可以**构造缓存键了。
	//
	// 主键本身仍不含网段（网段由 Cache 的网段层单独表达），所以这里放行不会
	// 造成跨网段串号：能不能共享由调用方根据答案的 scope 决定写哪一层。
	// 旧实现在这里直接 return false，等于「开 ECS 就放弃全部缓存」。

	do := false
	if opt := req.IsEdns0(); opt != nil {
		do = opt.Do()
	}
	return CacheKey{UserID: userID, QName: name, QType: q.Qtype, DO: do}, true
}
