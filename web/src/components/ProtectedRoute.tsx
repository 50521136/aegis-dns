import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { ShieldAlert } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'
import { FullPageSpinner } from '@/components/ui/spinner'
import { Button } from '@/components/ui/button'
import { Link } from 'react-router-dom'

/** 需要登录的路由守卫 */
export function ProtectedRoute() {
  const { isAuthenticated, initializing } = useAuth()
  const location = useLocation()

  if (initializing) {
    return (
      <div className="flex min-h-[100dvh] items-center justify-center">
        <FullPageSpinner label="正在恢复会话…" />
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }

  return <Outlet />
}

/** 需要 admin 角色的路由守卫 */
export function AdminRoute() {
  const { isAdmin, initializing } = useAuth()

  if (initializing) {
    return (
      <div className="flex min-h-[100dvh] items-center justify-center">
        <FullPageSpinner />
      </div>
    )
  }

  if (!isAdmin) {
    return (
      <div className="flex min-h-[70dvh] flex-col items-center justify-center gap-4 px-6 text-center">
        <div className="flex size-16 items-center justify-center rounded-2xl bg-danger/12 text-danger">
          <ShieldAlert className="size-8" />
        </div>
        <div className="space-y-1">
          <h1 className="text-2xl font-bold">403 · 无权访问</h1>
          <p className="text-sm text-muted-foreground">此页面仅限管理员访问。</p>
        </div>
        <Button asChild variant="outline">
          <Link to="/app">返回控制台</Link>
        </Button>
      </div>
    )
  }

  return <Outlet />
}
