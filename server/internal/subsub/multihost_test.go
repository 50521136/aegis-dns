package subsub

import "testing"

// hosts 一行多域名：`0.0.0.0 a.com b.com` 必须三个域名都入库。
//
// 旧实现只取 fields[1]，后面的域名被静默丢弃 —— 用户那份订阅里
// 有 315 行是这种形态。
func TestParseHostsLineWithMultipleDomains(t *testing.T) {
	content := []byte("0.0.0.0 ads1.example ads2.example ads3.example\n")
	p, err := Parse(content, FormatAuto, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.Entries) != 3 {
		t.Fatalf("一行三个域名应产生 3 条，实际 %d 条: %+v", len(p.Entries), p.Entries)
	}
	for _, e := range p.Entries {
		if e.Kind != "block" {
			t.Fatalf("%s 应为 block，实际 %s", e.Domain, e.Kind)
		}
	}
}

// 行内注释不能把后面的内容当域名。
func TestParseHostsLineInlineComment(t *testing.T) {
	content := []byte("127.0.0.1 mtalk.google.com  #台湾\n")
	p, err := Parse(content, FormatAuto, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("行内注释应只留一个域名，实际 %d 条: %+v", len(p.Entries), p.Entries)
	}
	if p.Entries[0].Domain != "mtalk.google.com" {
		t.Fatalf("域名不对: %q", p.Entries[0].Domain)
	}
}

// 混合格式：hosts 与 adblock 混排时两种写法都必须生效。
//
// 这是用户报障的根因：文件前 50 行全是 hosts，被嗅探判成 hosts 后，
// 排他筛选把全文 56% 的 `||domain^` 全丢了（32448 行只剩 6171 条）。
func TestParseMixedFormatKeepsBothStyles(t *testing.T) {
	content := []byte(
		"0.0.0.0 hosts-style.example\n" +
			"||adblock-style.example^\n" +
			"@@||allowed.example^\n" +
			"address=/dnsmasq-style.example/0.0.0.0\n" +
			"bare-domain.example\n")
	p, err := Parse(content, FormatAuto, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := map[string]string{}
	for _, e := range p.Entries {
		got[e.Domain] = e.Kind
	}
	want := map[string]string{
		"hosts-style.example":   "block",
		"adblock-style.example": "block",
		"allowed.example":       "allow",
		"dnsmasq-style.example": "block",
		"bare-domain.example":   "block",
	}
	for d, k := range want {
		if got[d] != k {
			t.Errorf("域名 %s 应为 %s，实际 %q（全量: %v）", d, k, got[d], got)
		}
	}
}

// 显式指定 hosts 格式时也不能丢掉 adblock 行 —— 混合列表很常见，
// 格式只是「主导格式」，不能当排他条件。
func TestParseExplicitHostsFormatKeepsAdblockLines(t *testing.T) {
	content := []byte("0.0.0.0 a.example\n||b.example^\n")
	p, err := Parse(content, FormatHosts, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("显式 hosts 格式下 adblock 行也不该丢，实际 %d 条: %+v", len(p.Entries), p.Entries)
	}
}

// TestParseSniffedHostsButAdblockLater 精确复刻用户报障的那份订阅：
// 前 50 行全是 hosts 写法（嗅探必然判成 hosts），adblock 行在之后。
//
// 旧实现按嗅探结果做排他筛选，于是 adblock 行整片丢弃 ——
// 32448 行的源最终只剩 6171 条，状态还显示 ok。
func TestParseSniffedHostsButAdblockLater(t *testing.T) {
	var b []byte
	// 60 行 hosts（超过嗅探窗口 50 行，确保判成 hosts）
	for i := 0; i < 60; i++ {
		b = append(b, []byte("0.0.0.0 hosts"+itoa(i)+".example\n")...)
	}
	// 之后 40 行 adblock
	for i := 0; i < 40; i++ {
		b = append(b, []byte("||adblock"+itoa(i)+".example^\n")...)
	}

	p, err := Parse(b, FormatAuto, "blocklist")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Detected != FormatHosts {
		t.Fatalf("前置条件：应嗅探为 hosts，实际 %q", p.Detected)
	}
	if len(p.Entries) != 100 {
		t.Fatalf("hosts 60 + adblock 40 应全部入库 = 100 条，实际 %d 条（adblock 行被丢了？）", len(p.Entries))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}
