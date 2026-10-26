import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bot, MessageSquareWarning, Send } from 'lucide-react'
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
import { FeedbackThread } from '@/components/FeedbackThread'
import { errorMessage } from '@/api/client'
import { createFeedback, listMyFeedback, replyMyFeedback } from '@/api/scope'
import { markFeedbackRead } from '@/api/notifications'
import { useUnreadCounts } from '@/hooks/useUnreadCounts'
import { getPublicConfig } from '@/api/member'
import { GeetestCaptcha, useCaptchaGate } from '@/components/GeetestCaptcha'
import type { CaptchaResult, FeedbackKind, FeedbackStatus } from '@/api/types'

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

  // 标记已读后要立刻刷新底部红点（见下方 effect）。
  const { refresh: refreshUnread } = useUnreadCounts()

  const listQuery = useQuery({ queryKey: ['feedback', 'mine'], queryFn: () => listMyFeedback() })
  const items = listQuery.data?.items ?? []

  // 进到这个页面就说明用户在看自己的反馈列表了 —— 把「有回复未读」的
  // 逐条标记为已读，红点随之消失。
  //
  // 为什么不是点开某条才标记：用户的习惯是到这一页扫一眼列表就知道
  // 有没有新回复，逼他逐条点开才能消红点只会让人干脆无视红点。
  // 只在拿到数据后跑一次（依赖 items 长度），避免每次渲染都发请求。
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
      toast.success('已提交', '管理员处理后会在这里看到回复')
      setDomain('')
      setMessage('')
      qc.invalidateQueries({ queryKey: ['feedback', 'mine'] })
    },
    onError: (err) => toast.error('提交失败', errorMessage(err)),
  })

  const followUp = useMutation({
    mutationFn: ({ id, text }: { id: string; text: string }) => replyMyFeedback(id, text),
    onSuccess: () => {
      toast.success('已发送', '管理员会在这条反馈下继续回复')
      qc.invalidateQueries({ queryKey: ['feedback', 'mine'] })
    },
    onError: (err) => toast.error('发送失败', errorMessage(err)),
  })

  const canSubmit = message.trim().length > 0 && !submit.isPending
  const hint = KIND_OPTIONS.find((o) => o.value === kind)?.hint

  return (
    <div className="min-w-0 space-y-6">
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
            <ul className="min-w-0 space-y-3">
              {items.map((f) => {
                const meta = STATUS_META[f.status] ?? STATUS_META.open
                return (
                  <li key={f.id} className="min-w-0 rounded-xl border border-border p-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <Badge variant={meta.variant}>{meta.label}</Badge>
                      <Badge variant="outline">
                        {KIND_OPTIONS.find((o) => o.value === f.kind)?.label ?? f.kind}
                      </Badge>
                      {/* AI 自动处理标识：让用户知道这条是机器判的、几秒就回了，
                          该不该继续追问一眼能看明白（需求 1 的「加个 ai 标识」） */}
                      {(f.ai_answered_at ?? 0) > 0 && (
                        <Badge variant="secondary" className="gap-0.5">
                          <Bot className="size-2.5" />
                          AI 自动处理
                        </Badge>
                      )}
                      <span className="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
                        {f.created_at}
                      </span>
                    </div>
                    {/* 域名单独一行并 break-all：挤在头部 flex 里时，
                        长域名会把「时间」挤出屏幕。 */}
                    {f.domain && (
                      <code className="mono-addr mt-2 block break-all text-xs text-muted-foreground">
                        {f.domain}
                      </code>
                    )}

                    {/* 时间线：一条反馈一串消息，用户追问与管理员回复都在里面，
                        不会再长出「第 2 条、第 3 条」。
                        fallback 只在接口没带 messages 时兜底（老数据/降级）。 */}
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
                      className="mt-3"
                    />

                    {f.status !== 'closed' && (
                      <FollowUpBox
                        pending={followUp.isPending}
                        onSend={(text) => followUp.mutate({ id: f.id, text })}
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

/**
 * 追问输入框：追加到当前反馈的时间线，而不是新开一条反馈。
 *
 * 默认收起成一行链接 —— 一屏十几条反馈如果每条都摆一个 textarea，
 * 页面会变得很长，而绝大多数人并不需要追问。
 */
function FollowUpBox({ pending, onSend }: { pending: boolean; onSend: (text: string) => void }) {
  const [text, setText] = useState('')
  const [open, setOpen] = useState(false)

  if (!open) {
    return (
      <Button size="sm" variant="ghost" className="mt-2" onClick={() => setOpen(true)}>
        <MessageSquareWarning className="size-3.5" />
        继续追问
      </Button>
    )
  }

  return (
    <div className="mt-3 min-w-0 space-y-2">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={2}
        maxLength={2000}
        placeholder="补充信息或继续追问（会追加到这条反馈里）"
        className="w-full min-w-0 rounded-xl border border-border px-3 py-2 text-sm leading-relaxed placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
        style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
      />
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          loading={pending}
          disabled={text.trim().length === 0}
          onClick={() => {
            onSend(text.trim())
            setText('')
            setOpen(false)
          }}
        >
          <Send />
          发送
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
          取消
        </Button>
      </div>
    </div>
  )
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
