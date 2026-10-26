package dns

import "testing"

// TestTrackerNewEntries 锁住 2026-10-26 补充的中国厂商条目。
// 这些是生产日志里真实出现过、但旧表没有的（表外流量曾占 73%）。
func TestTrackerNewEntries(t *testing.T) {
	cases := []struct {
		domain, wantName string
	}{
		{"applog-perf.uc.cn", "UC 上报"},
		{"onrt-stsdk.vivo.com.cn", "vivo SDK 上报"},
		{"vcode-od.vivo.com.cn", "vivo 校验上报"},
		{"obus-cn.dc.heytapmobi.com", "OPPO 数据采集"},
		{"metok.sys.miui.com", "MIUI 统计"},
		{"h.trace.qq.com", "腾讯追踪"},
		{"log.zijieapi.com", "字节跳动上报"},
		{"applog.zijieapi.com", "字节跳动上报"},
		{"toblog.ctobsnssdk.com", "字节手机上报"},
		{"api-access.pangolin-sdk-toutiao1.com", "穿山甲广告"},
		{"apmplus.volces.com", "火山 APMPlus"},
		{"ido.gepush.com", "个推上报"},
		{"stats.jpush.cn", "极光统计"},
		{"w.pandora.xiaomi.com", "MIUI 系统广告"},
		{"adreq.vivo.com.cn", "vivo 广告"},
		{"mssdk.volces.com", "火山风控 SDK"},
	}
	for _, c := range cases {
		name, _ := matchTracker(c.domain)
		if name != c.wantName {
			t.Errorf("matchTracker(%q) = %q, 期望 %q", c.domain, name, c.wantName)
		}
	}
}

// TestTrackerBusinessDomainsNotLabeled 锁住「业务域名不打标」的边界。
// 这些域名查询量大，误标会让日志满屏红、可信度归零。
func TestTrackerBusinessDomainsNotLabeled(t *testing.T) {
	for _, d := range []string{
		"www.baidu.com", "www.mi.com", "api.miwifi.com", "account.xiaomi.com",
		"www.qq.com", "mmbiz.qpic.cn", "i.gtimg.cn", "pull-h1.wxlivecdn.com",
		"www.163.com", "chat.deepseek.com", "www.taobao.com",
	} {
		if name, _ := matchTracker(d); name != "" {
			t.Errorf("业务域名 %q 不应有追踪器标签，实际 %q", d, name)
		}
	}
}
