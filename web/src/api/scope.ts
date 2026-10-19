import { api } from './client'
import type {
  Feedback,
  FeedbackKind,
  FeedbackListResponse,
  FeedbackStatus,
  Preferences,
  Scope,
  TlsStatus,
} from './types'

// ---------------------------------------------------------------------------
// 作用域前缀
//
// 全局层走 /admin/* 前缀，个人层走根路径。所有「规则/订阅」的读写都通过这里
// 拼路径，页面组件因此可以同一份代码服务两个作用域（见 Rules/Subscriptions 的
// scope 参数），不必复制两套页面。
// ---------------------------------------------------------------------------

/** scopePrefix 返回某个作用域对应的 API 前缀。 */
export function scopePrefix(scope: Scope): string {
  return scope === 'global' ? '/admin' : ''
}

// ---------------------------------------------------------------------------
// 证书管理（仅管理员）
// ---------------------------------------------------------------------------

export async function getTlsStatus(): Promise<TlsStatus> {
  const { data } = await api.get<TlsStatus>('/admin/tls')
  return data
}

export interface TlsUploadResult {
  ok: boolean
  subject: string
  dns_names: string[]
  not_after: number
  days_left: number
  fingerprint: string
  self_signed: boolean
}

export async function uploadTls(certPem: string, keyPem: string): Promise<TlsUploadResult> {
  const { data } = await api.post<TlsUploadResult>('/admin/tls', {
    cert_pem: certPem,
    key_pem: keyPem,
  })
  return data
}

// ---------------------------------------------------------------------------
// 反馈
// ---------------------------------------------------------------------------

export interface FeedbackQuery {
  page?: number
  page_size?: number
  status?: FeedbackStatus | ''
}

export async function createFeedback(input: {
  domain: string
  kind: FeedbackKind
  message: string
}): Promise<Feedback> {
  const { data } = await api.post<Feedback>('/feedback', input)
  return data
}

/** 普通用户看自己的反馈。 */
export async function listMyFeedback(params: FeedbackQuery = {}): Promise<FeedbackListResponse> {
  const { data } = await api.get<FeedbackListResponse>('/feedback', { params })
  return data
}

/** 管理员看全部反馈。 */
export async function listAllFeedback(params: FeedbackQuery = {}): Promise<FeedbackListResponse> {
  const { data } = await api.get<FeedbackListResponse>('/admin/feedback', { params })
  return data
}

/** 管理员回复或结单。 */
export async function replyFeedback(
  id: string,
  input: { status?: FeedbackStatus; reply?: string },
): Promise<Feedback> {
  const { data } = await api.put<Feedback>(`/admin/feedback/${id}`, input)
  return data
}

// ---------------------------------------------------------------------------
// 用户偏好
// ---------------------------------------------------------------------------

export async function getPreferences(): Promise<Preferences> {
  const { data } = await api.get<Preferences>('/me/preferences')
  return data
}

export async function updatePreferences(patch: Partial<Preferences>): Promise<Preferences> {
  const { data } = await api.put<Preferences>('/me/preferences', patch)
  return data
}
