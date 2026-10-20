import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Activity,
  BarChart3,
  Cpu,
  Database,
  HardDriveDownload,
  Save,
  Search,
  ShieldCheck,
  Trash2,
  Users,
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
    } as unknown as SettingsForm)
    setHydrated(true)
  }

  const saveMutation = useMutation({
    mutationFn: (values: SettingsForm) => {
      const payload: Partial<AdminSettings> = {
        ...values,
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
        description="用户、全局设置与系统状态"
        icon={<ShieldCheck className="size-5" />}
      />

      <Tabs defaultValue="traffic">
        <TabsList className="flex-wrap">
          <TabsTrigger value="traffic">
            <BarChart3 className="size-4" />
            流量排行
          </TabsTrigger>
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
        </TabsList>

        <TabsContent value="traffic">
          <TrafficPanel />
        </TabsContent>
        <TabsContent value="users">
          <UsersPanel />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsPanel />
        </TabsContent>
        <TabsContent value="system">
          <SystemPanel />
        </TabsContent>
      </Tabs>
    </div>
  )
}

/* ---------- 流量排行 ---------- */

const TRAFFIC_RANGES: { value: AdminStatsRange; label: string }[] = [
  { value: '1h', label: '近 1 小时' },
  { value: '24h', label: '近 24 小时' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
]

/**
 * TrafficPanel 展示每个用户的请求量排行。
 *
 * 数据来自 query_rollup 按 user_id 聚合，管理员用它判断谁在重度使用、
 * 谁的拦截率异常（拦截率接近 1 说明规则把它全拦了，八成是配错了）。
 */
function TrafficPanel() {
  const [range, setRange] = useState<AdminStatsRange>('24h')

  const q = useQuery({
    queryKey: ['admin', 'stats', 'users', range],
    queryFn: () => getAdminUserTraffic(range, 20),
    refetchInterval: 30000,
  })

  const items = q.data?.items ?? []
  const grandTotal = items.reduce((a, b) => a + b.total, 0)
  const max = items.length > 0 ? items[0].total : 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <BarChart3 className="size-5 text-primary" />
          用户请求量排行
        </CardTitle>
        <CardDescription>
          {q.data ? `共 ${items.length} 个用户有流量，合计 ${formatNumber(grandTotal)} 次查询` : '按解析请求量排序'}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="m-groove inline-flex flex-wrap gap-1 rounded-xl p-1">
          {TRAFFIC_RANGES.map((r) => (
            <button
              key={r.value}
              onClick={() => setRange(r.value)}
              className={cn(
                'rounded-lg px-3 py-1.5 text-sm transition-colors',
                range === r.value
                  ? 'm-pill font-medium text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {r.label}
            </button>
          ))}
        </div>

        {q.isError ? (
          <ErrorState message={errorMessage(q.error)} onRetry={() => q.refetch()} />
        ) : q.isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-14 rounded-xl" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<BarChart3 className="size-6" />}
            title="还没有流量数据"
            description="用户产生解析请求后，排行会显示在这里。"
          />
        ) : (
          <ol className="space-y-2">
            {items.map((u, i) => {
              const pct = max > 0 ? Math.round((u.total / max) * 100) : 0
              return (
                <li
                  key={u.user_id}
                  className="flex items-center gap-3 rounded-xl border border-border/60 px-3 py-2.5"
                >
                  {/* 名次：前三名用主色强调 */}
                  <span
                    className={cn(
                      'readout w-7 shrink-0 text-center text-sm',
                      i === 0 ? 'text-primary' : i < 3 ? 'text-foreground' : 'text-muted-foreground',
                    )}
                  >
                    {i + 1}
                  </span>

                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline justify-between gap-2">
                      <span className="truncate text-sm font-medium">
                        {u.username || '已删除用户'}
                      </span>
                      <span className="shrink-0 font-mono text-sm tabular-nums">
                        {formatNumber(u.total)}
                      </span>
                    </div>
                    {/* 占比条 + 拦截率 */}
                    <div className="mt-1 flex items-center gap-2">
                      <div className="h-1 flex-1 overflow-hidden rounded-full bg-muted">
                        <div
                          className="h-full rounded-full bg-primary"
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                      <span className="shrink-0 text-2xs text-muted-foreground">
                        拦截 {Math.round(u.block_rate * 100)}%
                      </span>
                    </div>
                  </div>
                </li>
              )
            })}
          </ol>
        )}
      </CardContent>
    </Card>
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
