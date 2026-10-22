import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, Search, Trash2, Users } from 'lucide-react'

import { deleteAdminUserRule, listAdminUserRules, listAdminUserSubs } from '@/api/adminAssets'
import { errorMessage } from '@/api/client'
import type { AdminOwnedRule } from '@/api/types'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useToast } from '@/components/ui/toast'
import { formatDateTimeSec } from '@/lib/format'

const KIND_META: Record<string, { label: string; variant: 'danger' | 'success' | 'warning' }> = {
  block: { label: '拦截', variant: 'danger' },
  allow: { label: '放行', variant: 'success' },
  rewrite: { label: '改写', variant: 'warning' },
}

/**
 * AdminUserRulesPanel 是管理员视角的「用户规则」列表。
 *
 * 为什么需要这一页：/admin/rules 一直是全局规则，普通用户自己加的规则
 * 在后台完全不可见。用户反馈「某域名被拦了」时，管理员翻不到那条规则，
 * 只能让他自己截图 —— 排查效率极低。
 *
 * 这里只读 + 删除。不提供编辑：改别人的规则容易越权改错，
 * 真要改应该由用户自己改，或者管理员先和用户确认。
 */
export function AdminUserRulesPanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const [page, setPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [q, setQ] = useState('')
  const [kind, setKind] = useState('')
  const [pendingDelete, setPendingDelete] = useState<AdminOwnedRule | null>(null)
  const pageSize = 20

  const rules = useQuery({
    queryKey: ['admin-user-rules', page, q, kind],
    queryFn: () => listAdminUserRules({ page, page_size: pageSize, q, kind }),
  })

  const del = useMutation({
    mutationFn: (id: string) => deleteAdminUserRule(id),
    onSuccess: () => {
      toast.success('已删除该规则', '该用户的缓存会随之失效')
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['admin-user-rules'] })
    },
    onError: (e) => toast.error('删除失败', errorMessage(e)),
  })

  const total = rules.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / pageSize))

  const doSearch = () => {
    setPage(1)
    setQ(searchInput.trim())
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && doSearch()}
            placeholder="搜域名、值或用户名"
            className="pl-9"
          />
        </div>
        <Select
          value={kind || 'all'}
          onValueChange={(v) => {
            setKind(v === 'all' ? '' : v)
            setPage(1)
          }}
        >
          <SelectTrigger className="w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部类型</SelectItem>
            <SelectItem value="block">拦截</SelectItem>
            <SelectItem value="allow">放行</SelectItem>
            <SelectItem value="rewrite">改写</SelectItem>
          </SelectContent>
        </Select>
        <Button variant="secondary" onClick={doSearch}>
          搜索
        </Button>
      </div>

      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Users className="size-3.5" />
        共 {total} 条用户规则（不含全局层）
      </div>

      {rules.isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-11 rounded-lg" />
          ))}
        </div>
      ) : rules.isError ? (
        <ErrorState message={errorMessage(rules.error)} onRetry={() => rules.refetch()} />
      ) : total === 0 ? (
        <EmptyState
          icon={<Ban className="size-6" />}
          title="还没有用户添加规则"
          description="用户在「规则」页添加的拦截/放行规则会出现在这里"
          className="py-10"
        />
      ) : (
        <>
          {/* 桌面：表格 */}
          <div className="hidden rounded-2xl border border-border/50 bg-card shadow-sm md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-32">用户</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  <TableHead>匹配内容</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-40">创建时间</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(rules.data?.items ?? []).map((r) => {
                  const meta = KIND_META[r.kind] ?? { label: r.kind, variant: 'warning' as const }
                  return (
                    <TableRow key={r.id}>
                      <TableCell className="font-medium">{r.username || '已删除用户'}</TableCell>
                      <TableCell>
                        <Badge variant={meta.variant}>{meta.label}</Badge>
                      </TableCell>
                      <TableCell className="font-mono text-[13px] break-all">
                        {r.pattern}
                        {r.value ? ` → ${r.value}` : ''}
                      </TableCell>
                      <TableCell>
                        <span className={r.enabled ? 'text-success' : 'text-muted-foreground'}>
                          {r.enabled ? '启用' : '停用'}
                        </span>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatDateTimeSec(r.created_at)}
                      </TableCell>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label="删除规则"
                          onClick={() => setPendingDelete(r)}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>

          {/* 手机：卡片 */}
          <div className="space-y-2 md:hidden">
            {(rules.data?.items ?? []).map((r) => {
              const meta = KIND_META[r.kind] ?? { label: r.kind, variant: 'warning' as const }
              return (
                <div key={r.id} className="rounded-2xl border border-border/50 bg-card p-4 shadow-sm">
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">{r.username || '已删除用户'}</p>
                      <p className="mt-1 break-all font-mono text-[13px]">{r.pattern}</p>
                    </div>
                    <Badge variant={meta.variant}>{meta.label}</Badge>
                  </div>
                  <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
                    <span>{formatDateTimeSec(r.created_at)}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setPendingDelete(r)}
                      className="text-danger"
                    >
                      <Trash2 className="size-3.5" />
                      删除
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>

          {pages > 1 && (
            <Pagination
              page={page}
              pageSize={pageSize}
              total={total}
              onPageChange={setPage}
            />
          )}
        </>
      )}

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="删除这条用户规则？"
        description={`${pendingDelete?.username ?? ''} 的 ${pendingDelete?.pattern ?? ''}。删除后该用户对这个域名的拦截/放行会立即失效。`}
        confirmText="删除"
        destructive
        loading={del.isPending}
        onConfirm={() => pendingDelete && del.mutate(pendingDelete.id)}
      />
    </div>
  )
}

