import { useEffect, useRef } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/hooks/useTheme'
import { AuthProvider } from '@/hooks/useAuth'
import { ToastProvider, useToast } from '@/components/ui/toast'
import { PublicLayout } from '@/components/layout/PublicLayout'
import { AppLayout } from '@/components/layout/AppLayout'
import { ProtectedRoute, AdminRoute, UserOnlyRoute } from '@/components/ProtectedRoute'
import { getVersion } from '@/api/system'

import Landing from '@/pages/Landing'
import Login from '@/pages/Login'
import Register from '@/pages/Register'
import ForgotPassword from '@/pages/ForgotPassword'
import Dashboard from '@/pages/Dashboard'
import DnsSetup from '@/pages/DnsSetup'
import Rules from '@/pages/Rules'
import Subscriptions from '@/pages/Subscriptions'
import QueryLog from '@/pages/QueryLog'
import Settings from '@/pages/Settings'
import Admin from '@/pages/Admin'
import AdminLogs from '@/pages/AdminLogs'
import AdminFeedback from '@/pages/AdminFeedback'
import AdminTls from '@/pages/AdminTls'
import Feedback from '@/pages/Feedback'
import NotFound from '@/pages/NotFound'

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
                      <Route path="/admin/manage" element={<Admin />} />
                      {/* 全局层与个人层共用同一份页面组件，只换 scope */}
                      <Route path="/admin/rules" element={<Rules scope="global" />} />
                      <Route path="/admin/subscriptions" element={<Subscriptions scope="global" />} />
                      <Route path="/admin/tls" element={<AdminTls />} />
                      <Route path="/admin/feedback" element={<AdminFeedback />} />
                      <Route path="/admin/logs" element={<AdminLogs />} />
                    </Route>
                  </Route>
                </Route>

                <Route path="/404" element={<NotFound />} />
                <Route path="*" element={<NotFound />} />
              </Routes>
            </AuthProvider>
          </BrowserRouter>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  )
}
