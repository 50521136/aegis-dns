import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Mail } from 'lucide-react'

import { getMe } from '@/api/me'
import { api } from '@/api/client'
import { errorMessage } from '@/api/client'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { useToast } from '@/components/ui/toast'

/**
 * DailyReportCard 是「每日邮件日报」的订阅开关。
 *
 * 默认关闭：这是用户主动订阅的通知，不该默认替他勾上。
 * 打开后每天在管理员设置的时间收到前一天的数据汇总
 * （查询量、拦截量、缓存命中、平均延迟）。
 */
export function DailyReportCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const me = useQuery({ queryKey: ['me'], queryFn: getMe })

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api.put('/me/daily-report', { enabled }),
    onSuccess: (_r, enabled) => {
      toast.success(enabled ? '已开启日报推送' : '已关闭日报推送')
      qc.invalidateQueries({ queryKey: ['me'] })
    },
    onError: (err) => toast.error('设置失败', errorMessage(err)),
  })

  const on = Boolean(me.data?.daily_report)
  const hasEmail = Boolean(me.data?.email)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Mail className="size-4" />
          通知设置
        </CardTitle>
        <CardDescription>每日把前一天的解析数据汇总成一封邮件发给你。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between gap-3 rounded-xl border border-border/60 p-3">
          <div className="min-w-0">
            <Label htmlFor="daily-report" className="text-sm">
              每日邮件日报
            </Label>
            <p className="mt-1 text-xs text-muted-foreground">
              包含查询量、拦截量、缓存命中与平均延迟。默认关闭。
            </p>
          </div>
          <Switch
            id="daily-report"
            checked={on}
            disabled={toggle.isPending || !hasEmail}
            onCheckedChange={(v) => toggle.mutate(v)}
          />
        </div>
        {!hasEmail && (
          <p className="text-xs text-warning">
            还没有填写邮箱，填好之后才能接收日报。
          </p>
        )}
      </CardContent>
    </Card>
  )
}
