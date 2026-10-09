import * as React from 'react'
import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface CheckboxProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, 'type'> {
  onCheckedChange?: (checked: boolean) => void
}

/** 轻量复选框（原生 input + 视觉层），保持触摸目标 >= 24px 命中区 */
const Checkbox = React.forwardRef<HTMLInputElement, CheckboxProps>(
  ({ className, checked, onCheckedChange, onChange, ...props }, ref) => {
    return (
      <span className="relative inline-flex size-5 shrink-0 items-center justify-center">
        <input
          ref={ref}
          type="checkbox"
          checked={checked}
          onChange={(e) => {
            onChange?.(e)
            onCheckedChange?.(e.target.checked)
          }}
          className={cn(
            'peer size-5 cursor-pointer appearance-none rounded-md border border-border bg-background transition-colors',
            'checked:border-primary checked:bg-primary',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30',
            'disabled:cursor-not-allowed disabled:opacity-50',
            className,
          )}
          {...props}
        />
        <Check className="pointer-events-none absolute size-3.5 text-primary-foreground opacity-0 peer-checked:opacity-100" />
      </span>
    )
  },
)
Checkbox.displayName = 'Checkbox'

export { Checkbox }
