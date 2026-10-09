import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Activity,
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
} from '@/api/admin'
import { errorMessage } from '@/api/client'
import type { AdminSettings, User } from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
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
import { formatDateTime, formatUptime } from '@/lib/format'

/* ---------- 用户管理 ---------- */

function UsersPanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const [page, setPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [q, setQ] = useState('')
  const [pendingDelete, setPendingDelete] = useState<User | null>(null)
  const pageSize = 20

  const usersQuery = useQuery({
    queryKey: ['admin', 'users', page, q],
    queryFn: () => listUsers({ page, page_size: pageSize, q: q || undefined }),
  })

  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'users'] })

  const updateMutation = useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: { enabled?: boolean; role?: 'admin' | 'user' } }) =>
      updateUser(id, patch),
    onSuccess: () => {
      toast.success('用户已更新')
      invalidate()
    },
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteUser(id),
    onSuccess: () => {
      toast.success('用户已删除')
      setPendingDelete(null)
      invalidate()
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const items = usersQuery.data?.items ?? []
  const total = usersQuery.data?.total ?? 0

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            <Users className="size-5 text-primary" />
            用户管理
          </CardTitle>
          <CardDescription>启用 / 禁用账号、调整角色或删除用户。</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center gap-2">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  setQ(searchInput.trim())
                  setPage(1)
                }
              }}
              placeholder="搜索用户名或邮箱…"
              className="pl-9"
            />
          </div>
          <Button
            variant="secondary"
            onClick={() => {
              setQ(searchInput.trim())
              setPage(1)
            }}
          >
            搜索
          </Button>
        </div>

        {usersQuery.isError ? (
          <ErrorState message={errorMessage(usersQuery.error)} onRetry={() => usersQuery.refetch()} />
        ) : usersQuery.isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-14 rounded-xl" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState icon={<Users className="size-6" />} title="没有找到用户" />
        ) : (
          <>
            <div className="rounded-2xl border border-border/60">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>用户名</TableHead>
                    <TableHead className="hidden md:table-cell">邮箱</TableHead>
                    <TableHead className="hidden lg:table-cell">客户端 ID</TableHead>
                    <TableHead className="w-28">角色</TableHead>
                    <TableHead className="w-20">启用</TableHead>
                    <TableHead className="hidden md:table-cell w-32">注册时间</TableHead>
                    <TableHead className="w-16 text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((u) => (
                    <TableRow key={u.id}>
                      <TableCell className="font-medium">{u.username}</TableCell>
                      <TableCell className="hidden md:table-cell text-sm text-muted-foreground">
                        {u.email || '—'}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <code className="font-mono text-xs">{u.client_id}</code>
                      </TableCell>
                      <TableCell>
                        <Select
                          value={u.role}
                          onValueChange={(v) => updateMutation.mutate({ id: u.id, patch: { role: v as 'admin' | 'user' } })}
                        >
                          <SelectTrigger className="h-8 w-24 text-xs">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="user">用户</SelectItem>
                            <SelectItem value="admin">管理员</SelectItem>
                          </SelectContent>
                        </Select>
                      </TableCell>
                      <TableCell>
                        <Switch
                          checked={u.enabled}
                          onCheckedChange={(v) => updateMutation.mutate({ id: u.id, patch: { enabled: v } })}
                          aria-label="启用"
                        />
                      </TableCell>
                      <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                        {formatDateTime(u.created_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            className="text-danger hover:bg-danger/10"
                            onClick={() => setPendingDelete(u)}
                            aria-label="删除"
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <Pagination page={page} pageSize={pageSize} total={total} onPageChange={setPage} />
          </>
        )}
      </CardContent>

      <ConfirmDialog
        open={Boolean(pendingDelete)}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="删除用户"
        description={
          pendingDelete
            ? `确定要删除用户「${pendingDelete.username}」吗？其规则、订阅与统计数据都会被清除。`
            : ''
        }
        destructive
        confirmText="删除"
        loading={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />
    </Card>
  )
}

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
        </TabsList>

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
