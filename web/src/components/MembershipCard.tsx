import { useQuery } from '@tanstack/react-query'
import { Crown, Gift, Zap } from 'lucide-react'

import { getMyMembership } from '@/api/member'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { formatNumber } from '@/lib/format'

/**
 * MembershipCard 展示当前档位与两档配额差异。
 *
 * 刻意把「免费档 vs 会员档」并排列出来：只显示自己那一档的话，
 * 用户永远不知道自己错过了什么，会员体系也就失去了引导作用。
 */
export function MembershipCard() {
  const q = useQuery({ queryKey: ['membership'], queryFn: getMyMembership })

  if (q.isLoading) return <Skeleton className="h-44 rounded-2xl" />
  if (q.isError || !q.data) return null

  const m = q.data
  const isVip = m.tier === 'vip'
  const rows = [
    { label: '自定义规则', free: m.tiers.free.max_rules, vip: m.tiers.vip.max_rules, mine: m.quota.max_rules },
    { label: '自定义订阅', free: m.tiers.free.max_subs, vip: m.tiers.vip.max_subs, mine: m.quota.max_subs },
    {
      label: '每日请求量',
      free: m.tiers.free.daily_queries,
      vip: m.tiers.vip.daily_queries,
      mine: m.quota.daily_queries,
    },
  ]

  return (
    <Card className={cn(isVip && 'border-primary/40')}>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {isVip ? <Crown className="size-5 text-primary" /> : <Zap className="size-5 text-muted-foreground" />}
          我的账户
          {isVip ? (
            <Badge variant="success">会员 · 剩余 {m.member_days} 天</Badge>
          ) : (
            <Badge variant="secondary">普通用户</Badge>
          )}
        </CardTitle>
        <CardDescription>
          {isVip
            ? '会员配额已生效，到期后自动降为普通用户，无需手动操作。'
            : '邀请好友注册可获得会员天数，会员享受更高的规则、订阅与请求量上限。'}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="overflow-hidden rounded-xl border border-border">
          <div className="grid grid-cols-3 gap-px bg-border text-xs">
            <div className="bg-muted/40 px-3 py-1.5 font-medium">配额项</div>
            <div className="bg-muted/40 px-3 py-1.5 font-medium">普通用户</div>
            <div className="bg-primary/10 px-3 py-1.5 font-medium text-primary">会员</div>
          </div>
          {rows.map((r) => (
            <div key={r.label} className="grid grid-cols-3 gap-px border-t border-border bg-border text-sm">
              <div className="flex items-center justify-between bg-background px-3 py-1.5">
                <span>{r.label}</span>
                <span className="font-mono text-xs text-muted-foreground">
                  当前 {r.mine === 0 ? '不限' : formatNumber(r.mine)}
                </span>
              </div>
              <div className="bg-background px-3 py-1.5 font-mono text-sm">
                {r.free === 0 ? '不限' : formatNumber(r.free)}
              </div>
              <div className="bg-background px-3 py-1.5 font-mono text-sm text-primary">
                {r.vip === 0 ? '不限' : formatNumber(r.vip)}
              </div>
            </div>
          ))}
        </div>

        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Gift className="size-3.5" />
          已通过邀请带来 {m.invite.total_uses} 位用户，累计获得 {m.invite.reward_days} 天会员
        </p>
      </CardContent>
    </Card>
  )
}
