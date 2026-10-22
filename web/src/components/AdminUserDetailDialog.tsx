import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, CheckCircle2, Loader2, RotateCcw } from 'lucide-react'

import { getAdminUser, setAdminUserQuota, updateUser } from '@/api/admin'
import { revokeUserMember, setUserMember } from '@/api/member'
import type { AdminUserDetail } from '@/api/types'
import { formatDateTime } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
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
import { Switch } from '@/components/ui/switch'
import { useToast } from '@/components/ui/toast'

/** 配额的三个字段。0 = 跟随档位，-1 = 不限，正数 = 手填上限。 */
type QuotaKey = 'max_rules' | 'max_subs' | 'daily_queries'

const QUOTA_FIELDS: { key: QuotaKey; label: string; unit: string }[] = [
  { key: 'max_rules', label: '自定义规则', unit: '条' },
  { key: 'max_subs', label: '自定义订阅', unit: '个' },
  { key: 'daily_queries', label: '每日请求量', unit: '次' },
]

function fmtLimit(v: number): string {
  if (v === 0) return '不限'
  return v.toLocaleString('zh-CN')
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-1.5">
      <span className="shrink-0 text-xs text-muted-foreground">{label}</span>
      <span className="min-w-0 break-all text-right text-sm">{children}</span>
    </div>
  )
}

