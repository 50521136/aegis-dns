package dns

import (
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/rules"
)

// Registry 保存全部用户的运行时配置，是 dnsd 的核心数据结构。
//
// 读路径完全无锁：查询先 Load() 拿到一个不可变快照指针，之后只读；
// 热更新时构建全新快照再 Store()，旧快照交给 GC。
// 这是文档 3.3 的设计，也是「配置生效延迟 < 2s 且零停机」的实现基础。
type Registry struct {
	current   atomic.Pointer[registrySnapshot]
	pools     *resolver.Cache
	version   atomic.Int64
	reloads   atomic.Int64
	buildMS   atomic.Int64
	lastBuild atomic.Int64
}

// registrySnapshot 是一次构建产生的不可变配置快照。
type registrySnapshot struct {
	version    int64
	byClientID map[string]*UserRuntime
	byUserID   map[string]*UserRuntime
	byIP       *cidrIndex
	fallback   *UserRuntime
	defaults   model.Defaults
	loadedAt   time.Time
	// users 是全部租户（含禁用的），供管理统计使用。
	users []*UserRuntime
}

// UserRuntime 是单个租户的运行时状态。
//
// Rules 与 Pool 用 atomic.Pointer 单独持有，使增量重载可以只替换某个用户的
// RuleSet，而其余用户继续复用旧对象（文档 7.5 增量重建）。
type UserRuntime struct {
	UserID   string
	ClientID string
	Enabled  bool
	// Rules 是该用户当前的规则集，热更新时整体替换。
	Rules atomic.Pointer[rules.RuleSet]
	// Upstreams 是该用户自定义的上游地址（空表示用全局默认）。
	Upstreams []string
	// Pool 是该用户实际使用的上游池（可能与其他用户共享）。
	Pool *resolver.Pool
	// RuleStats 保存构建时的规则条数统计，供 UI 展示。
	RuleStats rules.Stats
}

// NewRegistry 创建一个空的 Registry。
func NewRegistry(pools *resolver.Cache) *Registry {
	r := &Registry{pools: pools}
	// 放一个空快照，保证 Load 永不为 nil（避免启动瞬间的 nil 判断散落各处）。
	r.current.Store(&registrySnapshot{
		byClientID: map[string]*UserRuntime{},
		byIP:       newCIDRIndex(),
		fallback:   &UserRuntime{UserID: "", ClientID: ""},
	})
	return r
}

// Load 返回当前快照（无锁）。
func (r *Registry) Load() *registrySnapshot { return r.current.Load() }

// Version 返回当前快照版本号。
func (r *Registry) Version() int64 { return r.version.Load() }

// BuildInfo 返回构建耗时与重载次数，供 /admin/system 展示。
func (r *Registry) BuildInfo() (reloads int64, lastBuildMS int64, lastBuildAt int64) {
	return r.reloads.Load(), r.buildMS.Load(), r.lastBuild.Load()
}

// Fallback 返回默认策略的 UserRuntime（未识别请求使用）。
func (s *registrySnapshot) Fallback() *UserRuntime {
	if s == nil || s.fallback == nil {
		return &UserRuntime{}
	}
	return s.fallback
}

// Defaults 返回全局默认配置。
func (s *registrySnapshot) Defaults() model.Defaults {
	if s == nil {
		return model.Defaults{}
	}
	return s.defaults
}

// Users 返回全部租户运行时。
func (s *registrySnapshot) Users() []*UserRuntime {
	if s == nil {
		return nil
	}
	return s.users
}

// LookupClientID 按 client_id 查租户（DoT SNI / DoH Host / 路径式）。
func (s *registrySnapshot) LookupClientID(cid string) *UserRuntime {
	if s == nil || cid == "" {
		return nil
	}
	// client_id 只含小写字母数字，DNS 标签不区分大小写，这里统一转小写。
	return s.byClientID[strings.ToLower(cid)]
}

