package api

import (
	"strings"
	"testing"
)

func TestBuildDNSProfile(t *testing.T) {
	out := buildDNSProfile("w6akqqjgi3", profileTarget{
		Protocol:    "HTTPS",
		ServerURL:   "https://w6akqqjgi3.example.com/dns-query",
		Fingerprint: "DC:1E:BA:02",
	})
	for _, want := range []string{
		"com.apple.dnsSettings.managed",
		"<string>HTTPS</string>",
		"https://w6akqqjgi3.example.com/dns-query",
		"DC:1E:BA:02",
		"<string>Configuration</string>",
	} {
		if !contains(out, want) {
			t.Errorf("缺少 %q", want)
		}
	}
	// UUID 必须稳定：同一 client_id 两次生成结果一致
	a := stableUUID("profile:x")
	b := stableUUID("profile:x")
	if a != b {
		t.Errorf("UUID 不稳定: %s vs %s", a, b)
	}
	if len(a) != 36 {
		t.Errorf("UUID 长度不对: %s", a)
	}
	t.Logf("profile UUID=%s", a)
}

// 描述文件绝不能生成地址为空的版本。
//
// 线上真实踩过：管理员关掉 DoH 后，ServerURL 变成空字符串，
// 但文件照样生成 —— 装上之后 iOS 显示「已安装」，解析全挂，
// 用户完全看不出哪里错了。这类「装上了但不可用」的失败最难查。
func TestProfileNeverEmitsEmptyAddress(t *testing.T) {
	// DoH 可用：应该给 HTTPS + ServerURL
	https := profileTarget{Protocol: "HTTPS", ServerURL: "https://x.example.com/dns-query"}
	if !https.Usable() {
		t.Fatal("DoH 目标应判定为可用")
	}
	body := buildDNSProfile("cid1", https)
	if !strings.Contains(body, "<string>HTTPS</string>") {
		t.Error("缺少 DNSProtocol=HTTPS")
	}
	if !strings.Contains(body, "<string>https://x.example.com/dns-query</string>") {
		t.Error("缺少 ServerURL")
	}

	// DoT 可用：应该给 TLS + ServerName
	dot := profileTarget{Protocol: "TLS", ServerName: "x.example.com"}
	if !dot.Usable() {
		t.Fatal("DoT 目标应判定为可用")
	}
	body = buildDNSProfile("cid1", dot)
	if !strings.Contains(body, "<string>TLS</string>") {
		t.Error("缺少 DNSProtocol=TLS")
	}
	if !strings.Contains(body, "<key>ServerName</key>") {
		t.Error("DoT 描述文件必须带 ServerName")
	}
	if !strings.Contains(body, "<string>x.example.com</string>") {
		t.Error("缺少 ServerName 值")
	}
	if strings.Contains(body, "<key>ServerURL</key>") {
		t.Error("TLS 形态不该出现 ServerURL 键")
	}

	// 地址为空：一律判定不可用，handler 会拒绝生成
	if (profileTarget{Protocol: "HTTPS"}).Usable() {
		t.Error("空 ServerURL 不该判定为可用")
	}
	if (profileTarget{Protocol: "TLS"}).Usable() {
		t.Error("空 ServerName 不该判定为可用")
	}
}

// 同一个 client_id 每次生成的描述文件必须一致，
// 否则重复安装会在「VPN 与设备管理」里堆出一串同名描述文件。
func TestProfileUUIDStable(t *testing.T) {
	p := profileTarget{Protocol: "HTTPS", ServerURL: "https://a.example.com/dns-query"}
	a := buildDNSProfile("same-cid", p)
	b := buildDNSProfile("same-cid", p)
	if a != b {
		t.Error("同一 client_id 两次生成的描述文件不一致")
	}
	c := buildDNSProfile("other-cid", p)
	if a == c {
		t.Error("不同 client_id 不该生成相同描述文件")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
