import { useEffect, useRef, useState } from 'react'
import { FileText, ShieldCheck } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'

interface TermsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  version?: string
  content: string
  /**
   * 受控的「已同意」状态。
   *
   * 和页面上的勾选框共享同一份状态：用户在弹窗里勾了，页面上的框也同步勾上，
   * 不用他再勾一次 —— 两处各存一份是这类弹窗最常见的体验 bug。
   */
  agreed: boolean
  onAgreedChange: (agreed: boolean) => void
  /** 点「同意并继续」时回调（由调用方决定是继续提交还是只关窗） */
  onAgree: () => void
  /** 「不同意」的文案，注册场景是「不同意，返回」，阅读场景是「关闭」 */
  declineText?: string
}

/**
 * 用户协议弹窗。
 *
 * 注册时必须让用户**真的看到**协议内容才允许勾选，所以正文放在独立滚动区里
 * 原样渲染（whitespace-pre-wrap）。刻意不解析 Markdown / HTML：
 * 协议正文由管理员在后台填写，一旦支持 HTML 就等于给了一个存储型 XSS 入口。
 *
 * 滚动区用 flex-1 + overflow-y-auto，而不是让整个弹窗滚动 ——
 * 后者会把底部的「同意」按钮一起滚走，用户得先滚回去才能继续。
 */
export function TermsDialog({
  open,
  onOpenChange,
  title,
  version,
  content,
  agreed,
  onAgreedChange,
  onAgree,
  declineText = '不同意',
}: TermsDialogProps) {
  // 弹窗每次打开都回到顶部：上次读到一半的位置对这次没有意义，
  // 而且停在中段会让人以为自己漏读了前半部分。
  const scrollRef = useRef<HTMLDivElement>(null)
  const [scrolledToEnd, setScrolledToEnd] = useState(false)

  useEffect(() => {
    if (!open) return
    setScrolledToEnd(false)
    const el = scrollRef.current
    if (el) el.scrollTop = 0
  }, [open])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    // 留 24px 容差：末尾可能正好是一行空白，滚到底也差几像素。
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 24) {
      setScrolledToEnd(true)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="overflow-hidden md:max-w-2xl">
        <DialogHeader>
          <div className="flex items-start gap-3">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
              <FileText className="size-5" />
            </div>
            <div className="min-w-0 space-y-1.5">
              <DialogTitle className="break-words">{title}</DialogTitle>
              <DialogDescription>
                {version ? `版本 ${version} · ` : ''}
                请在同意前完整阅读以下内容
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        {/* 正文滚动区。min-h-0 是 flex 子项能真正滚动的前提，
            少了它 flex 会把内容撑开、滚动条永远不出现。 */}
        <div
          ref={scrollRef}
          onScroll={onScroll}
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain rounded-xl border border-border/60 bg-muted/30 px-4 py-3"
        >
          <pre className="whitespace-pre-wrap break-words font-sans text-[13px] leading-relaxed text-foreground/90">
            {content}
          </pre>
        </div>

        <div className="space-y-3">
          <label className="flex cursor-pointer items-start gap-2.5 text-sm">
            <Checkbox
              checked={agreed}
              onCheckedChange={(v) => onAgreedChange(v)}
              aria-label="我已阅读并同意"
            />
            <span className="min-w-0 leading-relaxed">
              我已阅读并同意《{title}》
              {!scrolledToEnd && (
                <span className="mt-0.5 block text-xs text-muted-foreground">
                  建议滚动到底部完整阅读后再勾选
                </span>
              )}
            </span>
          </label>

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {declineText}
            </Button>
            <Button
              onClick={onAgree}
              disabled={!agreed}
              title={agreed ? undefined : '请先勾选同意'}
            >
              <ShieldCheck />
              同意并继续
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  )
}
