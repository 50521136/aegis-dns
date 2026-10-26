import { NavLink } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { useUnreadCounts } from '@/hooks/useUnreadCounts'
import { navItems } from './navItems'
import { NavBadge } from './NavBadge'

/**
 * 移动端底部 TabBar（< 768px），最多 5 个主入口。
 *
 * 红点规则（需求 4）：用户和管理员都要，且只标在真正有未读的入口上 ——
 *   - 「通知」入口：未读通知数（数字）；
 *   - 「反馈」入口：有回复没看 / 有追问没处理的条数（数字）；
 *   - 管理员的「反馈处理」入口同理。
 *
 * 底部栏空间极窄，数字超过 99 会用 99+ 收住（见 NavBadge）。
 */
export function BottomTabBar() {
  const { isAdmin } = useAuth()
  const { notifications, feedback } = useUnreadCounts()
  const items = navItems
    .filter((i) => i.mobile && (i.adminOnly ? isAdmin : !(i.userOnly && isAdmin)))
    .slice(0, 5)

  const badgeFor = (to: string): number => {
    if (to === '/app/notifications') return notifications
    if (to === '/app/feedback' || to === '/admin/feedback') return feedback
    return 0
  }

  return (
    <nav className="m-rail hairline-t pb-safe fixed inset-x-0 bottom-0 z-40 md:hidden">
      <div className="grid grid-cols-5">
        {items.map((item) => {
          const Icon = item.icon
          const badge = badgeFor(item.to)
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
                  <span className="relative inline-flex">
                    <Icon className={cn('size-5', isActive && 'drop-shadow-sm')} />
                    <NavBadge count={badge} />
                  </span>
                  <span className="text-2xs font-medium">
                    {item.label}
                    {/* 无障碍：红点是视觉提示，读屏用户靠这段文字知道有未读 */}
                    {badge > 0 && <span className="sr-only">（{badge} 条未读）</span>}
                  </span>
                </>
              )}
            </NavLink>
          )
        })}
      </div>
    </nav>
  )
}
