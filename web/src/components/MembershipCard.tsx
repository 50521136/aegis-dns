import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Crown, Gift, Info, Zap } from 'lucide-react'

import { getMyMembership } from '@/api/member'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import { formatDateTime, formatNumber } from '@/lib/format'

/**
 * MembershipCard 是「我的账户」里的会员卡片。
 *
 * 这里**只显示用户自己的状态和套餐**。之前把「配额项 / 普通用户 / 会员」三列
 * 全档位对比表摆在首页，用户一进来先看到一堆与自己无关的数字，真正要看的
 * 「我现在是什么、还剩多久」反而要去找。
 *
 * 各档位的差异没有消失，收进右侧那个小图标后面的说明弹窗里 —— 想知道细节
 * 点一下就有，不想知道也不占版面。
 */
export function MembershipCard() {
  const q = useQuery({ queryKey: ['membership'], queryFn: getMyMembership })
  const [helpOpen, setHelpOpen] = useState(false)

  if (q.isLoading) return <Skeleton className="h-32 rounded-2xl" />
  if (q.isError || !q.data) return null

  const m = q.data
  const isMember = m.is_member

  return (
    <Card className={cn(isMember && 'border-primary/40')}>
      <CardHeader className="pb-3">
        <CardTitle className="flex flex-wrap items-center gap-2">
          {isMember ? (
            <Crown className="size-5 text-primary" />
          ) : (
            <Zap className="size-5 text-muted-foreground" />
          )}
          我的账户
          {isMember ? (
            <Badge variant="success">
              {m.member_forever ? '永久会员' : `剩 ${m.member_days} 天`}
            </Badge>
          ) : (
            <Badge variant="secondary">未开通</Badge>
          )}
        </CardTitle>
      </CardHeader>

      <CardContent className="space-y-3">
        <div className="flex items-center justify-between gap-3 rounded-xl border border-border px-3 py-2.5">
          <div className="min-w-0">
            <p className="text-[11px] text-muted-foreground">当前套餐</p>
            <p className="truncate text-sm font-medium">{m.tier_name}</p>
          </div>
          <button
            type="button"
            aria-label="各会员种类说明"
            onClick={() => setHelpOpen(true)}
            className="shrink-0 rounded-full p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <Info className="size-4" />
          </button>
        </div>

        <div className="flex items-baseline justify-between gap-2 px-1">
          <span className="text-[11px] text-muted-foreground">到期时间</span>
          <span className="font-mono text-xs">
            {m.member_forever
              ? '永久有效'
              : isMember
                ? formatDateTime(m.member_until)
                : '—'}
          </span>
        </div>

        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Gift className="size-3.5 shrink-0" />
          已通过邀请带来 {m.invite.total_uses} 位用户，累计获得 {m.invite.reward_days} 天会员
        </p>
      </CardContent>

      {/* 各会员种类说明：小弹窗、小字号，只解释差异，不做任何操作 */}
      <Dialog open={helpOpen} onOpenChange={setHelpOpen}>
        <DialogContent className="max-w-[19rem] gap-2 p-4 md:max-w-[19rem]">
          <DialogHeader className="gap-1">
            <DialogTitle className="text-sm">会员种类</DialogTitle>
            <DialogDescription className="text-[11px] leading-relaxed">
              每种会员一套配额，数值越大越宽松，「不限」表示不做限制。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-1.5">
            {m.tier_list.map((t) => {
              const mine = t.id === m.tier
              return (
                <div
                  key={t.id}
                  className={cn(
                    'rounded-lg border px-2.5 py-1.5',
                    mine ? 'border-primary/40 bg-primary/5' : 'border-border/60',
                  )}
                >
                  <p className="flex items-center gap-1.5 text-[12px] font-medium">
                    {t.name}
                    {mine && (
                      <span className="rounded bg-primary/15 px-1 py-px text-[10px] text-primary">
                        当前
                      </span>
                    )}
                    {t.is_default && (
                      <span className="text-[10px] font-normal text-muted-foreground">默认档</span>
                    )}
                  </p>
                  <p className="mt-0.5 font-mono text-[10px] leading-relaxed text-muted-foreground">
                    规则 {t.max_rules ? formatNumber(t.max_rules) : '不限'} · 订阅{' '}
                    {t.max_subs ? formatNumber(t.max_subs) : '不限'} · 每日请求{' '}
                    {t.daily_queries ? formatNumber(t.daily_queries) : '不限'}
                  </p>
                </div>
              )
            })}
          </div>

          <p className="text-[10px] leading-relaxed text-muted-foreground">
            会员种类只在有效期内生效，到期自动回到默认档，无需手动操作。
          </p>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
