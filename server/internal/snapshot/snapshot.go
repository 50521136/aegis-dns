// Package snapshot 负责配置快照的生成、原子写入与读取。
//
// 为什么用文件而不是消息队列（文档 7.1）：文件本身即是持久化状态，
// dnsd 重启后直接读文件即可恢复，不依赖 apid 在线；
// 且不引入任何 RPC，满足架构红线 C1。
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// AtomicWrite 原子写入文件：先写临时文件 → fsync → rename。
//
// rename 在同一文件系统内是原子的，读者要么看到完整旧文件、要么看到完整新文件，
// 绝不会读到写了一半的内容。这也是 dnsd 侧可以无锁读文件的前提。
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建快照目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时快照失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败路径清理；成功 rename 后此调用无副作用

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时快照失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync 临时快照失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时快照失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("原子替换快照失败: %w", err)
	}
	// 再 fsync 一次目录项，确保 rename 本身也落盘（掉电后可恢复）。
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Build 从数据库构建一份完整快照。
//
// changedUsers 非空时生成增量快照：只重新序列化这些用户的数据，
// 但 JSON 里仍然包含全部用户 —— dnsd 据 ChangedUsers 决定「只重建谁」。
// 之所以不真的只写部分用户，是因为文件是 dnsd 重启后的唯一数据来源，
// 必须始终是自包含的完整状态。
func Build(ctx context.Context, st *store.Store, version int64, changedUsers []string) (*model.Snapshot, error) {
	settings, err := st.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	// 会员档位按生成快照这一刻判定：到期后下一次重建快照就会自动降档，
	// 不需要任何人手动改用户的档位字段。
	now := time.Now()
	// 档位表一次取出：按用户算配额时纯内存查，避免 N+1。
	tiers, err := st.TierMap(ctx)
	if err != nil {
		return nil, err
	}
	users, err := st.ListUsersAll(ctx)
	if err != nil {
		return nil, err
	}
	rulesByUser, err := st.AllRules(ctx)
	if err != nil {
		return nil, err
	}
	ipsByUser, err := st.AllUserIPs(ctx)
	if err != nil {
		return nil, err
	}
	subsByUser, err := st.AllSubEntries(ctx)
	if err != nil {
		return nil, err
	}
	// 全局层：管理员的 is_global 规则与订阅，全体开启全局的用户共享同一份。
	globalRules, err := st.AllGlobalRules(ctx)
	if err != nil {
		return nil, err
	}
	globalBlock, globalAllow, err := st.AllGlobalSubEntries(ctx)
	if err != nil {
		return nil, err
	}

	snap := &model.Snapshot{
		Version:      version,
		Schema:       model.SnapshotSchema,
		GeneratedAt:  time.Now().UTC(),
		ChangedUsers: changedUsers,
		Defaults: model.Defaults{
			Domain:          settings.Domain,
			Upstreams:       settings.Upstreams,
			BootstrapDNS:    settings.BootstrapDNS,
			BlockingMode:    settings.BlockingMode,
			CustomIP:        settings.CustomIP,
			FallbackPolicy:  settings.FallbackPolicy,
			CacheSize:       settings.CacheSize,
			CacheMinTTL:     settings.CacheMinTTL,
			CacheMaxTTL:     settings.CacheMaxTTL,
			RateLimitQPS:    settings.RateLimitQPS,
			MaxInflight:     settings.MaxInflight,
			QueryTimeout:    settings.QueryTimeoutMS,
			QueryLogSize:    settings.QueryLogSize,
			EnableQueryLog:  settings.EnableQueryLog,
			EnableDoH:       settings.EnableDoH,
			EnableDoT:       settings.EnableDoT,
			EnablePlainDNS:  settings.EnablePlainDNS,
			EnableECS:       settings.EnableECS,
			EnableDNSSEC:    settings.EnableDNSSEC,
			AllowRootDomain: settings.AllowRootDomain,
		},
		Global: model.GlobalSnap{
			Rules:    globalRules,
			SubBlock: globalBlock,
			SubAllow: globalAllow,
		},
		Users: make([]model.UserSnap, 0, len(users)),
	}
	// 全局指纹在填充完全局层之后计算（见函数末尾）

	for _, u := range users {
		us := model.UserSnap{
			UserID:         u.ID,
			ClientID:       u.ClientID,
			Enabled:        u.Enabled,
			UseGlobalRules: u.UseGlobalRules,
			IPs:            ipsByUser[u.ID],
			// 档位在这里被压成一个数字：dnsd 不需要知道「会员」是什么，
			// 它只要知道这个用户每天能查多少次。
			DailyQueries: model.QuotaForUser(u, settings, tiers, now).DailyQueries,
			MemberUntil:  u.MemberUntil,
		}
		if rules, ok := rulesByUser[u.ID]; ok {
			us.Rules = rules
		}
		if subs, ok := subsByUser[u.ID]; ok {
			us.SubBlock = subs[0]
			us.SubAllow = subs[1]
		}
		// 指纹必须在规则与订阅填好之后算：只覆盖影响解析结果的内容，
		// 不含配额与会员期 —— 那些不影响任何已缓存的应答，
		// 算进来会让「加个会员天数」白白清掉这个用户的缓存。
		us.Fingerprint = userFingerprint(u, us)
		// 该用户自己的「清除缓存」序号（普通用户只能清自己那份）
		if v, err := st.GetMeta(ctx, "cache_flush_user_seq:"+u.ID); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				us.CacheFlushSeq = n
			}
		}
		snap.Users = append(snap.Users, us)
	}
	snap.GlobalFingerprint = globalFingerprint(settings, snap)
	// 清除缓存请求序号：读不到就当 0（不清），不能让一次读库失败清掉全站缓存
	if v, err := st.GetMeta(ctx, "cache_flush_seq"); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			snap.CacheFlushSeq = n
		}
	}
	return snap, nil
}

