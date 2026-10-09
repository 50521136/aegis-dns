import { useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Eye, EyeOff, LogIn } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '@/hooks/useAuth'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'

const schema = z.object({
  username: z.string().min(1, '请输入用户名'),
  password: z.string().min(1, '请输入密码'),
})

type FormValues = z.infer<typeof schema>

export default function Login() {
  const { login } = useAuth()
  const toast = useToast()
  const navigate = useNavigate()
  const location = useLocation()
  const [showPassword, setShowPassword] = useState(false)

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { username: '', password: '' } })

  const from = (location.state as { from?: string } | null)?.from || '/app'

  const onSubmit = async (values: FormValues) => {
    try {
      await login(values.username, values.password)
      toast.success('登录成功', `欢迎回来，${values.username}`)
      navigate(from, { replace: true })
    } catch (err) {
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

        <Button type="submit" className="w-full" size="lg" loading={isSubmitting}>
          {!isSubmitting && <LogIn />}
          登录
        </Button>
      </form>

      <p className="mt-6 text-center text-sm text-muted-foreground">
        还没有账号？{' '}
        <Link to="/register" className="font-medium text-primary hover:underline">
          立即注册
        </Link>
      </p>
    </div>
  )
}
