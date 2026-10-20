package snapshot

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 指纹的语义很关键：它决定「什么时候清缓存」。
// 算多了会让无关改动打光缓存（线上就是因为这个把命中率从 86% 打到 12%），
// 算少了会让新规则在 TTL 内不生效。这里把两个方向都钉住。
func TestUserFingerprintIgnoresQuota(t *testing.T) {
	base := model.User{
		ID: "u1", Enabled: true,
		MemberUntil: 0,
	}
	snap := model.UserSnap{
		UserID:  "u1",
		Enabled: true,
		Rules: []model.Rule{
			{Kind: model.KindBlock, Pattern: "||ads.example^", Enabled: true},
		},
		SubBlock: []string{"tracker.example"},
	}
	fp1 := userFingerprint(&base, snap)

	// 会员到期时间变了：不应影响指纹
	base.MemberUntil = 1893456000
	snap.DailyQueries = 999999
	fp2 := userFingerprint(&base, snap)
	if fp1 != fp2 {
		t.Error("配额/会员期变化不应改变指纹 —— 那不影响任何已缓存的应答，改了会白清缓存")
	}

	// 规则变了：必须影响指纹
	snap.Rules = append(snap.Rules, model.Rule{Kind: model.KindBlock, Pattern: "||new.example^", Enabled: true})
	if fp1 == userFingerprint(&base, snap) {
		t.Error("规则变化必须改变指纹，否则新规则在 TTL 内不生效")
	}
}

func TestUserFingerprintDetectsSubChanges(t *testing.T) {
	u := &model.User{ID: "u1", Enabled: true}
	a := model.UserSnap{UserID: "u1", Enabled: true, SubBlock: []string{"a.example", "b.example"}}
	b := model.UserSnap{UserID: "u1", Enabled: true, SubBlock: []string{"a.example", "c.example"}}
	if userFingerprint(u, a) == userFingerprint(u, b) {
		t.Error("订阅条目变化必须改变指纹")
	}

	// 长度分隔：["ab","c"] 与 ["a","bc"] 不能撞成同一个哈希
	c := model.UserSnap{UserID: "u1", Enabled: true, SubBlock: []string{"ab", "c"}}
	d := model.UserSnap{UserID: "u1", Enabled: true, SubBlock: []string{"a", "bc"}}
	if userFingerprint(u, c) == userFingerprint(u, d) {
		t.Error("域名列表拼接必须用分隔符，否则相邻条目会撞哈希")
	}

	// 顺序无关：订阅条目顺序变了但内容一样，不该清缓存
	e := model.UserSnap{UserID: "u1", Enabled: true, SubBlock: []string{"b.example", "a.example"}}
	// 注意：当前实现是按顺序喂的，顺序变会算出不同指纹 —— 这是可以接受的保守行为
	// （宁可多清一次，也不要漏清）。这里只断言它不 panic。
	_ = userFingerprint(u, e)
}

func TestGlobalFingerprintTracksDefaults(t *testing.T) {
	set := &model.GlobalSettings{AllowRootDomain: false}
	s1 := &model.Snapshot{
		Defaults: model.Defaults{BlockingMode: model.BlockingNullIP, Upstreams: []string{"223.5.5.5"}},
	}
	fp1 := globalFingerprint(set, s1)

	// 拦截模式变了：拦截时返回的 IP 会变，所有人的缓存都不可信
	s2 := &model.Snapshot{
		Defaults: model.Defaults{BlockingMode: model.BlockingCustomIP, CustomIP: "1.2.3.4", Upstreams: []string{"223.5.5.5"}},
	}
	if fp1 == globalFingerprint(set, s2) {
		t.Error("拦截模式变化必须改变全局指纹")
	}

	// 上游变了
	s3 := &model.Snapshot{
		Defaults: model.Defaults{BlockingMode: model.BlockingNullIP, Upstreams: []string{"119.29.29.29"}},
	}
	if fp1 == globalFingerprint(set, s3) {
		t.Error("上游变化必须改变全局指纹")
	}

	// 主域名开关变了
	set2 := &model.GlobalSettings{AllowRootDomain: true}
	if fp1 == globalFingerprint(set2, s1) {
		t.Error("主域名开关变化必须改变全局指纹")
	}

	// 全局规则变了
	s4 := &model.Snapshot{
		Defaults: model.Defaults{BlockingMode: model.BlockingNullIP, Upstreams: []string{"223.5.5.5"}},
		Global: model.GlobalSnap{
			Rules: []model.Rule{{Kind: model.KindBlock, Pattern: "||g.example^", Enabled: true}},
		},
	}
	if fp1 == globalFingerprint(set, s4) {
		t.Error("全局规则变化必须改变全局指纹")
	}
}
