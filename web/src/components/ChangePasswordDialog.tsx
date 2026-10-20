import { useState } from 'react'
import { KeyRound } from 'lucide-react'

import { changePassword } from '@/api/me'
import { errorMessage } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useToast } from '@/components/ui/toast'

/**
 * ChangePasswordDialog 是头像菜单里的「修改密码」。
 *
 * 放在头像菜单而不是独立页面：改密码是个低频的一次性动作，
 * 为它占一个侧边栏位（还要分管理员/普通用户两套入口）不值得。
 * 管理员和普通用户走同一个弹窗，也就不需要「账号安全」页面了。
 */
export function ChangePasswordDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const toast = useToast()
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)

  const reset = () => {
    setOldPw('')
    setNewPw('')
    setConfirm('')
  }

  const submit = async () => {
    if (newPw !== confirm) {
      toast.error('两次输入不一致', '请重新确认新密码')
      return
    }
    setBusy(true)
    try {
      await changePassword(oldPw, newPw)
      toast.success('密码已修改', '其他已登录设备需要重新登录')
      reset()
      onOpenChange(false)
    } catch (err) {
      toast.error('修改失败', errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const valid = oldPw.length > 0 && newPw.length >= 8 && newPw === confirm

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-5 text-primary" />
            修改密码
          </DialogTitle>
          <DialogDescription>修改后其他设备上的登录会失效。</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="old-pw">当前密码</Label>
            <Input
              id="old-pw"
              type="password"
              autoComplete="current-password"
              value={oldPw}
              onChange={(e) => setOldPw(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="new-pw">新密码</Label>
            <Input
              id="new-pw"
              type="password"
              autoComplete="new-password"
              placeholder="至少 8 位，含字母和数字"
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="confirm-pw">确认新密码</Label>
            <Input
              id="confirm-pw"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
            {confirm && newPw !== confirm && (
              <p className="text-xs text-danger">两次输入不一致</p>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button loading={busy} disabled={!valid} onClick={submit}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
