import { NavLink } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { navItems } from './navItems'

/** 移动端底部 TabBar（< 768px），最多 5 个主入口 */
export function BottomTabBar() {
  const { isAdmin } = useAuth()
  const items = navItems
    .filter((i) => i.mobile && (i.adminOnly ? isAdmin : !(i.userOnly && isAdmin)))
    .slice(0, 5)

  return (
    <nav className="m-rail hairline-t pb-safe fixed inset-x-0 bottom-0 z-40 md:hidden">
      <div className="grid grid-cols-5">
        {items.map((item) => {
          const Icon = item.icon
          return (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                cn(
                  'flex min-h-[56px] flex-col items-center justify-center gap-1 px-1 py-2 transition-colors',
                  isActive ? 'text-primary' : 'text-muted-foreground',
                )
              }
            >
              {({ isActive }) => (
                <>
                  <Icon className={cn('size-5', isActive && 'drop-shadow-sm')} />
                  <span className="text-2xs font-medium">{item.label}</span>
                </>
              )}
            </NavLink>
          )
        })}
      </div>
    </nav>
  )
}
