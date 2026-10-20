package dns

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件锁住「规则变更后缓存没有失效」这个缺陷。
//
// 缓存键是 {UserID, QName, QType, DO}，不含规则版本。所以规则改了、键没变，
// 旧应答会一直被命中 —— 新加的拦截规则对该域名在 TTL 内完全不生效
// （cache_max_ttl 默认 86400，最长一天）。
//
// 用户侧的表现是「我在面板上加了拦截规则，查了一下还是通的，过好一阵子才好」，
// 而设计文档承诺的配置生效延迟是 < 2s。
//
// 现场表现很迷惑：同一个租户、同一份快照里，verify-block.test 立刻被拦，
// example.org 却照常解析 —— 区别只在于后者此前被查过、缓存里有旧应答。

func putCache(t *testing.T, c *Cache, userID, name string) CacheKey {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(name+".", dns.TypeA)
	key, ok := CacheKeyFrom(userID, req)
	if !ok {
		t.Fatalf("构造缓存键失败: userID=%q name=%q", userID, name)
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   net.IPv4(1, 2, 3, 4).To4(),
	}}
	if !c.Put(key, resp, req) {
		t.Fatalf("写入缓存失败: %v", key)
	}
	return key
}

func TestFlushUsersOnlyDropsListedTenants(t *testing.T) {
	c := NewCache(64, 60, 3600)
	k1 := putCache(t, c, "usr_t1", "example.org")
	k2 := putCache(t, c, "usr_t2", "example.org")
	kf := putCache(t, c, "", "example.org")

	c.FlushUsers([]string{"usr_t1", ""})

	if _, hit := c.Get(k1); hit {
		t.Error("usr_t1 的缓存应被清掉")
	}
	if _, hit := c.Get(kf); hit {
		t.Error("fallback（UserID 为空）的缓存应被清掉")
	}
	if _, hit := c.Get(k2); !hit {
		t.Error("usr_t2 没改规则，它的缓存不该被牵连清掉（否则规则改动会打光整机命中率）")
	}
}

func TestLoaderInvalidatesCacheByFingerprint(t *testing.T) {
	reg := buildTwoTenantRegistry(t)
	c := NewCache(64, 60, 3600)
	h := NewHandler(reg, c, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l := NewLoader("/nonexistent/config.json", reg, h, slog.New(slog.NewTextHandler(io.Discard, nil)))

	prev := &model.Snapshot{
		Version:           2,
		GlobalFingerprint: "g1",
		Users: []model.UserSnap{
			{UserID: "usr_t1", Fingerprint: "u1"},
			{UserID: "usr_t2", Fingerprint: "u2"},
		},
	}

	// 1) 全量重建但内容没变：缓存必须保留。
	//    这正是线上 24 小时被清 33 次的原因 —— 订阅刷新、配额调整都会触发
	//    全量重建，按 ChangedUsers 判断就会把整机缓存打光。
	k1 := putCache(t, c, "usr_t1", "example.org")
	k2 := putCache(t, c, "usr_t2", "example.org")
	l.invalidateCache(prev, &model.Snapshot{
		Version: 3, GlobalFingerprint: "g1",
		Users: []model.UserSnap{
			{UserID: "usr_t1", Fingerprint: "u1"},
			{UserID: "usr_t2", Fingerprint: "u2"},
		},
	})
	if _, hit := c.Get(k1); !hit {
		t.Error("规则没变的全量重建不该清缓存")
	}
	if _, hit := c.Get(k2); !hit {
		t.Error("规则没变的全量重建不该清缓存")
	}

	// 2) 只有 usr_t1 的指纹变了：只清他 + fallback
	l.invalidateCache(prev, &model.Snapshot{
		Version: 4, GlobalFingerprint: "g1",
		Users: []model.UserSnap{
			{UserID: "usr_t1", Fingerprint: "u1-changed"},
			{UserID: "usr_t2", Fingerprint: "u2"},
		},
	})
	if _, hit := c.Get(k1); hit {
		t.Error("改了规则的 usr_t1 的缓存必须被清掉，否则新规则在 TTL 内不生效")
	}
	if _, hit := c.Get(k2); !hit {
		t.Error("usr_t2 没改规则，它的缓存不该被牵连")
	}

	// 3) 全局指纹变了：所有租户都不可信
	k1b := putCache(t, c, "usr_t1", "example.org")
	k2b := putCache(t, c, "usr_t2", "example.org")
	l.invalidateCache(prev, &model.Snapshot{
		Version: 5, GlobalFingerprint: "g2",
		Users: []model.UserSnap{
			{UserID: "usr_t1", Fingerprint: "u1-changed"},
			{UserID: "usr_t2", Fingerprint: "u2"},
		},
	})
	if _, hit := c.Get(k1b); hit {
		t.Error("全局层变更后 usr_t1 的缓存应清掉")
	}
	if _, hit := c.Get(k2b); hit {
		t.Error("全局层变更后 usr_t2 的缓存也应清掉（拦截模式/上游变了影响所有人）")
	}

	// 4) 用户被删除：他的缓存要清
	k3 := putCache(t, c, "usr_t2", "gone.example")
	l.invalidateCache(prev, &model.Snapshot{
		Version: 6, GlobalFingerprint: "g2",
		Users: []model.UserSnap{{UserID: "usr_t1", Fingerprint: "u1-changed"}},
	})
	if _, hit := c.Get(k3); hit {
		t.Error("被删除用户的缓存应清掉")
	}
}

func TestLoaderSkipsCacheFlushWhenVersionUnchanged(t *testing.T) {
	reg := buildTwoTenantRegistry(t)
	c := NewCache(64, 60, 3600)

	k := putCache(t, c, "usr_t1", "example.org")

	// Registry 当前版本是 1；再用版本 1 重载，Build 会直接跳过。
	if got := reg.Version(); got != 1 {
		t.Fatalf("前置条件：registry 版本应为 1，实际 %d", got)
	}
	verBefore := reg.Version()
	if err := reg.Build(&model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight: 300, QueryTimeout: 10000,
			BlockingMode: model.BlockingNullIP, Upstreams: []string{"1.1.1.1"},
		},
	}); err != nil {
		t.Fatalf("重复 Build 不该报错: %v", err)
	}
	if reg.Version() != verBefore {
		t.Fatal("版本未前进，LoadOnce 不该触发 invalidateCache")
	}
	if _, hit := c.Get(k); !hit {
		t.Error("空转重载不该清缓存")
	}
}
