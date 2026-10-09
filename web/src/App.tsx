import { useEffect, useRef } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/hooks/useTheme'
import { AuthProvider } from '@/hooks/useAuth'
import { ToastProvider, useToast } from '@/components/ui/toast'
import { PublicLayout } from '@/components/layout/PublicLayout'
import { AppLayout } from '@/components/layout/AppLayout'
import { ProtectedRoute, AdminRoute } from '@/components/ProtectedRoute'
import { getVersion } from '@/api/system'

import Landing from '@/pages/Landing'
import Login from '@/pages/Login'
import Register from '@/pages/Register'
import Dashboard from '@/pages/Dashboard'
import DnsSetup from '@/pages/DnsSetup'
import Rules from '@/pages/Rules'
import Subscriptions from '@/pages/Subscriptions'
import Stats from '@/pages/Stats'
import QueryLog from '@/pages/QueryLog'
import Settings from '@/pages/Settings'
import Admin from '@/pages/Admin'
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
                </Route>

                {/* 需要登录 */}
                <Route element={<ProtectedRoute />}>
                  <Route element={<AppLayout />}>
                    <Route path="/app" element={<Dashboard />} />
                    <Route path="/app/setup" element={<DnsSetup />} />
                    <Route path="/app/rules" element={<Rules />} />
                    <Route path="/app/subscriptions" element={<Subscriptions />} />
                    <Route path="/app/stats" element={<Stats />} />
                    <Route path="/app/logs" element={<QueryLog />} />
                    <Route path="/app/settings" element={<Settings />} />

                    {/* 仅管理员 */}
                    <Route element={<AdminRoute />}>
                      <Route path="/admin" element={<Admin />} />
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
