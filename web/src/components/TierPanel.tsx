import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Save, Trash2 } from 'lucide-react'

import { deleteTier, listTiers, saveTier } from '@/api/tiers'
import { errorMessage } from '@/api/client'
import type { TierDef } from '@/api/types'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'

/** 空表单：新增档位时的初始值 */
const EMPTY: TierDef = {
  id: '',
  name: '',
  sort: 100,
  max_rules: 0,
  max_subs: 0,
  daily_queries: 0,
  is_default: false,
}

/**
 * 会员种类（档位）管理。
 *
 * 之前只有 free/vip 两档写死在代码里，想加「高级会员」就得改代码发版。
 * 现在档位是数据：管理员自己加，每个档位一套配额。
 *
 * free / vip 是内置的：用户没有显式指定档位时，配额换算要靠这两个 id 兜底，
 * 删掉之后老用户会算不出配额，所以后端拒绝删除，这里也不给删除按钮。
 */
export function TierPanel() {
  const qc = useQueryClient()
  const toast = useToast()
  const [draft, setDraft] = useState<TierDef | null>(null)
  const [pendingDelete, setPendingDelete] = useState<TierDef | null>(null)

  const { data, isLoading } = useQuery({ queryKey: ['tiers'], queryFn: listTiers })

  const save = useMutation({
    mutationFn: (t: TierDef) => saveTier(t),
    onSuccess: () => {
      toast.success('档位已保存', '新配额已下发到解析服务')
      setDraft(null)
      qc.invalidateQueries({ queryKey: ['tiers'] })
    },
    onError: (e) => toast.error('保存失败', errorMessage(e)),
  })

  const del = useMutation({
    mutationFn: (id: string) => deleteTier(id),
    onSuccess: () => {
      toast.success('档位已删除')
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['tiers'] })
    },
    // 后端会因为「还有用户在用」拒绝删除，原因必须原样显示出来，
    // 否则管理员只会看到「删除失败」而不知道要先改用户档位。
    onError: (e) => toast.error('删除失败', errorMessage(e)),
  })

  const set = (patch: Partial<TierDef>) => setDraft((d) => (d ? { ...d, ...patch } : d))

  return (
    <section className="rounded-2xl border border-border/60 bg-card p-4 shadow-sm md:p-6">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">会员种类</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            每个种类一套配额，配额填 0 表示不限。会员种类只在会员有效期内生效，
            到期自动回到默认档；发放时由「调整会员」同时指定种类和天数。
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={() => setDraft({ ...EMPTY })}>
          <Plus className="size-4" />
          新增档位
        </Button>
      </div>

      {isLoading && <Skeleton className="h-24 w-full" />}

      <div className="space-y-2">
        {(data ?? []).map((t) => {
          const builtin = t.id === 'free' || t.id === 'vip'
          return (
            <div
              key={t.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border/50 p-3"
            >
              <div className="min-w-0">
                <p className="flex items-center gap-2 text-sm font-medium">
                  {t.name}
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                    {t.id}
                  </code>
                  {t.is_default && (
                    <span className="rounded bg-primary/15 px-1.5 py-0.5 text-[11px] text-primary">
                      默认档
                    </span>
                  )}
                  {builtin && (
                    <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                      内置
                    </span>
                  )}
                </p>
                <p className="mt-1 font-mono text-xs text-muted-foreground">
                  规则 {t.max_rules || '不限'} · 订阅 {t.max_subs || '不限'} · 请求{' '}
                  {t.daily_queries ? t.daily_queries.toLocaleString('zh-CN') : '不限'}
                </p>
              </div>
              <div className="flex items-center gap-1">
                <Button size="sm" variant="ghost" onClick={() => setDraft({ ...t })}>
                  编辑
                </Button>
                {!builtin && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label="删除档位"
                    onClick={() => setPendingDelete(t)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                )}
              </div>
            </div>
          )
        })}
      </div>

      {draft && (
        <div className="mt-4 space-y-3 rounded-xl border border-primary/40 bg-primary/5 p-3">
          <p className="text-sm font-medium">{draft.id ? `编辑 ${draft.name || draft.id}` : '新增档位'}</p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">标识（小写字母/数字/短横线）</Label>
              <Input
                value={draft.id}
                disabled={draft.id === 'free' || draft.id === 'vip'}
                placeholder="svip"
                onChange={(e) => set({ id: e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, '') })}
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">显示名称</Label>
              <Input
                value={draft.name}
                placeholder="高级会员"
                onChange={(e) => set({ name: e.target.value })}
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">自定义规则数（0 = 不限）</Label>
              <Input
                inputMode="numeric"
                value={String(draft.max_rules)}
                onChange={(e) => set({ max_rules: Number(e.target.value.replace(/\D/g, '')) || 0 })}
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">自定义订阅数（0 = 不限）</Label>
              <Input
                inputMode="numeric"
                value={String(draft.max_subs)}
                onChange={(e) => set({ max_subs: Number(e.target.value.replace(/\D/g, '')) || 0 })}
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">每日请求量（0 = 不限）</Label>
              <Input
                inputMode="numeric"
                value={String(draft.daily_queries)}
                onChange={(e) =>
                  set({ daily_queries: Number(e.target.value.replace(/\D/g, '')) || 0 })
                }
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">排序（小的在前）</Label>
              <Input
                inputMode="numeric"
                value={String(draft.sort)}
                onChange={(e) => set({ sort: Number(e.target.value.replace(/\D/g, '')) || 0 })}
              />
            </div>
          </div>
          <div className="flex items-center justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setDraft(null)}>
              取消
            </Button>
            <Button
              size="sm"
              loading={save.isPending}
              disabled={!draft.id || !draft.name}
              onClick={() => save.mutate(draft)}
            >
              <Save className="size-4" />
              保存
            </Button>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title={`删除档位「${pendingDelete?.name ?? ''}」？`}
        description="还有用户属于这个档位时会拒绝删除，请先把那些用户改到别的档位。"
        confirmText="删除"
        destructive
        loading={del.isPending}
        onConfirm={() => pendingDelete && del.mutate(pendingDelete.id)}
      />
    </section>
  )
}
