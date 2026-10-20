import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ArrowLeft, KeyRound, Mail } from 'lucide-react'

import { forgotPassword, sendEmailCode } from '@/api/member'
import { errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/toast'

const schema = z.object({
  email: z.string().email('请输入有效的邮箱地址'),
  code: z.string().min(4, '请输入收到的验证码'),
  password: z
    .string()
    .min(8, '密码至少 8 个字符')
    .regex(/[A-Za-z]/, '密码需包含字母')
    .regex(/[0-9]/, '密码需包含数字'),
})

type FormValues = z.infer<typeof schema>

/**
 * ForgotPassword 用邮箱验证码重置密码。
 *
 * 不提示「该邮箱是否注册过」：这个页面在公网上，
 * 任何能试出差异的反馈都会变成账号枚举工具。
 */
export default function ForgotPassword() {
  const toast = useToast()
  const navigate = useNavigate()
  const [sending, setSending] = useState(false)
  const [countdown, setCountdown] = useState(0)

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: '', code: '', password: '' },
  })

  const email = watch('email')

  useEffect(() => {
    if (countdown <= 0) return
    const t = setTimeout(() => setCountdown((c) => c - 1), 1000)
    return () => clearTimeout(t)
  }, [countdown])

  const sendCode = async () => {
    if (!email || !/^\S+@\S+\.\S+$/.test(email)) {
      toast.error('请先填写有效邮箱')
      return
    }
    setSending(true)
    try {
      const res = await sendEmailCode(email, 'reset')
      toast.success('如果该邮箱已注册', res.message ?? '验证码已发出')
      setCountdown(60)
    } catch (err) {
      toast.error('发送失败', errorMessage(err))
    } finally {
      setSending(false)
    }
  }

  const onSubmit = async (values: FormValues) => {
    try {
      await forgotPassword(values.email, values.code, values.password)
      toast.success('密码已重置', '请用新密码登录')
      navigate('/login', { replace: true })
    } catch (err) {
      toast.error('重置失败', errorMessage(err))
    }
  }

  return (
    <div className="mx-auto w-full max-w-md">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="size-5 text-primary" />
            找回密码
          </CardTitle>
          <CardDescription>通过注册邮箱接收验证码，验证后设置新密码</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="email">注册邮箱</Label>
              <div className="flex gap-2">
                <Input
                  id="email"
                  type="email"
                  autoComplete="email"
                  placeholder="you@example.com"
                  className="min-w-0 flex-1"
                  {...register('email')}
                />
                <Button
                  type="button"
                  variant="secondary"
                  disabled={sending || countdown > 0}
                  onClick={sendCode}
                >
                  <Mail />
                  {countdown > 0 ? `${countdown}s` : '发送验证码'}
                </Button>
              </div>
              {errors.email && <p className="text-xs text-danger">{errors.email.message}</p>}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="code">验证码</Label>
              <Input
                id="code"
                className="font-mono tracking-widest"
                inputMode="numeric"
                maxLength={6}
                placeholder="6 位数字"
                autoComplete="one-time-code"
                {...register('code')}
              />
              {errors.code && <p className="text-xs text-danger">{errors.code.message}</p>}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="password">新密码</Label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                placeholder="至少 8 位，含字母和数字"
                {...register('password')}
              />
              {errors.password && <p className="text-xs text-danger">{errors.password.message}</p>}
            </div>

            <Button type="submit" className="w-full" loading={isSubmitting}>
              重置密码
            </Button>

            <Link
              to="/login"
              className="flex items-center justify-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
            >
              <ArrowLeft className="size-4" />
              返回登录
            </Link>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
