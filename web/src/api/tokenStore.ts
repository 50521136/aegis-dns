/* ============================================================
   令牌存储
   - access_token：仅存内存（设计文档 10.1），刷新页面后靠 refresh 换取
   - refresh_token：localStorage 持久化，保证刷新页面仍保持登录
   ============================================================ */

const REFRESH_KEY = 'aegis-refresh-token'

let accessToken: string | null = null
let refreshToken: string | null = readRefresh()

function readRefresh(): string | null {
  try {
    return localStorage.getItem(REFRESH_KEY)
  } catch {
    return null
  }
}

export const tokenStore = {
  getAccess(): string | null {
    return accessToken
  },
  setAccess(token: string | null) {
    accessToken = token
  },
  getRefresh(): string | null {
    return refreshToken
  },
  setRefresh(token: string | null) {
    refreshToken = token
    try {
      if (token) localStorage.setItem(REFRESH_KEY, token)
      else localStorage.removeItem(REFRESH_KEY)
    } catch {
      /* localStorage 不可用时忽略 */
    }
  },
  /** 登录 / 刷新成功后统一写入 */
  set(tokens: { access_token: string; refresh_token?: string }) {
    accessToken = tokens.access_token
    if (tokens.refresh_token) this.setRefresh(tokens.refresh_token)
  },
  clear() {
    accessToken = null
    refreshToken = null
    try {
      localStorage.removeItem(REFRESH_KEY)
    } catch {
      /* ignore */
    }
  },
  hasSession(): boolean {
    return Boolean(refreshToken)
  },
}
