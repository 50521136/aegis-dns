package api

import (
	"strings"
	"testing"
)

// 这个文件锁住「主域名被拿去当网站后，不能再把它当 DNS 接入点下发」。
//
// 场景：管理员有通配符证书，子域名形态 <id>.<域名>/dns-query 可用；
// 同时主域名挂了自己的网站。此时路径式地址 <域名>/dns-query/<id>
// 指向的是网站而不是 DNS 服务 —— 发给用户只会让他白试一趟，
// 而报错看起来像「服务坏了」。

// TestRootReservedHidesPathForm 断言开了开关就不下发路径式地址。
func TestRootReservedHidesPathForm(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 9443, DoT: 853}, true, true, allProtos)

	if cfg.DoHURLPath != "" {
		t.Fatalf("主域名已保留给网站时不该下发路径式地址，实际 %q", cfg.DoHURLPath)
	}
	// 子域名形态必须仍然可用，否则用户就没有任何 DoH 地址了
	if cfg.DoHURL != "https://abc123.example.com:9443/dns-query" {
		t.Fatalf("子域名形态不能受影响，实际 %q", cfg.DoHURL)
	}
}

// TestRootReservedKeepsPathFormWhenOnlyOption 断言没有通配符时路径式仍然下发。
//
// 这条是安全阀：证书不通配时路径式是唯一可用形态，
// 如果因为开关就把它藏了，用户会一条地址都拿不到。
func TestRootReservedKeepsPathFormWhenOnlyOption(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 9443, DoT: 853}, false, true, allProtos)

	if cfg.DoHURLPath == "" {
		t.Fatal("无通配符时路径式是唯一可用地址，不能被隐藏")
	}
	if !strings.Contains(cfg.DoHURLPath, "example.com:9443/dns-query/abc123") {
		t.Fatalf("路径式地址不对：%q", cfg.DoHURLPath)
	}
	// 主地址也走路径式
	if cfg.DoHURL != cfg.DoHURLPath {
		t.Fatalf("无通配符时主地址应等于路径式，实际 %q vs %q", cfg.DoHURL, cfg.DoHURLPath)
	}
}

// TestRootReservedOffShowsBoth 断言开关关闭时两种形态都下发（兼容老行为）。
func TestRootReservedOffShowsBoth(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 9443, DoT: 853}, true, false, allProtos)

	if cfg.DoHURL == "" || cfg.DoHURLPath == "" {
		t.Fatalf("开关关闭时两种形态都该下发，实际 doh_url=%q doh_url_path=%q",
			cfg.DoHURL, cfg.DoHURLPath)
	}
	if cfg.DoHURL == cfg.DoHURLPath {
		t.Fatal("通配符可用时两种形态应不同")
	}
}

// TestRootReservedNoClientID 断言没有 client_id 时不生成路径式地址。
//
// clientID 为空时 subHost 退化成裸域名，两个地址会一样；
// 拼出 "/dns-query/" 这种尾斜杠地址没有意义。
func TestRootReservedNoClientID(t *testing.T) {
	cfg := buildDNSConfig("example.com", "", "",
		dnsPorts{DoH: 443, DoT: 853}, true, true, allProtos)

	if strings.HasSuffix(cfg.DoHURLPath, "/dns-query/") {
		t.Fatalf("空 client_id 不该拼出尾斜杠地址：%q", cfg.DoHURLPath)
	}
}

// TestRootReservedGuidesUseSubdomain 断言平台指引不会引导到主域名。
//
// 指引是用户唯一照着做的地方，它若还写着裸域名，
// 前面把地址藏掉的努力等于白做。
func TestRootReservedGuidesUseSubdomain(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "",
		dnsPorts{DoH: 9443, DoT: 853}, true, true, allProtos)
	joined := ""
	for _, g := range cfg.PlatformGuides {
		joined += strings.Join(g.Steps, "\n")
	}
	if strings.Contains(joined, "https://example.com") {
		t.Error("指引里不该出现以裸主域名为入口的地址")
	}
	if !strings.Contains(joined, "abc123.example.com") {
		t.Error("指引里应使用子域名形态")
	}
}
