import { motion } from 'framer-motion'
import { Moon, Sun } from 'lucide-react'
import { useTheme } from '@/hooks/useTheme'
import { Button } from '@/components/ui/button'

export function ThemeToggle({ className }: { className?: string }) {
  const { theme, toggleTheme } = useTheme()
  const isDark = theme === 'dark'
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={toggleTheme}
      className={className}
      aria-label={isDark ? '切换到浅色主题' : '切换到暗色主题'}
      title={isDark ? '浅色模式' : '暗色模式'}
    >
      <motion.span
        key={theme}
        initial={{ rotate: -90, opacity: 0, scale: 0.6 }}
        animate={{ rotate: 0, opacity: 1, scale: 1 }}
        transition={{ duration: 0.2 }}
        className="flex items-center justify-center"
      >
        {isDark ? <Sun className="size-5" /> : <Moon className="size-5" />}
      </motion.span>
    </Button>
  )
}
