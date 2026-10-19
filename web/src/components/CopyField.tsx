import { Check, Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useCopy } from '@/hooks/useCopy'
import { cn } from '@/lib/utils'

interface CopyFieldProps {
  value: string
  label?: string
  mono?: boolean
  className?: string
}

/** 带复制按钮的只读字段，用于展示 DNS 地址等 */
export function CopyField({ value, label, mono = true, className }: CopyFieldProps) {
  const { copied, copy } = useCopy()
  return (
    <div className={cn('space-y-1.5', className)}>
      {label && <p className="text-xs font-medium text-muted-foreground">{label}</p>}
      {/* min-w-0 必须有：Grid/Flex 子项默认 min-width:auto，长地址会把容器撑爆、
          把右侧复制按钮挤出卡片。地址用 break-all 换行，不截断 —— 截断后用户没法核对。 */}
      <div className="flex min-w-0 items-center gap-2 rounded-xl border border-border bg-background p-1 pl-3.5">
        <code
          className={cn(
            'min-w-0 flex-1 break-all text-sm leading-snug',
            mono ? 'font-mono text-[13px]' : 'font-sans',
          )}
          title={value}
        >
          {value}
        </code>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={() => copy(value)}
          aria-label="复制"
          title="复制"
        >
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
      </div>
    </div>
  )
}
