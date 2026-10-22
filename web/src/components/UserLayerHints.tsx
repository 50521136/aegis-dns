import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Globe, GlobeLock } from 'lucide-react'

import { getMe } from '@/api/me'
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
