package rules

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// TestSplitPatternAcceptsHostsLines 验证规则框能接受 hosts 写法。
//
// 用户从 hosts 文件抄一行（`127.0.0.1 ads.example.com`）填进规则框是最常见的
// 动作，旧实现直接拿整串去 normalizeDomain，报「含非法字符 " "」——
// 看起来像「这个格式不支持」。
func TestSplitPatternAcceptsHostsLines(t *testing.T) {
	ok := map[string]string{
		"127.0.0.1 baidu.com":           "baidu.com",
		"0.0.0.0 baidu.com":             "baidu.com",
		"127.0.0.1  baidu.com":          "baidu.com", // 多个空格
		"0.0.0.0 ads.example.com  # 注释": "ads.example.com",
		"::1 ads.example.com":           "ads.example.com",
		"0.0.0.0 ads.example.com":       "ads.example.com",
		"baidu.com":                     "baidu.com", // 纯域名不受影响
	}
	for in, want := range ok {
		got, _, err := splitPattern(in)
		if err != nil {
			t.Errorf("splitPattern(%q) 应通过，实际报错 %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("splitPattern(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 真实 IP 不是黑洞地址：语义是「指到某个地址」，不能静默当成拦截 ——
	// 那会把一个加速条目变成封锁（线上就是这么把 mtalk.google.com 拦死的）。
	if _, _, err := splitPattern("142.250.157.188 mtalk.google.com"); err == nil {
		t.Error("真实 IP 的 hosts 行应报错，不能当成拦截规则")
	}
	// 一行多个域名：规则框只承载一条，必须明确让他改用导入。
	if _, _, err := splitPattern("0.0.0.0 a.com b.com"); err == nil {
		t.Error("一行多域名应报错并提示用导入")
	}
}

// TestHostsLineWithHostsEntryBuildsRule 验证 hosts 写法真的能构建出规则并生效。
func TestHostsLineWithHostsEntryBuildsRule(t *testing.T) {
	rs, err := Build(Layers{UserRules: []model.Rule{
		{Kind: model.KindBlock, Pattern: "127.0.0.1 baidu-block-probe.example", Enabled: true},
		{Kind: model.KindBlock, Pattern: "0.0.0.0 ads-probe.example", Enabled: true},
	}})
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	for _, h := range []string{"baidu-block-probe.example", "ads-probe.example"} {
		if d := rs.Match(h, dns.TypeA); d.Action != ActionBlock {
			t.Errorf("%q 应被拦截，实际 %v", h, d.Action)
		}
	}
}

// TestHostsLineWildcardInSubscription 验证 hosts 行里的通配不被丢掉。
//
// `0.0.0.0 log*.aliyuncs.com`、`0.0.0.0 *.bjmantis.net` 这类写法在公共
// hosts 列表里存在。旧实现一律走 normalizeDomain，含 * 的必然报错然后被
// `continue` 掉 —— 整行静默消失（实测一份源 4 条）。
func TestHostsLineWildcardInSubscription(t *testing.T) {
	cases := []string{
		"0.0.0.0 log*.aliyuncs.com",
		"0.0.0.0 *.bjmantis.net",
		"0.0.0.0 report.*.qq.com",
	}
	for _, line := range cases {
		pl, err := ParseLineAllForSubscription(line)
		if err != nil {
			t.Errorf("%q 应解析出条目，实际报错 %v", line, err)
			continue
		}
		if len(pl) != 1 || !pl[0].Glob {
			t.Errorf("%q 应解析出一条通配条目，实际 %+v", line, pl)
		}
	}

	// 用户手写规则走 trie，不支持中间通配 —— 必须报错，不能假装支持。
	// （这里返回的是「hosts 行里没有可用域名」而不是 ErrSkip：含 * 的域名
	// normalizeDomain 必然失败，然后被当成整行无可用域名。两者都是明确失败，
	// 不会被静默当成拦截。）
	if _, err := ParseLineAll("0.0.0.0 log*.aliyuncs.com"); err == nil {
		t.Error("非订阅入口应报错（trie 不支持中间通配）")
	}
}
