import { api } from './client'
import type {
  Announcement,
  AnnouncementList,
  CaptchaConfig,
  CaptchaResult,
  InviteCode,
  MyMembership,
} from './types'

// === 会员 ===

/** 我的会员档位、配额与邀请战绩 */
export async function getMyMembership(): Promise<MyMembership> {
  const { data } = await api.get<MyMembership>('/me/membership')
  return data
}

/** 管理员调整某个用户的会员（天数可负 = 扣减；可同时指定会员种类） */
export async function setUserMember(
  userId: string,
  days: number,
  opts?: { tier_id?: string; permanent?: boolean },
) {
  const { data } = await api.post(`/admin/users/${userId}/member`, {
    days,
    tier_id: opts?.tier_id ?? '',
    permanent: opts?.permanent ?? false,
  })
  return data as {
    member_until: number
    tier: string
    tier_name: string
    member_forever: boolean
    member_days: number
  }
}

/** 管理员直接取消会员 */
export async function revokeUserMember(userId: string) {
  const { data } = await api.post(`/admin/users/${userId}/member/revoke`)
  return data as { member_until: number; tier: string }
}

// === 邀请码 ===

export async function listInviteCodes(): Promise<InviteCode[]> {
  const { data } = await api.get<{ items: InviteCode[] }>('/admin/invite-codes')
  return data.items ?? []
}

export interface InviteCodeInput {
  code?: string
  note?: string
  reward_days?: number
  /** 核销后发给邀请人的会员种类；不传 = 用全局设置 */
  reward_tier?: string
  max_uses?: number
  expires_at?: number
  enabled?: boolean
}

export async function createInviteCode(input: InviteCodeInput): Promise<InviteCode> {
  const { data } = await api.post<InviteCode>('/admin/invite-codes', input)
  return data
}

export async function updateInviteCode(code: string, input: InviteCodeInput): Promise<InviteCode> {
  const { data } = await api.patch<InviteCode>(`/admin/invite-codes/${code}`, input)
  return data
}

export async function deleteInviteCode(code: string): Promise<void> {
  await api.delete(`/admin/invite-codes/${code}`)
}

// === 公告 ===

export async function getMyAnnouncements(): Promise<AnnouncementList> {
  const { data } = await api.get<AnnouncementList>('/me/announcements')
  return data
}

export async function readAnnouncement(id: number): Promise<void> {
  await api.post(`/me/announcements/${id}/read`)
}

export async function dismissAnnouncement(id: number): Promise<void> {
  await api.post(`/me/announcements/${id}/dismiss`)
}

export async function listAnnouncements(): Promise<Announcement[]> {
  const { data } = await api.get<{ items: Announcement[] }>('/admin/announcements')
  return data.items ?? []
}

export interface AnnouncementInput {
  title: string
  body: string
  level: Announcement['level']
  mode: Announcement['mode']
  dismissible: boolean
  enabled: boolean
  pinned: boolean
  starts_at: number
  ends_at: number
}

export async function createAnnouncement(input: AnnouncementInput): Promise<Announcement> {
  const { data } = await api.post<Announcement>('/admin/announcements', input)
  return data
}

export async function updateAnnouncement(id: number, input: Partial<AnnouncementInput>): Promise<Announcement> {
  const { data } = await api.patch<Announcement>(`/admin/announcements/${id}`, input)
  return data
}

export async function deleteAnnouncement(id: number): Promise<void> {
  await api.delete(`/admin/announcements/${id}`)
}

// === 邮件 ===

/** 发送邮箱验证码（注册验证 / 找回密码） */
export async function sendEmailCode(
  email: string,
  purpose: 'register' | 'reset' | 'change',
  captcha?: CaptchaResult,
) {
  // 换邮箱必须走带登录态的接口：/auth/email/send-code 是公开路由，
  // 服务端拿不到 currentUser，会一律回「请先登录」。
  const url = purpose === 'change' ? '/me/email/send-code' : '/auth/email/send-code'
  const { data } = await api.post(url, { email, purpose, captcha })
  return data as { sent: boolean; message: string }
}

/** 用验证码重置密码 */
export async function forgotPassword(email: string, code: string, newPassword: string) {
  const { data } = await api.post('/auth/forgot-password', {
    email,
    code,
    new_password: newPassword,
  })
  return data as { ok: boolean }
}

/** 管理员发送测试邮件 */
export async function testEmail(to: string) {
  const { data } = await api.post('/admin/settings/test-email', { to })
  return data as { ok: boolean; message: string }
}

/** 登录/注册页需要的公开配置 */
export interface PublicConfig {
  allow_register: boolean
  require_invite: boolean
  require_email_verify: boolean
  email_available: boolean
  domain: string
  allow_user_invite: boolean
  invite_reward_days: number
  signup_member_days: number
  allow_email_change: boolean
  /** 后台允许的邮箱后缀（空数组 = 不限制）。注册页据此把后缀锁成下拉。 */
  email_domains: string[]
  /**
   * 极验验证配置。登录/注册/找回密码页据此决定要不要初始化验证组件。
   * 并进这个接口而不是单开一个，省掉首屏一次额外往返。
   */
  captcha?: CaptchaConfig
  /**
   * 用户协议。登录/注册页据此决定要不要渲染勾选框、要不要弹窗。
   *
   * terms_enabled=false 时前端完全不渲染这一块，老站点行为不变。
   */
  terms_enabled: boolean
  terms_title: string
  terms_version: string
  terms_content: string
}

export async function getPublicConfig(): Promise<PublicConfig> {
  const { data } = await api.get<PublicConfig>('/public/config')
  return data
}

// === 用户自建邀请码 ===

export interface MyInviteCodes {
  items: (InviteCode & { link?: string })[]
  total: number
  can_create: boolean
  enabled: boolean
  max_codes: number
  reward_days: number
  /** 邀请奖励的会员种类显示名（空串 = 默认会员档） */
  reward_tier_name?: string
  invited: number
  earned_days: number
  member_until: number
  member_days: number
}

export async function getMyInviteCodes(): Promise<MyInviteCodes> {
  const { data } = await api.get<MyInviteCodes>('/me/invite-codes')
  return data
}

export async function createMyInviteCode(): Promise<InviteCode & { link: string }> {
  const { data } = await api.post('/me/invite-codes')
  return data
}

export async function deleteMyInviteCode(code: string): Promise<void> {
  await api.delete(`/me/invite-codes/${code}`)
}

// === 每日签到 ===

export interface CheckinState {
  enabled: boolean
  daily_days: number
  /** 签到发放的会员种类 id（默认会员档） */
  daily_tier?: string
  streak_days: number
  streak_bonus: number
  checked_today: boolean
  streak: number
  total_days: number
  granted_today: number
  member_until: number
  member_days: number
  /** 会员状态（后端算好，前端不自己判断到期） */
  member_forever?: boolean
  is_member?: boolean
  tier?: string
  tier_name?: string
}

export async function getCheckin(): Promise<CheckinState> {
  const { data } = await api.get<CheckinState>('/me/checkin')
  return data
}

export interface CheckinResult {
  granted: number
  bonus: number
  streak: number
  total_days: number
  checked_today: boolean
  member_until: number
  member_days: number
  bonus_message?: string
}

export async function doCheckin(): Promise<CheckinResult> {
  const { data } = await api.post<CheckinResult>('/me/checkin')
  return data
}

// === 更换邮箱 ===

export async function changeEmail(email: string, code: string) {
  const { data } = await api.post('/me/email', { email, code })
  return data as { ok: boolean; email: string }
}
