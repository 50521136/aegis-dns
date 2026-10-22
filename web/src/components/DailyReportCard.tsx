import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Mail, Send } from 'lucide-react'
import { useEffect, useState } from 'react'

import { getMe } from '@/api/me'
import { api, errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { useToast } from '@/components/ui/toast'

/**
 * DailyReportCard 是「每日邮件日报」的订阅开关 + 推送时间 + 测试推送。
 *
 * 默认关闭、默认 09:00：这是用户主动订阅的通知，不该默认替他勾上。
 * 推送时间由用户自己定，精确到分钟；管理员那边只有总开关，
 * 不再有全局时间 —— 两处都能改时间的话，谁生效说不清。
 */
export function DailyReportCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const me = useQuery({ queryKey: ['me'], queryFn: getMe })

  const saved = me.data?.report_time || '09:00'
  const [time, setTime] = useState(saved)
  // 服务端值变了才同步到输入框，否则用户正改到一半会被覆盖
  useEffect(() => {
    setTime(saved)
  }, [saved])

  const save = useMutation({
    mutationFn: (patch: { enabled?: boolean; time?: string }) =>
      api.put('/me/daily-report', patch),
    onSuccess: () => {
      toast.success('已保存')
      qc.invalidateQueries({ queryKey: ['me'] })
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const test = useMutation({
    mutationFn: () => api.post('/me/daily-report/test'),
    onSuccess: () => toast.success('测试邮件已发出', '去邮箱看看排版是否正常'),
    onError: (err) => toast.error('发送失败', errorMessage(err)),
  })

  const on = Boolean(me.data?.daily_report)
  const hasEmail = Boolean(me.data?.email)
  const timeChanged = time !== saved

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
            disabled={save.isPending || !hasEmail}
            onCheckedChange={(v) => save.mutate({ enabled: v })}
          />
        </div>

        <div className="flex flex-wrap items-end gap-3 rounded-xl border border-border/60 p-3">
          <div className="space-y-1">
            <Label htmlFor="report-time" className="text-sm">
              推送时间（北京时间）
            </Label>
            <Input
              id="report-time"
              type="time"
              value={time}
              disabled={save.isPending}
              className="h-9 w-32 font-mono"
              onChange={(e) => setTime(e.target.value)}
            />
          </div>
          <Button
            size="sm"
            variant="secondary"
            disabled={!timeChanged || save.isPending}
            loading={save.isPending}
            onClick={() => save.mutate({ time })}
          >
            保存时间
          </Button>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border/60 p-3">
          <p className="text-xs text-muted-foreground">
            先发一封看看效果？测试推送会立刻发到你邮箱。
          </p>
          <Button
            size="sm"
            variant="outline"
            disabled={!hasEmail || test.isPending}
            loading={test.isPending}
            onClick={() => test.mutate()}
          >
            <Send className="size-3.5" />
            测试推送
          </Button>
        </div>

        {!hasEmail && (
          <p className="text-xs text-warning">还没有填写邮箱，填好之后才能接收日报。</p>
        )}
      </CardContent>
    </Card>
  )
}
