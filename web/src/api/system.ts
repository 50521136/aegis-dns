import axios from 'axios'
import { API_BASE } from './client'

/* 无需认证的系统端点 */

export async function healthz(): Promise<boolean> {
  try {
    const { status } = await axios.get(`${API_BASE}/healthz`, { timeout: 5000 })
    return status >= 200 && status < 300
  } catch {
    return false
  }
}

export interface VersionInfo {
  version: string
  [key: string]: unknown
}

export async function getVersion(): Promise<VersionInfo> {
  const { data } = await axios.get<VersionInfo>(`${API_BASE}/version`, { timeout: 5000 })
  return data
}
