import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ScrollText, Search } from 'lucide-react'
import { getQueryLog } from '@/api/stats'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
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
import { formatDateTime, latencyTone } from '@/lib/format'
import { cn } from '@/lib/utils'

const ACTION_META: Record<string, { label: string; variant: 'success' | 'danger' | 'default' | 'warning' }> = {
  allowed: { label: '放行', variant: 'success' },
  blocked: { label: '拦截', variant: 'danger' },
  cached: { label: '缓存', variant: 'default' },
  rewritten: { label: '改写', variant: 'warning' },
}

const toneClass = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

export default function QueryLog() {
  const [page, setPage] = useState(1)
  const [domainInput, setDomainInput] = useState('')
  const [domain, setDomain] = useState('')
  const [action, setAction] = useState('all')
  const pageSize = 50

  const query = useQuery({
    queryKey: ['stats', 'querylog', page, domain, action],
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
              <SelectValue placeholder="全部动作" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部动作</SelectItem>
              <SelectItem value="allowed">放行</SelectItem>
              <SelectItem value="blocked">拦截</SelectItem>
              <SelectItem value="cached">缓存</SelectItem>
              <SelectItem value="rewritten">改写</SelectItem>
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
                  return (
                    <TableRow key={row.id}>
                      <TableCell className="text-xs text-muted-foreground tabular-nums">
                        {formatDateTime(row.ts)}
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-[13px]">{row.domain}</span>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{row.qtype || '—'}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={meta.variant}>{meta.label}</Badge>
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
              return (
                <div key={row.id} className="rounded-2xl border border-border/50 bg-card p-4 shadow-sm">
                  <div className="flex items-center justify-between gap-2">
                    <span className="break-all font-mono text-[13px]">{row.domain}</span>
                    <Badge variant={meta.variant}>{meta.label}</Badge>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span>{formatDateTime(row.ts)}</span>
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

          <Pagination page={page} pageSize={pageSize} total={total} onPageChange={setPage} />
        </>
      )}
    </div>
  )
}
