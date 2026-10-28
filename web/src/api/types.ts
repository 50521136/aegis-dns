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
  /** 会员剩余天数（后端算好，前端直接显示）；永久会员为 0，看 member_forever */
  member_days?: number
  /** 永久会员 */
  member_forever?: boolean
  /** 此刻生效的会员种类 id（free / vip / svip …） */
  tier?: string
  /** 库里记的会员种类；空串 = 会员期内走默认会员档 */
  tier_id?: string
  invited_by?: string
  /** 是否订阅每日邮件日报 */
  daily_report?: boolean
  /** 日报推送时间（北京时间 HH:MM） */
  report_time?: string
}

export interface Tokens {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

export interface PlatformGuide {
  platform: string
  /** false 表示该平台需要的协议都被关掉了，页面显示「暂不支持」 */
  supported: boolean
  steps: string[]
}

export interface DnsConfig {
  client_id: string
  domain: string
  doh_url: string
  doh_url_path: string
  dot_host: string
  dot_port: number
  // DoQ（DNS-over-QUIC）：走 UDP，与 DoT 同端口号但传输层不同。
  // QUIC 没有路径段放 client_id，所以租户靠 SNI 识别，证书无通配符时
  // 只能给主域名（与 DoT 同一个限制）。
  doq_addr?: string
  doq_host?: string
  doq_port?: number
  // DoH3 与 DoH 是同一个 URL，只是传输换成 QUIC，
  // 所以没有独立地址，只有一个「也支持 HTTP/3」的标志。
  doh3_supported?: boolean
  plain_dns: string | null
  cert_fingerprint_sha256: string
  /** 协议开关：关掉的协议后端不下发地址 */
  enable_doh: boolean
  enable_dot: boolean
  enable_plain_dns: boolean
  enable_doq?: boolean
  enable_doh3?: boolean
  /**
   * iOS / macOS 描述文件（.mobileconfig）的开关。
   *
   * 三个值都是后端算好的**生效值**：profile_doh / profile_dot 已经和全局
   * enable_doh / enable_dot 求过交集，前端直接用来决定按钮显隐，
   * 不要再自己与一次 —— 两处各算一遍必然有一天对不上。
   */
  enable_ios_profile: boolean
  profile_doh: boolean
  profile_dot: boolean
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
  /**
   * 该订阅自己的拉取超时（秒），0 表示跟随全局默认。
   *
   * 存在理由是慢速大源：默认 20 秒超时后，几个 1MB+ 且只有 30KB/s 的
   * 公共列表会必然失败，而它们是全局规则 —— 失败等于十几万条规则
   * 永远停在旧版本。给个按源放宽的口子，不必为它把全站默认调长。
   */
  timeout_sec?: number
  /**
   * 非空表示这条订阅是被「自动收录」或「手动合并」转成全局的
   * （空 = 管理员自己建的）。界面上据此加「收录」徽章。
   */
  promoted_at?: string
  /** 收录前的归属用户（仅供追溯） */
  promoted_from?: string
}

export interface SubscriptionInput {
  name: string
  url: string
  list_type: ListType
  format: SubFormat
  enabled: boolean
  /** 0 或不传 = 跟随全局默认超时 */
  timeout_sec?: number
}

/**
 * 订阅链接重复检测的结果。
 *
 * 只报「哪些订阅指向同一份内容」，不改任何数据。定时刷新本身已经会
 * 自动共享同组订阅的下载与解析，所以这些条目不需要人工处理也不会
 * 重复拉取；报出来是为了让人知道「哪几个人订了同一份」。
 */
export interface SubscriptionDuplicates {
  total_enabled: number
  /** 每轮刷新因自动共享而省掉的拉取次数 */
  auto_saved_fetches: number
  groups: {
    canonical_key: string
    /** 这一组里出现过的原始链接（形态不同但内容相同） */
    urls: string[]
    entries: number
    extra_fetches: number
    subscriptions: {
      id: string
      user_id: string
      username: string
      name: string
      /** 这条订阅自己的条目数（合并时默认保留最多的一份） */
      entries: number
    }[]
  }[]
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

export type QueryAction = 'blocked' | 'allowed' | 'handled'

export interface QueryLogItem {
  id: number
  ts: string | number
  domain: string
  qtype: string
  rcode: string
  action: QueryAction | string
  /** 应答是否来自缓存。它是状态的修饰（显示成「已处理（缓存）」），不是状态本身 */
  cache_hit?: boolean
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
  query_log_retention_min: number
  allow_register: boolean
  invite_code: string
  update_repo: string
  update_channel: 'stable' | 'beta'

  /** 裸域名（主域名本身）是否可用；关掉后只认 <client_id>.域名 */
  allow_root_domain: boolean
  /** 主域名已用作网站/前端域名，不再下发基于裸主域名的 DoH 路径式地址 */
  root_domain_reserved: boolean
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
  /** 注册赠送的天数与会员种类（种类空串 = 默认会员档） */
  signup_member_days: number
  signup_member_tier: string
  /** 每日签到 */
  checkin_enabled: boolean
  checkin_days: number
  checkin_bonus_every: number
  checkin_bonus_days: number
  checkin_tier: string
  /** 邀请奖励的会员种类（空串 = 默认会员档） */
  member_reward_tier: string
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

