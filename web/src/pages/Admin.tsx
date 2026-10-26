import { useState } from 'react'
import { TierPanel } from '@/components/TierPanel'
import { listTiers } from '@/api/tiers'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm, type UseFormReturn } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Activity,
  BarChart3,
  Check,
  Cpu,
  Database,
  DatabaseBackup,
  Download,
  Gift,
  HardDriveDownload,
  KeyRound,
  LogOut,
  Mail,
  Megaphone,
  PlugZap,
  Save,
  Search,
  Send,
  Settings,
  ShieldCheck,
  Trash2,
  Users,
  X,
} from 'lucide-react'
import {
  deleteUser,
  getSettings,
  getSystem,
  listUsers,
  rebuildSnapshot,
  updateSettings,
  updateUser,
  runCleanup,
  type CleanupResult,
} from '@/api/admin'
import { testEmail } from '@/api/member'
import { errorMessage } from '@/api/client'
import { getAdminUserTraffic, type AdminStatsRange } from '@/api/adminStats'
import type { AdminSettings, User } from '@/api/types'
import { cn } from '@/lib/utils'
import { PageHeader } from '@/components/PageHeader'
import { AiSettingsCard } from '@/components/AiSettingsCard'
import { CaptchaSettingsCard } from '@/components/CaptchaSettingsCard'
import { DnsPortCard } from '@/components/DnsPortCard'
import { downloadBackup, testUpstream, testProxy, type ProxyTestResult, type UpstreamTestResult } from '@/api/admin'
import { PasswordCard, UpdateCard } from './Settings'
import { useAuth } from '@/hooks/useAuth'
import { UsersPanel } from './AdminUsersPanel'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Pagination } from '@/components/Pagination'
import { StatCard } from '@/components/StatCard'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { formatDateTime, formatNumber, formatUptime } from '@/lib/format'
import { InvitePanel, MemberOverview } from '@/pages/AdminInvite'
import { AnnouncementPanel } from '@/pages/AdminAnnouncement'
import { Textarea } from '@/components/ui/textarea'

/* ---------- 用户管理 ---------- */


/* ---------- 全局设置 ---------- */

const settingsSchema = z.object({
  domain: z.string().min(1, '请输入主域名'),
  upstreams: z.string(),
  bootstrap_dns: z.string(),
  proxy_url: z.string(),
  proxy_enabled: z.boolean(),
  blocking_mode: z.enum(['nxdomain', 'null_ip', 'refused', 'custom_ip']),
  custom_ip: z.string(),
  fallback_policy: z.enum(['passthrough', 'refused', 'blocklist_only']),
  cache_size: z.coerce.number().int().min(0),
  cache_min_ttl: z.coerce.number().int().min(0),
  cache_max_ttl: z.coerce.number().int().min(0),
  rate_limit_qps: z.coerce.number().int().min(0),
  max_inflight: z.coerce.number().int().min(1),
  query_timeout_ms: z.coerce.number().int().min(100),
  query_log_size: z.coerce.number().int().min(0),
  enable_query_log: z.boolean(),
  sub_interval_hours: z.coerce.number().int().min(1),
  query_log_retention_min: z.coerce.number().int().min(1),
  allow_register: z.boolean(),
  invite_code: z.string(),
  update_repo: z.string(),
  update_channel: z.enum(['stable', 'beta']),

  allow_root_domain: z.boolean(),
  root_domain_reserved: z.boolean(),
  stats_retention_days: z.coerce.number().int().min(0),
  max_id_resets: z.coerce.number().int().min(0),
  allow_user_rules: z.boolean(),
  allow_user_subs: z.boolean(),
  max_sub_entries: z.coerce.number().int().min(0),
  // 两档配额（free_max_rules / vip_* 等）不在这里：
  // 配额的唯一编辑入口是「会员种类」面板。
  // 两边都放输入框的话，管理员在一个页面改了、另一个页面还显示旧数字，
  // 保存时又会把旧值写回去，等于互相覆盖。
  member_reward_days: z.coerce.number().int().min(1),
  daily_report_enabled: z.boolean(),
  allow_user_invite: z.boolean(),
  user_invite_max_codes: z.coerce.number().int().min(0),
  smtp_enabled: z.boolean(),
  smtp_host: z.string(),
  smtp_port: z.coerce.number().int().min(1),
  smtp_user: z.string(),
  smtp_pass: z.string(),
  smtp_from: z.string(),
  smtp_from_name: z.string(),
  smtp_tls: z.string(),
  email_verify_register: z.boolean(),
  email_domain_whitelist: z.string(),
  allow_email_change: z.boolean(),
  // 反馈邮件通知：反馈是用户报障的唯一入口，没有提醒的话一条误杀申诉
  // 可能压一整天 —— 管理员不会一直盯着面板。
  feedback_notify_enabled: z.boolean(),
  feedback_notify_email: z.string(),
  feedback_notify_user_reply: z.boolean(),
  enable_ecs: z.boolean(),
  enable_dnssec: z.boolean(),
  enable_doh: z.boolean(),
  enable_dot: z.boolean(),
  enable_plain_dns: z.boolean(),
  signup_member_days: z.coerce.number().int().min(0),
  // 奖励发放的会员种类。空串是合法值 = 默认会员档，所以不能用 min(1)。
  signup_member_tier: z.string(),
  checkin_tier: z.string(),
  member_reward_tier: z.string(),
  checkin_enabled: z.boolean(),
  checkin_days: z.coerce.number().int().min(0),
  checkin_bonus_every: z.coerce.number().int().min(0),
  checkin_bonus_days: z.coerce.number().int().min(0),
})

type SettingsForm = z.infer<typeof settingsSchema>
type SettingsSection = 'dns' | 'general' | 'member' | 'email'

