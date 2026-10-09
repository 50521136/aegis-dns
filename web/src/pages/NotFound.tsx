import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { Button } from '@/components/ui/button'

export default function NotFound() {
  return (
    <div className="flex min-h-[70dvh] flex-col items-center justify-center gap-5 px-6 text-center">
      <div className="flex size-16 items-center justify-center rounded-2xl bg-primary/10 text-primary">
        <Compass className="size-8" />
      </div>
      <div className="space-y-1">
        <h1 className="text-4xl font-bold tracking-tight">404</h1>
        <p className="text-sm text-muted-foreground">你访问的页面不存在或已被移动。</p>
      </div>
      <Button asChild>
        <Link to="/">返回首页</Link>
      </Button>
    </div>
  )
}
