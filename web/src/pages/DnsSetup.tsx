import { useMemo, useState } from 'react'
import { cn } from '@/lib/utils'
import { useQuery } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import {
  Check,
  Copy,
  Apple,
  BookOpen,
  Fingerprint,
  Globe2,
  Monitor,
  Smartphone,
  Terminal,
  Wifi,
  Download,
} from 'lucide-react'
import { downloadMobileConfig, getDnsConfig, getMe } from '@/api/me'
import { PageHeader } from '@/components/PageHeader'
import { CopyField } from '@/components/CopyField'
import { ErrorState } from '@/components/ErrorState'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import { useToast } from '@/components/ui/toast'
import { ClientIDActions } from '@/components/ClientIDActions'
import { getMyMembership } from '@/api/member'

const platformMeta: Record<string, { label: string; icon: React.ReactNode }> = {
  ios: { label: 'iOS', icon: <Apple className="size-4" /> },
  android: { label: 'Android', icon: <Smartphone className="size-4" /> },
  windows: { label: 'Windows', icon: <Monitor className="size-4" /> },
  macos: { label: 'macOS', icon: <Apple className="size-4" /> },
  linux: { label: 'Linux', icon: <Terminal className="size-4" /> },
  router: { label: '路由器', icon: <Wifi className="size-4" /> },
}

export default function DnsSetup() {
  const toast = useToast()
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['me', 'dns-config'],
    queryFn: getDnsConfig,
  })
  // 重置次数与自定义 ID 是否用过都要展示，用户才知道还剩几次机会
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: getMe })
  const { data: membership } = useQuery({ queryKey: ['membership'], queryFn: getMyMembership })

  // 指引文案里出现的地址都做成可点复制的芯片 ——
  // 用户照着步骤走的时候，最烦的就是回头去别处找那一串地址再手抄。
  const copyTokens = useMemo(() => {
    if (!data) return []
    return [
      data.doh_url,
      data.doh_url_path,
      data.dot_host && data.dot_port ? `${data.dot_host}:${data.dot_port}` : '',
      data.dot_host,
      data.plain_dns ?? '',
      data.domain,
    ].filter(Boolean) as string[]
  }, [data])

  // 安卓排第一：手机用户占绝大多数，进来先看到自己平台的步骤。
  // 只把 android 提前，其余保持后端给的顺序（iOS / Windows / macOS）。
  const guides = useMemo(() => {
    const list = data?.platform_guides ?? []
    return [...list].sort((a, b) => {
      const rank = (p: string) => (p.toLowerCase() === 'android' ? 0 : 1)
      return rank(a.platform) - rank(b.platform)
    })
  }, [data])

  return (
    <div className="space-y-6">
      <PageHeader
        title="我的 DNS 配置"
        description="复制下方地址，或扫描二维码快速接入你的设备"
        icon={<BookOpen className="size-5" />}
      />

      {isError ? (
        <ErrorState message={errorMessage(error)} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="grid gap-6 lg:grid-cols-3">
          <Skeleton className="h-72 rounded-2xl lg:col-span-2" />
          <Skeleton className="h-72 rounded-2xl" />
        </div>
      ) : data ? (
        <>

          {/* 分平台指引 */}
          {guides.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>分平台设置指引</CardTitle>
              </CardHeader>
              <CardContent>
                <Tabs defaultValue={(guides.find((g) => g.supported) ?? guides[0]).platform}>
                  <TabsList className="grid h-auto w-full grid-cols-2 gap-1 sm:inline-flex sm:w-auto sm:flex-wrap">
                    {guides.map((g) => {
                      const meta = platformMeta[g.platform.toLowerCase()]
                      return (
                        <TabsTrigger
                          key={g.platform}
                          value={g.platform}
                          className={cn(!g.supported && 'text-muted-foreground/60')}
                        >
                          {meta?.icon}
                          {meta?.label ?? g.platform}
                          {!g.supported && <span className="ml-1 text-[10px]">暂不支持</span>}
                        </TabsTrigger>
                      )
                    })}
                  </TabsList>
                  {guides.map((g) => (
                    <TabsContent key={g.platform} value={g.platform}>
                      {!g.supported && (
                        <p className="mb-2 rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning">
                          该平台需要的协议当前未开放
                        </p>
                      )}
                      <ol className="space-y-2">
                        {g.steps.map((step, i) => (
                          <li key={i} className="flex items-start gap-3">
                            <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary tabular-nums">
                              {i + 1}
                            </span>
                            <span className="min-w-0 pt-0.5 text-sm">
                              <HighlightStep text={step} tokens={copyTokens} />
                            </span>
                          </li>
                        ))}
                      </ol>

                      {/* iOS / macOS 原生支持 com.apple.dnsSettings.managed，
                          装了描述文件就系统级生效，不用逐台手填地址。
                          Windows / Android 没有等价机制，不给这个按钮。 */}
                      {(g.platform === 'ios' || g.platform === 'macos') && data.client_id && (
                        <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={async () => {
                              try {
                                await downloadMobileConfig(data.client_id)
                                toast.success('描述文件已下载', '打开后到「设置」里安装即可')
                              } catch (err) {
                                toast.error('下载失败', errorMessage(err))
                              }
                            }}
                          >
                            <Download />
                            下载 .mobileconfig 描述文件
                          </Button>
                          <span className="text-xs text-muted-foreground">
                            装上后系统级生效，适合多设备
                          </span>
                        </div>
                      )}
                    </TabsContent>
                  ))}
                </Tabs>
              </CardContent>
            </Card>
          )}

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
            {/* 地址列表 */}
            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Globe2 className="size-5 text-primary" />
                  接入地址
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-xs text-muted-foreground">客户端 ID</span>
                  <code className="rounded-md bg-muted px-2 py-1 font-mono text-[13px] font-medium">
                    {data.client_id}
                  </code>
                  {/* 重置入口就放在 ID 旁边：看到它的那一刻才最可能想改 */}
                  <ClientIDActions
                    clientID={data.client_id}
                    resetUsed={membership?.id_reset_count ?? me?.id_reset_count}
                    resetLimit={membership?.max_id_resets}
                    customUsed={membership?.custom_id_used ?? me?.custom_id_used}
                  />
                </div>

                {/* 管理员关掉的协议不下发地址，这里也就不显示 ——
                    没有地址可复制，用户就不会去配一个连不上的东西 */}
                {data.enable_doh && data.doh_url && (
                  <CopyField label="DoH（DNS over HTTPS）" value={data.doh_url} />
                )}
                {/* 证书没有通配符时主推地址本身就是路径式，两个值一样，
                    再列一遍只会让人以为哪里配错了 */}
                {data.enable_doh && data.doh_url_path && data.doh_url_path !== data.doh_url && (
                  <CopyField label="DoH（路径式，兼容不能改 Host 的客户端）" value={data.doh_url_path} />
                )}
                {data.enable_dot && data.dot_host && (
                  <CopyField label="DoT（DNS over TLS）" value={`${data.dot_host}:${data.dot_port}`} />
                )}
                {data.enable_plain_dns && data.plain_dns && (
                  <CopyField label="普通 DNS（UDP/TCP）" value={data.plain_dns} />
                )}
                {!data.enable_doh && !data.enable_dot && !data.enable_plain_dns && (
                  <p className="text-sm text-danger">管理员已关闭全部接入协议，请联系管理员。</p>
                )}
              </CardContent>
            </Card>

            {/* 二维码 */}
            <Card>
              <CardHeader>
                <CardTitle>扫码配置</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col items-center gap-4">
                {data.enable_doh && data.doh_url ? (
                  <>
                    <div className="rounded-2xl border border-border bg-white p-4 shadow-sm">
                      <QRCodeSVG value={data.doh_url} size={168} level="M" />
                    </div>
                    <p className="text-center text-xs text-muted-foreground">
                      使用支持 DoH 的客户端扫描
                      <br />
                      快速导入接入地址
                    </p>
                  </>
                ) : (
                  <p className="py-6 text-center text-xs text-muted-foreground">
                    DoH 未开启，
                    <br />
                    暂无可扫码的接入地址
                  </p>
                )}
              </CardContent>
            </Card>
          </div>

          {/* 证书指纹 */}
          {data.cert_fingerprint_sha256 && (
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Fingerprint className="size-5 text-primary" />
                  证书指纹（SHA-256）
                </CardTitle>
              </CardHeader>
              <CardContent>
                <CopyField value={data.cert_fingerprint_sha256} />
                <p className="mt-2 text-xs text-muted-foreground">
                  用于部分客户端的证书固定（pinning）校验，可确认连接未被中间人篡改。
                </p>
              </CardContent>
            </Card>
          )}

        </>
      ) : null}
    </div>
  )
}