function SettingsPanel({ section }: { section: SettingsSection }) {
  const qc = useQueryClient()
  const toast = useToast()

  const settingsQuery = useQuery({ queryKey: ['admin', 'settings'], queryFn: getSettings })
  const form = useForm<SettingsForm>({ resolver: zodResolver(settingsSchema) })

  const [hydrated, setHydrated] = useState(false)
  if (settingsQuery.data && !hydrated) {
    const s = settingsQuery.data
    form.reset({
      ...s,
      upstreams: (s.upstreams ?? []).join('\n'),
      bootstrap_dns: (s.bootstrap_dns ?? []).join('\n'),
      proxy_url: s.proxy_url ?? '',
      proxy_enabled: s.proxy_enabled ?? false,
      // 密码永远从空开始：后端不回传明文，回填空串表示「不修改」
      smtp_pass: '',
    } as unknown as SettingsForm)
    setHydrated(true)
  }

  const saveMutation = useMutation({
    mutationFn: (values: SettingsForm) => {
      const payload: Partial<AdminSettings> = {
        ...values,
        bootstrap_dns: values.bootstrap_dns.split('\n').map((v) => v.trim()).filter(Boolean),
        upstreams: values.upstreams.split('\n').map((u) => u.trim()).filter(Boolean),
      }
      // 空密码不发：后端也会忽略，但请求体里带空串容易被日志误读成「清空了密码」
      if (!payload.smtp_pass) delete payload.smtp_pass
      return updateSettings(payload)
    },
    onSuccess: () => {
      toast.success('设置已保存', '配置将在 2 秒内生效')
      qc.invalidateQueries({ queryKey: ['admin', 'settings'] })
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  if (settingsQuery.isError) {
    return <ErrorState message={errorMessage(settingsQuery.error)} onRetry={() => settingsQuery.refetch()} />
  }
  if (settingsQuery.isLoading) {
    return <Skeleton className="h-[480px] rounded-2xl" />
  }

  const err = form.formState.errors
  const show = (x: SettingsSection) => section === x
  const toggle = (name: keyof SettingsForm) => (
    <Switch
      checked={Boolean(form.watch(name as never))}
      onCheckedChange={(v) => form.setValue(name as never, v as never)}
    />
  )

  return (
    <form onSubmit={form.handleSubmit((v) => saveMutation.mutate(v))} className="space-y-6">
      {/* ───────── DNS 设置 ───────── */}
      {show('dns') && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>域名与上游</CardTitle>
              <CardDescription>主域名用于生成用户专属子域名，上游为默认解析服务器。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="domain">主域名</Label>
                <Input id="domain" className="font-mono" {...form.register('domain')} />
                {err.domain && <p className="text-xs text-danger">{err.domain.message}</p>}
              </div>
              <div className="space-y-2">
                <Label htmlFor="upstreams">上游服务器（每行一个）</Label>
                <Textarea id="upstreams" rows={4} className="font-mono text-sm" {...form.register('upstreams')} />
                <p className="text-xs text-muted-foreground">
                  支持 UDP/TCP（<code>223.5.5.5</code>）、DoT（<code>tls://…</code>）、DoH（<code>https://…/dns-query</code>）。
                  第一个为主，其余在对冲延迟后并发竞速。国内机房实测：UDP 5–7ms，DoH 41–56ms。
                </p>
                {err.upstreams && <p className="text-xs text-danger">{err.upstreams.message}</p>}
              </div>
              <div className="space-y-2">
                <Label htmlFor="bootstrap_dns">引导 DNS（每行一个 IP）</Label>
                <Textarea id="bootstrap_dns" rows={2} className="font-mono text-sm" {...form.register('bootstrap_dns')} />
                <p className="text-xs text-muted-foreground">
                  只接受 IP：用来解析 DoH/DoT 上游里的主机名。用域名做引导是循环依赖。
                </p>
              </div>
              <UpstreamTester upstream={form.watch('upstreams')} bootstrap={form.watch('bootstrap_dns')} />
            </CardContent>
          </Card>

          <DnsPortCard />

          <Card>
            <CardHeader>
              <CardTitle>拦截与兜底策略</CardTitle>
              <CardDescription>命中拦截规则时返回什么，以及未识别请求怎么处理。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label>拦截返回方式</Label>
                <Select
                  value={form.watch('blocking_mode')}
                  onValueChange={(v) => form.setValue('blocking_mode', v as SettingsForm['blocking_mode'])}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="null_ip">0.0.0.0 / ::（推荐，最快失败）</SelectItem>
                    <SelectItem value="nxdomain">NXDOMAIN（域名不存在）</SelectItem>
                    <SelectItem value="refused">REFUSED（明确拒绝）</SelectItem>
                    <SelectItem value="custom_ip">自定义 IP</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {form.watch('blocking_mode') === 'custom_ip' && (
                <div className="space-y-2">
                  <Label htmlFor="custom_ip">自定义拦截 IP</Label>
                  <Input id="custom_ip" className="font-mono" placeholder="例如 0.0.0.0" {...form.register('custom_ip')} />
                  {err.custom_ip && <p className="text-xs text-danger">{err.custom_ip.message}</p>}
                </div>
              )}
              <div className="space-y-2">
                <Label>未识别请求的处理</Label>
                <Select
                  value={form.watch('fallback_policy')}
                  onValueChange={(v) => form.setValue('fallback_policy', v as SettingsForm['fallback_policy'])}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="blocklist_only">仅应用全局黑名单</SelectItem>
                    <SelectItem value="passthrough">透传上游</SelectItem>
                    <SelectItem value="refused">直接 REFUSED</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">
                  「透传上游」会让本服务变成开放解析器，存在被滥用的风险。
                </p>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>缓存与性能</CardTitle>
              <CardDescription>
                缓存直接决定延迟。改动后建议观察命中率：日志里「规则变更，已定向失效缓存」不应频繁出现。
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <NumberField form={form} name="cache_size" label="缓存条目上限" />
                <NumberField form={form} name="cache_min_ttl" label="最小 TTL（秒）" />
                <NumberField form={form} name="cache_max_ttl" label="最大 TTL（秒）" />
                <NumberField form={form} name="query_timeout_ms" label="查询总超时（毫秒）" />
                <NumberField form={form} name="max_inflight" label="最大并发查询" />
                <NumberField form={form} name="rate_limit_qps" label="单 IP 限速 QPS（0=不限）" />
              </div>
              <p className="text-xs text-muted-foreground">
                提高最小 TTL 能显著抬升命中率（很多 CDN 域名 TTL 只有 15–20 秒，永远等不到第二次查询命中），
                代价是最多陈旧这么久。单上游超时固定 1.5 秒、对冲延迟 300 毫秒，都是代码常量。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>EDNS 与 DNSSEC</CardTitle>
              <CardDescription>两个都会改变转发给上游的报文，默认关闭。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              {/* 接入协议开关：关掉后用户端不再显示对应地址，解析入口也会直接拒绝 */}
              <div className="space-y-2">
                <p className="text-sm font-medium">接入协议</p>
                <p className="text-xs text-muted-foreground">
                  关掉之后「我的 DNS」不再显示该协议的地址，已按此协议接入的客户端会被拒绝（REFUSED）。
                  三个至少要留一个开着。
                </p>
                {[
                  { key: 'enable_doh' as const, name: 'DoH（DNS over HTTPS）', desc: '走 443，兼容性最好，浏览器和 iOS/macOS 都能用。' },
                  { key: 'enable_dot' as const, name: 'DoT（DNS over TLS）', desc: '走 853，Android「私人 DNS」只认这个。' },
                  { key: 'enable_plain_dns' as const, name: '普通 DNS（UDP/TCP 53）', desc: '不加密。出于防滥用考虑，用户页从不展示明文地址，这里只控制 53 端口是否接受查询。' },
                ].map((it) => (
                  <div
                    key={it.key}
                    className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3"
                  >
                    <div className="min-w-0">
                      <p className="text-sm font-medium">{it.name}</p>
                      <p className="mt-0.5 text-xs text-muted-foreground">{it.desc}</p>
                    </div>
                    {toggle(it.key)}
                  </div>
                ))}
                {!form.watch('enable_doh') && !form.watch('enable_dot') && !form.watch('enable_plain_dns') && (
                  <p className="text-xs text-danger">三个协议全部关闭后服务将不可用，保存会被拒绝。</p>
                )}
                {/* DoH 是主路径：iOS/macOS 描述文件、二维码、路径式地址全靠它。
                    关掉之后这些入口会一起消失，用户会以为服务坏了。 */}
                {!form.watch('enable_doh') && (
                  <p className="rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning">
                    关闭 DoH 会连带失效：iOS / macOS 描述文件、接入二维码、路径式 DoH 地址，
                    以及所有用 DoH 接入的设备（会直接收到 REFUSED）。只保留 DoT 时，
                    Android「私人 DNS」仍可用，其余平台需要改用支持 DoT 的客户端。
                  </p>
                )}
                {!form.watch('enable_dot') && (
                  <p className="rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning">
                    关闭 DoT 后 Android「私人 DNS」无法接入，该平台指引会显示「暂不支持」。
                  </p>
                )}
              </div>

              <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium">EDNS Client Subnet（ECS）</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    把客户端所在网段（IPv4 /24、IPv6 /56）带给上游，让 CDN 返回更近的节点。
                    代价：用户网段会暴露给上游；且缓存仍按域名共享，第一个用户的网段决定了这次应答，
                    后续用户直接命中缓存 —— 真正的就近效果会被缓存削弱。
                  </p>
                </div>
                {toggle('enable_ecs')}
              </div>
              <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium">DNSSEC（请求并透传）</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    向上游带上 DO 位，并把上游的 AD 位透给客户端。<strong className="text-foreground">这不是本地校验</strong>
                    ：真正的链式校验要自己做。想拿到「已校验」的保证，上游本身必须验证
                    —— 阿里 / 腾讯公共 DNS 不验证，1.1.1.1 与 8.8.8.8 验证但延迟高一个数量级。
                  </p>
                </div>
                {toggle('enable_dnssec')}
              </div>
            </CardContent>
          </Card>
        </>
      )}

      {/* ───────── 常规设置 ───────── */}
      {show('general') && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>访问与注册</CardTitle>
              <CardDescription>控制谁能用这套服务。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              <ToggleRow
                title="允许公开注册"
                desc="关闭后只能由管理员建号"
                checked={Boolean(form.watch('allow_register'))}
                onChange={(v) => form.setValue('allow_register', v)}
              />
              <ToggleRow
                title="主域名本身可解析"
                desc="关闭时裸域名请求直接 REFUSED，用户分配的子域名不受影响"
                checked={Boolean(form.watch('allow_root_domain'))}
                onChange={(v) => form.setValue('allow_root_domain', v)}
              />
              <ToggleRow
                title="主域名已用作网站"
                desc="打开后不再下发 <主域名>/dns-query/<ID> 这种路径式 DoH 地址（它会指向你的网站而不是 DNS 服务）。需要通配符证书，否则路径式是唯一可用形态、不会被隐藏"
                checked={Boolean(form.watch('root_domain_reserved'))}
                onChange={(v) => form.setValue('root_domain_reserved', v)}
              />
              <div className="space-y-2">
                <Label htmlFor="invite_code">全局邀请码（可选）</Label>
                <Input id="invite_code" className="font-mono uppercase" placeholder="留空表示不需要邀请码" {...form.register('invite_code')} />
                <p className="text-xs text-muted-foreground">
                  填了以后注册必须带这个码。<strong className="text-foreground">多码与奖励机制</strong>在「会员与邀请」里管理。
                </p>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>租户功能开关</CardTitle>
              <CardDescription>关掉后普通用户不能新建，已有内容照常生效。</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-3 sm:grid-cols-2">
              <ToggleRow
                title="允许用户加规则"
                desc="个人规则层的总开关"
                checked={Boolean(form.watch('allow_user_rules'))}
                onChange={(v) => form.setValue('allow_user_rules', v)}
              />
              <ToggleRow
                title="允许用户加订阅"
                desc="个人订阅层的总开关"
                checked={Boolean(form.watch('allow_user_subs'))}
                onChange={(v) => form.setValue('allow_user_subs', v)}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>日志与订阅</CardTitle>
              <CardDescription>查询日志的开关、容量与保留期。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <ToggleRow
                title="记录查询日志"
                desc="关掉后用户端和管理端都看不到新的查询记录"
                checked={Boolean(form.watch('enable_query_log'))}
                onChange={(v) => form.setValue('enable_query_log', v)}
              />
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <NumberField form={form} name="query_log_size" label="内存日志缓冲条数" />
                <NumberField form={form} name="query_log_retention_min" label="日志保留分钟（默认 60）" />
                <NumberField form={form} name="stats_retention_days" label="统计保留天数（0=不清理）" />
                <NumberField form={form} name="sub_interval_hours" label="订阅刷新间隔（小时）" />
                <NumberField form={form} name="max_id_resets" label="自助重置 ID 次数（0=关闭）" />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>在线更新与代理</CardTitle>
              <CardDescription>更新走 GitHub，国内机房通常需要代理。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="update_repo">更新仓库</Label>
                  <Input id="update_repo" className="font-mono" placeholder="owner/repo" {...form.register('update_repo')} />
                </div>
                <div className="space-y-2">
                  <Label>更新通道</Label>
                  <Select value={form.watch('update_channel')} onValueChange={(v) => form.setValue('update_channel', v as SettingsForm['update_channel'])}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="stable">stable</SelectItem>
                      <SelectItem value="beta">beta</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="space-y-3 rounded-xl border border-border/60 p-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">出口代理</p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      在线更新与订阅拉取共用。国内机房访问 GitHub 常完全不通，这里填现成的代理即可，不影响解析。
                    </p>
                  </div>
                  <Switch
                    checked={Boolean(form.watch('proxy_enabled'))}
                    onCheckedChange={(v) => form.setValue('proxy_enabled', v)}
                    aria-label="启用代理"
                  />
                </div>
                <Input className="font-mono" placeholder="socks5://127.0.0.1:1080 或 http://user:pass@1.2.3.4:7890" {...form.register('proxy_url')} />
                <p className="text-xs text-muted-foreground">支持 http / https / socks5，必须带协议前缀。</p>
                <ProxyTester
                  proxyURL={form.watch('proxy_url') ?? ''}
                  proxyEnabled={Boolean(form.watch('proxy_enabled'))}
                />
              </div>

              <AiSettingsCard />
            </CardContent>
          </Card>
        </>
      )}

      {/* ───────── 会员与邀请 ───────── */}
      {show('member') && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>订阅条目上限</CardTitle>
              <CardDescription>
                各档位的配额在下面的「会员种类」里改 —— 配额只留一个编辑入口，避免两处数字打架。
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <NumberField form={form} name="max_sub_entries" label="单订阅条目上限" />
              <p className="text-xs text-muted-foreground">
                0 表示不限制。每日请求量按北京时间自然日归零，由 dnsd 在解析入口判定，超额直接 REFUSED。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>每日邮件日报</CardTitle>
              <CardDescription>
                每天把前一天的解析数据（查询量、拦截量、缓存命中、平均延迟）汇总成一封报表邮件。
                用户还要在自己的「设置 → 安全」里单独打开，默认关闭。
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <ToggleRow
                title="开启日报推送"
                desc="关闭后所有人都不再收到日报；用户自己的订阅选择会保留"
                checked={Boolean(form.watch('daily_report_enabled'))}
                onChange={(v) => form.setValue('daily_report_enabled', v)}
              />
              <p className="text-xs text-muted-foreground">
                推送时间由每个用户在自己的「设置 → 安全 → 通知设置」里设置（默认 09:00）——
                时间只留一个来源，避免两处都能改。用户在那里还能点「测试推送」当场确认排版。
                只统计「昨天 0 点到今天 0 点」的数据；当天没有任何查询的用户不会收到邮件。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>注册赠送与签到</CardTitle>
              <CardDescription>拉新的第一推动力：注册即得，每日签到还能继续攒。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <NumberField form={form} name="signup_member_days" label="注册赠送天数（0=不送）" />
                <TierSelect form={form} name="signup_member_tier" label="注册赠送的会员种类" />
              </div>
              <ToggleRow
                title="开启每日签到"
                desc="用户每天可签到一次，获得会员天数"
                checked={Boolean(form.watch('checkin_enabled'))}
                onChange={(v) => form.setValue('checkin_enabled', v)}
              />
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <NumberField form={form} name="checkin_days" label="单次签到赠送天数" />
                <TierSelect form={form} name="checkin_tier" label="签到赠送的会员种类" />
                <NumberField form={form} name="checkin_bonus_every" label="累计签到门槛（天，0=不设）" />
                <NumberField form={form} name="checkin_bonus_days" label="累计额外奖励天数" />
              </div>
              <p className="text-xs text-muted-foreground">
                按累计签到天数发放，不要求连续：门槛 7 天时，累计第 7、14、21 天各触发一次。
                中间漏签不影响，累计够天数就给。赠送的会员种类只在有效期内生效，
                到期自动回到默认档；用户已经在更高档位时不会被降下来。
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>邀请设置</CardTitle>
              <CardDescription>用户自建邀请码时的规则。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <NumberField form={form} name="member_reward_days" label="邀请奖励默认天数" />
                <TierSelect form={form} name="member_reward_tier" label="邀请奖励的会员种类" />
                <NumberField form={form} name="user_invite_max_codes" label="每用户最多邀请码数" />
              </div>
              <ToggleRow
                title="允许用户自建邀请码"
                desc="开启后普通用户能生成自己的邀请链接拉新，奖励天数固定用上面的值（不给用户自选）"
                checked={Boolean(form.watch('allow_user_invite'))}
                onChange={(v) => form.setValue('allow_user_invite', v)}
              />
            </CardContent>
          </Card>
        </>
      )}

      {/* ───────── 邮箱 ───────── */}
      {show('email') && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>邮件服务（SMTP）</CardTitle>
              <CardDescription>注册验证、找回密码、更换邮箱都依赖它。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              <ToggleRow
                title="启用邮件服务"
                desc="关掉后上面三项都不可用"
                checked={Boolean(form.watch('smtp_enabled'))}
                onChange={(v) => form.setValue('smtp_enabled', v)}
              />
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_host">SMTP 服务器</Label>
                  <Input id="smtp_host" placeholder="smtp.example.com" {...form.register('smtp_host')} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_port">端口</Label>
                  <Input id="smtp_port" type="number" placeholder="465" {...form.register('smtp_port')} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_user">用户名</Label>
                  <Input id="smtp_user" placeholder="通常就是邮箱地址" {...form.register('smtp_user')} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_pass">
                    密码 / 授权码
                    {settingsQuery.data?.smtp_pass_set && (
                      <span className="ml-2 text-xs text-success">已设置，留空表示不修改</span>
                    )}
                  </Label>
                  <Input
                    id="smtp_pass"
                    type="password"
                    autoComplete="new-password"
                    placeholder={settingsQuery.data?.smtp_pass_set ? '••••••••' : 'SMTP 授权码'}
                    {...form.register('smtp_pass')}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_from">发件人地址</Label>
                  <Input id="smtp_from" placeholder="noreply@example.com" {...form.register('smtp_from')} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_from_name">发件人名称</Label>
                  <Input id="smtp_from_name" placeholder="可选" {...form.register('smtp_from_name')} />
                </div>
                <div className="space-y-1.5">
                  <Label>加密方式</Label>
                  <Select value={form.watch('smtp_tls')} onValueChange={(v) => form.setValue('smtp_tls', v)}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="tls">TLS（隐式，465）</SelectItem>
                      <SelectItem value="starttls">STARTTLS（587）</SelectItem>
                      <SelectItem value="none">不加密（仅本地中继）</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <EmailTester defaultTo={form.watch('smtp_from')} enabled={Boolean(form.watch('smtp_enabled'))} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>邮箱策略</CardTitle>
              <CardDescription>限制可用后缀能挡掉一次性邮箱，避免「注册送会员」被刷。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <ToggleRow
                title="注册需要邮箱验证"
                desc="开启后注册必须先收到验证码；未配置好邮件时不允许开启"
                checked={Boolean(form.watch('email_verify_register'))}
                onChange={(v) => form.setValue('email_verify_register', v)}
              />
              <ToggleRow
                title="允许用户更换邮箱"
                desc="需新邮箱收到验证码才能换"
                checked={Boolean(form.watch('allow_email_change'))}
                onChange={(v) => form.setValue('allow_email_change', v)}
              />
              <div className="space-y-2">
                <Label htmlFor="email_domain_whitelist">允许的邮箱后缀</Label>
                <Textarea
                  id="email_domain_whitelist"
                  rows={3}
                  className="font-mono text-sm"
                  placeholder="qq.com, 163.com, gmail.com"
                  {...form.register('email_domain_whitelist')}
                />
                <p className="text-xs text-muted-foreground">
                  逗号或换行分隔，留空表示不限制。写 <code>qq.com</code> 会同时允许 <code>mail.qq.com</code> 这类子域。
                </p>
              </div>
            </CardContent>
          </Card>

          {/* 反馈提醒：反馈是报障的唯一入口，没有邮件的话用户提交完就没人知道。
              与「邮箱策略」分开成一张卡，是因为它属于运营通知，不是注册策略。 */}
          <Card>
            <CardHeader>
              <CardTitle>反馈邮件通知</CardTitle>
              <CardDescription>
                用户提交反馈时提醒管理员，管理员回复时通知提交者。
                只在「邮件服务」已启用时生效，发信失败不影响反馈本身。
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <ToggleRow
                title="启用反馈邮件通知"
                desc={
                  form.watch('smtp_enabled')
                    ? '总开关。关掉后两条通知都不发'
                    : '邮件服务未启用，打开这个开关也不会有信发出 —— 请先在上面的「邮件服务」里配置 SMTP'
                }
                checked={Boolean(form.watch('feedback_notify_enabled'))}
                onChange={(v) => form.setValue('feedback_notify_enabled', v)}
              />
              <div className="space-y-2">
                <Label htmlFor="feedback_notify_email">新反馈提醒发给谁</Label>
                <Input
                  id="feedback_notify_email"
                  className="font-mono"
                  placeholder="留空 = 自动发给所有管理员账号的邮箱"
                  {...form.register('feedback_notify_email')}
                />
                <p className="text-xs text-muted-foreground">
                  多个地址用逗号分隔。留空时自动取本站所有管理员账号绑定的邮箱 ——
                  多数部署只有一个管理员，让他再去填一遍自己的邮箱纯属多余。
                </p>
              </div>
              <ToggleRow
                title="回复时也通知提交者"
                desc="给用户的邮箱发一封「你的反馈已回复」。有人只想要来新工单的提醒，不想主动给用户发信，所以拆成单独开关"
                checked={Boolean(form.watch('feedback_notify_user_reply'))}
                onChange={(v) => form.setValue('feedback_notify_user_reply', v)}
              />
            </CardContent>
          </Card>
        </>
      )}

      <div className="flex justify-end">
        <Button type="submit" loading={saveMutation.isPending}>
          <Check />
          保存设置
        </Button>
      </div>
    </form>
  )
}

