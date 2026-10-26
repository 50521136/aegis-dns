import { Suspense, lazy, useEffect, useRef } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/hooks/useTheme'
import { AuthProvider } from '@/hooks/useAuth'
import { RouteErrorBoundary } from '@/components/ErrorBoundary'
import { ToastProvider, useToast } from '@/components/ui/toast'
import { PublicLayout } from '@/components/layout/PublicLayout'
import { AppLayout } from '@/components/layout/AppLayout'
import { ProtectedRoute, AdminRoute, UserOnlyRoute } from '@/components/ProtectedRoute'
import { getVersion } from '@/api/system'
import { RouteFallback } from '@/components/RouteFallback'

// 页面全部按路由懒加载。
//
// 原来这里是 20 多个同步 import，于是所有页面 + recharts + framer-motion
// 全被打进入口 chunk：单文件 1.32 MB（gzip 394 KB）。在 2 Mbps 的机器上
// 一次页面加载要 5.3 秒，而普通用户一个会话只看其中两三个页面。
//
// 拆开之后入口只留壳组件 + 落地页；Admin 那一坨（Admin.tsx 1412 行、
// 带 recharts 的图表）只有管理员进后台才会下载。
//
// 唯一刻意保留同步导入的是 Landing：它是未登录访客的第一屏，
// 让它多等一个网络往返去取 chunk 不值得。
import Landing from '@/pages/Landing'
import NotFound from '@/pages/NotFound'

// 命名导出的页面要在 lazy 里转成 default，否则 React.lazy 会拿到
// 模块对象而不是组件，报 “Element type is invalid”。
const lazyNamed = <T extends Record<string, unknown>, K extends keyof T>(
  loader: () => Promise<T>,
  name: K,
) => lazy(() => loader().then((m) => ({ default: m[name] as unknown as React.ComponentType })))

const Login = lazy(() => import('@/pages/Login'))
const Register = lazy(() => import('@/pages/Register'))
const ForgotPassword = lazy(() => import('@/pages/ForgotPassword'))
const Dashboard = lazy(() => import('@/pages/Dashboard'))
const DnsSetup = lazy(() => import('@/pages/DnsSetup'))
const Account = lazy(() => import('@/pages/Account'))
const Rules = lazy(() => import('@/pages/Rules'))
const Subscriptions = lazy(() => import('@/pages/Subscriptions'))
const QueryLog = lazy(() => import('@/pages/QueryLog'))
const Settings = lazy(() => import('@/pages/Settings'))
const Feedback = lazy(() => import('@/pages/Feedback'))
const Admin = lazy(() => import('@/pages/Admin'))
const AdminLogs = lazy(() => import('@/pages/AdminLogs'))
const AdminFeedback = lazy(() => import('@/pages/AdminFeedback'))
const AdminTls = lazy(() => import('@/pages/AdminTls'))

// Admin.tsx 里是多个命名导出，共用同一个模块 —— 用 lazyNamed 拆出来，
// 打包后它们仍然落在同一个 chunk 里（这正是我们要的：进一次后台只拉一次）。
const AdminUsersPage = lazyNamed(() => import('@/pages/Admin'), 'AdminUsersPage')
const AdminMemberPage = lazyNamed(() => import('@/pages/Admin'), 'AdminMemberPage')
const AdminAnnouncePage = lazyNamed(() => import('@/pages/Admin'), 'AdminAnnouncePage')
const AdminDnsPage = lazyNamed(() => import('@/pages/Admin'), 'AdminDnsPage')
const AdminGeneralPage = lazyNamed(() => import('@/pages/Admin'), 'AdminGeneralPage')
const AdminSecurityPage = lazyNamed(() => import('@/pages/Admin'), 'AdminSecurityPage')
const AdminEmailPage = lazyNamed(() => import('@/pages/Admin'), 'AdminEmailPage')
const AdminSystemPage = lazyNamed(() => import('@/pages/Admin'), 'AdminSystemPage')
const AdminRulesPage = lazyNamed(() => import('@/pages/AdminAssetsPages'), 'AdminRulesPage')
const AdminSubsPage = lazyNamed(() => import('@/pages/AdminAssetsPages'), 'AdminSubsPage')

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
})

/**
 * SPA 资源级更新检测（设计文档 14.2）：
 * 每 10 分钟请求 /api/v1/version，与启动时版本比对，变化则提示刷新。
 */
