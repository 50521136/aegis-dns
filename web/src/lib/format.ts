/** 数字 / 时间 / 字节 等格式化工具 */

/** 千分位数字，例如 125430 -> "125,430" */
export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  return new Intl.NumberFormat('en-US').format(n)
}

/** 紧凑数字，例如 125430 -> "125.4K" */
export function formatCompact(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

/** 百分比，输入 0.1454 -> "14.5%" */
export function formatPercent(ratio: number | null | undefined, digits = 1): string {
  if (ratio === null || ratio === undefined || Number.isNaN(ratio)) return '—'
  return `${(ratio * 100).toFixed(digits)}%`
}

/** 相对时间，例如 "3 分钟前" */
export function formatRelative(input: string | number | null | undefined): string {
  if (input === null || input === undefined || input === '') return '—'
  const d = typeof input === 'number' ? new Date(input * 1000) : new Date(input)
  if (Number.isNaN(d.getTime())) return '—'
  const diff = Date.now() - d.getTime()
  const sec = Math.round(diff / 1000)
  if (sec < 0) return formatDateTime(d)
  if (sec < 60) return '刚刚'
  const min = Math.round(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr} 小时前`
  const day = Math.round(hr / 24)
  if (day < 30) return `${day} 天前`
  return formatDateTime(d)
}

/** 本地化日期时间 */
export function formatDateTime(input: string | number | Date | null | undefined): string {
  if (input === null || input === undefined || input === '') return '—'
  const d = input instanceof Date ? input : typeof input === 'number' ? new Date(input * 1000) : new Date(input)
  if (Number.isNaN(d.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(d)
}

/** 仅日期 */
export function formatDate(input: string | number | Date | null | undefined): string {
  if (input === null || input === undefined || input === '') return '—'
  const d = input instanceof Date ? input : typeof input === 'number' ? new Date(input * 1000) : new Date(input)
  if (Number.isNaN(d.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' }).format(d)
}

/** 秒数 -> "1天 2小时 3分" */
export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '—'
  let s = Math.floor(seconds)
  const d = Math.floor(s / 86400)
  s %= 86400
  const h = Math.floor(s / 3600)
  s %= 3600
  const m = Math.floor(s / 60)
  const parts: string[] = []
  if (d) parts.push(`${d} 天`)
  if (h) parts.push(`${h} 小时`)
  if (m) parts.push(`${m} 分`)
  if (!parts.length) parts.push(`${s} 秒`)
  return parts.join(' ')
}

/** 字节 -> 人类可读 */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 毫秒延迟着色 */
export function latencyTone(ms: number): 'success' | 'warning' | 'danger' {
  if (ms < 50) return 'success'
  if (ms < 200) return 'warning'
  return 'danger'
}
