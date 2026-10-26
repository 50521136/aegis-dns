import { cn } from '@/lib/utils'

/**
 * NavBadge 是导航项右上角的未读红点 / 计数徽章。
 *
 * 为什么区分「数字」和「纯圆点」：
 *   - 数量在 1~99 时显示数字，用户能判断值不值得点进去；
 *   - 超过 99 显示 99+，避免徽章被数字撑宽、把导航文字挤歪；
 *   - 桌面端侧栏是窄条 + 文字，塞数字容易和标签打架，所以用纯圆点。
 *
 * absolute 定位 + translate 让它压在图标右上角而不参与布局 ——
 * 参与布局会让「有红点」和「没红点」时图标位置不同，切换时整行抖动。
 */
export function NavBadge({
  count,
  dot = false,
  className,
}: {
  count: number
  /** true 时只画一个圆点（不显示数字），用于空间紧张的位置。 */
  dot?: boolean
  className?: string
}) {
  if (!count || count <= 0) return null

  if (dot) {
    return (
      <span
        aria-label={`${count} 条未读`}
        className={cn(
          'absolute -right-0.5 -top-0.5 size-2 rounded-full bg-danger ring-2 ring-background',
          className,
        )}
      />
    )
  }

  const label = count > 99 ? '99+' : String(count)
  return (
    <span
      aria-label={`${count} 条未读`}
      className={cn(
        // min-w 而不是固定宽度：一位数是圆点状，两位数自然变宽，
        // 三位数以上用 99+ 兜住，不会被撑成一条。
        'absolute -right-1.5 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[9.5px] font-semibold leading-none text-white tabular-nums ring-2 ring-background',
        className,
      )}
    >
      {label}
    </span>
  )
}
