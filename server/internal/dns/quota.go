package dns

import (
	"sync"
	"time"
)

// dailyQuota 按用户统计「当天已解析次数」，用于会员请求量上限。
//
// 为什么放在 dnsd 进程内存里而不是查库：
// 这是每条查询都要过的热路径，查库等于给每次解析加一次 IO。
// 代价是进程重启后计数归零 —— 对「每日上限」这种软约束可以接受，
// 而且重启本身是运维事件，用户不会因此获得持续性的超额收益。
//
// 为什么只按用户计数、不按 client_id 或 IP：
// 配额是发给「人」的，一个人可能有手机、电脑、路由器三个接入点，
// 按设备算会让上限形同虚设。
type dailyQuota struct {
	mu sync.Mutex
	m  map[string]*dayCount
	// day 是当前统计的自然日（UTC，unix/86400）。跨天时整表清空。
	day int64
}

type dayCount struct {
	n int64
}

// newDailyQuota 创建一个计数器。
func newDailyQuota() *dailyQuota {
	return &dailyQuota{m: make(map[string]*dayCount, 256)}
}

// allow 判断该用户今天是否还能再解析一次，并顺手计数。
//
// limit <= 0 表示不限量，直接放行且不计数（省一次 map 写）。
// 超过上限后不再累加：上限是硬性拒绝，继续累加只会让数字变得没有意义。
func (q *dailyQuota) allow(uid string, limit int64, now time.Time) bool {
	if limit <= 0 || uid == "" {
		return true
	}
	day := now.Unix() / 86400

	q.mu.Lock()
	defer q.mu.Unlock()

	// 跨天：整表清空比逐条判断便宜得多，而且不会有残留条目泄漏。
	if q.day != day {
		q.day = day
		q.m = make(map[string]*dayCount, len(q.m))
	}

	c, ok := q.m[uid]
	if !ok {
		c = &dayCount{}
		q.m[uid] = c
	}
	if c.n >= limit {
		return false
	}
	c.n++
	return true
}

// usage 返回该用户今天的已用次数（用于面板展示与排障）。
func (q *dailyQuota) usage(uid string, now time.Time) int64 {
	day := now.Unix() / 86400
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.day != day {
		return 0
	}
	if c, ok := q.m[uid]; ok {
		return c.n
	}
	return 0
}
