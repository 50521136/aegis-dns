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
	// MemberUntil 是会员到期时间（Unix 秒），0 表示从未开通/已过期。
	// 档位不单独存字段：到期即自动降级，不需要任何定时任务。
	MemberUntil int64 `json:"member_until"`
	// CustomMaxRules / CustomMaxSubs / CustomDailyQueries 是管理员给单个用户
	// 手填的配额覆盖。0 = 按档位默认（见 model.QuotaFor）。负数没有意义，
	// 写入前会被钳到 >= 0。
	CustomMaxRules     int `json:"custom_max_rules"`
	CustomMaxSubs      int `json:"custom_max_subs"`
	CustomDailyQueries int `json:"custom_daily_queries"`
	// TierID 是管理员直接指定的会员种类（tiers.id）。
	// 空串表示按 member_until 自动判定 free/vip，兼容老的会员逻辑。
	TierID string `json:"tier_id"`
	// LogClearedAt 是用户「清空自己的日志」的时间点。
	// 只影响用户自己的查询视图（ts > LogClearedAt），不删数据，
	// 所以管理员视角照旧能查到 —— 用户清的是自己的视图，不是库。
	LogClearedAt int64 `json:"-"`
	// InvitedBy 记录拉新关系，用于统计与防止重复发放奖励。
	InvitedBy string `json:"invited_by,omitempty"`
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
	// 协议开关：关掉的协议既不对外展示，也在解析入口直接拒绝。
	// 由 apid 从全局设置写进快照，dnsd 无需读库即可执行。
	EnableDoH      bool `json:"enable_doh"`
	EnableDoT      bool `json:"enable_dot"`
	EnablePlainDNS bool `json:"enable_plain_dns"`
	// AllowRootDomain 决定裸域名（如 adguard.com）能否被当作合法入口。
	// 为真时它按「无租户」处理、只套全局规则；为假时直接 REFUSED。
	AllowRootDomain bool `json:"allow_root_domain"`
	// EnableECS / EnableDNSSEC 是转发行为开关，dnsd 据此决定要不要动查询报文。
	EnableECS    bool `json:"enable_ecs"`
	EnableDNSSEC bool `json:"enable_dnssec"`
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
	// CacheFlushSeq 是该用户自己的「清除缓存」序号。
	// 普通用户只能清自己的缓存，所以不能复用全局的 CacheFlushSeq ——
	// 那个一改，全站缓存都会被清掉。
	CacheFlushSeq int64 `json:"cache_flush_seq,omitempty"`
	// 订阅条目在这里被「压平」成纯域名数组：数十万条时 JSON 体积最小。
	SubAllow []string `json:"sub_allow,omitempty"`
	SubBlock []string `json:"sub_block,omitempty"`

	// DailyQueries 是该用户每日请求量上限，0 表示不限。
	// 由 apid 在生成快照时按会员档位算好，dnsd 只负责计数与判定 ——
	// dnsd 不该知道「会员」这个概念，它只需要一个数字。
	DailyQueries int64 `json:"daily_queries,omitempty"`
	// MemberUntil 带进快照只用于展示与排障（例如自检横幅），
	// 档位判定已经在 DailyQueries 里体现。
	MemberUntil int64 `json:"member_until,omitempty"`

	// Fingerprint 是该用户「影响解析结果」的内容指纹（规则 + 订阅条目 + 开关）。
	//
	// 用途：dnsd 判断这次重建到底有没有改到这个用户，没改就不必清他的缓存。
	// 不带配额字段 —— 会员到期只影响每日上限，不影响任何已缓存的应答。
	Fingerprint string `json:"fp,omitempty"`
}

