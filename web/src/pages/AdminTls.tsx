import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, Lock, RefreshCw, ShieldAlert, Upload } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { useToast } from '@/components/ui/toast'
import { getTlsStatus, uploadTls } from '@/api/scope'
import { errorMessage } from '@/api/client'
import { cn } from '@/lib/utils'

/** 到期多少天内开始告警 */
const WARN_DAYS = 30

function fmtDate(unix: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false })
}

export default function AdminTls() {
  const qc = useQueryClient()
  const toast = useToast()

  const [certPem, setCertPem] = useState('')
  const [keyPem, setKeyPem] = useState('')

  const statusQuery = useQuery({ queryKey: ['admin', 'tls'], queryFn: getTlsStatus })
  const st = statusQuery.data

  const upload = useMutation({
    mutationFn: () => uploadTls(certPem, keyPem),
    onSuccess: (res) => {
      toast.success('证书已更新', `覆盖 ${res.dns_names.join(', ') || res.subject}`)
      setCertPem('')
      setKeyPem('')
      qc.invalidateQueries({ queryKey: ['admin', 'tls'] })
    },
    onError: (err) => toast.error('上传失败', errorMessage(err)),
  })

  const canSubmit = certPem.trim().length > 0 && keyPem.trim().length > 0 && !upload.isPending

  return (
    <div className="space-y-6">
      <PageHeader
        title="证书管理"
        description="DoT / DoH 使用这里的证书；通配符证书才能支持 <标识>.<域名> 形式的接入地址"
        icon={<Lock className="size-5" />}
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => statusQuery.refetch()}
            disabled={statusQuery.isFetching}
          >
            <RefreshCw className={cn(statusQuery.isFetching && 'animate-spin')} />
            刷新
          </Button>
        }
      />

      {/* ── 当前状态 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            当前证书
            {st?.available ? (
              st.self_signed ? (
                <Badge variant="warning">自签</Badge>
              ) : (
                <Badge variant="success">有效</Badge>
              )
            ) : (
              <Badge variant="danger">未配置</Badge>
            )}
          </CardTitle>
          <CardDescription>
            {st?.available
              ? 'DoT / DoH 正在使用这张证书'
              : '没有证书时 DoT 与 DoH 会被跳过，只有 UDP/TCP DNS 可用'}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {statusQuery.isLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner /> 读取中…
            </div>
          ) : !st?.available ? (
            <div className="flex items-start gap-2 rounded-xl bg-warning/10 p-3 text-sm text-warning">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" />
              <span>还没有可用证书。上传下方 PEM 后 DoT / DoH 立即可用，不需要重启服务。</span>
            </div>
          ) : (
            <>
              <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
                <Field label="主体" value={st.subject} mono />
                <Field label="签发者" value={st.issuer} mono />
                <Field label="生效时间" value={fmtDate(st.not_before)} />
                <Field
                  label="到期时间"
                  value={fmtDate(st.not_after)}
                  tone={st.days_left < 0 ? 'danger' : st.days_left < WARN_DAYS ? 'warning' : undefined}
                />
                <Field label="证书文件" value={st.cert_file} mono />
                <Field label="私钥文件" value={st.key_file} mono />
              </dl>

              <div className="flex flex-wrap items-center gap-2">
                <span className="eyebrow">覆盖域名</span>
                {st.dns_names.length === 0 ? (
                  <span className="text-sm text-muted-foreground">无 SAN</span>
                ) : (
                  st.dns_names.map((n) => (
                    <Badge key={n} variant="outline" className="font-mono">
                      {n}
                    </Badge>
                  ))
                )}
              </div>

              <div className="flex flex-wrap items-center gap-3 text-sm">
                <span className="eyebrow">剩余</span>
                <span
                  className={cn(
                    'readout text-lg',
                    st.days_left < 0
                      ? 'text-danger'
                      : st.days_left < WARN_DAYS
                        ? 'text-warning'
                        : 'text-success',
                  )}
                >
                  {st.days_left}
                </span>
                <span className="text-muted-foreground">天</span>
                {st.days_left < 0 && (
                  <span className="flex items-center gap-1 text-danger">
                    <AlertTriangle className="size-4" /> 已过期，客户端会拒绝连接
                  </span>
                )}
                {st.days_left >= 0 && st.days_left < WARN_DAYS && (
                  <span className="flex items-center gap-1 text-warning">
                    <AlertTriangle className="size-4" /> 即将到期，请尽快续期
                  </span>
                )}
                {st.self_signed && (
                  <span className="text-muted-foreground">
                    （自签证书只适合内网测试，公网客户端会因校验失败拒绝连接）
                  </span>
                )}
              </div>

              <div className="flex flex-wrap items-center gap-3">
                <span className="eyebrow">指纹</span>
                <code className="mono-addr text-xs text-muted-foreground">{st.fingerprint}</code>
              </div>
            </>
          )}
        </CardContent>
      </Card>

      {/* ── 上传 ── */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">上传新证书</CardTitle>
          <CardDescription>
            粘贴 PEM 全文。证书与私钥会先校验配对，通过后才写盘并热加载 ——
            填错不会把现有证书弄坏。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cert">证书（含证书链，BEGIN CERTIFICATE）</Label>
            <textarea
              id="cert"
              value={certPem}
              onChange={(e) => setCertPem(e.target.value)}
              rows={6}
              spellCheck={false}
              placeholder={'-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\n（中间证书可选，但建议带上）'}
              className="w-full rounded-xl border border-border px-3 py-2 font-mono text-xs leading-relaxed text-foreground placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
              style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="key">私钥（BEGIN PRIVATE KEY）</Label>
            <textarea
              id="key"
              value={keyPem}
              onChange={(e) => setKeyPem(e.target.value)}
              rows={5}
              spellCheck={false}
              placeholder={'-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----'}
              className="w-full rounded-xl border border-border px-3 py-2 font-mono text-xs leading-relaxed text-foreground placeholder:text-muted-foreground/60 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
              style={{ background: 'var(--input-bg)', boxShadow: 'var(--groove-inset)' }}
            />
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <Button onClick={() => upload.mutate()} disabled={!canSubmit} loading={upload.isPending}>
              <Upload />
              校验并启用
            </Button>
            {upload.isSuccess && (
              <span className="flex items-center gap-1 text-sm text-success">
                <CheckCircle2 className="size-4" /> 已启用
              </span>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

function Field({
  label,
  value,
  mono,
  tone,
}: {
  label: string
  value: string
  mono?: boolean
  tone?: 'danger' | 'warning'
}) {
  return (
    <div className="min-w-0">
      <dt className="eyebrow mb-1">{label}</dt>
      <dd
        className={cn(
          'truncate text-sm',
          mono && 'font-mono text-xs',
          tone === 'danger' && 'text-danger',
          tone === 'warning' && 'text-warning',
        )}
        title={value}
      >
        {value || '—'}
      </dd>
    </div>
  )
}
