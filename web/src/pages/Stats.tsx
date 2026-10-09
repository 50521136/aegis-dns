import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { Ban, BarChart3, Database, Globe2, Timer } from 'lucide-react'
import { getSummary, getTimeseries, getTop, type StatsRange, type TopType } from '@/api/stats'
import { PageHeader } from '@/components/PageHeader'
import { StatCard } from '@/components/StatCard'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/api/client'
import { formatCompact, formatNumber, formatPercent } from '@/lib/format'

const RANGES: { value: StatsRange; label: string }[] = [
  { value: '1h', label: '1 小时' },
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
]

function intervalFor(range: StatsRange): string {
  switch (range) {
    case '1h':
      return '1m'
    case '24h':
      return '1h'
    case '7d':
      return '1h'
    case '30d':
      return '1d'
  }
}

function formatTs(ts: string | number, range: StatsRange): string {
  const d = typeof ts === 'number' ? new Date(ts * 1000) : new Date(ts)
  if (Number.isNaN(d.getTime())) return String(ts)
  if (range === '1h') return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (range === '30d') return `${d.getMonth() + 1}/${d.getDate()}`
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}h`
}

const PIE_COLORS = ['rgb(var(--success))', 'rgb(var(--danger))', 'rgb(var(--primary))', 'rgb(var(--warning))']

export default function Stats() {
  const [range, setRange] = useState<StatsRange>('24h')
  const [topType, setTopType] = useState<TopType>('all')

  const summary = useQuery({
    queryKey: ['stats', 'summary', range],
    queryFn: () => getSummary(range),
  })

  const timeseries = useQuery({
    queryKey: ['stats', 'timeseries', range, intervalFor(range)],
    queryFn: () => getTimeseries(range, intervalFor(range)),
  })

  const top = useQuery({
    queryKey: ['stats', 'top', range, topType],
    queryFn: () => getTop(range, 10, topType),
  })

  const s = summary.data

  const chartData = (timeseries.data?.points ?? []).map((p) => ({
    ...p,
    label: formatTs(p.ts, range),
  }))

  const pieData = s
    ? [
        { name: '缓存命中', value: s.cached },
        { name: '已拦截', value: s.blocked },
        { name: '放行', value: s.allowed },
      ].filter((d) => d.value > 0)
    : []

  const topData = (top.data?.items ?? []).map((d) => ({
    domain: d.domain.length > 24 ? `${d.domain.slice(0, 22)}…` : d.domain,
    full: d.domain,
    hits: d.hits,
  }))

  return (
    <div className="space-y-6">
      <PageHeader
        title="统计分析"
        description="查询量、拦截率、缓存命中与热门域名"
        icon={<BarChart3 className="size-5" />}
        actions={
          <Tabs value={range} onValueChange={(v) => setRange(v as StatsRange)}>
            <TabsList>
              {RANGES.map((r) => (
                <TabsTrigger key={r.value} value={r.value}>
                  {r.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        }
      />

      {summary.isError ? (
        <ErrorState message={errorMessage(summary.error)} onRetry={() => summary.refetch()} />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {summary.isLoading
            ? Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[132px] rounded-2xl" />)
            : (
              <>
                <StatCard label="总查询量" value={s?.total ?? 0} icon={<Globe2 className="size-4" />} tone="primary" />
                <StatCard
                  label="拦截率"
                  value={formatPercent(s?.block_rate)}
                  icon={<Ban className="size-4" />}
                  tone="danger"
                  animate={false}
                />
                <StatCard
                  label="缓存命中率"
                  value={formatPercent(s?.cache_hit_rate)}
                  icon={<Database className="size-4" />}
                  tone="success"
                  animate={false}
                />
                <StatCard
                  label="独立域名"
                  value={s?.unique_domains ?? 0}
                  icon={<Timer className="size-4" />}
                  tone="warning"
                />
              </>
            )}
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>查询时间序列</CardTitle>
          </CardHeader>
          <CardContent>
            {timeseries.isLoading ? (
              <Skeleton className="h-72 w-full rounded-xl" />
            ) : timeseries.isError ? (
              <ErrorState message={errorMessage(timeseries.error)} onRetry={() => timeseries.refetch()} />
            ) : chartData.length === 0 ? (
              <EmptyState icon={<BarChart3 className="size-6" />} title="暂无数据" />
            ) : (
              <div className="h-72 w-full">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={chartData} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="rgb(var(--border))" vertical={false} />
                    <XAxis
                      dataKey="label"
                      tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))' }}
                      tickLine={false}
                      axisLine={false}
                      minTickGap={24}
                    />
                    <YAxis
                      tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))' }}
                      tickLine={false}
                      axisLine={false}
                      tickFormatter={(v) => formatCompact(Number(v))}
                    />
                    <Tooltip
                      cursor={{ fill: 'rgb(var(--muted) / 0.5)' }}
                      contentStyle={{
                        background: 'rgb(var(--card))',
                        border: '1px solid rgb(var(--border))',
                        borderRadius: 12,
                        fontSize: 12,
                        color: 'rgb(var(--foreground))',
                      }}
                      formatter={(value: number, name: string) => {
                        const map: Record<string, string> = {
                          total: '总量',
                          blocked: '拦截',
                          cached: '缓存',
                          allowed: '放行',
                        }
                        return [formatNumber(value), map[name] ?? name]
                      }}
                    />
                    <Legend
                      formatter={(v: string) => {
                        const map: Record<string, string> = {
                          total: '总量',
                          blocked: '拦截',
                          cached: '缓存',
                          allowed: '放行',
                        }
                        return map[v] ?? v
                      }}
                      wrapperStyle={{ fontSize: 12 }}
                    />
                    <Bar dataKey="total" fill="rgb(var(--primary))" radius={[4, 4, 0, 0]} maxBarSize={28} />
                    <Bar dataKey="blocked" fill="rgb(var(--danger))" radius={[4, 4, 0, 0]} maxBarSize={28} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>流量构成</CardTitle>
          </CardHeader>
          <CardContent>
            {summary.isLoading ? (
              <Skeleton className="h-64 w-full rounded-xl" />
            ) : pieData.length === 0 ? (
              <EmptyState icon={<Database className="size-6" />} title="暂无数据" className="py-8" />
            ) : (
              <div className="h-64 w-full">
                <ResponsiveContainer width="100%" height="100%">
                  <PieChart>
                    <Pie
                      data={pieData}
                      dataKey="value"
                      nameKey="name"
                      innerRadius={54}
                      outerRadius={84}
                      paddingAngle={3}
                      stroke="rgb(var(--card))"
                      strokeWidth={2}
                    >
                      {pieData.map((_, i) => (
                        <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                      ))}
                    </Pie>
                    <Tooltip
                      contentStyle={{
                        background: 'rgb(var(--card))',
                        border: '1px solid rgb(var(--border))',
                        borderRadius: 12,
                        fontSize: 12,
                        color: 'rgb(var(--foreground))',
                      }}
                      formatter={(value: number) => formatNumber(value)}
                    />
                    <Legend wrapperStyle={{ fontSize: 12 }} />
                  </PieChart>
                </ResponsiveContainer>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>TOP 域名</CardTitle>
          <Tabs value={topType} onValueChange={(v) => setTopType(v as TopType)}>
            <TabsList className="h-9">
              <TabsTrigger value="all">全部</TabsTrigger>
              <TabsTrigger value="blocked">仅拦截</TabsTrigger>
            </TabsList>
          </Tabs>
        </CardHeader>
        <CardContent>
          {top.isLoading ? (
            <Skeleton className="h-72 w-full rounded-xl" />
          ) : top.isError ? (
            <ErrorState message={errorMessage(top.error)} onRetry={() => top.refetch()} />
          ) : topData.length === 0 ? (
            <EmptyState icon={<BarChart3 className="size-6" />} title="暂无数据" />
          ) : (
            <div className="h-80 w-full">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={topData} layout="vertical" margin={{ top: 4, right: 16, left: 8, bottom: 4 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="rgb(var(--border))" horizontal={false} />
                  <XAxis
                    type="number"
                    tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))' }}
                    tickLine={false}
                    axisLine={false}
                    tickFormatter={(v) => formatCompact(Number(v))}
                  />
                  <YAxis
                    type="category"
                    dataKey="domain"
                    width={150}
                    tick={{ fontSize: 11, fill: 'rgb(var(--muted-foreground))', fontFamily: 'JetBrains Mono, monospace' }}
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    cursor={{ fill: 'rgb(var(--muted) / 0.5)' }}
                    contentStyle={{
                      background: 'rgb(var(--card))',
                      border: '1px solid rgb(var(--border))',
                      borderRadius: 12,
                      fontSize: 12,
                      color: 'rgb(var(--foreground))',
                    }}
                    formatter={(value: number) => [formatNumber(value), '命中']}
                    labelFormatter={(label, payload) => payload?.[0]?.payload?.full ?? label}
                  />
                  <Bar dataKey="hits" fill="rgb(var(--primary))" radius={[0, 4, 4, 0]} maxBarSize={22} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
