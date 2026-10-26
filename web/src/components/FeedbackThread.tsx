import { ShieldCheck, User } from 'lucide-react'
import type { FeedbackMessage, FeedbackRole } from '@/api/types'
import { cn } from '@/lib/utils'

/**
 * 反馈时间线：把一条反馈的往返消息按时间铺成一串气泡。
 *
 * 为什么需要它：此前每条反馈只有一个 admin_reply 字段，用户想追问
 * **只能再提交一条新反馈** —— 列表里于是出现「同一个问题的第 2 条、
 * 第 3 条」，管理员结单时还得挨个翻。现在一条反馈一串消息，
 * 用户的追问和管理员的回复都在同一条里。
 *
 * 布局要点（手机端尤其重要）：
 *   - 气泡最大宽度 85%：占满整行会让「谁说的」完全看不出来；
 *   - `break-words` + `min-w-0`：用户可能贴一个超长域名或一整段
 *     没有空格的日志，不加这些会把卡片撑破、整页横向滚动；
 *   - 不用表格/固定宽度：手机上表格必然溢出。
 */

export interface FeedbackThreadProps {
  messages: FeedbackMessage[]
  /**
   * 老数据兜底：接口没返回 messages 时用 message + admin_reply 拼两条。
   *
   * 为什么保留：迁移会把历史数据回填进 feedback_messages，但接口降级
   * （时间线读失败）时 messages 会是空数组 —— 那时至少要让用户看到原文，
   * 而不是一片空白。
   */
  fallback?: { role: FeedbackRole; content: string; created_at: string; author?: string }[]
  className?: string
}

/** 统一形状：接口消息带 id，兜底数据没有。渲染只关心这五个字段。 */
interface Row {
  key: string
  role: FeedbackRole
  content: string
  created_at: string
  author?: string
}

export function FeedbackThread({ messages, fallback, className }: FeedbackThreadProps) {
  const list: Row[] =
    messages.length > 0
      ? messages.map((m) => ({
          key: m.id,
          role: m.role,
          content: m.content,
          created_at: m.created_at,
          author: m.author,
        }))
      : (fallback ?? []).map((m, i) => ({
          key: `fb-${i}-${m.created_at}`,
          role: m.role,
          content: m.content,
          created_at: m.created_at,
          author: m.author,
        }))
  if (list.length === 0) return null

  return (
    <ul className={cn('min-w-0 space-y-2', className)}>
      {list.map((m) => {
        const isAdmin = m.role === 'admin'
        return (
          <li
            key={m.key}
            className={cn('flex min-w-0 gap-2', isAdmin ? 'justify-start' : 'justify-end')}
          >
            {isAdmin && (
              <span
                className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/12 text-primary"
                aria-hidden="true"
              >
                <ShieldCheck className="size-3.5" />
              </span>
            )}
            <div className={cn('min-w-0 max-w-[85%]', isAdmin ? 'items-start' : 'items-end')}>
              <div
                className={cn(
                  'flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground',
                  isAdmin ? 'justify-start' : 'justify-end',
                )}
              >
                <span className="font-medium">{isAdmin ? m.author || '管理员' : m.author || '我'}</span>
                <span className="font-mono">{m.created_at}</span>
              </div>
              <div
                className={cn(
                  'mt-1 min-w-0 rounded-xl px-3 py-2 text-sm leading-relaxed',
                  // break-words 必须加：长域名/无空格日志会把气泡撑出屏幕。
                  'whitespace-pre-wrap break-words',
                  isAdmin
                    ? 'border-l-2 border-primary bg-primary/8 text-foreground'
                    : 'bg-muted text-foreground',
                )}
              >
                {m.content}
              </div>
            </div>
            {!isAdmin && (
              <span
                className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground"
                aria-hidden="true"
              >
                <User className="size-3.5" />
              </span>
            )}
          </li>
        )
      })}
    </ul>
  )
}
