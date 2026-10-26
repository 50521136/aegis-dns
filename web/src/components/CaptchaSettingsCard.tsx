import { useEffect, useState } from 'react'
import { AlertTriangle, ShieldCheck, Trash2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { getSettings, updateSettings } from '@/api/admin'
import { cn } from '@/lib/utils'

/**
 * CaptchaSettingsCard 配置极验 v4 行为验证。
 *
 * 它单独成一个组件、自己读写接口，而不是并入 SettingsPanel 的大表单：
 * 私钥是「只写不读」的字段（后端永不回显明文），塞进 react-hook-form 之后
 * 每次 hydration 都会拿到 undefined，保存时又会把它当空串提交 —— 那正是
 * 「改别的设置顺手把密钥清掉」的经典事故。这里显式管理自己的状态。
 *
 * 三条已在后端落实、界面上也要说清的行为：
 *  1. **没填 captcha_id / 私钥时视为未启用**，不会把注册登录锁死；
 *  2. **极验接口不可用时放行**（fail-open），第三方故障不该导致全站进不去；
 *  3. 每个入口独立开关 —— 只担心注册被刷就只开注册，别逼老用户每次登录都滑一次。
 */
export function CaptchaSettingsCard() {
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [clearing, setClearing] = useState(false)

  const [enabled, setEnabled] = useState(false)
  const [captchaID, setCaptchaID] = useState('')
  const [captchaKey, setCaptchaKey] = useState('')
  const [keySet, setKeySet] = useState(false)
  const [scenes, setScenes] = useState({
    geetest_on_register: false,
    geetest_on_login: false,
    geetest_on_forgot: false,
    geetest_on_feedback: false,
  })

  const load = async () => {
    try {
      const s = await getSettings()
      setEnabled(s.geetest_enabled ?? false)
      setCaptchaID(s.geetest_id ?? '')
      setKeySet(Boolean(s.geetest_key_set))
      setScenes({
        geetest_on_register: s.geetest_on_register ?? false,
        geetest_on_login: s.geetest_on_login ?? false,
        geetest_on_forgot: s.geetest_on_forgot ?? false,
        geetest_on_feedback: s.geetest_on_feedback ?? false,
      })
    } catch (err) {
      toast.error('读取验证配置失败', errorMessage(err))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      const patch: Record<string, unknown> = {
        geetest_enabled: enabled,
        geetest_id: captchaID.trim(),
        ...scenes,
      }
      // 留空表示「保持现在的」：后端对空串不做覆盖，
      // 显式清空要走下面那个按钮（geetest_key_clear）。
      if (captchaKey.trim() !== '') patch.geetest_key = captchaKey.trim()
      await updateSettings(patch as never)
      toast.success('验证配置已保存', enabled ? '已启用的入口会在下次提交时弹出验证' : '总开关关闭，所有入口都不验证')
      if (captchaKey.trim() !== '') setKeySet(true)
      setCaptchaKey('')
    } catch (err) {
      toast.error('保存失败', errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const clearKey = async () => {
    setClearing(true)
    try {
      await updateSettings({ geetest_key_clear: true } as never)
      setKeySet(false)
      setCaptchaKey('')
      toast.success('私钥已清除', '极验验证会按「未配置」处理，不会拦截任何请求')
    } catch (err) {
      toast.error('清除失败', errorMessage(err))
    } finally {
      setClearing(false)
    }
  }

  if (loading) return null

  // 参数不全时的真实后果要说出来：开关看着是开的，实际一个入口都没验。
  const misconfigured = enabled && (captchaID.trim() === '' || !keySet)
  const sceneOn = Object.values(scenes).filter(Boolean).length

  return (
    <div className="min-w-0 space-y-4 rounded-xl border border-border/60 p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <ShieldCheck className="size-4 shrink-0" />
            人机验证（极验 v4）
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            只挡会被脚本刷的入口。解析请求（53 / 853 / 443）永远不加验证 ——
            那是每毫秒都在跑的路径，加一层等于把整个 DNS 服务废掉。
          </p>
        </div>
        <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="启用极验验证" />
      </div>

      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <div className="min-w-0 space-y-1.5">
          <Label className="text-xs">Captcha ID</Label>
          <Input
            className="min-w-0 font-mono"
            placeholder="从极验控制台复制"
            value={captchaID}
            onChange={(e) => setCaptchaID(e.target.value)}
            spellCheck={false}
          />
        </div>
        <div className="min-w-0 space-y-1.5">
          <Label className="flex items-center gap-1.5 text-xs">
            Captcha Key（私钥）
            {keySet && <Badge variant="success">已配置</Badge>}
          </Label>
          <Input
            className="min-w-0 font-mono"
            type="password"
            placeholder={keySet ? '留空表示保持不变' : '从极验控制台复制'}
            value={captchaKey}
            onChange={(e) => setCaptchaKey(e.target.value)}
            autoComplete="new-password"
          />
        </div>
      </div>

      <div className="space-y-2">
        <p className="text-xs font-medium text-foreground">哪些入口需要验证</p>
        <div className="space-y-2">
          {[
            {
              key: 'geetest_on_register' as const,
              name: '注册',
              desc: '最容易被批量脚本刷的入口，建议常开',
            },
            {
              key: 'geetest_on_forgot' as const,
              name: '找回密码（发码）',
              desc: '能给任意邮箱发信，是垃圾邮件放大器；验证挂在发码这一步，不是最后的重置动作',
            },
            {
              key: 'geetest_on_login' as const,
              name: '登录',
              desc: '能挡撞库，但老用户每次登录都要滑一次会直接走人 —— 默认关闭',
            },
            {
              key: 'geetest_on_feedback' as const,
              name: '提交反馈',
              desc: '反馈量通常很小，加了只是烦人 —— 默认关闭',
            },
          ].map((it) => (
            <div
              key={it.key}
              className="flex min-w-0 items-start justify-between gap-4 rounded-lg border border-border bg-background px-3 py-2.5"
            >
              <div className="min-w-0">
                <p className="text-sm">{it.name}</p>
                <p className="mt-0.5 break-words text-xs text-muted-foreground">{it.desc}</p>
              </div>
              <Switch
                checked={scenes[it.key]}
                onCheckedChange={(v) => setScenes((s) => ({ ...s, [it.key]: v }))}
                aria-label={`${it.name}需要验证`}
              />
            </div>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">
          已启用 {sceneOn} 个入口。
          <span className="text-foreground"> 只有总开关打开、参数填全、且该入口开关打开，才会真的弹验证</span>
          —— 少任何一道都按「不验证」处理，避免把自己锁在门外。
        </p>
      </div>

      {misconfigured && (
        <div className="flex items-start gap-2 rounded-lg bg-warning/10 px-3 py-2 text-xs text-warning">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          <span className="min-w-0 break-words">
            {captchaID.trim() === '' ? '还没填 Captcha ID' : '还没填 Captcha Key 私钥'}
            ，当前所有入口都不会验证。填完保存才会生效。
          </span>
        </div>
      )}

      <div className={cn('flex flex-wrap items-center gap-2')}>
        <Button size="sm" onClick={save} loading={saving}>
          保存验证配置
        </Button>
        {keySet && (
          <Button size="sm" variant="ghost" onClick={clearKey} loading={clearing}>
            <Trash2 />
            清除私钥
          </Button>
        )}
        <span className="min-w-0 break-words text-xs text-muted-foreground">
          私钥只写不读，保存后不再回显；极验接口自身故障时会放行本次请求，不会把注册登录锁死。
        </span>
      </div>
    </div>
  )
}
