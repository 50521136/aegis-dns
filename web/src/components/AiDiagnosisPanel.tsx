import { useState } from 'react'
import { Brain, Check, Info, Play, Sparkles, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { applyDiagnosis, diagnoseFeedback } from '@/api/scope'
import type { Feedback } from '@/api/types'
import { cn } from '@/lib/utils'

/**
 * AiDiagnosisPanel 显示 AI 对一条反馈的诊断结论，并提供「采纳」按钮。
 *
 * 设计取向：把「模型的判断」和「规则引擎的事实」分开呈现。
 * 模型只负责把事实翻译成结论与措辞，所以界面上要能看到它依据的是
 * 哪些确定性判定（哪个层拦的、来自哪个订阅），否则管理员无法判断
 * 这条建议该不该信。
 *
 * 采纳是唯一会改规则的动作，且服务端会二次校验，
 * 这里不做任何「自动执行」。
 */

const VERDICT_META: Record<string, { label: string; variant: 'danger' | 'success' | 'secondary' }> = {
  wrong_block: { label: '判为误拦', variant: 'danger' },
  correct: { label: '规则正确', variant: 'success' },
  unclear: { label: '证据不足', variant: 'secondary' },
}

const ACTION_LABEL: Record<string, string> = {
  add_global_allow: '建议全局放行',
  add_user_allow: '建议该用户放行',
  no_action: '无需处理',
  need_human: '需人工判断',
}

export function AiDiagnosisPanel({ feedback, onDone }: { feedback: Feedback; onDone: () => void }) {
  const toast = useToast()
  const [busy, setBusy] = useState<'diagnose' | 'apply' | null>(null)

  const status = feedback.ai_status ?? ''
  const domains = (feedback.ai_domains ?? '').split(',').map((s) => s.trim()).filter(Boolean)
  const canApply =
    feedback.ai_action === 'add_global_allow' || feedback.ai_action === 'add_user_allow'
  const canDiagnose = status === '' || status === 'failed' || status === 'done'

  const runDiagnose = async () => {
    setBusy('diagnose')
    try {
      await diagnoseFeedback(feedback.id)
      toast.info('已开始诊断', '模型分析完成后会自动出现在这里')
      onDone()
    } catch (err) {
      toast.error('诊断失败', errorMessage(err))
    } finally {
      setBusy(null)
    }
  }

  const runApply = async () => {
    setBusy('apply')
    try {
      const res = await applyDiagnosis(feedback.id)
      toast.success(
        `已写入 ${res.rule_count} 条规则`,
        `${res.global ? '全局放行' : '该用户放行'}：${res.applied.join('、')}`,
      )
      onDone()
    } catch (err) {
      toast.error('采纳失败', errorMessage(err))
    } finally {
      setBusy(null)
    }
  }

  // 没诊断过：只给一个入口，不占版面。
  if (status === '') {
    return (
      <div className="mt-2">
        <Button variant="secondary" size="sm" onClick={runDiagnose} loading={busy === 'diagnose'}>
          <Sparkles />
          AI 诊断
        </Button>
      </div>
    )
  }

  if (status === 'pending') {
    return (
      <div className="mt-2 flex items-center gap-2 rounded-lg bg-muted/60 px-2.5 py-2 text-xs text-muted-foreground">
        <Spinner />
        AI 正在分析…
      </div>
    )
  }

  if (status === 'failed') {
    return (
      <div className="mt-2 space-y-2">
        <div className="flex items-start gap-2 rounded-lg bg-danger/10 px-2.5 py-2 text-xs text-danger">
          <X className="mt-0.5 size-3.5 shrink-0" />
          <span className="min-w-0 break-words">{feedback.ai_reason || '诊断失败'}</span>
        </div>
        <Button variant="secondary" size="sm" onClick={runDiagnose} loading={busy === 'diagnose'}>
          <Play />
          重试诊断
        </Button>
      </div>
    )
  }

  const meta = VERDICT_META[feedback.ai_verdict ?? ''] ?? { label: '已诊断', variant: 'secondary' as const }

  return (
    <div className="mt-2 space-y-2 rounded-lg border border-border/60 bg-muted/30 p-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Brain className="size-3.5 shrink-0 text-muted-foreground" />
        <Badge variant={meta.variant}>{meta.label}</Badge>
        <Badge variant="outline">{ACTION_LABEL[feedback.ai_action ?? ''] ?? feedback.ai_action}</Badge>
        {feedback.ai_model && (
          <span className="font-mono text-[11px] text-muted-foreground">{feedback.ai_model}</span>
        )}
      </div>

      {feedback.ai_reason && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          <span className="font-medium text-foreground">依据：</span>
          {feedback.ai_reason}
        </p>
      )}

      {domains.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <span className="text-muted-foreground">建议放行：</span>
          {domains.map((d) => (
            <code key={d} className="mono-addr rounded bg-background px-1.5 py-0.5">
              {d}
            </code>
          ))}
        </div>
      )}

      {/* 闸门记录：模型建议了但被安全校验挡下来的内容，必须让管理员看到 */}
      {feedback.ai_guard_notes && (
        <div className="flex items-start gap-1.5 rounded-md bg-warning/10 px-2 py-1.5 text-[11px] text-warning">
          <Info className="mt-0.5 size-3 shrink-0" />
          <span className="min-w-0 break-words">安全校验：{feedback.ai_guard_notes}</span>
        </div>
      )}

      {feedback.ai_reply && (
        <p className="rounded-md bg-background/60 px-2 py-1.5 text-xs">
          <span className="text-muted-foreground">拟回复用户：</span>
          {feedback.ai_reply}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2 pt-0.5">
        {canApply && (
          <Button size="sm" onClick={runApply} loading={busy === 'apply'}>
            <Check />
            采纳并写入规则
          </Button>
        )}
        {canDiagnose && (
          <Button variant="ghost" size="sm" onClick={runDiagnose} loading={busy === 'diagnose'}>
            <Play />
            重新诊断
          </Button>
        )}
        {!canApply && feedback.ai_action && (
          <span className={cn('text-[11px]', 'text-muted-foreground')}>
            {feedback.ai_action === 'no_action'
              ? '模型认为规则没问题，可直接结单'
              : '这条建议不写规则，需要你手工判断'}
          </span>
        )}
      </div>
    </div>
  )
}
