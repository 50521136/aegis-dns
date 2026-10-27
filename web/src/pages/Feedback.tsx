import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Bot,
  ChevronRight,
  MessageSquare,
  MessageSquareWarning,
  Send,
} from 'lucide-react'
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FeedbackThread } from '@/components/FeedbackThread'
import { errorMessage } from '@/api/client'
import { createFeedback, listMyFeedback, replyMyFeedback } from '@/api/scope'
import { markFeedbackRead } from '@/api/notifications'
import { useUnreadCounts } from '@/hooks/useUnreadCounts'
import { getPublicConfig } from '@/api/member'
import { GeetestCaptcha, useCaptchaGate } from '@/components/GeetestCaptcha'
import type { CaptchaResult, Feedback, FeedbackKind, FeedbackStatus } from '@/api/types'
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

/**
 * 用户侧反馈页。
 *
 * 结构与管理端一致（列表摘要 + 点开二级弹窗）：
 *   - 上面保留提交表单 —— 用户来这里的第一件事往往是提新问题；
 *   - 下面「我的反馈」只显示一行摘要（类型 / 域名 / AI 标识 / 状态 / 时间），
 *     点开才展开完整时间线与追问框。
 *
 * 为什么不再每条都展开时间线：反馈一多，一屏十几条各自铺着气泡和输入框，
 * 页面会长到没法用，而用户实际只关心「有没有新回复」。
 */
export default function Feedback() {
  const qc = useQueryClient()
  const toast = useToast()

  const [domain, setDomain] = useState('')
  const [kind, setKind] = useState<FeedbackKind>('wrong_block')
  const [message, setMessage] = useState('')
  const [activeId, setActiveId] = useState<string | null>(null)

  // 标记已读后要立刻刷新底部红点（见下方 effect）。
  const { refresh: refreshUnread } = useUnreadCounts()

  const listQuery = useQuery({ queryKey: ['feedback', 'mine'], queryFn: () => listMyFeedback() })
  const items = listQuery.data?.items ?? []

  // 从列表里取当前打开的那条：不额外请求，也不再存一份副本
  // （列表刷新后详情跟着更新，不需要手动同步）。
  const active = useMemo(() => items.find((f) => f.id === activeId) ?? null, [items, activeId])

  // 进到这个页面就说明用户在看自己的反馈列表了 —— 把「有回复未读」的
  // 逐条标记为已读，红点随之消失。
  //
  // 为什么不是点开某条才标记：用户的习惯是到这一页扫一眼列表就知道
  // 有没有新回复，逼他逐条点开才能消红点只会让人干脆无视红点。
  useEffect(() => {
    const unread = (listQuery.data?.items ?? []).filter(
      (f) => lastAdminTs(f) > (f.user_read_at ?? 0),
    )
    if (unread.length === 0) return
    void Promise.all(unread.map((f) => markFeedbackRead(f.id).catch(() => undefined)))
      // 标记完要立刻刷新未读数：不刷的话底部红点会在这个页面停着不动，
      // 看着像「进了页面也没消」，用户会反复点。
      .then(() => refreshUnread())
  }, [listQuery.data, refreshUnread])

  const cfgQuery = useQuery({ queryKey: ['public', 'config'], queryFn: getPublicConfig, staleTime: 60000 })
  const gate = useCaptchaGate(cfgQuery.data?.captcha, 'feedback')

  const submit = useMutation({
    mutationFn: (captcha?: CaptchaResult) =>
      createFeedback({ domain: domain.trim(), kind, message: message.trim(), captcha }),
    onSuccess: () => {
      toast.success('已提交', '处理后会在这里看到回复')
      setDomain('')
      setMessage('')
      qc.invalidateQueries({ queryKey: ['feedback', 'mine'] })
    },
    onError: (err) => toast.error('提交失败', errorMessage(err)),
  })

  const canSubmit = message.trim().length > 0 && !submit.isPending
  const hint = KIND_OPTIONS.find((o) => o.value === kind)?.hint

  return (
    <div className="min-w-0 space-y-5">
      <PageHeader
        title="反馈"
        description="规则拦错了或漏拦了，在这里告诉管理员"
        icon={<MessageSquareWarning className="size-5" />}
        actions={items.length > 0 ? <Badge variant="secondary">{items.length} 条</Badge> : undefined}
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
          {/* min-w-0 是关键：Grid/Flex 子项默认 min-width:auto，不会收缩到
              内容以下。长域名会把整列撑破，在手机上表现为整页可以左右滑
              （就是「数据跑到屏幕外」）。 */}
          <div className="grid min-w-0 gap-4 sm:grid-cols-2">
            <div className="min-w-0 space-y-2">
              <Label htmlFor="fb-domain">域名（可选）</Label>
              <Input
                id="fb-domain"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
                placeholder="ads.example.com"
                className="min-w-0 font-mono"
                spellCheck={false}
              />
            </div>
            <div className="min-w-0 space-y-2">
              <Label htmlFor="fb-kind">问题类型</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as FeedbackKind)}>
                <SelectTrigger className="min-w-0">
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

          <div className="min-w-0 space-y-2">
            <Label htmlFor="fb-message">说明</Label>
            <textarea
              id="fb-message"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={4}
              maxLength={2000}
              placeholder={hint}
              className="w-full min-w-0 rounded-xl border border-border px-3 py-2 text-sm leading-relaxed text-foreground placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
              style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
            />
            <p className="text-right text-xs text-muted-foreground">{message.length} / 2000</p>
          </div>

          <Button
            onClick={() => {
              void gate
                .run(async (captcha) => submit.mutateAsync(captcha))
                .catch((err) => toast.error('验证未完成', errorMessage(err)))
            }}
            disabled={!canSubmit}
            loading={submit.isPending}
          >
            <Send />
            提交
          </Button>
          <GeetestCaptcha
            config={cfgQuery.data?.captcha}
            handleRef={gate.handleRef}
            onError={(m) => toast.error('验证组件异常', m)}
          />
        </CardContent>
      </Card>

      {/* ── 我的反馈（列表摘要） ── */}
      <div className="min-w-0 space-y-2">
        <div className="flex items-baseline justify-between gap-2">
          <h2 className="text-sm font-semibold">我的反馈</h2>
          <span className="text-xs text-muted-foreground">点开查看对话与追问</span>
        </div>

        {listQuery.isLoading ? (
          <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Spinner /> 读取中…
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<MessageSquare className="size-6" />}
            title="还没有提交过反馈"
            description="遇到规则拦错或漏拦时，可以在上面提交"
          />
        ) : (
          <ul className="min-w-0 divide-y divide-border/60 overflow-hidden rounded-xl border border-border/70">
            {items.map((f) => (
              <FeedbackRow key={f.id} f={f} onOpen={() => setActiveId(f.id)} />
            ))}
          </ul>
        )}
      </div>

      <FeedbackDetailDialog
        feedback={active}
        onClose={() => setActiveId(null)}
        onChanged={() => qc.invalidateQueries({ queryKey: ['feedback', 'mine'] })}
        onToast={(t, m) => toast.success(t, m)}
        onErr={(t, m) => toast.error(t, m)}
      />
    </div>
  )
}

