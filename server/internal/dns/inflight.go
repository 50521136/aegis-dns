package dns

import (
	"context"
	"strconv"
	"sync"

	"github.com/miekg/dns"
)

// 这个文件实现「同一域名并发查询合并」（singleflight）。
//
// 为什么需要：
//
// 手机、路由器、智能音箱这类客户端在冷启动或重连时会**同时**发出多条
// 完全相同的查询（一次连发 5～10 条是常态）。缓存只能挡住「前一条已经
// 写完之后」的请求 —— 并发到达的这一批全部 miss，每一条都各自去打上游、
// 各自写缓存。
//
// 线上实测 onrt-stsdk.vivo.com.cn 在 10 分钟内被查 92 次，97% 的间隔
// 小于 60 秒、中位数是 0 秒（同一秒内多条），但缓存命中率只有 66%。
// 没命中的那 31 次不是 TTL 过期，而是并发击穿。
//
// 后果不只是多打几次上游：上游看到的是「同一秒内同一个域名被问 N 次」，
// 这本身就会触发部分公共 DNS 的限流，反过来把延迟推高。
//
// 设计约束：
//
//  1. **只合并会写缓存的那类请求**。带 ECS 的请求按既有设计不进缓存
//     （见 cache.PutWith 的 hasECS 判断），合并它们会把 leader 的网段
//     答案发给别的客户端 —— 那是错误的结果，不是性能问题。
//  2. **DO 位与 DNSSEC 开关参与 key**。开了 DNSSEC 的请求会多拿 RRSIG，
//     和没开的不是同一个答案，合并会互相污染。
//  3. **等待者必须拿副本**。leader 拿到应答后会继续改它（回填报文 ID、
//     置 Compress、写缓存），共享同一个 *dns.Msg 是数据竞争。
//  4. **等待者必须能被自己的超时打断**。上游卡住时不能让等待者跟着
//     无限等 —— 每个请求有自己的 ctx deadline。

// flightCall 是一次进行中的上游查询。
type flightCall struct {
	done chan struct{}
	// resp 是所有等待者共享的只读副本；等待者自己再 Copy 一次才能用。
	resp *dns.Msg
	src  string
	err  error
}

// flightGroup 是按 key 合并并发查询的最小实现。
type flightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

func newFlightGroup() *flightGroup {
	return &flightGroup{m: make(map[string]*flightCall, 64)}
}

// FlightKey 由缓存键和 DNSSEC 开关拼成。
// 用 strconv 而不是 fmt 拼：这条路径每条未命中查询都要走一次，
// fmt 的反射开销在这里是白给的。
func FlightKey(k CacheKey, dnssec bool) string {
	buf := make([]byte, 0, len(k.UserID)+len(k.QName)+24)
	buf = append(buf, k.UserID...)
	buf = append(buf, '|')
	buf = append(buf, k.QName...)
	buf = append(buf, '|')
	buf = strconv.AppendUint(buf, uint64(k.QType), 10)
	buf = append(buf, '|')
	if k.DO {
		buf = append(buf, '1')
	} else {
		buf = append(buf, '0')
	}
	if dnssec {
		buf = append(buf, '|', 's')
	}
	return string(buf)
}

// Do 执行一次合并查询。
//
// key 为空字符串表示「不参与合并」，直接执行 fn。
//
// shared 为 true 表示这次调用是等待者，resp 是从别的请求那里拿来的共享
// 副本 —— 调用方必须自己 Copy 之后才能修改或写缓存。
func (g *flightGroup) Do(
	ctx context.Context,
	key string,
	fn func() (*dns.Msg, string, error),
) (resp *dns.Msg, src string, err error, shared bool) {
	if g == nil || key == "" {
		resp, src, err = fn()
		return resp, src, err, false
	}

	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.resp, c.src, c.err, true
		case <-ctx.Done():
			// 上游比自己的 deadline 还慢：直接放弃，不要拖着自己的
			// 客户端一起等。返回 ctx 的错误，调用方按上游失败处理。
			return nil, "", ctx.Err(), true
		}
	}
	c := &flightCall{done: make(chan struct{})}
	g.m[key] = c
	g.mu.Unlock()

	// leader 独占 fn 的返回值；给等待者的那份在这里复制。
	// 顺序很关键：先写 c.resp/c.src/c.err，再从 map 摘除，最后 close(done)
	// —— 否则新来的请求会拿到一个还没填好的 call。
	resp, src, err = fn()
	if resp != nil {
		c.resp = resp.Copy()
	}
	c.src = src
	c.err = err

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
	close(c.done)

	return resp, src, err, false
}

// Len 返回当前进行中的合并组数（系统状态展示用）。
func (g *flightGroup) Len() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.m)
}

// flightKeyOf 判断这次查询能不能参与合并，能则返回合并键。
//
// 两条排除条件：
//
//   - 缓存键构造失败（没有 question 等畸形报文）→ 不合并，让它单独走，
//     否则所有畸形报文会挤在同一个 key 上互相干扰；
//   - 请求自带 ECS → 不合并。带 ECS 的应答本来就按「客户端网段」定制，
//     按既有设计也不进缓存；合并会把 leader 网段的答案发给别的网段。
func flightKeyOf(k CacheKey, cacheable, dnssec bool, req *dns.Msg) (string, bool) {
	if !cacheable {
		return "", false
	}
	if hasECS(req) {
		return "", false
	}
	return FlightKey(k, dnssec), true
}
