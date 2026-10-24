import { api } from './client'
import type { QueryLogItem } from './types'

/* ---------------- 管理员的全局统计与日志 ---------------- */

/** 按用户汇总的请求量，用于排行榜 */
export interface UserTrafficRow {
  user_id: string
  username: string
  total: number
  blocked: number
  allowed: number
  cached: number
  block_rate: number
  /** 识别不出租户的汇总桶（扫描器/未绑定 IP 的设备），不是真实用户 */
  anonymous?: boolean
}

export interface UserTrafficResponse {
  range: string
  items: UserTrafficRow[]
  total_users: number
}

// today 是北京时间的「今天 0 点起」，不是滚动 24 小时。
// 后端 timeRange() 一直支持，只是前端类型里漏了，导致选「今日」时
// 只能退化成 24h。
export type AdminStatsRange = 'today' | '1h' | '24h' | '7d' | '30d'

export async function getAdminUserTraffic(
  range: AdminStatsRange = '24h',
  limit = 20,
): Promise<UserTrafficResponse> {
  const { data } = await api.get<UserTrafficResponse>('/admin/stats/users', {
    params: { range, limit },
  })
  return data
}

/** 全局日志条目：比用户版多 user_id / username */
export interface AdminQueryLogItem extends QueryLogItem {
  user_id: string
  username?: string
}

export interface AdminQueryLogResponse {
  items: AdminQueryLogItem[]
  total: number
  limit: number
  offset: number
}

export interface AdminQueryLogParams {
  limit?: number
  offset?: number
  /** 留空表示看全部用户 */
  user_id?: string
  domain?: string
  action?: string
}

export async function getAdminQueryLog(
  params: AdminQueryLogParams = {},
): Promise<AdminQueryLogResponse> {
  const { data } = await api.get<AdminQueryLogResponse>('/admin/stats/querylog', { params })
  return data
}
