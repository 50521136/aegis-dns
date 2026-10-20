package dns

import "strings"

// trackerSuffixes 是已知追踪器域名后缀。
//
// 只放「几乎只用于跨站跟踪与广告投放」的域名，不放 google.com / facebook.com
// 这类主域 —— 把整站标成追踪器会让日志满屏红色，用户很快就不看了。
//
// 判断用后缀匹配，所以 doubleclick.net 同时覆盖 ad.doubleclick.net。
var trackerSuffixes = []string{
	"doubleclick.net",
	"google-analytics.com",
	"googletagmanager.com",
	"googlesyndication.com",
	"googleadservices.com",
	"adservice.google.com",
	"analytics.google.com",
	"scorecardresearch.com",
	"quantserve.com",
	"quantcount.com",
	"adsrvr.org",
	"adnxs.com",
	"rubiconproject.com",
	"pubmatic.com",
	"criteo.com",
	"criteo.net",
	"taboola.com",
	"outbrain.com",
	"moatads.com",
	"serving-sys.com",
	"2mdn.net",
	"amazon-adsystem.com",
	"ads-twitter.com",
	"analytics.twitter.com",
	"branch.io",
	"app-measurement.com",
	"firebaseinstallations.googleapis.com",
	"adjust.com",
	"appsflyer.com",
	"umeng.com",
	"umengcloud.com",
	"cnzz.com",
	"hm.baidu.com",
	"pos.baidu.com",
	"cpro.baidu.com",
	"tanx.com",
	"mmstat.com",
	"alimama.com",
	"simba.taobao.com",
	"hotjar.com",
	"fullstory.com",
	"mouseflow.com",
	"clarity.ms",
	"bat.bing.com",
	"ads.linkedin.com",
	"px.ads.linkedin.com",
	"analytics.tiktok.com",
	"business-api.tiktok.com",
	"sc-static.net",
	"tr.snapchat.com",
	"ads.pinterest.com",
	"ct.pinterest.com",
	"segment.io",
	"segment.com",
	"mixpanel.com",
	"amplitude.com",
	"heapanalytics.com",
	"sentry-cdn.com",
	"bugsnag.com",
}

// trackerIndex 是后缀 → 原始后缀的索引，用于 O(1) 判断。
var trackerIndex = func() map[string]struct{} {
	m := make(map[string]struct{}, len(trackerSuffixes))
	for _, s := range trackerSuffixes {
		m[s] = struct{}{}
	}
	return m
}()

// matchTracker 返回命中的追踪器后缀；不是追踪器时返回空串。
//
// 逐级剥掉最左边的标签再查表：a.b.doubleclick.net 会依次试
// a.b.doubleclick.net / b.doubleclick.net / doubleclick.net，命中即返回。
// 域名层级最多十来个标签，直接循环比维护反向 Trie 划算得多。
func matchTracker(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if _, ok := trackerIndex[d]; ok {
			return d
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return ""
		}
		d = d[i+1:]
		// 剩两级（如 com.cn）还没命中就不必再剥了
		if !strings.Contains(d, ".") {
			return ""
		}
	}
}
