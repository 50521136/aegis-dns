// Package stats 在内存中聚合解析统计，并按批次落库。
//
// 为什么不在查询路径上直接写数据库（文档 C2 / P2）：SQLite 的写锁等待会让
// 单次查询从微秒级退化到秒级。这里的做法是查询只往内存计数器上加一笔，
// 由后台每 60s 把聚合结果一次性提交。
package stats

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// BucketSeconds 是聚合桶的粒度（文档 8.3 query_rollup 的 5 分钟对齐）。
const BucketSeconds int64 = 300

// shardCount 是分片数。查询路径上的热点是互斥锁，分片把竞争摊开；
// 16 个分片在我们的目标量级（数千 QPS）下已足够。
const shardCount = 16

// Collector 收集并聚合解析统计。
type Collector struct {
	shards  [shardCount]*shard
	ring    *ring
	enabled atomic.Bool

	// 进程级累计计数，供 /admin/system 与 summary 快速展示。
	total    atomic.Int64
	blocked  atomic.Int64
	cached   atomic.Int64
	allowed  atomic.Int64
	rewrite  atomic.Int64
	latencyN atomic.Int64
	latencyS atomic.Int64

	// 最近一分钟的滑动计数（用于计算实时 QPS）。
	minuteStart atomic.Int64
	minuteCount atomic.Int64
}

type shard struct {
	mu      sync.Mutex
	rollups map[string]*rollupAcc
	tops    map[string]*topAcc
}

type rollupAcc struct {
	userID string
	ts     int64
	total  int64
	blocked int64
	cached int64
	allowed int64
	latency int64
}

type topAcc struct {
	userID  string
	ts      int64
	domain  string
	hits    int64
	blocked int64
}

// Event 是一次查询的统计事件。
type Event struct {
	UserID    string
	Domain    string
	QType     string
	RCode     string
	Action    string // allowed | blocked | cached | rewritten | upstream
	Client    string // 已脱敏的客户端标识
	LatencyMS int64
}

// New 创建一个收集器。
//
// ringSize 是内存查询日志环形缓冲的容量；enabled 控制是否记录查询日志。
func New(ringSize int, enabled bool) *Collector {
	c := &Collector{ring: newRing(ringSize)}
	c.enabled.Store(enabled)
	for i := range c.shards {
		c.shards[i] = &shard{
			rollups: make(map[string]*rollupAcc, 64),
			tops:    make(map[string]*topAcc, 128),
		}
	}
	c.minuteStart.Store(time.Now().Unix())
	return c
}

// SetQueryLogEnabled 动态开关查询日志记录。
func (c *Collector) SetQueryLogEnabled(on bool) { c.enabled.Store(on) }

// Record 记录一次查询。
//
// 这个方法在查询热路径上被调用，因此只做「加计数 + 写 map」，
// 不做任何分配以外的工作（map key 用拼接字符串，Go 会复用底层缓冲）。
func (c *Collector) Record(ev Event) {
	// 进程级计数。
	c.total.Add(1)
	switch ev.Action {
	case "blocked":
		c.blocked.Add(1)
	case "cached":
		c.cached.Add(1)
	case "rewritten":
		c.rewrite.Add(1)
	default:
		c.allowed.Add(1)
	}
	c.latencyN.Add(1)
	c.latencyS.Add(ev.LatencyMS)
	c.bumpMinute()

	s := c.shardFor(ev.UserID)
	ts := time.Now().Unix() / BucketSeconds * BucketSeconds

	s.mu.Lock()
	rk := ev.UserID + "|" + itoa(ts)
	r, ok := s.rollups[rk]
	if !ok {
		r = &rollupAcc{userID: ev.UserID, ts: ts}
		s.rollups[rk] = r
	}
	r.total++
	r.latency += ev.LatencyMS
	switch ev.Action {
	case "blocked":
		r.blocked++
	case "cached":
		r.cached++
	default:
		r.allowed++
	}

	if ev.Domain != "" {
		tk := ev.UserID + "|" + itoa(ts) + "|" + ev.Domain
		t, ok := s.tops[tk]
		if !ok {
			t = &topAcc{userID: ev.UserID, ts: ts, domain: ev.Domain}
			s.tops[tk] = t
		}
		t.hits++
		if ev.Action == "blocked" {
			t.blocked++
		}
	}
	s.mu.Unlock()

	// 查询日志走独立的环形缓冲，且受开关控制。
	if c.enabled.Load() && ev.Domain != "" {
		c.ring.push(model.QueryLogRow{
			UserID:    ev.UserID,
			TS:        time.Now().Unix(),
			Domain:    ev.Domain,
			QType:     ev.QType,
			RCode:     ev.RCode,
			Action:    ev.Action,
			Client:    ev.Client,
			LatencyMS: ev.LatencyMS,
		})
	}
}

