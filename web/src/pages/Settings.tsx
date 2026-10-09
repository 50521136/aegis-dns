import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  CheckCircle2,
  Copy,
  Download,
  KeyRound,
  LogOut,
  Plus,
  RefreshCw,
  Settings as SettingsIcon,
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
} from '@/api/me'
import { checkUpdate, applyUpdate } from '@/api/admin'
import { errorMessage } from '@/api/client'
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

function PasswordCard() {
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

function TokensCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [createdToken, setCreatedToken] = useState<string | null>(null)
  const [pendingDelete, setPendingDelete] = useState<ApiToken | null>(null)
  const { copied, copy } = useCopy()

  const tokensQuery = useQuery({ queryKey: ['me', 'tokens'], queryFn: listTokens })

  const createMutation = useMutation({
    mutationFn: (n: string) => createToken(n),
    onSuccess: (res) => {
      setCreatedToken(res.token)
      setCreateOpen(false)
      setName('')
      qc.invalidateQueries({ queryKey: ['me', 'tokens'] })
    },
    onError: (err) => toast.error('创建失败', errorMessage(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteToken(id),
    onSuccess: () => {
      toast.success('Token 已撤销')
      setPendingDelete(null)
      qc.invalidateQueries({ queryKey: ['me', 'tokens'] })
    },
    onError: (err) => toast.error('撤销失败', errorMessage(err)),
  })

  const tokens = tokensQuery.data ?? []

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="size-5 text-primary" />
            长期 API Token
          </CardTitle>
          <CardDescription>用于脚本、CLI 或第三方面板直接调用 API，可随时撤销。</CardDescription>
        </div>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus />
          新建
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        {tokensQuery.isLoading ? (
          Array.from({ length: 2 }).map((_, i) => <Skeleton key={i} className="h-14 rounded-xl" />)
        ) : tokens.length === 0 ? (
          <EmptyState icon={<KeyRound className="size-6" />} title="暂无 API Token" className="py-8" />
        ) : (
          tokens.map((t) => (
            <div
              key={t.id}
              className="flex items-center justify-between gap-3 rounded-xl border border-border bg-background px-4 py-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <p className="truncate text-sm font-medium">{t.name || '未命名'}</p>
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{t.prefix}…</code>
                </div>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  创建于 {formatDateTime(t.created_at)}
                  {t.last_used_at ? ` · 最近使用 ${formatDateTime(t.last_used_at)}` : ''}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                className="text-danger hover:bg-danger/10"
                onClick={() => setPendingDelete(t)}
                aria-label="撤销"
              >
                <Trash2 />
              </Button>
            </div>
          ))
        )}
      </CardContent>

      {/* 新建 token */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="md:max-w-md">
          <DialogHeader>
            <DialogTitle>新建 API Token</DialogTitle>
            <DialogDescription>给它起个名字，便于日后识别用途。</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="token-name">名称</Label>
            <Input
              id="token-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 ci-deploy"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button onClick={() => createMutation.mutate(name)} loading={createMutation.isPending} disabled={!name.trim()}>
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 展示 token（仅一次） */}
      <Dialog open={Boolean(createdToken)} onOpenChange={(o) => !o && setCreatedToken(null)}>
        <DialogContent className="md:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <CheckCircle2 className="size-5 text-success" />
              Token 已创建
            </DialogTitle>
            <DialogDescription>
              请立即复制保存，此 Token 仅显示这一次，关闭后无法再次查看。
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2 rounded-xl border border-border bg-background p-2 pl-3.5">
            <code className="min-w-0 flex-1 break-all font-mono text-[13px]">{createdToken}</code>
            <Button variant="ghost" size="icon-sm" onClick={() => createdToken && copy(createdToken)} aria-label="复制">
              {copied ? <CheckCircle2 className="text-success" /> : <Copy />}
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={() => setCreatedToken(null)}>我已保存</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(pendingDelete)}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="撤销 Token"
        description={pendingDelete ? `确定要撤销「${pendingDelete.name}」吗？使用该 Token 的调用将立即失效。` : ''}
        destructive
        confirmText="撤销"
        loading={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />
    </Card>
  )
}

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

function UpdateCard() {
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
  const { user, logout } = useAuth()
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
          <TabsTrigger value="security">安全</TabsTrigger>
          <TabsTrigger value="data">数据</TabsTrigger>
          <TabsTrigger value="update">更新</TabsTrigger>
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

        <TabsContent value="security" className="space-y-6">
          <PasswordCard />
          <TokensCard />
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

        <TabsContent value="data">
          <ImportExportCard />
        </TabsContent>

        <TabsContent value="update">
          <UpdateCard />
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
