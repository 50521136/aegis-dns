import * as React from 'react'
import { cn } from '@/lib/utils'

interface EmptyStateProps {
  icon?: React.ReactNode
  title: string
  description?: string
  action?: React.ReactNode
  className?: string
}

export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 rounded-2xl border border-dashed border-border bg-card/40 px-6 py-14 text-center',
        className,
      )}
    >
      {icon && (
        <div className="flex size-14 items-center justify-center rounded-2xl bg-muted text-muted-foreground">
          {icon}
        </div>
      )}
      <div className="space-y-1">
        <p className="text-base font-semibold">{title}</p>
        {description && <p className="max-w-md text-sm text-muted-foreground">{description}</p>}
      </div>
      {action && <div className="mt-1">{action}</div>}
    </div>
  )
}
