import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  ListFilter,
  Pencil,
  Plus,
  Search,
  Trash2,
  Upload,
  Download,
} from 'lucide-react'
import {
  createRule,
  deleteRule,
  deleteRules,
  importRules,
  listRules,
  updateRule,
} from '@/api/rules'
import { errorMessage } from '@/api/client'
import type { Rule, RuleInput, RuleKind } from '@/api/types'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Pagination } from '@/components/Pagination'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

const KIND_LABEL: Record<RuleKind, string> = {
  block: '拦截',
  allow: '放行',
  rewrite_a: '改写 A',
  rewrite_aaaa: '改写 AAAA',
  rewrite_cname: '改写 CNAME',
}

const KIND_TONE: Record<RuleKind, 'danger' | 'success' | 'warning' | 'accent' | 'default'> = {
  block: 'danger',
  allow: 'success',
  rewrite_a: 'warning',
  rewrite_aaaa: 'warning',
  rewrite_cname: 'accent',
}

const QTYPES = ['A', 'AAAA', 'CNAME', 'TXT', 'MX', 'NS'] as const

const ruleSchema = z
  .object({
    kind: z.enum(['block', 'allow', 'rewrite_a', 'rewrite_aaaa', 'rewrite_cname']),
    pattern: z.string().min(1, '请输入匹配规则'),
    value: z.string(),
    qtypes: z.array(z.string()),
    enabled: z.boolean(),
  })
  .refine(
    (v) => !v.kind.startsWith('rewrite') || v.value.trim().length > 0,
    { message: '改写规则必须填写目标值', path: ['value'] },
  )

type RuleForm = z.infer<typeof ruleSchema>

