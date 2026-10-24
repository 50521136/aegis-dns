package api

import (
	"strings"
	"testing"
)

// 这个文件锁住「下发给客户端的地址必须跟着配置端口走」这条。
//
// 真实故障：listen_doh 改成非 443 后 dnsd 在新端口正常服务，
// 但面板还在发 https://host/dns-query（隐含 443），用户照填连不上，
// 而服务端日志只有握手失败 —— 排查方向会整个偏到证书或网络。
// 所以端口处理必须有断言，不能只靠「看起来对」。

var allProtos = protoSet{DoH: true, DoT: true, Plain: true}

// TestDoHURLStandardPort 断言 443 时 URL 不带端口。
//
// 443 是 DoH 的默认端口，带上 :443 虽然能用，但地址更啰嗦，
// 而且部分客户端对带端口的 URL 处理不一致。
func TestDoHURLStandardPort(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, allProtos)
	if cfg.DoHURL != "https://abc123.example.com/dns-query" {
		t.Fatalf("443 时应省略端口，实际 %q", cfg.DoHURL)
	}
	if strings.Contains(cfg.DoHURL, ":443") {
		t.Fatal("443 不该显式出现")
	}
}

// TestDoHURLNonStandardPort 断言非 443 时 URL 必须带端口。
//
// 这是核心用例：不带端口客户端会回落到 443，而 dnsd 在别的端口上，
// 结果就是「服务正常但用户连不上」。
func TestDoHURLNonStandardPort(t *testing.T) {
	cases := []struct {
		port int
		want string
	}{
		{8443, "https://abc123.example.com:8443/dns-query"},
		{4433, "https://abc123.example.com:4433/dns-query"},
		{80, "https://abc123.example.com:80/dns-query"},
	}
	for _, c := range cases {
		cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: c.port, DoT: 853}, true, allProtos)
		if cfg.DoHURL != c.want {
			t.Errorf("端口 %d：期望 %q，实际 %q", c.port, c.want, cfg.DoHURL)
		}
	}
}

// TestDoHURLPathForm 断言无通配符时的路径式地址也带端口。
//
// 两个分支（子域名式 / 路径式）都要覆盖：只改一个分支的话，
// 用户一旦用上路径式地址，端口又会丢。
func TestDoHURLPathForm(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 8443, DoT: 853}, false, allProtos)
	want := "https://example.com:8443/dns-query/abc123"
	if cfg.DoHURLPath != want {
		t.Fatalf("路径式地址应带端口，期望 %q，实际 %q", want, cfg.DoHURLPath)
	}
	// 无通配符时主地址走路径式
	if cfg.DoHURL != want {
		t.Fatalf("无通配符时主地址应是路径式，实际 %q", cfg.DoHURL)
	}
}

// TestDoTPortFollowsConfig 断言 DoT 端口跟着 listen_dot 走。
func TestDoTPortFollowsConfig(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 8853}, true, allProtos)
	if cfg.DoTPort != 8853 {
		t.Fatalf("DoT 端口应跟随配置 8853，实际 %d", cfg.DoTPort)
	}
	if cfg.DoTAddr != "abc123.example.com:8853" {
		t.Fatalf("DoT 地址不对：%q", cfg.DoTAddr)
	}
}

// TestPortsZeroFallBack 断言端口未配置时回落到标准端口。
//
// 老配置里可能没有 listen_doh 这一项，不能因此生成 :0 这种地址。
func TestPortsZeroFallBack(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{}, true, allProtos)
	if strings.Contains(cfg.DoHURL, ":0") {
		t.Fatalf("端口 0 不能出现在地址里：%q", cfg.DoHURL)
	}
	if cfg.DoTPort != 853 {
		t.Fatalf("DoT 端口 0 应回落到 853，实际 %d", cfg.DoTPort)
	}
}

// TestDisabledProtoNoAddr 断言关掉的协议不下发地址。
//
// 下发了地址用户就会照着填，填了连不上比看不到更糟。
func TestDisabledProtoNoAddr(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 8443, DoT: 853},
		true, protoSet{DoH: false, DoT: true, Plain: false})
	if cfg.DoHURL != "" || cfg.DoHURLPath != "" {
		t.Fatalf("DoH 关闭时不该下发地址，实际 %q / %q", cfg.DoHURL, cfg.DoHURLPath)
	}
	if cfg.DoTHost == "" {
		t.Fatal("DoT 开启时应有地址")
	}
}

// TestPlatformGuidesCarryPort 断言平台指引里的地址也带端口。
//
// 指引是用户唯一会照着做的地方：它给的地址错了，
// 前面算得再对也没用。
func TestPlatformGuidesCarryPort(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 8443, DoT: 8853}, true, allProtos)
	joined := ""
	for _, g := range cfg.PlatformGuides {
		joined += strings.Join(g.Steps, "\n")
	}
	if !strings.Contains(joined, ":8443") {
		t.Error("平台指引里应出现 DoH 端口 8443")
	}
	// DoT 换到非 853 时，Android 私人 DNS 用不了，文案必须说出来，
	// 而不是让用户照着填一个连不上的主机名。
	if !strings.Contains(joined, "8853") {
		t.Error("DoT 非标准端口时指引应提到实际端口 8853")
	}
	if !strings.Contains(joined, "无法连接") {
		t.Error("DoT 离开 853 时必须提示 Android 连不上")
	}
}

// TestAndroidGuideStandardPort 断言 DoT 保持 853 时提示正常、不出现警告。
func TestAndroidGuideStandardPort(t *testing.T) {
	cfg := buildDNSConfig("example.com", "abc123", "", dnsPorts{DoH: 443, DoT: 853}, true, allProtos)
	for _, g := range cfg.PlatformGuides {
		if g.Platform != "android" {
			continue
		}
		joined := strings.Join(g.Steps, "\n")
		if strings.Contains(joined, "无法连接") {
			t.Fatalf("853 时不该出现警告，实际：%s", joined)
		}
		if !strings.Contains(joined, "853") {
			t.Fatal("应说明 Android 用 853")
		}
		return
	}
	t.Fatal("没找到 android 指引")
}
