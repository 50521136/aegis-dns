import { useCallback, useEffect, useRef, useState } from 'react'
import { ShieldCheck } from 'lucide-react'
import type { CaptchaResult, CaptchaScene } from '@/api/types'
import { cn } from '@/lib/utils'

/**
 * 极验 v4 行为验证。
 *
 * 用 `product: 'bind'`（隐藏按钮式）而不是 float/popup：
 * float 会在页面上渲染一个独立的「点击验证」按钮，用户必须**先点它再点提交** ——
 * 一次操作被拆成两个按钮，实际用起来很别扭，而且很多人会直接点提交、
 * 然后以为是表单坏了。bind 模式允许我们在提交动作里调 showCaptcha()，
 * 用户看到的仍是「填完点提交 → 弹出验证 → 通过后自动继续」。
 *
 * 三条实现要点：
 *  1. **脚本只加载一次**。多次 append <script> 会重复定义 initGeetest4。
 *  2. **卸载时 destroy**。不销毁的话弹窗 DOM 会留在 body 上，
 *     路由切走后还能看到幽灵遮罩。
 *  3. **失败要能看见**。脚本加载失败时给一句明确的话，而不是留一个
 *     永远转圈的空框 —— 用户会以为是自己网络的问题。
 */

declare global {
  interface Window {
    initGeetest4?: (config: Record<string, unknown>, callback: (obj: GeetestInstance) => void) => void
  }
}

interface GeetestInstance {
  getValidate: () => CaptchaResult | false
  onReady: (cb: () => void) => GeetestInstance
  onSuccess: (cb: () => void) => GeetestInstance
  onError: (cb: (err: { code?: string; msg?: string }) => void) => GeetestInstance
  onClose: (cb: () => void) => GeetestInstance
  showCaptcha: () => void
  reset: () => void
  destroy: () => void
}

const SCRIPT_SRC = 'https://static.geetest.com/v4/gt4.js'
let scriptPromise: Promise<void> | null = null

/** 加载极验脚本（只加载一次）。 */
function loadGeetestScript(): Promise<void> {
  if (typeof window !== 'undefined' && window.initGeetest4) {
    return Promise.resolve()
  }
  if (scriptPromise) return scriptPromise

  scriptPromise = new Promise<void>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(`script[src="${SCRIPT_SRC}"]`)
    if (existing) {
      existing.addEventListener('load', () => resolve())
      existing.addEventListener('error', () => reject(new Error('极验脚本加载失败')))
      return
    }
    const el = document.createElement('script')
    el.src = SCRIPT_SRC
    el.async = true
    el.onload = () => resolve()
    el.onerror = () => {
      // 失败后清掉缓存的 promise，否则重试永远拿到同一个 rejected promise。
      scriptPromise = null
      reject(new Error('极验脚本加载失败'))
    }
    document.head.appendChild(el)
  })
  return scriptPromise
}

/** 极验配置（由 /public/config 下发）。 */
export interface CaptchaConfig {
  enabled: boolean
  captcha_id: string
  scenes?: Partial<Record<CaptchaScene, boolean>>
}

/** 暴露给业务代码的验证句柄。 */
export interface CaptchaHandle {
  /** 主动弹出验证。用户通过后 resolve 四个字段。 */
  show: () => Promise<CaptchaResult>
  /** 验证是否已就绪（脚本加载完成、实例可用） */
  isReady: () => boolean
}

export interface GeetestCaptchaProps {
  config: CaptchaConfig | undefined
  /** 由 useCaptchaGate 提供，组件把句柄注册进来 */
  handleRef: React.MutableRefObject<CaptchaHandle | null>
  onError?: (message: string) => void
  className?: string
}

/**
 * 极验验证的宿主组件：本身不渲染按钮，只负责初始化实例并注册句柄。
 *
 * 挂在表单所在页面即可（不可见），实际交互由提交动作触发。
 */