// userFingerprint 计算单个用户「影响解析结果」的指纹。
//
// 覆盖：规则集、订阅压平后的域名列表、启用状态、是否挂全局层。
// 不覆盖：配额、会员期、上游自定义（上游改了会体现在 defaults/global 里，
// 或者由快照版本推进触发重建，不需要在这里重复表达）。
func userFingerprint(u *model.User, us model.UserSnap) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "e=%t|g=%t|", us.Enabled, us.UseGlobalRules)
	for _, r := range us.Rules {
		fmt.Fprintf(h, "%s,%s,%s,%s,%t;", r.Kind, r.Pattern, r.Value, strings.Join(r.QTypes, "+"), r.Enabled)
	}
	// 订阅条目数量级是几十万，逐条格式化太慢；直接喂字节更快。
	// 用长度分隔避免 ["ab","c"] 与 ["a","bc"] 撞成同一个哈希。
	h.Write([]byte("|B|"))
	for _, d := range us.SubBlock {
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	h.Write([]byte("|A|"))
	for _, d := range us.SubAllow {
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	_ = u
	return strconv.FormatUint(h.Sum64(), 16)
}

// globalFingerprint 计算全局层与「影响应答的默认值」的指纹。
//
// 这些内容一变，所有租户的缓存都得清：拦截模式决定拦截时返回什么 IP，
// 上游决定应答内容，主域名开关决定裸域名能不能用。
func globalFingerprint(settings *model.GlobalSettings, snap *model.Snapshot) string {
	h := fnv.New64a()
	d := snap.Defaults
	fmt.Fprintf(h, "bm=%s|ip=%s|fp=%s|root=%t|qto=%d|cmin=%d|cmax=%d|up=%s|bs=%s|",
		d.BlockingMode, d.CustomIP, d.FallbackPolicy, settings.AllowRootDomain,
		d.QueryTimeout, d.CacheMinTTL, d.CacheMaxTTL,
		strings.Join(d.Upstreams, ","), strings.Join(d.BootstrapDNS, ","))
	for _, r := range snap.Global.Rules {
		fmt.Fprintf(h, "%s,%s,%s,%s,%t;", r.Kind, r.Pattern, r.Value, strings.Join(r.QTypes, "+"), r.Enabled)
	}
	h.Write([]byte("|GB|"))
	for _, x := range snap.Global.SubBlock {
		h.Write([]byte(x))
		h.Write([]byte{0})
	}
	h.Write([]byte("|GA|"))
	for _, x := range snap.Global.SubAllow {
		h.Write([]byte(x))
		h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// Marshal 把快照序列化为 JSON。
//
// 用 json.Marshal 而不是 Encoder+SetIndent：快照文件只在排障时人工阅读，
// 紧凑格式能显著降低几十万条订阅条目时的文件体积（体积直接影响 dnsd 加载耗时）。
func Marshal(snap *model.Snapshot) ([]byte, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("序列化快照失败: %w", err)
	}
	return data, nil
}

// Write 构建并原子写入快照，返回写入的快照对象。
func Write(ctx context.Context, st *store.Store, path string, version int64, changedUsers []string) (*model.Snapshot, error) {
	snap, err := Build(ctx, st, version, changedUsers)
	if err != nil {
		return nil, err
	}
	data, err := Marshal(snap)
	if err != nil {
		return nil, err
	}
	if err := AtomicWrite(path, data); err != nil {
		return nil, err
	}
	return snap, nil
}

// Read 从磁盘读取快照。
func Read(path string) (*model.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap model.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("解析快照 %s 失败: %w", path, err)
	}
	return &snap, nil
}

// ReadVersion 只读取快照头部并提取 version 字段。
//
// 供 dnsd 的 30s 轮询兜底通道使用：几十万条订阅的快照可能有几十 MB，
// 每 30s 完整解析一次纯属浪费，这里只扫头部若干字节找 version。
func ReadVersion(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if n == 0 {
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	return scanVersion(buf[:n])
}

// scanVersion 在 JSON 头部字节里定位 `"version": <数字>`。
//
// 手写扫描而不是 json.Unmarshal：传入的是被截断的头部，完整解析必然失败。
// 快照的第一个字段就是 version，落在前 64 字节内，所以这个简化是安全的；
// 若结构变化导致扫描不到，返回 0 —— 调用方会退化为「按 mtime 判断」。
func scanVersion(head []byte) (int64, error) {
	const key = `"version":`
	idx := bytes.Index(head, []byte(key))
	if idx < 0 {
		return 0, nil
	}
	rest := head[idx+len(key):]
	// 跳过空白。
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '	') {
		i++
	}
	start := i
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if start == i {
		return 0, nil
	}
	return strconv.ParseInt(string(rest[start:i]), 10, 64)
}
