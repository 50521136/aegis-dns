import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCheck, MessageSquare, MessageSquareWarning, Send } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { AiDiagnosisPanel } from '@/components/AiDiagnosisPanel'
import { FeedbackThread } from '@/components/FeedbackThread'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/EmptyState'
import { Spinner } from '@/components/ui/spinner'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { listAllFeedback, replyFeedback } from '@/api/scope'
import type { Feedback, FeedbackStatus } from '@/api/types'
import { cn } from '@/lib/utils'

const STATUS_TABS: { value: FeedbackStatus | ''; label: string }[] = [
  { value: 'open', label: '待处理' },
  { value: 'replied', label: '已回复' },
  { value: 'closed', label: '已结单' },
  { value: '', label: '全部' },
]

const STATUS_META: Record<FeedbackStatus, { label: string; variant: 'default' | 'success' | 'secondary' }> = {
  open: { label: '待处理', variant: 'default' },
  replied: { label: '已回复', variant: 'success' },
  closed: { label: '已结单', variant: 'secondary' },
}

const KIND_LABEL: Record<string, string> = {
  wrong_block: '误拦了',
  wrong_allow: '该拦没拦',
  other: '其他',
}

export default function AdminFeedback() {
  const qc = useQueryClient()
  const toast = useToast()

  const [status, setStatus] = useState<FeedbackStatus | ''>('open')
  const [replyingId, setReplyingId] = useState<string | null>(null)
  const [replyText, setReplyText] = useState('')

  const listQuery = useQuery({
    queryKey: ['admin', 'feedback', status],
    queryFn: () => listAllFeedback({ status: status || undefined, page_size: 50 }),
  })
  const items = listQuery.data?.items ?? []
  const openCount = listQuery.data?.open_count ?? 0

  const reply = useMutation({
    mutationFn: ({ id, text, next }: { id: string; text: string; next?: FeedbackStatus }) =>
      replyFeedback(id, { reply: text, status: next }),
    onSuccess: () => {
      toast.success('已保存')
      setReplyingId(null)
      setReplyText('')
      qc.invalidateQueries({ queryKey: ['admin', 'feedback'] })
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const close = (f: Feedback) => reply.mutate({ id: f.id, text: '', next: 'closed' })

  const startReply = (f: Feedback) => {
    setReplyingId(f.id)
    setReplyText(f.admin_reply)
  }

  return (
    <div className="min-w-0 space-y-6">
      <PageHeader
        title="反馈处理"
        description="用户提交的误拦 / 漏拦申诉；回复后用户可在「反馈」页看到"
        icon={<MessageSquareWarning className="size-5" />}
        actions={
          openCount > 0 ? <Badge variant="default">{openCount} 条待处理</Badge> : undefined
        }
      />

      {/* 状态筛选：用玻璃分段控件而不是下拉，一眼看到各状态的量 */}
      <div className="m-groove inline-flex flex-wrap gap-1 rounded-xl p-1">
        {STATUS_TABS.map((t) => (
          <button
            key={t.value || 'all'}
            onClick={() => setStatus(t.value)}
            className={cn(
              'rounded-lg px-3 py-1.5 text-sm transition-colors',
              status === t.value
                ? 'm-pill font-medium text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {STATUS_TABS.find((t) => t.value === status)?.label ?? '全部'}反馈
          </CardTitle>
          <CardDescription>共 {listQuery.data?.total ?? 0} 条</CardDescription>
        </CardHeader>
        <CardContent>
          {listQuery.isLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner /> 读取中…
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              icon={<MessageSquare className="size-6" />}
              title="没有反馈"
              description="这个状态下暂时没有需要处理的内容"
            />
          ) : (
            <ul className="min-w-0 space-y-3">
              {items.map((f) => {
                const meta = STATUS_META[f.status] ?? STATUS_META.open
                const editing = replyingId === f.id
                return (
                  <li key={f.id} className="min-w-0 rounded-xl border border-border p-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <Badge variant={meta.variant}>{meta.label}</Badge>
                      <Badge variant="outline">{KIND_LABEL[f.kind] ?? f.kind}</Badge>
                      <span className="min-w-0 truncate text-sm font-medium">{f.username}</span>
                      <span className="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
                        {f.created_at}
                      </span>
                    </div>
                    {/* 域名单独一行并 break-all：挤在头部 flex 里时，
                        长域名会把时间挤出屏幕（手机端整页横向滚动）。 */}
                    {f.domain && (
                      <code className="mono-addr mt-2 block break-all text-xs text-muted-foreground">
                        {f.domain}
                      </code>
                    )}

                    {/* 时间线：一条反馈一串消息。
                        此前正文与回复分成两块显示，用户追问又会变成新的一条反馈，
                        列表里同一个问题散成好几条，结单时只能挨个翻。 */}
                    <FeedbackThread
                      messages={f.messages ?? []}
                      fallback={
                        f.messages?.length
                          ? undefined
                          : [
                              {
                                role: 'user' as const,
                                content: f.message,
                                created_at: f.created_at,
                                author: f.username,
                              },
                              ...(f.admin_reply
                                ? [
                                    {
                                      role: 'admin' as const,
                                      content: f.admin_reply,
                                      created_at: f.updated_at,
                                      author: '管理员',
                                    },
                                  ]
                                : []),
                            ]
                      }
                      className="mt-2"
                    />

                    {editing ? (
                      <div className="mt-3 space-y-2">
                        <textarea
                          value={replyText}
                          onChange={(e) => setReplyText(e.target.value)}
                          rows={3}
                          maxLength={2000}
                          placeholder="回复内容（用户可见）"
                          className="w-full rounded-xl border border-border px-3 py-2 text-sm leading-relaxed placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
                          style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
                        />
                        <div className="flex flex-wrap gap-2">
                          <Button
                            size="sm"
                            loading={reply.isPending}
                            disabled={replyText.trim().length === 0}
                            onClick={() => reply.mutate({ id: f.id, text: replyText.trim() })}
                          >
                            <Send />
                            发送回复
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => setReplyingId(null)}>
                            取消
                          </Button>
                        </div>
                      </div>
                    ) : (
                      <div className="mt-2 flex flex-wrap items-center gap-2">
                        <Button size="sm" variant="outline" onClick={() => startReply(f)}>
                          <MessageSquare />
                          {f.admin_reply ? '修改回复' : '回复'}
                        </Button>
                        {f.status !== 'closed' && (
                          <Button size="sm" variant="ghost" onClick={() => close(f)}>
                            <CheckCheck />
                            结单
                          </Button>
                        )}
                      </div>
                    )}

                    {/* AI 诊断结论：只在还没结单时显示，已结单的没必再占版面 */}
                    {f.status !== 'closed' && (
                      <AiDiagnosisPanel
                        feedback={f}
                        onDone={() => qc.invalidateQueries({ queryKey: ['admin', 'feedback'] })}
                      />
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
