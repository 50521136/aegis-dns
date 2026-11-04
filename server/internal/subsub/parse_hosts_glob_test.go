package subsub

import "testing"

// TestParseIgnoresRealIPHostsLines 验证 hosts 里指向真实 IP 的行不再降级成拦截。
//
// 线上实测事故：一份源的 hosts 段有 147 行是 `142.250.157.188 mtalk.google.com`
// 这种「指定优选 IP」的写法，旧实现把它当改写、又因为订阅层只承载拦/放而
// 降级成 block —— 于是 mtalk.google.com（Google FCM 推送）、dl.google.com、
// media.githubusercontent.com 全被解析成 0.0.0.0。
// AdGuard Home 对这类行做的是 DNS 改写，不是拦截；订阅层承载不了改写，
// 正确做法是忽略这条，而不是反过来把它封掉。
func TestParseIgnoresRealIPHostsLines(t *testing.T) {
	content := "0.0.0.0 ads.example.com\n" +
		"142.250.157.188 mtalk.google.com\n" +
		"180.163.151.161 dl.google.com\n" +
		"20.200.245.247 github.com\n"

	res, err := Parse([]byte(content), FormatHosts, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	got := map[string]string{}
	for _, e := range res.Entries {
		got[e.Domain] = e.Kind
	}
	if got["ads.example.com"] != "block" {
		t.Errorf("0.0.0.0 写法应拦截，实际 %q", got["ads.example.com"])
	}
	for _, d := range []string{"mtalk.google.com", "dl.google.com", "github.com"} {
		if k, ok := got[d]; ok {
			t.Errorf("%q 指向真实 IP，应被忽略，却入库为 %q", d, k)
		}
	}
	if res.RewriteIgnored != 3 {
		t.Errorf("忽略计数应为 3，实际 %d", res.RewriteIgnored)
	}
	// 这类行不该算进降级计数（降级代表「真的变成拦截了」）。
	if res.RewriteDowngraded != 0 {
		t.Errorf("真实 IP 行不该计入降级，实际 %d", res.RewriteDowngraded)
	}
}

// TestParseKeepsBlackholeRewrites 确认黑洞改写仍然降级成拦截。
//
// 修 hosts 真实 IP 时不能把「真的想拦」的写法一起丢掉。
func TestParseKeepsBlackholeRewrites(t *testing.T) {
	content := "||a.example.com^$dnsrewrite=0.0.0.0\n" +
		"||b.example.com^$dnsrewrite=127.0.0.1\n" +
		"||c.example.com^$dnsrewrite=::\n" +
		"||d.example.com^$dnsrewrite=NXDOMAIN\n" +
		"||e.example.com^$dnsrewrite=ad-block.dns.adguard.com\n"

	res, err := Parse([]byte(content), FormatAdblock, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(res.Entries) != 5 {
		t.Fatalf("5 条黑洞改写都应保留，实际 %d 条", len(res.Entries))
	}
	for _, e := range res.Entries {
		if e.Kind != "block" {
			t.Errorf("%q 应为 block，实际 %q", e.Domain, e.Kind)
		}
	}
	if res.RewriteDowngraded != 5 {
		t.Errorf("降级计数应为 5，实际 %d", res.RewriteDowngraded)
	}
	if res.RewriteIgnored != 0 {
		t.Errorf("不该有被忽略的条目，实际 %d", res.RewriteIgnored)
	}
}

// TestParseKeepsWildcardPatterns 验证订阅里的通配模式不再被丢弃。
//
// 旧实现：rules.ParseLineAll 对含 * 的行返回 ErrSkip，实测一份 33554 行的
// 源里有 258 行因此静默失效（at1.doubanio.com 照样解析出去）。
func TestParseKeepsWildcardPatterns(t *testing.T) {
	content := "||at*.doubanio.com^\n" +
		"||163487*.qmgmw.com^\n" +
		"||js.*2023*.live^\n" +
		"||plain.example.com^\n"

	res, err := Parse([]byte(content), FormatAdblock, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := map[string]bool{}
	for _, e := range res.Entries {
		got[e.Domain] = true
	}
	for _, want := range []string{
		"at*.doubanio.com",
		"163487*.qmgmw.com",
		"js.*2023*.live",
		"plain.example.com",
	} {
		if !got[want] {
			t.Errorf("通配/普通条目 %q 应保留，实际入库 %v", want, got)
		}
	}
}

// TestParseRejectsDangerousWildcard 验证会把匹配范围放大到整个后缀的模式被拒绝。
//
// `*`、`com*`、`*com` 这类没有点的写法一旦放行，等于把某个后缀下的全部域名
// 一次拦掉，必须挡在解析阶段。而 `at*.com` 是列表作者的真实意图（拦住所有
// 以 at 开头的 .com），保留。
func TestParseRejectsDangerousWildcard(t *testing.T) {
	content := "||*^\n" +
		"||com*^\n" +
		"||*com^\n" +
		"||safe.example.com^\n" +
		"||at*.com^\n"

	res, err := Parse([]byte(content), FormatAdblock, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := map[string]bool{}
	for _, e := range res.Entries {
		got[e.Domain] = true
	}
	if len(got) != 2 || !got["safe.example.com"] || !got["at*.com"] {
		t.Fatalf("应只保留 safe.example.com 与 at*.com，实际 %+v", res.Entries)
	}
}

// TestParseKeepsUnderscoreDomains 验证标签中间带下划线的域名能入库。
//
// 公共 hosts 列表里大量出现 `upload_data.qq.com`、`aphone_pv_log.kltb.com.cn`、
// `1980_s.gw.irs03.com` 这类真实域名（虽然不符合 RFC 主机名规范，但确实在解析）。
// 旧实现把它们当 CSS 片段一律丢掉 —— 实测一份源 22 条因此整行消失。
// 但以 `_` 开头的标签是服务记录 / CSS 片段（`_dmarc.qq.com`、`_300x250.js`），
// 继续拒绝。
func TestParseKeepsUnderscoreDomains(t *testing.T) {
	content := "127.0.0.1 upload_data.qq.com\n" +
		"127.0.0.1 aphone_pv_log.kltb.com.cn\n" +
		"0.0.0.0 1980_s.gw.irs03.com\n" +
		"127.0.0.1 _dmarc.qq.com\n" +
		"0.0.0.0 _300x250.js\n"

	res, err := Parse([]byte(content), FormatHosts, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := map[string]bool{}
	for _, e := range res.Entries {
		got[e.Domain] = true
	}
	for _, want := range []string{
		"upload_data.qq.com",
		"aphone_pv_log.kltb.com.cn",
		"1980_s.gw.irs03.com",
	} {
		if !got[want] {
			t.Errorf("%q 应入库（下划线在标签中间是真域名）", want)
		}
	}
	for _, bad := range []string{"_dmarc.qq.com", "_300x250.js"} {
		if got[bad] {
			t.Errorf("%q 不应入库（以 _ 开头是服务记录/CSS 片段）", bad)
		}
	}
}