export function AdminUserDetailDialog({
  userId,
  onClose,
}: {
  userId: string | null
  onClose: () => void
}) {
  const qc = useQueryClient()
  const toast = useToast()
  const [draft, setDraft] = useState<Record<QuotaKey, string>>({
    max_rules: '0',
    max_subs: '0',
    daily_queries: '0',
  })
  // 会员草稿：种类 + 天数/永久。种类与有效期是一个概念的两个维度，
  // 所以放在同一组控件里一次提交 —— 分成两个入口就会出现
  // 「会员档」和「超级会员」并存的状态（这次要消灭的就是它）。
  const [memberDraft, setMemberDraft] = useState({
    tierId: '',
    days: '30',
    permanent: false,
  })

  const { data, isLoading, error } = useQuery({
    queryKey: ['admin-user', userId],
    queryFn: () => getAdminUser(userId as string),
    enabled: !!userId,
  })

  // 详情加载完把手填值灌进输入框。用 custom 而不是 quota：
  // 输入框里要显示「你自己填了什么」，不是叠加后的生效值，
  // 否则一打开就变成「已覆盖」，管理员没法一眼看出原来是不是默认。
  useEffect(() => {
    if (!data) return
    setDraft({
      max_rules: String(data.custom.max_rules),
      max_subs: String(data.custom.max_subs),
      daily_queries: String(data.custom.daily_queries),
    })
    // 默认选中「此刻生效的种类」：界面上下拉框里永远是一个具体种类，
    // 不再有「自动」这种要用户自己理解的选项。
    setMemberDraft({
      tierId: data.tier_id || data.tier,
      days: '30',
      permanent: data.member_forever,
    })
  }, [data])

  const saveQuota = useMutation({
    mutationFn: () =>
      setAdminUserQuota(userId as string, {
        max_rules: Number(draft.max_rules) || 0,
        max_subs: Number(draft.max_subs) || 0,
        daily_queries: Number(draft.daily_queries) || 0,
      }),
    onSuccess: () => {
      toast.success('配额已更新')
      qc.invalidateQueries({ queryKey: ['admin-user', userId] })
      qc.invalidateQueries({ queryKey: ['admin-users'] })
    },
    onError: () => toast.error('保存失败'),
  })

  // 发放 / 调整会员：种类与有效期一次提交
  const applyMember = useMutation({
    mutationFn: () =>
      setUserMember(userId as string, Number(memberDraft.days) || 0, {
        tier_id: memberDraft.tierId,
        permanent: memberDraft.permanent,
      }),
    onSuccess: (r) => {
      toast.success(
        '会员已更新',
        r.member_forever ? `永久 ${r.tier_name}` : `${r.tier_name} · 剩 ${r.member_days} 天`,
      )
      qc.invalidateQueries({ queryKey: ['admin-user', userId] })
      qc.invalidateQueries({ queryKey: ['admin-users'] })
    },
    onError: () => toast.error('调整会员失败'),
  })

  const revokeMember = useMutation({
    mutationFn: () => revokeUserMember(userId as string),
    onSuccess: () => {
      toast.success('会员已取消')
      qc.invalidateQueries({ queryKey: ['admin-user', userId] })
      qc.invalidateQueries({ queryKey: ['admin-users'] })
    },
    onError: () => toast.error('取消失败'),
  })

  const toggle = useMutation({
    mutationFn: (patch: {
      enabled?: boolean
      use_global_rules?: boolean
      rules_enabled?: boolean
      subs_enabled?: boolean
    }) => updateUser(userId as string, patch),
    onSuccess: () => {
      toast.success('已更新')
      qc.invalidateQueries({ queryKey: ['admin-user', userId] })
      qc.invalidateQueries({ queryKey: ['admin-users'] })
    },
    onError: () => toast.error('更新失败'),
  })

  const u: AdminUserDetail | undefined = data
  // 档位下拉的选项：全部会员种类，按 sort 排序
  const tierOptions = Object.values(u?.tiers ?? {}).sort((a, b) => a.sort - b.sort)
  // 默认档显示名：非会员到期后回落到的那一档
  const defaultTierName =
    tierOptions.find((t) => t.is_default)?.name ?? tierOptions[0]?.name ?? '普通用户'
  const quotaChanged =
    !!u &&
    (Number(draft.max_rules) !== u.custom.max_rules ||
      Number(draft.max_subs) !== u.custom.max_subs ||
      Number(draft.daily_queries) !== u.custom.daily_queries)

  return (
    <Dialog open={!!userId} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85dvh] gap-3 p-4 md:max-w-2xl md:gap-4 md:p-6">
        <DialogHeader>
          <DialogTitle className="break-all">
            {isLoading ? '加载中…' : (u?.username ?? '用户详情')}
          </DialogTitle>
          <DialogDescription>
            查看与调整这个用户的会员状态、开关和使用量。改动立即生效。
          </DialogDescription>
        </DialogHeader>

        {isLoading && (
          <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
            <Loader2 className="mr-2 size-4 animate-spin" />
            读取中…
          </div>
        )}

        {error && !isLoading && (
          <p className="py-6 text-center text-sm text-destructive">读取用户信息失败</p>
        )}

        {u && (
          <div className="space-y-4 overflow-y-auto">
            {/* 基本信息 */}
            <section className="rounded-xl border border-border/50 p-3">
              <Row label="接入 ID">
                <code className="font-mono text-xs">{u.client_id}</code>
              </Row>
              <Row label="邮箱">{u.email || '—'}</Row>
              <Row label="角色">
                <Badge variant={u.role === 'admin' ? 'warning' : 'default'}>
                  {u.role === 'admin' ? '管理员' : '普通用户'}
                </Badge>
              </Row>
              <Row label="注册时间">{formatDateTime(u.created_at) || '—'}</Row>
            </section>

            {/* 会员：种类 + 有效期，一个概念一套控件 */}
            <section className="rounded-xl border border-border/50 p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="text-sm font-medium">会员</span>
                {u.role === 'admin' ? (
                  <Badge variant="warning">管理员 · 不受配额限制</Badge>
                ) : u.is_member ? (
                  <Badge variant="success">
                    {u.tier_name} · {u.member_forever ? '永久' : `剩 ${u.member_days} 天`}
                  </Badge>
                ) : (
                  <Badge variant="secondary">{u.tier_name}</Badge>
                )}
              </div>
              <Row label="到期时间">
                {u.member_forever ? '永久有效' : formatDateTime(u.member_until) || '—'}
              </Row>

              <div className="mt-2 space-y-2 border-t border-border/40 pt-2">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <div className="space-y-1">
                    <Label htmlFor="member-tier" className="text-xs text-muted-foreground">
                      会员种类
                    </Label>
                    <select
                      id="member-tier"
                      className="h-9 w-full rounded-lg border border-border bg-background px-2 text-sm"
                      value={memberDraft.tierId}
                      disabled={applyMember.isPending}
                      onChange={(e) =>
                        setMemberDraft((d) => ({ ...d, tierId: e.target.value }))
                      }
                    >
                      {tierOptions.map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.name}
                          {t.is_default ? '（默认档）' : ''}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="member-days" className="text-xs text-muted-foreground">
                      发放天数（负数 = 扣减）
                    </Label>
                    <Input
                      id="member-days"
                      inputMode="numeric"
                      disabled={memberDraft.permanent}
                      value={memberDraft.days}
                      onChange={(e) =>
                        setMemberDraft((d) => ({
                          ...d,
                          days: e.target.value.replace(/[^\d-]/g, ''),
                        }))
                      }
                    />
                  </div>
                </div>

                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    className="size-3.5 accent-[rgb(var(--primary))]"
                    checked={memberDraft.permanent}
                    onChange={(e) =>
                      setMemberDraft((d) => ({ ...d, permanent: e.target.checked }))
                    }
                  />
                  设为永久会员（忽略天数）
                </label>

                <div className="flex items-center justify-end gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={revokeMember.isPending || (!u.is_member && !u.tier_id)}
                    onClick={() => revokeMember.mutate()}
                  >
                    取消会员
                  </Button>
                  <Button
                    size="sm"
                    loading={applyMember.isPending}
                    disabled={memberDraft.permanent ? !memberDraft.tierId : !Number(memberDraft.days)}
                    onClick={() => applyMember.mutate()}
                  >
                    发放 / 调整
                  </Button>
                </div>

                <p className="text-[11px] leading-relaxed text-muted-foreground">
                  会员种类只在有效期内生效，到期自动回到默认档「{defaultTierName}」。
                  发放不会把用户从更高的种类降下来 —— 要降档请先「取消会员」再按新种类发放。
                </p>
              </div>
            </section>

            {/* 开关 */}
            <section className="space-y-2 rounded-xl border border-border/50 p-3">
              <span className="text-sm font-medium">开关</span>
              {[
                { key: 'enabled' as const, label: '账号启用', value: u.enabled },
                {
                  key: 'use_global_rules' as const,
                  label: '叠加全局规则',
                  value: u.use_global_rules,
                },
                { key: 'rules_enabled' as const, label: '允许自定义规则', value: u.rules_enabled },
                { key: 'subs_enabled' as const, label: '允许自定义订阅', value: u.subs_enabled },
              ].map((s) => (
                <div key={s.key} className="flex items-center justify-between gap-3">
                  <Label htmlFor={`sw-${s.key}`} className="text-sm font-normal">
                    {s.label}
                  </Label>
                  <Switch
                    id={`sw-${s.key}`}
                    checked={s.value}
                    disabled={toggle.isPending}
                    onCheckedChange={(v) => toggle.mutate({ [s.key]: v })}
                  />
                </div>
              ))}
            </section>

            {/* 使用量与配额 */}
            <section className="rounded-xl border border-border/50 p-3">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-sm font-medium">使用量 / 配额</span>
                <span className="text-[11px] text-muted-foreground">
                  {u.custom.max_rules || u.custom.max_subs || u.custom.daily_queries
                    ? '已手动覆盖'
                    : '按档位默认'}
                </span>
              </div>
              <div className="space-y-2">
                {QUOTA_FIELDS.map((f) => {
                  const used =
                    f.key === 'max_rules'
                      ? u.usage.rules
                      : f.key === 'max_subs'
                        ? u.usage.subs
                        : u.usage.today_queries
                  const limit = u.quota[f.key]
                  return (
                    <div key={f.key} className="flex items-center justify-between gap-3 text-sm">
                      <span className="text-muted-foreground">{f.label}</span>
                      <span className="font-mono text-xs">
                        已用 {used ?? '—'} / {fmtLimit(limit)}
                        {f.unit !== '次' && limit > 0 ? ` ${f.unit}` : ''}
                      </span>
                    </div>
                  )
                })}
              </div>
            </section>

            {/* 配额覆盖 */}
            <section className="space-y-2 rounded-xl border border-border/50 p-3">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium">手动调整配额</span>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    setDraft({ max_rules: '0', max_subs: '0', daily_queries: '0' })
                  }
                >
                  <RotateCcw className="size-3.5" />
                  恢复默认
                </Button>
              </div>
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                填 <code>0</code> 表示跟随档位（规则 {fmtLimit(u.quota_default.max_rules)} / 订阅{' '}
                {fmtLimit(u.quota_default.max_subs)} / 请求 {fmtLimit(u.quota_default.daily_queries)}）；
                填 <code>-1</code> 表示不限。
              </p>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                {QUOTA_FIELDS.map((f) => (
                  <div key={f.key} className="space-y-1">
                    <Label htmlFor={`q-${f.key}`} className="text-xs text-muted-foreground">
                      {f.label}
                    </Label>
                    <Input
                      id={`q-${f.key}`}
                      inputMode="numeric"
                      value={draft[f.key]}
                      onChange={(e) =>
                        setDraft((d) => ({ ...d, [f.key]: e.target.value.replace(/[^\d-]/g, '') }))
                      }
                    />
                  </div>
                ))}
              </div>
              <div className="flex items-center justify-end gap-2 pt-1">
                {saveQuota.isSuccess && !quotaChanged && (
                  <span className="flex items-center gap-1 text-xs text-emerald-600">
                    <CheckCircle2 className="size-3.5" />
                    已保存
                  </span>
                )}
                <Button
                  size="sm"
                  disabled={!quotaChanged || saveQuota.isPending}
                  loading={saveQuota.isPending}
                  onClick={() => saveQuota.mutate()}
                >
                  保存配额
                </Button>
              </div>
            </section>

            {u.role === 'admin' && (
              <p className="flex items-start gap-2 rounded-xl bg-muted/40 p-3 text-xs text-muted-foreground">
                <Ban className="mt-0.5 size-3.5 shrink-0" />
                管理员不参与会员体系，也不受配额限制；上面的配额只对普通用户生效。
              </p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
