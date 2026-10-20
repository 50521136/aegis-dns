package api

import "testing"

func TestBuildDNSProfile(t *testing.T) {
	out := buildDNSProfile("w6akqqjgi3", "https://w6akqqjgi3.example.com/dns-query", "DC:1E:BA:02")
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

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
