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
import { formatDateTimeSec } from '@/lib/format'
import { cn } from '@/lib/utils'

/**
 * QueryLogDetail 是单条日志的详情弹窗。
 *
 * 排布原则（这一版重做的重点）：
 *
 * 1. **键值同行，不上下叠。** 旧版每个字段是「标签一行 + 值一行」，
 *    四组字段就吃掉整屏高度，而值往往只有 "A" / "6 ms" 这么几个字符 ——
 *    空间全花在标签的独立行上。现在标签固定窄列、值占满右侧，
 *    一行一对，扫读时眼睛只做水平移动。
 * 2. **层次靠字号与色阶，不靠边框堆叠。** 旧版每个分组一个圆角边框卡片，
 *    四个卡片叠在一起只剩一堆等重的线。现在分组的标题用 eyebrow、
 *    上沿一道极细分隔线，值的字重比标签高一档，层级自然分出来。
 * 3. **应答单独提亮。** 它是唯一的长文本块，用深色铭牌（m-plate）反底，
 *    在整片浅色里形成唯一的视觉落点。
 * 4. 桌面端两列并排（请求 / 响应），弹窗宽度从 2xl 提到 3xl —— 键值布局的
 *    瓶颈是横向空间，加宽比加高有用。手机端仍是底部抽屉、单列。
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
    const allowed = row.action === 'allowed'
    // 三分类：已拦截 / 允许项（命中放行规则）/ 已处理（正常解析、改写、缓存命中）。
    // 缓存只是修饰，跟在状态后面。
    const base = blocked ? '已拦截' : allowed ? '允许项' : '已处理'
    return {
      blocked,
      allowed,
      actionLabel: row.cache_hit ? `${base}（缓存）` : base,
      actionVariant: blocked ? ('danger' as const) : allowed ? ('success' as const) : ('default' as const),
    }
  }, [row])

  if (!row || !meta) return null

  return (
    <Dialog open={open} onOpenChange={(v) => !v && reset()}>
      {/* 宽度（这是「宽度撑满」的修复点）：
          基类在手机上给的是 `inset-x-0` —— 左右贴边、100% 屏宽，看起来就是
          「撑满整个屏幕、左右都顶到边」。用户要的是两边留白。

          改成左右各留 1rem（16px）的浮层：`inset-x-4` 覆盖基类的
          `inset-x-0`，同时把只圆上角的顶部抽屉（rounded-t-2xl）改成四角
          全圆 —— 一旦两侧不贴边、底下露出页面，只圆上角会显得像被切断。

          md 以上另有约束（见下方写宽度那条），两者不冲突：
          手机走 inset-x-3 + 100%，桌面走 calc(100%-2rem) + max-w-3xl。 */}
      <DialogContent className="inset-x-4 max-h-[85dvh] gap-2 rounded-2xl p-3.5 pb-[calc(1rem+env(safe-area-inset-bottom))] md:inset-x-auto md:left-1/2 md:w-[calc(100%-2rem)] md:max-w-3xl md:translate-x-[calc(-50%+5px)] md:gap-3 md:rounded-2xl md:p-4 md:pb-4">
        <DialogHeader className="gap-1">
          <DialogTitle className="break-all pr-8 font-mono text-[14px] leading-tight md:text-[15px]">
            {row.domain}
          </DialogTitle>
          <DialogDescription className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <Badge variant={meta.actionVariant} className="shrink-0">
              {meta.actionLabel}
            </Badge>
            <span className="font-mono text-[11px] tabular-nums">{formatDateTimeSec(row.ts)}</span>
          </DialogDescription>
        </DialogHeader>

        {/* 追踪器：命中时它是这条日志最值得看的信息。用左侧一道强调竖线，
            比整块横幅更省高度，也更容易和下面的字段区分开。 */}
        {row.tracker && (
          <div className="flex items-center gap-1.5 rounded-r border-l-2 border-warning bg-warning/8 py-1 pl-2 pr-1.5">
            <Radar className="size-3 shrink-0 text-warning" />
            <span className="min-w-0 flex-1 truncate text-[12px] font-medium text-warning">
              {row.tracker}
            </span>
            <span className="shrink-0 text-[9.5px] text-muted-foreground">内置库</span>
          </div>
        )}

        {/* 请求 / 响应并排：桌面两列，手机单列。 */}
        <div className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
          <div className="space-y-1.5">
            {/* 配对顺序要紧：手机双列下按 DOM 顺序两两成对。
                类型+协议 一行、缓存+ECS 一行；ECS 的值是 /24 网段，
                半栏放不下所以带 span 独占，放最后不打断前面的配对。 */}
            <Group label="请求">
              <KV k="类型" v={row.qtype || '—'} mono />
              <KV k="协议" v={protoLabel(row.proto)} mono />
              <KV k="缓存" v={row.cache_hit ? '命中' : '未命中'} />
              <KV k="ECS" v={row.ecs || '—'} mono span />
            </Group>
            {/* 客户端两个字段都独占整行：IP 是 /24 网段（16 字符），
                名称可能被用户设成任意中文名，塞进半栏必然折行。 */}
            <Group label="客户端">
              <KV k="IP" v={row.client || '—'} mono span />
              <KV k="名称" v={row.client_name || '—'} span />
            </Group>
          </div>

          <Group label="响应">
            <KV k="上游" v={row.upstream || '—'} mono />
            <KV
              k="耗时"
              v={`${row.latency_ms} ms`}
              mono
              tone={row.latency_ms >= 100 ? 'danger' : undefined}
            />
            <KV k="代码" v={row.rcode || '—'} mono />
            <KV
              k="状态"
              v={meta.actionLabel}
              tone={meta.blocked ? 'danger' : meta.allowed ? 'success' : undefined}
            />
          </Group>
        </div>

        {/* 应答：用深色铭牌反底，让整片浅色里有一个明确的视觉落点。
            高度上限手机 96px / 桌面 128px —— 原来 max-h-40（160px）
            在手机上能吃掉弹窗四分之一高度，而应答里真正要看的是前两三行。
            超出部分块内滚动，不撑高弹窗。 */}
        <div>
          <p className="mb-0.5 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground/80">
            应答
          </p>
          {row.answer ? (
            <pre className="m-plate max-h-24 overflow-auto rounded-md px-2 py-1.5 font-mono text-[11px] leading-[1.45] md:max-h-32">
              {row.answer}
            </pre>
          ) : (
            <p className="rounded-md border border-dashed border-border/70 px-2 py-1.5 text-[11.5px] text-muted-foreground">
              {meta.blocked ? '拦截应答，无上游记录' : '这条记录没有留下应答内容'}
            </p>
          )}
        </div>

        {/* 操作：一行按钮 + 右侧一句说明，不再单独占一个带框分区。 */}
        <div className="border-t border-border/50 pt-2.5">
          {added ? (
            <p className="flex items-center gap-2 text-[13px] text-success">
              <CheckCircle2 className="size-4 shrink-0" />
              已添加{added === 'block' ? '拦截' : '放行'}规则，后续查询立即生效
            </p>
          ) : hasRoom ? (
            <div className="flex flex-wrap items-center gap-2">
              {/* 已经是这个状态就不再给同一个按钮：
                  一条已经被拦截的记录旁边摆着「加入拦截」，
                  只会让人以为没生效、手贱再点一次。 */}
              {!meta.blocked && (
                <Button
                  size="sm"
                  variant="danger"
                  loading={addRule.isPending && addRule.variables === 'block'}
                  onClick={() => addRule.mutate('block')}
                >
                  <Ban />
                  加入拦截
                </Button>
              )}
              {!meta.allowed && (
                <Button
                  size="sm"
                  variant="success"
                  loading={addRule.isPending && addRule.variables === 'allow'}
                  onClick={() => addRule.mutate('allow')}
                >
                  <CheckCircle2 />
                  加入放行
                </Button>
              )}
              <span className="ml-auto flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Tag className="size-3 shrink-0" />
                写入{isAdmin ? '全局' : '个人'}规则层
                {!isAdmin && limit > 0 && (
                  <span className="tabular-nums">
                    · {used}/{limit}
                  </span>
                )}
              </span>
            </div>
          ) : (
            <p className="flex items-start gap-2 text-[13px] text-muted-foreground">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" />
              个人规则已用满配额（{used} / {limit}），无法再添加。删除不需要的规则后可以继续。
            </p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}

/** 协议显示名：AdGuard 那种「DNS-over-TLS」写法，比裸 dot 好认。 */
function protoLabel(proto: string | undefined): string {
  switch ((proto || '').toLowerCase()) {
    case 'doh':
      return 'DNS-over-HTTPS'
    case 'dot':
      return 'DNS-over-TLS'
    case 'doq':
      return 'DNS-over-QUIC'
    case 'tcp':
      return 'DNS-over-TCP'
    case 'udp':
    case 'plain':
      return '明文 DNS'
    default:
      return proto ? proto.toUpperCase() : '—'
  }
}

/**
 * Group 是一组键值行。
 *
 * 分组标题刻意做得很轻：11px 小字、行高压到 1.3、上下几乎不留白。
 * 它是给人扫读用的路标，不是独立的一层内容 —— 之前 `mb-1 pt-1.5`
 * 加上 1.4 的行高，光标题就占 21px，三个分组白花 60 多像素，
 * 在手机上那是整整三行的高度。
 *
 * 列数随断点反转：
 *   - 手机（<sm）：**双列并排**。单列时 10 个字段各占一行，而值多数只有
 *     2~7 个字符（"AAAA"、"命中"、"27 ms"），整行右侧四十多个字符宽全是空的。
 *     两两配对把行数压掉一半，横向空间才真正被用上。
 *   - 桌面（≥sm）：单列。此时外层已经左右分了两栏，栏内再分两列会把值挤到折行。
 */
function Group({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section>
      <p className="mb-0 border-t border-border/40 pt-0.5 text-[9.5px] font-medium uppercase leading-[1.25] tracking-[0.1em] text-muted-foreground/75">
        {label}
      </p>
      <dl className="grid grid-cols-2 gap-x-3 gap-y-0 sm:grid-cols-1">{children}</dl>
    </section>
  )
}

/**
 * KV 是一行「标签 | 值」。
 *
 * 标签窄列右对齐 + 值左对齐，让同一列里所有值形成一条竖直的起跑线，
 * 扫读时眼睛只做水平移动，不必在参差的左边界之间来回找。
 *
 * 标签宽度取 2.25rem（36px）：标签全是两三个汉字（类型/协议/缓存/ECS/IP…），
 * 11px 下两个字只要 24px，给更宽会让「IP」这种短标签左侧空出一大截，
 * 看起来像标签和值之间隔了一道沟。
 *
 * 行高取 1.5（19px→17px）：这是手机上 10 行同尺寸内容的累计差别，
 * 再紧就会挤到不易扫读的程度。
 *
 * 值用 break-all 换行而不是 truncate：域名、ECS 网段、应答被截断后就没法
 * 核对，而核对正是打开这个弹窗的目的。
 *
 * span 让长值在手机双列布局里独占一行（ECS 网段、客户端 IP 这类
 * 16 个字符宽的值，塞进半栏必然折行）。
 */
function KV({
  k,
  v,
  mono,
  tone,
  span,
}: {
  k: string
  v: string
  mono?: boolean
  tone?: 'danger' | 'success'
  span?: boolean
}) {
  return (
    <div className={cn('flex min-w-0 items-start gap-1.5', span && 'col-span-2 sm:col-span-1')}>
      <dt className="w-9 shrink-0 text-right text-[10.5px] leading-[1.5] text-muted-foreground">
        {k}
      </dt>
      <dd
        className={cn(
          'min-w-0 flex-1 break-all text-[12.5px] leading-[1.5]',
          mono && 'font-mono tabular-nums',
          tone === 'danger' && 'text-danger',
          tone === 'success' && 'text-success',
        )}
      >
        {v}
      </dd>
    </div>
  )
}