  /**
   * 订阅刷新调度。
   *
   * sub_refresh_hour/minute 是每日刷新时刻（本地时区，0 点是合法值），
   * 按固定时刻对齐而不是「启动后 +24h」—— 后者会被 apid 重启不断漂移。
   */
  sub_refresh_hour: number
  sub_refresh_minute: number
  /** 把这一批订阅摊开的窗口（分钟），避免同时冲击源站与 CPU */
  sub_spread_minutes: number
  /** 单个订阅的单次拉取默认超时（秒）；慢源可在订阅上单独放宽 */
  sub_fetch_timeout_sec: number

  /**
   * 反馈邮件通知。
   *
   * 反馈是「用户报障」的唯一入口，而管理员不会一直盯着面板 ——
   * 没有邮件提醒，一条误杀申诉可能压一整天。
   */
  feedback_notify_enabled: boolean
  /** 新反馈提醒的收件地址，多个用逗号分隔；留空自动取所有管理员账号的邮箱 */
  feedback_notify_email: string
  /** 管理员回复时是否也给提交者发一封 */
  feedback_notify_user_reply: boolean

  /**
   * 极验 v4 行为验证。
   *
   * 只作用于会被脚本刷的入口（注册 / 登录 / 找回密码 / 提交反馈），
   * 解析请求绝不加 —— 那是每毫秒都在跑的路径。
   */
  geetest_enabled: boolean
  geetest_id: string
  /** 只读：私钥只回「是否已配置」，永不回显明文 */
  geetest_key_set: boolean
  geetest_on_register: boolean
  geetest_on_login: boolean
  geetest_on_forgot: boolean
  geetest_on_feedback: boolean

  /** 接入协议开关：关掉的协议不下发地址、已接入的客户端会被 REFUSED */
  enable_doh: boolean
  enable_dot: boolean
  enable_plain_dns: boolean
  /**
   * iOS / macOS 描述文件（.mobileconfig）的三个开关。
   *
   * enable_ios_profile 管这条路径整体能不能用；
   * profile_doh / profile_dot 是描述文件里两种载荷（HTTPS / TLS）各自的开关。
   *
   * 后两个是上面 enable_doh / enable_dot 的**下级**：生效值 = 全局 && 本开关。
   * 全局关掉的协议绝不允许出现在描述文件里 —— 那样用户下到的文件装上之后
   * iOS 显示「已安装」但解析全挂，比不给按钮更糟。
   */
  enable_ios_profile: boolean
  profile_doh: boolean
  profile_dot: boolean
}

/** PUT /admin/settings 里只写不读的极验私钥字段。 */
export interface GeetestKeyPatch {
  /** 留空表示不修改 */
  geetest_key?: string
  /** 显式清空已保存的私钥 */
  geetest_key_clear?: boolean
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

  /** AI 诊断（异步，独立于 status） */
  ai_status?: AIDiagStatus
  ai_verdict?: AIVerdict
  ai_reason?: string
  ai_action?: AIActionType
  ai_reply?: string
  /** 建议放行的域名，逗号分隔 */
  ai_domains?: string
  /** 闸门拒绝了哪些、为什么 */
  ai_guard_notes?: string
  ai_model?: string
  ai_ran_at?: number
  /**
   * 非零表示这条反馈是 **AI 自动处理并结单** 的，前端据此显示「AI 自动处理」徽章。
   * 和 ai_status='done' 不是一回事：done 只是诊断跑完了，结论可能还在等人采纳。
   */
  ai_answered_at?: number
  /** 非零表示 AI 判断处理不了，正在等人工介入。 */
  needs_human_at?: number
  /** 用户已读到哪（时间戳）：管理员回复晚于它就说明用户还没看。 */
  user_read_at?: number
  /** 管理员已读到哪（时间戳）：用户追问晚于它就说明管理员还没处理。 */
  admin_read_at?: number

