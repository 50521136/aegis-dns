import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { getUnreadCounts } from '@/api/notifications'
import { useAuth } from './useAuth'

/**
 * useUnreadCounts 提供导航红点需要的未读数。
 *
 * 刷新策略刻意做得很轻：
 *   - 路由切换时刷新一次（用户刚点完某个入口，数字该跟着变）；
 *   - 页面重新可见时刷新（切回标签页、手机从后台回来）；
 *   - 不做定时轮询。红点只是「有新东西」的提示，延迟几十秒无人受影响，
 *     而每 30 秒一次请求会让每个在线用户都常驻一条轮询链路。
 *
 * 组件卸载后不再 setState：异步请求回来时人可能已经离开页面，
 * 那会触发 React 的「更新已卸载组件」警告。
 */
export function useUnreadCounts() {
  const { user } = useAuth()
  const location = useLocation()
  const [notifications, setNotifications] = useState(0)
  const [feedback, setFeedback] = useState(0)
  const alive = useRef(true)

  const refresh = useCallback(async () => {
    if (!user) return
    try {
      const c = await getUnreadCounts()
      if (!alive.current) return
      setNotifications(c.notifications ?? 0)
      setFeedback(c.feedback ?? 0)
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

  return { notifications, feedback, refresh }
}
