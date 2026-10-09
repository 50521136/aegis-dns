import {
  Activity,
  BarChart3,
  BookOpen,
  LayoutDashboard,
  ListFilter,
  ScrollText,
  Settings,
  Shield,
  type LucideIcon,
} from 'lucide-react'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /** 是否在移动端底部 TabBar 中展示（最多 5 个主入口） */
  mobile?: boolean
  adminOnly?: boolean
  end?: boolean
}

export const navItems: NavItem[] = [
  { to: '/app', label: '总览', icon: LayoutDashboard, mobile: true, end: true },
  { to: '/app/setup', label: '我的 DNS', icon: BookOpen, mobile: true },
  { to: '/app/rules', label: '规则', icon: ListFilter, mobile: true },
  { to: '/app/subscriptions', label: '订阅', icon: Shield, mobile: true },
  { to: '/app/stats', label: '统计', icon: BarChart3 },
  { to: '/app/logs', label: '查询日志', icon: ScrollText },
  { to: '/app/settings', label: '设置', icon: Settings, mobile: true },
  { to: '/admin', label: '管理后台', icon: Activity, adminOnly: true },
]