  /**
   * 时间线：用户与管理员往返的全部消息。
   *
   * 此前只有单个 admin_reply，用户追问只能再提交一条新反馈，
   * 列表里于是出现「同一个问题的第 2 条、第 3 条」。
   */
  messages?: FeedbackMessage[]
}

/** 反馈时间线上的一条消息。 */
export interface FeedbackMessage {
  id: string
  feedback_id: string
  role: FeedbackRole
  /** 提交时的用户名快照 */
  author: string
  content: string
  created_at: string
}

export type FeedbackRole = 'user' | 'admin'

export type AIDiagStatus = '' | 'pending' | 'done' | 'failed'
export type AIVerdict = 'wrong_block' | 'correct' | 'unclear'
export type AIActionType = 'add_global_allow' | 'add_user_allow' | 'no_action' | 'need_human'

/** AI 模型配置（Key 不回显，只有 ai_key_set 表示是否已配置）。 */
export interface AISettings {
  ai_enabled: boolean
  ai_base_url: string
  ai_model: string
  ai_timeout_sec: number
  ai_max_per_hour: number
  ai_trigger_mode: 'off' | 'on_submit' | 'manual'
  ai_key_set: boolean
}

/** 模型连通性测试结果。 */
export interface AITestResult {
  ok: boolean
  model?: string
  base_url?: string
  elapsed_ms?: number
  error?: string
  sample?: {
    verdict: string
    reason: string
    action: { type: string; domains?: string[] }
    reply: string
  }
}

/** 极验 v4 的公开配置（私钥永不出后端）。 */
export interface CaptchaConfig {
  enabled: boolean
  captcha_id: string
  /** 每个入口是否要求验证 */
  scenes: Partial<Record<CaptchaScene, boolean>>
}

export type CaptchaScene = 'register' | 'login' | 'forgot' | 'feedback'

/** 前端 getValidate() 返回的四个字段。 */
export interface CaptchaResult {
  lot_number: string
  captcha_output: string
  pass_token: string
  gen_time: string
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
  /** 核销后发给邀请人的会员种类；空串 = 用全局设置 */
  reward_tier: string
  max_uses: number
  used_count: number
  expires_at: number
  enabled: boolean
  created_at: number
  /** 后端算好的可用状态，前端不要自己拼三个条件 */
  usable: boolean
  unusable_reason?: string
}

/** 我的会员信息。会员状态只有一份：种类 + 有效期。 */
export interface MyMembership {
  /** 此刻生效的会员种类 id 与显示名 */
  tier: string
  tier_name: string
  /** 库里记的种类；空串 = 会员期内走默认会员档 */
  tier_id: string
  is_member: boolean
  member_forever: boolean
  member_until: number
  member_days: number
  quota: { max_rules: number; max_subs: number; daily_queries: number }
  /** 全部会员种类，用于「各会员种类说明」小弹窗 */
  tier_list: TierDef[]
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

/** 单个用户的完整信息（管理员详情弹窗用） */
export interface AdminUserDetail {
  id: string
  username: string
  email: string
  role: 'admin' | 'user'
  client_id: string
  enabled: boolean
  use_global_rules: boolean
  rules_enabled: boolean
  subs_enabled: boolean
  id_reset_count: number
  custom_id_used: boolean
  created_at: string
  member_until: string
  is_member: boolean
  member_days: number
  /** 永久会员：true 时 member_days 无意义 */
  member_forever: boolean
  /** 此刻生效的会员种类 id 与显示名（界面只显示这两个） */
  tier: string
  tier_name: string
  /** 生效配额（档位默认 + 手填覆盖） */
  quota: { max_rules: number; max_subs: number; daily_queries: number }
  /** 档位默认配额，用来显示「默认 200，你手填了 500」 */
  quota_default: { max_rules: number; max_subs: number; daily_queries: number }
  /** 手填覆盖：0 = 跟随档位，-1 = 不限 */
  custom: { max_rules: number; max_subs: number; daily_queries: number }
  /** 管理员直接指定的档位；空串 = 按会员状态自动判定 */
  tier_id: string
  /** 全部可选档位 */
  tiers: Record<string, TierDef>
  usage: { rules?: number; subs?: number; today_queries?: number }
}

/** 会员种类（档位）：每个档位一套配额，0 表示不限 */
export interface TierDef {
  id: string
  name: string
  sort: number
  max_rules: number
  max_subs: number
  daily_queries: number
  is_default: boolean
}

/** 管理员视角：用户自己加的规则（带归属） */
export interface AdminOwnedRule {
  id: string
  user_id: string
  username: string
  kind: string
  pattern: string
  value: string
  qtypes: string[]
  enabled: boolean
  created_at?: string
}

/** 管理员视角：用户自己加的订阅（带归属） */
export interface AdminOwnedSub {
  id: string
  user_id: string
  username: string
  name: string
  url: string
  list_type: string
  format: string
  enabled: boolean
  last_count: number
  last_status: string
  fail_count: number
  entry_count: number
}

// ---------------------------------------------------------------------------
// 站内通知
// ---------------------------------------------------------------------------

/** 通知类别，前端按它选图标与跳转，不靠解析正文猜。 */
export type NotificationKind =
  | 'sub_promoted'
  | 'checkin_bonus'
  | 'feedback_ai'
  | 'feedback_reply'
  | 'system'

/** 一条站内通知（只属于当前登录用户）。 */
export interface Notification {
  id: string
  kind: NotificationKind
  title: string
  body: string
  /** 点开通知后跳转的前端路由，可能为空 */
  link?: string
  /** 关联业务对象 id（订阅、反馈） */
  ref_id?: string
  /** 空串表示未读 */
  read_at: string
  created_at: string
}

export interface NotificationListResponse {
  items: Notification[]
  total: number
  /** 未读总数（导航红点用） */
  unread: number
}

/** 导航红点需要的各项未读数，一次请求拿全。 */
export interface UnreadCounts {
  notifications: number
  feedback: number
}
