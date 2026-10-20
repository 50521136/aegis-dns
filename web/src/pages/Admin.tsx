import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Activity,
  BarChart3,
  Check,
  Cpu,
  Database,
  HardDriveDownload,
  KeyRound,
  LogOut,
  PlugZap,
  Save,
  Search,
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
import { errorMessage } from '@/api/client'
import { getAdminUserTraffic, type AdminStatsRange } from '@/api/adminStats'
import type { AdminSettings, User } from '@/api/types'
import { cn } from '@/lib/utils'
import { PageHeader } from '@/components/PageHeader'
import { testUpstream, type UpstreamTestResult } from '@/api/admin'
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

/* ---------- 用户管理 ---------- */


/* ---------- 全局设置 ---------- */

const settingsSchema = z.object({
  domain: z.string().min(1, '请输入主域名'),
  upstreams: z.string(),
  bootstrap_dns: z.string(),
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
  query_log_retention_days: z.coerce.number().int().min(1),
  allow_register: z.boolean(),
  invite_code: z.string(),
  update_repo: z.string(),
  update_channel: z.enum(['stable', 'beta']),

  allow_root_domain: z.boolean(),
  stats_retention_days: z.coerce.number().int().min(0),
  max_id_resets: z.coerce.number().int().min(0),
  allow_user_rules: z.boolean(),
  allow_user_subs: z.boolean(),
  max_rules_per_user: z.coerce.number().int().min(0),
  max_subs_per_user: z.coerce.number().int().min(0),
  max_sub_entries: z.coerce.number().int().min(0),
  max_rules_global: z.coerce.number().int().min(0),
  max_subs_global: z.coerce.number().int().min(0),
})

type SettingsForm = z.infer<typeof settingsSchema>

