import { AlertCircle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface ErrorStateProps {
  title?: string
  message?: string
  onRetry?: () => void
  className?: string
}

export function ErrorState({ title = '加载失败', message, onRetry, className }: ErrorStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 rounded-2xl border border-danger/30 bg-danger/5 px-6 py-12 text-center',
        className,
      )}
    >
      <div className="flex size-12 items-center justify-center rounded-2xl bg-danger/12 text-danger">
        <AlertCircle className="size-6" />
      </div>
      <div className="space-y-1">
        <p className="text-base font-semibold">{title}</p>
        {message && <p className="max-w-md break-words text-sm text-muted-foreground">{message}</p>}
      </div>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw />
          重试
        </Button>
      )}
    </div>
  )
}
