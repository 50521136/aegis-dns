import { api } from './client'
import type { AdminOwnedRule, AdminOwnedSub } from './types'

export interface Paged<T> {
  items: T[]
  total: number
  page: number
  size: number
}

/**
 * 跨租户列出用户自己加的规则。
 *
 * 管理员的 /admin/rules 只有全局层，用户侧的规则在后台一直看不到 ——
 * 用户说「某个域名被拦了」，管理员翻不到那条规则，只能让他自己截图。
 */
export async function listAdminUserRules(params: {
  page?: number
  page_size?: number
  user_id?: string
  q?: string
  kind?: string
}): Promise<Paged<AdminOwnedRule>> {
  const { data } = await api.get<Paged<AdminOwnedRule>>('/admin/user-rules', { params })
  return data
}

/** 删除某个用户的一条规则（运维出口，会落审计） */
export async function deleteAdminUserRule(id: string): Promise<void> {
  await api.delete(`/admin/user-rules/${id}`)
}

/** 跨租户列出用户自己加的订阅 */
export async function listAdminUserSubs(params: {
  page?: number
  page_size?: number
  user_id?: string
  q?: string
}): Promise<Paged<AdminOwnedSub>> {
  const { data } = await api.get<Paged<AdminOwnedSub>>('/admin/user-subs', { params })
  return data
}
