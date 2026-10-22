import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Check,
  CheckCircle2,
  Copy,
  Download,
  KeyRound,
  LogOut,
  Pencil,
  Plus,
  RefreshCw,
  Settings as SettingsIcon,
  Shield,
  Trash2,
  Upload,
} from 'lucide-react'
import {
  changePassword,
  createToken,
  deleteToken,
  exportConfig,
  getMe,
  importConfig,
  listTokens,
  resetMyClientID,
  setMyClientID,
} from '@/api/me'
import { checkUpdate, applyUpdate } from '@/api/admin'
import { errorMessage } from '@/api/client'
import { getPreferences, updatePreferences } from '@/api/scope'
import type { ApiToken, ExportPayload } from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { useCopy } from '@/hooks/useCopy'
import { useAuth } from '@/hooks/useAuth'
import { useTheme } from '@/hooks/useTheme'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatDateTime } from '@/lib/format'
import { ChangeEmailForm } from '@/components/ChangeEmailForm'
import { DailyReportCard } from '@/components/DailyReportCard'

/* ---------- 修改密码 ---------- */

const pwdSchema = z
  .object({
    old_password: z.string().min(1, '请输入当前密码'),
    new_password: z
      .string()
      .min(8, '新密码至少 8 个字符')
      .regex(/[A-Za-z]/, '需包含字母')
      .regex(/[0-9]/, '需包含数字'),
    confirm: z.string(),
  })
  .refine((v) => v.new_password === v.confirm, {
    message: '两次输入的密码不一致',
    path: ['confirm'],
  })

type PwdForm = z.infer<typeof pwdSchema>

