import { api } from './client'
import type {
  Notification,
  NotificationListResponse,
  UnreadCounts,
} from './types'

// ---------------------------------------------------------------------------
// 站内通知
//
// 与公告（announcements）是两套东西：公告一个 id 对所有人可见、由管理员
// 运营；通知是按人产生的（「你的订阅 xxx 已被收录」对别人不成立）。
// ---------------------------------------------------------------------------

export async function listNotifications(
  limit = 50,
  offset = 0,
): Promise<NotificationListResponse> {
  const { data } = await api.get<NotificationListResponse>('/notifications', {
    params: { limit, offset },
  })
  return data
}

export async function markNotificationRead(id: string): Promise<void> {
  await api.post(`/notifications/${id}/read`)
}

export async function markAllNotificationsRead(): Promise<{ marked: number }> {
  const { data } = await api.post<{ marked: number }>('/notifications/read-all')
  return data
}

export async function deleteNotification(id: string): Promise<void> {
  await api.delete(`/notifications/${id}`)
}

/**
 * 读取导航红点需要的未读数（通知 + 反馈）。
 *
 * 一次返回两边，避免为两个小数字发两次请求 —— 这个接口会被前端在
 * 每次路由切换时调用，多一次往返是白费。
 */
export async function getUnreadCounts(): Promise<UnreadCounts> {
  const { data } = await api.get<UnreadCounts>('/unread-counts')
  return data
}

/** 用户把某条反馈标记为「已看过回复」（消掉反馈入口的红点）。 */
export async function markFeedbackRead(id: string): Promise<void> {
  await api.post(`/feedback/${id}/read`)
}

/** 管理员把某条反馈标记为「已处理」（消掉管理端红点）。 */
export async function adminMarkFeedbackRead(id: string): Promise<void> {
  await api.post(`/admin/feedback/${id}/read`)
}
