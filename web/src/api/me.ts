import { api } from './client'
import type { ApiToken, ApiTokenCreated, DnsConfig, ExportPayload, User } from './types'

export async function getMe(): Promise<User> {
  const { data } = await api.get<User>('/me')
  return data
}

export async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  await api.put('/me/password', { old_password: oldPassword, new_password: newPassword })
}

export async function getDnsConfig(): Promise<DnsConfig> {
  const { data } = await api.get<DnsConfig>('/me/dns-config')
  return data
}

export async function exportConfig(): Promise<ExportPayload> {
  const { data } = await api.get<ExportPayload>('/me/export')
  return data
}

export async function importConfig(payload: ExportPayload): Promise<void> {
  await api.post('/me/import', payload)
}

export async function listTokens(): Promise<ApiToken[]> {
  const { data } = await api.get<ApiToken[] | { items: ApiToken[] }>('/me/tokens')
  return Array.isArray(data) ? data : (data.items ?? [])
}

export async function createToken(name: string): Promise<ApiTokenCreated> {
  const { data } = await api.post<ApiTokenCreated>('/me/tokens', { name })
  return data
}

export async function deleteToken(id: string): Promise<void> {
  await api.delete(`/me/tokens/${id}`)
}
