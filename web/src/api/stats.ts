import { api } from './client'
import type {
  QueryLogResponse,
  StatsSummary,
  TimeseriesResponse,
  TopResponse,
} from './types'

export type StatsRange = '1h' | '24h' | '7d' | '30d'
export type TopType = 'all' | 'blocked'

export async function getSummary(range: StatsRange = '24h'): Promise<StatsSummary> {
  const { data } = await api.get<StatsSummary>('/stats/summary', { params: { range } })
  return data
}

export async function getTimeseries(
  range: StatsRange = '7d',
  interval = '1h',
): Promise<TimeseriesResponse> {
  const { data } = await api.get<TimeseriesResponse>('/stats/timeseries', {
    params: { range, interval },
  })
  return data
}

export async function getTop(
  range: StatsRange = '24h',
  limit = 10,
  type: TopType = 'all',
): Promise<TopResponse> {
  const { data } = await api.get<TopResponse>('/stats/top', { params: { range, limit, type } })
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
