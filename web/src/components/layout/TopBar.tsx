import { useNavigate } from 'react-router-dom'
import { LogOut, Menu, User as UserIcon } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'
import { useToast } from '@/components/ui/toast'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { ThemeToggle } from './ThemeToggle'

interface TopBarProps {
  title?: string
  onOpenNav?: () => void
}

export function TopBar({ title, onOpenNav }: TopBarProps) {
  const { user, logout } = useAuth()
  const toast = useToast()
  const navigate = useNavigate()

  const handleLogout = async () => {
    try {
      await logout()
      toast.success('已退出登录')
      navigate('/login')
    } catch {
      toast.error('退出失败', '请重试')
    }
  }

  return (
    <header className="sticky top-0 z-30 flex h-16 items-center gap-3 border-b border-border bg-background/85 px-4 backdrop-blur-lg md:px-6">
      {onOpenNav && (
        <Button
          variant="ghost"
          size="icon"
          className="md:hidden"
          onClick={onOpenNav}
          aria-label="打开菜单"
        >
          <Menu />
        </Button>
      )}

      <div className="min-w-0 flex-1">
        {title && <h2 className="truncate text-lg font-semibold">{title}</h2>}
      </div>

      <ThemeToggle />

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            className="flex size-10 items-center justify-center rounded-xl bg-primary/15 text-sm font-semibold text-primary uppercase transition-colors hover:bg-primary/20 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            aria-label="账户菜单"
          >
            {user?.username?.slice(0, 1) ?? <UserIcon className="size-5" />}
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuLabel>
            <div className="space-y-0.5">
              <p className="text-sm font-semibold text-foreground">{user?.username}</p>
              <p className="text-xs font-normal text-muted-foreground">{user?.email || '未绑定邮箱'}</p>
            </div>
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => navigate('/app/settings')}>
            <UserIcon />
            账号设置
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={handleLogout}
            className="text-danger focus:bg-danger/10 focus:text-danger"
          >
            <LogOut />
            退出登录
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  )
}