/**
 * HighlightStep 把指引文案里的地址高亮成可点击复制的芯片。
 *
 * 为什么要在文本里做匹配而不是让后端返回结构化步骤：
 * 步骤本身是给人读的一句话（「填入：xxx（Android 使用 DoT）」），
 * 拆成「文字 + 地址 + 文字」三段既啰嗦又容易在改文案时漏掉一段。
 * 这里按已知地址做最长匹配，匹配到就渲染成按钮，其余原样输出。
 */
function HighlightStep({ text, tokens }: { text: string; tokens: string[] }) {
  const toast = useToast()
  const [copied, setCopied] = useState<string | null>(null)

  const parts = useMemo(() => {
    // 长 token 优先，避免主域名把子域名地址截断
    const list = [...new Set(tokens.filter(Boolean))].sort((a, b) => b.length - a.length)
    const out: { text: string; token?: string }[] = []
    let buf = ''
    let i = 0
    while (i < text.length) {
      const hit = list.find((t) => text.startsWith(t, i))
      if (hit) {
        if (buf) {
          out.push({ text: buf })
          buf = ''
        }
        out.push({ text: hit, token: hit })
        i += hit.length
        continue
      }
      buf += text[i]
      i++
    }
    if (buf) out.push({ text: buf })
    return out
  }, [text, tokens])

  const copyOne = async (v: string) => {
    try {
      await navigator.clipboard.writeText(v)
      setCopied(v)
      setTimeout(() => setCopied((c) => (c === v ? null : c)), 1600)
      toast.success('已复制', v)
    } catch {
      toast.error('复制失败', '请手动选中复制')
    }
  }

  return (
    <>
      {parts.map((p, i) =>
        p.token ? (
          <button
            key={i}
            type="button"
            onClick={() => copyOne(p.token!)}
            className="mx-0.5 inline-flex max-w-full items-baseline gap-1 rounded-md bg-primary/10 px-1.5 py-0.5 font-mono text-[12px] font-medium text-primary align-baseline transition-colors hover:bg-primary/20"
            title="点击复制"
          >
            <span className="break-all">{p.text}</span>
            {copied === p.token ? (
              <Check className="size-3 shrink-0" />
            ) : (
              <Copy className="size-3 shrink-0 opacity-60" />
            )}
          </button>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  )
}
