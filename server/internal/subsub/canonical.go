package subsub

import (
	"net/url"
	"regexp"
	"strings"
)

// 订阅链接的等价识别。
//
// 要解决的问题：同一个规则源会被用户用各种形态的链接添加 ——
// 原始 GitHub raw、jsDelivr 的 gh 通道、ghproxy 之类的加速前缀、
// gitcode 的镜像、github.com/…/releases/latest/download 的发布地址。
// 这些链接指向**同一份内容**，但字符串完全不同，于是被当成几个独立订阅，
// 各自下载、各自解析、各自在快照里占一份内存。
//
// 一条硬规则：**只认链接形态，绝不读内容判断。** 读内容意味着为了「猜两个源
// 是不是同一个」而把两份几十万行的列表都拉下来解析一遍 —— 那比重复拉取更贵，
// 而且一旦猜错，一个人的订阅内容会被套到另一个人身上，造成真实的拦截错误。
//
// 判不准的一律**不合并**（返回各自的原始键）。宁可多拉一次，也不要错误共享。

// mirrorPrefixes 是常见的 GitHub 加速/代理前缀。
//
// 匹配方式是「剥掉前缀后剩下的仍然是合法 URL」，所以不需要穷举，
// 但列出来的这些是线上实际出现过的（见部署机 subscriptions 表）。
// 顺序有意义：长的放前面，避免 https://ghproxy.net/ 把更长的前缀吃掉。
var mirrorPrefixes = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
	"https://ghproxy.cc/",
	"https://gh.llkk.cc/",
	"https://github.boki.moe/",
	"https://cdn.akaere.online/",
	"https://ghproxy.1888866.xyz/",
	"https://gh.xxooo.cf/",
	"https://gh-proxy.net/",
	"https://hk.gh-proxy.com/",
	"https://edgeone.gh-proxy.com/",
}

// reRawGitHub 匹配 raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>。
var reRawGitHub = regexp.MustCompile(`(?i)^https?://raw\.githubusercontent\.com/([^/]+)/([^/]+)/([^/]+)/(.+)$`)

// reJSDelivrGH 匹配 jsDelivr 的 GitHub 通道：
//
//	cdn.jsdelivr.net/gh/<owner>/<repo>@<ref>/<path>
//	gcore.jsdelivr.net/gh/<owner>/<repo>@<ref>/<path>
//	fastly.jsdelivr.net/gh/...
//
// ref 段可能带 refs/heads/ 或 refs/tags/ 前缀（jsDelivr 支持这种写法），
// 所以 ref 部分要允许含斜杠 —— 不能用 [^/]+，否则
// `@refs/heads/master/rules.txt` 会把 ref 截成 "refs"、路径截成
// "heads/master/rules.txt"，与 raw 形态永远对不上。
// 归一化时再把 refs/heads/ 前缀剥掉，才能和 raw 链接的 master 对齐。
var reJSDelivrGH = regexp.MustCompile(`(?i)^https?://(?:cdn|gcore|fastly|testingcf)\.jsdelivr\.net/gh/([^/]+)/([^@]+)@(.+)$`)

// reGitHubCom 匹配 github.com 上两种等价写法：
//
//	github.com/<owner>/<repo>/raw/<ref>/<path>
//	github.com/<owner>/<repo>/releases/latest/download/<asset>
//
// 后者语义上不是 raw 文件，但它是「同一个发布产物」的规范地址，
// 加速镜像会套在这个地址前面，所以必须能归一化到同一键。
var reGitHubComRaw = regexp.MustCompile(`(?i)^https?://github\.com/([^/]+)/([^/]+)/raw/([^/]+)/(.+)$`)
var reGitHubComRelease = regexp.MustCompile(`(?i)^https?://github\.com/([^/]+)/([^/]+)/releases/(?:latest/)?download/(.+)$`)

// reGitCode 匹配 gitcode 的 raw 镜像：raw.gitcode.com/<owner>/<repo>/raw/<ref>/<path>。
var reGitCode = regexp.MustCompile(`(?i)^https?://raw\.gitcode\.com/([^/]+)/([^/]+)/raw/([^/]+)/(.+)$`)

// refAliases 把常见的 ref 写法归一到同一形态。
//
// 不能把 master 和 main 当成同一个：那两个是**不同的分支**，内容通常不同，
// 合并会串内容。只剥掉 refs/heads/ 这类显式前缀。
func normalizeRef(ref string) string {
	r := strings.TrimSpace(ref)
	r = strings.TrimPrefix(r, "refs/heads/")
	r = strings.TrimPrefix(r, "refs/tags/")
	return r
}

// stripMirrorPrefix 逐层剥掉加速前缀（可能套了不止一层）。
func stripMirrorPrefix(raw string) string {
	cur := strings.TrimSpace(raw)
	for i := 0; i < 3; i++ { // 最多剥三层，防病态输入
		stripped := false
		for _, p := range mirrorPrefixes {
			if strings.HasPrefix(cur, p) {
				rest := cur[len(p):]
				// 必须剩下一个看起来像 URL 的东西才认，否则
				// 一个真的叫 ghfast.top 的规则源会被误剥。
				if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
					cur = rest
					stripped = true
					break
				}
			}
		}
		if !stripped {
			break
		}
	}
	return cur
}

