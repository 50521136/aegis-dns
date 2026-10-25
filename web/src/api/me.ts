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
 * 全局层（管理员内置的规则与订阅）对当前用户的生效情况与规模。
 *
 * 全局层在用户自己的规则/订阅列表里看不到，用户容易以为「没有任何规则在生效」
 * 而自己再挂一份大订阅 —— 订阅条目按用户压平进快照，重复挂既占用户配额
 * 也占服务器内存。这个接口给前端提供「已内置多少」的真实数字。
 */
export interface GlobalLayerInfo {
  /** 用户是否开启了全局层叠加 */
  enabled: boolean
  /** 启用的全局规则条数（不含订阅） */
  rule_count: number
  /** 启用的全局订阅份数 */
  sub_count: number
  /** 全局订阅展开后的域名条目总数 */
  sub_entry_count: number
  /** enabled 且确实有内容时为 true —— 前端据此决定要不要提示 */
  has_rules: boolean
}

export async function getGlobalLayer(): Promise<GlobalLayerInfo> {
  const { data } = await api.get<GlobalLayerInfo>('/me/global-layer')
  return data
}

/**
 * 下载 iOS / macOS 的加密 DNS 描述文件。
 *
 * 不能直接用 <a href> 指向接口：该路由要 Bearer 鉴权，浏览器直接跳转会拿到 401。
 * 所以走 axios 取 blob，再用临时 object URL 触发下载。
 */
/** 自助重置 client_id 的结果：新地址 + 剩余次数 */
export interface ResetClientIDResult {
  client_id: string
  old_client_id: string
  used: number
  limit: number
  remaining: number
  dns_config: DnsConfig
}

/**
 * 重置自己的 client_id（接入地址）。
 *
 * 旧子域会立刻失效，所以这个操作是有次数上限的，用完只能找管理员。
 */
export async function resetMyClientID(): Promise<ResetClientIDResult> {
  const { data } = await api.post<ResetClientIDResult>('/me/reset-client-id')
  return data
}

/**
 * 自定义接入 ID。
 *
 * 每个用户只有一次机会，后端会在已用过时返回 403 CUSTOM_ID_USED。
 * 成功后返回新的 DNS 接入配置，旧子域名立刻失效。
 */
export async function setMyClientID(clientID: string): Promise<ResetClientIDResult> {
  const { data } = await api.post<ResetClientIDResult>('/me/client-id', { client_id: clientID })
  return data
}

export async function downloadMobileConfig(
  clientID: string,
  proto: 'doh' | 'dot' = 'doh',
): Promise<void> {
  // proto 决定描述文件里写 DNSProtocol=HTTPS 还是 TLS。
  // 后端会校验该协议是否开启，关着就返回 409。
  const { data } = await api.get('/me/mobileconfig', { responseType: 'blob', params: { proto } })
  const url = URL.createObjectURL(data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `dnsforge-${clientID}-${proto}.mobileconfig`
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
