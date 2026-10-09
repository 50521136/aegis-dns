import { useEffect, useState } from 'react'

/** 通用媒体查询 hook */
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined') return false
    return window.matchMedia(query).matches
  })

  useEffect(() => {
    const mql = window.matchMedia(query)
    const handler = (e: MediaQueryListEvent) => setMatches(e.matches)
    setMatches(mql.matches)
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [query])

  return matches
}

/** 是否桌面端（>= 768px，导航切换点） */
export function useIsDesktop(): boolean {
  return useMediaQuery('(min-width: 768px)')
}

/** 是否宽屏（>= 1024px） */
export function useIsLarge(): boolean {
  return useMediaQuery('(min-width: 1024px)')
}

/** 是否尊重 reduced motion */
export function usePrefersReducedMotion(): boolean {
  return useMediaQuery('(prefers-reduced-motion: reduce)')
}