// Snapshot 是 apid 写给 dnsd 的完整配置快照（data/runtime/config.json）。
// Snapshot 是 apid 写给 dnsd 的完整配置快照。
//
// GlobalFingerprint 与 UserSnap.Fingerprint 一起用于缓存失效判定：
// 「全量重建」不等于「规则变了」—— 一次订阅刷新、一次配额调整都会触发全量重建，
// 但绝大多数用户的规则一个字节都没动。按 ChangedUsers 决定清不清缓存，
// 结果就是缓存被反复整体清空（线上实测 24 小时 33 次），命中率从理论 86% 掉到 12%。
type Snapshot struct {
	Version     int64     `json:"version"`
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`
	Defaults    Defaults  `json:"defaults"`
	// Global 是全局规则层，由管理员的 is_global 条目压平而成。
	Global GlobalSnap `json:"global"`
	// GlobalFingerprint 是全局层（全局规则/订阅 + 影响应答的默认值）的指纹。
	// 它变了才需要整体清缓存。
	GlobalFingerprint string `json:"global_fp,omitempty"`
	// CacheFlushSeq 是「清除缓存」的请求序号。
	// 管理员点一次清除，apid 就把它 +1 并重建快照；dnsd 发现序号变了就整体清空缓存。
	// 用序号而不是布尔：布尔清一次之后还得有人负责改回去，序号天然是边沿触发的。
	CacheFlushSeq int64 `json:"cache_flush_seq,omitempty"`
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
	BootstrapDNS []string `json:"bootstrap_dns"`
	// ProxyURL 是出口代理（http/https/socks5），给在线更新与订阅拉取用。
	// 空表示不走代理。
	ProxyURL       string `json:"proxy_url"`
	ProxyEnabled   bool   `json:"proxy_enabled"`
	BlockingMode   string `json:"blocking_mode"`
	CustomIP       string `json:"custom_ip"`
	FallbackPolicy string `json:"fallback_policy"`
	CacheSize      int    `json:"cache_size"`
	CacheMinTTL    int    `json:"cache_min_ttl"`
	CacheMaxTTL    int    `json:"cache_max_ttl"`
	RateLimitQPS   int    `json:"rate_limit_qps"`
	MaxInflight    int    `json:"max_inflight"`
	QueryTimeoutMS int    `json:"query_timeout_ms"`
	QueryLogSize   int    `json:"query_log_size"`
	EnableQueryLog bool   `json:"enable_query_log"`
	SubIntervalH   int    `json:"sub_interval_hours"`
	// LogRetentionMin 是查询日志保留分钟数。
	//
	// 原来按「天」保留（默认 7 天），小机器上几百万行日志会把磁盘和
	// 分页查询一起拖死。查询日志的用途是「刚才那条为什么被拦」，
	// 一小时足够定位问题，再久就该看统计聚合了。
	LogRetentionMin int `json:"query_log_retention_min"`
	// LogRetentionD 是旧字段（按天），只用于读取老配置。
	LogRetentionD int    `json:"query_log_retention_days,omitempty"`
	AllowRegister bool   `json:"allow_register"`
	InviteCode    string `json:"invite_code"`
	UpdateRepo    string `json:"update_repo"`
	UpdateChannel string `json:"update_channel"`

	// --- 配额：不限制的话单个用户就能把内存打爆 ---
	//
	// 一次订阅拉进来 23 万条是常态，而订阅条目是**按用户压平**进快照的，
	// 几个用户订阅同一份列表就会成倍膨胀。默认取严格档。
	MaxSubEntries int `json:"max_sub_entries"` // 单个订阅解析后的条目上限（超出截断）

	// 会员两档配额。档位不落库，按 users.member_until 实时判定，
	// 这里只描述「每档能给多少」。
	//
	// DailyQueries 为 0 表示不限量；单位是「每天」且按 UTC 自然日归零。
	FreeMaxRules     int   `json:"free_max_rules"`
	FreeMaxSubs      int   `json:"free_max_subs"`
	FreeDailyQueries int64 `json:"free_daily_queries"`
	VipMaxRules      int   `json:"vip_max_rules"`
	VipMaxSubs       int   `json:"vip_max_subs"`
	VipDailyQueries  int64 `json:"vip_daily_queries"`

	// MemberRewardDays 是邀请码的默认奖励天数（新建码时的预填值）。
	MemberRewardDays int `json:"member_reward_days"`

	// AllowUserInvite 允许普通用户自己生成邀请码。
	//
	// 奖励天数不开放给用户填：能自选天数等于能给自己无限续会员，
	// 用户码一律用 MemberRewardDays，只能决定「发给谁」。
	AllowUserInvite bool `json:"allow_user_invite"`
	// UserInviteMaxCodes 是每个普通用户最多能同时持有的邀请码数量。
	UserInviteMaxCodes int `json:"user_invite_max_codes"`

	// SignupMemberDays 是新注册用户直接获得的会员天数，0 表示不送。
	// 这是拉新的第一推动力：注册即得，用户才有理由留下来试用。
	SignupMemberDays int `json:"signup_member_days"`

	// --- 每日签到 ---
	CheckinEnabled bool `json:"checkin_enabled"`
	// EnableDoH / EnableDoT / EnablePlainDNS 是接入协议开关。
	// 至少要留一个开着，否则用户拿不到任何可用地址。
	EnableDoH      bool `json:"enable_doh"`
	EnableDoT      bool `json:"enable_dot"`
	EnablePlainDNS bool `json:"enable_plain_dns"`
	// CheckinDays 是单次签到赠送的天数。
	CheckinDays int `json:"checkin_days"`
	// CheckinBonusEvery 是「累计签到满多少天」触发额外奖励的门槛，0 表示不设累计奖励。
	CheckinBonusEvery int `json:"checkin_bonus_every"`
	// CheckinBonusDays 是达到累计门槛时额外赠送的天数。
	CheckinBonusDays int `json:"checkin_bonus_days"`
	// CheckinStreakDays / CheckinStreakBonus 是旧字段（按连续签到计奖励）。
	// 保留只为读取老配置：NormalizeCheckin 会把它们搬到新字段上，
	// 否则升级后用户已配好的 7/7 会静默失效。
	CheckinStreakDays  int `json:"checkin_streak_days,omitempty"`
	CheckinStreakBonus int `json:"checkin_streak_bonus,omitempty"`

	// --- 邮件服务 ---
	//
	// 用于注册邮箱验证与找回密码。SMTPPass 只写不读：
	// GET /admin/settings 返回时会被抹掉，只留一个「是否已设置」的布尔。
	SMTPEnabled  bool   `json:"smtp_enabled"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUser     string `json:"smtp_user"`
	SMTPPass     string `json:"smtp_pass"`
	SMTPFrom     string `json:"smtp_from"`
	SMTPFromName string `json:"smtp_from_name"`
	SMTPTLS      string `json:"smtp_tls"`

	// EmailVerifyRegister 为真时，注册必须先通过邮箱验证码。
	// 与 AllowRegister 是两回事：后者管「能不能注册」，这个管「注册要不要验证」。
	EmailVerifyRegister bool `json:"email_verify_register"`

	// EmailDomainWhitelist 是允许使用的邮箱后缀白名单（逗号分隔，如 qq.com,163.com）。
	// 空表示不限制。用于挡掉一次性邮箱：这类域名每天批量注册，
	// 在「注册送会员」的场景下会被直接刷穿。
	EmailDomainWhitelist string `json:"email_domain_whitelist"`
	// AllowEmailChange 允许用户自己更换邮箱（需新邮箱验证码）。
	AllowEmailChange bool `json:"allow_email_change"`

	// EnableECS 为真时把客户端的 /24 网段通过 EDNS Client Subnet 传给上游，
	// 让 CDN 返回离用户更近的节点。
	//
	// 代价必须说清楚：缓存要按网段分片（命中率下降）、用户网段会泄漏给上游。
	EnableECS bool `json:"enable_ecs"`
	// EnableDNSSEC 为真时在转发查询里带上 DO 位，并把上游的 AD 位透传给客户端。
	//
	// 注意这是「请求并透传」，不是本地校验：真正的链式校验（DNSKEY/DS/RRSIG）
	// 要自己做，每条新域名多几次 RTT。想拿到「已校验」的保证，
	// 上游本身必须验证（阿里/腾讯公共 DNS 不验证，1.1.1.1 与 8.8.8.8 验证）。
	EnableDNSSEC bool `json:"enable_dnssec"`

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
		// 缓存键是 (用户, 域名, 类型, DO)，条目数远多于域名数：
		// 线上 24 小时 2659 个独立域名，按 3 个租户 × 约 1.5 种类型算就接近 1.2 万条，
		// 原来的 1 万会把当天的热条目挤出去，命中率被 LRU 自己吃掉一部分。
		CacheSize:      30000,
		CacheMinTTL:    60,
		CacheMaxTTL:    86400,
		RateLimitQPS:   0, // 0 = 不限制
		MaxInflight:    300,
		QueryTimeoutMS: 10000,
		QueryLogSize:   10000,
		EnableQueryLog: true,
		// 三个协议默认全开，升级后行为不变
		EnableDoH:      true,
		EnableDoT:      true,
		EnablePlainDNS: true,
		// 国内可达的两个公共解析器，用于解析 DoH/DoT 上游的主机名。
		BootstrapDNS:    []string{"223.5.5.5", "119.29.29.29"},
		SubIntervalH:    24,
		LogRetentionMin: 60,
		AllowRegister:   true,
		InviteCode:      "",
		UpdateChannel:   "stable",

		AllowRootDomain: true,
		StatsRetentionD: 30,
		MaxIDResets:     3,
		AllowUserRules:  true,
		AllowUserSubs:   true,

		MaxSubEntries: 50000,

		// 免费档刻意压得比原来的统一配额更低：会员的意义就在于这两档有差别。
		FreeMaxRules:       20,
		FreeMaxSubs:        1,
		FreeDailyQueries:   50000,
		VipMaxRules:        200,
		VipMaxSubs:         10,
		VipDailyQueries:    0, // 会员不限请求量
		MemberRewardDays:   30,
		AllowUserInvite:    true,
		UserInviteMaxCodes: 5,
		SignupMemberDays:   0,

		CheckinEnabled:    false,
		CheckinDays:       1,
		CheckinBonusEvery: 7,
		CheckinBonusDays:  7,

		// 邮件默认关闭：没配 SMTP 就打开验证会让所有人都注册不了。
		SMTPEnabled:          false,
		SMTPPort:             465,
		SMTPTLS:              "tls",
		EmailVerifyRegister:  false,
		EmailDomainWhitelist: "",
		AllowEmailChange:     true,
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
	Username string `json:"username,omitempty"`
	TS       int64  `json:"ts"`
	Domain   string `json:"domain"`
	QType    string `json:"qtype"`
	RCode    string `json:"rcode"`
	Action   string `json:"action"`
	// CacheHit 表示这次应答来自缓存。它是状态的修饰（显示成「已处理（缓存）」），
	// 不是独立状态。
	CacheHit  bool   `json:"cache_hit"`
	Client    string `json:"client"`
	LatencyMS int64  `json:"latency_ms"`
	// 详情字段：列表页用不到，点开单条时才需要。
	// 老记录这些列是空的，前端按空处理即可。
	Proto      string `json:"proto,omitempty"`       // udp|tcp|dot|doh
	ECS        string `json:"ecs,omitempty"`         // EDNS Client Subnet
	Upstream   string `json:"upstream,omitempty"`    // 实际应答的上游
	Answer     string `json:"answer,omitempty"`      // 应答内容（截断）
	Tracker    string `json:"tracker,omitempty"`     // 命中的已知追踪器
	ClientName string `json:"client_name,omitempty"` // 客户端备注名
}

