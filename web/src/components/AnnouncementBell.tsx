import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Bell, Check, Info, Megaphone, X } from 'lucide-react'

import { dismissAnnouncement, getMyAnnouncements, readAnnouncement } from '@/api/member'
import type { Announcement } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { formatDateTime } from '@/lib/format'

// sessionStorage 键：记录本次会话已经关掉过哪些弹窗。
//
// 为什么需要它：「关闭」只是关掉这一次，「不再弹出」才是永久的。
// 如果只靠服务端状态，关掉之后刷新页面又会弹出来，用户会以为按钮坏了。
const SESSION_KEY = 'aegis.dismissed_popups'

function loadSessionDismissed(): Set<number> {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY)
    if (!raw) return new Set()
    return new Set(JSON.parse(raw) as number[])
  } catch {
    return new Set()
  }
}

function saveSessionDismissed(ids: Set<number>) {
  try {
    sessionStorage.setItem(SESSION_KEY, JSON.stringify([...ids]))
  } catch {
    // 隐私模式下 sessionStorage 可能不可写，退化成「每次刷新都弹」也能用
  }
}

const LEVEL_META = {
  info: { icon: Info, cls: 'text-primary', bg: 'bg-primary/10' },
  warning: { icon: AlertTriangle, cls: 'text-warning', bg: 'bg-warning/10' },
  danger: { icon: AlertTriangle, cls: 'text-danger', bg: 'bg-danger/10' },
} as const

/**
 * AnnouncementBell 是顶栏的公告入口。
 *
 * 红点/数量来自未读数；弹窗类公告在进入页面时自动弹一次。
 */
export function AnnouncementBell() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [sessionDismissed, setSessionDismissed] = useState<Set<number>>(() => loadSessionDismissed())
  const [popup, setPopup] = useState<Announcement | null>(null)

  const query = useQuery({
    queryKey: ['announcements', 'mine'],
    queryFn: getMyAnnouncements,
    refetchInterval: 60000,
    staleTime: 30000,
  })

  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const unread = query.data?.unread ?? 0

  // 挑一条该弹的：popup 模式、未被永久静音、本会话也没关过
  useEffect(() => {
    if (popup) return
    const next = items.find(
      (a) => a.mode === 'popup' && (a.dismissed_at ?? 0) === 0 && !sessionDismissed.has(a.id),
    )
    if (next) setPopup(next)
  }, [items, popup, sessionDismissed])

  const markRead = useMutation({
    mutationFn: (id: number) => readAnnouncement(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['announcements'] }),
  })

  const dismiss = useMutation({
    mutationFn: (id: number) => dismissAnnouncement(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['announcements'] }),
  })

  // 关闭弹窗：只关这一次，下次刷新还会提醒
  const closePopup = () => {
    if (!popup) return
    if (popup.dismissible) {
      const next = new Set(sessionDismissed)
      next.add(popup.id)
      setSessionDismissed(next)
      saveSessionDismissed(next)
    } else {
      // 不可静音的公告：关闭即视为已读，否则用户没有任何办法摆脱它
      markRead.mutate(popup.id)
      const next = new Set(sessionDismissed)
      next.add(popup.id)
      setSessionDismissed(next)
      saveSessionDismissed(next)
    }
    setPopup(null)
  }

  const dismissForever = () => {
    if (!popup) return
    dismiss.mutate(popup.id)
    setPopup(null)
  }

  return (
    <>
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>
          <button
            className="relative flex size-10 items-center justify-center rounded-xl text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            aria-label={unread > 0 ? `公告（${unread} 条未读）` : '公告'}
          >
            <Bell className="size-5" />
            {unread > 0 && (
              <span className="absolute -right-0.5 -top-0.5 flex min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[10px] font-semibold leading-4 text-white">
                {unread > 99 ? '99+' : unread}
              </span>
            )}
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-[min(92vw,22rem)] p-0">
          <div className="flex items-center justify-between border-b border-border/60 px-3 py-2">
            <p className="flex items-center gap-1.5 text-sm font-semibold">
              <Megaphone className="size-4 text-primary" />
              公告
            </p>
            {unread > 0 && <Badge variant="danger">{unread} 条未读</Badge>}
          </div>
          <div className="max-h-[60vh] overflow-y-auto">
            {items.length === 0 ? (
              <p className="px-3 py-8 text-center text-sm text-muted-foreground">暂无公告</p>
            ) : (
              items.map((a) => (
                <button
                  key={a.id}
                  onClick={() => {
                    if ((a.read_at ?? 0) === 0) markRead.mutate(a.id)
                  }}
                  className={cn(
                    'flex w-full gap-2 border-b border-border/40 px-3 py-2.5 text-left last:border-0 hover:bg-muted/40',
                    (a.read_at ?? 0) === 0 && 'bg-primary/5',
                  )}
                >
                  <span className={cn('mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-lg', LEVEL_META[a.level]?.bg)}>
                    {(() => {
                      const Icon = LEVEL_META[a.level]?.icon ?? Info
                      return <Icon className={cn('size-3.5', LEVEL_META[a.level]?.cls)} />
                    })()}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5">
                      <span className="truncate text-sm font-medium">{a.title}</span>
                      {a.pinned && <Badge variant="secondary">置顶</Badge>}
                      {(a.read_at ?? 0) === 0 && <span className="size-1.5 shrink-0 rounded-full bg-danger" />}
                    </span>
                    {a.body && (
                      <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">{a.body}</span>
                    )}
                    <span className="mt-0.5 block text-[11px] text-muted-foreground/70">
                      {formatDateTime(a.created_at)}
                    </span>
                  </span>
                </button>
              ))
            )}
          </div>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* 弹窗类公告 */}
      <Dialog open={popup !== null} onOpenChange={(v) => !v && closePopup()}>
        <DialogContent className="max-w-lg">
          {popup && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  <span className={cn('flex size-7 items-center justify-center rounded-lg', LEVEL_META[popup.level]?.bg)}>
                    {(() => {
                      const Icon = LEVEL_META[popup.level]?.icon ?? Info
                      return <Icon className={cn('size-4', LEVEL_META[popup.level]?.cls)} />
                    })()}
                  </span>
                  {popup.title}
                </DialogTitle>
                <DialogDescription>{formatDateTime(popup.created_at)}</DialogDescription>
              </DialogHeader>
              <p className="whitespace-pre-wrap text-sm leading-relaxed">{popup.body}</p>
              <div className="flex flex-wrap items-center justify-end gap-2 pt-1">
                {popup.dismissible && (
                  <Button variant="ghost" size="sm" onClick={dismissForever} loading={dismiss.isPending}>
                    <X />
                    不再弹出
                  </Button>
                )}
                <Button size="sm" onClick={closePopup}>
                  <Check />
                  我知道了
                </Button>
              </div>
              {!popup.dismissible && (
                <p className="text-xs text-muted-foreground">
                  这条公告未开放「不再弹出」，每次登录都会提醒，直到管理员下线它。
                </p>
              )}
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}
