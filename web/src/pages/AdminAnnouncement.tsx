import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Megaphone, Pencil, Plus, Trash2 } from 'lucide-react'

import {
  createAnnouncement,
  deleteAnnouncement,
  listAnnouncements,
  updateAnnouncement,
  type AnnouncementInput,
} from '@/api/member'
import { errorMessage } from '@/api/client'
import type { Announcement } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useToast } from '@/components/ui/toast'
import { formatDateTime } from '@/lib/format'

const EMPTY: AnnouncementInput = {
  title: '',
  body: '',
  level: 'info',
  mode: 'silent',
  dismissible: true,
  enabled: true,
  pinned: false,
  starts_at: 0,
  ends_at: 0,
}

const LEVEL_LABEL: Record<string, string> = { info: '普通', warning: '重要', danger: '紧急' }

/**
 * AnnouncementPanel 管理站内公告。
 *
 * 两种投递方式：
 *   silent —— 只进顶栏公告栏，未读显示红点与数量
 *   popup  —— 同时弹窗；dismissible 决定用户能否「不再弹出」
 */
export function AnnouncementPanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Announcement | null>(null)
  const [form, setForm] = useState<AnnouncementInput>(EMPTY)

  const q = useQuery({ queryKey: ['admin', 'announcements'], queryFn: listAnnouncements })
  const invalidate = () => qc.invalidateQueries({ queryKey: ['admin', 'announcements'] })

  const openNew = () => {
    setEditing(null)
    setForm(EMPTY)
    setOpen(true)
  }
  const openEdit = (a: Announcement) => {
    setEditing(a)
    setForm({
      title: a.title, body: a.body, level: a.level, mode: a.mode,
      dismissible: a.dismissible, enabled: a.enabled, pinned: a.pinned,
      starts_at: a.starts_at, ends_at: a.ends_at,
    })
    setOpen(true)
  }

  const save = useMutation({
    mutationFn: () => (editing ? updateAnnouncement(editing.id, form) : createAnnouncement(form)),
    onSuccess: () => {
      invalidate()
      qc.invalidateQueries({ queryKey: ['announcements'] })
      setOpen(false)
      toast.success(editing ? '公告已更新' : '公告已发布')
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const toggle = useMutation({
    mutationFn: (a: Announcement) => updateAnnouncement(a.id, { enabled: !a.enabled }),
    onSuccess: () => {
      invalidate()
      qc.invalidateQueries({ queryKey: ['announcements'] })
    },
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  const remove = useMutation({
    mutationFn: (id: number) => deleteAnnouncement(id),
    onSuccess: () => {
      invalidate()
      qc.invalidateQueries({ queryKey: ['announcements'] })
      toast.success('已删除')
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const items = q.data ?? []

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <Megaphone className="size-5 text-primary" />
            公告
          </CardTitle>
          <CardDescription>
            静默公告只进公告栏并显示红点；弹窗公告会在用户进入控制台时弹出。
          </CardDescription>
        </div>
        <Button size="sm" onClick={openNew}>
          <Plus />
          发布公告
        </Button>
      </CardHeader>
      <CardContent>
        {q.isLoading ? (
          <Skeleton className="h-40 rounded-xl" />
        ) : items.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">还没有公告</p>
        ) : (
          <div className="space-y-2">
            {items.map((a) => (
              <div
                key={a.id}
                className="flex flex-wrap items-start gap-3 rounded-xl border border-border/60 p-3"
              >
                <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                  {a.mode === 'popup' ? (
                    <Megaphone className="size-4 text-primary" />
                  ) : (
                    <Bell className="size-4 text-primary" />
                  )}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <p className="truncate text-sm font-medium">{a.title}</p>
                    <Badge variant={a.level === 'danger' ? 'danger' : a.level === 'warning' ? 'warning' : 'secondary'}>
                      {LEVEL_LABEL[a.level] ?? a.level}
                    </Badge>
                    <Badge variant="secondary">{a.mode === 'popup' ? '弹窗' : '静默'}</Badge>
                    {a.mode === 'popup' && !a.dismissible && <Badge variant="secondary">不可静音</Badge>}
                    {a.pinned && <Badge variant="secondary">置顶</Badge>}
                    {!a.enabled && <Badge variant="secondary">已停用</Badge>}
                  </div>
                  <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">{a.body || '（无正文）'}</p>
                  <p className="mt-0.5 text-[11px] text-muted-foreground/70">
                    {formatDateTime(a.created_at)}
                    {a.ends_at > 0 && ` · 到期 ${formatDateTime(a.ends_at)}`}
                  </p>
                </div>
                <div className="flex items-center gap-1">
                  <Switch checked={a.enabled} onCheckedChange={() => toggle.mutate(a)} aria-label="启用" />
                  <Button variant="ghost" size="icon" onClick={() => openEdit(a)} aria-label="编辑">
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-danger hover:bg-danger/10"
                    onClick={() => remove.mutate(a.id)}
                    aria-label="删除"
                  >
                    <Trash2 />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{editing ? '编辑公告' : '发布公告'}</DialogTitle>
            <DialogDescription>发布后立即对全部用户可见</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div>
              <Label htmlFor="an-title">标题</Label>
              <Input
                id="an-title"
                value={form.title}
                onChange={(e) => setForm({ ...form, title: e.target.value })}
                placeholder="例如：本周六 02:00 例行维护"
              />
            </div>
            <div>
              <Label htmlFor="an-body">正文</Label>
              <Textarea
                id="an-body"
                rows={5}
                value={form.body}
                onChange={(e) => setForm({ ...form, body: e.target.value })}
                placeholder="支持换行，纯文本"
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label>级别</Label>
                <Select value={form.level} onValueChange={(v) => setForm({ ...form, level: v as AnnouncementInput['level'] })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="info">普通</SelectItem>
                    <SelectItem value="warning">重要</SelectItem>
                    <SelectItem value="danger">紧急</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div>
                <Label>投递方式</Label>
                <Select value={form.mode} onValueChange={(v) => setForm({ ...form, mode: v as AnnouncementInput['mode'] })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="silent">静默（只进公告栏）</SelectItem>
                    <SelectItem value="popup">弹窗</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="space-y-2 rounded-xl border border-border/60 p-3">
              {form.mode === 'popup' && (
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <p className="text-sm">允许「不再弹出」</p>
                    <p className="text-xs text-muted-foreground">
                      关掉后用户只能点「我知道了」，每次登录都会再弹
                    </p>
                  </div>
                  <Switch
                    checked={form.dismissible}
                    onCheckedChange={(v) => setForm({ ...form, dismissible: v })}
                    aria-label="允许不再弹出"
                  />
                </div>
              )}
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm">置顶</p>
                <Switch
                  checked={form.pinned}
                  onCheckedChange={(v) => setForm({ ...form, pinned: v })}
                  aria-label="置顶"
                />
              </div>
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm">立即启用</p>
                <Switch
                  checked={form.enabled}
                  onCheckedChange={(v) => setForm({ ...form, enabled: v })}
                  aria-label="启用"
                />
              </div>
            </div>
            <Button className="w-full" loading={save.isPending} onClick={() => save.mutate()}>
              {editing ? '保存修改' : '发布'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