// InviteCode 是一个邀请码。
//
// 码本身是分发出去的凭据，创建后不可修改（改了等于把已发出去的码作废）；
// 可改的只有备注、奖励天数、次数上限、有效期与开关。
type InviteCode struct {
	Code       string `json:"code"`
	OwnerID    string `json:"owner_id"`
	Note       string `json:"note"`
	RewardDays int    `json:"reward_days"`
	MaxUses    int    `json:"max_uses"` // 0 = 不限
	UsedCount  int    `json:"used_count"`
	ExpiresAt  int64  `json:"expires_at"` // 0 = 永不过期
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
}

// Announcement 是一条站内公告。
type Announcement struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Level string `json:"level"` // info | warning | danger
	Mode  string `json:"mode"`  // silent | popup
	// Dismissible 控制 popup 模式下是否提供「不再弹出」。
	// 关掉就只能点「我知道了」，每次登录都会再弹一次 —— 用于必须看到的通知。
	Dismissible bool  `json:"dismissible"`
	Enabled     bool  `json:"enabled"`
	Pinned      bool  `json:"pinned"`
	StartsAt    int64 `json:"starts_at"` // 0 = 立即生效
	EndsAt      int64 `json:"ends_at"`   // 0 = 永不过期
	CreatedAt   int64 `json:"created_at"`
}

// AnnouncementView 是面向用户的公告，带该用户自己的已读状态。
type AnnouncementView struct {
	Announcement
	ReadAt      int64 `json:"read_at"`
	DismissedAt int64 `json:"dismissed_at"`
}

// 公告等级与投递方式常量。
const (
	AnnLevelInfo    = "info"
	AnnLevelWarning = "warning"
	AnnLevelDanger  = "danger"

	AnnModeSilent = "silent"
	AnnModePopup  = "popup"
)
