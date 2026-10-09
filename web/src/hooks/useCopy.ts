import { useCallback, useRef, useState } from 'react'

/** 复制到剪贴板，带短暂的成功态反馈 */
export function useCopy(timeout = 1800) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const copy = useCallback(
    async (text: string) => {
      try {
        if (navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(text)
        } else {
          // 降级：execCommand
          const el = document.createElement('textarea')
          el.value = text
          el.style.position = 'fixed'
          el.style.opacity = '0'
          document.body.appendChild(el)
          el.select()
          document.execCommand('copy')
          document.body.removeChild(el)
        }
        setCopied(true)
        if (timer.current) clearTimeout(timer.current)
        timer.current = setTimeout(() => setCopied(false), timeout)
        return true
      } catch {
        return false
      }
    },
    [timeout],
  )

  return { copied, copy }
}