/** FeedbackRow 是列表里的一行摘要：类型 / 域名 / AI 标识 / 状态 / 时间。 */
function FeedbackRow({ f, onOpen }: { f: Feedback; onOpen: () => void }) {
  const meta = STATUS_META[f.status] ?? STATUS_META.open
  const aiHandled = (f.ai_answered_at ?? 0) > 0
  // 有新回复未读：管理员（含 AI）最后一条消息晚于我读到的位置。
  const unread = lastAdminTs(f) > (f.user_read_at ?? 0)

  return (
    <li className="min-w-0">
      <button
        type="button"
        onClick={onOpen}
        className={cn(
          'flex min-h-11 w-full min-w-0 items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/40',
          unread && 'bg-primary/4',
        )}
      >
        {/* 未读用左侧竖线，不打乱行内对齐 */}
        <span
          className={cn(
            'h-6 w-0.5 shrink-0 rounded-full',
            unread ? 'bg-primary' : 'bg-transparent',
          )}
        />
        <Badge variant="outline" className="shrink-0 text-[10px]">
          {KIND_OPTIONS.find((o) => o.value === f.kind)?.label ?? f.kind}
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
        {unread && (
          <Badge variant="default" className="shrink-0 text-[10px]">
            新回复
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

/** FeedbackDetailDialog 是二级详情：完整时间线 + 追问输入。 */
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
  onToast: (t: string, m: string) => void
  onErr: (t: string, m: string) => void
}) {
  const [text, setText] = useState('')

  const followUp = useMutation({
    mutationFn: ({ id, msg }: { id: string; msg: string }) => replyMyFeedback(id, msg),
    onSuccess: () => {
      onToast('已发送', '管理员会在这条反馈下继续回复')
      setText('')
      onChanged()
    },
    onError: (err) => onErr('发送失败', errorMessage(err)),
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
              {KIND_OPTIONS.find((o) => o.value === f.kind)?.label ?? f.kind}
            </Badge>
            {aiHandled && (
              <Badge variant="secondary" className="shrink-0 gap-0.5">
                <Bot className="size-2.5" />
                AI 自动处理
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription className="font-mono text-[11px] tabular-nums">
            {f.created_at}
          </DialogDescription>
        </DialogHeader>

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
        </div>

        {/* 已结单的不再提供追问：接口会拒绝（409），留着输入框只会让用户
            打完字才被告知「请重新提交」。 */}
        {f.status !== 'closed' ? (
          <div className="space-y-2 border-t border-border/50 pt-2.5">
            <textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              rows={2}
              maxLength={2000}
              placeholder="补充信息或继续追问（会追加到这条反馈里）"
              className="w-full rounded-xl border border-border px-3 py-2 text-sm leading-relaxed placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
              style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
            />
            <Button
              size="sm"
              loading={followUp.isPending}
              disabled={text.trim().length === 0}
              onClick={() => followUp.mutate({ id: f.id, msg: text.trim() })}
            >
              <Send />
              发送追问
            </Button>
          </div>
        ) : (
          <p className="border-t border-border/50 pt-2.5 text-[11.5px] text-muted-foreground">
            这条反馈已结单。如仍有问题，请重新提交一条新反馈。
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
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

/**
 * lastAdminTs 返回时间线上最后一条「非用户发言」的时间戳（秒）。
 *
 * 非用户而不是 role==='admin'：AI 自动回复也是以 admin 角色写入的
 * （前端气泡样式只区分 user/admin），这里按「不是我自己说的」判定，
 * AI 回复也算进未读。
 */
function lastAdminTs(f: { messages?: { role: string; created_at: string }[] }): number {
  let last = 0
  for (const m of f.messages ?? []) {
    if (m.role === 'user') continue
    const t = Math.floor(new Date(m.created_at).getTime() / 1000)
    if (t > last) last = t
  }
  return last
}