// splitRefPath 把「ref/路径」拆成两段。
//
// jsDelivr 的 ref 可以写成 `master`，也可以写成 `refs/heads/master`，
// 而路径本身可能有多级（`adblock/pro.plus.txt`）。所以不能简单按第一个
// 斜杠切 —— 必须先剥掉 refs/heads|tags 前缀，剥完之后第一段才是 ref。
func splitRefPath(rest string) (ref, path string) {
	r := rest
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		r = strings.TrimPrefix(r, p)
	}
	if i := strings.IndexByte(r, '/'); i > 0 {
		return r[:i], r[i+1:]
	}
	return r, ""
}

// CanonicalURL 把一个订阅链接归一到「内容等价键」。
//
// 返回的键只用于**判等**，不要拿去下载 —— 下载永远用用户填的原始 URL
// （镜像对用户可能更快，或原始地址在他那里根本不通）。
//
// 判不准时返回清理过的原始 URL（去 fragment、小写 host），这样
// 至少「同一链接的两种写法」还能合并，而语义不明的不会串。
func CanonicalURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = stripMirrorPrefix(s)

	// GitHub raw
	if m := reRawGitHub.FindStringSubmatch(s); m != nil {
		return "gh:" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) +
			"@" + normalizeRef(m[3]) + "/" + m[4]
	}
	// jsDelivr gh 通道
	if m := reJSDelivrGH.FindStringSubmatch(s); m != nil {
		ref, path := splitRefPath(m[3])
		return "gh:" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) +
			"@" + normalizeRef(ref) + "/" + path
	}
	// github.com/raw/...
	if m := reGitHubComRaw.FindStringSubmatch(s); m != nil {
		return "gh:" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) +
			"@" + normalizeRef(m[3]) + "/" + m[4]
	}
	// github.com/.../releases/latest/download/<asset>
	if m := reGitHubComRelease.FindStringSubmatch(s); m != nil {
		return "ghrel:" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) + "/" + m[3]
	}
	// gitcode 镜像
	if m := reGitCode.FindStringSubmatch(s); m != nil {
		return "gh:" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) +
			"@" + normalizeRef(m[3]) + "/" + m[4]
	}

	// 其它链接：只做安全归一（小写 scheme/host、去默认端口、去 fragment）。
	//
	// 不做路径归一：路径大小写、尾斜杠在很多服务上是**有意义**的，
	// 贸然归一可能把两个不同的文件判成同一个。
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
			(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
			u.Host = u.Host[:strings.LastIndex(u.Host, ":")]
		}
		u.Fragment = ""
		return "url:" + u.String()
	}
	return "raw:" + s
}

// DuplicateGroup 是一组指向同一份内容的订阅。
type DuplicateGroup struct {
	// CanonicalKey 是归一化后的等价键（仅用于分组说明，不对外当链接用）。
	CanonicalKey string `json:"canonical_key"`
	// URLs 这一组里出现过的原始链接（已去重）。
	URLs []string `json:"urls"`
	// SubIDs 展开到这份内容的订阅 ID。
	SubIDs []string `json:"sub_ids"`
	// UserIDs 涉及的租户。
	UserIDs []string `json:"user_ids"`
	// Entries 该组每个订阅各自的条目数（取自订阅记录，不是重新统计）。
	Entries int `json:"entries"`
	// ExtraFetches 是「做了去重之后本可以省掉的拉取次数」= 订阅数 - 1。
	ExtraFetches int `json:"extra_fetches"`
}

// FindDuplicateGroups 按内容等价键把订阅分组，只返回真正的重复组。
//
// 这是「检测」，不改任何数据 —— 管理端据此提示用户/管理员哪几份是重复的。
// 真正省掉重复拉取的是 Fetch 层的 singleflight（见 FetchShared）。
func FindDuplicateGroups(subs []*DupInput) []DuplicateGroup {
	type acc struct {
		key   string
		urls  []string
		ids   []string
		users []string
		count int
	}
	// 用 map 保序不稳，最后按 ExtraFetches 与条目数排序，保证输出稳定。
	groups := map[string]*acc{}
	seenURL := map[string]bool{}

	for _, s := range subs {
		key := CanonicalURL(s.URL)
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &acc{key: key}
			groups[key] = g
		}
		g.ids = append(g.ids, s.ID)
		g.users = append(g.users, s.UserID)
		g.count += s.LastCount
		// 同一组里多个订阅可能用同一个 URL，URLs 只留唯一的。
		u := strings.TrimSpace(s.URL)
		if !seenURL[key+"|"+u] {
			seenURL[key+"|"+u] = true
			g.urls = append(g.urls, u)
		}
	}

	out := make([]DuplicateGroup, 0, len(groups))
	for _, g := range groups {
		if len(g.ids) < 2 {
			continue
		}
		out = append(out, DuplicateGroup{
			CanonicalKey: g.key,
			URLs:         g.urls,
			SubIDs:       g.ids,
			UserIDs:      g.users,
			Entries:      g.count,
			ExtraFetches: len(g.ids) - 1,
		})
	}
	// 省得最多的排前面，管理员一眼看到该先处理哪个。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ExtraFetches > out[j-1].ExtraFetches; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// DupInput 是 FindDuplicateGroups 的输入形状（避免依赖 model 包的全部字段）。
type DupInput struct {
	ID        string
	UserID    string
	URL       string
	LastCount int
}
