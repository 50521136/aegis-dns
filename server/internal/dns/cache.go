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
}

type cacheShard struct {
	mu    sync.Mutex
	items map[CacheKey]*list.Element
	lru   *list.List
	cap   int
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
	per := size / len(c.shards)
	if per < 1 {
		per = 1
	}
	for i := range c.shards {
		c.shards[i] = &cacheShard{
			items: make(map[CacheKey]*list.Element, per),
			lru:   list.New(),
			cap:   per,
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
	if c == nil {
		return nil, false, false, ""
	}
	now := time.Now()
	s := c.shardFor(k)

	s.mu.Lock()
	el, ok := s.items[k]
	if !ok {
		s.mu.Unlock()
		return nil, false, false, ""
	}
	item := el.Value.(*cacheItem)
	entry := item.entry
	if now.After(entry.expiresAt) {
		// 过期即删，避免占着 LRU 位置。
		s.lru.Remove(el)
		delete(s.items, k)
		s.mu.Unlock()
		return nil, false, false, ""
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
	return msg, true, entry.allowRule, entry.action
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

// Put 写入缓存。
//
// 返回是否真的写入了：含 EDNS Client Subnet 的请求与 DNSSEC 元记录不缓存
// （文档 4.3 的「不缓存」清单），因为它们的答案与查询者位置/签名状态相关。
// Put 缓存一条应答（默认不是放行规则命中）。
func (c *Cache) Put(k CacheKey, msg *dns.Msg, req *dns.Msg) bool {
	return c.PutWith(k, msg, req, false)
}

// PutWith 缓存一条应答，并记下它是否来自「命中放行规则」。
func (c *Cache) PutWith(k CacheKey, msg *dns.Msg, req *dns.Msg, allowRule bool) bool {
	return c.put(k, msg, req, allowRule, "")
}

// PutWithAction 缓存一条由规则直接产生的应答（拦截 / 改写），并记下状态。
//
// action 会被存进缓存条目，命中时还原成查询日志里的状态 —— 否则被拦的
// 查询走缓存后会被记成「已处理」，拦截率凭空掉一截。
func (c *Cache) PutWithAction(k CacheKey, msg *dns.Msg, req *dns.Msg, action string) bool {
	return c.put(k, msg, req, false, action)
}

func (c *Cache) put(k CacheKey, msg *dns.Msg, req *dns.Msg, allowRule bool, action string) bool {
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
	if hasECS(req) {
		return false
	}
	switch k.QType {
	case dns.TypeDS, dns.TypeDNSKEY, dns.TypeRRSIG, dns.TypeNSEC, dns.TypeNSEC3:
		return false
	}

	// 规则决定型应答（拦截/改写）用固定 TTL：它只取决于规则，
	// 与上游记录的 TTL 无关。规则变更会定向失效缓存，所以长 TTL 是安全的。
	ttl := c.ttlFor(msg)
	if action != "" {
		ttl = ruleCacheTTL
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

	if el, ok := s.items[k]; ok {
		el.Value.(*cacheItem).entry = entry
		s.lru.MoveToFront(el)
		return true
	}
	el := s.lru.PushFront(&cacheItem{key: k, entry: entry})
	s.items[k] = el
	for s.lru.Len() > s.cap {
		oldest := s.lru.Back()
		if oldest == nil {
			break
		}
		s.lru.Remove(oldest)
		delete(s.items, oldest.Value.(*cacheItem).key)
	}
	return true
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
		s.mu.Unlock()
	}
	return n
}

// Flush 清空缓存（管理员操作）。
func (c *Cache) Flush() {
	if c == nil {
		return
	}
	for _, s := range c.shards {
		s.mu.Lock()
		s.items = make(map[CacheKey]*list.Element, s.cap)
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
	for _, s := range c.shards {
		s.mu.Lock()
		for k, el := range s.items {
			if _, ok := drop[k.UserID]; ok {
				s.lru.Remove(el)
				delete(s.items, k)
			}
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

func cacheRequestEligible(req *dns.Msg, injectedECS bool) bool {
	return req != nil && !hasECS(req) && !injectedECS
}

// CacheKeyFrom 由查询构造缓存键。带客户端 ECS 的请求不参与共享缓存，
// 因为答案可能依网段变化，而缓存键不包含网段。
func CacheKeyFrom(userID string, req *dns.Msg) (CacheKey, bool) {
	if req == nil || len(req.Question) == 0 {
		return CacheKey{}, false
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	name = strings.TrimSuffix(name, ".")
	if name == "" || hasECS(req) {
		// ECS 回答可能依客户端网段变化，不能放进不含网段的共享缓存键。
		return CacheKey{}, false
	}

	do := false
	if opt := req.IsEdns0(); opt != nil {
		do = opt.Do()
	}
	return CacheKey{UserID: userID, QName: name, QType: q.Qtype, DO: do}, true
}
