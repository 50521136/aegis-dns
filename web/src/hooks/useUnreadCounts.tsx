import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react'
import { useLocation } from 'react-router-dom'
import { getUnreadCounts } from '@/api/notifications'
import { useAuth } from './useAuth'

/**
 * 未读计数（导航红点）的全局共享状态。
 *
 * 为什么必须是 Context 而不是普通 hook：
 * 红点显示在导航栏（BottomTabBar / Sidebar），而「消掉红点」的动作发生在
 * 页面里（点开某条反馈、进入反馈页批量标记已读）。如果 useUnreadCounts
 * 每次调用都自己持一份 useState，页面里刷新的是**它自己那份**，
 * 导航栏读的仍是另一份旧值 —— 请求发出了、后端也置读了，红点却纹丝不动。
 * （这个坑实测踩到了：POST /read 与 GET /unread-counts 都 200，
 *   徽章仍显示「1 条未读」。）
 *
 * 刷新策略刻意做得很轻：
 *   - 路由切换时刷新（用户点完某个入口，数字该跟着变）；
 *   - 页面重新可见时刷新（切回标签页、手机从后台回来）；
 *   - 动作后手动 refresh（标记已读、发新反馈）；
 *   - 不做定时轮询。红点只是「有新东西」的提示，延迟几十秒无人受影响，
 *     而每 30 秒一次请求会让每个在线用户常驻一条轮询链路。
 */

interface UnreadCountsValue {
  notifications: number
  feedback: number
  refresh: () => Promise<void>
}

const UnreadCountsContext = createContext<UnreadCountsValue>({
  notifications: 0,
  feedback: 0,
  refresh: async () => {},
})

/**
 * UnreadCountsProvider 挂在登录后的布局外层（AppLayout），
 * 让导航栏与页面共享同一份未读数。
 */
export function UnreadCountsProvider({ children }: { children: React.ReactNode }) {
  const { user } = useAuth()
  const location = useLocation()
  const [counts, setCounts] = useState({ notifications: 0, feedback: 0 })
  const alive = useRef(true)

  const refresh = useCallback(async () => {
    if (!user) return
    try {
      const c = await getUnreadCounts()
      if (!alive.current) return
      setCounts({
        notifications: c.notifications ?? 0,
        feedback: c.feedback ?? 0,
      })
    } catch {
      // 红点读失败不该打扰用户：静默保持上一次的值。
    }
  }, [user])

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  // 登录态或路由变化时刷新。
  useEffect(() => {
    void refresh()
  }, [refresh, location.pathname])

  // 页面重新可见时刷新（手机切回前台最常见）。
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void refresh()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [refresh])

  return (
    <UnreadCountsContext.Provider value={{ ...counts, refresh }}>
      {children}
    </UnreadCountsContext.Provider>
  )
}

/**
 * useUnreadCounts 读取共享的未读数。
 *
 * 没有 Provider 时返回全 0 与一个空 refresh —— 不应崩掉整个界面：
 * 红点是锦上添花，缺了它页面照样能用。
 */
export function useUnreadCounts(): UnreadCountsValue {
  return useContext(UnreadCountsContext)
}
