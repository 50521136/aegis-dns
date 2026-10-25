import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { AlertTriangle, RefreshCw, Home } from 'lucide-react'

import { Button } from '@/components/ui/button'

/**
 * ErrorBoundary 拦住「某个页面渲染时抛异常」导致整站白屏。
 *
 * 为什么必须有：React 的错误边界是唯一能捕获渲染期异常的手段，而此前项目里
 * 只有 ErrorState —— 它处理的是**请求错误**（useQuery 的 isError），接不住
 * 「组件渲染时读到 undefined 而抛 TypeError」这类问题。没有边界时一个页面
 * 崩掉会卸载整个组件树，用户看到的是纯白页面，连侧边栏都没了，只能自己
 * 猜要不要刷新；而真实原因只留在浏览器控制台里，用户不会去看。
 *
 * 设计取舍：
 *
 *  1. **边界放在路由内容层，不放在最外层**。放在 App 根部的话，一旦页面崩了
 *     连导航都没了，用户只能刷新页面；放在内容区里，顶栏与侧边栏还在，
 *     可以直接点别的页面继续用。
 *  2. **按 pathname 重置**。否则用户在 /app/rules 崩了之后点「订阅」，
 *     边界仍然记着错误、继续显示错误页 —— 表现像「整个后台都坏了」。
 *  3. **不做自动重试**。渲染期异常通常是确定性的（数据形状不对），
 *     自动重试只会闪来闪去。给一个「重试」按钮，让用户在数据变化后手动点。
 *  4. **错误详情折叠起来但保留**。用户不需要看堆栈，但报障时能用上；
 *     直接打印在页面上会把界面变得像崩溃报告。
 */
interface ErrorBoundaryProps {
  children: ReactNode
  /** 用于在路由变化时重置边界状态 */
  resetKey?: string
}

interface ErrorBoundaryState {
  error: Error | null
  info: ErrorInfo | null
  showDetail: boolean
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null, info: null, showDetail: false }

  static getDerivedStateFromError(error: Error): Partial<ErrorBoundaryState> {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 留在控制台：用户报障时让它打开控制台截图，是唯一能拿到堆栈的途径。
    console.error('[ErrorBoundary] 页面渲染失败', error, info.componentStack)
    this.setState({ info })
  }

  componentDidUpdate(prev: ErrorBoundaryProps) {
    // 路由变了就复位：否则一处崩溃会让后续所有页面都显示错误页。
    if (this.state.error && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null, info: null, showDetail: false })
    }
  }

  render() {
    const { error, info, showDetail } = this.state
    if (!error) return this.props.children

    return (
      <div className="m-surface flex flex-col items-start gap-4 rounded-2xl p-6">
        <div className="flex items-center gap-2.5">
          <span className="flex size-9 items-center justify-center rounded-xl bg-danger/12 text-danger">
            <AlertTriangle className="size-5" />
          </span>
          <div>
            <h2 className="text-lg font-semibold leading-tight">这个页面没能显示出来</h2>
            <p className="mt-0.5 text-sm text-muted-foreground">
              页面在渲染时出错了。你的账号与 DNS 服务没有受影响，可以换个页面继续用。
            </p>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            onClick={() => this.setState({ error: null, info: null, showDetail: false })}
          >
            <RefreshCw className="size-4" />
            重试
          </Button>
          <Button size="sm" variant="ghost" asChild>
            <Link to="/app">
              <Home className="size-4" />
              回到总览
            </Link>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => this.setState({ showDetail: !showDetail })}
          >
            {showDetail ? '隐藏详情' : '查看详情'}
          </Button>
        </div>

        {showDetail && (
          <div className="w-full space-y-2">
            <p className="text-xs text-muted-foreground">
              反馈问题时可把下面这段贴给管理员：
            </p>
            <pre className="max-h-64 overflow-auto rounded-xl bg-muted/60 p-3 text-2xs leading-relaxed">
              {error.name}: {error.message}
              {info?.componentStack ? `\n${info.componentStack}` : ''}
            </pre>
          </div>
        )}
      </div>
    )
  }
}

/**
 * RouteErrorBoundary 是带路由感知的包装：pathname 变化时自动重置边界。
 *
 * 单独导出是因为 useLocation 只能在组件里用，而 ErrorBoundary 需要是 class。
 */
export function RouteErrorBoundary({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  return <ErrorBoundary resetKey={pathname}>{children}</ErrorBoundary>
}
