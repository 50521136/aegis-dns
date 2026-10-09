import { api } from './client'
import type {
  Rule,
  RuleImportResponse,
  RuleInput,
  RuleListResponse,
  RuleValidateResponse,
} from './types'

export interface RuleQuery {
  page?: number
  page_size?: number
  q?: string
  kind?: string
  enabled?: boolean
}

export async function listRules(params: RuleQuery = {}): Promise<RuleListResponse> {
  const { data } = await api.get<RuleListResponse>('/rules', { params })
  return data
}

export async function createRule(input: RuleInput): Promise<Rule> {
  const { data } = await api.post<Rule>('/rules', input)
  return data
}

export async function updateRule(id: string, input: Partial<RuleInput>): Promise<Rule> {
  const { data } = await api.put<Rule>(`/rules/${id}`, input)
  return data
}

export async function deleteRule(id: string): Promise<void> {
  await api.delete(`/rules/${id}`)
}

export async function deleteRules(ids: string[]): Promise<void> {
  await api.delete('/rules', { data: { ids } })
}

export async function importRules(content: string, enabled = true): Promise<RuleImportResponse> {
  const { data } = await api.post<RuleImportResponse>('/rules/import', {
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
