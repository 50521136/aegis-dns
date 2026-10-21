import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CalendarCheck, Flame, Gift, Sparkles } from 'lucide-react'

import { doCheckin, getCheckin } from '@/api/member'
import { errorMessage } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'

/**
 * CheckinCard 是每日签到。
 *
 * 展示「还能连几天拿额外奖励」比只显示连续天数更有推动力：
 * 用户知道再签两天就有大奖，才会回来。
 */
export function CheckinCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['checkin'], queryFn: getCheckin })

  const m = useMutation({
    mutationFn: () => doCheckin(),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['checkin'] })
      qc.invalidateQueries({ queryKey: ['membership'] })
      qc.invalidateQueries({ queryKey: ['me'] })
      toast.success(
        res.granted > 0 ? `签到成功，+${res.granted} 天会员` : '签到成功',
        res.bonus > 0 ? res.bonus_message : `已连续签到 ${res.streak} 天`,
      )
    },
    onError: (err) => toast.error('签到失败', errorMessage(err)),
  })

  if (q.isLoading) return <Skeleton className="h-36 rounded-2xl" />
  if (q.isError || !q.data) return null

  const d = q.data
  // 后端没开签到就整块不显示，而不是给一个永远点不动的按钮
  if (!d.enabled) return null

  // 距离下一次累计奖励还差几天（按累计签到天数算，不要求连续）
  const untilBonus =
    d.streak_days > 0 ? d.streak_days - (d.total_days % d.streak_days) : 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <CalendarCheck className="size-5 text-primary" />
          每日签到
          <Badge variant="secondary">+{d.daily_days} 天/次</Badge>
        </CardTitle>
        <CardDescription>
          {d.streak_days > 0 && d.streak_bonus > 0
            ? `累计签到每 ${d.streak_days} 天额外送 ${d.streak_bonus} 天`
            : '每天签到一次即可获得会员天数'}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid grid-cols-2 gap-2 min-[360px]:grid-cols-3">
          <Stat icon={<Flame className="size-3.5" />} label="连续签到" value={`${d.streak} 天`} />
          <Stat icon={<Gift className="size-3.5" />} label="累计签到" value={`${d.total_days} 天`} />
          <Stat icon={<Sparkles className="size-3.5" />} label="会员剩余" value={`${d.member_days} 天`} />
        </div>

        {d.streak_days > 0 && d.streak_bonus > 0 && !d.checked_today && (
          <p className="text-xs text-muted-foreground">
            {untilBonus === 0
              ? `今天签到就触发累计奖励，额外 +${d.streak_bonus} 天`
              : `再累计签到 ${untilBonus} 天可额外获得 ${d.streak_bonus} 天`}
          </p>
        )}

        <Button
          className="w-full"
          disabled={d.checked_today}
          loading={m.isPending}
          onClick={() => m.mutate()}
        >
          <CalendarCheck />
          {d.checked_today ? `今天已签到${d.granted_today > 0 ? `（+${d.granted_today} 天）` : ''}` : '立即签到'}
        </Button>
      </CardContent>
    </Card>
  )
}

function Stat({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <div className="rounded-xl border border-border/60 px-2 py-2 text-center">
      <p className="flex items-center justify-center gap-1 font-mono text-sm font-semibold tabular-nums">
        {icon}
        {value}
      </p>
      <p className="mt-0.5 text-xs text-muted-foreground">{label}</p>
    </div>
  )
}
