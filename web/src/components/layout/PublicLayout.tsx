import { Link, Outlet } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'
import { ThemeToggle } from './ThemeToggle'

export function PublicLayout() {
  return (
    <div className="flex min-h-[100dvh] flex-col bg-background">
      <header className="sticky top-0 z-30 border-b border-border/60 bg-background/85 backdrop-blur-lg">
        <div className="mx-auto flex h-16 w-full max-w-6xl items-center justify-between px-4 md:px-6">
          <Link to="/" className="flex items-center gap-2.5">
            <div className="flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
              <ShieldCheck className="size-5" />
            </div>
            <span className="text-base font-bold tracking-tight">DNSForge</span>
          </Link>
          <div className="flex items-center gap-2">
            <ThemeToggle />
            <Link
              to="/login"
              className="rounded-xl px-4 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              登录
            </Link>
            <Link
              to="/register"
              className="rounded-xl bg-primary px-4 py-2 text-sm font-medium text-primary-foreground shadow-sm transition-colors hover:bg-primary/90"
            >
              免费注册
            </Link>
          </div>
        </div>
      </header>

      <main className="flex-1">
        <Outlet />
      </main>

      <footer className="border-t border-border/60 py-6">
        <div className="mx-auto flex w-full max-w-6xl flex-col items-center justify-between gap-2 px-4 text-xs text-muted-foreground sm:flex-row md:px-6">
          <p>© {new Date().getFullYear()} DNSForge · 多租户加密 DNS 服务</p>
          <p className="font-mono">DoT · DoH · UDP/TCP 53</p>
        </div>
      </footer>
    </div>
  )
}
