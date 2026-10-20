import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Gift, Link2, Plus, Share2, Trash2, Users } from 'lucide-react'

import { createMyInviteCode, deleteMyInviteCode, getMyInviteCodes } from '@/api/member'
import { errorMessage } from '@/api/client'
import type { InviteCode } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { formatNumber } from '@/lib/format'

/**
 * InviteCard 是普通用户的拉新入口。
 *
 * 关键点：用户能自己生成邀请码和分享链接。
 * 只有管理员能发码的话，这套激励就退化成「管理员发福利」——
 * 用户没有动力去分享，拉新这件事根本转不起来。
 *
 * 但奖励天数不给用户填：能自选天数等于能给自己无限续会员，
 * 一律用站点配置的固定值。
 */
export function InviteCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const [copied, setCopied] = useState<string | null>(null)

  const q = useQuery({ queryKey: ['me', 'invite-codes'], queryFn: getMyInviteCodes })
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['me', 'invite-codes'] })
    qc.invalidateQueries({ queryKey: ['membership'] })
  }

  const create = useMutation({
    mutationFn: () => createMyInviteCode(),
    onSuccess: () => {
      invalidate()
      toast.success('邀请码已生成', '把链接发给好友，注册后你会自动获得会员天数')
    },
    onError: (err) => toast.error('生成失败', errorMessage(err)),
  })

  const remove = useMutation({
    mutationFn: (code: string) => deleteMyInviteCode(code),
    onSuccess: () => {
      invalidate()
      toast.success('邀请码已删除')
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const copy = async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(text)
      setTimeout(() => setCopied(null), 1500)
      toast.success('已复制', label)
    } catch {
      toast.error('复制失败', '请手动选中复制')
    }
  }

  if (q.isLoading) return <Skeleton className="h-52 rounded-2xl" />
  if (q.isError || !q.data) return null

  const d = q.data
  const items = d.items ?? []
  const primary = items[0] as (InviteCode & { link?: string }) | undefined

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Gift className="size-5 text-primary" />
          邀请好友
          <Badge variant="success">每人 +{d.reward_days} 天会员</Badge>
        </CardTitle>
        <CardDescription>
          把邀请链接发给好友，对方用它注册后，你立即获得 {d.reward_days} 天会员，可叠加。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* 战绩 */}
        <div className="grid grid-cols-3 gap-2">
          <Stat label="已邀请" value={formatNumber(d.invited)} suffix="人" />
          <Stat label="累计获得" value={formatNumber(d.earned_days)} suffix="天" />
          <Stat
            label="当前会员"
            value={d.member_days > 0 ? formatNumber(d.member_days) : '—'}
            suffix={d.member_days > 0 ? '天' : ''}
          />
        </div>

        {/* 主链接：有码就直接给链接，没码就给生成按钮 */}
        {primary ? (
          <div className="space-y-2">
            <p className="eyebrow">我的邀请链接</p>
            <div className="flex min-w-0 items-center gap-2 rounded-xl border border-border bg-muted/30 px-3 py-2">
              <Link2 className="size-4 shrink-0 text-muted-foreground" />
              <code className="min-w-0 flex-1 truncate font-mono text-[13px]">
                {primary.link ?? primary.code}
              </code>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => copy(primary.link ?? primary.code, primary.link ?? primary.code)}
              >
                {copied === primary.link ? <Check /> : <Copy />}
                复制
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() =>
                  copy(
                    `我在用这个加密 DNS，无广告、可自定义规则。用我的链接注册：${primary.link ?? primary.code}`,
                    '邀请文案',
                  )
                }
              >
                <Share2 />
                复制邀请文案
              </Button>
              {d.can_create && (
                <Button size="sm" variant="ghost" loading={create.isPending} onClick={() => create.mutate()}>
                  <Plus />
                  再生成一个
                </Button>
              )}
            </div>
          </div>
        ) : (
          <div className="rounded-xl border border-dashed border-border p-4 text-center">
            {d.enabled ? (
              <>
                <p className="text-sm text-muted-foreground">还没有邀请码，生成一个就能分享给好友</p>
                <Button className="mt-3" loading={create.isPending} onClick={() => create.mutate()}>
                  <Plus />
                  生成我的邀请链接
                </Button>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">本站未开放用户自建邀请码</p>
            )}
          </div>
        )}

        {/* 其余邀请码 */}
        {items.length > 1 && (
          <div className="space-y-1.5">
            <p className="eyebrow">全部邀请码</p>
            {items.map((c: InviteCode & { link?: string }) => (
              <div
                key={c.code}
                className="flex min-w-0 items-center gap-2 rounded-lg border border-border/60 px-3 py-1.5"
              >
                <code className="shrink-0 font-mono text-xs font-medium">{c.code}</code>
                <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                  已邀请 {c.used_count} 人
                </span>
                {!c.usable && <Badge variant="secondary">{c.unusable_reason ?? '不可用'}</Badge>}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => copy(c.link ?? c.code, c.code)}
                  aria-label="复制链接"
                >
                  <Copy />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="text-danger hover:bg-danger/10"
                  loading={remove.isPending && remove.variables === c.code}
                  onClick={() => remove.mutate(c.code)}
                  aria-label="删除"
                >
                  <Trash2 />
                </Button>
              </div>
            ))}
          </div>
        )}

        {d.max_codes > 0 && (
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Users className="size-3.5" />
            最多可持有 {d.max_codes} 个邀请码，当前 {items.length} 个
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function Stat({ label, value, suffix }: { label: string; value: string; suffix?: string }) {
  return (
    <div className="rounded-xl border border-border/60 px-3 py-2 text-center">
      <p className="font-mono text-lg font-semibold tabular-nums text-foreground">
        {value}
        {suffix && <span className="ml-0.5 text-xs font-normal text-muted-foreground">{suffix}</span>}
      </p>
      <p className="text-xs text-muted-foreground">{label}</p>
    </div>
  )
}