// LookupHost 按完整主机名（如 k7m2p9xq4a.dns.example.com）识别租户。
//
// 只取第一段作为 client_id：因为主域名固定，多段前缀不会出现在本系统里。
func (s *registrySnapshot) LookupHost(host string) *UserRuntime {
	if s == nil || host == "" {
		return nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	// 去掉可能的端口（Host 头可能带 :443）。
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// IPv6 字面量形式 [::1] 已在上面被截断，这里直接判定非域名。
	if strings.Contains(host, ":") {
		return nil
	}
	label, _, _ := strings.Cut(host, ".")
	if label == "" {
		return nil
	}
	return s.byClientID[label]
}

// LookupIP 按来源 IP 识别租户。
func (s *registrySnapshot) LookupIP(ip net.IP) *UserRuntime {
	if s == nil || s.byIP == nil {
		return nil
	}
	uid, _, ok := s.byIP.Lookup(ip)
	if !ok {
		return nil
	}
	return s.byUserID[uid]
}

// Build 根据快照构建（或增量更新）Registry。
//
// changedUsers 非空时，未列出的用户直接复用当前 Registry 里的 UserRuntime
// 对象与其 RuleSet 指针 —— 这是「增量重建」的全部意义：
// 一个用户改一条规则，不该让另外几百个用户的十万条规则重新建 Trie。
func (r *Registry) Build(snap *model.Snapshot) error {
	start := time.Now()

	if snap.Schema > model.SnapshotSchema {
		return fmt.Errorf("快照 schema 版本 %d 高于本二进制支持的 %d，拒绝加载", snap.Schema, model.SnapshotSchema)
	}
	if snap.Version <= r.version.Load() {
		return nil // 已经是最新，重复加载直接跳过
	}

	// 让上游池引用计数归零，构建完成后 Sweep 掉无人引用的旧池。
	r.pools.BeginGeneration()

	prev := r.current.Load()
	changed := map[string]bool{}
	for _, u := range snap.ChangedUsers {
		changed[u] = true
	}
	incremental := len(changed) > 0

	newSnap := &registrySnapshot{
		version:    snap.Version,
		byClientID: make(map[string]*UserRuntime, len(snap.Users)),
		byUserID:   make(map[string]*UserRuntime, len(snap.Users)),
		byIP:       newCIDRIndex(),
		defaults:   snap.Defaults,
		loadedAt:   time.Now(),
		users:      make([]*UserRuntime, 0, len(snap.Users)),
	}

	for i := range snap.Users {
		us := &snap.Users[i]
		cid := strings.ToLower(us.ClientID)

		// 增量路径：该用户没变且上一版存在，直接复用整个运行时对象
		// （包括已构建好的 RuleSet 指针）。
		if incremental && !changed[us.UserID] {
			if old := prev.LookupClientID(cid); old != nil {
				newSnap.byClientID[cid] = old
				newSnap.byUserID[us.UserID] = old
				newSnap.users = append(newSnap.users, old)
				if old.Pool != nil {
					// 让池继续被引用，Sweep 不会关掉它。
					if _, err := r.pools.Get(upstreamsFor(old.Upstreams, snap.Defaults)); err == nil {
						// 引用计数已累加，忽略返回值。
					}
				}
				for _, c := range us.IPs {
					_ = newSnap.byIP.Add(c, us.UserID, cid)
				}
				continue
			}
		}

		rs, err := rules.Build(us.Rules, us.SubAllow, us.SubBlock)
		if err != nil {
			// 单用户构建失败不影响其他用户（文档 P4 优雅降级）。
			rs, _ = rules.Build(nil, nil, nil)
		}

		ur := &UserRuntime{
			UserID:    us.UserID,
			ClientID:  cid,
			Enabled:   us.Enabled,
			Upstreams: us.Upstreams,
			RuleStats: rs.Stats,
		}
		ur.Rules.Store(rs)

		addrs := upstreamsFor(us.Upstreams, snap.Defaults)
		pool, err := r.pools.Get(addrs)
		if err != nil {
			return fmt.Errorf("为用户 %s 创建上游池失败: %w", us.UserID, err)
		}
		ur.Pool = pool

		newSnap.byClientID[cid] = ur
		newSnap.byUserID[us.UserID] = ur
		newSnap.users = append(newSnap.users, ur)
		for _, c := range us.IPs {
			if err := newSnap.byIP.Add(c, us.UserID, cid); err != nil {
				// 非法 CIDR 只跳过这一条，不让整个快照加载失败。
				continue
			}
		}
	}

	// 默认策略集：未识别请求使用。
	fallbackRules, err := buildFallbackRules(snap)
	if err != nil {
		fallbackRules, _ = rules.Build(nil, nil, nil)
	}
	fallbackPool, err := r.pools.Get(snap.Defaults.Upstreams)
	if err != nil {
		return fmt.Errorf("创建默认上游池失败: %w", err)
	}
	fallback := &UserRuntime{
		UserID:    "",
		ClientID:  "",
		Enabled:   true,
		Pool:      fallbackPool,
		RuleStats: fallbackRules.Stats,
	}
	fallback.Rules.Store(fallbackRules)
	newSnap.fallback = fallback

	// 原子替换：此前的查询仍用旧快照，切换无停机。
	r.current.Store(newSnap)
	r.version.Store(snap.Version)

	// 回收不再被引用的上游池。
	r.pools.Sweep()

	r.reloads.Add(1)
	r.buildMS.Store(time.Since(start).Milliseconds())
	r.lastBuild.Store(time.Now().Unix())
	return nil
}

// upstreamsFor 决定某用户实际使用的上游地址列表。
func upstreamsFor(user []string, def model.Defaults) []string {
	if len(user) > 0 {
		return user
	}
	return def.Upstreams
}

// buildFallbackRules 按 fallback_policy 构建未识别请求使用的规则集。
//
//	passthrough   —— 空规则集，纯转发（默认）
//	refused       —— 空规则集，由 handler 直接返回 REFUSED
//	blocklist_only—— 合并所有租户的订阅黑名单，作为「全局黑名单」
func buildFallbackRules(snap *model.Snapshot) (*rules.RuleSet, error) {
	if snap.Defaults.FallbackPolicy != model.FallbackBlocklistOnly {
		return rules.Build(nil, nil, nil)
	}
	var global []string
	seen := map[string]struct{}{}
	for _, u := range snap.Users {
		if !u.Enabled {
			continue
		}
		for _, d := range u.SubBlock {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			global = append(global, d)
		}
	}
	return rules.Build(nil, nil, global)
}

// poolStats 汇总所有池的上游地址（系统状态展示用）。
func (r *Registry) poolStats() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, u := range r.current.Load().Users() {
		for _, a := range u.Pool.Addrs() {
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	return out
}
