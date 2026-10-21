import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Mail } from 'lucide-react'

import { changeEmail, getPublicConfig, sendEmailCode } from '@/api/member'
import { errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/toast'

/**
 * ChangeEmailForm 在设置页里换绑邮箱。
 *
 * 必须先验证「新邮箱真的能收到信」：不验证就改，
 * 打错一个字就把账号绑到一个不存在的地址，之后找回密码彻底没救。
 */
export function ChangeEmailForm({
  currentEmail,
  onChanged,
}: {
  currentEmail: string
  onChanged: () => void
}) {
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [sending, setSending] = useState(false)
  const [countdown, setCountdown] = useState(0)

  const cfgQuery = useQuery({ queryKey: ['public', 'config'], queryFn: getPublicConfig, staleTime: 60000 })
  const cfg = cfgQuery.data
  const allowedDomains = cfg?.email_domains ?? []
  // 配了白名单时把输入拆成「本地部分 + 固定后缀」，避免用户手打后缀打错
  const [localPart, setLocalPart] = useState('')
  const [emailDomain, setEmailDomain] = useState('')
  useEffect(() => {
    if (allowedDomains.length > 0 && !emailDomain) setEmailDomain(allowedDomains[0])
  }, [allowedDomains, emailDomain])

  useEffect(() => {
    if (countdown <= 0) return
    const t = setTimeout(() => setCountdown((c) => c - 1), 1000)
    return () => clearTimeout(t)
  }, [countdown])

  const send = async () => {
    if (!/^\S+@\S+\.\S+$/.test(email)) {
      toast.error('邮箱格式不正确')
      return
    }
    setSending(true)
    try {
      await sendEmailCode(email, 'change')
      toast.success('验证码已发出', `请查收 ${email}`)
      setCountdown(60)
    } catch (err) {
      toast.error('发送失败', errorMessage(err))
    } finally {
      setSending(false)
    }
  }

  const m = useMutation({
    mutationFn: () => changeEmail(email.trim().toLowerCase(), code.trim()),
    onSuccess: (res) => {
      toast.success('邮箱已更换', res.email)
      setEmail('')
      setCode('')
      onChanged()
    },
    onError: (err) => toast.error('更换失败', errorMessage(err)),
  })

  if (!cfg?.allow_email_change) return null

  return (
    <div className="space-y-3 rounded-xl border border-border/60 p-3">
      <div className="flex items-center gap-2">
        <Mail className="size-4 text-muted-foreground" />
        <p className="text-sm font-medium">更换邮箱</p>
      </div>
      <p className="text-xs text-muted-foreground">
        当前：{currentEmail || '未绑定'}。更换后需要新邮箱收到验证码才能生效。
      </p>
      {!cfg.email_available && (
        <p className="text-xs text-danger">本站未配置邮件服务，暂时无法更换邮箱。</p>
      )}
      <div className="space-y-2">
        <Label htmlFor="new-email">新邮箱</Label>
        {/* 后台配了允许后缀时，后缀同样只能选不能填 —— 换邮箱走的是同一套
            白名单校验，让用户手填一个必然被拒的地址没有意义 */}
        {allowedDomains.length > 0 && (
          <p className="text-xs text-muted-foreground">
            只接受：{allowedDomains.map((d) => '@' + d).join('、')}
          </p>
        )}
        <div className="flex gap-2">
          {/* 后缀内嵌在输入框右侧，和注册页一致。
              之前用的是 datalist：选一个后缀会把整个输入值替换成 "@qq.com"，
              前面填的用户名直接被吃掉 —— 这才是「选后缀把输入框填掉」的原因。 */}
          {allowedDomains.length > 0 ? (
            <div className="relative min-w-0 flex-1">
              <Input
                id="new-email"
                type="text"
                className="pr-[calc(0.75rem+var(--suffix-w,7rem))]"
                style={{
                  ['--suffix-w' as string]: `${Math.max(6, Math.max(...allowedDomains.map((d) => d.length)) + 2)}ch`,
                }}
                placeholder="用户名"
                value={localPart}
                onChange={(e) => {
                  const v = e.target.value.replace(/@.*$/, '')
                  setLocalPart(v)
                  setEmail(v ? `${v}@${emailDomain}` : '')
                }}
              />
              <div className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-muted-foreground">
                <span>@</span>
                {allowedDomains.length === 1 ? (
                  <span className="max-w-[9rem] truncate">{allowedDomains[0]}</span>
                ) : (
                  <select
                    className="pointer-events-auto max-w-[9rem] cursor-pointer appearance-none bg-transparent text-sm text-foreground outline-none"
                    value={emailDomain}
                    onChange={(e) => {
                      setEmailDomain(e.target.value)
                      setEmail(localPart ? `${localPart}@${e.target.value}` : '')
                    }}
                  >
                    {allowedDomains.map((d) => (
                      <option key={d} value={d}>
                        {d}
                      </option>
                    ))}
                  </select>
                )}
              </div>
            </div>
          ) : (
            <Input
              id="new-email"
              type="email"
              className="min-w-0 flex-1"
              placeholder="new@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          )}
          <Button
            variant="secondary"
            disabled={sending || countdown > 0 || !cfg.email_available}
            onClick={send}
          >
            {countdown > 0 ? `${countdown}s` : '发送验证码'}
          </Button>
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="email-code">验证码</Label>
        <Input
          id="email-code"
          className="font-mono tracking-widest"
          inputMode="numeric"
          maxLength={6}
          placeholder="6 位数字"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      </div>
      <Button
        size="sm"
        loading={m.isPending}
        disabled={!email || code.length < 4}
        onClick={() => m.mutate()}
      >
        确认更换
      </Button>
    </div>
  )
}
