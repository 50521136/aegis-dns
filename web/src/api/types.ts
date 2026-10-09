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
  created_at: string
}

export interface RuleListResponse {
  items: Rule[]
  total: number
  page: number
  page_size: number
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

export interface SubscriptionRefreshResult {
  id: string
  last_count: number
  last_fetched: string
  last_status: string
  duration_ms: number
}

export interface RefreshAllResult {
  refreshed: number
  failed: number
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
