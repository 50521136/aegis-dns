// Package model 定义领域对象。
//
// 这里只放「跨进程共享」的结构：apid 写库、apid 生成快照、dnsd 读快照，
// 三方必须对字段名与语义达成一致。JSON tag 是快照格式的一部分，改动即破坏兼容。
package model

import "time"

// 用户角色。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 规则类型（rules.kind 与快照 rules[].kind 的取值）。
const (
	KindBlock        = "block"
	KindAllow        = "allow"
	KindRewriteA     = "rewrite_a"
	KindRewriteAAAA  = "rewrite_aaaa"
	KindRewriteCNAME = "rewrite_cname"
)

// 订阅列表类型。
const (
	ListBlock = "blocklist"
	ListAllow = "allowlist"
)

// 未识别请求的兜底策略。
const (
	FallbackPassthrough   = "passthrough"
	FallbackRefused       = "refused"
	FallbackBlocklistOnly = "blocklist_only"
)

// 拦截应答模式。
const (
	BlockingNXDOMAIN = "nxdomain"
	BlockingNullIP   = "null_ip"
	BlockingRefused  = "refused"
	BlockingCustomIP = "custom_ip"
)

// User 是一个租户。
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"` // 永不出现在任何响应里
	Role         string `json:"role"`
	ClientID     string `json:"client_id"`
	Enabled      bool   `json:"enabled"`
	// UseGlobalRules 决定该用户的规则集是否叠加全局层。默认 true。
	UseGlobalRules bool `json:"use_global_rules"`
	// IDResetCount 是该用户已经重置过几次 client_id。
	// 上限由全局设置 MaxIDResets 控制，0 表示禁止自助重置。
	IDResetCount int `json:"id_reset_count"`
	// RulesEnabled / SubsEnabled 是管理员对该用户的单独开关。
	// 与全局设置里的总开关取「与」：任一为关，该用户就建不了对应的东西。
	RulesEnabled bool `json:"rules_enabled"`
	SubsEnabled  bool `json:"subs_enabled"`
	// CustomIDUsed 标记用户是否已经用掉「自定义 client_id」那一次机会。
	CustomIDUsed bool  `json:"custom_id_used"`
	CreatedAt    int64 `json:"-"` // 对外由 handler 转成 RFC3339
}

// Rule 是一条自定义规则。
//
// IsGlobal 为真表示这条规则由管理员创建、作用于所有开启了全局层的用户；
// 此时 UserID 记的是创建者（管理员），不参与「本人规则」的过滤。
type Rule struct {
	ID        string   `json:"id"`
	UserID    string   `json:"user_id,omitempty"`
	Kind      string   `json:"kind"`
	Pattern   string   `json:"pattern"`
	Value     string   `json:"value"`
	QTypes    []string `json:"qtypes"`
	Enabled   bool     `json:"enabled"`
	IsGlobal  bool     `json:"is_global,omitempty"`
	CreatedAt int64    `json:"-"`
}