/** AdminUserSubsPanel 是管理员视角的「用户订阅」列表（只读）。 */
export function AdminUserSubsPanel() {
  const [page, setPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [q, setQ] = useState('')
  const pageSize = 20

  const subs = useQuery({
    queryKey: ['admin-user-subs', page, q],
    queryFn: () => listAdminUserSubs({ page, page_size: pageSize, q }),
  })

  const total = subs.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && (setPage(1), setQ(searchInput.trim()))}
            placeholder="搜订阅名、地址或用户名"
            className="pl-9"
          />
        </div>
        <Button variant="secondary" onClick={() => (setPage(1), setQ(searchInput.trim()))}>
          搜索
        </Button>
      </div>

      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <CheckCircle2 className="size-3.5" />
        共 {total} 个用户订阅（不含全局层）
      </div>

      {subs.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : subs.isError ? (
        <ErrorState message={errorMessage(subs.error)} onRetry={() => subs.refetch()} />
      ) : total === 0 ? (
        <EmptyState
          icon={<Users className="size-6" />}
          title="还没有用户添加订阅"
          description="用户添加的规则订阅会出现在这里"
          className="py-10"
        />
      ) : (
        <>
          <div className="space-y-2">
            {(subs.data?.items ?? []).map((s) => (
              <div
                key={s.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-border/50 p-3"
              >
                <div className="min-w-0">
                  <p className="flex items-center gap-2 text-sm font-medium">
                    {s.name || '(未命名)'}
                    <Badge variant="secondary" className="text-[11px]">
                      {s.username || '已删除用户'}
                    </Badge>
                  </p>
                  <p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">{s.url}</p>
                </div>
                <div className="flex items-center gap-3 font-mono text-xs text-muted-foreground">
                  <span>{s.entry_count.toLocaleString('zh-CN')} 条</span>
                  <span className={s.enabled ? 'text-success' : ''}>{s.enabled ? '启用' : '停用'}</span>
                </div>
              </div>
            ))}
          </div>
          {pages > 1 && (
            <Pagination page={page} pageSize={pageSize} total={total} onPageChange={setPage} />
          )}
        </>
      )}
    </div>
  )
}
