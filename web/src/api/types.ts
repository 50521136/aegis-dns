/* ============================================================
   与后端 REST API 契约对应的 TypeScript 类型
   见设计文档第九章 / 任务上下文 API 端点清单
   ============================================================ */

export interface User {
  id: string
  username: string
  email: string
  role: 'admin' | 'user'
  client_id: string
  enabled: boolean
  created_at: string
  use_global_rules?: boolean
  /** 已重置过几次 client_id */
  id_reset_count?: number
  /** 管理员对该用户的规则/订阅开关 */
  rules_enabled?: boolean
  subs_enabled?: boolean
  /** 是否已用掉「自定义接入 ID」那一次机会 */
  custom_id_used?: boolean
  /** 会员到期时间（Unix 秒），0 表示非会员 */
  member_until?: number
  /** 会员剩余天数（后端算好，前端直接显示） */
  member_days?: number
  /** 档位：free | vip */
  tier?: 'free' | 'vip'
  invited_by?: string
}

export interface Tokens {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

export interface PlatformGuide {
  platform: string
  steps: string[]
}

export interface DnsConfig {
  client_id: string
  domain: string
  doh_url: string
  doh_url_path: string
  dot_host: string
  dot_port: number
  plain_dns: string | null
  cert_fingerprint_sha256: string
  platform_guides: PlatformGuide[]
}

export interface AuthResponse {
  user: User
  dns_config: DnsConfig
  tokens: Tokens
}

export interface RefreshResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

/* ---------------- 规则 ---------------- */

export type RuleKind = 'block' | 'allow' | 'rewrite_a' | 'rewrite_aaaa' | 'rewrite_cname'

export interface Rule {
  id: string
  kind: RuleKind
  pattern: string
  value: string
  qtypes: string[]
  enabled: boolean
  /** 为真表示这是管理员创建的全局规则 */
  is_global?: boolean
  created_at: string
}

export interface RuleListResponse {
  items: Rule[]
  total: number
  page: number
  page_size: number
  /** 当前作用域的规则配额上限，0 或缺省表示不限制 */
  limit?: number
}

export interface RuleInput {
  kind: RuleKind
  pattern: string
  value: string
  qtypes: string[]
  enabled: boolean
}

export interface RuleValidateResponse {
  valid: boolean
  error?: string
}

export interface RuleImportResponse {
  created: number
  skipped: number
  errors: string[]
}

/* ---------------- 订阅 ---------------- */

export type ListType = 'blocklist' | 'allowlist'
export type SubFormat = 'auto' | 'adblock' | 'hosts' | 'dnsmasq' | 'domain'

export interface Subscription {
  id: string
  name: string
  url: string
  list_type: ListType
  format: SubFormat
  enabled: boolean
  /** 为真表示这是管理员创建的全局订阅 */
  is_global?: boolean
  last_count: number
  last_fetched: number | string | null
  last_status: string
  created_at: string
}

export interface SubscriptionInput {
  name: string
  url: string
  list_type: ListType
  format: SubFormat
  enabled: boolean
}

/** 刷新现在走后台任务，接口立刻回 202，只有 pending 与 last_status。 */
export interface SubscriptionRefreshResult {
  id: string
  last_status: string
  pending?: boolean
  // 同步模式下才会带上（保留字段，便于以后改回同步）
  last_count?: number
  last_fetched?: string
  duration_ms?: number
}

export interface RefreshAllResult {
  pending?: number
  queued?: boolean
  // 同步模式的旧字段，保留以兼容
  refreshed?: number
  failed?: number
}

export interface SubscriptionPreset {
  name: string
  url: string
  list_type: ListType
  description: string
}

/* ---------------- 统计 ---------------- */

export interface StatsSummary {
  range: string
  total: number
  blocked: number
  cached: number
  allowed: number
  block_rate: number
  cache_hit_rate: number
  avg_latency_ms: number
  unique_domains: number
}

export interface TimeseriesPoint {
  ts: string | number
  total: number
  blocked: number
  cached: number
  allowed: number
}

export interface TimeseriesResponse {
  points: TimeseriesPoint[]
}

export interface TopDomain {
  domain: string
  hits: number
  blocked: number
}

export interface TopResponse {
  items: TopDomain[]
}

export type QueryAction = 'allowed' | 'blocked' | 'cached' | 'rewritten'

export interface QueryLogItem {
  id: number
  ts: string | number
  domain: string
  qtype: string
  rcode: string
  action: QueryAction | string
  client: string
  latency_ms: number
  /** 接入协议：udp|tcp|dot|doh（老记录为空） */
  proto?: string
  /** EDNS Client Subnet */
  ecs?: string
  /** 实际应答的上游 */
  upstream?: string
  /** 应答内容（已截断） */
  answer?: string
  /** 命中的已知追踪器 */
  tracker?: string
  /** 客户端备注名 */
  client_name?: string
}

export interface QueryLogResponse {
  items: QueryLogItem[]
  total: number
}

/* ---------------- 用户设置 / PAT ---------------- */

export interface ApiToken {
  id: string
  name: string
  prefix: string
  created_at: string
  last_used_at?: string | null
}

export interface ApiTokenCreated {
  token: string
}

export interface ExportPayload {
  rules: RuleInput[]
  subscriptions: SubscriptionInput[]
}

/* ---------------- 管理 ---------------- */

export interface AdminUserListResponse {
  items: User[]
  total: number
  page: number
  page_size: number
}

export interface AdminSettings {
  domain: string
  upstreams: string[]
  /** 引导 DNS：只接受 IP，用于解析 DoH/DoT 上游的主机名 */
  bootstrap_dns: string[]
  /** 出口代理（http/https/socks5），给在线更新与订阅拉取用 */
  proxy_url: string
  proxy_enabled: boolean
  blocking_mode: 'nxdomain' | 'null_ip' | 'refused' | 'custom_ip'
  custom_ip: string
  fallback_policy: 'passthrough' | 'refused' | 'blocklist_only'
  cache_size: number
  cache_min_ttl: number
  cache_max_ttl: number
  rate_limit_qps: number
  max_inflight: number
  query_timeout_ms: number
  query_log_size: number
  enable_query_log: boolean
  sub_interval_hours: number
  query_log_retention_days: number
  allow_register: boolean
  invite_code: string
  update_repo: string
  update_channel: 'stable' | 'beta'

