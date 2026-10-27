import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  Clock,
  Info,
  ListPlus,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Shield,
  Trash2,
} from 'lucide-react'
import {
  createSubscription,
  deleteSubscription,
  listPresets,
  listSubscriptions,
  refreshAllSubscriptions,
  refreshSubscription,
  updateSubscription,
} from '@/api/subscriptions'
import { errorMessage } from '@/api/client'
import { lookupGlobalSubByURL } from '@/api/notifications'
import type {
  ListType,
  Scope,
  SubFormat,
  Subscription,
  SubscriptionInput,
} from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
import { GlobalLayerBanner, GlobalLayerHint, QuotaReadout } from '@/components/UserLayerHints'
import { DuplicateSubscriptionsCard } from '@/components/DuplicateSubscriptionsCard'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { formatNumber, formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'

const schema = z.object({
  name: z.string().min(1, '请输入订阅名称'),
  url: z.string().url('请输入有效的订阅 URL'),
  list_type: z.enum(['blocklist', 'allowlist']),
  format: z.enum(['auto', 'adblock', 'hosts', 'dnsmasq', 'domain']),
  enabled: z.boolean(),
  // 0 = 跟随全局默认超时。上限 600 与后端校验一致。
  timeout_sec: z.coerce.number().int().min(0).max(600),
})

type SubForm = z.infer<typeof schema>

const FORMAT_LABEL: Record<SubFormat, string> = {
  auto: '自动识别',
  adblock: 'Adblock',
  hosts: 'Hosts',
  dnsmasq: 'DNSMasq',
  domain: '域名列表',
}

/**
 * 解析 last_status 里的「短码 | 详情」。
 *
 * 后端约定格式为 `error: <短码> | <详情>`：短码给徽章（必须短，否则会把列表
 * 挤变形），详情给 toast。老数据只有 `error: <很长的一段>`，没有竖线 ——
 * 那种情况把整段当详情、短码统一显示「失败」，仍然可用。
 */
function splitStatus(status: string): { code: string; detail: string } {
  const body = status.replace(/^error:\s*/, '')
  const idx = body.indexOf(' | ')
  if (idx < 0) return { code: '失败', detail: body }
  return { code: body.slice(0, idx), detail: body.slice(idx + 3) }
}

/**
 * StatusBadge 只显示短码，完整原因点击后在 toast 里展开。
 *
 * 为什么不再把错误原文塞进徽章：真实报错动辄上百字（还带代理/直连两段原因），
 * 徽章被撑到几百像素，把整行布局挤出去，结果「想看的信息看不见、其它列也被
 * 挤没了」。短码保证布局稳定，详情按需展开。
 */
function StatusBadge({ status }: { status: string }) {
  const toast = useToast()

  if (!status) return <Badge variant="secondary">未同步</Badge>
  // 后台拉取中：公共规则源在慢线路上要 60~90 秒，必须让用户看到「在下载」，
  // 否则会以为是坏了、反复点刷新。
  if (status.startsWith('pending'))
    return (
      <Badge variant="default">
        <Loader2 className="size-3 animate-spin" />
        同步中
      </Badge>
    )
  if (status === 'ok' || status.startsWith('ok'))
    return (
      <Badge variant="success">
        <CheckCircle2 className="size-3" />
        正常
      </Badge>
    )

  const { code, detail } = splitStatus(status)
  return (
    <button
      type="button"
      className="inline-flex max-w-full items-center"
      title="点击查看失败原因"
      onClick={() => toast.error(`同步失败：${code}`, detail)}
    >
      <Badge variant="danger" className="cursor-pointer">
        <AlertTriangle className="size-3 shrink-0" />
        <span className="max-w-[12rem] truncate">{code}</span>
        <Info className="size-3 shrink-0 opacity-70" />
      </Badge>
    </button>
  )
}

// 与 Rules 同理：scope='global' 时就是管理员的「全局订阅」页。
export default function Subscriptions({ scope = 'user' }: { scope?: Scope }) {
  const isGlobal = scope === 'global'
  const qc = useQueryClient()
  const toast = useToast()

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<Subscription | null>(null)
  const [presetsOpen, setPresetsOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<Subscription | null>(null)
  const [refreshingId, setRefreshingId] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: ['subscriptions', scope],
    queryFn: () => listSubscriptions(scope),
    // 后台同步期间每 3 秒轮询一次，全部同步完自动停止 —— 否则用户得手动刷新
    // 才知道下载好了没有。
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((x) => x.last_status?.startsWith('pending'))
        ? 3000
        : false,
  })
  const presetsQuery = useQuery({
    queryKey: ['subscriptions', 'presets'],
    queryFn: listPresets,
    enabled: presetsOpen,
  })

  // 已经在列表里的源不能再加一次：同一份列表重复导入只会白占配额、
  // 把订阅列表撑得看不出重点，而用户点下去之前是不知道的。
  const existingUrls = new Set((listQuery.data?.items ?? []).map((s) => s.url))

  // 全局订阅查重（需求）：用户填完 URL 就查一次，命中就明确告诉他
  // 「全局订阅已有此规则，无需重复添加」，并禁用提交按钮。
  //
  // 这只是**提示**，真正的拦截在服务端（createSub 里再查一次）：
  // 光靠前端挡不住直接调 API，而重复添加会让用户的快照里同一份域名翻倍。
  const [globalHit, setGlobalHit] = useState<{ name: string; entries?: number } | null>(null)
  const [checkingGlobal, setCheckingGlobal] = useState(false)

  const items = listQuery.data?.items ?? []
  const limit = listQuery.data?.limit ?? 0

  const form = useForm<SubForm>({
    resolver: zodResolver(schema),
    defaultValues: { name: '', url: '', list_type: 'blocklist', format: 'auto', enabled: true, timeout_sec: 0 },
  })

  const openCreate = (preset?: { name: string; url: string; list_type: ListType }) => {
    setEditing(null)
    form.reset({
      name: preset?.name ?? '',
      url: preset?.url ?? '',
      list_type: preset?.list_type ?? 'blocklist',
      format: 'auto',
      enabled: true,
      timeout_sec: 0,
    })
    setGlobalHit(null)
    setEditorOpen(true)
  }

  const openEdit = (sub: Subscription) => {
    setEditing(sub)
    form.reset({
      name: sub.name,
      url: sub.url,
      list_type: sub.list_type,
      format: sub.format,
      enabled: sub.enabled,
      timeout_sec: sub.timeout_sec ?? 0,
    })
    setGlobalHit(null)
    setEditorOpen(true)
  }

  const invalidate = () => qc.invalidateQueries({ queryKey: ['subscriptions'] })

  const saveMutation = useMutation({
    mutationFn: (values: SubForm) => {
      const payload: SubscriptionInput = values
      return editing
        ? updateSubscription(editing.id, payload, scope)
        : createSubscription(payload, scope)
    },
    onSuccess: () => {
      toast.success(editing ? '订阅已更新' : '订阅已添加')
      setEditorOpen(false)
      invalidate()
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteSubscription(id, scope),
    onSuccess: () => {
      toast.success('订阅已删除')
      setPendingDelete(null)
      invalidate()
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const refreshMutation = useMutation({
    mutationFn: (id: string) => refreshSubscription(id, scope),
    onMutate: (id) => setRefreshingId(id),
    onSettled: () => setRefreshingId(null),
    onSuccess: (res) => {
      // 后台拉取，接口立刻回 202，结果靠列表轮询体现
      if (res.pending || res.last_status?.startsWith('pending')) {
        toast.info('已开始同步', '后台下载中，条目数会自动更新')
      } else if (res.last_status === 'ok' || res.last_status?.startsWith('ok')) {
        toast.success('刷新完成', `拉取 ${formatNumber(res.last_count ?? 0)} 条`)
      } else {
        toast.warning('刷新未成功', res.last_status || '未知状态')
      }
      invalidate()
    },
    onError: (err) => toast.error('刷新失败', errorMessage(err)),
  })

  const refreshAllMutation = useMutation({
    mutationFn: () => refreshAllSubscriptions(scope),
    onSuccess: (res) => {
      toast.info('已开始同步', `${res.pending ?? 0} 个订阅在后台下载中`)
      invalidate()
    },
    onError: (err) => toast.error('刷新失败', errorMessage(err)),
  })

  const toggleEnabled = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      updateSubscription(id, { enabled }, scope),
    onSuccess: () => invalidate(),
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title={isGlobal ? '全局订阅' : '订阅管理'}
        description="订阅外部规则列表，系统定时自动更新，失败时保留上次规则"
        icon={<Shield className="size-5" />}
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              onClick={() => refreshAllMutation.mutate()}
              loading={refreshAllMutation.isPending}
            >
              <RefreshCw />
              全部刷新
            </Button>
            <Button size="sm" onClick={() => openCreate()}>
              <Plus />
              添加订阅
            </Button>
            {isGlobal && (
              <Button variant="secondary" size="sm" onClick={() => setPresetsOpen(true)}>
                <ListPlus />
                推荐列表
              </Button>
            )}
          </>
        }
      />

      {/* 订阅配额 + 全局层状态。
          之前这一页完全看不到「还能加几个订阅」，用户只有点「添加订阅」
          提交之后才知道已经满了。 */}
      {/* 订阅页的空白最容易诱发重复添加：用户看到「还没有订阅」就以为没防护。
          先把「管理员已内置多少」摆出来，再谈他自己的配额。 */}
      {!isGlobal && <GlobalLayerBanner />}

      <div className="flex flex-wrap items-center gap-3">
        {!isGlobal && <GlobalLayerHint />}
        <QuotaReadout used={items.length} limit={limit} unit="个订阅" />
      </div>

      {/* 重复检测只给管理员：它能跨租户看到「谁和谁订了同一份」，
          这对普通用户没有意义，也不该暴露别人的订阅。 */}
      {isGlobal && <DuplicateSubscriptionsCard />}

      {listQuery.isError ? (
        <ErrorState message={errorMessage(listQuery.error)} onRetry={() => listQuery.refetch()} />
      ) : listQuery.isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-40 rounded-2xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<Shield className="size-6" />}
          title="还没有订阅"
          description={
            isGlobal
              ? '从推荐列表一键添加常用的广告拦截或白名单订阅源。'
              : '粘贴一个订阅地址即可，支持 adblock / hosts / 域名列表格式。'
          }
          action={
            <Button onClick={() => (isGlobal ? setPresetsOpen(true) : openCreate())}>
              <ListPlus />
              {isGlobal ? '浏览推荐订阅' : '添加订阅'}
            </Button>
          }
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {items.map((sub) => (
            <div
              key={sub.id}
              className="flex flex-col rounded-2xl border border-border/50 bg-card p-4 shadow-sm transition-all duration-200 hover:shadow-md sm:p-5"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="truncate text-base font-semibold" title={sub.name}>
                      {sub.name}
                    </p>
                    <Badge variant={sub.list_type === 'allowlist' ? 'success' : 'danger'}>
                      {sub.list_type === 'allowlist' ? '白名单' : '黑名单'}
                    </Badge>
                    {/* 收录来源标记：让管理员一眼分辨「这条全局规则是自己建的
                        还是被自动收录 / 手动合并上来的」。排查「突然全站多了
                        一批拦截」时这一步省掉大量翻日志的时间。 */}
                    {sub.promoted_at && (
                      <Badge variant="secondary" className="shrink-0" title={`收录时间 ${sub.promoted_at}`}>
                        收录
                      </Badge>
                    )}
                  </div>
                  <p
                    className="mt-1 line-clamp-2 break-all font-mono text-xs leading-snug text-muted-foreground"
                    title={sub.url}
                  >
                    {sub.url}
                  </p>
                </div>
                <Switch
                  checked={sub.enabled}
                  onCheckedChange={(v) => toggleEnabled.mutate({ id: sub.id, enabled: v })}
                  aria-label="启用订阅"
                />
              </div>

              <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
                <span className="flex items-center gap-1.5">
                  <span className="text-muted-foreground">条目</span>
                  <span className="font-semibold tabular-nums">{formatNumber(sub.last_count)}</span>
                </span>
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Clock className="size-3.5" />
                  {sub.last_fetched ? formatRelative(sub.last_fetched) : '从未同步'}
                </span>
                <StatusBadge status={sub.last_status} />
              </div>

              <div className="mt-3 flex items-center justify-end gap-1 border-t border-border/60 pt-2.5">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => refreshMutation.mutate(sub.id)}
                  loading={refreshingId === sub.id}
                >
                  {refreshingId !== sub.id && <RefreshCw />}
                  刷新
                </Button>
                <Button variant="ghost" size="icon-sm" onClick={() => openEdit(sub)} aria-label="编辑">
                  <Pencil />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setPendingDelete(sub)}
                  aria-label="删除"
                  className="text-danger hover:bg-danger/10"
                >
                  <Trash2 />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* 新建 / 编辑 */}
      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent className="md:max-w-lg">
          <DialogHeader>
            <DialogTitle>{editing ? '编辑订阅' : '添加订阅'}</DialogTitle>
            <DialogDescription>订阅源会按设定间隔自动拉取并合并到你的规则集。</DialogDescription>
          </DialogHeader>
          <form onSubmit={form.handleSubmit((v) => saveMutation.mutate(v))} className="space-y-4" noValidate>
            <div className="space-y-2">
              <Label htmlFor="name">名称</Label>
              <Input id="name" placeholder="例如 AdGuard DNS Filter" {...form.register('name')} />
              {form.formState.errors.name && (
                <p className="text-xs text-danger">{form.formState.errors.name.message}</p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="url">订阅 URL</Label>
              <Input
                id="url"
                placeholder="https://example.com/filter.txt"
                className="font-mono text-sm"
                {...form.register('url', {
                  // 离开输入框时查一次全局订阅。放在 blur 而不是 onChange：
                  // 每敲一个字发一次请求既浪费也没意义（半截 URL 查不到东西）。
                  onBlur: (e) => {
                    const v = String((e.target as HTMLInputElement).value || '').trim()
                    if (!v || !/^https?:\/\//i.test(v)) {
                      setGlobalHit(null)
                      return
                    }
                    setCheckingGlobal(true)
                    lookupGlobalSubByURL(v)
                      .then((r) => {
                        setGlobalHit(r.exists ? { name: r.name || '', entries: r.entries } : null)
                      })
                      .catch(() => setGlobalHit(null))
                      .finally(() => setCheckingGlobal(false))
                  },
                })}
              />
              {form.formState.errors.url && (
                <p className="text-xs text-danger">{form.formState.errors.url.message}</p>
              )}
              {checkingGlobal && (
                <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Loader2 className="size-3 animate-spin" />
                  正在检查是否已有全局订阅…
                </p>
              )}
              {/* 命中就明确说清楚「已经生效了」并给出规模 ——
                  只说「已存在」，用户会以为是自己列表里的重复项。 */}
              {globalHit && (
                <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2">
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warning" />
                  <div className="min-w-0">
                    <p className="text-xs font-medium text-warning">全局订阅已存在此规则</p>
                    <p className="mt-0.5 break-words text-[11.5px] leading-relaxed text-muted-foreground">
                      管理员已内置《{globalHit.name || '同名规则'}》
                      {typeof globalHit.entries === 'number'
                        ? `（${globalHit.entries.toLocaleString()} 条）`
                        : ''}
                      ，对所有用户生效，无需重复添加。重复添加只会占用你的订阅配额。
                    </p>
                  </div>
                </div>
              )}
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>列表类型</Label>
                <Select
                  value={form.watch('list_type')}
                  onValueChange={(v) => form.setValue('list_type', v as ListType)}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="blocklist">黑名单（拦截）</SelectItem>
                    <SelectItem value="allowlist">白名单（放行）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>格式</Label>
                <Select
                  value={form.watch('format')}
                  onValueChange={(v) => form.setValue('format', v as SubFormat)}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(FORMAT_LABEL) as SubFormat[]).map((f) => (
                      <SelectItem key={f} value={f}>
                        {FORMAT_LABEL[f]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="flex items-center justify-between rounded-xl border border-border bg-background px-4 py-3">
              <div>
                <p className="text-sm font-medium">启用订阅</p>
                <p className="text-xs text-muted-foreground">关闭后不再自动更新</p>
              </div>
              <Switch
                checked={form.watch('enabled')}
                onCheckedChange={(v) => form.setValue('enabled', v)}
              />
            </div>

            {/* 单个订阅的超时。默认跟随全局，慢源可单独放宽。
                没有这个口子的话，默认 20 秒会让几个 1MB+ / 30KB/s 的
                公共大源必然失败 —— 而它们是全局规则。 */}
            <div className="space-y-2">
              <Label htmlFor="sub-timeout">拉取超时（秒）</Label>
              <Input
                id="sub-timeout"
                type="number"
                min={0}
                max={600}
                className="font-mono"
                {...form.register('timeout_sec')}
              />
              <p className="text-xs text-muted-foreground">
                填 <span className="text-foreground">0</span> 表示跟随全局默认（在「常规设置 → 订阅刷新时间」里配）。
                慢速大源（1MB 以上、速度只有几十 KB/s 的公共列表）建议单独给 60–120 秒，
                否则每次刷新都会超时，规则会一直停在旧版本。
              </p>
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditorOpen(false)}>
                取消
              </Button>
              <Button
              type="submit"
              loading={saveMutation.isPending}
              // 命中全局订阅时禁用：服务端也会拒（409），
              // 这里先挡住是为了让用户不必提交一次才看到失败。
              disabled={scope === 'user' && !!globalHit}
            >
                {editing ? '保存' : '添加'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* 推荐订阅 */}
      <Dialog open={presetsOpen} onOpenChange={setPresetsOpen}>
        <DialogContent className="md:max-w-lg">
          <DialogHeader>
            <DialogTitle>推荐订阅</DialogTitle>
            <DialogDescription>点击即可添加常用的公共规则列表。</DialogDescription>
          </DialogHeader>
          <div className="max-h-[60vh] space-y-2 overflow-y-auto">
            {presetsQuery.isLoading ? (
              Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-16 rounded-xl" />)
            ) : presetsQuery.isError ? (
              <ErrorState message={errorMessage(presetsQuery.error)} onRetry={() => presetsQuery.refetch()} />
            ) : (presetsQuery.data?.length ?? 0) === 0 ? (
              <EmptyState icon={<Shield className="size-6" />} title="暂无推荐订阅" className="py-8" />
            ) : (
              presetsQuery.data!.map((p) => {
                const added = existingUrls.has(p.url)
                return (
                <div
                  key={p.url}
                  className={cn(
                    'flex items-center justify-between gap-3 rounded-xl border border-border bg-background p-4',
                    added && 'opacity-60',
                  )}
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="truncate text-sm font-medium">{p.name}</p>
                      <Badge variant={p.list_type === 'allowlist' ? 'success' : 'danger'}>
                        {p.list_type === 'allowlist' ? '白名单' : '黑名单'}
                      </Badge>
                    </div>
                    <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{p.description}</p>
                  </div>
                  {added ? (
                    <Button size="sm" variant="secondary" disabled title="该订阅已在列表中">
                      <Check />
                      已添加
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      onClick={() => {
                        setPresetsOpen(false)
                        openCreate({ name: p.name, url: p.url, list_type: p.list_type })
                      }}
                    >
                      <Plus />
                      添加
                    </Button>
                  )}
                </div>
                )
              })
            )}
          </div>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(pendingDelete)}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="删除订阅"
        description={pendingDelete ? `确定要删除订阅「${pendingDelete.name}」吗？其已导入的规则也会被移除。` : ''}
        destructive
        confirmText="删除"
        loading={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />
    </div>
  )
}