function SettingsPanel() {
  const qc = useQueryClient()
  const toast = useToast()

  const settingsQuery = useQuery({ queryKey: ['admin', 'settings'], queryFn: getSettings })

  const form = useForm<SettingsForm>({ resolver: zodResolver(settingsSchema) })

  // 数据加载完成后填充表单
  const [hydrated, setHydrated] = useState(false)
  if (settingsQuery.data && !hydrated) {
    const s = settingsQuery.data
    form.reset({
      ...s,
      upstreams: (s.upstreams ?? []).join('\n'),
      bootstrap_dns: (s.bootstrap_dns ?? []).join('\n'),
    } as unknown as SettingsForm)
    setHydrated(true)
  }

  const saveMutation = useMutation({
    mutationFn: (values: SettingsForm) => {
      const payload: Partial<AdminSettings> = {
        ...values,
        bootstrap_dns: values.bootstrap_dns
          .split('\n')
          .map((v) => v.trim())
          .filter(Boolean),
        upstreams: values.upstreams
          .split('\n')
          .map((u) => u.trim())
          .filter(Boolean),
      }
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

  return (
    <form onSubmit={form.handleSubmit((v) => saveMutation.mutate(v))} className="space-y-6">
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
            <Label htmlFor="upstreams">上游 DNS（每行一个）</Label>
            <textarea
              id="upstreams"
              {...form.register('upstreams')}
              className="min-h-[100px] w-full rounded-xl border border-border bg-background px-3.5 py-2.5 font-mono text-[13px] focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/30"
              placeholder={'1.1.1.1\n8.8.8.8\nhttps://dns.google/dns-query'}
            />
            <p className="text-xs text-muted-foreground">支持 IP、udp://、tls://、https:// 形式。</p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="bootstrap_dns">引导 DNS（每行一个 IP）</Label>
            <textarea
              id="bootstrap_dns"
              {...form.register('bootstrap_dns')}
              className="min-h-[70px] w-full rounded-xl border border-border bg-background px-3.5 py-2.5 font-mono text-[13px] focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/30"
              placeholder={'223.5.5.5\n119.29.29.29'}
            />
            <p className="text-xs text-muted-foreground">
              只填 IP：DoH/DoT 上游要先解析主机名才能连，而解析又依赖它，所以不能用域名做引导。
              留空则回退系统解析器（容器里经常没有，会表现为「上游全部超时」）。
            </p>
          </div>

          <UpstreamTester
            upstream={form.watch('upstreams')}
            bootstrap={form.watch('bootstrap_dns')}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>拦截与兜底策略</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label>拦截应答模式</Label>
            <Select
              value={form.watch('blocking_mode')}
              onValueChange={(v) => form.setValue('blocking_mode', v as SettingsForm['blocking_mode'])}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="null_ip">返回 0.0.0.0 / ::（推荐）</SelectItem>
                <SelectItem value="nxdomain">返回 NXDOMAIN</SelectItem>
                <SelectItem value="refused">返回 REFUSED</SelectItem>
                <SelectItem value="custom_ip">返回自定义 IP</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>自定义拦截 IP</Label>
            <Input
              className="font-mono"
              placeholder="仅 custom_ip 模式生效"
              disabled={form.watch('blocking_mode') !== 'custom_ip'}
              {...form.register('custom_ip')}
            />
          </div>
          <div className="space-y-2 sm:col-span-2">
            <Label>未识别请求兜底策略</Label>
            <Select
              value={form.watch('fallback_policy')}
              onValueChange={(v) => form.setValue('fallback_policy', v as SettingsForm['fallback_policy'])}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="passthrough">passthrough —— 直接放行</SelectItem>
                <SelectItem value="refused">refused —— 返回 REFUSED</SelectItem>
                <SelectItem value="blocklist_only">blocklist_only —— 仅应用全局黑名单</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>缓存与性能</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <NumberField form={form} name="cache_size" label="缓存容量（条）" />
          <NumberField form={form} name="cache_min_ttl" label="最小 TTL（秒）" />
          <NumberField form={form} name="cache_max_ttl" label="最大 TTL（秒）" />
          <NumberField form={form} name="rate_limit_qps" label="限流 QPS" />
          <NumberField form={form} name="max_inflight" label="最大并发查询" />
          <NumberField form={form} name="query_timeout_ms" label="查询超时（ms）" />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>日志与订阅</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <NumberField form={form} name="query_log_size" label="查询日志缓冲（条）" />
          <NumberField form={form} name="query_log_retention_days" label="日志保留（天）" />
          <NumberField form={form} name="stats_retention_days" label="统计保留（天，0=不清理）" />
          <NumberField form={form} name="sub_interval_hours" label="订阅刷新间隔（小时）" />
          <div className="flex items-center justify-between rounded-xl border border-border bg-background px-4 py-3 sm:col-span-2 lg:col-span-3">
            <div>
              <p className="text-sm font-medium">启用查询日志</p>
              <p className="text-xs text-muted-foreground">关闭可降低内存与磁盘占用</p>
            </div>
            <Switch
              checked={form.watch('enable_query_log')}
              onCheckedChange={(v) => form.setValue('enable_query_log', v)}
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>域名与接入</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
            <div className="min-w-0">
              <p className="text-sm font-medium">允许使用主域名</p>
              <p className="text-xs text-muted-foreground">
                开启后主域名本身也能解析（只套全局规则）；关闭则只认
                &lt;用户ID&gt;.主域名 这种子域名
              </p>
            </div>
            <Switch
              checked={form.watch('allow_root_domain')}
              onCheckedChange={(v) => form.setValue('allow_root_domain', v)}
            />
          </div>
          <NumberField form={form} name="max_id_resets" label="用户自助重置 ID 次数（0=禁止）" />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>配额与租户开关</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <NumberField form={form} name="max_rules_per_user" label="每用户规则上限（0=不限）" />
            <NumberField form={form} name="max_subs_per_user" label="每用户订阅上限（0=不限）" />
            <NumberField form={form} name="max_sub_entries" label="单订阅条目上限" />
            <NumberField form={form} name="max_rules_global" label="全局规则上限" />
            <NumberField form={form} name="max_subs_global" label="全局订阅上限" />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex items-center justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
              <div className="min-w-0">
                <p className="text-sm font-medium">允许用户加规则</p>
                <p className="text-xs text-muted-foreground">关闭后普通用户不能新建个人规则</p>
              </div>
              <Switch
                checked={form.watch('allow_user_rules')}
                onCheckedChange={(v) => form.setValue('allow_user_rules', v)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 rounded-xl border border-border bg-background px-4 py-3">
              <div className="min-w-0">
                <p className="text-sm font-medium">允许用户加订阅</p>
                <p className="text-xs text-muted-foreground">关闭后普通用户不能新建个人订阅</p>
              </div>
              <Switch
                checked={form.watch('allow_user_subs')}
                onCheckedChange={(v) => form.setValue('allow_user_subs', v)}
              />
            </div>
          </div>
          <CleanupButton />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>注册与更新</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="flex items-center justify-between rounded-xl border border-border bg-background px-4 py-3 sm:col-span-2">
            <div>
              <p className="text-sm font-medium">开放注册</p>
              <p className="text-xs text-muted-foreground">关闭后仅管理员可创建账号</p>
            </div>
            <Switch
              checked={form.watch('allow_register')}
              onCheckedChange={(v) => form.setValue('allow_register', v)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="invite_code">邀请码</Label>
            <Input id="invite_code" className="font-mono" placeholder="留空表示无需邀请码" {...form.register('invite_code')} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="update_repo">更新仓库</Label>
            <Input id="update_repo" className="font-mono" placeholder="owner/repo" {...form.register('update_repo')} />
          </div>
          <div className="space-y-2">
            <Label>更新渠道</Label>
            <Select
              value={form.watch('update_channel')}
              onValueChange={(v) => form.setValue('update_channel', v as SettingsForm['update_channel'])}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stable">stable</SelectItem>
                <SelectItem value="beta">beta</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardContent>
      </Card>

      <div className="flex justify-end gap-3">
        <Button type="button" variant="outline" onClick={() => settingsQuery.refetch()}>
          重置
        </Button>
        <Button type="submit" loading={saveMutation.isPending}>
          <Save />
          保存设置
        </Button>
      </div>
    </form>
  )
}

function NumberField({
  form,
  name,
  label,
}: {
  form: ReturnType<typeof useForm<SettingsForm>>
  name: keyof SettingsForm
  label: string
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={String(name)}>{label}</Label>
      <Input id={String(name)} type="number" className="tabular-nums" {...form.register(name)} />
    </div>
  )
}

/* ---------- 系统状态 ---------- */

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
                <div key={k} className="flex items-center justify-between border-b border-border/50 py-2 text-sm last:border-0">
                  <span className="text-muted-foreground">{k}</span>
                  <span className="font-mono">{String(v)}</span>
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

export default function Admin() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="管理后台"
        description="用户、全局设置、系统状态与账号安全"
        icon={<ShieldCheck className="size-5" />}
      />

      <Tabs defaultValue="users">
        <TabsList className="flex-wrap">
          <TabsTrigger value="users">
            <Users className="size-4" />
            用户
          </TabsTrigger>
          <TabsTrigger value="settings">
            <Activity className="size-4" />
            全局设置
          </TabsTrigger>
          <TabsTrigger value="system">
            <Cpu className="size-4" />
            系统状态
          </TabsTrigger>
          <TabsTrigger value="account">
            <KeyRound className="size-4" />
            账号安全
          </TabsTrigger>
        </TabsList>

        <TabsContent value="users">
          <UsersPanel />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsPanel />
        </TabsContent>
        <TabsContent value="system">
          <div className="space-y-6">
            <SystemPanel />
            {/* 在线更新是运维动作，放在系统状态里而不是租户设置页 */}
            <UpdateCard />
          </div>
        </TabsContent>
        <TabsContent value="account">
          <AdminAccountPanel />
        </TabsContent>
      </Tabs>
    </div>
  )
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
            ? `上次删除 ${formatNumber(result.total)} 行（统计保留 ${result.stats_retention_days} 天 / 日志保留 ${result.log_retention_days} 天）`
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
