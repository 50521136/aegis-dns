import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { QRCodeSVG } from 'qrcode.react'
import { ArrowRight, CheckCircle2, Eye, EyeOff, PartyPopper } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { CopyField } from '@/components/CopyField'
import { getPublicConfig, sendEmailCode } from '@/api/member'
import { useAuth } from '@/hooks/useAuth'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import type { DnsConfig } from '@/api/types'

const schema = z.object({
  username: z
    .string()
    .min(3, '用户名至少 3 个字符')
    .max(32, '用户名最多 32 个字符')
    .regex(/^[a-zA-Z0-9_-]+$/, '仅支持字母、数字、下划线与连字符'),
  email: z.string().email('请输入有效的邮箱地址'),
  invite_code: z.string().optional(),
  email_code: z.string().optional(),
  password: z
    .string()
    .min(8, '密码至少 8 个字符')
    .regex(/[A-Za-z]/, '密码需包含字母')
    .regex(/[0-9]/, '密码需包含数字'),
})

type FormValues = z.infer<typeof schema>

export default function Register() {
  const { register: registerUser } = useAuth()
  const toast = useToast()
  const navigate = useNavigate()
  const [showPassword, setShowPassword] = useState(false)
  const [dnsConfig, setDnsConfig] = useState<DnsConfig | null>(null)
  const {
    register,
    handleSubmit,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { username: '', email: '', password: '', invite_code: '', email_code: '' },
  })

  // 公开配置决定表单要显示哪些字段：不要邀请码的站点不该摆一个空框
  const cfgQuery = useQuery({ queryKey: ['public', 'config'], queryFn: getPublicConfig, staleTime: 60000 })
  const cfg = cfgQuery.data
  const [sending, setSending] = useState(false)
  const [countdown, setCountdown] = useState(0)
  const emailValue = watch('email')

  // 倒计时：不加的话用户会连点发送，然后被后端限流报错
  useEffect(() => {
    if (countdown <= 0) return
    const t = setTimeout(() => setCountdown((c) => c - 1), 1000)
    return () => clearTimeout(t)
  }, [countdown])

  const sendCode = async () => {
    if (!emailValue || !/^\S+@\S+\.\S+$/.test(emailValue)) {
      toast.error('请先填写有效邮箱', '验证码会发到这个地址')
      return
    }
    setSending(true)
    try {
      await sendEmailCode(emailValue, 'register')
      toast.success('验证码已发出', `请查收 ${emailValue}`)
      setCountdown(60)
    } catch (err) {
      toast.error('发送失败', errorMessage(err))
    } finally {
      setSending(false)
    }
  }


  const onSubmit = async (values: FormValues) => {
    try {
      const res = await registerUser({
        username: values.username,
        email: values.email,
        password: values.password,
        invite_code: values.invite_code?.trim() || undefined,
        email_code: values.email_code?.trim() || undefined,
      })
      toast.success('注册成功', '你的专属 DNS 地址已生成')
      if (res.dns_config) {
        setDnsConfig(res.dns_config)
      } else {
        toast.info('注册成功', '请前往「我的 DNS」查看接入地址')
        navigate('/app/setup', { replace: true })
      }
    } catch (err) {
      toast.error('注册失败', errorMessage(err))
    }
  }

  if (dnsConfig) {
    return (
      <div className="mx-auto flex w-full max-w-2xl flex-col px-4 py-12 md:py-16">
        <div className="flex flex-col items-center text-center">
          <div className="flex size-16 items-center justify-center rounded-2xl bg-success/12 text-success">
            <PartyPopper className="size-8" />
          </div>
          <h1 className="mt-5 text-3xl font-bold tracking-tight">注册成功 🎉</h1>
          <p className="mt-2 max-w-md text-sm text-muted-foreground">
            你的专属 DNS 地址已生成。把它填入设备即可享受加密、无广告的解析体验。
          </p>
        </div>

        <div className="mt-8 rounded-2xl border border-border/50 bg-card p-6 shadow-sm">
          <div className="grid gap-6 md:grid-cols-[auto_1fr] md:items-center">
            <div className="mx-auto rounded-xl border border-border bg-white p-3">
              <QRCodeSVG value={dnsConfig.doh_url} size={132} level="M" />
            </div>
            <div className="min-w-0 space-y-4">
              <CopyField label="DoH 地址" value={dnsConfig.doh_url} />
              <CopyField label="DoT 主机" value={`${dnsConfig.dot_host}:${dnsConfig.dot_port}`} />
              {dnsConfig.plain_dns && <CopyField label="普通 DNS" value={dnsConfig.plain_dns} />}
            </div>
          </div>
        </div>

        <div className="mt-6 flex flex-col gap-3 sm:flex-row">
          <Button className="flex-1" size="lg" onClick={() => navigate('/app/setup')}>
            <CheckCircle2 />
            查看完整配置指引
          </Button>
          <Button variant="outline" size="lg" onClick={() => navigate('/app')}>
            进入控制台
            <ArrowRight />
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-md flex-col justify-center px-4 py-14 md:py-20">
      <div className="text-center">
        <h1 className="text-3xl font-bold tracking-tight">创建账号</h1>
        <p className="mt-2 text-sm text-muted-foreground">注册后自动分配专属 DNS 子域名</p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="mt-8 space-y-5" noValidate>
        <div className="space-y-2">
          <Label htmlFor="username">用户名</Label>
          <Input id="username" autoComplete="username" placeholder="例如 alice" autoFocus {...register('username')} />
          {errors.username && <p className="text-xs text-danger">{errors.username.message}</p>}
        </div>

        <div className="space-y-2">
          <Label htmlFor="email">邮箱</Label>
          <Input id="email" type="email" autoComplete="email" placeholder="you@example.com" {...register('email')} />
          {errors.email && <p className="text-xs text-danger">{errors.email.message}</p>}
        </div>

        {/* 开启注册邮箱验证时才出现验证码字段 */}
        {cfg?.require_email_verify && (
          <div className="space-y-1.5">
            <Label htmlFor="email_code">邮箱验证码</Label>
            <div className="flex gap-2">
              <Input
                id="email_code"
                className="min-w-0 flex-1 font-mono tracking-widest"
                inputMode="numeric"
                maxLength={6}
                placeholder="6 位数字"
                autoComplete="one-time-code"
                {...register('email_code')}
              />
              <Button
                type="button"
                variant="secondary"
                disabled={sending || countdown > 0 || !cfg.email_available}
                onClick={sendCode}
              >
                {countdown > 0 ? `${countdown}s` : sending ? '发送中' : '获取验证码'}
              </Button>
            </div>
            {!cfg.email_available && (
              <p className="text-xs text-danger">本站未配置邮件服务，暂时无法注册</p>
            )}
          </div>
        )}

        {/* 需要邀请码时才出现 */}
        {cfg?.require_invite && (
          <div className="space-y-1.5">
            <Label htmlFor="invite_code">邀请码</Label>
            <Input
              id="invite_code"
              className="font-mono uppercase"
              placeholder="请输入邀请码"
              autoComplete="off"
              {...register('invite_code')}
            />
            <p className="text-xs text-muted-foreground">邀请码由管理员或已注册用户提供</p>
          </div>
        )}

        <div className="space-y-2">
          <Label htmlFor="password">密码</Label>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? 'text' : 'password'}
              autoComplete="new-password"
              placeholder="至少 8 位，含字母和数字"
              className="pr-11"
              {...register('password')}
            />
            <button
              type="button"
              onClick={() => setShowPassword((s) => !s)}
              className="absolute right-1 top-1 flex size-8 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-muted"
              aria-label={showPassword ? '隐藏密码' : '显示密码'}
            >
              {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </button>
          </div>
          {errors.password && <p className="text-xs text-danger">{errors.password.message}</p>}
        </div>

        <Button type="submit" className="w-full" size="lg" loading={isSubmitting}>
          注册
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        已有账号？{' '}
        <Link to="/login" className="font-medium text-primary hover:underline">
          去登录
        </Link>
      </p>
    </div>
  )
}
