import * as React from 'react'
import { createContext, useCallback, useContext, useMemo, useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'

export type ToastVariant = 'default' | 'success' | 'error' | 'warning' | 'info'

export interface ToastItem {
  id: number
  title: string
  description?: string
  variant: ToastVariant
  duration: number
}

interface ToastContextValue {
  toast: (t: Omit<ToastItem, 'id' | 'duration'> & { duration?: number }) => number
  success: (title: string, description?: string) => number
  error: (title: string, description?: string) => number
  warning: (title: string, description?: string) => number
  info: (title: string, description?: string) => number
  dismiss: (id: number) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

let seq = 0

const iconMap: Record<ToastVariant, React.ReactNode> = {
  default: <Info className="size-5 text-primary" />,
  info: <Info className="size-5 text-accent" />,
  success: <CheckCircle2 className="size-5 text-success" />,
  error: <XCircle className="size-5 text-danger" />,
  warning: <AlertTriangle className="size-5 text-warning" />,
}

const accentMap: Record<ToastVariant, string> = {
  default: 'border-l-primary',
  info: 'border-l-accent',
  success: 'border-l-success',
  error: 'border-l-danger',
  warning: 'border-l-warning',
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])

  const dismiss = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const toast = useCallback<ToastContextValue['toast']>(
    ({ title, description, variant = 'default', duration = 4000 }) => {
      const id = ++seq
      setItems((prev) => [...prev.slice(-4), { id, title, description, variant, duration }])
      if (duration > 0) {
        setTimeout(() => dismiss(id), duration)
      }
      return id
    },
    [dismiss],
  )

  const value = useMemo<ToastContextValue>(
    () => ({
      toast,
      dismiss,
      success: (title, description) => toast({ title, description, variant: 'success' }),
      error: (title, description) => toast({ title, description, variant: 'error', duration: 6000 }),
      warning: (title, description) => toast({ title, description, variant: 'warning' }),
      info: (title, description) => toast({ title, description, variant: 'info' }),
    }),
    [toast, dismiss],
  )

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="pointer-events-none fixed inset-x-0 top-0 z-[100] flex flex-col items-center gap-2 p-4 sm:inset-x-auto sm:right-0 sm:items-end"
        role="region"
        aria-label="通知"
      >
        <AnimatePresence initial={false}>
          {items.map((t) => (
            <motion.div
              key={t.id}
              layout
              initial={{ opacity: 0, y: -12, scale: 0.97 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, y: -8, scale: 0.97 }}
              transition={{ duration: 0.2, ease: 'easeOut' }}
              className={cn(
                'pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-xl border border-border border-l-4 bg-card p-4 shadow-lg',
                accentMap[t.variant],
              )}
              role="status"
            >
              <div className="mt-0.5 shrink-0">{iconMap[t.variant]}</div>
              <div className="min-w-0 flex-1">
                <p className="text-sm font-semibold leading-snug">{t.title}</p>
                {t.description && (
                  <p className="mt-1 break-words text-sm text-muted-foreground">{t.description}</p>
                )}
              </div>
              <button
                onClick={() => dismiss(t.id)}
                className="flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-muted"
                aria-label="关闭通知"
              >
                <X className="size-4" />
              </button>
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast 必须在 <ToastProvider> 内部使用')
  return ctx
}
