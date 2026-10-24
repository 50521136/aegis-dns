package dns

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/stats"
)

// 这个文件锁住「识别不出租户的请求不写查询日志」。
//
// 背景：全网扫描器（hostname.bind / version.bind）、直接拿 IP 打端口的探测、
// 没绑来源 IP 的设备，全部会落到 user_id="" 这个兜底桶。它们写进日志后，
// 管理员在用户排行榜上会看到一行没有名字的幽灵用户，拦截率接近 100%、
// 缓存命中 0 —— 看起来像数据坏了，实际是一堆扫描器。
//
// 这条行为很容易在后续重构里被「顺手补一条 record」改回去，所以必须有测试守着。
//
// 注意：h.record 在 collector 为 nil 时会直接 return，所以测试**必须传一个
// 真实的 Collector**，否则「没记录」这个断言会永远通过，等于什么都没验证。

// newFallbackTestHandler 构造一个带真实 collector 的 handler。
//
// 兜底策略必须在**构造 snapshot 时**就设成 refused —— registrySnapshot.defaults
// 是私有字段，Defaults() 返回的是副本，事后改那个副本不生效。
func newFallbackTestHandler(t *testing.T) (*Handler, *stats.Collector) {
	t.Helper()
	snap := &model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight:  300,
			QueryTimeout: 10000,
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"1.1.1.1"},
			// 关键：认不出租户时直接拒绝，才会走到要验证的那个分支
			FallbackPolicy: model.FallbackRefused,
			// 关键：ApplyDefaults 会用这个值覆盖 collector 的查询日志开关
			// （handler.go 里的 SetQueryLogEnabled）。不设的话零值 false
			// 会把日志关掉，「不该记录」那条断言就会假通过。
			EnableQueryLog: true,
		},
		Users: []model.UserSnap{
			// 租户 1 登记了来源 IP，用来做「能认出」的对照组
			{UserID: "usr_t1", ClientID: "aaaaaaaaaa", Enabled: true, IPs: []string{"127.0.0.1/32"}},
			{UserID: "usr_t2", ClientID: "bbbbbbbbbb", Enabled: true},
		},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		t.Fatalf("构建 registry 失败: %v", err)
	}

	col := stats.New(64, true)
	h := NewHandler(reg, NewCache(16, 60, 3600), col, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// 关键一步：handler 的运行时默认值（含 FallbackPolicy）来自
	// h.defaults，由 ApplyDefaults 灌入，而不是 registry。
	// 漏了这一步 FallbackPolicy 就是空串，不等于 "refused"，
	// 请求会被当成正常查询去解析 —— 测试会「通过」但验证的是错误路径。
	h.ApplyDefaults(snap.Defaults)
	return h, col
}

// logCount 返回 collector 环形缓冲里当前的查询日志条数。
//
// 用 Counters().RingSize 而不是 Flush()：Flush 会把缓冲取走并清空，
// 调试时多个断言会互相干扰；RingSize 是只读的当前长度。
func logCount(c *stats.Collector) int {
	return c.Counters().RingSize
}

// TestFallbackRefusedNotRecorded 断言兜底拒绝只计数、不落日志。
func TestFallbackRefusedNotRecorded(t *testing.T) {
	h, col := newFallbackTestHandler(t)

	req := new(dns.Msg)
	req.SetQuestion("hostname.bind.", dns.TypeTXT)

	// 不带任何租户线索、来源 IP 也不在 registry 里 → 认不出租户
	w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 5353}}
	h.Handle(w, req, &IdentifyHint{Proto: "udp"})

	if got := logCount(col); got != 0 {
		t.Fatalf("兜底拒绝不该写查询日志，实际写了 %d 条", got)
	}
	if got := h.fallbackRefused.Load(); got != 1 {
		t.Fatalf("应计入 fallbackRefused 计数器，实际 %d", got)
	}
	if w.msg == nil {
		t.Fatal("必须仍然回 REFUSED 响应，不能静默丢包")
	}
	if w.msg.Rcode != dns.RcodeRefused {
		t.Fatalf("应答码应为 REFUSED，实际 %d", w.msg.Rcode)
	}
}

// TestFallbackRefusedCounterAccumulates 断言计数器累加（保留可观测性）。
//
// 不落日志不等于不统计：还要能看出「有没有人在乱打」。
func TestFallbackRefusedCounterAccumulates(t *testing.T) {
	h, col := newFallbackTestHandler(t)

	for i := 0; i < 3; i++ {
		req := new(dns.Msg)
		req.SetQuestion("version.bind.", dns.TypeTXT)
		w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 5353}}
		h.Handle(w, req, &IdentifyHint{Proto: "udp"})
	}
	if got := h.fallbackRefused.Load(); got != 3 {
		t.Fatalf("三次兜底拒绝应计数为 3，实际 %d", got)
	}
	if got := logCount(col); got != 0 {
		t.Fatalf("三次兜底拒绝都不该写日志，实际 %d 条", got)
	}
}

// TestIdentifiedTenantStillRecorded 断言「能认出租户」的查询仍然记录。
//
// 这是对照组：不能为了挡噪音把正常日志一起关掉。
// 用租户 1 登记的来源 IP 127.0.0.1 让 identify 认出租户。
func TestIdentifiedTenantStillRecorded(t *testing.T) {
	h, col := newFallbackTestHandler(t)

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}}
	h.Handle(w, req, &IdentifyHint{Proto: "udp"})

	if got := logCount(col); got == 0 {
		t.Fatal("识别出租户的查询必须照常记录日志（对照组失败说明测试本身没生效）")
	}
	if got := h.fallbackRefused.Load(); got != 0 {
		t.Fatalf("认出租户的查询不该计入 fallbackRefused，实际 %d", got)
	}
}

// TestUnknownClientIDStillRecorded 断言「带了 client_id 但查不到」仍然记录。
//
// 这类和纯扫描器不同：它意味着用户配置错了地址（子域名抄错、账号被删、
// 旧二维码），管理员需要看到这条日志才能帮他定位。所以只有「什么线索都没有」
// 的请求才不记录。
func TestUnknownClientIDStillRecorded(t *testing.T) {
	h, col := newFallbackTestHandler(t)

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	w := &captureWriter{remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 5353}}
	h.Handle(w, req, &IdentifyHint{ClientID: "zzzzzzzzzz", Source: "sni", Proto: "dot"})

	if got := logCount(col); got == 0 {
		t.Fatal("带无效 client_id 的请求应当记录：用户配错了地址，管理员需要看到")
	}
}
