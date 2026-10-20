import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, Radar, ShieldAlert, Tag } from 'lucide-react'

import { createRule, listRules } from '@/api/rules'
import { errorMessage } from '@/api/client'
import type { QueryLogItem, RuleKind } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAuth } from '@/hooks/useAuth'
import { useToast } from '@/components/ui/toast'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

/**
 * QueryLogDetail 是单条日志的详情弹窗。
 *
 * 分四块：请求 / 响应 / 客户端 / 操作。
 *
 * 操作区只在两种情况下出现：
 *   - 管理员：全局规则层，随时可加；
 *   - 普通用户：个人规则还没到配额上限。
 * 没有额度时给出明确文案而不是把按钮灰掉不给理由 —— 用户点不动又不知道为什么
 * 是最糟的交互。
 */
export function QueryLogDetail({
  row,
  onClose,
}: {
  row: QueryLogItem | null
  onClose: () => void
}) {
  const { isAdmin } = useAuth()
  const toast = useToast()
  const qc = useQueryClient()
  const [added, setAdded] = useState<RuleKind | null>(null)

  const open = row !== null
  const scope = isAdmin ? 'global' : 'user'

  // 配额只在弹窗打开时才查：日志页本身不需要这个请求。
  const quota = useQuery({
    queryKey: ['rules', 'quota', scope],
    queryFn: () => listRules({ page: 1, page_size: 1 }, scope),
    enabled: open,
    staleTime: 10000,
  })

  const limit = quota.data?.limit ?? 0
  const used = quota.data?.total ?? 0
  // limit 为 0 表示不限额（管理员或未设配额）
  const hasRoom = isAdmin || limit === 0 || used < limit

  const addRule = useMutation({
    mutationFn: (kind: RuleKind) =>
      createRule(
        {
          kind,
          pattern: row!.domain,
          value: '',
          qtypes: [],
          enabled: true,
        },
        scope,
      ),
    onSuccess: (_res, kind) => {
      setAdded(kind)
      toast.success(
        kind === 'block' ? '已加入拦截规则' : '已加入放行规则',
        `${row?.domain} 已生效`,
      )
      qc.invalidateQueries({ queryKey: ['rules'] })
      qc.invalidateQueries({ queryKey: ['queryLog'] })
    },
    onError: (err) => toast.error('添加规则失败', errorMessage(err)),
  })

  const reset = () => {
    setAdded(null)
    onClose()
  }

  const meta = useMemo(() => {
    if (!row) return null
    const blocked = row.action === 'blocked'
    return {
      blocked,
      actionLabel: blocked ? '拦截' : '放行',
      actionVariant: blocked ? ('danger' as const) : ('success' as const),
    }
  }, [row])

  if (!row || !meta) return null

  return (
    <Dialog open={open} onOpenChange={(v) => !v && reset()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span className="break-all font-mono text-base">{row.domain}</span>
            <Badge variant={meta.actionVariant}>{meta.actionLabel}</Badge>
            {row.tracker && (
              <Badge variant="warning" title={`命中的追踪器后缀：${row.tracker}`}>
                <Radar className="size-3" />
                追踪器
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription>{formatDateTime(row.ts)}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Section title="请求">
            <Field label="类型" value={row.qtype || '—'} mono />
            <Field label="协议" value={row.proto ? row.proto.toUpperCase() : '—'} mono />
            <Field label="ECS" value={row.ecs || '—'} mono />
            <Field
              label="已知追踪器"
              value={row.tracker || '未命中'}
              mono
              tone={row.tracker ? 'warning' : undefined}
            />
          </Section>

          <Section title="响应">
            <Field label="状态" value={meta.actionLabel} tone={meta.blocked ? 'danger' : 'success'} />
            <Field label="响应代码" value={row.rcode || '—'} mono />
            <Field label="DNS 服务器" value={row.upstream || '—'} mono />
            <Field label="耗时" value={`${row.latency_ms} ms`} mono />
            <div className="col-span-2">
              <p className="eyebrow mb-1">响应</p>
              <pre className="max-h-40 overflow-auto rounded-lg bg-muted/40 p-2 font-mono text-[11px] leading-relaxed">
                {row.answer || '（无应答记录）'}
              </pre>
            </div>
          </Section>

          <Section title="客户端">
            <Field label="IP" value={row.client || '—'} mono />
            <Field label="名称" value={row.client_name || '—'} />
          </Section>

          <Section title="操作">
            <div className="col-span-2 space-y-2">
              {added ? (
                <p className="flex items-center gap-2 text-sm text-success">
                  <CheckCircle2 className="size-4" />
                  已添加{added === 'block' ? '拦截' : '放行'}规则，后续查询立即生效
                </p>
              ) : hasRoom ? (
                <div className="flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    variant="danger"
                    loading={addRule.isPending && addRule.variables === 'block'}
                    onClick={() => addRule.mutate('block')}
                  >
                    <Ban />
                    加入拦截
                  </Button>
                  <Button
                    size="sm"
                    variant="success"
                    loading={addRule.isPending && addRule.variables === 'allow'}
                    onClick={() => addRule.mutate('allow')}
                  >
                    <CheckCircle2 />
                    加入放行
                  </Button>
                </div>
              ) : (
                <p className="flex items-start gap-2 text-sm text-muted-foreground">
                  <ShieldAlert className="mt-0.5 size-4 shrink-0" />
                  个人规则已用满配额（{used} / {limit}），无法再添加。删除不需要的规则后可以继续。
                </p>
              )}
              {!isAdmin && limit > 0 && hasRoom && (
                <p className="text-xs text-muted-foreground">
                  个人规则配额：{used} / {limit}
                </p>
              )}
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <Tag className="size-3" />
                操作会直接写入{isAdmin ? '全局' : '个人'}规则层，按域名精确匹配。
              </p>
            </div>
          </Section>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border border-border/60 p-3">
      <p className="eyebrow mb-2">{title}</p>
      <div className="grid grid-cols-2 gap-x-4 gap-y-2">{children}</div>
    </section>
  )
}

function Field({
  label,
  value,
  mono,
  tone,
}: {
  label: string
  value: string
  mono?: boolean
  tone?: 'danger' | 'success' | 'warning'
}) {
  return (
    <div className="min-w-0">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p
        className={cn(
          'truncate text-sm',
          mono && 'font-mono text-[13px]',
          tone === 'danger' && 'text-danger',
          tone === 'success' && 'text-success',
          tone === 'warning' && 'text-warning',
        )}
        title={value}
      >
        {value}
      </p>
    </div>
  )
}