/** ToggleRow 是「标题 + 说明 + 开关」的通用行，设置项大多是这个形状。 */
function NumberField({
  form,
  name,
  label,
}: {
  form: UseFormReturn<SettingsForm>
  name: keyof SettingsForm & string
  label: string
}) {
  const e = form.formState.errors[name]
  return (
    <div className="space-y-1.5">
      <Label htmlFor={name}>{label}</Label>
      <Input id={name} type="number" className="font-mono" {...form.register(name as never)} />
      {e && <p className="text-xs text-danger">{String(e.message ?? '')}</p>}
    </div>
  )
}

/**
 * TierSelect 是「赠送的会员种类」下拉。
 *
 * 选项来自 /admin/tiers，第一项是空串 = 默认会员档（内置 vip）。
 * 不让管理员填 id 字符串：填错了发放时会静默回落到默认档，
 * 而界面上看起来一切正常。
 */
function TierSelect({
  form,
  name,
  label,
}: {
  form: UseFormReturn<SettingsForm>
  name: keyof SettingsForm & string
  label: string
}) {
  const { data } = useQuery({ queryKey: ['tiers'], queryFn: listTiers })
  const items = [...(data ?? [])].sort((a, b) => a.sort - b.sort)
  const fallback = items.find((t) => t.id === 'vip') ?? items.find((t) => !t.is_default)
  return (
    <div className="space-y-1.5">
      <Label htmlFor={name}>{label}</Label>
      <select
        id={name}
        className="h-9 w-full rounded-lg border border-border bg-background px-2 text-sm"
        {...form.register(name as never)}
      >
        <option value="">默认会员档{fallback ? `（${fallback.name}）` : ''}</option>
        {items.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
            {t.is_default ? '（默认档）' : ''}
          </option>
        ))}
      </select>
    </div>
  )
}

