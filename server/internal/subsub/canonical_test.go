package subsub

import "testing"

// 等价识别的核心用例。每一条都对应线上真实出现过的链接形态。
func TestCanonicalURLEquivalences(t *testing.T) {
	const raw666 = "https://raw.githubusercontent.com/qq5460168/666/master/rules.txt"
	const cdn666 = "https://cdn.jsdelivr.net/gh/qq5460168/666@refs/heads/master/rules.txt"

	if CanonicalURL(raw666) != CanonicalURL(cdn666) {
		t.Fatalf("raw 与 jsDelivr 应等价:\n raw=%s\n cdn=%s",
			CanonicalURL(raw666), CanonicalURL(cdn666))
	}

	// 用户明确给出的例子：加速前缀 + releases/latest/download
	const ghrel = "https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_adguard.txt"
	const ghrelFast = "https://ghfast.top/https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_adguard.txt"
	if CanonicalURL(ghrel) != CanonicalURL(ghrelFast) {
		t.Fatalf("releases 地址与它的加速形态应等价:\n a=%s\n b=%s",
			CanonicalURL(ghrel), CanonicalURL(ghrelFast))
	}

	// 线上实际存在的一对：同一份 217heidai/adblockfilters，一个走 gcore，一个走 github.boki.moe
	const gcore217 = "https://gcore.jsdelivr.net/gh/217heidai/adblockfilters@main/rules/adblockdns.txt"
	const boki217 = "https://github.boki.moe/https://raw.githubusercontent.com/217heidai/adblockfilters/main/rules/adblockdns.txt"
	if CanonicalURL(gcore217) != CanonicalURL(boki217) {
		t.Fatalf("gcore jsDelivr 与 boki 加速应等价:\n a=%s\n b=%s",
			CanonicalURL(gcore217), CanonicalURL(boki217))
	}

	// github.com/<o>/<r>/raw/<ref>/<path> 与 raw.githubusercontent 等价
	const ghcomRaw = "https://github.com/AdAway/adaway.github.io/raw/master/hosts.txt"
	const ghraw = "https://raw.githubusercontent.com/AdAway/adaway.github.io/master/hosts.txt"
	if CanonicalURL(ghcomRaw) != CanonicalURL(ghraw) {
		t.Fatalf("github.com/raw 与 raw.githubusercontent 应等价:\n a=%s\n b=%s",
			CanonicalURL(ghcomRaw), CanonicalURL(ghraw))
	}

	// 多层加速前缀也要能剥干净
	const doubleProxy = "https://gh-proxy.com/https://ghfast.top/https://raw.githubusercontent.com/a/b/main/c.txt"
	if CanonicalURL(doubleProxy) != CanonicalURL("https://raw.githubusercontent.com/a/b/main/c.txt") {
		t.Fatalf("嵌套加速前缀应能剥净: %s", CanonicalURL(doubleProxy))
	}

	// gitcode 镜像
	const gitcode = "https://raw.gitcode.com/rssv/qy-Ads-Rule/raw/main/black.txt"
	const ghqy = "https://raw.githubusercontent.com/rssv/qy-Ads-Rule/main/black.txt"
	if CanonicalURL(gitcode) != CanonicalURL(ghqy) {
		t.Fatalf("gitcode 镜像应等价: a=%s b=%s", CanonicalURL(gitcode), CanonicalURL(ghqy))
	}
}

// 这些**绝不能**被判成等价 —— 判错的代价是把一个人的规则套到另一个人身上。
func TestCanonicalURLMustNotMerge(t *testing.T) {
	cases := []struct{ a, b, why string }{
		{
			a:   "https://raw.githubusercontent.com/x/y/master/a.txt",
			b:   "https://raw.githubusercontent.com/x/y/main/a.txt",
			why: "master 与 main 是不同分支，内容通常不同",
		},
		{
			a:   "https://raw.githubusercontent.com/x/y/master/a.txt",
			b:   "https://raw.githubusercontent.com/x/y/master/b.txt",
			why: "同仓库不同文件",
		},
		{
			a:   "https://raw.githubusercontent.com/x/y/master/a.txt",
			b:   "https://raw.githubusercontent.com/x/z/master/a.txt",
			why: "不同仓库",
		},
		{
			a:   "https://big.oisd.nl/domainswild",
			b:   "https://big.oisd.nl/",
			why: "同域名不同路径，未经证实不能合并",
		},
		{
			a:   "https://example.com/rules.txt",
			b:   "https://example.com/rules.txt?token=abc",
			why: "query 可能决定返回内容（鉴权/分支），不归一",
		},
		{
			a:   "https://example.com/A.txt",
			b:   "https://example.com/a.txt",
			why: "路径大小写在某些服务上有意义",
		},
	}
	for _, c := range cases {
		if CanonicalURL(c.a) == CanonicalURL(c.b) {
			t.Errorf("不应等价（%s）:\n a=%s -> %s\n b=%s -> %s",
				c.why, c.a, CanonicalURL(c.a), c.b, CanonicalURL(c.b))
		}
	}
}

// 前缀剥离必须只对「剥完还是 URL」的情况生效，
// 否则一个真的以镜像域名开头的规则源会被剥坏。
func TestStripMirrorPrefixSafety(t *testing.T) {
	// 不是「前缀 + URL」的形态，不能被剥
	got := CanonicalURL("https://ghfast.top/rules.txt")
	want := "url:https://ghfast.top/rules.txt"
	if got != want {
		t.Fatalf("不应剥离非代理形态: got=%s want=%s", got, want)
	}
}

func TestFindDuplicateGroups(t *testing.T) {
	subs := []*DupInput{
		{ID: "s1", UserID: "u1", URL: "https://raw.githubusercontent.com/qq5460168/666/master/rules.txt", LastCount: 24450},
		{ID: "s2", UserID: "u2", URL: "https://cdn.jsdelivr.net/gh/qq5460168/666@refs/heads/master/rules.txt", LastCount: 24450},
		{ID: "s3", UserID: "u1", URL: "https://www.23-4.cn/rules/merged-block.txt", LastCount: 473431},
		{ID: "s4", UserID: "u2", URL: "https://www.23-4.cn/rules/merged-block.txt", LastCount: 473431},
		{ID: "s5", UserID: "u3", URL: "https://big.oisd.nl/domainswild", LastCount: 233655},
	}
	groups := FindDuplicateGroups(subs)
	if len(groups) != 2 {
		t.Fatalf("应识别出 2 个重复组，实际 %d: %+v", len(groups), groups)
	}
	// 每组两个订阅 → 各多拉 1 次
	for _, g := range groups {
		if g.ExtraFetches != 1 {
			t.Errorf("组 %s ExtraFetches 应为 1，实际 %d", g.CanonicalKey, g.ExtraFetches)
		}
		if len(g.SubIDs) != 2 {
			t.Errorf("组 %s 应含 2 个订阅，实际 %d", g.CanonicalKey, len(g.SubIDs))
		}
	}
	// 唯一源不该出现在结果里
	for _, g := range groups {
		for _, id := range g.SubIDs {
			if id == "s5" {
				t.Errorf("唯一源 s5 不该被判为重复")
			}
		}
	}
}
