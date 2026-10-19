import { api } from './client'
import { scopePrefix } from './scope'
import type {
  Rule,
  RuleImportResponse,
  RuleInput,
  RuleListResponse,
  RuleValidateResponse,
  Scope,
} from './types'

export interface RuleQuery {
  page?: number
  page_size?: number
  q?: string
  kind?: string
  enabled?: boolean
}

// 所有函数都带 scope：'user' 走 /rules，'global' 走 /admin/rules。
// 页面组件只传 scope，不用知道路径怎么拼。

export async function listRules(
  params: RuleQuery = {},
  scope: Scope = 'user',
): Promise<RuleListResponse> {
  const { data } = await api.get<RuleListResponse>(`${scopePrefix(scope)}/rules`, { params })
  return data
}

export async function createRule(input: RuleInput, scope: Scope = 'user'): Promise<Rule> {
  const { data } = await api.post<Rule>(`${scopePrefix(scope)}/rules`, input)
  return data
}

export async function updateRule(
  id: string,
  input: Partial<RuleInput>,
  scope: Scope = 'user',
): Promise<Rule> {
  const { data } = await api.put<Rule>(`${scopePrefix(scope)}/rules/${id}`, input)
  return data
}

export async function deleteRule(id: string, scope: Scope = 'user'): Promise<void> {
  await api.delete(`${scopePrefix(scope)}/rules/${id}`)
}

export async function deleteRules(ids: string[], scope: Scope = 'user'): Promise<void> {
  await api.delete(`${scopePrefix(scope)}/rules`, { data: { ids } })
}

export async function importRules(
  content: string,
  enabled = true,
  scope: Scope = 'user',
): Promise<RuleImportResponse> {
  const { data } = await api.post<RuleImportResponse>(`${scopePrefix(scope)}/rules/import`, {
    content,
    format: 'auto',
    enabled,
  })
  return data
}

export async function validateRule(
  input: Pick<RuleInput, 'kind' | 'pattern' | 'value' | 'qtypes'>,
): Promise<RuleValidateResponse> {
  const { data } = await api.post<RuleValidateResponse>('/rules/validate', input)
  return data
}