function ToggleRow({
  title,
  desc,
  checked,
  onChange,
}: {
  title: string
  desc: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
      <div className="min-w-0">
        <p className="text-sm font-medium">{title}</p>
        <p className="mt-0.5 text-xs text-muted-foreground">{desc}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={title} />
    </div>
  )
}

function SystemPanel() {
  const qc = useQueryClient()
  const toast = useToast()

  const systemQuery = useQuery({ queryKey: ['admin', 'system'], queryFn: getSystem })

  const rebuildMutation = useMutation({
    mutationFn: rebuildSnapshot,
    onSuccess: (res) => {
      toast.success('快照已重建', `当前配置版本 v${res.version}`)
      qc.invalidateQueries({ queryKey: ['admin', 'system'] })
    },
    onError: (err) => toast.error('重建失败', errorMessage(err)),
  })

  const sys = systemQuery.data

  return (
    <div className="space-y-6">
      {systemQuery.isError ? (
        <ErrorState message={errorMessage(systemQuery.error)} onRetry={() => systemQuery.refetch()} />
      ) : systemQuery.isLoading ? (
        <Skeleton className="h-40 rounded-2xl" />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard label="版本" value={sys?.version ?? '—'} icon={<Activity className="size-4" />} tone="primary" animate={false} />
          <StatCard label="用户数" value={sys?.users ?? 0} icon={<Users className="size-4" />} />
          <StatCard
            label="运行时长"
            value={formatUptime(sys?.uptime_seconds)}
            icon={<Cpu className="size-4" />}
            tone="success"
            animate={false}
          />
          <StatCard label="配置版本" value={sys?.config_version ?? 0} icon={<Database className="size-4" />} tone="warning" />
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>系统信息</CardTitle>
          <CardDescription>运行组件与配置快照状态</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {sys &&
            Object.entries(sys)
              .filter(([, v]) => typeof v !== 'object')
              .map(([k, v]) => (
                <div
                  key={k}
                  className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1 border-b border-border/50 py-2 text-sm last:border-0"
                >
                  <span className="shrink-0 text-muted-foreground">{k}</span>
                  {/* 证书指纹这类长串必须能断行：它一个单词 95 个字符，
                      不给 break-all 会把整行撑到 700 多像素，
                      手机上整个页面横向滚动。 */}
                  <span className="min-w-0 break-all text-right font-mono">{String(v)}</span>
                </div>
              ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <HardDriveDownload className="size-5 text-primary" />
            配置快照
          </CardTitle>
          <CardDescription>
            强制重新生成 runtime/config.json，DNS 引擎会在 2 秒内热加载生效。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button onClick={() => rebuildMutation.mutate()} loading={rebuildMutation.isPending}>
            <HardDriveDownload />
            立即重建快照
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}

/* ---------- 主页面 ---------- */

/**
 * 管理后台现在是多个独立页面，不再是一个大 Tab 面板。
 *
 * 拆开的理由：设置项已经从十几个涨到四十多个，全塞在一个页面里
 * 「改个缓存 TTL 要滚过整屏配额和 SMTP」，而且没有地址能直接发给别人。
 */
export function AdminPage({
  title,
  description,
  icon,
  children,
}: {
  title: string
  description: string
  icon: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="space-y-6">
      <PageHeader title={title} description={description} icon={icon} />
      {children}
    </div>
  )
}

/** 用户管理 */
export function AdminUsersPage() {
  return (
    <AdminPage
      title="用户"
      description="账号、接入 ID、会员天数与配额用量"
      icon={<Users className="size-5" />}
    >
      <UsersPanel />
    </AdminPage>
  )
}

/** 会员与邀请：配额、激励规则、邀请码全在这里 */
export function AdminMemberPage() {
  return (
    <AdminPage
      title="会员与邀请"
      description="会员种类（档位）与配额、注册赠送、每日签到、邀请码与拉新奖励"
      icon={<Gift className="size-5" />}
    >
      <SettingsPanel section="member" />
      <div className="space-y-6">
        <TierPanel />
        <MemberOverview />
        <InvitePanel />
      </div>
    </AdminPage>
  )
}

/** 公告 */
export function AdminAnnouncePage() {
  return (
    <AdminPage
      title="公告"
      description="静默公告进公告栏，弹窗公告进控制台时弹出"
      icon={<Megaphone className="size-5" />}
    >
      <AnnouncementPanel />
    </AdminPage>
  )
}

/** DNS 设置：域名、上游、拦截、缓存、EDNS/DNSSEC */
export function AdminDnsPage() {
  return (
    <AdminPage
      title="DNS 设置"
      description="域名与上游、拦截与兜底、缓存与性能、EDNS 与 DNSSEC"
      icon={<Activity className="size-5" />}
    >
      <SettingsPanel section="dns" />
    </AdminPage>
  )
}

/**
 * 安全验证：极验 v4 与各入口开关。
 *
 * 单独成一个页面而不是塞进「常规设置」：常规设置里已经有访问注册、租户开关、
 * 日志、更新、代理四张卡，再加一张只会让「验证开没开」这件事更难被找到 ——
 * 而它恰恰是出事时才去找的东西。
 */
export function AdminSecurityPage() {
  return (
    <AdminPage
      title="安全验证"
      description="极验 v4 人机验证：注册、登录、找回密码、提交反馈的独立开关"
      icon={<ShieldCheck className="size-5" />}
    >
      <div className="min-w-0 space-y-6">
        <Card>
          <CardHeader>
            <CardTitle>哪些地方需要验证</CardTitle>
            <CardDescription>
              被脚本刷的入口才需要验证；解析请求永远不加，那是每毫秒都在跑的路径。
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CaptchaSettingsCard />
          </CardContent>
        </Card>
      </div>
    </AdminPage>
  )
}

/** 常规设置：访问注册、租户开关、日志、更新与代理 */
export function AdminGeneralPage() {
  return (
    <AdminPage
      title="常规设置"
      description="访问与注册、租户开关、日志与订阅、在线更新与代理"
      icon={<Settings className="size-5" />}
    >
      <SettingsPanel section="general" />
    </AdminPage>
  )
}

/** 邮箱：SMTP 与邮箱策略 */
export function AdminEmailPage() {
  return (
    <AdminPage
      title="邮箱"
      description="SMTP 发信配置、注册验证、后缀白名单与自助换绑"
      icon={<Mail className="size-5" />}
    >
      <SettingsPanel section="email" />
    </AdminPage>
  )
}

/** 系统状态：运行状态、备份、在线更新 */
export function AdminSystemPage() {
  return (
    <AdminPage
      title="系统状态"
      description="服务运行状态、数据备份与在线更新"
      icon={<Cpu className="size-5" />}
    >
      <div className="space-y-6">
        <SystemPanel />
        <BackupCard />
        <UpdateCard />
      </div>
    </AdminPage>
  )
}

/** 兼容旧地址 /admin/manage：直接落到常规设置 */
export default function Admin() {
  return <AdminGeneralPage />
}

/* ---------- 手动清理 ---------- */

/**
 * CleanupButton 立即执行一次日志与统计清理。
 *
 * 定时任务每天跑一次，这个按钮给「现在就想腾空间」用。
 * 清理天数取上面保存的配置，所以改完保留天数要先保存再点。
 */
function CleanupButton() {
  const toast = useToast()
  const [result, setResult] = useState<CleanupResult | null>(null)

  const m = useMutation({
    mutationFn: runCleanup,
    onSuccess: (r) => {
      setResult(r)
      toast.success(
        '清理完成',
        r.total > 0
          ? `共删除 ${formatNumber(r.total)} 行，耗时 ${r.duration_ms}ms`
          : '没有需要清理的数据',
      )
    },
    onError: (err) => toast.error('清理失败', errorMessage(err)),
  })

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-background px-4 py-3">
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">立即清理过期数据</p>
        <p className="text-xs text-muted-foreground">
          {result
            ? `上次删除 ${formatNumber(result.total)} 行（统计保留 ${result.stats_retention_days} 天 / 日志保留 ${result.log_retention_min} 分钟）`
            : '按上面保存的保留天数清理统计与查询日志；定时任务每天自动跑一次'}
        </p>
      </div>
      <Button variant="outline" size="sm" loading={m.isPending} onClick={() => m.mutate()}>
        <Trash2 />
        立即清理
      </Button>
    </div>
  )
}

/* ---------- 账号安全 ---------- */

/**
 * AdminAccountPanel 承载管理员自己的账号安全。
 *
 * 管理员是控制面角色，不参与租户侧功能，所以不再给他一个「设置」页
 * （那页里全是接入 ID、外观、租户数据这类对他没意义的东西）。
 * 改密码和退出登录就放在这里。
 */
function AdminAccountPanel() {
  const { logout } = useAuth()
  return (
    <div className="space-y-6">
      <PasswordCard />
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <LogOut className="size-5 text-danger" />
            退出登录
          </CardTitle>
          <CardDescription>退出后需要重新登录才能继续管理。</CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="danger" onClick={logout}>
            <LogOut />
            退出登录
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}

/* ---------- 上游连通性自测 ---------- */

/**
 * UpstreamTester 逐个试解析上游，把「连不上 / 解析不出 / 正常」摊开。
 *
 * 为什么需要：DoH/DoT 上游配错、或引导 DNS 缺失时，保存设置是成功的，
 * 现象却是「所有查询都超时」。没有这个按钮，管理员只能改一处试一次。
 */
function UpstreamTester({ upstream, bootstrap }: { upstream: string; bootstrap: string }) {
  const [results, setResults] = useState<UpstreamTestResult[]>([])
  const [testing, setTesting] = useState(false)

  const list = upstream
    .split('\n')
    .map((v) => v.trim())
    .filter(Boolean)
  const boot = bootstrap
    .split('\n')
    .map((v) => v.trim())
    .filter(Boolean)

  const run = async () => {
    setTesting(true)
    setResults([])
    const out: UpstreamTestResult[] = []
    for (const up of list) {
      try {
        out.push(await testUpstream(up, boot))
      } catch (err) {
        out.push({ ok: false, upstream: up, error: errorMessage(err) })
      }
      setResults([...out])
    }
    setTesting(false)
  }

  return (
    <div className="space-y-3 rounded-xl border border-border/60 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="text-sm font-medium">上游连通性测试</p>
          <p className="text-xs text-muted-foreground">
            用当前填写的上游与引导 DNS 试解析 www.baidu.com（未保存也会用输入框里的值）
          </p>
        </div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={run}
          loading={testing}
          disabled={list.length === 0}
        >
          <PlugZap />
          测试全部
        </Button>
      </div>

      {results.length > 0 && (
        <div className="space-y-1.5">
          {results.map((r) => (
            <div
              key={r.upstream}
              className={cn(
                'flex flex-wrap items-center gap-2 rounded-lg px-2.5 py-2 text-xs',
                r.ok ? 'bg-success/10' : 'bg-danger/10',
              )}
            >
              {r.ok ? (
                <Check className="size-3.5 shrink-0 text-success" />
              ) : (
                <X className="size-3.5 shrink-0 text-danger" />
              )}
              <span className="min-w-0 flex-1 truncate font-mono" title={r.upstream}>
                {r.upstream}
              </span>
              {r.ok ? (
                <span className="tabular-nums text-muted-foreground">
                  {r.elapsed_ms}ms · {(r.answers ?? []).join(', ') || '无应答记录'}
                </span>
              ) : (
                <span className="min-w-0 text-danger">{r.error}</span>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

/* ---------- 出口代理实测 ---------- */

/**
 * ProxyTester 实测代理与直连两条链路，并把结论翻成一句可执行的话。
 *
 * 为什么两条都要测：订阅失败时管理员只知道「失败了」，无法区分是代理地址写错、
 * 代理本身不通、代理通了但源站挡它，还是压根该走直连。「代理不通但直连能通」
 * 和「两条都不通」的处置方式完全不同，只测代理等于只拿到一半结论。
 *
 * 测的是输入框里的值（未保存也能测），这样改完地址可以先验再存。
 */
function ProxyTester({ proxyURL, proxyEnabled }: { proxyURL: string; proxyEnabled: boolean }) {
  const toast = useToast()
  const [result, setResult] = useState<ProxyTestResult | null>(null)
  const [testing, setTesting] = useState(false)

  const run = async () => {
    setTesting(true)
    setResult(null)
    try {
      const res = await testProxy(proxyURL.trim(), proxyEnabled)
      setResult(res)
      if (!res.ok) {
        toast.error('代理配置有问题', res.reason ?? '未知原因')
        return
      }
      // 结论本身走 toast：它是「下一步做什么」，不该埋在卡片里被忽略。
      if (res.advice) {
        if (res.proxy_ok || res.direct_ok) toast.success('链路可用', res.advice)
        else toast.error('两条链路都不通', res.advice)
      }
    } catch (err) {
      toast.error('测试失败', errorMessage(err))
    } finally {
      setTesting(false)
    }
  }

  // 探针结果摊平成固定顺序，避免每行位置随对象键序跳动。
  const rows: { label: string; key: string }[] = [
    { label: '代理 · 出网', key: 'proxy:connectivity' },
    { label: '代理 · 订阅源', key: 'proxy:subscribe' },
    { label: '直连 · 出网', key: 'direct:connectivity' },
    { label: '直连 · 订阅源', key: 'direct:subscribe' },
  ]

  return (
    <div className="space-y-2 border-t border-border/60 pt-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          同时测代理与直连，用来判断订阅失败到底是代理问题还是源站问题
        </p>
        <Button type="button" variant="secondary" size="sm" onClick={run} loading={testing}>
          <PlugZap />
          测试代理
        </Button>
      </div>

      {result && !result.ok && (
        <div className="flex items-start gap-2 rounded-lg bg-danger/10 px-2.5 py-2 text-xs text-danger">
          <X className="mt-0.5 size-3.5 shrink-0" />
          <span className="min-w-0">{result.reason}</span>
        </div>
      )}

      {result?.ok && (
        <div className="space-y-1.5">
          {result.advice && (
            <div
              className={cn(
                'flex items-start gap-2 rounded-lg px-2.5 py-2 text-xs',
                result.proxy_ok || result.direct_ok ? 'bg-success/10 text-success' : 'bg-danger/10 text-danger',
              )}
            >
              {result.proxy_ok || result.direct_ok ? (
                <Check className="mt-0.5 size-3.5 shrink-0" />
              ) : (
                <X className="mt-0.5 size-3.5 shrink-0" />
              )}
              <span className="min-w-0">{result.advice}</span>
            </div>
          )}
          {rows.map(({ label, key }) => {
            const p = result.probes?.[key]
            // 未填代理时后端不跑代理探针，这一行直接不显示，避免一堆「未测试」。
            if (!p) return null
            return (
              <div
                key={key}
                className={cn(
                  'flex flex-wrap items-center gap-2 rounded-lg px-2.5 py-1.5 text-xs',
                  p.ok ? 'bg-success/10' : 'bg-danger/10',
                )}
              >
                {p.ok ? (
                  <Check className="size-3.5 shrink-0 text-success" />
                ) : (
                  <X className="size-3.5 shrink-0 text-danger" />
                )}
                <span className="w-24 shrink-0 text-muted-foreground">{label}</span>
                {p.ok ? (
                  <span className="tabular-nums text-muted-foreground">
                    {p.latency_ms}ms · HTTP {p.status}
                  </span>
                ) : (
                  <span className="min-w-0 flex-1 break-words text-danger">{p.error}</span>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

/* ---------- 系统备份 ---------- */

/**
 * BackupCard 一键下载整站备份。
 *
 * 内容：数据库（VACUUM INTO 一致性快照）、config.yaml、证书目录。
 * 数据库是 WAL 模式，直接拷主文件会漏掉未 checkpoint 的部分 ——
 * 那种「备份成功但恢复后数据是旧的」比没有备份更危险。
 */
function BackupCard() {
  const toast = useToast()
  const [busy, setBusy] = useState(false)

  const run = async () => {
    setBusy(true)
    try {
      await downloadBackup()
      toast.success('备份已开始下载', '包含数据库、配置与证书，请妥善保管')
    } catch (err) {
      toast.error('备份失败', errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <DatabaseBackup className="size-5 text-primary" />
          系统备份
        </CardTitle>
        <CardDescription>
          打包下载数据库快照、配置与证书。恢复方式见包内 README。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <Button variant="outline" onClick={run} loading={busy}>
          <Download />
          下载备份包
        </Button>
        <p className="text-xs text-muted-foreground">
          包内含用户数据与证书私钥（含私钥才能原地恢复），请按凭据管理，不要放公开位置。
        </p>
      </CardContent>
    </Card>
  )
}

/**
 * EmailTester 发一封测试邮件。
 *
 * 单独一个按钮而不是「保存时顺便测」：SMTP 出错的原因很多（端口、加密方式、
 * 授权码、发件人域名校验），必须让管理员能在改完后立刻单独验证这一环，
 * 否则用户收不到验证码时根本分不清是配置错还是别的问题。
 */
function EmailTester({ defaultTo, enabled }: { defaultTo: string; enabled: boolean }) {
  const toast = useToast()
  const [to, setTo] = useState('')
  const target = to.trim() || defaultTo

  const m = useMutation({
    mutationFn: () => testEmail(target),
    onSuccess: (res) => toast.success('测试邮件已发出', res.message ?? `请查收 ${target}`),
    onError: (err) => toast.error('发送失败', errorMessage(err)),
  })

  return (
    <div className="space-y-1.5">
      <div className="flex gap-2">
        <Input
          placeholder="收件邮箱（留空则发给发件人自己）"
          value={to}
          onChange={(e) => setTo(e.target.value)}
          className="min-w-0 flex-1"
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={!enabled || !target}
          loading={m.isPending}
          onClick={() => m.mutate()}
        >
          <Send />
          发送测试
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        测试用的是已保存的配置，改完记得先点保存再测试。
      </p>
    </div>
  )
}
