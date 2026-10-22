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

// 这个文件锁住一条真实的性能缺陷：拦截/改写结果不进缓存。
//
// 背景：被拦的域名恰恰是重复率最高的一类（遥测、广告、统计端点会被客户端
// 按秒重试）。线上实测 aeventlog.beacon.qq.com 一天 3277 次、全部命中拦截
// 规则，平均 26 秒一次；缓存 TTL 是 60 秒，本该全部命中缓存，实际每一条都
// 重新走「识别用户 → 几十万条后缀规则匹配 → 构造拦截响应」。
//
// 后果不只是慢：缓存命中率被这类流量拉到个位数（线上 7.7%），
// 「缓存命中率」这个指标因此完全失去诊断价值 —— 看起来像缓存坏了，
// 其实是 24% 的流量根本没机会进缓存。
//
// 修法是让 ActionBlock / ActionRewrite 分支也写入缓存。这里断言的是
// 最直接的事实：一次被拦截的查询之后，缓存里必须有条目。

// fakeWriter 是 ServeDNS 需要的最小 ResponseWriter。
type fakeWriter struct {
	remote net.Addr
	msgs   []*dns.Msg
}

func (f *fakeWriter) LocalAddr() net.Addr  { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53} }
func (f *fakeWriter) RemoteAddr() net.Addr { return f.remote }
func (f *fakeWriter) WriteMsg(m *dns.Msg) error {
	f.msgs = append(f.msgs, m.Copy())
	return nil
}
func (f *fakeWriter) Write(b []byte) (int, error) { return len(b), nil }
func (f *fakeWriter) Close() error                { return nil }
func (f *fakeWriter) TsigStatus() error           { return nil }
func (f *fakeWriter) TsigTimersOnly(bool)         {}
func (f *fakeWriter) Hijack()                     {}

// newBlockTestHandler 构造一个单租户 handler：来源 IP 127.0.0.1 归属该租户，
// 租户规则里拦掉 blocked.example。
func newBlockTestHandler(t *testing.T) *Handler {
	t.Helper()
	snap := &model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight:  300,
			QueryTimeout: 10000,
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"1.1.1.1:53"},
			CacheSize:    64,
			CacheMinTTL:  60,
			CacheMaxTTL:  3600,
		},
		Users: []model.UserSnap{
			{
				UserID:   "usr_blk",
				ClientID: "aaaaaaaaaa",
				Enabled:  true,
				IPs:      []string{"127.0.0.1/32"},
				SubBlock: []string{"blocked.example"},
			},
		},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		t.Fatalf("构建 registry 失败: %v", err)
	}
	h := NewHandler(reg, NewCache(64, 60, 3600), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rs := reg.Load(); rs != nil {
		h.ApplyDefaults(rs.Defaults())
	}
	return h
}

// TestBlockResponseIsCached 断言被拦截的查询结果会写入缓存。
//
// 修之前这里必然是 0：ActionBlock 分支在写缓存之前就 return 了。
func TestBlockResponseIsCached(t *testing.T) {
	h := newBlockTestHandler(t)
	cache := h.cache.Load()
	if cache == nil {
		t.Fatal("缓存未初始化")
	}

	req := new(dns.Msg)
	req.SetQuestion("ad.blocked.example.", dns.TypeA)
	w := &fakeWriter{remote: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}}

	h.ServeDNS(w, req)

	if len(w.msgs) == 0 {
		t.Fatal("拦截查询没有产生应答")
	}
	if got := w.msgs[0].Rcode; got != dns.RcodeSuccess {
		t.Fatalf("NullIP 模式的拦截应答应是 NOERROR，实际 rcode=%d", got)
	}
	if n := cache.Len(); n != 1 {
		t.Fatalf("被拦截的结果必须进缓存，期望 1 条，实际 %d 条", n)
	}

	// 第二次同样的查询必须命中缓存（命中会重置报文 ID，这里只校验
	// 缓存条目数不再增长，且应答仍是拦截结果）。
	w2 := &fakeWriter{remote: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}}
	h.ServeDNS(w2, req)
	if len(w2.msgs) == 0 {
		t.Fatal("第二次查询没有应答")
	}
	if n := cache.Len(); n != 1 {
		t.Fatalf("同一域名重复查询不该新增缓存条目，实际 %d 条", n)
	}
}

// TestBlockCacheHitKeepsBlockedStatus 断言命中缓存时状态仍是 blocked。
//
// 这是「拦截结果进缓存」的必要配套：缓存命中分支原本只会给出
// allowed / handled 两种状态，如果不把 action 一起存下来，
// 被拦的查询第二次开始就被记成「已处理」，拦截率会从 24% 掉到个位数。
func TestBlockCacheHitKeepsBlockedStatus(t *testing.T) {
	h := newBlockTestHandler(t)
	col := stats.New(64, true)
	h.collector = col

	req := new(dns.Msg)
	req.SetQuestion("ad.blocked.example.", dns.TypeA)

	for i := 0; i < 3; i++ {
		w := &fakeWriter{remote: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}}
		h.ServeDNS(w, req)
		if len(w.msgs) == 0 {
			t.Fatalf("第 %d 次查询没有应答", i+1)
		}
	}

	// 三次查询（首次走规则、后两次走缓存）都必须算作 blocked。
	snap := col.Counters()
	if snap.Total != 3 {
		t.Fatalf("总计数期望 3，实际 %d", snap.Total)
	}
	if snap.Blocked != 3 {
		t.Fatalf("三次拦截查询必须都记成 blocked，实际 blocked=%d（缓存命中丢了状态）", snap.Blocked)
	}
}

// TestBlockCacheKeyIsPerTenant 确保拦截缓存不会跨租户串味。
//
// 缓存键含 UserID，所以租户 A 的拦截结果不会被租户 B 拿去用 ——
// 这条是防止「A 拦了某域名，B 跟着被拦」的回归保护。
func TestBlockCacheKeyIsPerTenant(t *testing.T) {
	k1, ok1 := CacheKeyFrom("usr_a", mustMsg(t, "ad.example."))
	k2, ok2 := CacheKeyFrom("usr_b", mustMsg(t, "ad.example."))
	if !ok1 || !ok2 {
		t.Fatal("构造缓存键失败")
	}
	if k1 == k2 {
		t.Fatal("不同租户的拦截缓存键必须不同，否则会跨租户泄漏拦截结果")
	}
}

func mustMsg(t *testing.T, name string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeA)
	return m
}
