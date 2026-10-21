import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Eraser,
  RefreshCw,
  ScrollText,
  Search,
} from 'lucide-react'
import { clearMyLogs, getQueryLog } from '@/api/stats'
import type { QueryLogItem } from '@/api/types'
import { QueryLogDetail } from '@/components/QueryLogDetail'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
import { useToast } from '@/components/ui/toast'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { errorMessage } from '@/api/client'
import { formatDateTimeSec, latencyTone } from '@/lib/format'
import { cn } from '@/lib/utils'

// 查询状态只有三种：已拦截 / 允许项 / 已处理。
// 「缓存」不是状态，是修饰 —— 显示成「已处理（缓存）」「允许项（缓存）」。
const ACTION_META: Record<string, { label: string; variant: 'success' | 'danger' | 'default' | 'warning' }> = {
  blocked: { label: '已拦截', variant: 'danger' },
  allowed: { label: '允许项', variant: 'success' },
  handled: { label: '已处理', variant: 'default' },
}

/** statusLabel 拼出「状态（缓存）」这样的展示文案 */
function statusLabel(action: string, cacheHit?: boolean) {
  const meta = ACTION_META[action]
  const base = meta?.label ?? action
  return cacheHit ? `${base}（缓存）` : base
}

const toneClass = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

export default function QueryLog() {
  const [page, setPage] = useState(1)
  // 点开的那一条；null 表示弹窗关闭
  const [selected, setSelected] = useState<QueryLogItem | null>(null)
  const [confirmClear, setConfirmClear] = useState(false)
  const toast = useToast()

  const clearLogs = useMutation({
    mutationFn: clearMyLogs,
    onSuccess: () => {
      toast.success('已清空你看到的日志', '管理员侧的记录仍然保留')
      setConfirmClear(false)
      query.refetch()
    },
    onError: () => toast.error('清空失败'),
  })
  const [domainInput, setDomainInput] = useState('')
  const [domain, setDomain] = useState('')
  const [action, setAction] = useState('all')
  // 默认 20 条：50 条一页在手机上要划很久，需要更多可以在分页器里切
  const [pageSize, setPageSize] = useState(20)

  const query = useQuery({
    queryKey: ['stats', 'querylog', page, pageSize, domain, action],
    queryFn: () =>
      getQueryLog({
        limit: pageSize,
        offset: (page - 1) * pageSize,
        domain: domain || undefined,
        action: action === 'all' ? undefined : action,
      }),
    refetchInterval: 15000,
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0

  const doSearch = () => {
    setDomain(domainInput.trim())
    setPage(1)
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="查询日志"
        description="最近的 DNS 查询记录，每 15 秒自动刷新"
        icon={<ScrollText className="size-5" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => query.refetch()}
              disabled={query.isFetching}
            >
              <RefreshCw className={query.isFetching ? 'animate-spin' : undefined} />
              刷新
            </Button>
            {/* 清空的是「你自己看到的」，管理员那边的记录一条不动 */}
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setConfirmClear(true)}
              disabled={clearLogs.isPending}
            >
              <Eraser />
              清空日志
            </Button>
          </div>
        }
      />

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={domainInput}
            onChange={(e) => setDomainInput(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && doSearch()}
            placeholder="按域名筛选…"
            className="pl-9"
          />
        </div>
        <div className="flex items-center gap-2">
          <Select value={action} onValueChange={(v) => { setAction(v); setPage(1) }}>
            <SelectTrigger className="w-32">
              <SelectValue placeholder="全部状态" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              <SelectItem value="blocked">已拦截</SelectItem>
              <SelectItem value="allowed">允许项</SelectItem>
              <SelectItem value="handled">已处理</SelectItem>
            </SelectContent>
          </Select>
          <Button variant="secondary" onClick={doSearch}>
            筛选
          </Button>
        </div>
      </div>

      {query.isError ? (
        <ErrorState message={errorMessage(query.error)} onRetry={() => query.refetch()} />
      ) : query.isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ScrollText className="size-6" />}
          title="暂无查询记录"
          description="接入 DNS 并产生查询后，日志会显示在这里。"
        />
      ) : (
        <>
          {/* 桌面表格 */}
          <div className="hidden rounded-2xl border border-border/50 bg-card shadow-sm md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-44">时间</TableHead>
                  <TableHead>域名</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  <TableHead className="w-24">动作</TableHead>
                  <TableHead className="w-24">响应码</TableHead>
                  <TableHead className="w-32">客户端</TableHead>
                  <TableHead className="w-24 text-right">延迟</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((row) => {
                  const meta = ACTION_META[row.action] ?? { label: row.action, variant: 'default' as const }
                  const label = statusLabel(row.action, row.cache_hit)
                  return (
                    <TableRow
                      key={row.id}
                      className="cursor-pointer hover:bg-muted/40"
                      onClick={() => setSelected(row)}
                    >
                      <TableCell className="text-xs text-muted-foreground tabular-nums">
                        {formatDateTimeSec(row.ts)}
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-[13px]">{row.domain}</span>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{row.qtype || '—'}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={meta.variant}>{label}</Badge>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{row.rcode || '—'}</TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {row.client || '—'}
                      </TableCell>
                      <TableCell className={cn('text-right font-mono text-xs tabular-nums', toneClass[latencyTone(row.latency_ms)])}>
                        {row.latency_ms} ms
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>

          {/* 移动卡片 */}
          <div className="space-y-3 md:hidden">
            {items.map((row) => {
              const meta = ACTION_META[row.action] ?? { label: row.action, variant: 'default' as const }
              const label = statusLabel(row.action, row.cache_hit)
              // 手机端卡片必须能点开详情：之前只有桌面表格行绑了 onClick，
              // 手机上整页点不动，日志只能看不能查。
              return (
                <div
                  key={row.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => setSelected(row)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setSelected(row)
                    }
                  }}
                  className="cursor-pointer rounded-2xl border border-border/50 bg-card p-4 shadow-sm transition-colors active:bg-muted/40"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="break-all font-mono text-[13px]">{row.domain}</span>
                    <Badge variant={meta.variant}>{label}</Badge>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span>{formatDateTimeSec(row.ts)}</span>
                    <Badge variant="secondary">{row.qtype || '—'}</Badge>
                    <span className="font-mono">{row.rcode || '—'}</span>
                    <span className={cn('font-mono tabular-nums', toneClass[latencyTone(row.latency_ms)])}>
                      {row.latency_ms} ms
                    </span>
                  </div>
                </div>
              )
            })}
          </div>

          <Pagination
            page={page}
            pageSize={pageSize}
            total={total}
            onPageChange={setPage}
            onPageSizeChange={setPageSize}
          />
        </>
      )}

      {/* 单条详情：请求 / 响应 / 客户端 / 操作 */}
      <QueryLogDetail row={selected} onClose={() => setSelected(null)} />

      <ConfirmDialog
        open={confirmClear}
        title="清空查询日志？"
        description="只会清空你自己看到的记录，管理员侧的审计日志仍然保留。清空后无法恢复。"
        confirmText="清空"
        destructive
        loading={clearLogs.isPending}
        onConfirm={() => clearLogs.mutate()}
        onOpenChange={setConfirmClear}
      />
    </div>
  )
}