// Subscription 是一个订阅源。IsGlobal 语义同 Rule.IsGlobal。
type Subscription struct {
	ID           string `json:"id"`
	UserID       string `json:"user_id,omitempty"`
	ListType     string `json:"list_type"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Format       string `json:"format"`
	Enabled      bool   `json:"enabled"`
	IsGlobal     bool   `json:"is_global,omitempty"`
	LastETag     string `json:"-"`
	LastModified string `json:"-"`
	LastFetched  int64  `json:"-"`
	LastCount    int    `json:"last_count"`
	LastStatus   string `json:"last_status"`
	FailCount    int    `json:"fail_count"`
	CreatedAt    int64  `json:"-"`
}

// Feedback 是一条用户反馈（通常是误杀申诉）。
type Feedback struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Domain     string `json:"domain"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	Status     string `json:"status"`
	AdminReply string `json:"admin_reply"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// 反馈类型与状态。
const (
	FeedbackWrongBlock = "wrong_block" // 被误拦
	FeedbackWrongAllow = "wrong_allow" // 该拦没拦
	FeedbackOther      = "other"

	FeedbackOpen    = "open"
	FeedbackReplied = "replied"
	FeedbackClosed  = "closed"
)

// UserIP 是一条来源 IP 归属记录。
type UserIP struct {
	UserID string `json:"user_id"`
	CIDR   string `json:"cidr"`
}

// Defaults 是快照里的全局默认项，dnsd 在用户未覆盖时使用。
type Defaults struct {
	Domain    string   `json:"domain"`
	Upstreams []string `json:"upstreams"`
	// BootstrapDNS 用于解析 DoH/DoT 上游里的主机名。
	//
	// 只接受 IP 字面量：用 DoH 地址做引导是循环依赖（要先解析才能连）。
	// 不配的话，DoH 上游的主机名会走系统解析器，容器里经常没有可用解析器，
	// 表现为「上游配好了却全部超时」。
	BootstrapDNS   []string `json:"bootstrap_dns"`
	BlockingMode   string   `json:"blocking_mode"`
	CustomIP       string   `json:"custom_ip"`
	FallbackPolicy string   `json:"fallback_policy"`
	// 以下为解析行为调优，apid 从全局设置读取后写进快照，
	// 使 dnsd 无需读数据库也能拿到运行参数（P2 运行期零 IO）。
	CacheSize      int  `json:"cache_size"`
	CacheMinTTL    int  `json:"cache_min_ttl"`
	CacheMaxTTL    int  `json:"cache_max_ttl"`
	RateLimitQPS   int  `json:"rate_limit_qps"`
	MaxInflight    int  `json:"max_inflight"`
	QueryTimeout   int  `json:"query_timeout_ms"`
	QueryLogSize   int  `json:"query_log_size"`
	EnableQueryLog bool `json:"enable_query_log"`
	// AllowRootDomain 决定裸域名（如 adguard.com）能否被当作合法入口。
	// 为真时它按「无租户」处理、只套全局规则；为假时直接 REFUSED。
	AllowRootDomain bool `json:"allow_root_domain"`
}

// GlobalSnap 是快照里的全局层：管理员的规则与订阅条目，对所有
// UseGlobalRules 为真的用户生效。
type GlobalSnap struct {
	Rules    []Rule   `json:"rules,omitempty"`
	SubBlock []string `json:"sub_block,omitempty"`
	SubAllow []string `json:"sub_allow,omitempty"`
}

// UserSnap 是快照里的单个用户。
type UserSnap struct {
	UserID    string   `json:"user_id"`
	ClientID  string   `json:"client_id"`
	Enabled   bool     `json:"enabled"`
	Upstreams []string `json:"upstreams,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Rules     []Rule   `json:"rules,omitempty"`
	// UseGlobalRules 为假时，dnsd 不为该用户挂全局层。
	UseGlobalRules bool `json:"use_global_rules"`
	// 订阅条目在这里被「压平」成纯域名数组：数十万条时 JSON 体积最小。
	SubAllow []string `json:"sub_allow,omitempty"`
	SubBlock []string `json:"sub_block,omitempty"`
}

