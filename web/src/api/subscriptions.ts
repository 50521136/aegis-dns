import { api } from './client'
import { scopePrefix } from './scope'
import type {
  RefreshAllResult,
  Scope,
  Subscription,
  SubscriptionInput,
  SubscriptionPreset,
  SubscriptionRefreshResult,
} from './types'

// scope 决定走 /subscriptions 还是 /admin/subscriptions（全局层）。
export async function listSubscriptions(scope: Scope = 'user'): Promise<Subscription[]> {
  const { data } = await api.get<Subscription[] | { items: Subscription[] }>(
    `${scopePrefix(scope)}/subscriptions`,
  )
  return Array.isArray(data) ? data : (data.items ?? [])
}

export async function createSubscription(
  input: SubscriptionInput,
  scope: Scope = 'user',
): Promise<Subscription> {
  const { data } = await api.post<Subscription>(`${scopePrefix(scope)}/subscriptions`, input)
  return data
}

export async function updateSubscription(
  id: string,
  input: Partial<SubscriptionInput>,
  scope: Scope = 'user',
): Promise<Subscription> {
  const { data } = await api.put<Subscription>(`${scopePrefix(scope)}/subscriptions/${id}`, input)
  return data
}

export async function deleteSubscription(id: string, scope: Scope = 'user'): Promise<void> {
  await api.delete(`${scopePrefix(scope)}/subscriptions/${id}`)
}

export async function refreshSubscription(
  id: string,
  scope: Scope = 'user',
): Promise<SubscriptionRefreshResult> {
  const { data } = await api.post<SubscriptionRefreshResult>(
    `${scopePrefix(scope)}/subscriptions/${id}/refresh`,
  )
  return data
}

export async function refreshAllSubscriptions(scope: Scope = 'user'): Promise<RefreshAllResult> {
  const { data } = await api.post<RefreshAllResult>(
    `${scopePrefix(scope)}/subscriptions/refresh-all`,
  )
  return data
}

// 预置订阅是公共数据，与作用域无关。
export async function listPresets(): Promise<SubscriptionPreset[]> {
  const { data } = await api.get<{ presets: SubscriptionPreset[] }>('/subscriptions/presets')
  return data.presets ?? []
}
