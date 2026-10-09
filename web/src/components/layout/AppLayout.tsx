import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { AnimatePresence, motion } from 'framer-motion'
import { ShieldCheck, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { useIsDesktop } from '@/hooks/useMediaQuery'
import { Sidebar } from './Sidebar'
import { BottomTabBar } from './BottomTabBar'
import { TopBar } from './TopBar'
import { navItems } from './navItems'

function MobileDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { isAdmin } = useAuth()
  const items = navItems.filter((i) => !i.adminOnly || isAdmin)
  return (
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
            className="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm md:hidden"
            onClick={onClose}
          />
          <motion.aside
            initial={{ x: '-100%' }}
            animate={{ x: 0 }}
            exit={{ x: '-100%' }}
            transition={{ type: 'spring', stiffness: 400, damping: 36 }}
            className="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[80vw] flex-col border-r border-border bg-card pt-safe md:hidden"
          >
            <div className="flex h-16 items-center justify-between px-5">
              <div className="flex items-center gap-2.5">
                <div className="flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground">
                  <ShieldCheck className="size-5" />
                </div>
                <p className="text-base font-bold">DNSForge</p>
              </div>
              <button
                onClick={onClose}
                className="flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted"
                aria-label="关闭菜单"
              >
                <X className="size-5" />
              </button>
            </div>
            <nav className="flex-1 space-y-1 overflow-y-auto px-3 py-4">
              {items.map((item) => {
                const Icon = item.icon
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.end}
                    onClick={onClose}
                    className={({ isActive }) =>
                      cn(
                        'flex items-center gap-3 rounded-xl px-3 py-3 text-sm font-medium transition-colors',
                        isActive
                          ? 'bg-primary/10 text-primary'
                          : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                      )
                    }
                  >
                    <Icon className="size-5" />
                    {item.label}
                  </NavLink>
                )
              })}
            </nav>
          </motion.aside>
        </>
      )}
    </AnimatePresence>
  )
}

export function AppLayout() {
  const location = useLocation()
  const isDesktop = useIsDesktop()
  const [drawerOpen, setDrawerOpen] = useState(false)

  // 路由变化时滚动到顶部
  useEffect(() => {
    window.scrollTo({ top: 0, behavior: 'auto' })
  }, [location.pathname])

  const title = navItems.find((i) => location.pathname === i.to)?.label

  return (
    <div className="flex min-h-[100dvh] bg-background">
      <Sidebar />

      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar title={isDesktop ? undefined : title} onOpenNav={() => setDrawerOpen(true)} />

        <main className="flex-1 px-4 py-5 pb-24 md:px-6 md:py-6 md:pb-8">
          <motion.div
            key={location.pathname}
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
            className="mx-auto w-full max-w-6xl"
          >
            <Outlet />
          </motion.div>
        </main>
      </div>

      <BottomTabBar />
      <MobileDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} />
    </div>
  )
}
