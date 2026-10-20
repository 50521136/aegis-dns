import { api } from './client'
import { scopePrefix } from './scope'
import type {
  QueryLogResponse,
  Scope,
  StatsSummary,
  TimeseriesResponse,
  TopResponse,
} from './types'

export type StatsRange = '1h' | '24h' | '7d' | '30d'
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