export default function Rules() {
  const qc = useQueryClient()
  const toast = useToast()

  const [page, setPage] = useState(1)
  const [search, setSearch] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const [kindFilter, setKindFilter] = useState<string>('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<Rule | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState('')
  const [pendingDelete, setPendingDelete] = useState<Rule | null>(null)
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false)

  const pageSize = 20

  const queryKey = ['rules', page, search, kindFilter] as const
  const listQuery = useQuery({
    queryKey,
    queryFn: () =>
      listRules({
        page,
        page_size: pageSize,
        q: search || undefined,
        kind: kindFilter === 'all' ? undefined : kindFilter,
      }),
  })

  const items = listQuery.data?.items ?? []
  const total = listQuery.data?.total ?? 0

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['rules'] })
  }

  /* ------- 表单 ------- */
  const form = useForm<RuleForm>({
    resolver: zodResolver(ruleSchema),
    defaultValues: { kind: 'block', pattern: '', value: '', qtypes: [], enabled: true },
  })
  const watchKind = form.watch('kind')
  const watchQtypes = form.watch('qtypes')

  const openCreate = () => {
    setEditing(null)
    form.reset({ kind: 'block', pattern: '', value: '', qtypes: [], enabled: true })
    setEditorOpen(true)
  }

  const openEdit = (rule: Rule) => {
    setEditing(rule)
    form.reset({
      kind: rule.kind,
      pattern: rule.pattern,
      value: rule.value,
      qtypes: rule.qtypes ?? [],
      enabled: rule.enabled,
    })
    setEditorOpen(true)
  }

  const saveMutation = useMutation({
    mutationFn: (values: RuleForm) => {
      const payload: RuleInput = {
        kind: values.kind,
        pattern: values.pattern.trim(),
        value: values.value.trim(),
        qtypes: values.qtypes,
        enabled: values.enabled,
      }
      return editing ? updateRule(editing.id, payload) : createRule(payload)
    },
    onSuccess: () => {
      toast.success(editing ? '规则已更新' : '规则已创建')
      setEditorOpen(false)
      invalidate()
    },
    onError: (err) => toast.error('保存失败', errorMessage(err)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteRule(id),
    onSuccess: () => {
      toast.success('规则已删除')
      setPendingDelete(null)
      setSelected(new Set())
      invalidate()
    },
    onError: (err) => toast.error('删除失败', errorMessage(err)),
  })

  const bulkDeleteMutation = useMutation({
    mutationFn: (ids: string[]) => deleteRules(ids),
    onSuccess: () => {
      toast.success('已批量删除')
      setBulkDeleteOpen(false)
      setSelected(new Set())
      invalidate()
    },
    onError: (err) => toast.error('批量删除失败', errorMessage(err)),
  })

  const importMutation = useMutation({
    mutationFn: (content: string) => importRules(content, true),
    onSuccess: (res) => {
      toast.success('导入完成', `新增 ${res.created} 条，跳过 ${res.skipped} 条`)
      if (res.errors?.length) {
        toast.warning('部分规则有误', `${res.errors.length} 条未能导入`)
      }
      setImportOpen(false)
      setImportText('')
      invalidate()
    },
    onError: (err) => toast.error('导入失败', errorMessage(err)),
  })

  const toggleEnabled = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => updateRule(id, { enabled }),
    onSuccess: () => invalidate(),
    onError: (err) => toast.error('更新失败', errorMessage(err)),
  })

  const allSelected = useMemo(
    () => items.length > 0 && items.every((r) => selected.has(r.id)),
    [items, selected],
  )

  const toggleAll = () => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (allSelected) items.forEach((r) => next.delete(r.id))
      else items.forEach((r) => next.add(r.id))
      return next
    })
  }

  const toggleOne = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const doSearch = () => {
    setSearch(searchInput.trim())
    setPage(1)
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="规则管理"
        description="拦截、放行与改写规则，优先级：放行 > 改写 > 拦截"
        icon={<ListFilter className="size-5" />}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => setImportOpen(true)}>
              <Upload />
              批量导入
            </Button>
            <Button size="sm" onClick={openCreate}>
              <Plus />
              新建规则
            </Button>
          </>
        }
      />

      {/* 工具条 */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && doSearch()}
            placeholder="搜索域名或规则…"
            className="pl-9"
          />
        </div>
        <div className="flex items-center gap-2">
          <Select value={kindFilter} onValueChange={(v) => { setKindFilter(v); setPage(1) }}>
            <SelectTrigger className="w-36">
              <SelectValue placeholder="全部类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部类型</SelectItem>
              {(Object.keys(KIND_LABEL) as RuleKind[]).map((k) => (
                <SelectItem key={k} value={k}>
                  {KIND_LABEL[k]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="secondary" onClick={doSearch}>
            搜索
          </Button>
        </div>
      </div>

      {selected.size > 0 && (
        <div className="flex items-center justify-between rounded-xl border border-primary/30 bg-primary/5 px-4 py-2.5">
          <span className="text-sm text-primary">已选择 {selected.size} 条</span>
          <Button variant="danger" size="sm" onClick={() => setBulkDeleteOpen(true)}>
            <Trash2 />
            删除所选
          </Button>
        </div>
      )}

      {/* 内容 */}
      {listQuery.isError ? (
        <ErrorState message={errorMessage(listQuery.error)} onRetry={() => listQuery.refetch()} />
      ) : listQuery.isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-14 rounded-xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ListFilter className="size-6" />}
          title={search || kindFilter !== 'all' ? '没有匹配的规则' : '还没有任何规则'}
          description={
            search || kindFilter !== 'all'
              ? '换个关键词或筛选条件试试。'
              : '创建你的第一条拦截或放行规则，或从订阅列表批量导入。'
          }
          action={
            !search && kindFilter === 'all' ? (
              <Button onClick={openCreate}>
                <Plus />
                新建规则
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          {/* 桌面端表格 */}
          <div className="hidden rounded-2xl border border-border/50 bg-card shadow-sm md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10">
                    <Checkbox checked={allSelected} onCheckedChange={toggleAll} aria-label="全选" />
                  </TableHead>
                  <TableHead className="w-24">类型</TableHead>
                  <TableHead>匹配规则</TableHead>
                  <TableHead className="w-40">目标值</TableHead>
                  <TableHead className="w-24">记录类型</TableHead>
                  <TableHead className="w-20">启用</TableHead>
                  <TableHead className="w-32">创建时间</TableHead>
                  <TableHead className="w-24 text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((rule) => (
                  <TableRow key={rule.id}>
                    <TableCell>
                      <Checkbox
                        checked={selected.has(rule.id)}
                        onCheckedChange={() => toggleOne(rule.id)}
                        aria-label="选择"
                      />
                    </TableCell>
                    <TableCell>
                      <Badge variant={KIND_TONE[rule.kind]}>{KIND_LABEL[rule.kind]}</Badge>
                    </TableCell>
                    <TableCell>
                      <code className="font-mono text-[13px]">{rule.pattern}</code>
                    </TableCell>
                    <TableCell>
                      <span className="font-mono text-[13px] text-muted-foreground">
                        {rule.value || '—'}
                      </span>
                    </TableCell>
                    <TableCell>
                      {rule.qtypes?.length ? (
                        <div className="flex flex-wrap gap-1">
                          {rule.qtypes.map((q) => (
                            <Badge key={q} variant="secondary">
                              {q}
                            </Badge>
                          ))}
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground">全部</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={(v) => toggleEnabled.mutate({ id: rule.id, enabled: v })}
                        aria-label="启用"
                      />
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDateTime(rule.created_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="icon-sm" onClick={() => openEdit(rule)} aria-label="编辑">
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => setPendingDelete(rule)}
                          aria-label="删除"
                          className="text-danger hover:bg-danger/10"
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          {/* 移动端卡片列表 */}
          <div className="space-y-3 md:hidden">
            {items.map((rule) => (
              <div key={rule.id} className="rounded-2xl border border-border/50 bg-card p-4 shadow-sm">
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-2">
                    <Checkbox
                      checked={selected.has(rule.id)}
                      onCheckedChange={() => toggleOne(rule.id)}
                      aria-label="选择"
                    />
                    <Badge variant={KIND_TONE[rule.kind]}>{KIND_LABEL[rule.kind]}</Badge>
                  </div>
                  <Switch
                    checked={rule.enabled}
                    onCheckedChange={(v) => toggleEnabled.mutate({ id: rule.id, enabled: v })}
                    aria-label="启用"
                  />
                </div>
                <p className="mt-3 break-all font-mono text-[13px]">{rule.pattern}</p>
                {rule.value && (
                  <p className="mt-1 break-all font-mono text-xs text-muted-foreground">→ {rule.value}</p>
                )}
                <div className="mt-3 flex items-center justify-between">
                  <div className="flex flex-wrap gap-1">
                    {rule.qtypes?.length ? (
                      rule.qtypes.map((q) => (
                        <Badge key={q} variant="secondary">
                          {q}
                        </Badge>
                      ))
                    ) : (
                      <span className="text-xs text-muted-foreground">全部记录类型</span>
                    )}
                  </div>
                  <div className="flex gap-1">
                    <Button variant="ghost" size="icon-sm" onClick={() => openEdit(rule)} aria-label="编辑">
                      <Pencil />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => setPendingDelete(rule)}
                      aria-label="删除"
                      className="text-danger"
                    >
                      <Trash2 />
                    </Button>
                  </div>
                </div>
              </div>
            ))}
          </div>

          <Pagination page={page} pageSize={pageSize} total={total} onPageChange={setPage} />
        </>
      )}

      {/* 新建 / 编辑弹窗 */}
      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent className="md:max-w-lg">
          <DialogHeader>
            <DialogTitle>{editing ? '编辑规则' : '新建规则'}</DialogTitle>
            <DialogDescription>
              支持 Adblock 语法（<code className="font-mono">||ads.example.com^</code>）、裸域名、hosts 格式。
            </DialogDescription>
          </DialogHeader>

          <form
            onSubmit={form.handleSubmit((v) => saveMutation.mutate(v))}
            className="space-y-4"
            noValidate
          >
            <div className="space-y-2">
              <Label>规则类型</Label>
              <Select
                value={watchKind}
                onValueChange={(v) => form.setValue('kind', v as RuleKind)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(KIND_LABEL) as RuleKind[]).map((k) => (
                    <SelectItem key={k} value={k}>
                      {KIND_LABEL[k]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="pattern">匹配规则</Label>
              <Input
                id="pattern"
                placeholder="||ads.example.com^ 或 example.com"
                className="font-mono text-sm"
                {...form.register('pattern')}
              />
              {form.formState.errors.pattern && (
                <p className="text-xs text-danger">{form.formState.errors.pattern.message}</p>
              )}
            </div>

            {watchKind.startsWith('rewrite') && (
              <div className="space-y-2">
                <Label htmlFor="value">改写目标</Label>
                <Input
                  id="value"
                  placeholder={watchKind === 'rewrite_cname' ? 'target.example.com' : '192.168.1.10'}
                  className="font-mono text-sm"
                  {...form.register('value')}
                />
                {form.formState.errors.value && (
                  <p className="text-xs text-danger">{form.formState.errors.value.message}</p>
                )}
              </div>
            )}

            <div className="space-y-2">
              <Label>限定记录类型（留空表示全部）</Label>
              <div className="flex flex-wrap gap-3">
                {QTYPES.map((q) => (
                  <label key={q} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox
                      checked={watchQtypes.includes(q)}
                      onCheckedChange={(checked) => {
                        const next = checked
                          ? [...watchQtypes, q]
                          : watchQtypes.filter((x) => x !== q)
                        form.setValue('qtypes', next)
                      }}
                    />
                    {q}
                  </label>
                ))}
              </div>
            </div>

            <div className="flex items-center justify-between rounded-xl border border-border bg-background px-4 py-3">
              <div>
                <p className="text-sm font-medium">立即启用</p>
                <p className="text-xs text-muted-foreground">关闭后该规则不会生效</p>
              </div>
              <Switch
                checked={form.watch('enabled')}
                onCheckedChange={(v) => form.setValue('enabled', v)}
              />
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setEditorOpen(false)}>
                取消
              </Button>
              <Button type="submit" loading={saveMutation.isPending}>
                {editing ? '保存' : '创建'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* 批量导入弹窗 */}
      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="md:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Download className="size-5" />
              批量导入规则
            </DialogTitle>
            <DialogDescription>
              每行一条，自动识别 Adblock / hosts / 裸域名 / DNSMasq 格式。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Textarea
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              placeholder={'||ads.example.com^\n@@||safe.example.com^\n0.0.0.0 tracker.net'}
              className="min-h-[180px] font-mono text-[13px]"
            />
            <p className="text-xs text-muted-foreground">共 {importText.split('\n').filter((l) => l.trim()).length} 行</p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportOpen(false)}>
              取消
            </Button>
            <Button
              onClick={() => importMutation.mutate(importText)}
              loading={importMutation.isPending}
              disabled={!importText.trim()}
            >
              导入
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 单条删除确认 */}
      <ConfirmDialog
        open={Boolean(pendingDelete)}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title="删除规则"
        description={pendingDelete ? `确定要删除规则「${pendingDelete.pattern}」吗？此操作不可撤销。` : ''}
        destructive
        confirmText="删除"
        loading={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />

      {/* 批量删除确认 */}
      <ConfirmDialog
        open={bulkDeleteOpen}
        onOpenChange={setBulkDeleteOpen}
        title="批量删除规则"
        description={`确定要删除选中的 ${selected.size} 条规则吗？此操作不可撤销。`}
        destructive
        confirmText="全部删除"
        loading={bulkDeleteMutation.isPending}
        onConfirm={() => bulkDeleteMutation.mutate(Array.from(selected))}
      />
    </div>
  )
}
