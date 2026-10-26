import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Bot,
  CheckCheck,
  ChevronRight,
  MessageSquare,
  MessageSquareWarning,
  Send,
  UserRound,
} from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { AiDiagnosisPanel } from '@/components/AiDiagnosisPanel'
import { FeedbackThread } from '@/components/FeedbackThread'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/EmptyState'
import { Spinner } from '@/components/ui/spinner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { adminMarkFeedbackRead } from '@/api/notifications'
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

/**
 * 管理端反馈页。
 *
 * 需求原文：「反馈的条目，要不只显示反馈的列表，谁，类型，是否解决等，
 * 点开可以二级处理回复那些的，这样就不会占用太多位置了」
 *
 * 所以这里从「每条反馈展开完整时间线 + 回复框 + AI 面板」改成了：
 *   - 列表只显示一行摘要：谁、类型、域名、状态、AI 标识、时间；
 *   - 点开弹出二级详情：完整时间线、AI 诊断结论、回复输入、结单按钮。
 *
 * 为什么用弹窗而不是第二页：处理一条反馈要来回看「用户说了什么」和
 * 「AI 怎么判的」，跳到独立页面后返回会丢掉当前的筛选与滚动位置，
 * 一次处理十条就要重新找到位置十次。
 */
export default function AdminFeedback() {
  const qc = useQueryClient()
  const toast = useToast()

  const [status, setStatus] = useState<FeedbackStatus | ''>('open')
  const [activeId, setActiveId] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: ['admin', 'feedback', status],
    queryFn: () => listAllFeedback({ status: status || undefined, page_size: 50 }),
  })
  const items = listQuery.data?.items ?? []
  const openCount = listQuery.data?.open_count ?? 0

  // 从列表里取当前打开的那条：不额外请求，也不再存一份副本
  // （列表刷新后详情跟着更新，不需要手动同步）。
  const active = useMemo(() => items.find((f) => f.id === activeId) ?? null, [items, activeId])

  const openDetail = (f: Feedback) => {
    setActiveId(f.id)
    // 打开即视为「管理员已看过」：消掉管理端红点。
    // 失败不打扰（红点下次刷新还会在，不是数据损坏）。
    void adminMarkFeedbackRead(f.id).catch(() => undefined)
  }

  return (
    <div className="min-w-0 space-y-5">
      <PageHeader
        title="反馈处理"
        description="用户提交的误拦 / 漏拦申诉；点开条目可查看对话并回复"
        icon={<MessageSquareWarning className="size-5" />}
        actions={openCount > 0 ? <Badge variant="default">{openCount} 条待处理</Badge> : undefined}
      />

      <div className="m-groove inline-flex flex-wrap gap-1 rounded-xl p-1">
        {STATUS_TABS.map((t) => (
          <button
            key={t.value || 'all'}
            onClick={() => setStatus(t.value)}
            className={cn(
              'min-h-8 rounded-lg px-3 text-sm transition-colors',
              status === t.value
                ? 'm-pill font-medium text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {listQuery.isLoading ? (
        <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
          <Spinner /> 读取中…
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<MessageSquare className="size-6" />}
          title="没有反馈"
          description="这个状态下暂时没有需要处理的内容"
        />
      ) : (
        <ul className="min-w-0 divide-y divide-border/60 overflow-hidden rounded-xl border border-border/70">
          {items.map((f) => (
            <FeedbackRow key={f.id} f={f} onOpen={() => openDetail(f)} />
          ))}
        </ul>
      )}

      <FeedbackDetailDialog
        feedback={active}
        onClose={() => setActiveId(null)}
        onChanged={() => qc.invalidateQueries({ queryKey: ['admin', 'feedback'] })}
        onToast={(t) => toast.success(t)}
        onErr={(t, m) => toast.error(t, m)}
      />
    </div>
  )
}

/** FeedbackRow 是列表里的一行摘要：谁 / 类型 / 域名 / 状态 / 时间。 */
function FeedbackRow({ f, onOpen }: { f: Feedback; onOpen: () => void }) {
  const meta = STATUS_META[f.status] ?? STATUS_META.open
  const aiHandled = (f.ai_answered_at ?? 0) > 0
  const needHuman = (f.needs_human_at ?? 0) > 0
  // 管理员未读：用户最后一条发言晚于 admin_read_at，或 AI 要求人工。
  const unread = needHuman || lastUserTs(f) > (f.admin_read_at ?? 0)

  return (
    <li className="min-w-0">
      <button
        type="button"
        onClick={onOpen}
        className={cn(
          'flex min-h-11 w-full min-w-0 items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/40',
          unread && 'bg-danger/3',
        )}
      >
        {/* 未读用左侧竖线，不打乱行内对齐 */}
        <span
          className={cn(
            'h-6 w-0.5 shrink-0 rounded-full',
            unread ? 'bg-danger' : 'bg-transparent',
          )}
        />
        <UserRound className="size-3.5 shrink-0 text-muted-foreground" />
        <span
          className={cn(
            'shrink-0 text-[13px]',
            unread ? 'font-semibold text-foreground' : 'font-medium text-foreground/85',
          )}
        >
          {f.username || f.user_id.slice(0, 8)}
        </span>
        <Badge variant="outline" className="shrink-0 text-[10px]">
          {KIND_LABEL[f.kind] ?? f.kind}
        </Badge>
        {/* 域名是这行里唯一可能很长的字段：给它 flex-1 + truncate，
            挤压时只牺牲它，不把状态和时间顶出屏幕 */}
        <code className="mono-addr min-w-0 flex-1 truncate text-[11.5px] text-muted-foreground">
          {f.domain || '（未填域名）'}
        </code>
        {aiHandled && (
          <Badge variant="secondary" className="shrink-0 gap-0.5 text-[10px]">
            <Bot className="size-2.5" />
            AI
          </Badge>
        )}
        {needHuman && (
          <Badge variant="default" className="shrink-0 text-[10px]">
            待人工
          </Badge>
        )}
        <Badge variant={meta.variant} className="shrink-0 text-[10px]">
          {meta.label}
        </Badge>
        <span className="hidden shrink-0 font-mono text-[10.5px] tabular-nums text-muted-foreground/70 sm:inline">
          {shortTime(f.created_at)}
        </span>
        <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/50" />
      </button>
    </li>
  )
}

/** FeedbackDetailDialog 是二级详情：时间线 + AI 结论 + 回复 + 结单。 */
function FeedbackDetailDialog({
  feedback,
  onClose,
  onChanged,
  onToast,
  onErr,
}: {
  feedback: Feedback | null
  onClose: () => void
  onChanged: () => void
  onToast: (t: string) => void
  onErr: (t: string, m: string) => void
}) {
  const [replyText, setReplyText] = useState('')

  const reply = useMutation({
    mutationFn: ({ id, text, next }: { id: string; text: string; next?: FeedbackStatus }) =>
      replyFeedback(id, { reply: text, status: next }),
    onSuccess: () => {
      onToast('已保存')
      setReplyText('')
      onChanged()
    },
    onError: (err) => onErr('保存失败', errorMessage(err)),
  })

  if (!feedback) return null
  const f = feedback
  const meta = STATUS_META[f.status] ?? STATUS_META.open
  const aiHandled = (f.ai_answered_at ?? 0) > 0

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="inset-x-3 max-h-[88dvh] gap-2 rounded-2xl p-3.5 pb-[calc(1rem+env(safe-area-inset-bottom))] md:inset-x-auto md:left-1/2 md:w-[calc(100%-2rem)] md:max-w-2xl md:translate-x-[calc(-50%+5px)] md:p-4">
        <DialogHeader className="gap-1">
          <DialogTitle className="flex flex-wrap items-center gap-2 break-all text-[14px]">
            <span className="min-w-0 break-all">{f.domain || '（未填域名）'}</span>
            <Badge variant={meta.variant} className="shrink-0">
              {meta.label}
            </Badge>
            <Badge variant="outline" className="shrink-0">
              {KIND_LABEL[f.kind] ?? f.kind}
            </Badge>
            {aiHandled && (
              <Badge variant="secondary" className="shrink-0 gap-0.5">
                <Bot className="size-2.5" />
                AI 自动处理
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px]">
            <span className="text-foreground/80">{f.username || f.user_id.slice(0, 8)}</span>
            <span className="text-border">·</span>
            <span className="font-mono tabular-nums">{f.created_at}</span>
          </DialogDescription>
        </DialogHeader>

        {/* 时间线：用户与管理员（含 AI）往返的全部消息 */}
        <div className="min-h-0 flex-1 overflow-y-auto">
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
                            author: aiHandled ? 'AI 助手' : '管理员',
                          },
                        ]
                      : []),
                  ]
            }
          />

          {/* AI 诊断结论：已结单且是 AI 自动处理的不再显示面板（没意义），
              但保留结论让人能复盘「AI 当时怎么判的」。 */}
          {f.status !== 'closed' && (
            <AiDiagnosisPanel feedback={f} onDone={onChanged} />
          )}
        </div>

        <div className="space-y-2 border-t border-border/50 pt-2.5">
          <textarea
            value={replyText}
            onChange={(e) => setReplyText(e.target.value)}
            rows={2}
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
            {f.status !== 'closed' && (
              <Button
                size="sm"
                variant="outline"
                loading={reply.isPending}
                onClick={() => reply.mutate({ id: f.id, text: replyText.trim(), next: 'closed' })}
              >
                <CheckCheck />
                回复并结单
              </Button>
            )}
            {f.status !== 'closed' && replyText.trim().length === 0 && (
              <Button size="sm" variant="ghost" onClick={() => reply.mutate({ id: f.id, text: '', next: 'closed' })}>
                直接结单
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

/** lastUserTs 返回时间线上最后一条用户发言的时间戳（秒），无则 0。 */
function lastUserTs(f: Feedback): number {
  const msgs = f.messages ?? []
  let last = 0
  for (const m of msgs) {
    if (m.role !== 'user') continue
    const t = Math.floor(new Date(m.created_at).getTime() / 1000)
    if (t > last) last = t
  }
  // 时间线缺失时退回创建时间。
  if (last === 0) {
    const t = Math.floor(new Date(f.created_at).getTime() / 1000)
    return Number.isNaN(t) ? 0 : t
  }
  return last
}

/** shortTime 把 RFC3339 化成「10-25 14:03」。 */
function shortTime(raw: string): string {
  if (!raw) return ''
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}-${dd} ${hh}:${mi}`
}
