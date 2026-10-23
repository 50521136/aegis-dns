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

// 这个文件用基准测试量化热路径开销，用来判断「优化哪一处才真的有用」。
//
// 背景：上几轮优化（拦截进缓存、singleflight、规则 TTL、命中率口径修复）
// 都是靠读代码 + 线上指标定位的。要继续往下挖，需要知道每条查询在
// 「缓存命中 / 被拦截 / 走到上游」三种路径上各自花多少 CPU 和分配，
// 否则容易在没意义的地方反复改（前几轮就误判过两次）。
//
// 只测纯 CPU 路径，不含真实上游（那部分由 9ms 的网络 RTT 主导，
// 任何本地计算优化在它面前都不显著）。

func benchHandler(b *testing.B) (*Handler, *dns.Msg) {
	b.Helper()
	snap := &model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight:  1000,
			QueryTimeout: 10000,
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"1.1.1.1:53"},
			CacheSize:    4096,
			CacheMinTTL:  60,
			CacheMaxTTL:  3600,
		},
		Users: []model.UserSnap{
			{
				UserID:   "usr_bench",
				ClientID: "aaaaaaaaaa",
				Enabled:  true,
				IPs:      []string{"127.0.0.1/32"},
				SubBlock: []string{"ads.example.net", "tracker.example.org", "metrics.example.com"},
			},
		},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		b.Fatalf("构建 registry 失败: %v", err)
	}
	// 开查询日志：线上 enable_query_log=1，日志与详情拼接是热路径的一部分。
	col := stats.New(1024, true)
	h := NewHandler(reg, NewCache(4096, 60, 3600), col,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rs := reg.Load(); rs != nil {
		h.ApplyDefaults(rs.Defaults())
	}

	req := new(dns.Msg)
	req.SetQuestion("www.example.com.", dns.TypeA)
	return h, req
}

type benchWriter struct{}

func (benchWriter) LocalAddr() net.Addr         { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53} }
func (benchWriter) RemoteAddr() net.Addr        { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5353} }
func (benchWriter) WriteMsg(*dns.Msg) error     { return nil }
func (benchWriter) Write(b []byte) (int, error) { return len(b), nil }
func (benchWriter) Close() error                { return nil }
func (benchWriter) TsigStatus() error           { return nil }
func (benchWriter) TsigTimersOnly(bool)         {}
func (benchWriter) Hijack()                     {}

// BenchmarkBlockedPath 测「被规则拦截」路径（纯本地，无网络）。
func BenchmarkBlockedPath(b *testing.B) {
	h, _ := benchHandler(b)
	req := new(dns.Msg)
	req.SetQuestion("ads.example.net.", dns.TypeA)
	w := benchWriter{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeDNS(w, req)
	}
}

// BenchmarkCacheHitPath 测「缓存命中」路径。
//
// 先把响应塞进缓存，再反复查同一个域名 —— 第二次起命中缓存。
func BenchmarkCacheHitPath(b *testing.B) {
	h, req := benchHandler(b)
	w := benchWriter{}

	cache := h.cache.Load()
	key, ok := CacheKeyFrom("usr_bench", req)
	if !ok {
		b.Fatal("构造缓存键失败")
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	rr, _ := dns.NewRR("www.example.com. 300 IN A 93.184.216.34")
	resp.Answer = []dns.RR{rr}
	if !cache.Put(key, resp, req) {
		b.Fatal("写缓存失败")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeDNS(w, req)
	}
}

// BenchmarkCacheKeyFrom 单独测缓存键构造（含 ToLower + TrimSuffix 分配）。
func BenchmarkCacheKeyFrom(b *testing.B) {
	req := new(dns.Msg)
	req.SetQuestion("WWW.Example.COM.", dns.TypeA)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := CacheKeyFrom("usr_bench", req); !ok {
			b.Fatal("构造失败")
		}
	}
}

// BenchmarkMatchTracker 测追踪器后缀匹配（每条查询都会跑一次）。
func BenchmarkMatchTracker(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 命中与不命中各测一半的形态
		if i%2 == 0 {
			matchTracker("hm.baidu.com")
		} else {
			matchTracker("www.example.com")
		}
	}
}

// BenchmarkAnswerFrom 测应答详情拼接（查询日志开启时每条都跑）。
func BenchmarkAnswerFrom(b *testing.B) {
	m := new(dns.Msg)
	m.SetQuestion("www.example.com.", dns.TypeA)
	for _, ip := range []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"} {
		rr, _ := dns.NewRR("www.example.com. 300 IN A " + ip)
		m.Answer = append(m.Answer, rr)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = answerFrom(m)
	}
}

// BenchmarkMaskIP 测客户端 IP 掩码（每条查询都跑，当前用 fmt.Sprintf）。
func BenchmarkMaskIP(b *testing.B) {
	ip := net.IPv4(117, 136, 110, 163)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = maskIP(ip)
	}
}
