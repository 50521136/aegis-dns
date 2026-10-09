import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn('size-5 animate-spin text-muted-foreground', className)} />
}

export function FullPageSpinner({ label = '加载中…' }: { label?: string }) {
  return (
    <div className="flex min-h-[60dvh] flex-col items-center justify-center gap-3 text-muted-foreground">
      <Spinner className="size-7 text-primary" />
      <p className="text-sm">{label}</p>
    </div>
  )
}
