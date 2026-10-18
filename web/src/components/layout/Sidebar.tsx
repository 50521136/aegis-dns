import { NavLink } from 'react-router-dom'
import { motion } from 'framer-motion'
import { ShieldCheck } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { navItems } from './navItems'

export function Sidebar() {
  const { user, isAdmin } = useAuth()
  const items = navItems.filter((i) => !i.adminOnly || isAdmin)

  return (
    <aside className="m-rail hairline-r hidden md:flex md:w-64 md:flex-col lg:w-64">
      <div className="flex h-16 items-center gap-2.5 px-6">
        <div className="flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-accent">
          <ShieldCheck className="size-5" />
        </div>
        <div className="leading-tight">
          <p className="text-base font-bold tracking-tight">DNSForge</p>
          <p className="text-2xs text-muted-foreground">AegisDNS 控制台</p>
        </div>
      </div>

      <nav className="flex-1 space-y-1 overflow-y-auto px-3 py-4">
        {items.map((item) => {
          const Icon = item.icon
          return (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                cn(
                  'relative flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition-all duration-150',
                  isActive
                    ? 'm-pill text-foreground'
                    : 'text-muted-foreground hover:bg-[var(--hover-bg)] hover:text-foreground',
                )
              }
            >
              {({ isActive }) => (
                <>
                  {isActive && (
                    <motion.span
                      layoutId="sidebar-active"
                      className="absolute inset-y-2 left-1 w-1 rounded-full bg-primary"
                      transition={{ type: 'spring', stiffness: 500, damping: 34 }}
                    />
                  )}
                  <Icon className={cn('size-[18px] shrink-0', isActive && 'text-primary')} />
                  <span>{item.label}</span>
                </>
              )}
            </NavLink>
          )
        })}
      </nav>

      <div className="hairline-t p-4">
        <div className="m-groove flex items-center gap-3 rounded-xl p-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-full bg-primary/15 text-sm font-semibold text-primary uppercase">
            {user?.username?.slice(0, 1) ?? '?'}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">{user?.username}</p>
            <p className="truncate text-2xs text-muted-foreground">
              {isAdmin ? '管理员' : '普通用户'}
            </p>
          </div>
        </div>
      </div>
    </aside>
  )
}
