import { api } from './client'
import type { TierDef } from './types'

/** 列出全部会员种类 */
export async function listTiers(): Promise<TierDef[]> {
  const { data } = await api.get<{ items: TierDef[] }>('/admin/tiers')
  return data.items ?? []
}

/** 新建或更新一个会员种类（按 id upsert） */
export async function saveTier(t: TierDef): Promise<TierDef> {
  const { data } = await api.post<TierDef>('/admin/tiers', t)
  return data
}

/** 删除会员种类。内置 free/vip 与仍有用户在用的档位会被拒绝。 */
export async function deleteTier(id: string): Promise<void> {
  await api.delete(`/admin/tiers/${id}`)
}

/** 给用户指定档位，传空串 = 按会员状态自动判定 */
export async function setUserTier(userId: string, tierId: string): Promise<void> {
  await api.put(`/admin/users/${userId}/tier`, { tier_id: tierId })
}
