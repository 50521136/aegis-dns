import { api } from './client'
import type {
  RefreshAllResult,
  Subscription,
  SubscriptionInput,
  SubscriptionPreset,
  SubscriptionRefreshResult,
} from './types'

export async function listSubscriptions(): Promise<Subscription[]> {
  const { data } = await api.get<Subscription[] | { items: Subscription[] }>('/subscriptions')
  return Array.isArray(data) ? data : (data.items ?? [])
}

export async function createSubscription(input: SubscriptionInput): Promise<Subscription> {
  const { data } = await api.post<Subscription>('/subscriptions', input)
  return data
}

export async function updateSubscription(
  id: string,
  input: Partial<SubscriptionInput>,
): Promise<Subscription> {
  const { data } = await api.put<Subscription>(`/subscriptions/${id}`, input)
  return data
}

export async function deleteSubscription(id: string): Promise<void> {
  await api.delete(`/subscriptions/${id}`)
}

export async function refreshSubscription(id: string): Promise<SubscriptionRefreshResult> {
  const { data } = await api.post<SubscriptionRefreshResult>(`/subscriptions/${id}/refresh`)
  return data
}

export async function refreshAllSubscriptions(): Promise<RefreshAllResult> {
  const { data } = await api.post<RefreshAllResult>('/subscriptions/refresh-all')
  return data
}

export async function listPresets(): Promise<SubscriptionPreset[]> {
  const { data } = await api.get<{ presets: SubscriptionPreset[] }>('/subscriptions/presets')
  return data.presets ?? []
}
