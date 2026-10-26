import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Bell,
  BellOff,
  CheckCheck,
  Gift,
  Layers,
  MessageSquareWarning,
  Trash2,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { PageHeader } from '@/components/PageHeader'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/api/client'
import {
  deleteNotification,
  listNotifications,
  markAllNotificationsRead,
  markNotificationRead,
} from '@/api/notifications'
import type { Notification, NotificationKind } from '@/api/types'
import { cn } from '@/lib/utils'
import { useUnreadCounts } from '@/hooks/useUnreadCounts'

/**
 * 通知页存放「不需要用户立刻行动、但该知道一下」的消息：
 * 订阅被收录为全局规则、签到拿到奖励、反馈被处理。
 *
 * 为什么要有这一页而不是只发站内信：
 * 这些消息以前只会写在邮件里或者干脆没有 —— 用户不翻邮箱就永远不知道
 * 自己的订阅为什么消失了。放进面板后，通知和「他为什么看到这个结果」
 * 是同一个界面里的东西。
 *
 * 视觉上刻意做得比公告轻：通知是流水消息，不需要横幅那种强调，
 * 未读用左侧一道竖线 + 加粗标题区分就够了。
 */
export default function Notifications() {
  const navigate = useNavigate()
  const [items, setItems] = useState<Notification[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const { refresh: refreshCounts } = useUnreadCounts()

  const load = useCallback(async () => {
    setLoading(true)
    setErr('')
    try {
      const res = await listNotifications(100, 0)
      setItems(res.items ?? [])
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const unread = items.filter((n) => !n.read_at).length

  const openOne = async (n: Notification) => {
    if (!n.read_at) {
      // 先本地置读再发请求：点开瞬间红点就该消失，不该等一个往返。
      setItems((prev) =>
        prev.map((x) => (x.id === n.id ? { ...x, read_at: new Date().toISOString() } : x)),
      )
      try {
        await markNotificationRead(n.id)
        void refreshCounts()
      } catch {
        // 标记失败不回滚 UI：下次进来还会是未读，不算数据损坏。
      }
    }
    if (n.link) navigate(n.link)
  }

  const markAll = async () => {
    setItems((prev) =>
      prev.map((x) => (x.read_at ? x : { ...x, read_at: new Date().toISOString() })),
    )
    try {
      await markAllNotificationsRead()
      void refreshCounts()
    } catch (e) {
      setErr(errorMessage(e))
      void load()
    }
  }

  const remove = async (id: string) => {
    const backup = items
    setItems((prev) => prev.filter((x) => x.id !== id))
    try {
      await deleteNotification(id)
      void refreshCounts()
    } catch (e) {
      setErr(errorMessage(e))
      setItems(backup)
    }
  }

  return (
    <div className="min-w-0 space-y-4">
      <PageHeader
        title="通知"
        description="订阅被收录、签到奖励、反馈处理结果都会出现在这里。"
        actions={
          unread > 0 ? (
            <Button size="sm" variant="secondary" onClick={markAll}>
              <CheckCheck />
              全部已读
            </Button>
          ) : undefined
        }
      />

      {err && <ErrorState message={err} onRetry={load} />}

      {loading && items.length === 0 && (
        <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
          <Spinner /> 正在加载通知…
        </div>
      )}

      {!loading && items.length === 0 && !err && (
        <EmptyState
          icon={<BellOff className="size-6" />}
          title="暂无通知"
          description="订阅被收录为全局规则、签到拿到奖励、反馈被处理时，会在这里提醒你。"
        />
      )}

      {items.length > 0 && (
        <ul className="min-w-0 divide-y divide-border/50 overflow-hidden rounded-xl border border-border/60">
          {items.map((n) => (
            <NotificationRow
              key={n.id}
              n={n}
              onOpen={() => void openOne(n)}
              onDelete={() => void remove(n.id)}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function NotificationRow({
  n,
  onOpen,
  onDelete,
}: {
  n: Notification
  onOpen: () => void
  onDelete: () => void
}) {
  const unread = !n.read_at
  const Icon = iconFor(n.kind)
  return (
    <li
      className={cn(
        'group flex min-w-0 items-start gap-2.5 px-3 py-2.5 transition-colors',
        unread ? 'bg-primary/4' : 'hover:bg-muted/40',
      )}
    >
      {/* 未读用左侧竖线标记，不用圆点：竖线不打乱行内对齐，
          而圆点会把标题往右挤，两种状态下标题位置不一致。 */}
      <span
        className={cn(
          'mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md',
          unread ? 'bg-primary/12 text-primary' : 'bg-muted text-muted-foreground',
        )}
      >
        <Icon className="size-3.5" />
      </span>

      <button
        type="button"
        onClick={onOpen}
        className="min-w-0 flex-1 text-left"
      >
        <div className="flex min-w-0 items-baseline gap-2">
          <span
            className={cn(
              'min-w-0 break-words text-[13px] leading-snug',
              unread ? 'font-semibold text-foreground' : 'font-medium text-foreground/85',
            )}
          >
            {n.title}
          </span>
          {unread && (
            <Badge variant="default" className="shrink-0 text-[10px]">
              未读
            </Badge>
          )}
        </div>
        {n.body && (
          <p className="mt-0.5 break-words text-[12px] leading-relaxed text-muted-foreground">
            {n.body}
          </p>
        )}
        <p className="mt-1 font-mono text-[10.5px] tabular-nums text-muted-foreground/70">
          {formatTime(n.created_at)}
        </p>
      </button>

      <button
        type="button"
        onClick={onDelete}
        aria-label="删除通知"
        className="mt-0.5 shrink-0 rounded p-1 text-muted-foreground/50 transition-colors hover:bg-danger/10 hover:text-danger"
      >
        <Trash2 className="size-3.5" />
      </button>
    </li>
  )
}

function iconFor(kind: NotificationKind) {
  switch (kind) {
    case 'sub_promoted':
      return Layers
    case 'checkin_bonus':
      return Gift
    case 'feedback_ai':
    case 'feedback_reply':
      return MessageSquareWarning
    default:
      return Bell
  }
}

/** formatTime 把 RFC3339 化成「今天 14:03 / 10-25 14:03」。 */
function formatTime(raw: string): string {
  if (!raw) return ''
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const now = new Date()
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (sameDay) return `今天 ${hm}`
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${hm}`
}