export function PasswordCard() {
  const toast = useToast()
  const form = useForm<PwdForm>({
    resolver: zodResolver(pwdSchema),
    defaultValues: { old_password: '', new_password: '', confirm: '' },
  })

  const mutation = useMutation({
    mutationFn: (v: PwdForm) => changePassword(v.old_password, v.new_password),
    onSuccess: () => {
      toast.success('密码已修改')
      form.reset()
    },
    onError: (err) => toast.error('修改失败', errorMessage(err)),
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="size-5 text-primary" />
          修改密码
        </CardTitle>
        <CardDescription>建议使用字母、数字与符号组合，至少 8 位。</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={form.handleSubmit((v) => mutation.mutate(v))} className="space-y-4" noValidate>
          <div className="space-y-2">
            <Label htmlFor="old_password">当前密码</Label>
            <Input id="old_password" type="password" autoComplete="current-password" {...form.register('old_password')} />
            {form.formState.errors.old_password && (
              <p className="text-xs text-danger">{form.formState.errors.old_password.message}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="new_password">新密码</Label>
            <Input id="new_password" type="password" autoComplete="new-password" {...form.register('new_password')} />
            {form.formState.errors.new_password && (
              <p className="text-xs text-danger">{form.formState.errors.new_password.message}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="confirm">确认新密码</Label>
            <Input id="confirm" type="password" autoComplete="new-password" {...form.register('confirm')} />
            {form.formState.errors.confirm && (
              <p className="text-xs text-danger">{form.formState.errors.confirm.message}</p>
            )}
          </div>
          <Button type="submit" loading={mutation.isPending}>
            保存修改
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

/* ---------- API Token ---------- */

/* ---------- 导入 / 导出 ---------- */

function ImportExportCard() {
  const toast = useToast()
  const [importText, setImportText] = useState('')

  const exportMutation = useMutation({
    mutationFn: exportConfig,
    onSuccess: (data: ExportPayload) => {
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `dnsforge-config-${Date.now()}.json`
      a.click()
      URL.revokeObjectURL(url)
      toast.success('配置已导出')
    },
    onError: (err) => toast.error('导出失败', errorMessage(err)),
  })

  const importMutation = useMutation({
    mutationFn: (payload: ExportPayload) => importConfig(payload),
    onSuccess: () => {
      toast.success('配置已导入')
      setImportText('')
    },
    onError: (err) => toast.error('导入失败', errorMessage(err)),
  })

  const handleImport = () => {
    try {
      const parsed = JSON.parse(importText) as ExportPayload
      if (!Array.isArray(parsed.rules) && !Array.isArray(parsed.subscriptions)) {
        throw new Error('格式不正确：需包含 rules 或 subscriptions 数组')
      }
      importMutation.mutate({ rules: parsed.rules ?? [], subscriptions: parsed.subscriptions ?? [] })
    } catch (e) {
      toast.error('解析失败', e instanceof Error ? e.message : 'JSON 格式错误')
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>配置导入 / 导出</CardTitle>
        <CardDescription>导出你的规则与订阅为 JSON，或从备份文件恢复。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <Button variant="outline" onClick={() => exportMutation.mutate()} loading={exportMutation.isPending}>
          <Download />
          导出配置
        </Button>
        <Separator />
        <div className="space-y-2">
          <Label htmlFor="import-json">导入配置（JSON）</Label>
          <textarea
            id="import-json"
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            placeholder='{"rules": [...], "subscriptions": [...]}'
            className="min-h-[120px] w-full rounded-xl border border-border bg-background px-3.5 py-2.5 font-mono text-[13px] focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/30"
          />
          <Button onClick={handleImport} disabled={!importText.trim()} loading={importMutation.isPending}>
            <Upload />
            导入
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

/* ---------- 在线更新 ---------- */

export function UpdateCard() {
  const toast = useToast()
  const [component, setComponent] = useState<'apid' | 'dnsd' | 'all'>('all')
  const [confirmOpen, setConfirmOpen] = useState(false)

  const updateQuery = useQuery({
    queryKey: ['update', 'check'],
    queryFn: checkUpdate,
    retry: false,
  })

  const applyMutation = useMutation({
    mutationFn: () => applyUpdate(component),
    onSuccess: (res) => {
      toast.success('更新已触发', res.message || '服务将在后台重启')
      setConfirmOpen(false)
    },
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  const info = updateQuery.data

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <RefreshCw className="size-5 text-primary" />
          在线更新
        </CardTitle>
        <CardDescription>从 GitHub Releases 检查并安装新版本。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {updateQuery.isLoading ? (
          <Skeleton className="h-16 rounded-xl" />
        ) : updateQuery.isError ? (
          <p className="text-sm text-muted-foreground">
            无法检查更新：{errorMessage(updateQuery.error)}
          </p>
        ) : info ? (
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">当前</span>
              <code className="font-mono">{info.current}</code>
            </div>
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">最新</span>
              <code className="font-mono">{info.latest}</code>
            </div>
            {info.update_available ? (
              <Badge variant="warning">有可用更新</Badge>
            ) : (
              <Badge variant="success">已是最新</Badge>
            )}
          </div>
        ) : null}

        {info?.update_available && (
          <>
            {info.notes && (
              <div className="rounded-xl border border-border bg-muted/40 p-4 text-sm">
                <p className="mb-1 font-medium">更新说明</p>
                <p className="whitespace-pre-wrap text-muted-foreground">{info.notes}</p>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-3">
              <Select value={component} onValueChange={(v) => setComponent(v as typeof component)}>
                <SelectTrigger className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部组件</SelectItem>
                  <SelectItem value="apid">仅 apid</SelectItem>
                  <SelectItem value="dnsd">仅 dnsd</SelectItem>
                </SelectContent>
              </Select>
              <Button variant="primary" onClick={() => setConfirmOpen(true)}>
                立即更新
              </Button>
            </div>
          </>
        )}

        {!info?.update_available && !updateQuery.isLoading && !updateQuery.isError && (
          <Button variant="outline" size="sm" onClick={() => updateQuery.refetch()}>
            <RefreshCw />
            重新检查
          </Button>
        )}
      </CardContent>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="确认更新"
        description={`将下载并安装最新版本（${component}）。服务会短暂重启，DNS 解析不受影响。`}
        confirmText="开始更新"
        loading={applyMutation.isPending}
        onConfirm={() => applyMutation.mutate()}
      />
    </Card>
  )
}

/* ---------- 主页面 ---------- */

export default function Settings() {
  const { user, logout, isAdmin, refreshUser } = useAuth()
  const { theme, setTheme } = useTheme()
  const toast = useToast()

  const meQuery = useQuery({ queryKey: ['me'], queryFn: getMe })

  const handleLogout = async () => {
    try {
      await logout()
      toast.success('已退出登录')
    } catch {
      toast.error('退出失败')
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="设置"
        description="账号、安全与应用更新"
        icon={<SettingsIcon className="size-5" />}
      />

      <Tabs defaultValue="account">
        <TabsList className="flex-wrap">
          <TabsTrigger value="account">账号</TabsTrigger>
          {!isAdmin && <TabsTrigger value="rules">规则</TabsTrigger>}
          <TabsTrigger value="security">安全</TabsTrigger>
          {!isAdmin && <TabsTrigger value="data">数据</TabsTrigger>}
        </TabsList>

        <TabsContent value="account" className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>账号信息</CardTitle>
              <CardDescription>你的基础账户资料</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              {meQuery.isLoading ? (
                <Skeleton className="h-24 rounded-xl" />
              ) : (
                <div className="grid gap-4 sm:grid-cols-2">
                  <InfoRow label="用户名" value={meQuery.data?.username ?? user?.username ?? '—'} />
                  <InfoRow label="邮箱" value={meQuery.data?.email || user?.email || '未绑定'} />
                  <InfoRow
                    label="角色"
                    value={meQuery.data?.role === 'admin' ? '管理员' : '普通用户'}
                  />
                  <InfoRow label="客户端 ID" value={meQuery.data?.client_id ?? user?.client_id ?? '—'} mono />
                  <InfoRow
                    label="状态"
                    value={meQuery.data?.enabled === false ? '已禁用' : '正常'}
                  />
                  <InfoRow
                    label="注册时间"
                    value={formatDateTime(meQuery.data?.created_at ?? user?.created_at)}
                  />
                </div>
              )}
              {/* 换绑邮箱：只在后端开放且配好邮件时显示 */}
              <ChangeEmailForm
                currentEmail={meQuery.data?.email ?? user?.email ?? ''}
                onChanged={() => {
                  meQuery.refetch()
                  refreshUser()
                }}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>接入 ID</CardTitle>
              <CardDescription>
                你的专属子域名前缀。重置会立刻换掉接入地址，所有设备都要重新配置。
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ResetIDRow />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>外观</CardTitle>
              <CardDescription>选择界面主题</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="flex items-center gap-3">
                <Select value={theme} onValueChange={(v) => setTheme(v as 'light' | 'dark')}>
                  <SelectTrigger className="w-40">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="light">浅色</SelectItem>
                    <SelectItem value="dark">暗色</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="rules" className="space-y-6" hidden={isAdmin}>
          <GlobalRulesCard />
        </TabsContent>

        <TabsContent value="security" className="space-y-6">
          <PasswordCard />
          <DailyReportCard />
          {/* API 令牌（PAT）已从界面移除：它是给脚本化调用用的，
              而这里的目标用户是手机/电脑上的终端用户，不会去写脚本 ——
              对他们来说这一屏只有「这串东西是干嘛的」这一个疑问。
              后端接口保留，需要自动化时由管理员直接调用 API 生成。 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <LogOut className="size-5 text-danger" />
                退出登录
              </CardTitle>
              <CardDescription>退出后需要重新登录才能管理规则。</CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="danger" onClick={handleLogout}>
                <LogOut />
                退出登录
              </Button>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="data" hidden={isAdmin}>
          <ImportExportCard />
        </TabsContent>

      </Tabs>
    </div>
  )
}

function InfoRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={mono ? 'break-all font-mono text-sm' : 'text-sm font-medium'}>{value}</p>
    </div>
  )
}

/* ---------- 全局规则开关 ---------- */

/**
 * GlobalRulesCard 让用户自己决定是否叠加管理员的全局规则层。
 *
 * 这是「全局规则误伤我」时的自助逃生门：关掉之后只用自己的规则与订阅，
 * 不需要找管理员。注意它不影响订阅 —— 自己的订阅照常生效。
 */
function GlobalRulesCard() {
  const qc = useQueryClient()
  const toast = useToast()

  const prefQuery = useQuery({ queryKey: ['me', 'preferences'], queryFn: getPreferences })

  const toggle = useMutation({
    mutationFn: (next: boolean) => updatePreferences({ use_global_rules: next }),
    onSuccess: (res) => {
      toast.success(
        res.use_global_rules ? '已启用全局规则' : '已关闭全局规则',
        res.use_global_rules ? '管理员的规则将重新生效' : '只使用你自己的规则与订阅',
      )
      qc.invalidateQueries({ queryKey: ['me', 'preferences'] })
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const on = prefQuery.data?.use_global_rules ?? true

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Shield className="size-5 text-primary" />
          全局规则
        </CardTitle>
        <CardDescription>
          管理员设置的全局规则与订阅，默认对所有用户生效。关掉后只使用你自己的规则。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {prefQuery.isLoading ? (
          <Skeleton className="h-12 rounded-xl" />
        ) : (
          <div className="flex items-center justify-between gap-4 rounded-xl border border-border p-3">
            <div className="min-w-0">
              <p className="text-sm font-medium">叠加全局规则</p>
              <p className="text-xs text-muted-foreground">
                {on
                  ? '当前启用：管理员的拦截会作用于你的解析'
                  : '当前关闭：管理员的规则对你无效'}
              </p>
            </div>
            <Switch
              checked={on}
              disabled={toggle.isPending}
              onCheckedChange={(v) => toggle.mutate(v)}
              aria-label="叠加全局规则"
            />
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          提示：关闭全局规则不会删掉你自己的规则与订阅，也不会影响其他用户。
          如果只是某个域名被误拦，更推荐在「规则」里加一条白名单。
        </p>
      </CardContent>
    </Card>
  )
}

/* ---------- 重置接入 ID ---------- */

/**
 * ResetIDRow 让用户自助重置 client_id。
 *
 * 换 id 等于换掉整个接入地址，所以是有次数上限的（管理员在后台设置）。
 * 用完只能找管理员 —— 这里直接把剩余次数显示出来，而不是等点了才报错。
 */
function ResetIDRow() {
  const qc = useQueryClient()
  const toast = useToast()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const meQuery = useQuery({ queryKey: ['me'], queryFn: getMe })

  const m = useMutation({
    mutationFn: resetMyClientID,
    onSuccess: (r) => {
      toast.success(
        '接入 ID 已重置',
        `新 ID：${r.client_id}，还剩 ${r.remaining} 次。请到「我的 DNS」重新获取地址。`,
      )
      setConfirmOpen(false)
      qc.invalidateQueries({ queryKey: ['me'] })
      qc.invalidateQueries({ queryKey: ['dns-config'] })
    },
    onError: (err) => toast.error('重置失败', errorMessage(err)),
  })

  const used = meQuery.data?.id_reset_count ?? 0
  const customUsed = meQuery.data?.custom_id_used ?? false

  // 自定义 ID：只在还没用过时展开输入框，用过之后直接显示状态，
  // 免得用户填完才被后端拒一次。
  const [custom, setCustom] = useState('')
  const [editing, setEditing] = useState(false)
  const customMutation = useMutation({
    mutationFn: () => setMyClientID(custom.trim().toLowerCase()),
    onSuccess: (r) => {
      toast.success('接入 ID 已更新', `新 ID：${r.client_id}，请到「我的 DNS」重新获取地址。`)
      setEditing(false)
      setCustom('')
      qc.invalidateQueries({ queryKey: ['me'] })
      qc.invalidateQueries({ queryKey: ['dns-config'] })
    },
    onError: (err) => toast.error('设置失败', errorMessage(err)),
  })

  // 前端先做一遍和后端一致的校验，只为让用户边打边看到问题；
  // 真正的判定仍在后端（长度、字符集、占用、次数）。
  const customError = (() => {
    const v = custom.trim().toLowerCase()
    if (!v) return ''
    if (v.length < 6) return `至少 6 位，当前 ${v.length} 位`
    if (!/^[a-z0-9]+$/.test(v)) return '只能用小写字母和数字'
    if (v === meQuery.data?.client_id) return '这就是你当前的接入 ID'
    return ''
  })()
  const canSubmit = custom.trim().length > 0 && !customError

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border p-3">
        <div className="min-w-0">
          <p className="font-mono text-sm">{meQuery.data?.client_id ?? '—'}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            已重置 {used} 次{used > 0 ? '，重置次数由管理员限制' : ''}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setConfirmOpen(true)}>
          <RefreshCw />
          重置接入 ID
        </Button>
      </div>

      <div className="rounded-xl border border-border p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <p className="text-sm font-medium">自定义接入 ID</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {customUsed
                ? '已使用过，只能设置一次。如需再改请联系管理员。'
                : '可以自己指定，6 位以上，仅小写字母和数字。只能设置一次。'}
            </p>
          </div>
          {!customUsed && !editing && (
            <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
              <Pencil />
              自定义
            </Button>
          )}
          {customUsed && <Badge variant="secondary">已使用</Badge>}
        </div>

        {!customUsed && editing && (
          <div className="mt-3 space-y-2">
            <div className="flex items-center gap-2">
              <Input
                value={custom}
                onChange={(e) => setCustom(e.target.value)}
                placeholder="例如 shenzhen01"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            {customError && <p className="text-xs text-danger">{customError}</p>}
            <p className="text-xs text-muted-foreground">
              设置后旧的接入地址会立刻失效，所有设备都要重新配置。
            </p>
            <div className="flex gap-2">
              <Button
                size="sm"
                loading={customMutation.isPending}
                disabled={!canSubmit}
                onClick={() => customMutation.mutate()}
              >
                <Check />
                保存
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setEditing(false)
                  setCustom('')
                }}
              >
                取消
              </Button>
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="重置接入 ID"
        description="重置后旧的子域名会立刻失效，你所有设备的 DNS 配置都要重新设置。确定继续吗？"
        confirmText="重置"
        loading={m.isPending}
        onConfirm={() => m.mutate()}
      />
    </>
  )
}