export function GeetestCaptcha({ config, handleRef, onError, className }: GeetestCaptchaProps) {
  const [failed, setFailed] = useState(false)
  const instanceRef = useRef<GeetestInstance | null>(null)
  const readyRef = useRef(false)
  const pendingRef = useRef<{
    resolve: (v: CaptchaResult) => void
    reject: (e: Error) => void
  } | null>(null)

  // 回调放进 ref：父组件每次渲染都会传新函数，直接进依赖会导致
  // 验证组件被反复重建（用户滑到一半被销毁）。
  const errRef = useRef(onError)
  errRef.current = onError

  const captchaId = config?.enabled ? config.captcha_id : ''

  useEffect(() => {
    let disposed = false
    readyRef.current = false
    instanceRef.current = null
    handleRef.current = null

    if (!captchaId) return

    async function boot() {
      try {
        await loadGeetestScript()
      } catch (err) {
        if (!disposed) {
          setFailed(true)
          errRef.current?.(err instanceof Error ? err.message : '验证组件加载失败')
        }
        return
      }
      if (disposed || !window.initGeetest4) return

      window.initGeetest4(
        {
          captchaId,
          product: 'bind',
          language: 'zho',
          hideBar: ['close'],
          // 8 秒：极验接口抖动时不能让用户一直转圈。
          timeout: 8000,
        },
        (obj) => {
          if (disposed) {
            obj.destroy()
            return
          }
          instanceRef.current = obj
          obj
            .onReady(() => {
              if (disposed) return
              readyRef.current = true
              // 实例就绪后才把句柄交出去：过早暴露会让 show() 静默无反应。
              handleRef.current = {
                isReady: () => readyRef.current,
                show: () =>
                  new Promise<CaptchaResult>((resolve, reject) => {
                    if (!readyRef.current || !instanceRef.current) {
                      reject(new Error('验证组件尚未就绪，请稍后重试'))
                      return
                    }
                    pendingRef.current = { resolve, reject }
                    instanceRef.current.showCaptcha()
                  }),
              }
            })
            .onSuccess(() => {
              const v = obj.getValidate()
              if (v) {
                pendingRef.current?.resolve(v)
                pendingRef.current = null
                // 成功后重置，让下一次提交还能重新验证（lot_number 一次性）。
                obj.reset()
              }
            })
            .onError((e) => {
              pendingRef.current?.reject(new Error(e?.msg || '验证出错，请重试'))
              pendingRef.current = null
              errRef.current?.(e?.msg || '验证出错，请重试')
            })
            .onClose(() => {
              pendingRef.current?.reject(new Error('已取消验证'))
              pendingRef.current = null
            })
        },
      )
    }

    void boot()

    return () => {
      disposed = true
      pendingRef.current?.reject(new Error('验证组件已卸载'))
      pendingRef.current = null
      readyRef.current = false
      handleRef.current = null
      // 不销毁会把弹窗 DOM 留在 body 上，路由切走后仍有幽灵遮罩。
      try {
        instanceRef.current?.destroy()
      } catch {
        // 极验内部状态已失效时 destroy 会抛，忽略即可。
      }
      instanceRef.current = null
    }
  }, [captchaId, handleRef])

  if (!captchaId) return null

  if (failed) {
    return (
      <div
        className={cn(
          'rounded-xl border border-warn/40 bg-warn/8 px-3 py-2 text-xs text-muted-foreground',
          className,
        )}
      >
        验证组件加载失败，可稍后重试；如持续失败请刷新页面。
      </div>
    )
  }

  // 不渲染可见元素：bind 模式下验证界面由 showCaptcha() 弹出。
  // 这里留一个隐藏容器，便于极验把弹窗挂到组件树附近（而不是 body 末尾）。
  return <div className="hidden" aria-hidden="true" />
}

/**
 * 验证门：把「要不要验 / 怎么验」收敛成一个函数。
 *
 * 用法：
 *   const gate = useCaptchaGate(captchaConfig, 'login')
 *   ...
 *   await gate.run(async (captcha) => api.login({ ...form, captcha }))
 *
 * 关键点：`run` 在不需要验证时**不打扰用户**（直接提交，captcha 为 undefined），
 * 需要验证时才弹出。这样同一份表单代码在开/关验证两种配置下都能用。
 */
export function useCaptchaGate(config: CaptchaConfig | undefined, scene: CaptchaScene) {
  const handleRef = useRef<CaptchaHandle | null>(null)
  const required = Boolean(config?.enabled && config?.scenes?.[scene])

  const run = useCallback(
    async <T,>(submit: (captcha?: CaptchaResult) => Promise<T>): Promise<T> => {
      if (!required) return submit(undefined)
      const handle = handleRef.current
      if (!handle) {
        // 配置说要验但实例还没起来（脚本还在加载）。这种情况宁可报错
        // 也不能静默放行 —— 否则「开了验证但没生效」会长期没人发现。
        throw new Error('验证组件尚未就绪，请稍后重试')
      }
      const result = await handle.show()
      return submit(result)
    },
    [required],
  )

  return { required, run, handleRef }
}
