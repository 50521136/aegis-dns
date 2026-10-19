import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { MessageSquareWarning, Send } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/EmptyState'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { createFeedback, listMyFeedback } from '@/api/scope'
import type { FeedbackKind, FeedbackStatus } from '@/api/types'
import { cn } from '@/lib/utils'

const KIND_OPTIONS: { value: FeedbackKind; label: string; hint: string }[] = [
  { value: 'wrong_block', label: '误拦了', hint: '这个域名不该被拦，但被规则挡住了' },
  { value: 'wrong_allow', label: '该拦没拦', hint: '这个域名应该被拦，但现在能正常解析' },
  { value: 'other', label: '其他', hint: '其他问题或建议' },
]

const STATUS_META: Record<FeedbackStatus, { label: string; variant: 'default' | 'success' | 'secondary' }> = {
  open: { label: '待处理', variant: 'default' },
  replied: { label: '已回复', variant: 'success' },
  closed: { label: '已结单', variant: 'secondary' },
}

export default function Feedback() {
  const qc = useQueryClient()
  const toast = useToast()

  const [domain, setDomain] = useState('')
  const [kind, setKind] = useState<FeedbackKind>('wrong_block')
  const [message, setMessage] = useState('')

  const listQuery = useQuery({ queryKey: ['feedback', 'mine'], queryFn: () => listMyFeedback() })
  const items = listQuery.data?.items ?? []

  const submit = useMutation({
    mutationFn: () => createFeedback({ domain: domain.trim(), kind, message: message.trim() }),
    onSuccess: () => {
      toast.success('已提交', '管理员处理后会在这里看到回复')
      setDomain('')
      setMessage('')
      qc.invalidateQueries({ queryKey: ['feedback', 'mine'] })
    },
    onError: (err) => toast.error('提交失败', errorMessage(err)),
  })

  const canSubmit = message.trim().length > 0 && !submit.isPending
  const hint = KIND_OPTIONS.find((o) => o.value === kind)?.hint

  return (
    <div className="space-y-6">
      <PageHeader
        title="反馈"
        description="规则拦错了或漏拦了，在这里告诉管理员"
        icon={<MessageSquareWarning className="size-5" />}
      />

      {/* ── 提交 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">提交反馈</CardTitle>
          <CardDescription>
            带上域名会更容易定位：管理员能直接对照规则与查询日志确认
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="fb-domain">域名（可选）</Label>
              <Input
                id="fb-domain"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
                placeholder="ads.example.com"
                className="font-mono"
                spellCheck={false}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fb-kind">问题类型</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as FeedbackKind)}>
                <SelectTrigger>
                  <SelectValue placeholder="选择类型" />
                </SelectTrigger>
                <SelectContent>
                  {KIND_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="fb-message">说明</Label>
            <textarea
              id="fb-message"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={4}
              maxLength={2000}
              placeholder={hint}
              className="w-full rounded-xl border border-border px-3 py-2 text-sm leading-relaxed text-foreground placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
              style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
            />
            <p className="text-right text-xs text-muted-foreground">{message.length} / 2000</p>
          </div>

          <Button onClick={() => submit.mutate()} disabled={!canSubmit} loading={submit.isPending}>
            <Send />
            提交
          </Button>
        </CardContent>
      </Card>

      {/* ── 我的反馈 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">我的反馈</CardTitle>
          <CardDescription>共 {listQuery.data?.total ?? 0} 条</CardDescription>
        </CardHeader>
        <CardContent>
          {listQuery.isLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner /> 读取中…
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              icon={<MessageSquareWarning className="size-6" />}
              title="还没有提交过反馈"
              description="遇到规则拦错或漏拦时，可以在上面提交"
            />
          ) : (
            <ul className="space-y-3">
              {items.map((f) => {
                const meta = STATUS_META[f.status] ?? STATUS_META.open
                return (
                  <li key={f.id} className="rounded-xl border border-border p-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={meta.variant}>{meta.label}</Badge>
                      <Badge variant="outline">
                        {KIND_OPTIONS.find((o) => o.value === f.kind)?.label ?? f.kind}
                      </Badge>
                      {f.domain && (
                        <code className="mono-addr text-xs text-muted-foreground">{f.domain}</code>
                      )}
                      <span className="ml-auto font-mono text-xs text-muted-foreground">
                        {f.created_at}
                      </span>
                    </div>
                    <p className="mt-2 whitespace-pre-wrap text-sm">{f.message}</p>
                    {f.admin_reply && (
                      <div
                        className={cn(
                          'mt-2 rounded-lg bg-primary/8 p-2.5 text-sm',
                          'border-l-2 border-primary',
                        )}
                      >
                        <p className="eyebrow mb-1">管理员回复</p>
                        <p className="whitespace-pre-wrap">{f.admin_reply}</p>
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
