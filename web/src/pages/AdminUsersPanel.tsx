import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AtSign,
  Check,
  Gift,
  Info,
  KeyRound,
  RefreshCw,
  Search,
  Trash2,
  Users,
} from 'lucide-react'
import {
  bulkDeleteUsers,
  deleteUser,
  getUsersUsage,
  listUsers,
  resetUserClientID,
  setUserClientID,
  setUserPassword,
  updateUser,
} from '@/api/admin'
import { errorMessage } from '@/api/client'
import type { User } from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { AdminUserDetailDialog } from '@/components/AdminUserDetailDialog'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Pagination } from '@/components/Pagination'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useToast } from '@/components/ui/toast'
import { useAuth } from '@/hooks/useAuth'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { revokeUserMember, setUserMember } from '@/api/member'

/**
 * 用户管理面板。
 *
 * 除了单个启用/禁用/改角色，这里还支持批量选择删除、给单个用户改密码、
 * 重置接入 ID，以及按用户开关「能否自己加规则/订阅」。
 */
export function UsersPanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const { user: me } = useAuth()

  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [searchInput, setSearchInput] = useState('')
  const [q, setQ] = useState('')

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [pendingDelete, setPendingDelete] = useState<User | null>(null)
  const [bulkOpen, setBulkOpen] = useState(false)
  const [pwUser, setPwUser] = useState<User | null>(null)
  const [pwValue, setPwValue] = useState('')
  const [resetUser, setResetUser] = useState<User | null>(null)
  const [cidUser, setCidUser] = useState<User | null>(null)
  // 会员调整弹窗
  const [memberUser, setMemberUser] = useState<User | null>(null)
  // 详情弹窗：会员状态、开关、使用量、配额覆盖都在这里改
  const [detailUser, setDetailUser] = useState<string | null>(null)
  const [memberDays, setMemberDays] = useState(30)
  const [cidValue, setCidValue] = useState('')

  const usersQuery = useQuery({
    queryKey: ['admin', 'users', page, pageSize, q],
    queryFn: () => listUsers({ page, page_size: pageSize, q: q || undefined }),
  })

  // 规则/订阅占用：后台列表要显示「3/50」，逐个用户查会 N+1，所以一次拿全量
  const usageQuery = useQuery({ queryKey: ['admin', 'users', 'usage'], queryFn: getUsersUsage })
  const usageOf = useMemo(() => {
    const m = new Map<string, { rules: number; subs: number }>()
    for (const u of usageQuery.data?.items ?? []) m.set(u.user_id, { rules: u.rules, subs: u.subs })
    return m
  }, [usageQuery.data])
  const limits = usageQuery.data?.limits

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['admin', 'users'] })
  }

  const items = usersQuery.data?.items ?? []
  const total = usersQuery.data?.total ?? 0

  // 全选只作用于当前页：跨页全选会让管理员在不知情的情况下删掉看不到的人
  const pageIds = items.map((u) => u.id).filter((id) => id !== me?.id)
  const allChecked = pageIds.length > 0 && pageIds.every((id) => selected.has(id))

  const toggleOne = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const toggleAll = () => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (allChecked) pageIds.forEach((id) => next.delete(id))
      else pageIds.forEach((id) => next.add(id))
      return next
    })
  }

  const updateMutation = useMutation({
    mutationFn: ({
      id,
      patch,
    }: {
      id: string
      patch: {
        enabled?: boolean
        role?: 'admin' | 'user'
        rules_enabled?: boolean
        subs_enabled?: boolean
      }
    }) => updateUser(id, patch),
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

  const bulkMutation = useMutation({
    mutationFn: () => bulkDeleteUsers([...selected]),
    onSuccess: (r) => {
      toast.success(
        '批量删除完成',
        r.skipped > 0 ? `删除 ${r.deleted} 个，跳过 ${r.skipped} 个` : `删除 ${r.deleted} 个`,
      )
      setSelected(new Set())
      setBulkOpen(false)
      invalidate()
    },
    onError: (err) => toast.error('批量删除失败', errorMessage(err)),
  })

  const pwMutation = useMutation({
    mutationFn: () => setUserPassword(pwUser!.id, pwValue),
    onSuccess: () => {
      toast.success('密码已修改', '该用户的所有登录会话已失效，需要重新登录')
      setPwUser(null)
      setPwValue('')
    },
    onError: (err) => toast.error('修改密码失败', errorMessage(err)),
  })

  const cidMutation = useMutation({
    mutationFn: () => setUserClientID(cidUser!.id, cidValue.trim().toLowerCase()),
    onSuccess: (r) => {
      toast.success('接入 ID 已设置', `新 ID：${r.client_id}`)
      setCidUser(null)
      setCidValue('')
      invalidate()
    },
    onError: (err) => toast.error('设置失败', errorMessage(err)),
  })

  const resetMutation = useMutation({
    mutationFn: () => resetUserClientID(resetUser!.id),
    onSuccess: (r) => {
      toast.success('接入 ID 已重置', `新 ID：${r.client_id}`)
      setResetUser(null)
      invalidate()
    },
    onError: (err) => toast.error('重置失败', errorMessage(err)),
  })

  const memberMut = useMutation({
    mutationFn: ({ id, days }: { id: string; days: number }) => setUserMember(id, days),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setMemberUser(null)
      toast.success(
        r.tier === 'vip' ? '会员已生效' : '会员已取消',
        r.tier === 'vip' ? `剩余 ${r.member_days} 天` : '已降为普通用户',
      )
    },
    onError: (err) => toast.error('调整失败', errorMessage(err)),
  })

  const revokeMut = useMutation({
    mutationFn: (id: string) => revokeUserMember(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] })
      setMemberUser(null)
      toast.success('会员已取消')
    },
    onError: (err) => toast.error('取消失败', errorMessage(err)),
  })

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            <Users className="size-5 text-primary" />
            用户管理
          </CardTitle>
          <CardDescription>
            启用 / 禁用账号、调整角色、改密码、重置接入 ID；勾选后可批量删除。
          </CardDescription>
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

        {/* 批量操作条：只在有选中项时出现，避免常驻占位 */}
        {selected.size > 0 && (
          <div className="m-groove flex flex-wrap items-center gap-3 rounded-xl px-4 py-2.5">
            <span className="text-sm">
              已选 <span className="readout text-primary">{selected.size}</span> 个用户
            </span>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
              取消选择
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="ml-auto text-danger hover:bg-danger/10"
              onClick={() => setBulkOpen(true)}
            >
              <Trash2 />
              批量删除
            </Button>
          </div>
        )}

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
            <div className="overflow-x-auto rounded-2xl border border-border/60">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-10">
                      <input
                        type="checkbox"
                        checked={allChecked}
                        onChange={toggleAll}
                        aria-label="全选本页"
                        className="size-4 accent-[rgb(var(--primary))]"
                      />
                    </TableHead>
                    <TableHead>用户名</TableHead>
                    <TableHead className="hidden lg:table-cell">客户端 ID</TableHead>
                    <TableHead className="hidden xl:table-cell">规则 / 订阅</TableHead>
                    <TableHead className="w-28">角色</TableHead>
                    <TableHead className="w-16">启用</TableHead>
                    <TableHead className="hidden md:table-cell w-20">规则</TableHead>
                    <TableHead className="hidden md:table-cell w-20">订阅</TableHead>
                    <TableHead className="hidden md:table-cell w-32">注册时间</TableHead>
                    <TableHead className="w-28 text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((u) => {
                    const isSelf = u.id === me?.id
                    const usage = usageOf.get(u.id)
                    // 每个用户的上限取决于他自己的档位，不能拿统一数字比对
                    const tierLimits = limits ? limits[u.tier === 'vip' ? 'vip' : 'free'] : null
                    const rulesPct =
                      tierLimits && tierLimits.max_rules > 0
                        ? (usage?.rules ?? 0) / tierLimits.max_rules
                        : 0
                    return (
                      <TableRow key={u.id} className={cn(selected.has(u.id) && 'bg-primary/5')}>
                        <TableCell>
                          <input
                            type="checkbox"
                            checked={selected.has(u.id)}
                            onChange={() => toggleOne(u.id)}
                            disabled={isSelf}
                            aria-label={`选择 ${u.username}`}
                            title={isSelf ? '不能删除自己' : undefined}
                            className="size-4 accent-[rgb(var(--primary))]"
                          />
                        </TableCell>
                        <TableCell className="font-medium">
                          <div className="flex items-center gap-1.5">
                            {u.username}
                            {isSelf && <Badge variant="secondary">自己</Badge>}
                            {/* 档位由后端按到期时间算好，前端不自己判断 */}
                            {u.role !== 'admin' && u.tier === 'vip' && (
                              <Badge variant="success" title={`剩余 ${u.member_days} 天`}>
                                会员 {u.member_days}天
                              </Badge>
                            )}
                          </div>
                          <p className="text-xs text-muted-foreground md:hidden">
                            {u.email || '—'}
                          </p>
                        </TableCell>
                        <TableCell className="hidden lg:table-cell">
                          {/* 管理员是控制面角色，不用 DNS 接入，给他一个 client_id
                              只会让人以为那是个能用的子域名 */}
                          {u.role === 'admin' ? (
                            <span className="text-xs text-muted-foreground">—</span>
                          ) : (
                            <code className="font-mono text-xs">{u.client_id}</code>
                          )}
                        </TableCell>
                        <TableCell className="hidden xl:table-cell">
                          <span
                            className={cn(
                              'font-mono text-xs tabular-nums',
                              rulesPct >= 0.9 && 'text-warning',
                            )}
                          >
                            {usage?.rules ?? 0}
                            {tierLimits && tierLimits.max_rules > 0
                              ? ` / ${tierLimits.max_rules}`
                              : ''}
                          </span>
                          <span className="mx-1 text-muted-foreground">·</span>
                          <span className="font-mono text-xs tabular-nums">
                            {usage?.subs ?? 0}
                            {tierLimits && tierLimits.max_subs > 0
                              ? ` / ${tierLimits.max_subs}`
                              : ''}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Select
                            value={u.role}
                            onValueChange={(v) =>
                              updateMutation.mutate({ id: u.id, patch: { role: v as 'admin' | 'user' } })
                            }
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
                            onCheckedChange={(v) =>
                              updateMutation.mutate({ id: u.id, patch: { enabled: v } })
                            }
                            aria-label="启用"
                          />
                        </TableCell>
                        <TableCell className="hidden md:table-cell">
                          <Switch
                            checked={u.rules_enabled !== false}
                            onCheckedChange={(v) =>
                              updateMutation.mutate({ id: u.id, patch: { rules_enabled: v } })
                            }
                            aria-label="允许加规则"
                          />
                        </TableCell>
                        <TableCell className="hidden md:table-cell">
                          <Switch
                            checked={u.subs_enabled !== false}
                            onCheckedChange={(v) =>
                              updateMutation.mutate({ id: u.id, patch: { subs_enabled: v } })
                            }
                            aria-label="允许加订阅"
                          />
                        </TableCell>
                        <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                          {formatDateTime(u.created_at)}
                        </TableCell>
                        <TableCell>
                          <div className="flex justify-end gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              onClick={() => setDetailUser(u.id)}
                              aria-label="查看详情"
                              title="查看详情 / 调整配额"
                            >
                              <Info className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              onClick={() => {
                                setPwUser(u)
                                setPwValue('')
                              }}
                              aria-label="修改密码"
                              title="修改密码"
                            >
                              <KeyRound />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              disabled={u.role === 'admin'}
                              onClick={() => {
                                setCidUser(u)
                                setCidValue(u.client_id)
                              }}
                              aria-label="自定义接入 ID"
                              title="自定义接入 ID（不消耗用户的次数）"
                            >
                              <AtSign />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              disabled={u.role === 'admin'}
                              onClick={() => setResetUser(u)}
                              aria-label="重置接入 ID"
                              title="重置接入 ID"
                            >
                              <RefreshCw />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              disabled={u.role === 'admin'}
                              onClick={() => {
                                setMemberUser(u)
                                setMemberDays(30)
                              }}
                              aria-label="调整会员"
                              title="调整会员天数"
                            >
                              <Gift />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              className="text-danger hover:bg-danger/10"
                              disabled={isSelf}
                              onClick={() => setPendingDelete(u)}
                              aria-label="删除"
                            >
                              <Trash2 />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
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
      </CardContent>

      {/* 单个删除 */}
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

      {/* 批量删除 */}
      <ConfirmDialog
        open={bulkOpen}
        onOpenChange={setBulkOpen}
        title={`批量删除 ${selected.size} 个用户`}
        description={`这些用户的规则、订阅与统计数据都会被清除，且不可恢复。`}
        destructive
        confirmText={`删除 ${selected.size} 个`}
        loading={bulkMutation.isPending}
        onConfirm={() => bulkMutation.mutate()}
      />

      {/* 重置接入 ID */}
      <ConfirmDialog
        open={Boolean(resetUser)}
        onOpenChange={(o) => !o && setResetUser(null)}
        title="重置接入 ID"
        description={
          resetUser
            ? `将给「${resetUser.username}」分配新的 client_id（当前 ${resetUser.client_id}）。旧子域名会立刻失效，对方所有设备都要重新配置。`
            : ''
        }
        confirmText="重置"
        loading={resetMutation.isPending}
        onConfirm={() => resetMutation.mutate()}
      />

      {/* 自定义接入 ID */}
      <AdminUserDetailDialog userId={detailUser} onClose={() => setDetailUser(null)} />

      <Dialog
        open={Boolean(cidUser)}
        onOpenChange={(o) => {
          if (!o) {
            setCidUser(null)
            setCidValue('')
          }
        }}
      >
        <DialogContent className="md:max-w-md">
          <DialogHeader>
            <DialogTitle>自定义接入 ID</DialogTitle>
            <DialogDescription>
              直接指定「{cidUser?.username}」的子域名前缀。6 位以上，仅小写字母和数字。
              这不消耗用户那一次自助机会。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="cid-input">接入 ID</Label>
            <Input
              id="cid-input"
              value={cidValue}
              onChange={(e) => setCidValue(e.target.value)}
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
            {cidValue.trim() && !/^[a-z0-9]{6,32}$/.test(cidValue.trim().toLowerCase()) && (
              <p className="text-xs text-danger">需要 6-32 位，只能用小写字母和数字</p>
            )}
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setCidUser(null)
                setCidValue('')
              }}
            >
              取消
            </Button>
            <Button
              loading={cidMutation.isPending}
              disabled={!/^[a-z0-9]{6,32}$/.test(cidValue.trim().toLowerCase())}
              onClick={() => cidMutation.mutate()}
            >
              <Check />
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 改密码 */}
      <Dialog
        open={Boolean(pwUser)}
        onOpenChange={(o) => {
          if (!o) {
            setPwUser(null)
            setPwValue('')
          }
        }}
      >
        <DialogContent className="md:max-w-md">
          <DialogHeader>
            <DialogTitle>修改密码</DialogTitle>
            <DialogDescription>
              为「{pwUser?.username}」设置新密码。保存后该用户的所有登录会话会失效。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="new-pw">新密码</Label>
            <Input
              id="new-pw"
              type="password"
              value={pwValue}
              onChange={(e) => setPwValue(e.target.value)}
              placeholder="至少 8 位，含字母和数字"
              autoComplete="new-password"
            />
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setPwUser(null)
                setPwValue('')
              }}
            >
              取消
            </Button>
            <Button
              loading={pwMutation.isPending}
              disabled={pwValue.length < 8}
              onClick={() => pwMutation.mutate()}
            >
              <Check />
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 会员调整 */}
      <Dialog open={memberUser !== null} onOpenChange={(v) => !v && setMemberUser(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>调整会员</DialogTitle>
            <DialogDescription>
              {memberUser?.tier === 'vip'
                ? `「${memberUser.username}」当前是会员，剩余 ${memberUser.member_days} 天。`
                : `「${memberUser?.username}」当前是普通用户。`}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div>
              <Label htmlFor="member-days">增减天数</Label>
              <Input
                id="member-days"
                type="number"
                value={memberDays}
                onChange={(e) => setMemberDays(Number(e.target.value))}
              />
              <p className="mt-1 text-xs text-muted-foreground">
                正数延长，负数扣减（扣到过去即立即到期）。从「现在与原到期时间中较晚者」起算，
                连续调整不会冲掉剩余时间。
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              {[7, 30, 90, 365].map((d) => (
                <Button key={d} size="sm" variant="secondary" onClick={() => setMemberDays(d)}>
                  +{d} 天
                </Button>
              ))}
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="ghost"
              className="text-danger hover:bg-danger/10"
              loading={revokeMut.isPending}
              onClick={() => memberUser && revokeMut.mutate(memberUser.id)}
            >
              直接取消会员
            </Button>
            <Button
              loading={memberMut.isPending}
              disabled={!memberDays}
              onClick={() =>
                memberUser && memberMut.mutate({ id: memberUser.id, days: memberDays })
              }
            >
              <Check />
              应用
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
