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
  patch: { enabled?: boolean; role?: 'admin' | 'user' },
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
