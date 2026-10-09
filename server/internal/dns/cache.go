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

// CacheEntry 是一条缓存记录。
type CacheEntry struct {
	msg       *dns.Msg
	storedAt  time.Time
	expiresAt time.Time
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
func (c *Cache) Get(k CacheKey) (*dns.Msg, bool) {
	if c == nil {
		return nil, false
	}
	now := time.Now()
	s := c.shardFor(k)

	s.mu.Lock()
	el, ok := s.items[k]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	item := el.Value.(*cacheItem)
	entry := item.entry
	if now.After(entry.expiresAt) {
		// 过期即删，避免占着 LRU 位置。
		s.lru.Remove(el)
		delete(s.items, k)
		s.mu.Unlock()
		return nil, false
	}
	s.lru.MoveToFront(el)
	msg := entry.msg.Copy()
	elapsed := uint32(now.Sub(entry.storedAt).Seconds())
	s.mu.Unlock()

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
	return msg, true
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
func (c *Cache) Put(k CacheKey, msg *dns.Msg, req *dns.Msg) bool {
	if c == nil || msg == nil {
		return false
	}
	if msg.Rcode != dns.RcodeSuccess && msg.Rcode != dns.RcodeNameError {
		// 只缓存正常应答与 NXDOMAIN；SERVFAIL/REFUSED 缓存下来会放大故障。
		return false
	}
	if hasECS(req) {
		return false
	}
	switch k.QType {
	case dns.TypeDS, dns.TypeDNSKEY, dns.TypeRRSIG, dns.TypeNSEC, dns.TypeNSEC3:
		return false
	}

	ttl := c.ttlFor(msg)
	now := time.Now()
	entry := &CacheEntry{
		msg:       msg.Copy(),
		storedAt:  now,
		expiresAt: now.Add(time.Duration(ttl) * time.Second),
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

// CacheKeyFrom 由查询构造缓存键。
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
	do := false
	if opt := req.IsEdns0(); opt != nil {
		do = opt.Do()
	}
	return CacheKey{UserID: userID, QName: name, QType: q.Qtype, DO: do}, true
}
