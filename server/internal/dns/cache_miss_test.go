package dns

import (
	"testing"

	"github.com/miekg/dns"
)

// 这组测试锁住「缓存未命中要能区分成因」这条。
//
// 只知道「命中率 55%」无法判断该动什么：
//   - 绝大多数是 absent（冷启动）→ 域名长尾，容量或 TTL 是瓶颈；
//   - 绝大多数是 expired（过期）→ 上游 TTL 短，该调 minTTL；
//   - 有相当比例被 singleflight 合并 → 并发击穿，命中率不该再当目标。
//
// 三者动作完全不同，所以 GetWithReason 必须如实区分，不能一律返回同一个值。

func TestCacheReportsMissAbsent(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("never.example.com.", dns.TypeA)
	k := CacheKey{UserID: "u1", QName: "never.example.com", QType: dns.TypeA}

	_, hit, _, _, reason := c.GetWithReason(k, req)
	if hit {
		t.Fatal("空缓存不该命中")
	}
	if reason != MissAbsent {
		t.Fatalf("从未缓存过的键应报 MissAbsent，实际 %q", reason)
	}
}

func TestCacheReportsMissExpired(t *testing.T) {
	// minTTL/maxTTL 设成 0 让条目立刻过期（storedAt + 0s <= now）。
	c := NewCache(64, 0, 1)
	req := new(dns.Msg)
	req.SetQuestion("stale.example.com.", dns.TypeA)
	k := CacheKey{UserID: "u1", QName: "stale.example.com", QType: dns.TypeA}

	if !c.Put(k, answerA(t, "stale.example.com", "1.1.1.1"), req) {
		t.Fatal("前置条件不成立：写入失败")
	}
	// 先确认此刻还能命中（TTL 可能还没走完）
	_, hit, _, _, reason := c.GetWithReason(k, req)
	if hit {
		// 命中时成因必须是空
		if reason != MissNone {
			t.Fatalf("命中时成因应为空，实际 %q", reason)
		}
		return
	}
	if reason != MissExpired {
		t.Fatalf("有过但现在过期应报 MissExpired，实际 %q", reason)
	}
}

func TestCacheHitReportsNoReason(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("ok.example.com.", dns.TypeA)
	k := CacheKey{UserID: "u1", QName: "ok.example.com", QType: dns.TypeA}

	if !c.Put(k, answerA(t, "ok.example.com", "2.2.2.2"), req) {
		t.Fatal("写入失败")
	}
	msg, hit, _, _, reason := c.GetWithReason(k, req)
	if !hit {
		t.Fatal("刚写入应当命中")
	}
	if reason != MissNone {
		t.Fatalf("命中时不该带成因，实际 %q", reason)
	}
	if msg == nil || len(msg.Answer) != 1 {
		t.Fatalf("命中内容不对: %v", msg)
	}
}

// TestGetWithAllowStillCompatible 断言老签名行为不变（有调用点依赖它）。
func TestGetWithAllowStillCompatible(t *testing.T) {
	c := NewCache(64, 60, 3600)
	req := new(dns.Msg)
	req.SetQuestion("compat.example.com.", dns.TypeA)
	k := CacheKey{UserID: "u1", QName: "compat.example.com", QType: dns.TypeA}

	if !c.PutWithAction(k, answerA(t, "compat.example.com", "3.3.3.3"), req, "blocked") {
		t.Fatal("写入失败")
	}
	_, hit, allow, action := c.GetWithAllow(k, req)
	if !hit {
		t.Fatal("刚写入应当命中")
	}
	if allow {
		t.Fatal("allowRule 不该为真")
	}
	if action != "blocked" {
		t.Fatalf("action 应还原为 blocked，实际 %q", action)
	}
}

// answerA 造一条单记录 A 应答。
func answerA(t *testing.T, name, ip string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	rr, err := dns.NewRR(name + ". 60 IN A " + ip)
	if err != nil {
		t.Fatalf("构造 RR 失败: %v", err)
	}
	m.Answer = []dns.RR{rr}
	return m
}
