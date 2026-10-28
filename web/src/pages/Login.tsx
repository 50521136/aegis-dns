import { useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Eye, EyeOff, LogIn } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { TermsDialog } from '@/components/TermsDialog'
import { useAuth } from '@/hooks/useAuth'
import { useToast } from '@/components/ui/toast'
import { errorMessage, normalizeError } from '@/api/client'
import { getPublicConfig } from '@/api/member'
import { useQuery } from '@tanstack/react-query'
import { GeetestCaptcha, useCaptchaGate } from '@/components/GeetestCaptcha'

const schema = z.object({
  username: z.string().min(1, '请输入用户名'),
  password: z.string().min(1, '请输入密码'),
})

type FormValues = z.infer<typeof schema>

export default function Login() {
  const { login } = useAuth()
  const toast = useToast()
  // 极验配置：并进 /public/config，与注册页共用同一份缓存。
  const cfgQuery = useQuery({ queryKey: ['public', 'config'], queryFn: getPublicConfig, staleTime: 60000 })
  const gate = useCaptchaGate(cfgQuery.data?.captcha, 'login')
  const navigate = useNavigate()
  const location = useLocation()
  const [showPassword, setShowPassword] = useState(false)

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { username: '', password: '' } })

  const from = (location.state as { from?: string } | null)?.from || '/app'
  const cfg = cfgQuery.data
  // 登录场景只放勾选框，**不自动弹窗**：用户是回来用的，不是来签协议的，
  // 每次登录都糊一个弹窗等于给自己找骂。想看正文点链接即可。
  const [agreed, setAgreed] = useState(false)
  const [termsOpen, setTermsOpen] = useState(false)

  const onSubmit = async (values: FormValues) => {
    // 未勾选直接拦在前端：省一次请求，也避免把密码发上去再被 422 打回。
    if (cfg?.terms_enabled && !agreed) {
      toast.error('请先同意协议', `需要勾选同意《${cfg.terms_title || '用户协议'}》才能登录`)
      return
    }
    try {
      // 验证通过后才真正发登录请求：不需要验证时 gate.run 直接放行，
      // 不额外打扰用户。
      const u = await gate.run((captcha) =>
        login(values.username, values.password, captcha, cfg?.terms_enabled ? agreed : undefined),
      )
      toast.success('登录成功', `欢迎回来，${values.username}`)
      // 管理员默认进管理后台：他没有租户侧功能，落到 /app 会被 UserOnlyRoute 再弹一次
      const fallback = u.role === 'admin' ? '/admin' : '/app'
      navigate(from === '/app' ? fallback : from, { replace: true })
    } catch (err) {
      // 后端兜底：管理员可能刚打开协议开关，而页面配置还是旧的。
      if (normalizeError(err).code === 'TERMS_REQUIRED') {
        toast.error('请先同意协议', errorMessage(err))
        setTermsOpen(true)
        return
      }
      toast.error('登录失败', errorMessage(err))
    }
  }

  return (
    <div className="mx-auto flex w-full max-w-md flex-col justify-center px-4 py-14 md:py-20">
      <div className="text-center">
        <h1 className="text-3xl font-bold tracking-tight">欢迎回来</h1>
        <p className="mt-2 text-sm text-muted-foreground">登录以管理你的 DNS 规则与订阅</p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="mt-8 space-y-5" noValidate>
        <div className="space-y-2">
          <Label htmlFor="username">用户名</Label>
          <Input
            id="username"
            autoComplete="username"
            placeholder="请输入用户名"
            autoFocus
            {...register('username')}
          />
          {errors.username && <p className="text-xs text-danger">{errors.username.message}</p>}
        </div>

        <div className="space-y-2">
          <Label htmlFor="password">密码</Label>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? 'text' : 'password'}
              autoComplete="current-password"
              placeholder="请输入密码"
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

        {/* 协议勾选框：不弹窗，就摆在这里。后台没开时整块不渲染。 */}
        {cfg?.terms_enabled && (
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <Checkbox
              checked={agreed}
              onCheckedChange={(v) => setAgreed(v)}
              aria-label="我已阅读并同意"
            />
            <span className="min-w-0 leading-relaxed">
              我已阅读并同意{' '}
              <button
                type="button"
                onClick={(e) => {
                  e.preventDefault()
                  setTermsOpen(true)
                }}
                className="font-medium text-primary hover:underline"
              >
                《{cfg.terms_title || '用户协议'}》
              </button>
              {cfg.terms_version && (
                <span className="text-xs text-muted-foreground">（版本 {cfg.terms_version}）</span>
              )}
            </span>
          </label>
        )}

        <Button type="submit" className="w-full" size="lg" loading={isSubmitting}>
          {!isSubmitting && <LogIn />}
          登录
        </Button>
        {/* 极验宿主：bind 模式下不可见，提交时由 gate.run 触发弹窗 */}
        <GeetestCaptcha
          config={cfgQuery.data?.captcha}
          handleRef={gate.handleRef}
          onError={(m) => toast.error('验证组件异常', m)}
        />
      </form>

      <div className="mt-4 text-center">
        <Link
          to="/forgot-password"
          className="text-sm text-muted-foreground hover:text-foreground hover:underline"
        >
          忘记密码？
        </Link>
      </div>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        还没有账号？{' '}
        <Link to="/register" className="font-medium text-primary hover:underline">
          立即注册
        </Link>
      </p>

      {/* 只在用户点「《用户协议》」时才打开，不主动弹 */}
      {cfg?.terms_enabled && (
        <TermsDialog
          open={termsOpen}
          onOpenChange={setTermsOpen}
          title={cfg.terms_title || '用户协议'}
          version={cfg.terms_version}
          content={cfg.terms_content}
          agreed={agreed}
          onAgreedChange={setAgreed}
          onAgree={() => setTermsOpen(false)}
          declineText="关闭"
        />
      )}
    </div>
  )
}
