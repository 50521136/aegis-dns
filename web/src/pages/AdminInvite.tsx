import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CalendarClock, Copy, Gift, Plus, Trash2, Users } from 'lucide-react'

import {
  createInviteCode,
  deleteInviteCode,
  listInviteCodes,
  updateInviteCode,
} from '@/api/member'
import { listTiers } from '@/api/tiers'
import { errorMessage } from '@/api/client'
import type { InviteCode } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useToast } from '@/components/ui/toast'
import { formatDateTime } from '@/lib/format'

/**
 * InvitePanel 管理邀请码。
 *
 * 邀请码是「拉新 → 奖励会员天数」的凭据：被邀请人注册时填码，
 * 码的归属人自动获得奖励天数。归属人默认是创建它的管理员。
 */
export function InvitePanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  // reward_tier 是「核销后发给邀请人的会员种类」，空串 = 用全局设置
  const [form, setForm] = useState({
    code: '',
    note: '',
    reward_days: 30,
    reward_tier: '',
    max_uses: 0,
    expires: '',
  })

  const q = useQuery({ queryKey: ['admin', 'invite-codes'], queryFn: listInviteCodes })
  const tiersQuery = useQuery({ queryKey: ['tiers'], queryFn: listTiers })
  const tierList = [...(tiersQuery.data ?? [])].sort((a, b) => a.sort - b.sort)
  const tierName = (id: string) =>
    tierList.find((t) => t.id === id)?.name ?? (id || '默认会员档')

  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'invite-codes'] })

  const create = useMutation({
    mutationFn: () =>
      createInviteCode({
        code: form.code.trim() || undefined,
        note: form.note.trim(),
        reward_days: Number(form.reward_days) || 0,
        reward_tier: form.reward_tier,
        max_uses: Number(form.max_uses) || 0,
        expires_at: form.expires ? Math.floor(new Date(form.expires).getTime() / 1000) : 0,
      }),
    onSuccess: (c) => {
      invalidate()
      setOpen(false)
      setForm({ code: '', note: '', reward_days: 30, reward_tier: '', max_uses: 0, expires: '' })
      toast.success('邀请码已创建', c.code)
    },
    onError: (err) => toast.error('创建失败', errorMessage(err)),
  })

  const toggle = useMutation({
    mutationFn: (c: InviteCode) => updateInviteCode(c.code, { enabled: !c.enabled }),
    onSuccess: invalidate,
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  const remove = useMutation({
    mutationFn: (code: string) => deleteInviteCode(code),
    onSuccess: () => {
      invalidate()
      toast.success('已删除')
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const copy = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code)
      toast.success('已复制', code)
    } catch {
      toast.error('复制失败', '请手动选中复制')
    }
  }

  const items = q.data ?? []

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <Gift className="size-5 text-primary" />
            邀请码
          </CardTitle>
          <CardDescription>
            这里管理<strong className="text-foreground">管理员发出的</strong>邀请码。
            普通用户也能在「总览 → 邀请好友」里生成自己的邀请链接，奖励天数固定用全局设置里的值
            —— 能自选天数等于能给自己无限续会员。
          </CardDescription>
        </div>
        <Button size="sm" onClick={() => setOpen(true)}>
          <Plus />
          新建邀请码
        </Button>
      </CardHeader>
      <CardContent>
        {q.isLoading ? (
          <Skeleton className="h-40 rounded-xl" />
        ) : items.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">还没有邀请码</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>邀请码</TableHead>
                  <TableHead>备注</TableHead>
                  <TableHead>奖励天数</TableHead>
                  <TableHead>使用</TableHead>
                  <TableHead>有效期至</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((c) => (
                  <TableRow key={c.code}>
                    <TableCell>
                      <button
                        onClick={() => copy(c.code)}
                        className="flex items-center gap-1.5 font-mono text-sm font-medium hover:text-primary"
                        title="点击复制"
                      >
                        {c.code}
                        <Copy className="size-3 opacity-50" />
                      </button>
                    </TableCell>
                    <TableCell className="max-w-[12rem] truncate text-sm text-muted-foreground">
                      {c.note || '—'}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-sm">
                      <span className="font-mono">{c.reward_days} 天</span>
                      <span className="ml-1 text-xs text-muted-foreground">
                        {tierName(c.reward_tier)}
                      </span>
                    </TableCell>
                    <TableCell className="font-mono text-sm">
                      {c.used_count}
                      {c.max_uses > 0 ? ` / ${c.max_uses}` : ' / 不限'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {c.expires_at > 0 ? formatDateTime(c.expires_at) : '永不过期'}
                    </TableCell>
                    <TableCell>
                      {c.usable ? (
                        <Badge variant="success">可用</Badge>
                      ) : (
                        <Badge variant="secondary">{c.unusable_reason ?? '不可用'}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Switch
                          checked={c.enabled}
                          onCheckedChange={() => toggle.mutate(c)}
                          aria-label="启用"
                        />
                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={() => remove.mutate(c.code)}
                          aria-label="删除"
                          className="text-danger hover:bg-danger/10"
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
        )}
      </CardContent>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>新建邀请码</DialogTitle>
            <DialogDescription>留空邀请码则自动生成</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div>
              <Label htmlFor="iv-code">邀请码</Label>
              <Input
                id="iv-code"
                className="font-mono uppercase"
                placeholder="留空自动生成"
                value={form.code}
                onChange={(e) => setForm({ ...form, code: e.target.value.toUpperCase() })}
              />
            </div>
            <div>
              <Label htmlFor="iv-note">备注</Label>
              <Input
                id="iv-note"
                placeholder="发给谁 / 哪场活动"
                value={form.note}
                onChange={(e) => setForm({ ...form, note: e.target.value })}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label htmlFor="iv-reward">奖励天数</Label>
                <Input
                  id="iv-reward"
                  type="number"
                  min={0}
                  value={form.reward_days}
                  onChange={(e) => setForm({ ...form, reward_days: Number(e.target.value) })}
                />
              </div>
              <div>
                <Label htmlFor="iv-uses">可用次数</Label>
                <Input
                  id="iv-uses"
                  type="number"
                  min={0}
                  placeholder="0 = 不限"
                  value={form.max_uses}
                  onChange={(e) => setForm({ ...form, max_uses: Number(e.target.value) })}
                />
              </div>
            </div>
            <div>
              <Label htmlFor="iv-tier">奖励会员种类</Label>
              <select
                id="iv-tier"
                className="h-9 w-full rounded-lg border border-border bg-background px-2 text-sm"
                value={form.reward_tier}
                onChange={(e) => setForm({ ...form, reward_tier: e.target.value })}
              >
                <option value="">跟随「邀请奖励的会员种类」设置</option>
                {tierList.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                    {t.is_default ? '（默认档）' : ''}
                  </option>
                ))}
              </select>
              <p className="mt-1 text-xs text-muted-foreground">
                好友用这个码注册后，你获得的奖励按这里选定的种类发放。
              </p>
            </div>
            <div>
              <Label htmlFor="iv-exp">有效期至</Label>
              <Input
                id="iv-exp"
                type="datetime-local"
                value={form.expires}
                onChange={(e) => setForm({ ...form, expires: e.target.value })}
              />
              <p className="mt-1 text-xs text-muted-foreground">留空表示永不过期</p>
            </div>
            <Button className="w-full" loading={create.isPending} onClick={() => create.mutate()}>
              <Plus />
              创建
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

/** 会员概览：谁在用、还有多久到期 */
export function MemberOverview() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Users className="size-5 text-primary" />
          会员体系说明
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm text-muted-foreground">
        <p>
          档位不落库：用户表只存<strong className="text-foreground">会员到期时间</strong>，
          档位由当前时间实时判定。到期即自动降级，不需要任何定时任务，
          也不会出现「明明过期了还享受着会员配额」的情况。
        </p>
        <p className="flex items-start gap-2">
          <CalendarClock className="mt-0.5 size-4 shrink-0" />
          在「用户」标签里可以给单个用户加减会员天数，扣减到过去即立即到期。
        </p>
        <p>
          邀请码带来的奖励天数会叠加在剩余时间之上，连续兑换不会互相冲掉。
        </p>
      </CardContent>
    </Card>
  )
}
