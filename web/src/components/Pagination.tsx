import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  /** 传了才显示每页条数选择器；不传则维持固定条数 */
  onPageSizeChange?: (size: number) => void
  pageSizeOptions?: number[]
  className?: string
}

/** 当前页左右各显示几个页码 */
const SIBLINGS = 1
/** 少于等于这个页数就全列出来，不做省略 */
const NO_GAP_LIMIT = 7
/** 点省略号一次跳几页 */
const GAP_JUMP = 5

const DEFAULT_SIZES = [10, 20, 50, 100]

/**
 * 把总页数压成一个带省略号的滑动窗口。
 *
 * 之前只显示「第 X / 共 Y 页」+ 前后翻，页数一多就只能一页页点过去。
 * 这里始终保留首页和末页，中间只露出当前页附近的几个，跨度大时插省略号。
 */
function buildWindow(current: number, totalPages: number): (number | 'gap-back' | 'gap-fwd')[] {
  if (totalPages <= NO_GAP_LIMIT) {
    return Array.from({ length: totalPages }, (_, i) => i + 1)
  }

  const left = Math.max(2, current - SIBLINGS)
  const right = Math.min(totalPages - 1, current + SIBLINGS)

  // gap 带方向：省略号可点击，左省略号往回跳、右省略号往前跳。
  // 不这么做的话 26 页时想跳远只能一页页点「下一页」，等于没有分页器。
  const out: (number | 'gap-back' | 'gap-fwd')[] = [1]
  if (left > 2) out.push('gap-back')
  for (let p = left; p <= right; p++) out.push(p)
  if (right < totalPages - 1) out.push('gap-fwd')
  out.push(totalPages)
  return out
}

export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = DEFAULT_SIZES,
  className,
}: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1
  const to = Math.min(page * pageSize, total)
  const pages = buildWindow(page, totalPages)

  const changeSize = (v: string) => {
    const size = Number(v)
    if (!onPageSizeChange || size === pageSize) return
    // 换条数后原页码会指向完全不同的内容，一律回到第一页
    onPageSizeChange(size)
    onPageChange(1)
  }

  return (
    <div
      className={cn(
        'flex flex-col gap-3 text-sm sm:flex-row sm:items-center sm:justify-between',
        className,
      )}
    >
      <div className="flex items-center gap-3">
        <span className="text-muted-foreground tabular-nums">
          {from}-{to} / 共 {total} 条
        </span>
        {onPageSizeChange && (
          <Select value={String(pageSize)} onValueChange={changeSize}>
            <SelectTrigger className="h-8 w-[7.5rem] text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {pageSizeOptions.map((s) => (
                <SelectItem key={s} value={String(s)}>
                  每页 {s} 条
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>

      <div className="flex items-center gap-1.5">
        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => onPageChange(page - 1)}
          disabled={page <= 1}
          aria-label="上一页"
        >
          <ChevronLeft />
        </Button>

        {/* 手机宽度塞不下页码条，只留前后翻 + 页码读数 */}
        <div className="hidden items-center gap-1 sm:flex">
          {pages.map((p, i) =>
            p === 'gap-back' || p === 'gap-fwd' ? (
              <button
                key={`${p}-${i}`}
                onClick={() =>
                  onPageChange(
                    Math.min(
                      totalPages,
                      Math.max(1, p === 'gap-back' ? page - GAP_JUMP : page + GAP_JUMP),
                    ),
                  )
                }
                aria-label={p === 'gap-back' ? `往前跳 ${GAP_JUMP} 页` : `往后跳 ${GAP_JUMP} 页`}
                className="min-w-8 rounded-lg px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-[var(--hover-bg)] hover:text-foreground"
              >
                …
              </button>
            ) : (
              <button
                key={p}
                onClick={() => onPageChange(p)}
                aria-current={p === page ? 'page' : undefined}
                className={cn(
                  'min-w-8 rounded-lg px-2 py-1 text-xs tabular-nums transition-colors',
                  p === page
                    ? 'm-pill font-semibold text-foreground'
                    : 'text-muted-foreground hover:bg-[var(--hover-bg)] hover:text-foreground',
                )}
              >
                {p}
              </button>
            ),
          )}
        </div>

        <span className="min-w-[3.5rem] text-center text-xs tabular-nums text-muted-foreground sm:hidden">
          {page} / {totalPages}
        </span>

        <Button
          variant="outline"
          size="icon-sm"
          onClick={() => onPageChange(page + 1)}
          disabled={page >= totalPages}
          aria-label="下一页"
        >
          <ChevronRight />
        </Button>
      </div>
    </div>
  )
}
