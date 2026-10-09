import axios, { AxiosError, type AxiosInstance, type InternalAxiosRequestConfig } from 'axios'
import { tokenStore } from './tokenStore'
import type { ApiErrorBody, RefreshResponse } from './types'

/** 同源部署：apid 同时托管 SPA 与 /api/v1，无需 CORS。 */
export const API_BASE = '/api/v1'

export interface ApiError {
  status: number
  code: string
  message: string
  details?: Record<string, unknown>
}

/** 把 axios 错误规范化为统一的 ApiError */
export function normalizeError(err: unknown): ApiError {
  if (axios.isAxiosError(err)) {
    const ax = err as AxiosError<ApiErrorBody>
    const body = ax.response?.data
    if (body && typeof body === 'object' && 'error' in body && body.error) {
      return {
        status: ax.response?.status ?? 0,
        code: body.error.code ?? 'INTERNAL_ERROR',
        message: body.error.message ?? ax.message,
        details: body.error.details,
      }
    }
    return {
      status: ax.response?.status ?? 0,
      code: ax.code === 'ECONNABORTED' ? 'TIMEOUT' : 'NETWORK_ERROR',
      message: ax.message || '网络请求失败',
    }
  }
  if (err instanceof Error) {
    return { status: 0, code: 'CLIENT_ERROR', message: err.message }
  }
  return { status: 0, code: 'UNKNOWN', message: '未知错误' }
}

/** 从任意错误中提取可读信息 */
export function errorMessage(err: unknown): string {
  return normalizeError(err).message
}

/** 会话失效时的回调（由 AuthProvider 注入，避免循环依赖） */
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn
}

/** 裸 axios 实例，专用于刷新请求，避免拦截器递归 */
const rawClient = axios.create({ baseURL: API_BASE, timeout: 15000 })

/** 共享刷新 Promise，防止并发 401 触发多次刷新（refresh token 轮转冲突） */
let refreshing: Promise<string> | null = null

async function doRefresh(): Promise<string> {
  const refresh = tokenStore.getRefresh()
  if (!refresh) throw new Error('NO_REFRESH_TOKEN')
  const res = await rawClient.post<RefreshResponse>('/auth/refresh', { refresh_token: refresh })
  tokenStore.set(res.data)
  return res.data.access_token
}

export const api: AxiosInstance = axios.create({
  baseURL: API_BASE,
  timeout: 20000,
  headers: { 'Content-Type': 'application/json' },
})

// 请求拦截：注入 Bearer Token
api.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const token = tokenStore.getAccess()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 响应拦截：401 自动刷新并重放原请求
api.interceptors.response.use(
  (res) => res,
  async (error: AxiosError) => {
    const original = error.config as (InternalAxiosRequestConfig & { _retry?: boolean }) | undefined
    const status = error.response?.status
    const url = original?.url ?? ''

    const isAuthEndpoint =
      url.includes('/auth/login') || url.includes('/auth/register') || url.includes('/auth/refresh')

    if (status === 401 && original && !original._retry && !isAuthEndpoint) {
      original._retry = true
      // 并发请求共享同一个刷新 Promise
      refreshing ??= doRefresh().finally(() => {
        refreshing = null
      })
      try {
        const newToken = await refreshing
        original.headers.Authorization = `Bearer ${newToken}`
        return api(original)
      } catch (refreshErr) {
        tokenStore.clear()
        onUnauthorized?.()
        return Promise.reject(refreshErr)
      }
    }
    return Promise.reject(error)
  },
)
