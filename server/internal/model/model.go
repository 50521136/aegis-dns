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
	CreatedAt    int64  `json:"-"` // 对外由 handler 转成 RFC3339
}

// Rule 是一条自定义规则。
type Rule struct {
	ID        string   `json:"id"`
	UserID    string   `json:"user_id,omitempty"`
	Kind      string   `json:"kind"`
	Pattern   string   `json:"pattern"`
	Value     string   `json:"value"`
	QTypes    []string `json:"qtypes"`
	Enabled   bool     `json:"enabled"`
	CreatedAt int64    `json:"-"`
}

// Subscription 是一个订阅源。
type Subscription struct {
	ID           string `json:"id"`
	UserID       string `json:"user_id,omitempty"`
	ListType     string `json:"list_type"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Format       string `json:"format"`
	Enabled      bool   `json:"enabled"`
	LastETag     string `json:"-"`
	LastModified string `json:"-"`
	LastFetched  int64  `json:"-"`
	LastCount    int    `json:"last_count"`
	LastStatus   string `json:"last_status"`
	FailCount    int    `json:"fail_count"`
	CreatedAt    int64  `json:"-"`
}

// UserIP 是一条来源 IP 归属记录。
type UserIP struct {
	UserID string `json:"user_id"`
	CIDR   string `json:"cidr"`
}

// Defaults 是快照里的全局默认项，dnsd 在用户未覆盖时使用。
type Defaults struct {
	Domain         string   `json:"domain"`
	Upstreams      []string `json:"upstreams"`
	BlockingMode   string   `json:"blocking_mode"`
	CustomIP       string   `json:"custom_ip"`
	FallbackPolicy string   `json:"fallback_policy"`
	// 以下为解析行为调优，apid 从全局设置读取后写进快照，
	// 使 dnsd 无需读数据库也能拿到运行参数（P2 运行期零 IO）。
	CacheSize    int  `json:"cache_size"`
	CacheMinTTL  int  `json:"cache_min_ttl"`
	CacheMaxTTL  int  `json:"cache_max_ttl"`
	RateLimitQPS int  `json:"rate_limit_qps"`
	MaxInflight  int  `json:"max_inflight"`
	QueryTimeout int  `json:"query_timeout_ms"`
	QueryLogSize int  `json:"query_log_size"`
	EnableQueryLog bool `json:"enable_query_log"`
}

// UserSnap 是快照里的单个用户。
type UserSnap struct {
	UserID    string   `json:"user_id"`
	ClientID  string   `json:"client_id"`
	Enabled   bool     `json:"enabled"`
	Upstreams []string `json:"upstreams,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Rules     []Rule   `json:"rules,omitempty"`
	// 订阅条目在这里被「压平」成纯域名数组：数十万条时 JSON 体积最小。
	SubAllow []string `json:"sub_allow,omitempty"`
	SubBlock []string `json:"sub_block,omitempty"`
}

// Snapshot 是 apid 写给 dnsd 的完整配置快照（data/runtime/config.json）。
type Snapshot struct {
	Version     int64      `json:"version"`
	Schema      int        `json:"schema"`
	GeneratedAt time.Time  `json:"generated_at"`
	Defaults    Defaults   `json:"defaults"`
	// ChangedUsers 非空时表示增量快照：dnsd 只重建这些用户的 RuleSet，
	// 其余复用旧对象。为空表示全量。
	ChangedUsers []string   `json:"changed_users,omitempty"`
	Users        []UserSnap `json:"users"`
}

// SnapshotSchema 是当前快照格式的大版本号。
//
// dnsd 遇到比自己支持的大版本更高的快照会拒绝加载并保留旧配置，
// 避免「apid 升级后写出的新格式把老 dnsd 喂坏」。
const SnapshotSchema = 1

// GlobalSettings 是管理员可改的全局设置（存 meta 表，前缀 setting.）。
type GlobalSettings struct {
	Domain         string   `json:"domain"`
	Upstreams      []string `json:"upstreams"`
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
		SubIntervalH:   24,
		LogRetentionD:  7,
		AllowRegister:  true,
		InviteCode:     "",
		UpdateChannel:  "stable",
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
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	TS        int64  `json:"ts"`
	Domain    string `json:"domain"`
	QType     string `json:"qtype"`
	RCode     string `json:"rcode"`
	Action    string `json:"action"`
	Client    string `json:"client"`
	LatencyMS int64  `json:"latency_ms"`
}
