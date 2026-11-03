package config

import "testing"

// TestNormalizeFrontendDefaults 确认只写 frontend_upstream 时，白名单自动退到
// domain 裸域名 —— 老配置文件加一行就能用上，不必同时写两处。
func TestNormalizeFrontendDefaults(t *testing.T) {
	c := Default()
	c.Domain = "AdGuardHome.LV10.REN."
	c.DNS.FrontendUpstream = "  http://127.0.0.1:3000  "

	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := c.DNS.FrontendUpstream; got != "http://127.0.0.1:3000" {
		t.Fatalf("FrontendUpstream = %q，期望去掉首尾空格", got)
	}
	if len(c.DNS.FrontendHosts) != 1 || c.DNS.FrontendHosts[0] != "adguardhome.lv10.ren" {
		t.Fatalf("FrontendHosts = %v，期望退到归一后的裸域名", c.DNS.FrontendHosts)
	}
}

// TestNormalizeFrontendHosts 覆盖白名单的归一、去重与顺序保留。
func TestNormalizeFrontendHosts(t *testing.T) {
	c := Default()
	c.Domain = "adguardhome.lv10.ren"
	c.DNS.FrontendUpstream = "http://127.0.0.1:3000"
	c.DNS.FrontendHosts = []string{"  AdGuardHome.lv10.ren. ", "", "adguardhome.lv10.ren", "www.lv10.ren"}

	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"adguardhome.lv10.ren", "www.lv10.ren"}
	if len(c.DNS.FrontendHosts) != len(want) {
		t.Fatalf("FrontendHosts = %v，期望 %v", c.DNS.FrontendHosts, want)
	}
	for i := range want {
		if c.DNS.FrontendHosts[i] != want[i] {
			t.Fatalf("FrontendHosts[%d] = %q，期望 %q", i, c.DNS.FrontendHosts[i], want[i])
		}
	}
}

// TestNormalizeFrontendRejectsBadHost 确认带端口/路径的白名单项直接报错，
// 而不是被静默截断成一个永远匹配不上的残缺字符串。
func TestNormalizeFrontendRejectsBadHost(t *testing.T) {
	for _, bad := range []string{"adguardhome.lv10.ren:443", "adguardhome.lv10.ren/x"} {
		c := Default()
		c.Domain = "adguardhome.lv10.ren"
		c.DNS.FrontendUpstream = "http://127.0.0.1:3000"
		c.DNS.FrontendHosts = []string{bad}
		if err := c.normalize(); err == nil {
			t.Fatalf("frontend_hosts=%q 期望报错，得到 nil", bad)
		}
	}
}

// TestNormalizeFrontendNeedsHost 确认启用反代但既没有白名单也没有 domain 时报错。
func TestNormalizeFrontendNeedsHost(t *testing.T) {
	c := Default()
	c.Domain = ""
	c.DNS.FrontendUpstream = "http://127.0.0.1:3000"
	if err := c.normalize(); err == nil {
		t.Fatal("没有可用 Host 时期望报错，得到 nil")
	}
}

// TestNormalizeFrontendDisabledByDefault 确认默认配置不启用反代。
func TestNormalizeFrontendDisabledByDefault(t *testing.T) {
	c := Default()
	c.Domain = "adguardhome.lv10.ren"
	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.DNS.FrontendUpstream != "" {
		t.Fatalf("默认配置不应启用反代，得到 %q", c.DNS.FrontendUpstream)
	}
	// 白名单即使没启用也会退到 domain，这不影响行为（未启用时根本不用它），
	// 但能让自检脚本读到「配了就会生效的域名」。
	if len(c.DNS.FrontendHosts) != 1 || c.DNS.FrontendHosts[0] != "adguardhome.lv10.ren" {
		t.Fatalf("FrontendHosts = %v，期望退到 domain", c.DNS.FrontendHosts)
	}
}
