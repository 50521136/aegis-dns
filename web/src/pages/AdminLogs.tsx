import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  RefreshCw,
  ScrollText,
  Search,
  ShieldAlert,
} from 'lucide-react'
import { getAdminQueryLog } from '@/api/adminStats'
import { listUsers } from '@/api/admin'
import { errorMessage } from '@/api/client'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTimeSec, latencyTone } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { QueryLogItem } from '@/api/types'
import { QueryLogDetail } from '@/components/QueryLogDetail'

// 状态只有三种：已拦截 / 允许项 / 已处理。「缓存」是修饰不是状态。
// 这里漏了 handled 会导致管理员全局日志直接显示英文原值（线上就出现过）。
function statusLabel(action: string, cacheHit?: boolean) {
  const meta = ACTION_META[action]
  const base = meta?.label ?? action
  return cacheHit ? `${base}（缓存）` : base
}

const ACTION_META: Record<string, { label: string; variant: 'success' | 'danger' | 'default' | 'warning' }> = {
  allowed: { label: '允许项', variant: 'success' },
  blocked: { label: '已拦截', variant: 'danger' },
  handled: { label: '已处理', variant: 'default' },
}

const toneClass = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

/**
 * 全局查询日志：管理员视角，能看所有租户的解析记录。
 *
 * 与用户版 QueryLog 的区别是多一列「用户」和按用户筛选 ——
 * 处理投诉（某域名被误拦）和排查滥用（谁在刷量）都要靠它。
 */
export default function AdminLogs() {
  const [page, setPage] = useState(1)
  // 点开的那一条；null 表示弹窗关闭
  const [selected, setSelected] = useState<QueryLogItem | null>(null)
  const [domainInput, setDomainInput] = useState('')
  const [domain, setDomain] = useState('')
  const [action, setAction] = useState('all')
  const [userId, setUserId] = useState('all')
  // 默认 20 条，避免一屏 50 行把页面拉得过长
  const [pageSize, setPageSize] = useState(20)

  // 用户下拉：拉一页足够多的用户，管理员一般不会有成百上千个租户
  const usersQuery = useQuery({
    queryKey: ['admin', 'users', 'for-log-filter'],
    queryFn: () => listUsers({ page: 1, page_size: 200 }),
  })
  const users = usersQuery.data?.items ?? []

  const query = useQuery({
    queryKey: ['admin', 'querylog', page, pageSize, domain, action, userId],
    queryFn: () =>
      getAdminQueryLog({
        limit: pageSize,
        offset: (page - 1) * pageSize,
        domain: domain || undefined,
        action: action === 'all' ? undefined : action,
        user_id: userId === 'all' ? undefined : userId,
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
        title="全局日志"
        description="所有用户的 DNS 查询记录，每 15 秒自动刷新"
        icon={<ScrollText className="size-5" />}
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => query.refetch()}
            disabled={query.isFetching}
          >
            <RefreshCw className={query.isFetching ? 'animate-spin' : undefined} />
            刷新
          </Button>
        }
      />

      <Card>
        <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center">
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
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={userId}
              onValueChange={(v) => {
                setUserId(v)
                setPage(1)
              }}
            >
              <SelectTrigger className="w-40">
                <SelectValue placeholder="全部用户" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部用户</SelectItem>
                {users.map((u) => (
                  <SelectItem key={u.id} value={u.id}>
                    {u.username}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <Select
              value={action}
              onValueChange={(v) => {
                setAction(v)
                setPage(1)
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="全部动作" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部动作</SelectItem>
                <SelectItem value="allowed">放行</SelectItem>
                <SelectItem value="blocked">拦截</SelectItem>
                <SelectItem value="handled">已处理</SelectItem>
              </SelectContent>
            </Select>

            <Button variant="secondary" onClick={doSearch}>
              筛选
            </Button>
          </div>
        </CardContent>
      </Card>

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
          description="用户产生解析后，日志会显示在这里。"
        />
      ) : (
        <>
          <div className="hidden rounded-2xl border border-border/50 bg-card shadow-sm md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-40">时间</TableHead>
                  <TableHead className="w-32">用户</TableHead>
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
                  const label = statusLabel(row.action, row.cache_hit)
                  const meta = ACTION_META[row.action] ?? {
                    label: row.action,
                    variant: 'default' as const,
                  }
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
                        <span className="text-sm">{row.username || '已删除用户'}</span>
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
                      <TableCell
                        className={cn(
                          'text-right font-mono text-xs tabular-nums',
                          toneClass[latencyTone(row.latency_ms)],
                        )}
                      >
                        {row.latency_ms} ms
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>

          {/* 移动端卡片 */}
          <div className="space-y-3 md:hidden">
            {items.map((row) => {
              const label = statusLabel(row.action, row.cache_hit)
              const meta = ACTION_META[row.action] ?? {
                label: row.action,
                variant: 'default' as const,
              }
              // 管理员全局日志的手机卡片同样要能点开详情：
              // 用户侧修过一次，这里漏了，手机上管理员依然点不动。
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
                    <span className="inline-flex items-center gap-1">
                      <ShieldAlert className="size-3" />
                      {row.username || '已删除用户'}
                    </span>
                    <span>{formatDateTimeSec(row.ts)}</span>
                    <Badge variant="secondary">{row.qtype || '—'}</Badge>
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
      <QueryLogDetail row={selected} onClose={() => setSelected(null)} />
    </div>
  )
}
