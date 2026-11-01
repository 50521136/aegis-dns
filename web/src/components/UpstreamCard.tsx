import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { Loader2, RefreshCw, Server, CheckCircle2, AlertTriangle } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { previewSplitRule, refreshSplitRule, type SplitRulePreview } from '@/api/admin'
import { useToast } from '@/components/ui/toast'
import { formatRelative } from '@/lib/format'

interface Preset {
  id: string
  name: string
  url: string
  desc: string
  format: string
  kind: string
}

interface Props {
  /** 当前表单值 */
  cn: string
  os: string
  fallback: string
  mode: string
  preset: string
  url: string
  gfwPreset: string
  gfwUrl: string
  autoUpdate: boolean
  domesticPresets: Preset[]
  gfwPresets: Preset[]
  updatedAt: number
  count: number
  error: string
  gfwUpdatedAt: number
  gfwCount: number
  gfwError: string
  onChange: (patch: {
    upstreams_cn?: string
    upstreams_os?: string
    fallback_upstreams?: string
    upstream_mode?: string
    split_rule_preset?: string
    split_rule_url?: string
    gfw_rule_preset?: string
    gfw_rule_url?: string
    split_rule_auto_update?: boolean
  }) => void
}

/** 上游选择模式的说明。值必须与后端 resolver 的模式名一致。 */
const MODES = [
  { value: 'fastest', label: '最快（推荐）', desc: '按历史表现优先打最快的上游，慢的自然被冷落' },
  { value: 'hedge', label: '对冲', desc: '主上游 300ms 未响应才并发备用，省流量但备用常闲置' },
  { value: 'parallel', label: '并行', desc: '全部并发取最快。实测更慢：要等最慢的那个完成' },
  { value: 'load_balance', label: '轮询', desc: '依次轮流使用，不并发。分散压力但延迟不均' },
]

/**
 * UpstreamCard 是「上游服务器 + 国内外分流」的合并配置卡片。
 *
 * 为什么合并：域名要么国内要么境外，所以上游只需要「国内」「境外」两组。
 * 旧的 upstreams 字段在填了 upstreams_cn 之后彻底失效
 * （upstreamsFor 优先返回 CN），界面上还留输入框纯属误导。
 *
 * 为什么需要分流：国内 DNS 解析境外域名时，递归查询本身是明文跨境 UDP，
 * GFW 会抢先注入假应答 —— 实测从根服务器做完整递归，最后一步问权威
 * 服务器同样被注入。加密只保护「客户端↔上游」，保护不了「上游↔权威」。
 * 所以必须把境外域名交给出口不在墙内的上游，而国内域名继续走本地低延迟上游。
 */
