package dns

import "strings"

// trackerEntry 是内置追踪器库的一条记录。
//
// 只放「几乎只用于跨站跟踪与广告投放」的域名，不放 google.com / qq.com
// 这类主域 —— 把整站标成追踪器会让日志满屏红色，用户很快就不看了。
// 所以这里出现的是 configsvr.msf.3g.qq.com、hm.baidu.com 这种具体子域。
type trackerEntry struct {
	suffix   string
	name     string
	category string
}

// trackerDB 是内置追踪器库。
//
// 早先这里只有一串后缀，命中后日志里只能显示「已知追踪器」——
// 用户看得到「是追踪器」，但不知道是哪家的、干什么用的，排查时等于没信息。
// 补上名称与类别后，日志能直接写「QQ International（客户交互）」。
//
// 判断用后缀匹配，所以 doubleclick.net 同时覆盖 ad.doubleclick.net。
var trackerDB = []trackerEntry{
	// --- 广告投放 ---
	{"doubleclick.net", "DoubleClick", "广告"},
	{"googlesyndication.com", "Google AdSense", "广告"},
	{"googleadservices.com", "Google Ads", "广告"},
	{"adservice.google.com", "Google Ads", "广告"},
	{"2mdn.net", "Google DoubleClick", "广告"},
	{"adnxs.com", "AppNexus (Xandr)", "广告"},
	{"adsrvr.org", "The Trade Desk", "广告"},
	{"rubiconproject.com", "Magnite / Rubicon", "广告"},
	{"pubmatic.com", "PubMatic", "广告"},
	{"criteo.com", "Criteo", "广告"},
	{"criteo.net", "Criteo", "广告"},
	{"taboola.com", "Taboola", "广告"},
	{"outbrain.com", "Outbrain", "广告"},
	{"moatads.com", "Moat", "广告"},
	{"serving-sys.com", "Sizmek", "广告"},
	{"amazon-adsystem.com", "Amazon Ads", "广告"},
	{"ads-twitter.com", "X (Twitter) Ads", "广告"},
	{"analytics.twitter.com", "X (Twitter) Analytics", "广告"},
	{"ads.linkedin.com", "LinkedIn Ads", "广告"},
	{"px.ads.linkedin.com", "LinkedIn Ads", "广告"},
	{"ads.pinterest.com", "Pinterest Ads", "广告"},
	{"ct.pinterest.com", "Pinterest Ads", "广告"},
	{"analytics.tiktok.com", "TikTok Analytics", "广告"},
	{"business-api.tiktok.com", "TikTok Ads", "广告"},
	{"sc-static.net", "Snapchat Ads", "广告"},
	{"tr.snapchat.com", "Snapchat Ads", "广告"},
	{"tanx.com", "阿里妈妈 Tanx", "广告"},
	{"alimama.com", "阿里妈妈", "广告"},
	{"simba.taobao.com", "阿里妈妈 Simba", "广告"},
	{"pos.baidu.com", "百度联盟", "广告"},
	{"cpro.baidu.com", "百度联盟", "广告"},
	{"gdt.qq.com", "腾讯广点通", "广告"},
	{"e.qq.com", "腾讯广告", "广告"},
	{"pgdt.gtimg.cn", "腾讯广点通", "广告"},
	{"ad.xiaomi.com", "小米广告", "广告"},
	{"ad.mi.com", "小米广告", "广告"},
	{"adsmind.apdcdn.tc.qq.com", "腾讯广告", "广告"},

	// --- 统计与分析 ---
	{"google-analytics.com", "Google Analytics", "统计"},
	{"analytics.google.com", "Google Analytics", "统计"},
	{"googletagmanager.com", "Google Tag Manager", "统计"},
	{"scorecardresearch.com", "Comscore", "统计"},
	{"quantserve.com", "Quantcast", "统计"},
	{"quantcount.com", "Quantcast", "统计"},
	{"hotjar.com", "Hotjar", "统计"},
	{"fullstory.com", "FullStory", "统计"},
	{"mouseflow.com", "Mouseflow", "统计"},
	{"clarity.ms", "Microsoft Clarity", "统计"},
	{"bat.bing.com", "Microsoft Ads", "统计"},
	{"segment.io", "Segment", "统计"},
	{"segment.com", "Segment", "统计"},
	{"mixpanel.com", "Mixpanel", "统计"},
	{"amplitude.com", "Amplitude", "统计"},
	{"heapanalytics.com", "Heap", "统计"},
	{"branch.io", "Branch", "统计"},
	{"app-measurement.com", "Firebase Analytics", "统计"},
	{"firebaseinstallations.googleapis.com", "Firebase", "统计"},
	{"adjust.com", "Adjust", "统计"},
	{"appsflyer.com", "AppsFlyer", "统计"},
	{"umeng.com", "友盟 +", "统计"},
	{"umengcloud.com", "友盟 +", "统计"},
	{"cnzz.com", "友盟 CNZZ", "统计"},
	{"hm.baidu.com", "百度统计", "统计"},
	{"mmstat.com", "阿里妈妈统计", "统计"},
	{"sentry-cdn.com", "Sentry", "统计"},
	{"bugsnag.com", "Bugsnag", "统计"},

	// --- 客户交互 / 上报（AdGuard 把这类归到「客户交互」）---
	// configsvr.msf.3g.qq.com 这类 msf 域名是 QQ / 腾讯系的消息与上报通道，
	// AdGuard 标为「QQ International / 客户交互」。
	{"msf.3g.qq.com", "QQ International", "客户交互"},
	{"msf.qq.com", "QQ 消息服务", "客户交互"},
	{"configsvr.msf.qq.com", "QQ 配置上报", "客户交互"},
	{"report.qq.com", "腾讯上报", "客户交互"},
	{"pingfore.qq.com", "腾讯 Ping", "客户交互"},
	{"r.inews.qq.com", "腾讯新闻上报", "客户交互"},
	{"oth.eve.mdt.qq.com", "腾讯 MTA 统计", "客户交互"},
	{"mta.qq.com", "腾讯 MTA", "客户交互"},
	{"beacon.qq.com", "腾讯灯塔", "客户交互"},
	{"btrace.qq.com", "腾讯灯塔", "客户交互"},
	{"pingjs.qq.com", "腾讯前端监控", "客户交互"},
	{"log.tbs.qq.com", "腾讯 TBS 日志", "客户交互"},
	{"snowflake.qq.com", "腾讯 Snowflake", "客户交互"},
	{"wx.qq.com", "微信", "客户交互"},
	{"dns.weixin.qq.com", "微信 DNS", "客户交互"},
	{"short.weixin.qq.com", "微信", "客户交互"},
	{"long.weixin.qq.com", "微信", "客户交互"},
	{"szminorshort.weixin.qq.com", "微信", "客户交互"},
	{"mp.weixin.qq.com", "微信公众号", "客户交互"},
	{"res.wx.qq.com", "微信资源", "客户交互"},
	{"log.mmstat.com", "阿里妈妈上报", "客户交互"},
	{"w.mmstat.com", "阿里妈妈上报", "客户交互"},
	{"ac.mmstat.com", "阿里妈妈上报", "客户交互"},
	{"cnlogs.taobao.com", "淘宝日志", "客户交互"},
	{"ty.taobao.com", "淘宝统计", "客户交互"},
	{"log.m.taobao.com", "淘宝日志", "客户交互"},
	{"ut.taobao.com", "淘宝统计", "客户交互"},
	{"ex.m.jd.com", "京东上报", "客户交互"},
	{"log.sina.com.cn", "新浪上报", "客户交互"},
	{"beacon.sina.com.cn", "新浪灯塔", "客户交互"},
	{"growingio.com", "GrowingIO", "客户交互"},
	{"sensorsdata.cn", "神策数据", "客户交互"},
	{"kuaishouzt.com", "快手上报", "客户交互"},
	{"log.kuaishou.com", "快手日志", "客户交互"},
	{"api.kuaishouzt.com", "快手上报", "客户交互"},
	{"log.mistat.xiaomi.com", "小米统计", "客户交互"},
	{"data.mistat.xiaomi.com", "小米统计", "客户交互"},
	{"tracking.miui.com", "MIUI 上报", "客户交互"},
	{"metrics1.data.hicloud.com", "华为上报", "客户交互"},
	{"metrics2.data.hicloud.com", "华为上报", "客户交互"},
	{"metrics5.data.hicloud.com", "华为上报", "客户交互"},
	{"logservice.hicloud.com", "华为日志", "客户交互"},
	{"hianalytics.com", "华为分析", "客户交互"},
	{"logbak.hicloud.com", "华为日志", "客户交互"},
}

// trackerIndex 是后缀 → 条目 的索引，用于 O(1) 判断。
var trackerIndex = func() map[string]trackerEntry {
	m := make(map[string]trackerEntry, len(trackerDB))
	for _, e := range trackerDB {
		m[e.suffix] = e
	}
	return m
}()

// matchTracker 返回命中的追踪器名称与类别；不是追踪器时都返回空串。
//
// 逐级剥掉最左边的标签再查表：a.b.doubleclick.net 会依次试
// a.b.doubleclick.net / b.doubleclick.net / doubleclick.net，命中即返回。
// 域名层级最多十来个标签，直接循环比维护反向 Trie 划算得多。
func matchTracker(domain string) (name, category string) {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if e, ok := trackerIndex[d]; ok {
			return e.name, e.category
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return "", ""
		}
		d = d[i+1:]
		// 剩两级（如 com.cn）还没命中就不必再剥了
		if !strings.Contains(d, ".") {
			return "", ""
		}
	}
}
