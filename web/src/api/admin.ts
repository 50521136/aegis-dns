import { api } from './client'
import type {
  AdminSettings,
  AdminUserListResponse,
  SystemInfo,
  UpdateApplyResult,
  UpdateInfo,
  User,
} from './types'

export interface AdminUserQuery {
  page?: number
  page_size?: number
  q?: string
}

export async function listUsers(params: AdminUserQuery = {}): Promise<AdminUserListResponse> {
  const { data } = await api.get<AdminUserListResponse>('/admin/users', { params })
  return data
}

export async function updateUser(
  id: string,
  patch: {
    enabled?: boolean
    role?: 'admin' | 'user'
    rules_enabled?: boolean
    subs_enabled?: boolean
  },
): Promise<User> {
  const { data } = await api.put<User>(`/admin/users/${id}`, patch)
  return data
}

export async function deleteUser(id: string): Promise<void> {
  await api.delete(`/admin/users/${id}`)
}

export async function getSettings(): Promise<AdminSettings> {
  const { data } = await api.get<AdminSettings>('/admin/settings')
  return data
}

export async function updateSettings(patch: Partial<AdminSettings>): Promise<AdminSettings> {
  const { data } = await api.put<AdminSettings>('/admin/settings', patch)
  return data
}

export async function rebuildSnapshot(): Promise<{ version: number }> {
  const { data } = await api.post<{ version: number }>('/admin/snapshot/rebuild')
  return data
}

export async function getSystem(): Promise<SystemInfo> {
  const { data } = await api.get<SystemInfo>('/admin/system')
  return data
}

export async function checkUpdate(): Promise<UpdateInfo> {
  const { data } = await api.get<UpdateInfo>('/update/check')
  return data
}

export async function applyUpdate(
  component: 'apid' | 'dnsd' | 'all',
): Promise<UpdateApplyResult> {
  const { data } = await api.post<UpdateApplyResult>('/admin/update/apply', { component })
  return data
}

/* ---------------- 用户批量与运维 ---------------- */

export interface BulkDeleteResult {
  deleted: number
  skipped: number
}

export async function bulkDeleteUsers(ids: string[]): Promise<BulkDeleteResult> {
  const { data } = await api.post<BulkDeleteResult>('/admin/users/bulk-delete', { ids })
  return data
}

export async function resetUserClientID(
  id: string,
): Promise<{ id: string; username: string; client_id: string; old_client_id: string }> {
  const { data } = await api.post(`/admin/users/${id}/reset-client-id`)
  return data
}

/**
 * 管理员直接指定某个用户的接入 ID。
 *
 * 不消耗该用户「自定义一次」的机会 —— 用户填错字或机会用完之后想再改都靠这里。
 */
export async function setUserClientID(id: string, clientID: string): Promise<{ client_id: string }> {
  const { data } = await api.post(`/admin/users/${id}/client-id`, { client_id: clientID })
  return data
}

export async function setUserPassword(id: string, password: string): Promise<void> {
  await api.post(`/admin/users/${id}/password`, { password })
}

export interface UserUsage {
  user_id: string
  rules: number
  subs: number
}

export interface UsersUsageResponse {
  items: UserUsage[]
  limits: { max_rules_per_user: number; max_subs_per_user: number }
}

export async function getUsersUsage(): Promise<UsersUsageResponse> {
  const { data } = await api.get<UsersUsageResponse>('/admin/users/usage')
  return data
}

export interface CleanupResult {
  deleted: Record<string, number>
  total: number
  stats_retention_days: number
  log_retention_days: number
  duration_ms: number
}

export async function runCleanup(): Promise<CleanupResult> {
  const { data } = await api.post<CleanupResult>('/admin/maintenance/cleanup', {})
  return data
}
