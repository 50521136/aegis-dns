import { Loader2 } from 'lucide-react'

/**
 * RouteFallback 是路由懒加载的占位。
 *
 * 刻意做得极简：一个居中的转圈 + 一行灰字，不画骨架屏。
 * 理由是这个状态通常只持续几十毫秒（chunk 走本地缓存或很快到达），
 * 复杂骨架屏反而会闪一下再消失，比什么都不画更难受。
 *
 * 高度用 min-h-[50vh] 而不是 min-h-screen：嵌在 AppLayout 的内容区里，
 * 撑满整屏会把侧栏一起挤动。
 */
export function RouteFallback() {
  return (
    <div className="flex min-h-[50vh] flex-col items-center justify-center gap-3">
      <Loader2 className="size-5 animate-spin text-muted-foreground" />
      <p className="text-xs text-muted-foreground">加载中…</p>
    </div>
  )
}
