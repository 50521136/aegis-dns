package dns

import (
	"testing"

	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/splitrule"
)

// TestUseOverseas 钉住三层决策语义。
//
// 这三条是用户明确拍板的规则，改动前先想清楚：
//
//	白名单命中        → 国内
//	否则黑名单命中    → 境外
//	都没命中          → 国内（安全默认）
func TestUseOverseas(t *testing.T) {
	snap := &registrySnapshot{
		DomesticSet: splitrule.NewSet([]string{
			"qq.com", "baidu.com", "weixin.com",
		}, false),
		GfwSet: splitrule.NewSet([]string{
			"google.com", "twitter.com", "telegram.org",
		}, false),
	}
	// 用非 nil 的指针表示「配了境外上游」。测试里不实际查询，
	// 只需要它不为 nil 就能走进分流判断。
	overseas := &resolver.Pool{}

	cases := []struct {
		name  string
		qname string
		want  bool
	}{
		// 第 1 层：白名单命中 → 国内
		{"白名单精确命中", "qq.com", false},
		{"白名单子域名命中", "www.qq.com", false},
		{"白名单多级子域名", "a.b.c.qq.com", false},

		// 第 2 层：白名单未命中 + 黑名单命中 → 境外
		{"黑名单精确命中", "google.com", true},
		{"黑名单子域名命中", "www.google.com", true},
		{"黑名单多级子域名", "api.v2.google.com", true},

		// 第 3 层：都没命中 → 国内
		// qq.com.cn 是真实踩过的坑：dnsmasq-china-list 里没有它，
		// 单表白名单模式下 *.qq.com.cn 全被当成境外域名绕道境外。
		{"两边都未命中", "qq.com.cn", false},
		{"两边都未命中的子域名", "dns.weixin.qq.com.cn", false},
		{"完全未知域名", "some-new-site.example", false},

		// 白名单优先于黑名单
		// 两个列表偶尔会重叠（某国内 CDN 被误收进 gfw），
		// 这时按国内处理更安全 —— 走境外会拿到非最优节点。
		{"白名单优先", "qq.com", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := useOverseas(overseas, snap, c.qname)
			if got != c.want {
				t.Fatalf("useOverseas(%q) = %v, 期望 %v", c.qname, got, c.want)
			}
		})
	}
}

// TestUseOverseasNoPool 没配境外上游时必须全部走国内。
//
// 这是「留空 = 关闭分流」的保证：管理员只配国内上游时，
// 分流规则表即使还在也不该影响任何查询。
func TestUseOverseasNoPool(t *testing.T) {
	snap := &registrySnapshot{
		DomesticSet: splitrule.NewSet(nil, false),
		GfwSet:      splitrule.NewSet([]string{"google.com"}, false),
	}
	if useOverseas(nil, snap, "www.google.com") {
		t.Fatal("没配境外上游时不该走境外")
	}
}

// TestUseOverseasDomesticWins 白名单与黑名单同时命中时按国内处理。
func TestUseOverseasDomesticWins(t *testing.T) {
	snap := &registrySnapshot{
		DomesticSet: splitrule.NewSet([]string{"overlap.com"}, false),
		GfwSet:      splitrule.NewSet([]string{"overlap.com"}, false),
	}
	if useOverseas(&resolver.Pool{}, snap, "www.overlap.com") {
		t.Fatal("白名单命中时应优先走国内，实际走了境外")
	}
}
