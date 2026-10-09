package subsub

// Preset 是一条推荐订阅（文档 6.5）。
//
// 存在代码常量里而不是数据库：这些是「产品内置内容」，
// 跟着二进制版本走，让用户能在升级后自动拿到修正过的 URL。
type Preset struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	ListType    string `json:"list_type"`
	Description string `json:"description"`
	// Recommended 标记低误杀、适合默认开启的订阅。
	Recommended bool `json:"recommended"`
}

// Presets 是内置推荐订阅列表。
//
// 选源原则：优先低误杀、长期维护活跃的项目。
// 默认不推荐激进的全量列表 —— 用户被误杀一次就可能放弃整个服务（文档 R8）。
var Presets = []Preset{
	{
		Name:        "AdGuard DNS Filter",
		URL:         "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
		ListType:    "blocklist",
		Description: "AdGuard 官方综合广告与追踪拦截列表，误杀率低，推荐首选",
		Recommended: true,
	},
	{
		Name:        "OISD Basic",
		URL:         "https://big.oisd.nl/domainswild",
		ListType:    "blocklist",
		Description: "OISD 基础版，专注于明确的广告与追踪域名，误杀率极低",
		Recommended: true,
	},
	{
		Name:        "1Hosts Lite",
		URL:         "https://raw.githubusercontent.com/badmojr/1Hosts/master/Lite/domains.txt",
		ListType:    "blocklist",
		Description: "1Hosts 轻量版，体积小、加载快，适合资源受限的部署",
		Recommended: true,
	},
	{
		Name:        "StevenBlack Hosts",
		URL:         "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts",
		ListType:    "blocklist",
		Description: "经典 hosts 合集，广告 + 恶意软件，兼容性最广",
	},
	{
		Name:        "AdAway",
		URL:         "https://adaway.org/hosts.txt",
		ListType:    "blocklist",
		Description: "面向移动端的广告拦截列表，条目精简",
	},
	{
		Name:        "AdGuard Allowlist",
		URL:         "https://raw.githubusercontent.com/AdguardTeam/AdguardFilters/master/BaseFilter/sections/allowlist.txt",
		ListType:    "allowlist",
		Description: "AdGuard 官方白名单，配合黑名单使用可显著降低误杀",
		Recommended: true,
	},
	{
		Name:        "Hagezi Pro",
		URL:         "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/pro.txt",
		ListType:    "blocklist",
		Description: "Hagezi 均衡档，兼顾拦截率与误杀率",
	},
	{
		Name:        "Hagezi Light",
		URL:         "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/light.txt",
		ListType:    "blocklist",
		Description: "Hagezi 最保守档，只拦明确的广告域名",
	},
}

// PresetList 返回内置订阅列表。
func PresetList() []Preset { return Presets }
