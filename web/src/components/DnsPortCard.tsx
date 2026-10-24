import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertTriangle, CheckCircle2, Loader2, Lock, RefreshCw, Save } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { checkDNSPort, getDNSPorts, updateDNSPort } from '@/api/scope'

/**
 * DnsPortCard 让管理员在面板里改 DoH（HTTPS）监听端口。
 *
 * 这个功能的风险比一般设置高得多：dnsd 一停，所有用这台 DNS 的设备
 * 立刻解析不了域名 —— 包括正在打开这个面板的浏览器。所以界面上的
 * 每一处设计都是为了「让管理员在动手前就知道会不会出事」：
 *
 *  - 输入停顿后自动探测占用，不用等点保存才知道冲突；
 *  - 把「谁占着这个端口」直接写出来，而不是只说「不可用」；
 *  - 明确列出不能用的端口及原因（DoT/明文/面板自己）；
 *  - 保存前二次确认，说清会重启 dnsd、断几秒。
 */

type PortsInfo = Awaited<ReturnType<typeof getDNSPorts>>
type CheckResult = { port: number; ok: boolean; current?: boolean; reason: string }

export function DnsPortCard() {
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [info, setInfo] = useState<PortsInfo | null>(null)
  const [port, setPort] = useState('')
  const [check, setCheck] = useState<CheckResult | null>(null)
  const [checking, setChecking] = useState(false)
  const timer = useRef<number | null>(null)

  const load = useCallback(async () => {
    try {
      const d = await getDNSPorts()
      setInfo(d)
      setPort(String(d.doh_port || ''))
    } catch (err) {
      toast.error('读取端口配置失败', errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 输入停顿 500ms 后自动探测，避免每敲一个数字就打一次请求
  useEffect(() => {
    if (loading || !port) return
    const n = Number(port)
    if (!Number.isInteger(n) || n < 1 || n > 65535) {
      setCheck({ port: n, ok: false, reason: '端口必须在 1-65535 之间' })
      return
    }
    if (info && n === info.doh_port) {
      setCheck({ port: n, ok: true, current: true, reason: '当前使用的端口' })
      return
    }
    if (timer.current) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(async () => {
      setChecking(true)
      try {
        const r = await checkDNSPort(n)
        setCheck(r)
      } catch (err) {
        setCheck({ port: n, ok: false, reason: errorMessage(err) })
      } finally {
        setChecking(false)
      }
    }, 500)
    return () => {
      if (timer.current) window.clearTimeout(timer.current)
    }
  }, [port, info, loading])

  const save = async () => {
    const n = Number(port)
    if (!check?.ok) {
      toast.error('端口不可用', check?.reason ?? '请先确认端口可用')
      return
    }
    if (info && n === info.doh_port) {
      toast.info('端口未变化')
      return
    }
    const ok = window.confirm(
      `确认把 DoH（HTTPS）端口改成 ${n}？\n\n` +
        `保存后会重启 dnsd，所有通过这台 DNS 解析的设备会中断 1~3 秒。\n` +
        `如果新端口在防火墙/安全组没有放行，外网用户将完全无法使用 DoH。\n\n` +
        `失败会自动回滚到原端口 ${info?.doh_port}。`,
    )
    if (!ok) return

    setSaving(true)
    try {
      const r = await updateDNSPort(n)
      toast.success('已生效', r.message)
      await load()
      setCheck(null)
    } catch (err) {
      // 服务端失败会带回滚说明，直接透出比「保存失败」有用得多
      toast.error('修改失败', errorMessage(err))
      await load()
    } finally {
      setSaving(false)
    }
  }

  if (loading) return null

  const changed = info ? Number(port) !== info.doh_port : false
  const conflicts = info
    ? [
        { label: '明文 UDP', port: parsePort(info.listen_udp) },
        { label: '明文 TCP', port: parsePort(info.listen_tcp) },
        { label: 'DoT', port: parsePort(info.listen_dot) },
        { label: '面板', port: parsePort(info.panel_listen) },
      ].filter((x) => x.port > 0)
    : []

  return (
    <Card>
      <CardHeader>
        <CardTitle>DNS 监听端口</CardTitle>
        <CardDescription>
          改完会重启 DNS 引擎生效。其它协议的端口请看下方只读值，本页只允许改 HTTPS（DoH）端口。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="doh_port" className="flex items-center gap-2">
            HTTPS / DoH 端口
            <Badge variant="outline">当前 {info?.doh_port || '未设置'}</Badge>
          </Label>
          <div className="flex items-center gap-2">
            <Input
              id="doh_port"
              inputMode="numeric"
              className="max-w-[10rem] font-mono"
              value={port}
              onChange={(e) => setPort(e.target.value.replace(/[^\d]/g, ''))}
            />
            <Button variant="secondary" size="sm" onClick={() => void load()} disabled={saving}>
              <RefreshCw />
              重新读取
            </Button>
          </div>

          {/* 探测结果：把结论和原因都写出来，不只给「可用/不可用」 */}
          {checking && (
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Loader2 className="size-3 animate-spin" />
              正在检查端口占用…
            </p>
          )}
          {!checking && check && (
            <p
              className={
                check.ok
                  ? 'flex items-start gap-1.5 text-xs text-success'
                  : 'flex items-start gap-1.5 text-xs text-danger'
              }
            >
              {check.ok ? (
                <CheckCircle2 className="mt-0.5 size-3.5 shrink-0" />
              ) : (
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              )}
              <span className="min-w-0 break-words">
                {check.current ? `${check.port} ${check.reason}` : check.reason}
              </span>
            </p>
          )}
        </div>

        <div className="rounded-lg bg-muted/40 p-2.5 text-xs text-muted-foreground">
          <p className="flex items-center gap-1.5 font-medium text-foreground">
            <Lock className="size-3.5" />
            不能使用以下端口
          </p>
          <ul className="mt-1 space-y-0.5 pl-5">
            {conflicts.map((c) => (
              <li key={c.label} className="list-disc">
                {c.port} —— {c.label}正在使用（同一进程内不能复用）
              </li>
            ))}
          </ul>
        </div>

        <div className="grid gap-2 text-xs sm:grid-cols-2">
          <div className="flex justify-between rounded-md bg-muted/30 px-2 py-1.5">
            <span className="text-muted-foreground">明文 UDP</span>
            <code className="mono-addr">{info?.listen_udp || '关闭'}</code>
          </div>
          <div className="flex justify-between rounded-md bg-muted/30 px-2 py-1.5">
            <span className="text-muted-foreground">明文 TCP</span>
            <code className="mono-addr">{info?.listen_tcp || '关闭'}</code>
          </div>
          <div className="flex justify-between rounded-md bg-muted/30 px-2 py-1.5">
            <span className="text-muted-foreground">DoT</span>
            <code className="mono-addr">{info?.listen_dot || '关闭'}</code>
          </div>
          <div className="flex justify-between rounded-md bg-muted/30 px-2 py-1.5">
            <span className="text-muted-foreground">DoH 明文（反代用）</span>
            <code className="mono-addr">{info?.listen_doh_plain || '关闭'}</code>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={save} loading={saving} disabled={!changed || !check?.ok}>
            <Save />
            保存并重启
          </Button>
          {changed && !check?.ok && (
            <span className="text-xs text-danger">端口不可用，无法保存</span>
          )}
          {!changed && <span className="text-xs text-muted-foreground">端口未改动</span>}
        </div>

        <p className="text-xs text-muted-foreground">
          提醒：改端口后必须在云厂商控制台的安全组里放行，否则只有本机能连。
          Android「私人 DNS」走 DoT 且端口固定 853，与本项无关。
        </p>
      </CardContent>
    </Card>
  )
}

function parsePort(addr: string | undefined): number {
  if (!addr) return 0
  const i = addr.lastIndexOf(':')
  if (i < 0) return 0
  const n = Number(addr.slice(i + 1))
  return Number.isInteger(n) ? n : 0
}
