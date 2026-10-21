import { api } from './client'
import { scopePrefix } from './scope'
import type {
  QueryLogResponse,
  Scope,
  StatsSummary,
  TimeseriesResponse,
  TopResponse,
} from './types'

export type StatsRange = 'today' | '1h' | '24h' | '7d' | '30d'
export type TopType = 'all' | 'blocked'

// 统计接口的用户版与管理版返回结构一致，只是作用域不同：
// /stats/* 只看自己，/admin/stats/* 看全站。用同一个 scope 参数区分，
// 和规则/订阅页的做法保持一致。

export async function getSummary(range: StatsRange = '24h', scope: Scope = 'user'): Promise<StatsSummary> {
  const { data } = await api.get<StatsSummary>(`${scopePrefix(scope)}/stats/summary`, { params: { range } })
  return data
}

export async function getTimeseries(
  range: StatsRange = '7d',
  interval = '1h',
  scope: Scope = 'user',
): Promise<TimeseriesResponse> {
  const { data } = await api.get<TimeseriesResponse>(`${scopePrefix(scope)}/stats/timeseries`, {
    params: { range, interval },
  })
  return data
}

export async function getTop(
  range: StatsRange = '24h',
  limit = 10,
  type: TopType = 'all',
  scope: Scope = 'user',
): Promise<TopResponse> {
  const { data } = await api.get<TopResponse>(`${scopePrefix(scope)}/stats/top`, {
    params: { range, limit, type },
  })
  return data
}

export interface QueryLogParams {
  limit?: number
  offset?: number
  domain?: string
  action?: string
}

export async function getQueryLog(params: QueryLogParams = {}): Promise<QueryLogResponse> {
  const { data } = await api.get<QueryLogResponse>('/stats/querylog', { params })
  return data
}

/** 首页公开统计（无需登录，只含聚合数字） */
export interface PublicStats {
  available: boolean
  window: string
  total?: number
  blocked?: number
  allowed?: number
  block_rate?: number
  cache_hit_rate?: number
  unique_domains?: number
}

export async function getPublicStats(): Promise<PublicStats> {
  const { data } = await api.get<PublicStats>('/public/stats')
  return data
}

/**
 * 清除解析缓存。
 *
 * 缓存住在 dnsd 进程里，apid 只能下发指令 —— 所以这里是「已下发」而不是「已清空」，
 * dnsd 下次轮询（通常 30 秒内）才真正清。UI 文案要说清楚这一点。
 */
export async function flushCache(): Promise<{ ok: boolean; message: string }> {
  const { data } = await api.post('/admin/cache/flush')
  return data as { ok: boolean; message: string }
}
