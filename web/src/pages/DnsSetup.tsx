import { useQuery } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import {
  Apple,
  BookOpen,
  Fingerprint,
  Globe2,
  Monitor,
  Smartphone,
  Terminal,
  Wifi,
} from 'lucide-react'
import { getDnsConfig } from '@/api/me'
import { PageHeader } from '@/components/PageHeader'
import { CopyField } from '@/components/CopyField'
import { ErrorState } from '@/components/ErrorState'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/api/client'

const platformMeta: Record<string, { label: string; icon: React.ReactNode }> = {
  ios: { label: 'iOS', icon: <Apple className="size-4" /> },
  android: { label: 'Android', icon: <Smartphone className="size-4" /> },
  windows: { label: 'Windows', icon: <Monitor className="size-4" /> },
  macos: { label: 'macOS', icon: <Apple className="size-4" /> },
  linux: { label: 'Linux', icon: <Terminal className="size-4" /> },
  router: { label: '路由器', icon: <Wifi className="size-4" /> },
}

export default function DnsSetup() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['me', 'dns-config'],
    queryFn: getDnsConfig,
  })

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
                </div>

                <CopyField label="DoH（DNS over HTTPS）" value={data.doh_url} />
                <CopyField label="DoH（路径式，兼容不能改 Host 的客户端）" value={data.doh_url_path} />
                <CopyField label="DoT（DNS over TLS）" value={`${data.dot_host}:${data.dot_port}`} />
                {data.plain_dns && <CopyField label="普通 DNS（UDP/TCP）" value={data.plain_dns} />}
              </CardContent>
            </Card>

            {/* 二维码 */}
            <Card>
              <CardHeader>
                <CardTitle>扫码配置</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col items-center gap-4">
                <div className="rounded-2xl border border-border bg-white p-4 shadow-sm">
                  <QRCodeSVG value={data.doh_url} size={168} level="M" />
                </div>
                <p className="text-center text-xs text-muted-foreground">
                  使用支持 DoH 的客户端扫描
                  <br />
                  快速导入接入地址
                </p>
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

          {/* 分平台指引 */}
          {data.platform_guides && data.platform_guides.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>分平台设置指引</CardTitle>
              </CardHeader>
              <CardContent>
                <Tabs defaultValue={data.platform_guides[0].platform}>
                  <TabsList className="flex-wrap">
                    {data.platform_guides.map((g) => {
                      const meta = platformMeta[g.platform.toLowerCase()]
                      return (
                        <TabsTrigger key={g.platform} value={g.platform}>
                          {meta?.icon}
                          {meta?.label ?? g.platform}
                        </TabsTrigger>
                      )
                    })}
                  </TabsList>
                  {data.platform_guides.map((g) => (
                    <TabsContent key={g.platform} value={g.platform}>
                      <ol className="space-y-2">
                        {g.steps.map((step, i) => (
                          <li key={i} className="flex items-start gap-3">
                            <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary tabular-nums">
                              {i + 1}
                            </span>
                            <span className="pt-0.5 text-sm">{step}</span>
                          </li>
                        ))}
                      </ol>
                    </TabsContent>
                  ))}
                </Tabs>
              </CardContent>
            </Card>
          )}
        </>
      ) : null}
    </div>
  )
}
