import {
  Activity,
  BarChart3,
  BookOpen,
  LayoutDashboard,
  ListFilter,
  Lock,
  MessageSquareWarning,
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
  /** 仅普通用户可见：管理员是控制面角色，不参与租户侧的规则/订阅/日志 */
  userOnly?: boolean
  end?: boolean
  /** 分组：普通用户只看到 main，管理员额外看到 admin */
  group: 'main' | 'admin'
}

export const navItems: NavItem[] = [
  // ── 租户功能（管理员看不到）──
  { to: '/app', label: '总览', icon: LayoutDashboard, mobile: true, end: true, userOnly: true, group: 'main' },
  { to: '/app/setup', label: '我的 DNS', icon: BookOpen, mobile: true, userOnly: true, group: 'main' },
  { to: '/app/rules', label: '规则', icon: ListFilter, mobile: true, userOnly: true, group: 'main' },
  { to: '/app/subscriptions', label: '订阅', icon: Shield, mobile: true, userOnly: true, group: 'main' },
  { to: '/app/stats', label: '统计', icon: BarChart3, userOnly: true, group: 'main' },
  { to: '/app/logs', label: '查询日志', icon: ScrollText, userOnly: true, group: 'main' },
  { to: '/app/feedback', label: '反馈', icon: MessageSquareWarning, mobile: true, userOnly: true, group: 'main' },

  // ── 账号设置：两边都要（改密码、令牌）──
  { to: '/app/settings', label: '设置', icon: Settings, mobile: true, group: 'main' },

  // ── 仅管理员 ──
  { to: '/admin', label: '管理后台', icon: Activity, adminOnly: true, mobile: true, end: true, group: 'admin' },
  { to: '/admin/logs', label: '全局日志', icon: ScrollText, adminOnly: true, mobile: true, group: 'admin' },
  { to: '/admin/rules', label: '全局规则', icon: ListFilter, adminOnly: true, mobile: true, group: 'admin' },
  { to: '/admin/subscriptions', label: '全局订阅', icon: Shield, adminOnly: true, group: 'admin' },
  { to: '/admin/tls', label: '证书', icon: Lock, adminOnly: true, group: 'admin' },
  {
    to: '/admin/feedback',
    label: '反馈处理',
    icon: MessageSquareWarning,
    adminOnly: true,
    mobile: true,
    group: 'admin',
  },
]

/** 分组标题：main 不显示标题，admin 显示「管理员」分隔 */
export const GROUP_LABEL: Record<NavItem['group'], string> = {
  main: '',
  admin: '管理员',
}