  /** 裸域名（主域名本身）是否可用；关掉后只认 <client_id>.域名 */
  allow_root_domain: boolean
  /** 统计汇总保留天数，0 = 不按时间清理 */
  stats_retention_days: number
  /** 每个用户可自助重置 client_id 的次数，0 = 禁止 */
  max_id_resets: number
  /** 租户侧总开关 */
  allow_user_rules: boolean
  allow_user_subs: boolean

  /** 配额，0 表示不限制 */
  /** 会员两档配额 */
  free_max_rules: number
  free_max_subs: number
  free_daily_queries: number
  vip_max_rules: number
  vip_max_subs: number
  vip_daily_queries: number
  member_reward_days: number
  /** 邮件服务（密码只回传 smtp_pass_set，不回传明文） */
  smtp_enabled: boolean
  smtp_host: string
  smtp_port: number
  smtp_user: string
  /** 只写字段：留空表示不修改，后端不回传明文 */
  smtp_pass?: string
  smtp_pass_set: boolean
  smtp_from: string
  smtp_from_name: string
  smtp_tls: string
  email_verify_register: boolean
  max_sub_entries: number
}

export interface SystemInfo {
  version: string
  component: string
  uptime_seconds: number
  users: number
  config_version: number
  [key: string]: unknown
}

export interface UpdateInfo {
  current: string
  latest: string
  update_available: boolean
  notes: string
  url: string
  sha256: string
  released_at: string
  reason?: string
}

export interface UpdateApplyResult {
  applied: boolean
  message: string
}

/* ---------------- 通用 ---------------- */

export interface ApiErrorBody {
  error: {
    code: string
    message: string
    details?: Record<string, unknown>
  }
}

export interface Paginated<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export interface HealthResponse {
  status?: string
  version?: string
  [key: string]: unknown
}

/** 规则/订阅的作用域：个人层或管理员的全局层。 */
export type Scope = 'user' | 'global'

/** 证书状态（GET /admin/tls）。 */
export interface TlsStatus {
  available: boolean
  self_signed: boolean
  subject: string
  issuer: string
  dns_names: string[]
  not_before: number
  not_after: number
  days_left: number
  fingerprint: string
  cert_file: string
  key_file: string
  dot_doh_enabled: boolean
}

/** 用户反馈。 */
export interface Feedback {
  id: string
  user_id: string
  username: string
  domain: string
  kind: FeedbackKind
  message: string
  status: FeedbackStatus
  admin_reply: string
  created_at: string
  updated_at: string
}

export type FeedbackKind = 'wrong_block' | 'wrong_allow' | 'other'
export type FeedbackStatus = 'open' | 'replied' | 'closed'

export interface FeedbackListResponse {
  items: Feedback[]
  total: number
  page: number
  page_size: number
  /** 仅管理员接口返回：待处理条数 */
  open_count?: number
}

/** 用户偏好。 */
export interface Preferences {
  use_global_rules: boolean
}

/** 邀请码 */
export interface InviteCode {
  code: string
  owner_id: string
  note: string
  reward_days: number
  max_uses: number
  used_count: number
  expires_at: number
  enabled: boolean
  created_at: number
  /** 后端算好的可用状态，前端不要自己拼三个条件 */
  usable: boolean
  unusable_reason?: string
}

/** 我的会员信息 */
export interface MyMembership {
  tier: 'free' | 'vip'
  member_until: number
  member_days: number
  quota: { max_rules: number; max_subs: number; daily_queries: number }
  tiers: Record<'free' | 'vip', { max_rules: number; max_subs: number; daily_queries: number }>
  invite: { total_uses: number; reward_days: number }
  /** 自助重置 client_id 的次数上限与已用次数 */
  max_id_resets: number
  id_reset_count: number
  custom_id_used: boolean
}

/** 公告 */
export interface Announcement {
  id: number
  title: string
  body: string
  level: 'info' | 'warning' | 'danger'
  mode: 'silent' | 'popup'
  dismissible: boolean
  enabled: boolean
  pinned: boolean
  starts_at: number
  ends_at: number
  created_at: number
  read_at?: number
  dismissed_at?: number
}

export interface AnnouncementList {
  items: Announcement[]
  unread: number
}
