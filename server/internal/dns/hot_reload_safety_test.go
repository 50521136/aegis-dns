package dns

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
)

// 历史背景：这两条断言原本守的是「带 ECS / 会注入 ECS 的请求一律不进缓存」。
//
// 那个做法换来的是「开 ECS 就把全部缓存关掉」——线上实测命中率从 50% 掉到
// 1%。正确形态是「网段进缓存分层，而不是把请求踢出缓存」，所以断言反过来了：
// 带 ECS 的请求**必须能**构造缓存键，由 Cache 的网段层负责隔离。
func TestCacheKeyFromAcceptsECSRequest(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	m.SetEdns0(1232, false)
	m.IsEdns0().Option = append(m.IsEdns0().Option, &dns.EDNS0_SUBNET{
		Code:          dns.EDNS0SUBNET,
		Family:        1,
		SourceNetmask: 24,
		Address:       net.IPv4(192, 0, 2, 1),
	})
	if _, cacheable := CacheKeyFrom("user-a", m); !cacheable {
		t.Fatal("带 ECS 的请求应当能构造缓存键（网段由网段层隔离，不能整个踢出缓存）")
	}
}

func TestCacheKeyFromAcceptsOrdinaryEDNS(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)
	m.SetEdns0(1232, false)
	if _, cacheable := CacheKeyFrom("user-a", m); !cacheable {
		t.Fatal("普通 EDNS 请求不带 ECS，应允许缓存")
	}
}

func TestLoaderDoesNotApplySameVersionTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	snap := &model.Snapshot{
		Version: 7,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"223.5.5.5"},
			MaxInflight:  300,
			QueryTimeout: 5000,
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("序列化快照失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入快照失败: %v", err)
	}

	reg := NewRegistry(resolver.NewCache())
	h := NewHandler(reg, NewCache(64, 60, 3600), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l := NewLoader(path, reg, h, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 线上日志里同一 version 被加载过两次：fsnotify 与 30s 轮询会同时触发，
	// 两次都读到同一份文件。回调（刷新运行时参数）跟着跑两次是无谓开销，
	// 更糟的是每次都会重算一遍缓存失效。
	reloads := 0
	l.OnReload(func(*model.Snapshot) { reloads++ })

	if err := l.LoadOnce(); err != nil {
		t.Fatalf("首次加载失败: %v", err)
	}
	if err := l.LoadOnce(); err != nil {
		t.Fatalf("重复加载应静默跳过，实际报错: %v", err)
	}

	if reloads != 1 {
		t.Fatalf("同一版本只应应用一次，实际回调 %d 次", reloads)
	}
	if got := reg.Version(); got != 7 {
		t.Fatalf("registry 版本=%d，期望 7", got)
	}
}

func TestSemaphoreReleaseUsesAcquiredGeneration(t *testing.T) {
	old := semaphore(make(chan struct{}, 1))
	current := &atomic.Pointer[semaphore]{}
	current.Store(&old)

	release, ok := acquireSemaphore(current)
	if !ok {
		t.Fatal("首次 acquire 应成功")
	}

	next := semaphore(make(chan struct{}, 1))
	current.Store(&next)
	release()

	if len(old) != 0 {
		t.Fatal("release 未从 acquire 时持有的旧信号量归还名额")
	}
	if len(next) != 0 {
		t.Fatal("release 不应误归还到新信号量")
	}
}
