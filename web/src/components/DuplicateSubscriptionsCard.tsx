import { useState } from 'react'
import { CopyX, Layers, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/api/client'
import { listDuplicateSubscriptions } from '@/api/subscriptions'
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
                      <p className="mt-1.5 break-words text-[11px] text-muted-foreground">
                        归属：
                        {g.subscriptions.map((s) => s.username || s.user_id.slice(0, 12)).join('、')}
                      </p>
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
