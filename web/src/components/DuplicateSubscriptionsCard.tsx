import { useState } from 'react'
import { CopyX, GitMerge, Layers, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/api/client'
import { listDuplicateSubscriptions } from '@/api/subscriptions'
import { mergeDuplicateSubs } from '@/api/notifications'
import type { SubscriptionDuplicates } from '@/api/types'

/**
 * DuplicateSubscriptionsCard 检测「内容等价的重复订阅」。
 *
 * 默认收起 —— 这是一个诊断工具，不是日常要看的列表；全局订阅页
 * 本来就以「管理订阅」为主，常驻一张诊断卡会把正事挤下去。
 *
 * 关键区分（这个卡片的文案必须说清楚，否则会被误解成「要手动删」）：
 *   - 定时刷新**已经**自动共享同组订阅的下载与解析，不会重复拉取；
 *   - 这张卡报出来的是「哪几个人订了同一份」，供人决定要不要清理。
 *
 * 识别只按链接形态（GitHub raw / jsDelivr / 加速前缀的等价关系），
 * 不下载内容比对 —— 判不准的宁可不合并，因为把两个人的订阅判成
 * 同一份会让 A 的规则套到 B 身上，造成真实拦截错误。
 */
export function DuplicateSubscriptionsCard() {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [data, setData] = useState<SubscriptionDuplicates | null>(null)
  // 手动合并：记录每一组选中的保留项（key = canonical_key）。
  const [keepByGroup, setKeepByGroup] = useState<Record<string, string>>({})
  const [mergingKey, setMergingKey] = useState<string | null>(null)
  const [mergeMsg, setMergeMsg] = useState('')

  const scan = async () => {
    setLoading(true)
    setErr('')
    try {
      setData(await listDuplicateSubscriptions())
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }

  /** mergeGroup 合并一组：保留 keepId，其余删除。 */
  const mergeGroup = async (g: SubscriptionDuplicates['groups'][number]) => {
    const ids = g.subscriptions.map((x) => x.id)
    // 默认保留条目数最多的那条（最完整的一份）。管理员可以用下面的
    // 单选切换成别的 —— 比如他想保留自己账号名下的那条。
    const keep =
      keepByGroup[g.canonical_key] ??
      g.subscriptions.reduce((a, b) => (b.entries > a.entries ? b : a)).id

    setMergingKey(g.canonical_key)
    setErr('')
    setMergeMsg('')
    try {
      const res = await mergeDuplicateSubs(ids, keep)
      // 通知是后台发的（重建快照要十几秒，同步发会被请求超时打断），
      // 所以这里不能说「已通知 N 位」，只能说「已安排」。
      setMergeMsg(
        `已合并为全局订阅：删除 ${res.removed} 条重复订阅，涉及 ${res.affected_users} 位用户。` +
          `保留的那份已移到「全局订阅」并对所有人生效，相关用户稍后会收到通知。`,
      )
      await scan()
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setMergingKey(null)
    }
  }

  return (
    <div className="min-w-0 rounded-xl border border-border/60 p-3">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <Layers className="size-4 shrink-0" />
            重复订阅检测
          </p>
          <p className="mt-0.5 break-words text-xs text-muted-foreground">
            识别指向同一份内容的不同链接形态（GitHub raw、jsDelivr、各类加速前缀）。
            只认链接，不下载内容比对。
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          {open ? (
            <>
              <Button size="sm" variant="secondary" onClick={scan} loading={loading}>
                <RefreshCw />
                开始检测
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
                收起
              </Button>
            </>
          ) : (
            <Button
              size="sm"
              variant="secondary"
              onClick={() => {
                setOpen(true)
                if (!data) void scan()
              }}
            >
              <CopyX />
              检测重复
            </Button>
          )}
        </div>
      </div>

      {open && (
        <div className="mt-3 min-w-0 space-y-3">
          {loading && !data && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Spinner /> 正在比对链接…
            </div>
          )}
          {err && <p className="break-words text-xs text-danger">{err}</p>}
          {mergeMsg && (
            <p className="rounded-lg bg-success/10 px-3 py-2 text-xs text-success">{mergeMsg}</p>
          )}

          {data && (
            <>
              <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>启用中的订阅 {data.total_enabled} 个</span>
                <span className="text-border">·</span>
                <span>
                  发现{' '}
                  <span className="font-medium text-foreground">{data.groups.length}</span> 组重复
                </span>
                {data.auto_saved_fetches > 0 && (
                  <>
                    <span className="text-border">·</span>
                    <Badge variant="success">
                      每轮自动少拉 {data.auto_saved_fetches} 次
                    </Badge>
                  </>
                )}
              </div>

              {data.groups.length === 0 ? (
                <p className="rounded-lg bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
                  没有发现重复订阅。注意：只有链接能被确定判等的才算重复 ——
                  指向同一份内容但域名完全不同的镜像（例如自建的反代）
                  不做合并，避免把不同内容判成同一份。
                </p>
              ) : (
                <ul className="min-w-0 space-y-2">
                  {data.groups.map((g) => (
                    <li key={g.canonical_key} className="min-w-0 rounded-lg border border-border p-2.5">
                      <div className="flex min-w-0 flex-wrap items-center gap-2">
                        <Badge variant="outline">{g.subscriptions.length} 份</Badge>
                        <span className="text-xs text-muted-foreground">
                          合计 {g.entries.toLocaleString()} 条
                        </span>
                        <span className="ml-auto shrink-0 text-xs text-success">
                          少拉 {g.extra_fetches} 次
                        </span>
                      </div>
                      {/* 链接逐条单独成行并 break-all：两个长链接并排放必然溢出手机屏 */}
                      <ul className="mt-2 min-w-0 space-y-1">
                        {g.urls.map((u) => (
                          <li key={u}>
                            <code className="block break-all text-[11px] leading-relaxed text-muted-foreground">
                              {u}
                            </code>
                          </li>
                        ))}
                      </ul>
                      {/* 逐条列出订阅并允许选「保留哪一条」：
                          合并要删掉其余的，删错就是用户真实丢了规则集，
                          所以保留项必须让人看见并可选，不能静默挑一条。 */}
                      <ul className="mt-2 min-w-0 space-y-1">
                        {g.subscriptions.map((sub) => {
                          const keepId =
                            keepByGroup[g.canonical_key] ??
                            g.subscriptions.reduce((a, b) => (b.entries > a.entries ? b : a)).id
                          return (
                            <li key={sub.id} className="flex min-w-0 items-center gap-2">
                              <input
                                type="radio"
                                name={`keep-${g.canonical_key}`}
                                checked={keepId === sub.id}
                                onChange={() =>
                                  setKeepByGroup((p) => ({ ...p, [g.canonical_key]: sub.id }))
                                }
                                className="size-3.5 shrink-0 accent-[var(--primary)]"
                              />
                              <span className="shrink-0 text-[11.5px] font-medium">
                                {sub.username || sub.user_id.slice(0, 10)}
                              </span>
                              <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
                                {sub.name}
                              </span>
                              <span className="shrink-0 font-mono text-[10.5px] tabular-nums text-muted-foreground/70">
                                {sub.entries.toLocaleString()} 条
                              </span>
                            </li>
                          )
                        })}
                      </ul>

                      <div className="mt-2 flex flex-wrap items-center gap-2">
                        <Button
                          size="sm"
                          variant="outline"
                          loading={mergingKey === g.canonical_key}
                          onClick={() => void mergeGroup(g)}
                        >
                          <GitMerge />
                          合并为全局
                        </Button>
                        <span className="min-w-0 flex-1 text-[11px] leading-relaxed text-muted-foreground">
                          保留选中那条并
                          <strong className="font-medium text-foreground">转为全局订阅</strong>
                          （所有人继续生效），其余 {g.subscriptions.length - 1} 条删除；
                          相关用户都会收到通知
                        </span>
                      </div>
                    </li>
                  ))}
                </ul>
              )}

              <p className="text-xs text-muted-foreground">
                标记为重复的订阅<span className="text-foreground">不需要手动处理</span>
                ：定时刷新时同组只会真正下载、解析一次，内容共享给这些订阅，
                各自仍保留自己的条目与规则生效范围。
              </p>
            </>
          )}
        </div>
      )}
    </div>
  )
}