// Snapshot 是 apid 写给 dnsd 的完整配置快照（data/runtime/config.json）。
type Snapshot struct {
	Version     int64     `json:"version"`
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`
	Defaults    Defaults  `json:"defaults"`
	// Global 是全局规则层，由管理员的 is_global 条目压平而成。
	Global GlobalSnap `json:"global"`
	// ChangedUsers 非空时表示增量快照：dnsd 只重建这些用户的 RuleSet，
	// 其余复用旧对象。为空表示全量。
	ChangedUsers []string   `json:"changed_users,omitempty"`
	Users        []UserSnap `json:"users"`
}

// SnapshotSchema 是当前快照格式的大版本号。
//
// dnsd 遇到比自己支持的大版本更高的快照会拒绝加载并保留旧配置，
// 避免「apid 升级后写出的新格式把老 dnsd 喂坏」。
//
// v2 新增 Global 全局层与 UserSnap.UseGlobalRules。必须升版本：老 dnsd 会
// 忽略 global 字段，于是全局规则静默失效 —— 这比拒绝加载更难排查。
// StatusPending 表示订阅正在后台拉取，条目数还没更新。
//
// 前缀 "pending" 是给前端判断用的：看到它就该轮询，而不是显示成「未同步」这种
// 看起来像出错的状态。公共规则源在慢线路上要 60~90 秒，必须让用户知道在下载中。
const StatusPending = "pending: 同步中"

const SnapshotSchema = 2

// GlobalSettings 是管理员可改的全局设置（存 meta 表，前缀 setting.）。
type GlobalSettings struct {
	Domain    string   `json:"domain"`
	Upstreams []string `json:"upstreams"`
	// BootstrapDNS 用于解析 DoH/DoT 上游里的主机名，只接受 IP 字面量。
	BootstrapDNS   []string `json:"bootstrap_dns"`
	BlockingMode   string   `json:"blocking_mode"`
	CustomIP       string   `json:"custom_ip"`
	FallbackPolicy string   `json:"fallback_policy"`
	CacheSize      int      `json:"cache_size"`
	CacheMinTTL    int      `json:"cache_min_ttl"`
	CacheMaxTTL    int      `json:"cache_max_ttl"`
	RateLimitQPS   int      `json:"rate_limit_qps"`
	MaxInflight    int      `json:"max_inflight"`
	QueryTimeoutMS int      `json:"query_timeout_ms"`
	QueryLogSize   int      `json:"query_log_size"`
	EnableQueryLog bool     `json:"enable_query_log"`
	SubIntervalH   int      `json:"sub_interval_hours"`
	LogRetentionD  int      `json:"query_log_retention_days"`
	AllowRegister  bool     `json:"allow_register"`
	InviteCode     string   `json:"invite_code"`
	UpdateRepo     string   `json:"update_repo"`
	UpdateChannel  string   `json:"update_channel"`

	// --- 配额：不限制的话单个用户就能把内存打爆 ---
	//
	// 一次订阅拉进来 23 万条是常态，而订阅条目是**按用户压平**进快照的，
	// 几个用户订阅同一份列表就会成倍膨胀。默认取严格档。
	MaxRulesPerUser int `json:"max_rules_per_user"` // 单个用户的个人规则上限
	MaxSubsPerUser  int `json:"max_subs_per_user"`  // 单个用户的个人订阅上限
	MaxSubEntries   int `json:"max_sub_entries"`    // 单个订阅解析后的条目上限（超出截断）
	MaxRulesGlobal  int `json:"max_rules_global"`   // 全局规则上限
	MaxSubsGlobal   int `json:"max_subs_global"`    // 全局订阅上限

	// AllowRootDomain 为真时，主域名本身（如 example.com）也能解析，
	// 而不只是 <client_id>.example.com。方便用户在没法填子域名的客户端里直接用主域名。
	AllowRootDomain bool `json:"allow_root_domain"`

	// StatsRetentionD 是统计汇总（query_rollup / top_domains）的保留天数。
	StatsRetentionD int `json:"stats_retention_days"`

	// MaxIDResets 是每个用户可自助重置 client_id 的次数上限，0 = 禁止。
	MaxIDResets int `json:"max_id_resets"`

	// AllowUserRules / AllowUserSubs 是租户侧的总开关。
	// 关掉后所有普通用户都不能新建个人规则 / 订阅（已有的照常生效）。
	AllowUserRules bool `json:"allow_user_rules"`
	AllowUserSubs  bool `json:"allow_user_subs"`
}

// DefaultSettings 返回一套合理的出厂设置。
//
// 注意 Domain 留空：必须由部署者显式填写，否则子域名无法派生。
func DefaultSettings() GlobalSettings {
	return GlobalSettings{
		Domain:         "",
		Upstreams:      []string{"1.1.1.1", "8.8.8.8", "https://dns.google/dns-query"},
		BlockingMode:   BlockingNullIP,
		CustomIP:       "",
		FallbackPolicy: FallbackPassthrough,
		CacheSize:      10000,
		CacheMinTTL:    60,
		CacheMaxTTL:    86400,
		RateLimitQPS:   0, // 0 = 不限制
		MaxInflight:    300,
		QueryTimeoutMS: 10000,
		QueryLogSize:   10000,
		EnableQueryLog: true,
		// 国内可达的两个公共解析器，用于解析 DoH/DoT 上游的主机名。
		BootstrapDNS:  []string{"223.5.5.5", "119.29.29.29"},
		SubIntervalH:  24,
		LogRetentionD: 7,
		AllowRegister: true,
		InviteCode:    "",
		UpdateChannel: "stable",

		AllowRootDomain: true,
		StatsRetentionD: 30,
		MaxIDResets:     3,
		AllowUserRules:  true,
		AllowUserSubs:   true,

		MaxRulesPerUser: 50,
		MaxSubsPerUser:  3,
		MaxSubEntries:   50000,
		MaxRulesGlobal:  500,
		MaxSubsGlobal:   20,
	}
}

// AppVersion 是一条在线更新记录。
type AppVersion struct {
	Channel    string `json:"channel"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Notes      string `json:"notes"`
	ReleasedAt int64  `json:"released_at"`
}

// RollupRow 是一行 5 分钟粒度的统计聚合。
type RollupRow struct {
	UserID     string `json:"user_id"`
	TS         int64  `json:"ts"`
	Total      int64  `json:"total"`
	Blocked    int64  `json:"blocked"`
	Cached     int64  `json:"cached"`
	Allowed    int64  `json:"allowed"`
	LatencySum int64  `json:"latency_sum"`
}

// TopDomainRow 是一行 TOP 域名统计。
type TopDomainRow struct {
	UserID  string `json:"user_id"`
	TS      int64  `json:"ts"`
	Domain  string `json:"domain"`
	Hits    int64  `json:"hits"`
	Blocked int64  `json:"blocked"`
}

// QueryLogRow 是一条查询日志。
type QueryLogRow struct {
	ID     int64  `json:"id"`
	UserID string `json:"user_id"`
	// Username 只在查询时通过 LEFT JOIN 填充，用于管理员全局日志；
	// 用户看自己的日志时不需要，为空即可。
	Username  string `json:"username,omitempty"`
	TS        int64  `json:"ts"`
	Domain    string `json:"domain"`
	QType     string `json:"qtype"`
	RCode     string `json:"rcode"`
	Action    string `json:"action"`
	Client    string `json:"client"`
	LatencyMS int64  `json:"latency_ms"`
}