function VersionWatcher() {
  const toast = useToast()
  const initial = useRef<string | null>(null)

  useEffect(() => {
    let cancelled = false

    const check = async () => {
      try {
        const v = await getVersion()
        const version = String(v.version ?? '')
        if (!version) return
        if (initial.current === null) {
          initial.current = version
        } else if (version !== initial.current) {
          toast.info('服务已更新', '检测到新版本，点击右上角刷新页面以应用')
          initial.current = version
        }
      } catch {
        /* 忽略：/version 不可用不影响使用 */
      }
    }

    check()
    const timer = setInterval(check, 10 * 60 * 1000)
    return () => {
      cancelled = true
      clearInterval(timer)
      void cancelled
    }
  }, [toast])

  return null
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <ToastProvider>
          <BrowserRouter>
            <AuthProvider>
              <VersionWatcher />
              {/* 懒加载页面的加载态。放在 Routes 外层而不是每个 Route 里：
                  路由切换时整个内容区替换成占位，避免旧页面残留造成「点了没反应」的错觉。 */}
              <Suspense fallback={<RouteFallback />}>
                {/* 错误边界放在**内容区**而不是 App 根部：
                    放根部的话一个页面崩掉连侧边栏和顶栏一起没了，用户只能刷新；
                    放这里则导航还在，可以点别的页面继续用。
                    pathname 变化时自动复位，避免一处崩溃让后续页面都显示错误页。 */}
                <RouteErrorBoundary>
                <Routes>
                {/* 公共页面 */}
                <Route element={<PublicLayout />}>
                  <Route path="/" element={<Landing />} />
                  <Route path="/login" element={<Login />} />
                  <Route path="/register" element={<Register />} />
                  <Route path="/forgot-password" element={<ForgotPassword />} />
                </Route>

                {/* 需要登录 */}
                <Route element={<ProtectedRoute />}>
                  <Route element={<AppLayout />}>
                    {/* 租户功能：管理员进来会被送回 /admin（他是控制面角色，不参与租户侧） */}
                    <Route element={<UserOnlyRoute />}>
                      <Route path="/app" element={<Dashboard />} />
                      <Route path="/app/setup" element={<DnsSetup />} />
                      <Route path="/app/account" element={<Account />} />
                      <Route path="/app/rules" element={<Rules />} />
                      <Route path="/app/subscriptions" element={<Subscriptions />} />
                      <Route path="/app/logs" element={<QueryLog />} />
                      <Route path="/app/feedback" element={<Feedback />} />
                    </Route>

                    {/* 账号设置两边都要：管理员也要改密码、管令牌 */}
                    <Route element={<UserOnlyRoute />}>
                      <Route path="/app/settings" element={<Settings />} />
                    </Route>

                    {/* 仅管理员 */}
                    <Route element={<AdminRoute />}>
                      {/* 管理员落地页是总览（全站统计 + 流量排行）；
                          用户/设置/系统这些控制面功能挪到 /admin/manage */}
                      <Route path="/admin" element={<Dashboard scope="global" />} />
                      {/* 管理后台按主题拆成独立页面：设置项已经四十多个，
                          全塞一个 Tab 面板里既难找，也没法把某个页面的地址发出去 */}
                      <Route path="/admin/manage" element={<Admin />} />
                      <Route path="/admin/users" element={<AdminUsersPage />} />
                      <Route path="/admin/member" element={<AdminMemberPage />} />
                      <Route path="/admin/announcements" element={<AdminAnnouncePage />} />
                      <Route path="/admin/dns" element={<AdminDnsPage />} />
                      <Route path="/admin/general" element={<AdminGeneralPage />} />
                      <Route path="/admin/security" element={<AdminSecurityPage />} />
                      <Route path="/admin/email" element={<AdminEmailPage />} />
                      <Route path="/admin/system" element={<AdminSystemPage />} />
                      {/* 全局层与个人层共用同一份页面组件，只换 scope */}
                      <Route path="/admin/rules" element={<AdminRulesPage />} />
                      <Route path="/admin/subscriptions" element={<AdminSubsPage />} />
                      <Route path="/admin/tls" element={<AdminTls />} />
                      <Route path="/admin/feedback" element={<AdminFeedback />} />
                      <Route path="/admin/logs" element={<AdminLogs />} />
                    </Route>
                  </Route>
                </Route>

                  <Route path="/404" element={<NotFound />} />
                  <Route path="*" element={<NotFound />} />
                </Routes>
                </RouteErrorBoundary>
              </Suspense>
            </AuthProvider>
          </BrowserRouter>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  )
}
