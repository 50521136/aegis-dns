import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import {
  Activity,
  Ban,
  BookOpen,
  Database,
  Globe2,
  RefreshCw,
  ShieldCheck,
  TrendingUp,
} from 'lucide-react'
import { getSummary, getTimeseries, getTop } from '@/api/stats'
import { useAuth } from '@/hooks/useAuth'
import { StatCard } from '@/components/StatCard'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorState } from '@/components/ErrorState'
import { EmptyState } from '@/components/EmptyState'
import { errorMessage } from '@/api/client'
import { formatCompact, formatNumber, formatPercent } from '@/lib/format'

function formatTs(ts: string | number): string {
  const d = typeof ts === 'number' ? new Date(ts * 1000) : new Date(ts)
  if (Number.isNaN(d.getTime())) return String(ts)
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:00`
}

export default function Dashboard() {
  const { user, dnsConfig } = useAuth()

  const summary = useQuery({
    queryKey: ['stats', 'summary', '24h'],
    queryFn: () => getSummary('24h'),
  })

  const timeseries = useQuery({
    queryKey: ['stats', 'timeseries', '7d', '1h'],
    queryFn: () => getTimeseries('7d', '1h'),
  })

  const top = useQuery({
    queryKey: ['stats', 'top', '24h', 'blocked'],
    queryFn: () => getTop('24h', 8, 'blocked'),
  })

  const s = summary.data
  const chartData = (timeseries.data?.points ?? []).map((p) => ({
    ...p,
    label: formatTs(p.ts),
  }))

  return (
    <div className="space-y-6">
      <PageHeader
        title={`你好，${user?.username ?? ''}`}
        description="这里是你的 DNS 服务总览"
        icon={<Activity className="size-5" />}
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link to="/app/setup">
              <BookOpen />
              接入配置
            </Link>
          </Button>
        }
      />

      {/* 统计卡：1 -> 2 -> 4 列 */}
      {summary.isError ? (
        <ErrorState
          title="无法加载统计"
          message={errorMessage(summary.error)}
          onRetry={() => summary.refetch()}
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {summary.isLoading
            ? Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[132px] rounded-2xl" />)
            : (
              <>
                <StatCard
                  label="总查询量"
                  value={s?.total ?? 0}
                  icon={<Globe2 className="size-4" />}
                  tone="primary"
                />
                <StatCard
                  label="已拦截"
                  value={s?.blocked ?? 0}
                  icon={<Ban className="size-4" />}
                  tone="danger"
                  delta={s?.block_rate}
                  deltaLabel="拦截率"
                />
                <StatCard
                  label="缓存命中"
                  value={s?.cached ?? 0}
                  icon={<Database className="size-4" />}
                  tone="success"
                  delta={s?.cache_hit_rate}
                  deltaLabel="命中率"
                />
                <StatCard
                  label="平均延迟"
                  value={s?.avg_latency_ms ?? 0}
                  suffix="ms"
                  icon={<TrendingUp className="size-4" />}
                  tone="warning"
                />
              </>
            )}
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        {/* 时间序列 */}
        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>查询趋势（近 7 天）</CardTitle>
            <div className="flex items-center gap-3 text-xs text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <span className="size-2.5 rounded-full bg-primary" /> 总量
              </span>
              <span className="flex items-center gap-1.5">
                <span className="size-2.5 rounded-full bg-danger" /> 拦截
              </span>
            </div>
          </CardHeader>
          <CardContent>
            {timeseries.isLoading ? (
              <Skeleton className="h-64 w-full rounded-xl" />
            ) : timeseries.isError ? (
              <ErrorState
                message={errorMessage(timeseries.error)}
                onRetry={() => timeseries.refetch()}
              />
            ) : chartData.length === 0 ? (
              <EmptyState
                icon={<Activity className="size-6" />}
                title="暂无统计数据"
                description="接入 DNS 并产生查询后，这里会显示趋势图。"
              />
            ) : (
              <div className="h-64 w-full">
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={chartData} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                    <defs>
                      <linearGradient id="gTotal" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" stopColor="rgb(var(--primary))" stopOpacity={0.35} />
                        <stop offset="100%" stopColor="rgb(var(--primary))" stopOpacity={0} />
                      </linearGradient>
                      <linearGradient id="gBlocked" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" stopColor="rgb(var(--danger))" stopOpacity={0.3} />
                        <stop offset="100%" stopColor="rgb(var(--danger))" stopOpacity={0} />
                      </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray="3 3" stroke="rgb(var(--border))" vertical={false} />
                    <XAxis
                      dataKey="label"
                      tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))' }}
                      tickLine={false}
                      axisLine={false}
                      minTickGap={28}
                    />
                    <YAxis
                      tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))' }}
                      tickLine={false}
                      axisLine={false}
                      tickFormatter={(v) => formatCompact(Number(v))}
                    />
                    <Tooltip
                      contentStyle={{
                        background: 'rgb(var(--card))',
                        border: '1px solid rgb(var(--border))',
                        borderRadius: 12,
                        fontSize: 12,
                        color: 'rgb(var(--foreground))',
                      }}
                      formatter={(value: number, name: string) => [formatNumber(value), name === 'total' ? '总量' : '拦截']}
                    />
                    <Area
                      type="monotone"
                      dataKey="total"
                      stroke="rgb(var(--primary))"
                      strokeWidth={2}
                      fill="url(#gTotal)"
                    />
                    <Area
                      type="monotone"
                      dataKey="blocked"
                      stroke="rgb(var(--danger))"
                      strokeWidth={2}
                      fill="url(#gBlocked)"
                    />
                  </AreaChart>
                </ResponsiveContainer>
              </div>
            )}
          </CardContent>
        </Card>

        {/* TOP 拦截域名 */}
        <Card>
          <CardHeader>
            <CardTitle>拦截最多的域名</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            {top.isLoading ? (
              Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-9 w-full rounded-lg" />)
            ) : top.isError ? (
              <ErrorState message={errorMessage(top.error)} onRetry={() => top.refetch()} />
            ) : (top.data?.items?.length ?? 0) === 0 ? (
              <EmptyState icon={<ShieldCheck className="size-6" />} title="暂无拦截记录" className="py-8" />
            ) : (
              top.data!.items.map((d, i) => (
                <motion.div
                  key={d.domain}
                  initial={{ opacity: 0, x: -6 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ delay: i * 0.03, duration: 0.2 }}
                  className="flex items-center gap-3 rounded-lg px-2 py-2 transition-colors hover:bg-muted/50"
                >
                  <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-xs font-semibold tabular-nums text-muted-foreground">
                    {i + 1}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-mono text-[13px]" title={d.domain}>
                    {d.domain}
                  </span>
                  <Badge variant="danger" className="tabular-nums">
                    {formatCompact(d.hits)}
                  </Badge>
                </motion.div>
              ))
            )}
          </CardContent>
        </Card>
      </div>

      {/* 快捷入口 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <QuickLink
          to="/app/setup"
          icon={<BookOpen className="size-5" />}
          title="我的 DNS"
          desc={dnsConfig ? dnsConfig.doh_url : '查看专属接入地址与二维码'}
        />
        <QuickLink
          to="/app/rules"
          icon={<ShieldCheck className="size-5" />}
          title="规则管理"
          desc="添加拦截 / 放行 / 改写规则"
        />
        <QuickLink
          to="/app/subscriptions"
          icon={<RefreshCw className="size-5" />}
          title="订阅列表"
          desc="管理外部规则列表与自动更新"
        />
      </div>
    </div>
  )
}

function QuickLink({
  to,
  icon,
  title,
  desc,
}: {
  to: string
  icon: React.ReactNode
  title: string
  desc: string
}) {
  return (
    <Link
      to={to}
      className="group flex items-start gap-3 rounded-2xl border border-border/50 bg-card p-5 shadow-sm transition-all duration-200 hover:-translate-y-0.5 hover:shadow-md"
    >
      <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
        {icon}
      </div>
      <div className="min-w-0">
        <p className="text-sm font-semibold">{title}</p>
        <p className="mt-0.5 truncate text-xs text-muted-foreground" title={desc}>
          {desc}
        </p>
      </div>
    </Link>
  )
}
