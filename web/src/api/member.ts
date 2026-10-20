import { api } from './client'
import type { Announcement, AnnouncementList, InviteCode, MyMembership } from './types'

// === 会员 ===

/** 我的会员档位、配额与邀请战绩 */
export async function getMyMembership(): Promise<MyMembership> {
  const { data } = await api.get<MyMembership>('/me/membership')
  return data
}

/** 管理员调整某个用户的会员天数（负数表示扣减） */
export async function setUserMember(userId: string, days: number) {
  const { data } = await api.post(`/admin/users/${userId}/member`, { days })
  return data as { member_until: number; tier: string; member_days: number }
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
export async function sendEmailCode(email: string, purpose: 'register' | 'reset') {
  const { data } = await api.post('/auth/email/send-code', { email, purpose })
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
