package subsub

import (
	"context"
	"sync"
	"time"
)

// 拉取去重：同一份内容在一轮刷新里只下载、只解析一次。
//
// 背景：同一个规则源常被多个用户（或同一个用户）用不同形态的链接添加 ——
// GitHub raw、jsDelivr、各种加速前缀。这些链接字符串不同但内容相同，
// 旧实现会各拉一遍：一份 24 万条的列表被下两次、解析两次、`||domain^`
// 正则跑两遍。CPU 和出口带宽都白花。
//
// 这一层只做「下载与解析」的共享：
//   - 不共享 sub_entries —— 每个订阅仍然各自持有自己的条目，
//     快照结构不变。共享快照要改 rules 层的层构建，一旦判等判错，
//     一个人的规则会套到另一个人身上，代价太大。
//   - 键是 CanonicalURL（见 canonical.go），判不准的天然落在不同键上，
//     相当于不做去重 —— 这是刻意选的保守行为。

// sharedFetch 是一次可被多个订阅共用的拉取结果。
type sharedFetch struct {
	done chan struct{}
	res  *FetchResult
	err  error
}

// fetchCache 按内容等价键合并同一轮的并发拉取。
//
// 生命周期由调用方控制（一轮 RefreshAll 一个实例），不跨轮复用：
// 跨轮复用会让 ETag 语义变得难以推理 —— 某个订阅拿到 304、另一个拿到
// 200，共用一份缓存时谁赢谁输没有正确答案。一轮一清最干净。
type fetchCache struct {
	mu       sync.Mutex
	inflight map[string]*sharedFetch
	// 一轮内已完成的结果，避免同组第二个订阅再走一次网络。
	done map[string]*sharedFetch
	// savedFetches 是因去重而省掉的拉取次数（供日志与页面上报）。
	savedFetches int
}

func newFetchCache() *fetchCache {
	return &fetchCache{
		inflight: map[string]*sharedFetch{},
		done:     map[string]*sharedFetch{},
	}
}

// do 用 key 合并并发调用：同一 key 只有第一个真正执行 fn，其余等它的结果。
//
// 注意 etag/lastModified 的取舍：同组里不同的订阅可能有不同的 ETag
// （一个上次成功、一个一直失败）。共用一次拉取时用**第一个**到达者的
// 条件头 —— 拿到 304 的话，那些没有 ETag 的订阅会沿用旧条目，
// 状态仍标 ok，不会丢规则。反过来（用空 ETag 强制全量）会每次都重下
// 几十 MB，违背去重的初衷。
func (c *fetchCache) do(key string, fn func() (*FetchResult, error)) (*FetchResult, error) {
	c.mu.Lock()
	if d, ok := c.done[key]; ok {
		c.savedFetches++
		c.mu.Unlock()
		<-d.done
		return d.res, d.err
	}
	if f, ok := c.inflight[key]; ok {
		c.savedFetches++
		c.mu.Unlock()
		<-f.done
		return f.res, f.err
	}
	f := &sharedFetch{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()

	f.res, f.err = fn()

	c.mu.Lock()
	close(f.done)
	delete(c.inflight, key)
	c.done[key] = f
	c.mu.Unlock()
	return f.res, f.err
}

// parseCache 按「内容等价键 + 列表类型」缓存解析结果。
//
// 键里必须带 ListType：同一份内容按 blocklist 和 allowlist 解析，
// 得到的 entries 不同（kind 默认值由 list_type 决定），复用会串类别。
type parseCache struct {
	mu sync.Mutex
	m  map[string]*ParseResult
}

func newParseCache() *parseCache {
	return &parseCache{m: map[string]*ParseResult{}}
}

func (c *parseCache) get(key string) (*ParseResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *parseCache) put(key string, v *ParseResult) {
	c.mu.Lock()
	c.m[key] = v
	c.mu.Unlock()
}

// refreshSession 是一轮刷新的共享上下文。
//
// 为 nil 时一切都退化成「每个订阅各拉各的」，也就是去重前的行为 ——
// 这样单个订阅的「立即刷新」（用户手动点）不会去共享别的东西。
type refreshSession struct {
	fetch *fetchCache
	parse *parseCache
	start time.Time
}

func newRefreshSession() *refreshSession {
	return &refreshSession{
		fetch: newFetchCache(),
		parse: newParseCache(),
		start: time.Now(),
	}
}

// sessionKey 组合出解析缓存键。
func sessionKey(canonical, listType string) string {
	return canonical + "\x00" + listType
}

var _ = context.Background // 保持 import 稳定，ctx 由调用方传入