export function UpstreamCard(props: Props) {
  const {
    cn, os, fallback, mode, preset, url, gfwPreset, gfwUrl, autoUpdate,
    domesticPresets, gfwPresets,
    updatedAt, count, error, gfwUpdatedAt, gfwCount, gfwError, onChange,
  } = props
  const toast = useToast()
  const [preview, setPreview] = useState<SplitRulePreview | null>(null)
  const [previewKind, setPreviewKind] = useState<'domestic' | 'gfw'>('domestic')

  const previewMutation = useMutation({
    mutationFn: (v: { url: string; preset: string }) => previewSplitRule(v.url, v.preset),
    onSuccess: (d) => {
      setPreview(d)
      if (d.total === 0) {
        toast.error('解析结果为空', '地址可访问但没有解析出域名，请检查格式')
      } else {
        toast.success('解析成功', `共 ${d.total.toLocaleString()} 条规则`)
      }
    },
    onError: (e) => toast.error('拉取失败', String(e)),
  })

  const refreshMutation = useMutation({
    mutationFn: () => refreshSplitRule(),
    onSuccess: (d) => toast.success('已刷新', d.message),
    onError: (e) => toast.error('刷新失败', String(e)),
  })

  const splitEnabled = os.trim().length > 0
  const modeInfo = MODES.find((m) => m.value === mode) ?? MODES[0]

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Server className="size-4" />
          上游服务器
          {splitEnabled ? (
            <Badge variant="success">分流已启用</Badge>
          ) : (
            <Badge variant="secondary">未分流</Badge>
          )}
        </CardTitle>
        <CardDescription>
          国内域名走国内上游（低延迟），境外域名走境外上游（抗污染）。
          不填境外上游时全部走国内，行为与不分流一致。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        {/* ───── 国内上游 ───── */}
        <div className="space-y-2">
          <Label htmlFor="upstreams_cn">国内上游（每行一个）</Label>
          <Textarea
            id="upstreams_cn"
            rows={3}
            className="font-mono text-sm"
            placeholder="quic://dns.alidns.com&#10;tls://dns.alidns.com"
            value={cn}
            onChange={(e) => onChange({ upstreams_cn: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            国内域名和「两边都没命中」的域名走这里。支持 UDP/TCP（<code>223.5.5.5</code>）、
            DoT（<code>tls://…</code>，端口要写 <code>:853</code>）、DoH（<code>https://…/dns-query</code>）。
          </p>
        </div>

        {/* ───── 境外上游 ───── */}
        <div className="space-y-2">
          <Label htmlFor="upstreams_os">境外上游（每行一个）</Label>
          <Textarea
            id="upstreams_os"
            rows={3}
            className="font-mono text-sm"
            placeholder="https://your-overseas-doh.example/dns-query"
            value={os}
            onChange={(e) => onChange({ upstreams_os: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            被墙域名走这里。<strong>留空 = 关闭分流</strong>，全部走国内上游。
            Cloudflare / Google / Quad9 的公共 DoH 在国内机房实测全部被封，需要填实际可达的地址。
          </p>
        </div>

        {/* ───── 备用 + 模式 ───── */}
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="fallback_upstreams">备用服务器（每行一个）</Label>
            <Textarea
              id="fallback_upstreams"
              rows={2}
              className="font-mono text-sm"
              placeholder="留空则不启用"
              value={fallback}
              onChange={(e) => onChange({ fallback_upstreams: e.target.value })}
            />
            <p className="text-xs text-muted-foreground">
              主上游全部失败时才启用，只作用于国内侧。
            </p>
          </div>
          <div className="space-y-2">
            <Label>上游选择模式</Label>
            <Select value={mode || 'fastest'} onValueChange={(v) => onChange({ upstream_mode: v })}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MODES.map((m) => (
                  <SelectItem key={m.value} value={m.value}>
                    {m.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">{modeInfo.desc}</p>
          </div>
        </div>

        {/* ───── 分流规则 ───── */}
        <div className="space-y-4 rounded-lg border border-border/60 p-3">
          <div>
            <p className="text-sm font-medium">分流规则</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              三层判定：<strong>白名单命中 → 国内</strong>；否则<strong>黑名单命中 → 境外</strong>；
              都没命中 → 国内（安全默认）。
            </p>
          </div>

          {/* 白名单 */}
          <div className="space-y-2">
            <Label className="flex items-center gap-2">
              国内白名单
              <Badge variant="secondary">命中走国内</Badge>
            </Label>
            <Select
              value={preset || 'custom'}
              onValueChange={(v) => {
                if (v === 'custom') {
                  onChange({ split_rule_preset: 'custom' })
                  return
                }
                const p = domesticPresets.find((x) => x.id === v)
                if (p) onChange({ split_rule_preset: p.id, split_rule_url: p.url })
              }}
            >
              <SelectTrigger>
                <SelectValue placeholder="选择规则来源" />
              </SelectTrigger>
              <SelectContent>
                {domesticPresets.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
                <SelectItem value="custom">自定义地址</SelectItem>
              </SelectContent>
            </Select>
            {preset && preset !== 'custom' && (
              <p className="text-xs text-muted-foreground">
                {domesticPresets.find((p) => p.id === preset)?.desc}
              </p>
            )}
            <Input
              className="font-mono text-sm"
              placeholder="https://.../china-list.txt"
              value={url}
              onChange={(e) => onChange({ split_rule_url: e.target.value, split_rule_preset: 'custom' })}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!url || previewMutation.isPending}
                onClick={() => {
                  setPreviewKind('domestic')
                  previewMutation.mutate({ url, preset })
                }}
              >
                {previewMutation.isPending && previewKind === 'domestic' ? <Loader2 className="animate-spin" /> : null}
                预览
              </Button>
              {count > 0 && (
                <span className="text-xs text-muted-foreground">
                  {count.toLocaleString()} 条
                  {updatedAt > 0 && ` · ${formatRelative(updatedAt)}`}
                </span>
              )}
            </div>
            {error && (
              <div className="flex items-start gap-2 rounded border border-danger/30 bg-danger/5 px-2 py-1.5">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-danger" />
                <p className="text-xs text-danger">{error}</p>
              </div>
            )}
          </div>

          {/* 黑名单 */}
          <div className="space-y-2 border-t border-border/60 pt-3">
            <Label className="flex items-center gap-2">
              被墙黑名单
              <Badge variant="secondary">命中走境外</Badge>
            </Label>
            <Select
              value={gfwPreset || 'custom'}
              onValueChange={(v) => {
                if (v === 'custom') {
                  onChange({ gfw_rule_preset: 'custom' })
                  return
                }
                const p = gfwPresets.find((x) => x.id === v)
                if (p) onChange({ gfw_rule_preset: p.id, gfw_rule_url: p.url })
              }}
            >
              <SelectTrigger>
                <SelectValue placeholder="选择规则来源" />
              </SelectTrigger>
              <SelectContent>
                {gfwPresets.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
                <SelectItem value="custom">自定义地址</SelectItem>
              </SelectContent>
            </Select>
            {gfwPreset && gfwPreset !== 'custom' && (
              <p className="text-xs text-muted-foreground">
                {gfwPresets.find((p) => p.id === gfwPreset)?.desc}
              </p>
            )}
            <Input
              className="font-mono text-sm"
              placeholder="https://.../gfw.txt"
              value={gfwUrl}
              onChange={(e) => onChange({ gfw_rule_url: e.target.value, gfw_rule_preset: 'custom' })}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!gfwUrl || previewMutation.isPending}
                onClick={() => {
                  setPreviewKind('gfw')
                  previewMutation.mutate({ url: gfwUrl, preset: gfwPreset })
                }}
              >
                {previewMutation.isPending && previewKind === 'gfw' ? <Loader2 className="animate-spin" /> : null}
                预览
              </Button>
              {gfwCount > 0 && (
                <span className="text-xs text-muted-foreground">
                  {gfwCount.toLocaleString()} 条
                  {gfwUpdatedAt > 0 && ` · ${formatRelative(gfwUpdatedAt)}`}
                </span>
              )}
            </div>
            {gfwError && (
              <div className="flex items-start gap-2 rounded border border-danger/30 bg-danger/5 px-2 py-1.5">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-danger" />
                <p className="text-xs text-danger">{gfwError}</p>
              </div>
            )}
          </div>

          {/* 自动更新 + 拉取 */}
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border/60 pt-3">
            <div className="flex items-center gap-2">
              <Switch
                id="split_rule_auto_update"
                checked={autoUpdate}
                onCheckedChange={(v) => onChange({ split_rule_auto_update: v })}
              />
              <Label htmlFor="split_rule_auto_update" className="cursor-pointer text-sm">
                自动更新（每 20 小时，失败保留旧名单）
              </Label>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={refreshMutation.isPending}
              onClick={() => refreshMutation.mutate()}
            >
              {refreshMutation.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              立即拉取
            </Button>
          </div>
        </div>

        {preview && preview.total > 0 && (
          <div className="space-y-2 rounded-lg border border-border/60 p-3">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="size-4 text-success" />
              <span className="text-sm font-medium">
                解析出 {preview.total.toLocaleString()} 条
              </span>
              <Badge variant="secondary">{preview.format}</Badge>
            </div>
            <div className="max-h-32 overflow-y-auto rounded bg-muted/40 p-2">
              <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
                {preview.sample.join(' · ')}
                {preview.truncated && ' …'}
              </p>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
