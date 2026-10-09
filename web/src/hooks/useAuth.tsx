import * as React from 'react'
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as authApi from '@/api/auth'
import { getMe } from '@/api/me'
import { setUnauthorizedHandler } from '@/api/client'
import { tokenStore } from '@/api/tokenStore'
import type { AuthResponse, DnsConfig, User } from '@/api/types'

interface AuthContextValue {
  user: User | null
  dnsConfig: DnsConfig | null
  /** 首次加载（尝试用 refresh token 恢复会话）是否仍在进行 */
  initializing: boolean
  isAuthenticated: boolean
  isAdmin: boolean
  login: (username: string, password: string) => Promise<User>
  register: (username: string, email: string, password: string) => Promise<AuthResponse>
  logout: () => Promise<void>
  refreshUser: () => Promise<User | null>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [dnsConfig, setDnsConfig] = useState<DnsConfig | null>(null)
  const [initializing, setInitializing] = useState(true)
  const queryClient = useQueryClient()

  const clearSession = useCallback(() => {
    tokenStore.clear()
    setUser(null)
    setDnsConfig(null)
  }, [])

  // 会话失效（refresh 失败）时统一清理
  useEffect(() => {
    setUnauthorizedHandler(() => {
      clearSession()
      queryClient.clear()
    })
    return () => setUnauthorizedHandler(null)
  }, [clearSession, queryClient])

  const loadMe = useCallback(async (): Promise<User | null> => {
    try {
      const me = await getMe()
      setUser(me)
      return me
    } catch {
      return null
    }
  }, [])

  // 启动时尝试恢复会话
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      if (tokenStore.hasSession()) {
        try {
          await authApi.refresh()
          if (!cancelled) await loadMe()
        } catch {
          if (!cancelled) clearSession()
        }
      }
      if (!cancelled) setInitializing(false)
    })()
    return () => {
      cancelled = true
    }
  }, [loadMe, clearSession])

  const login = useCallback(
    async (username: string, password: string) => {
      const res = await authApi.login({ username, password })
      setUser(res.user)
      setDnsConfig(res.dns_config ?? null)
      queryClient.clear()
      return res.user
    },
    [queryClient],
  )

  const register = useCallback(
    async (username: string, email: string, password: string) => {
      const res = await authApi.register({ username, email, password })
      setUser(res.user)
      setDnsConfig(res.dns_config ?? null)
      queryClient.clear()
      return res
    },
    [queryClient],
  )

  const logout = useCallback(async () => {
    await authApi.logout()
    clearSession()
    queryClient.clear()
  }, [clearSession, queryClient])

  const value = useMemo<AuthContextValue>(
    () => ({
      user,
      dnsConfig,
      initializing,
      isAuthenticated: Boolean(user),
      isAdmin: user?.role === 'admin',
      login,
      register,
      logout,
      refreshUser: loadMe,
    }),
    [user, dnsConfig, initializing, login, register, logout, loadMe],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 <AuthProvider> 内部使用')
  return ctx
}
