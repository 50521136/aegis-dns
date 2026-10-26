import { api } from './client'
import { tokenStore } from './tokenStore'
import type { AuthResponse, CaptchaResult, RefreshResponse } from './types'

export interface RegisterPayload {
  username: string
  email: string
  password: string
  /** 需要邀请码时必填 */
  invite_code?: string
  /** 开启注册邮箱验证时必填 */
  email_code?: string
  /** 开启人机验证时必填 */
  captcha?: CaptchaResult
}

export interface LoginPayload {
  username: string
  password: string
  /** 开启人机验证时必填 */
  captcha?: CaptchaResult
}

export async function register(payload: RegisterPayload): Promise<AuthResponse> {
  const { data } = await api.post<AuthResponse>('/auth/register', payload)
  tokenStore.set(data.tokens)
  return data
}

export async function login(payload: LoginPayload): Promise<AuthResponse> {
  const { data } = await api.post<AuthResponse>('/auth/login', payload)
  tokenStore.set(data.tokens)
  return data
}

export async function refresh(): Promise<RefreshResponse> {
  const refreshToken = tokenStore.getRefresh()
  if (!refreshToken) throw new Error('NO_REFRESH_TOKEN')
  const { data } = await api.post<RefreshResponse>('/auth/refresh', { refresh_token: refreshToken })
  tokenStore.set(data)
  return data
}

export async function logout(): Promise<void> {
  const refreshToken = tokenStore.getRefresh()
  try {
    if (refreshToken) {
      await api.post('/auth/logout', { refresh_token: refreshToken })
    }
  } finally {
    tokenStore.clear()
  }
}
