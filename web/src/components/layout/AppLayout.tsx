import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { motion } from 'framer-motion'
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
  const items = navItems.filter((i) => (i.adminOnly ? isAdmin : !(i.userOnly && isAdmin)))

  // Esc 关闭。移动端用不到，但平板接了键盘、桌面窄窗口调试时都需要。
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  // 抽屉打开时锁住背景滚动。
  // 不锁的话手指在遮罩上滑会滚动下面的页面，关掉抽屉后位置已经跑偏。
  useEffect(() => {
    if (!open) return
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [open])

  // 这里**故意不用 AnimatePresence + exit 动画**。
  //
  // 踩过的坑：原来用 <AnimatePresence> 包 motion 元素做滑入滑出，结果是
  // 点导航跳转后抽屉一直盖在屏幕上，点 X 和点遮罩都关不掉，只能刷新。
  // 实测确认 state 已经变成 false（body 滚动锁定被正确还原了），但 DOM
  // 停在 transform:none / opacity:1 的「打开态」，AnimatePresence 的
  // 退出动画既没播放也没回调 —— 元素成了孤儿。
  //
  // 现在改成「DOM 常驻 + CSS 过渡控制显隐」：
  // 关闭时用 -translate-x-full 移出视口并 pointer-events-none，
  // 只影响合成层，不涉及卸载，因此不存在「动画没跑完就卡住」这种状态。
  // 一个抽屉的滑入滑出本来也不需要动画库来编排。
  return (
    <>
      {/* 遮罩：关闭时透明且不接收点击 */}
      <div
        aria-hidden={!open}
        onClick={onClose}
        className={cn(
          'fixed inset-0 z-50 bg-black/50 backdrop-blur-sm transition-opacity duration-200 md:hidden',
          open ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
      />
      <aside
        aria-hidden={!open}
        aria-label="导航菜单"
        className={cn(
          'fixed inset-y-0 left-0 z-50 flex w-72 max-w-[80vw] flex-col border-r border-border bg-card pt-safe',
          'transition-transform duration-200 ease-out md:hidden',
          // 关闭时移出视口 + 禁止交互，双保险：即使过渡被浏览器跳过也不会挡住页面
          open ? 'translate-x-0' : 'pointer-events-none -translate-x-full',
        )}
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
                tabIndex={open ? 0 : -1}
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
      </aside>
    </>
  )
}

export function AppLayout() {
  const location = useLocation()
  const isDesktop = useIsDesktop()
  const [drawerOpen, setDrawerOpen] = useState(false)

  // 路由一变就无条件关掉抽屉。
  //
  // 这是上一版那个「侧边栏关不掉」bug 的兜底：不管用户是点导航跳转、
  // 点浏览器后退、还是程序里 push 了新路由，只要路径变了，抽屉一定收起。
  // 只依赖 NavLink 的 onClick 是不够的 —— 任何一次动画或事件异常都会
  // 让它停留在打开状态，而那种状态下用户只能刷新。
  useEffect(() => {
    setDrawerOpen(false)
  }, [location.pathname])

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

        <main className="flex-1 px-4 py-5 pb-[calc(7rem+env(safe-area-inset-bottom))] md:px-6 md:py-6 md:pb-8">
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
