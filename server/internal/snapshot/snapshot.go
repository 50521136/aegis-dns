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
	"os"
	"path/filepath"
	"strconv"
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

	snap := &model.Snapshot{
		Version:      version,
		Schema:       model.SnapshotSchema,
		GeneratedAt:  time.Now().UTC(),
		ChangedUsers: changedUsers,
		Defaults: model.Defaults{
			Domain:         settings.Domain,
			Upstreams:      settings.Upstreams,
			BlockingMode:   settings.BlockingMode,
			CustomIP:       settings.CustomIP,
			FallbackPolicy: settings.FallbackPolicy,
			CacheSize:      settings.CacheSize,
			CacheMinTTL:    settings.CacheMinTTL,
			CacheMaxTTL:    settings.CacheMaxTTL,
			RateLimitQPS:   settings.RateLimitQPS,
			MaxInflight:    settings.MaxInflight,
			QueryTimeout:   settings.QueryTimeoutMS,
			QueryLogSize:   settings.QueryLogSize,
			EnableQueryLog: settings.EnableQueryLog,
		},
		Users: make([]model.UserSnap, 0, len(users)),
	}

	for _, u := range users {
		us := model.UserSnap{
			UserID:   u.ID,
			ClientID: u.ClientID,
			Enabled:  u.Enabled,
			IPs:      ipsByUser[u.ID],
		}
		if rules, ok := rulesByUser[u.ID]; ok {
			us.Rules = rules
		}
		if subs, ok := subsByUser[u.ID]; ok {
			us.SubBlock = subs[0]
			us.SubAllow = subs[1]
		}
		snap.Users = append(snap.Users, us)
	}
	return snap, nil
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
