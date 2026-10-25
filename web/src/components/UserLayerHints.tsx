import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Globe, GlobeLock, ShieldCheck } from 'lucide-react'

import { getGlobalLayer, getMe } from '@/api/me'
import { cn } from '@/lib/utils'

/**
 * GlobalLayerHint 显示「管理员的全局规则/订阅是否叠加到你身上」。
 *
 * 这个开关在「设置」里，但用户在规则页看到的效果（某个域名还是被拦）
 * 取决于它，跑到设置页去查很别扭 —— 直接在这一页给他一个可点的提示。
 *
 * 只在用户侧渲染：管理员看自己的全局页时不需要这个提示。
 */
export function GlobalLayerHint({ className }: { className?: string }) {
  const me = useQuery({ queryKey: ['me'], queryFn: getMe })
  if (me.isLoading) return null

  const on = me.data?.use_global_rules !== false

  return (
    <Link
      to="/app/settings"
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
        on
          ? 'border-success/40 bg-success/10 text-success hover:bg-success/15'
          : 'border-border bg-muted/40 text-muted-foreground hover:bg-muted',
        className,
      )}
      title={
        on
          ? '管理员下发的全局规则与全局订阅当前对你在生效'
          : '全局规则与全局订阅当前没有叠加，只有你自己的规则/订阅在生效'
      }
    >
      {on ? <Globe className="size-3.5" /> : <GlobeLock className="size-3.5" />}
      全局层{on ? '已叠加' : '未叠加'}
    </Link>
  )
}

/**
 * QuotaReadout 是「已用 / 上限」的读数条。
 *
 * 用尽时变红：用户加到上限应该当场看见，而不是提交时才被拒绝。
 * limit 为 0 表示不限，此时只显示已用数量。
 */
export function QuotaReadout({
  used,
  limit,
  unit,
  label = '配额',
}: {
  used: number
  limit: number
  unit: string
  label?: string
}) {
  const full = limit > 0 && used >= limit
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="eyebrow">{label}</span>
      <span className={cn('readout text-sm tabular-nums', full ? 'text-danger' : 'text-foreground')}>
        {used}
      </span>
      <span className="font-mono text-muted-foreground">
        / {limit > 0 ? limit : '不限'}
      </span>
      <span className="text-muted-foreground">{unit}</span>
      {limit > 0 && (
        <div className="h-1 w-20 overflow-hidden rounded-full bg-muted">
          <div
            className={cn('h-full rounded-full', full ? 'bg-danger' : 'bg-primary')}
            style={{ width: `${Math.min(100, Math.round((used / limit) * 100))}%` }}
          />
        </div>
      )}
      {full && <span className="text-danger">已达上限</span>}
    </div>
  )
}

/**
 * GlobalLayerBanner 告诉用户「管理员已经内置了一批规则/订阅，一般无需自己添加」。
 *
 * 解决的是一个很具体的体感问题：管理员在全局层挂好公共规则集之后，
 * 用户打开「我的规则」看到的是**一片空白**（全局规则属于管理员，不在他的列表里），
 * 于是以为自己没有任何防护，再挂一份 OISD 之类的大订阅 ——
 * 订阅条目会按用户压平进快照，重复挂既吃他自己的订阅配额，也吃服务器内存。
 *
 * 三条设计约束：
 *  1. **数字必须是真的**。没有全局内容（或用户自己关掉了全局层）时整块不渲染 ——
 *     在没有任何规则时提示「已内置」是纯误导，比不提示更糟。
 *  2. 只报规模，不列内容。全局规则是管理员的配置，用户没必要逐条看到；
 *     他要知道的是「有多少在替我挡」。
 *  3. 给出口子。用户确实想自己加（例如某站点被全局规则误伤要自己放行）时，
 *     文案不能把话说死，只提示「一般无需」。
 */
export function GlobalLayerBanner({ className }: { className?: string }) {
  const q = useQuery({ queryKey: ['me', 'global-layer'], queryFn: getGlobalLayer, staleTime: 60_000 })
  const info = q.data
  if (!info || !info.has_rules) return null

  // 条目数上万时显示成「6.1 万」这种量级，逐位数字反而看不清规模。
  const fmt = (n: number) => (n >= 10000 ? `${(n / 10000).toFixed(1)} 万` : String(n))

  const parts: string[] = []
  if (info.sub_entry_count > 0) parts.push(`${info.sub_count} 份公共订阅（${fmt(info.sub_entry_count)} 条域名）`)
  if (info.rule_count > 0) parts.push(`${info.rule_count} 条规则`)

  return (
    <div
      className={cn(
        'flex items-start gap-3 rounded-xl border border-primary/25 bg-primary/5 px-3.5 py-3 text-sm',
        className,
      )}
      role="status"
    >
      <ShieldCheck className="mt-0.5 size-4 shrink-0 text-primary" />
      <div className="space-y-1">
        <p className="font-medium text-foreground">
          管理员已内置 {parts.join(' 与 ')}，正在为你生效
        </p>
        <p className="text-xs leading-relaxed text-muted-foreground">
          公共规则集已覆盖常见广告、追踪与恶意域名，<span className="font-medium text-foreground">一般无需自己再添加</span>
          。重复挂同一批订阅不会提升拦截效果，反而会占用你自己的订阅配额与服务器内存。
          只有在你需要针对某个特定域名单独放行/拦截时，才在这里添加。
        </p>
        <p className="text-xs text-muted-foreground">
          被全局规则误伤？可以在{' '}
          <Link to="/app/settings" className="text-primary underline underline-offset-2">
            设置 → 全局规则层
          </Link>{' '}
          临时关闭，或到{' '}
          <Link to="/app/feedback" className="text-primary underline underline-offset-2">
            误杀反馈
          </Link>{' '}
          告诉我们。
        </p>
      </div>
    </div>
  )
}