func (c *Collector) bumpMinute() {
	now := time.Now().Unix()
	start := c.minuteStart.Load()
	if now-start >= 60 {
		// CAS 失败说明别的 goroutine 已经翻页，本次不重复计数。
		if c.minuteStart.CompareAndSwap(start, now) {
			c.minuteCount.Store(0)
		}
	}
	c.minuteCount.Add(1)
}

func (c *Collector) shardFor(userID string) *shard {
	h := fnv.New32a()
	h.Write([]byte(userID))
	return c.shards[h.Sum32()%shardCount]
}

// itoa 是无分配的整数转字符串（避免 strconv 的反射路径与临时对象）。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Flush 取出并清空当前聚合结果，供后台批量落库。
//
// 返回值是「自上次 Flush 以来的增量」，而不是累计值 ——
// 因为落库用的是 UPSERT 累加语义（见 store.WriteStats）。
func (c *Collector) Flush() (rollups []RollupRow, tops []TopRow, logs []model.QueryLogRow) {
	for _, s := range c.shards {
		s.mu.Lock()
		for _, r := range s.rollups {
			rollups = append(rollups, RollupRow{
				UserID: r.userID, TS: r.ts, Total: r.total, Blocked: r.blocked,
				Cached: r.cached, Allowed: r.allowed, LatencySum: r.latency,
			})
		}
		for _, t := range s.tops {
			tops = append(tops, TopRow{
				UserID: t.userID, TS: t.ts, Domain: t.domain, Hits: t.hits, Blocked: t.blocked,
			})
		}
		s.rollups = make(map[string]*rollupAcc, 64)
		s.tops = make(map[string]*topAcc, 128)
		s.mu.Unlock()
	}
	logs = c.ring.drain()
	return rollups, tops, logs
}

// RollupRow 是聚合结果的一行。
type RollupRow struct {
	UserID     string
	TS         int64
	Total      int64
	Blocked    int64
	Cached     int64
	Allowed    int64
	LatencySum int64
}

// TopRow 是 TOP 域名聚合结果的一行。
type TopRow struct {
	UserID  string
	TS      int64
	Domain  string
	Hits    int64
	Blocked int64
}

// Counters 返回进程级累计计数。
type Counters struct {
	Total      int64   `json:"total"`
	Blocked    int64   `json:"blocked"`
	Cached     int64   `json:"cached"`
	Allowed    int64   `json:"allowed"`
	Rewritten  int64   `json:"rewritten"`
	AvgLatency float64 `json:"avg_latency_ms"`
	QPS        float64 `json:"qps"`
	RingSize   int     `json:"query_log_buffered"`
}

// Counters 返回当前的累计计数与实时 QPS。
func (c *Collector) Counters() Counters {
	n := c.latencyN.Load()
	var avg float64
	if n > 0 {
		avg = float64(c.latencyS.Load()) / float64(n)
	}
	elapsed := time.Now().Unix() - c.minuteStart.Load()
	if elapsed < 1 {
		elapsed = 1
	}
	return Counters{
		Total:      c.total.Load(),
		Blocked:    c.blocked.Load(),
		Cached:     c.cached.Load(),
		Allowed:    c.allowed.Load(),
		Rewritten:  c.rewrite.Load(),
		AvgLatency: avg,
		QPS:        float64(c.minuteCount.Load()) / float64(elapsed),
		RingSize:   c.ring.len(),
	}
}

// === 环形缓冲 ===

// ring 是固定容量的查询日志缓冲。
//
// 满时覆盖最旧的一条：查询日志是排障用的，宁可丢最早的也不要阻塞查询，
// 更不能让它无限增长吃光内存（文档 R11）。
type ring struct {
	mu   sync.Mutex
	buf  []model.QueryLogRow
	head int
	size int
	cap  int
}

func newRing(capacity int) *ring {
	if capacity <= 0 {
		capacity = 1000
	}
	return &ring{buf: make([]model.QueryLogRow, capacity), cap: capacity}
}

func (r *ring) push(row model.QueryLogRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := (r.head + r.size) % r.cap
	if r.size == r.cap {
		// 已满：覆盖最旧位置并推进 head。
		r.buf[r.head] = row
		r.head = (r.head + 1) % r.cap
		return
	}
	r.buf[idx] = row
	r.size++
}

// drain 取出全部条目并清空。
func (r *ring) drain() []model.QueryLogRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil
	}
	out := make([]model.QueryLogRow, 0, r.size)
	for i := 0; i < r.size; i++ {
		out = append(out, r.buf[(r.head+i)%r.cap])
	}
	r.head = 0
	r.size = 0
	return out
}

func (r *ring) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}
