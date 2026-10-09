import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  ListPlus,
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
import type { ListType, SubFormat, Subscription, SubscriptionInput } from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
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
})

type SubForm = z.infer<typeof schema>

const FORMAT_LABEL: Record<SubFormat, string> = {
  auto: '自动识别',
  adblock: 'Adblock',
  hosts: 'Hosts',
  dnsmasq: 'DNSMasq',
  domain: '域名列表',
}

function StatusBadge({ status }: { status: string }) {
  if (!status) return <Badge variant="secondary">未同步</Badge>
  if (status === 'ok' || status.startsWith('ok'))
    return (
      <Badge variant="success">
        <CheckCircle2 className="size-3" />
        正常
      </Badge>
    )
  return (
    <Badge variant="danger">
      <AlertTriangle className="size-3" />
      {status.replace(/^error:\s*/, '')}
    </Badge>
  )
}

export default function Subscriptions() {
  const qc = useQueryClient()
  const toast = useToast()

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<Subscription | null>(null)
  const [presetsOpen, setPresetsOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<Subscription | null>(null)
  const [refreshingId, setRefreshingId] = useState<string | null>(null)

  const listQuery = useQuery({ queryKey: ['subscriptions'], queryFn: listSubscriptions })
  const presetsQuery = useQuery({
    queryKey: ['subscriptions', 'presets'],
    queryFn: listPresets,
    enabled: presetsOpen,
  })

  const items = listQuery.data ?? []

  const form = useForm<SubForm>({
    resolver: zodResolver(schema),
    defaultValues: { name: '', url: '', list_type: 'blocklist', format: 'auto', enabled: true },
  })

  const openCreate = (preset?: { name: string; url: string; list_type: ListType }) => {
    setEditing(null)
    form.reset({
      name: preset?.name ?? '',
      url: preset?.url ?? '',
      list_type: preset?.list_type ?? 'blocklist',
      format: 'auto',
      enabled: true,
    })
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
    })
    setEditorOpen(true)
  }

  const invalidate = () => qc.invalidateQueries({ queryKey: ['subscriptions'] })

  const saveMutation = useMutation({
    mutationFn: (values: SubForm) => {
      const payload: SubscriptionInput = values
      return editing ? updateSubscription(editing.id, payload) : createSubscription(payload)
    },
    onSuccess: () => {
      toast.success(editing ? '订阅已更新' : '订阅已添加')
      setEditorOpen(false)
      invalidate()
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteSubscription(id),
    onSuccess: () => {
      toast.success('订阅已删除')
      setPendingDelete(null)
      invalidate()
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const refreshMutation = useMutation({
    mutationFn: (id: string) => refreshSubscription(id),
    onMutate: (id) => setRefreshingId(id),
    onSettled: () => setRefreshingId(null),
    onSuccess: (res) => {
      if (res.last_status === 'ok' || res.last_status.startsWith('ok')) {
        toast.success('刷新完成', `拉取 ${formatNumber(res.last_count)} 条，耗时 ${res.duration_ms}ms`)
      } else {
        toast.warning('刷新未成功', res.last_status || '未知状态')
      }
      invalidate()
    },
    onError: (err) => toast.error('刷新失败', errorMessage(err)),
  })

  const refreshAllMutation = useMutation({
    mutationFn: refreshAllSubscriptions,
    onSuccess: (res) => {
      toast.success('全部刷新完成', `成功 ${res.refreshed} 个，失败 ${res.failed} 个`)
      invalidate()
    },
    onError: (err) => toast.error('刷新失败', errorMessage(err)),
  })

  const toggleEnabled = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      updateSubscription(id, { enabled }),
    onSuccess: () => invalidate(),
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        title="订阅管理"
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
          </>
        }
      />

      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" onClick={() => setPresetsOpen(true)}>
          <ListPlus />
          从推荐列表添加
        </Button>
      </div>

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
          description="从推荐列表一键添加常用的广告拦截或白名单订阅源。"
          action={
            <Button onClick={() => setPresetsOpen(true)}>
              <ListPlus />
              浏览推荐订阅
            </Button>
          }
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {items.map((sub) => (
            <div
              key={sub.id}
              className="flex flex-col rounded-2xl border border-border/50 bg-card p-5 shadow-sm transition-all duration-200 hover:shadow-md"
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
                  </div>
                  <p className="mt-1 truncate font-mono text-xs text-muted-foreground" title={sub.url}>
                    {sub.url}
                  </p>
                </div>
                <Switch
                  checked={sub.enabled}
                  onCheckedChange={(v) => toggleEnabled.mutate({ id: sub.id, enabled: v })}
                  aria-label="启用订阅"
                />
              </div>

              <div className="mt-4 grid grid-cols-2 gap-3 text-sm">
                <div className="flex items-center gap-2">
                  <span className="text-muted-foreground">条目</span>
                  <span className="font-semibold tabular-nums">{formatNumber(sub.last_count)}</span>
                </div>
                <div className="flex items-center gap-2">
                  <Clock className="size-3.5 text-muted-foreground" />
                  <span className="text-xs text-muted-foreground">
                    {sub.last_fetched ? formatRelative(sub.last_fetched) : '从未同步'}
                  </span>
                </div>
              </div>

              <div className="mt-3">
                <StatusBadge status={sub.last_status} />
              </div>

              <div className="mt-4 flex items-center justify-end gap-1 border-t border-border/60 pt-3">
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
                {...form.register('url')}
              />
              {form.formState.errors.url && (
                <p className="text-xs text-danger">{form.formState.errors.url.message}</p>
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

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditorOpen(false)}>
                取消
              </Button>
              <Button type="submit" loading={saveMutation.isPending}>
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
              presetsQuery.data!.map((p) => (
                <div
                  key={p.url}
                  className={cn(
                    'flex items-center justify-between gap-3 rounded-xl border border-border bg-background p-4',
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
                </div>
              ))
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
