import * as React from 'react'
import { useEffect, useRef, useState } from 'react'
import { ArrowDownRight, ArrowUpRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatNumber } from '@/lib/format'

interface StatCardProps {
  label: string
  value: number | string
  icon?: React.ReactNode
  /** 变化率，例如 0.123 表示 +12.3% */
  delta?: number
  deltaLabel?: string
  /** 数值颜色 */
  tone?: 'default' | 'primary' | 'success' | 'danger' | 'warning'
  suffix?: string
  className?: string
  /** 是否启用数字滚动动画 */
  animate?: boolean
  /** 卡片右上角的操作按钮（如「清除缓存」） */
  action?: React.ReactNode
}

const toneMap: Record<NonNullable<StatCardProps['tone']>, string> = {
  default: 'text-foreground',
  primary: 'text-primary',
  success: 'text-success',
  danger: 'text-danger',
  warning: 'text-warning',
}

const iconToneMap: Record<NonNullable<StatCardProps['tone']>, string> = {
  default: 'bg-muted text-foreground',
  primary: 'bg-primary/10 text-primary',
  success: 'bg-success/12 text-success',
  danger: 'bg-danger/12 text-danger',
  warning: 'bg-warning/15 text-warning',
}

/** 数字滚动动画（count-up），尊重 prefers-reduced-motion */
function useCountUp(target: number, enabled: boolean, duration = 800) {
  const [value, setValue] = useState(enabled ? 0 : target)
  const frame = useRef<number | null>(null)

  useEffect(() => {
    if (!enabled || typeof target !== 'number' || Number.isNaN(target)) {
      setValue(target)
      return
    }
    const reduce =
      typeof window !== 'undefined' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduce) {
      setValue(target)
      return
    }
    const start = performance.now()
    const from = 0
    const tick = (now: number) => {
      const p = Math.min((now - start) / duration, 1)
      const eased = 1 - Math.pow(1 - p, 3)
      setValue(Math.round(from + (target - from) * eased))
      if (p < 1) frame.current = requestAnimationFrame(tick)
    }
    frame.current = requestAnimationFrame(tick)
    return () => {
      if (frame.current) cancelAnimationFrame(frame.current)
    }
  }, [target, enabled, duration])

  return value
}

export function StatCard({
  label,
  value,
  icon,
  delta,
  deltaLabel,
  tone = 'default',
  suffix,
  className,
  animate = true,
  action,
}: StatCardProps) {
  const isNumber = typeof value === 'number'
  const animated = useCountUp(isNumber ? value : 0, isNumber && animate)
  const display = isNumber ? formatNumber(animated) : value

  return (
    <div
      className={cn(
        'm-surface group flex flex-col rounded-2xl p-5 transition-all duration-200 hover:-translate-y-0.5',
        className,
      )}
    >
      {/* 眉标：等宽 + 宽字距的小节标题。
          标题必须 nowrap：手机两列时卡片只有 170px 左右，
          标题行里一旦再塞进按钮，中文会被逐字竖排（「缓/存/命/中」）。 */}
      <div className="flex items-start justify-between gap-2">
        <span className="eyebrow min-w-0 truncate whitespace-nowrap">{label}</span>
        {icon && (
          <div className={cn('flex size-7 shrink-0 items-center justify-center rounded-lg', iconToneMap[tone])}>
            {icon}
          </div>
        )}
      </div>

      {/* 读数居中：四张卡数字都在中线上，扫视时不用左右找 */}
      <div className={cn('mt-3 flex items-baseline justify-center', toneMap[tone])}>
        <span className="readout readout-stat">{display}</span>
        {suffix && <span className="readout-unit">{suffix}</span>}
      </div>

      {delta !== undefined && (
        <div className="mt-2.5 flex items-center justify-center gap-1.5 text-xs">
          <span
            className={cn(
              'inline-flex items-center gap-0.5 rounded-full px-1.5 py-0.5 font-mono font-medium tabular-nums',
              delta >= 0 ? 'bg-success/12 text-success' : 'bg-danger/12 text-danger',
            )}
          >
            {delta >= 0 ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
            {Math.abs(delta * 100).toFixed(1)}%
          </span>
          {deltaLabel && <span className="text-muted-foreground">{deltaLabel}</span>}
        </div>
      )}

      {/* 操作放数字下方：放在标题行会把标题挤成竖排 */}
      {action && <div className="mt-2.5 flex justify-center">{action}</div>}

      {/* 刻度尺贴底对齐：有/无环比行的卡片，刻度都在同一水平线上 */}
      <div className="tick-row mt-auto pt-4" aria-hidden="true">
        {Array.from({ length: 12 }).map((_, i) => (
          <i key={i} className={i % 4 === 0 ? 'strong' : undefined} />
        ))}
      </div>
    </div>
  )
}
