import { api } from './client'
import type {
  CaptchaResult,
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
  /** 开启人机验证时必填 */
  captcha?: CaptchaResult
  domain: string
  kind: FeedbackKind
  message: string
}): Promise<Feedback> {
  const { data } = await api.post<Feedback>('/feedback', input)
  return data
}

/** 普通用户看自己的反馈。 */
/**
 * 用户在自己的反馈下追问。
 *
 * 走这个接口而不是再 POST /feedback —— 后者会在列表里长出一条新记录，
 * 同一个问题散成好几条，管理员结单时还得挨个翻。
 */
export async function replyMyFeedback(id: string, message: string): Promise<Feedback> {
  const { data } = await api.post<Feedback>(`/feedback/${id}/reply`, { message })
  return data
}

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

// ---------------------------------------------------------------------------
// AI 诊断（管理员）
// ---------------------------------------------------------------------------

import type { AISettings, AITestResult } from './types'

/**
 * 触发一次 AI 诊断。
 *
 * 立即返回 202，模型结果异步写入 feedback 的 ai_* 字段，
 * 前端靠轮询列表刷新，不要在此同步等待。
 */
export async function diagnoseFeedback(id: string): Promise<{ id: string; ai_status: string }> {
  const { data } = await api.post<{ id: string; ai_status: string }>(`/admin/feedback/${id}/diagnose`)
  return data
}

/**
 * 采纳诊断建议：把建议的域名写成放行规则。
 *
 * 这是唯一会真正改动规则的接口，服务端会重新做一次安全校验，
 * 所以库里存的建议被改脏也拦得住。
 */
export async function applyDiagnosis(
  id: string,
): Promise<{ applied: string[]; rule_count: number; global: boolean }> {
  const { data } = await api.post<{ applied: string[]; rule_count: number; global: boolean }>(
    `/admin/feedback/${id}/apply`,
  )
  return data
}

export async function getAISettings(): Promise<AISettings> {
  const { data } = await api.get<AISettings>('/admin/ai/settings')
  return data
}

export async function updateAISettings(patch: Partial<AISettings> & { ai_key?: string; clear_key?: boolean }): Promise<void> {
  await api.put('/admin/ai/settings', patch)
}

/** 用一条假数据跑一次模型，验证地址/Key/模型名是否都对。 */
export async function testAI(): Promise<AITestResult> {
  const { data } = await api.post<AITestResult>('/admin/ai/test')
  return data
}

// ---------------------------------------------------------------------------
// DNS 监听端口（管理员）
// ---------------------------------------------------------------------------

export interface DNSPortsInfo {
  listen_udp: string
  listen_tcp: string
  listen_dot: string
  listen_doh: string
  listen_doh_plain: string
  doh_port: number
  dot_port: number
  panel_listen: string
  panel_tls_listen: string
  in_use_by_dnsd: Record<string, boolean>
  in_use_by_panel: Record<string, boolean>
}

export interface DNSPortCheck {
  port: number
  ok: boolean
  current?: boolean
  reason: string
}

export async function getDNSPorts(): Promise<DNSPortsInfo> {
  const { data } = await api.get<DNSPortsInfo>('/admin/dns/ports')
  return data
}

/** 只探测、不改配置：输入框失焦或停顿后调用。 */
export async function checkDNSPort(port: number): Promise<DNSPortCheck> {
  const { data } = await api.post<DNSPortCheck>('/admin/dns/ports/check', { port })
  return data
}

/** 写入并重启 dnsd；失败时服务端会自动回滚并返回说明。 */
export async function updateDNSPort(dohPort: number): Promise<{ ok: boolean; port: number; message: string }> {
  const { data } = await api.put<{ ok: boolean; port: number; message: string }>('/admin/dns/ports', {
    doh_port: dohPort,
  })
  return data
}
