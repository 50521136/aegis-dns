import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, Wand2 } from 'lucide-react'

import { resetMyClientID, setMyClientID } from '@/api/me'
import { errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/toast'

/**
 * ClientIDActions 是客户端 ID 旁边的「重置 / 自定义」入口。
 *
 * 放在接入地址页而不是设置页：用户看到 ID 的那一刻才最可能想改它，
 * 让他跳到设置页再回来，等于把「刚才看到的那个值」忘掉一半。
 */
export function ClientIDActions({
  clientID,
  disabled,
  resetUsed,
  resetLimit,
  customUsed,
}: {
  clientID: string
  disabled?: boolean
  resetUsed?: number
  resetLimit?: number
  customUsed?: boolean
}) {
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [custom, setCustom] = useState('')
  const [confirmReset, setConfirmReset] = useState(false)

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['dnsConfig'] })
    qc.invalidateQueries({ queryKey: ['me'] })
  }

  const doReset = useMutation({
    mutationFn: () => resetMyClientID(),
    onSuccess: (res) => {
      refresh()
      setConfirmReset(false)
      setOpen(false)
      toast.success('客户端 ID 已重置', `新 ID：${res.client_id}，记得更新已配置的设备`)
    },
    onError: (err) => toast.error('重置失败', errorMessage(err)),
  })

  const doCustom = useMutation({
    mutationFn: () => setMyClientID(custom.trim()),
    onSuccess: (res) => {
      refresh()
      setOpen(false)
      setCustom('')
      toast.success('自定义 ID 已生效', `新 ID：${res.client_id}`)
    },
    onError: (err) => toast.error('设置失败', errorMessage(err)),
  })

  const resetLeft =
    resetLimit && resetLimit > 0 ? Math.max(0, resetLimit - (resetUsed ?? 0)) : null
  const canReset = !disabled && (resetLeft === null || resetLeft > 0)
  const canCustom = !disabled && !customUsed

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        disabled={!canReset && !canCustom}
        title={
          !canReset && !canCustom
            ? '自助重置次数已用完，且自定义 ID 已使用过'
            : '重置或自定义客户端 ID'
        }
      >
        <RefreshCw />
        重置 / 自定义
      </Button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>客户端 ID</DialogTitle>
            <DialogDescription>
              当前：<span className="font-mono">{clientID}</span>
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <section className="rounded-xl border border-border/60 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium">随机重置</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    换一个新 ID，旧的立刻失效。已配置的设备需要重新填地址。
                  </p>
                  {resetLeft !== null && (
                    <p className="mt-1 text-xs text-muted-foreground">
                      剩余次数：{resetLeft} / {resetLimit}
                    </p>
                  )}
                </div>
                <Button
                  size="sm"
                  variant={confirmReset ? 'danger' : 'secondary'}
                  disabled={!canReset}
                  loading={doReset.isPending}
                  onClick={() => (confirmReset ? doReset.mutate() : setConfirmReset(true))}
                >
                  {confirmReset ? '确认重置' : '重置'}
                </Button>
              </div>
              {confirmReset && (
                <p className="mt-2 text-xs text-danger">
                  重置后所有设备都要改配置，确定继续？
                </p>
              )}
            </section>

            <section className="space-y-2 rounded-xl border border-border/60 p-3">
              <div>
                <p className="text-sm font-medium">自定义 ID</p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  只能设置一次。4–32 位小写字母或数字，不能占用主域名的首段。
                </p>
              </div>
              <div className="flex gap-2">
                <div className="min-w-0 flex-1">
                  <Label htmlFor="custom-id" className="sr-only">
                    自定义 ID
                  </Label>
                  <Input
                    id="custom-id"
                    className="font-mono"
                    placeholder="例如 myhome2026"
                    value={custom}
                    disabled={!canCustom}
                    onChange={(e) => setCustom(e.target.value.toLowerCase())}
                  />
                </div>
                <Button
                  size="sm"
                  disabled={!canCustom || !/^[a-z0-9]{4,32}$/.test(custom.trim())}
                  loading={doCustom.isPending}
                  onClick={() => doCustom.mutate()}
                >
                  <Wand2 />
                  设置
                </Button>
              </div>
              {!canCustom && (
                <p className="text-xs text-muted-foreground">自定义 ID 的机会已经用过了。</p>
              )}
            </section>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}
