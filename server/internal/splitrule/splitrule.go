// Package splitrule 负责「哪些域名该走境外上游」这份名单的获取与匹配。
//
// 为什么需要独立成包：这份名单既要在 apid 侧拉取、解析、存库，又要在
// dnsd 侧做每查询一次的匹配。两边共用同一套解析规则，否则会出现
// 「apid 认为某域名该走境外，dnsd 却按国内查」这种难查的偏差。
package splitrule

import (
	"sort"
	"strings"
)

// Preset 是一个内置的分流规则来源。
//
// 为什么内置而不是让用户自己找：多数人不知道去哪拿这份名单，
// 而选错来源会直接导致分流失效。预设里挑的都是社区长期维护、
// 每天自动更新的仓库。
type Preset struct {
	// ID 是稳定标识，存进设置里用于回显。
	ID string `json:"id"`
	// Name 是展示名。
	Name string `json:"name"`
	// URL 是规则文件地址。
	URL string `json:"url"`
	// Desc 说明这份名单的特点与适用场景。
	Desc string `json:"desc"`
	// Format 是解析格式：adblock（||domain^）、plain（裸域名）、
	// dnsmasq（server=/domain/）、gfwlist（Base64+Adblock）。
	Format string `json:"format"`
	// Kind 区分这张表是白名单还是黑名单，决定命中后走哪一侧。
	Kind string `json:"kind"`
}

// 规则表的两种语义。
const (
	// KindDomestic 是白名单：命中走国内上游。
	KindDomestic = "domestic"
	// KindGfw 是黑名单：白名单未命中时再查，命中走境外上游。
	KindGfw = "gfw"
)

// Presets 是内置规则源，分两组：白名单（国内域名）与黑名单（被墙域名）。
//
// 为什么需要两份名单而不是一份：白名单永远不全。实测
// dnsmasq-china-list 里没有 qq.com.cn，只查白名单会让 *.qq.com.cn
// 这类国内域名绕道境外解析（慢且可能拿到非最优 CDN 节点）。
// 两份一起用才能覆盖三种情况：
//
//	白名单命中            → 国内（快）
//	否则黑名单命中        → 境外（干净）
//	都没命中              → 国内（安全默认）
//
// 数据源选 Loyalsoldier/v2ray-rules-dat：它同时提供 china-list.txt
// 和 gfw.txt，都是纯域名、每日构建，且 gfw.txt 里没有混入国内域名
// （对比 rwx9032 那份会把 qq.com/baidu.com 收进来）。
var Presets = []Preset{
	// ---- 白名单：命中即走国内 ----
	{
		ID:     "china-list",
		Name:   "国内域名白名单（推荐）",
		URL:    "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/china-list.txt",
		Desc:   "国内可直连域名，约 10.9 万条，每日自动构建。命中走国内上游。",
		Format: "plain",
		Kind:   KindDomestic,
	},
	{
		ID:     "china-list-dnsmasq",
		Name:   "国内域名白名单（dnsmasq 格式）",
		URL:    "https://raw.githubusercontent.com/felixonmars/dnsmasq-china-list/master/accelerated-domains.china.conf",
		Desc:   "同上，dnsmasq 语法（server=/domain/），约 10.9 万条。数据源较老但稳定。",
		Format: "dnsmasq",
		Kind:   KindDomestic,
	},

	// ---- 黑名单：白名单未命中时再查，命中即走境外 ----
	{
		ID:     "gfw-loyalsoldier",
		Name:   "GFW 黑名单（推荐）",
		URL:    "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/gfw.txt",
		Desc:   "被墙域名，约 4500 条，纯域名格式，每日自动构建。不含国内域名。",
		Format: "plain",
		Kind:   KindGfw,
	},
	{
		ID:     "gfwlist-adblock",
		Name:   "gfwlist（Adblock 格式）",
		URL:    "https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt",
		Desc:   "gfwlist 官方源，Base64 编码的 Adblock 语法，约 5000 条。最权威但更新较慢。",
		Format: "gfwlist",
		Kind:   KindGfw,
	},
	{
		ID:     "gfw-rwx9032",
		Name:   "gfwlist 转换版（条目最多）",
		URL:    "https://raw.githubusercontent.com/rwx9032/AdguardHome/refs/heads/main/AdguardHome_Upstreams_Autoupdate.txt",
		Desc:   "约 11 万条。注意：该文件混有国内域名与上游 DNS 地址，只取 gfwlist 段落。",
		Format: "adblock",
		Kind:   KindGfw,
	},
}

