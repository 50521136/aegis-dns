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
}: StatCardProps) {
  const isNumber = typeof value === 'number'
  const animated = useCountUp(isNumber ? value : 0, isNumber && animate)
  const display = isNumber ? formatNumber(animated) : value

  return (
    <div
      className={cn(
        'group rounded-2xl border border-border/50 bg-card p-6 shadow-sm transition-all duration-200 hover:shadow-md hover:-translate-y-0.5',
        className,
      )}
    >
      <div className="flex items-center gap-2.5">
        {icon && (
          <div className={cn('flex size-8 items-center justify-center rounded-lg', iconToneMap[tone])}>
            {icon}
          </div>
        )}
        <span className="text-sm text-muted-foreground">{label}</span>
      </div>

      <div className={cn('mt-3 flex items-baseline gap-1.5', toneMap[tone])}>
        <span className="text-4xl font-bold leading-none tabular-nums">{display}</span>
        {suffix && <span className="text-lg font-semibold text-muted-foreground">{suffix}</span>}
      </div>

      {delta !== undefined && (
        <div className="mt-2 flex items-center gap-1 text-xs">
          <span
            className={cn(
              'inline-flex items-center gap-0.5 font-medium',
              delta >= 0 ? 'text-success' : 'text-danger',
            )}
          >
            {delta >= 0 ? <ArrowUpRight className="size-3.5" /> : <ArrowDownRight className="size-3.5" />}
            {Math.abs(delta * 100).toFixed(1)}%
          </span>
          {deltaLabel && <span className="text-muted-foreground">{deltaLabel}</span>}
        </div>
      )}
    </div>
  )
}
