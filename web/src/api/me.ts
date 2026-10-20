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

/**
 * 下载 iOS / macOS 的加密 DNS 描述文件。
 *
 * 不能直接用 <a href> 指向接口：该路由要 Bearer 鉴权，浏览器直接跳转会拿到 401。
 * 所以走 axios 取 blob，再用临时 object URL 触发下载。
 */
export async function downloadMobileConfig(clientID: string): Promise<void> {
  const { data } = await api.get('/me/mobileconfig', { responseType: 'blob' })
  const url = URL.createObjectURL(data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `dnsforge-${clientID}.mobileconfig`
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻 revoke 在部分浏览器上会打断下载，延后释放
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
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