// PresetsByKind 返回某一类（白名单/黑名单）的预设，供前端分栏展示。
func PresetsByKind(kind string) []Preset {
	out := make([]Preset, 0, len(Presets))
	for _, p := range Presets {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// legacyPresetAlias 把改版前的预设 ID 映射到新 ID。
//
// 为什么需要：预设 ID 存在设置表里，用户升级程序后旧 ID 仍在库里。
// 直接删掉旧 ID 会让解析格式退回默认值，而默认值与被删预设的格式不同
// （实测 dnsmasq 格式的文件按 adblock 解析得到 0 条，分流当场失效，
// 表现为「境外域名又解析不出来了」）。
var legacyPresetAlias = map[string]string{
	"china-list-inverse": "china-list-dnsmasq",
	"gfwlist-plain":      "gfw-loyalsoldier",
}

// PresetByID 按 ID 查找预设，兼容改版前的旧 ID。
func PresetByID(id string) (Preset, bool) {
	if mapped, ok := legacyPresetAlias[id]; ok {
		id = mapped
	}
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Set 是一份已排序的域名后缀集合，用于判断某个查询是否该走境外上游。
//
// 复用排序数组 + 二分查找而不是 map 或 trie：
//   - 11 万条规则时，二分查找约 17 次比较，实测单次 <1µs
//   - 查询路径每一条都要走，不能有分配；排序数组是只读的，天然并发安全
//   - 内存占用比 trie 小一个量级（11 万条约 3MB）
type Set struct {
	// sorted 是去重排序后的后缀（小写、无尾点）。
	sorted []string
	// inverse 为真表示这是白名单语义：命中 → 国内，未命中 → 境外。
	inverse bool
}

// NewSet 从域名列表构建集合。domains 里可以有重复、大小写混杂、带前导点。
func NewSet(domains []string, inverse bool) *Set {
	clean := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		d = normalizeDomain(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		clean = append(clean, d)
	}
	sort.Strings(clean)
	return &Set{sorted: clean, inverse: inverse}
}

// Size 返回集合条目数。
func (s *Set) Size() int {
	if s == nil {
		return 0
	}
	return len(s.sorted)
}

// Inverse 返回是否为白名单语义。
func (s *Set) Inverse() bool {
	if s == nil {
		return false
	}
	return s.inverse
}

// Empty 判断集合是否为空。
func (s *Set) Empty() bool {
	return s == nil || len(s.sorted) == 0
}

// Match 判断该域名是否应当走境外上游。
//
// 空集合时返回 false（全部走国内），这是安全默认值：规则没加载成功
// 也不该把流量推向更慢的境外上游。
func (s *Set) Match(domain string) bool {
	if s == nil || len(s.sorted) == 0 {
		return false
	}
	d := normalizeDomain(domain)
	if d == "" {
		return false
	}
	hit := false
	// 逐级剥掉最左标签做后缀匹配：a.b.example.com → b.example.com → example.com
	for {
		if i := sort.SearchStrings(s.sorted, d); i < len(s.sorted) && s.sorted[i] == d {
			hit = true
			break
		}
		dot := strings.IndexByte(d, '.')
		if dot < 0 {
			break
		}
		d = d[dot+1:]
	}
	if s.inverse {
		// 白名单语义：命中说明是国内域名，走国内上游。
		return !hit
	}
	return hit
}

// normalizeDomain 把域名整理成小写、无尾点、无前导点的形式。
func normalizeDomain(d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	d = strings.ToLower(d)
	d = strings.TrimPrefix(d, ".")
	d = strings.TrimSuffix(d, ".")
	// 明显的非域名内容直接丢弃（解析器已经过滤，这里兜底）。
	if d == "" || strings.ContainsAny(d, " \t/\\") {
		return ""
	}
	return d
}
