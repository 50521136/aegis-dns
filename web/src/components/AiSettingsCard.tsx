import { useEffect, useState } from 'react'
import { Brain, ChevronDown, ListChecks, PlugZap, Save, Sparkles } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { useToast } from '@/components/ui/toast'
import { errorMessage } from '@/api/client'
import { getAISettings, listAIModels, testAI, updateAISettings } from '@/api/scope'
import type { AIModelInfo, AITestResult } from '@/api/types'

/**
 * AiSettingsCard 配置「反馈自动诊断」用的模型。
 *
 * 走 OpenAI 兼容的 /chat/completions，所以 DeepSeek / 通义 / Kimi /
 * 自建 vLLM 都能填。Key 只写不读：保存后接口只回「已配置」，
 * 不会再吐明文 —— 想换就重新填，想清空打开下面的开关。
 *
 * 「测试」按钮用一条假证据跑一次真实调用，能当场区分
 * 「Key 错」「模型名错」「地址不通」三种情况，
 * 避免出现「保存成功但一跑就失败」。
 */
export function AiSettingsCard() {
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<AITestResult | null>(null)
  const [fetchingModels, setFetchingModels] = useState(false)
  const [models, setModels] = useState<AIModelInfo[]>([])
  const [modelsError, setModelsError] = useState('')
  const [showModels, setShowModels] = useState(false)

  const [enabled, setEnabled] = useState(false)
  const [baseURL, setBaseURL] = useState('')
  const [model, setModel] = useState('')
  const [fallback, setFallback] = useState('')
  const [maxTokens, setMaxTokens] = useState(4000)
  const [apiKey, setApiKey] = useState('')
  const [keySet, setKeySet] = useState(false)
  const [timeoutSec, setTimeoutSec] = useState(30)
  const [maxPerHour, setMaxPerHour] = useState(20)
  const [triggerMode, setTriggerMode] = useState<'off' | 'on_submit' | 'manual'>('manual')

  const load = async () => {
    try {
      const s = await getAISettings()
      setEnabled(s.ai_enabled)
      setBaseURL(s.ai_base_url)
      setModel(s.ai_model)
      setFallback(s.ai_fallback_models ?? '')
      setMaxTokens(s.ai_max_tokens ?? 4000)
      setKeySet(s.ai_key_set)
      setTimeoutSec(s.ai_timeout_sec || 30)
      setMaxPerHour(s.ai_max_per_hour ?? 20)
      setTriggerMode(s.ai_trigger_mode || 'manual')
    } catch (err) {
      toast.error('读取 AI 配置失败', errorMessage(err))
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
        ai_enabled: enabled,
        ai_base_url: baseURL.trim(),
        ai_model: model.trim(),
        ai_fallback_models: fallback.trim(),
        ai_max_tokens: maxTokens,
        ai_timeout_sec: timeoutSec,
        ai_max_per_hour: maxPerHour,
        ai_trigger_mode: triggerMode,
      }
      // 只有真的填了新 Key 才提交，留空表示「保持现在的」。
      if (apiKey.trim() !== '') patch.ai_key = apiKey.trim()
      await updateAISettings(patch)
      toast.success('已保存')
      setApiKey('')
      setKeySet(keySet || apiKey.trim() !== '')
      setTestResult(null)
    } catch (err) {
      toast.error('保存失败', errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const fetchModels = async () => {
    setFetchingModels(true)
    setModelsError('')
    try {
      const res = await listAIModels()
      setModels(res.models || [])
      setShowModels(true)
      if (!res.ok) {
        setModelsError(res.error ?? '上游未返回模型列表')
        toast.error('获取模型列表失败', res.error ?? '请检查模型地址与 Key')
      } else {
        toast.success(`获取到 ${res.models?.length ?? 0} 个模型`)
      }
    } catch (err) {
      setModelsError(errorMessage(err))
      toast.error('获取模型列表失败', errorMessage(err))
    } finally {
      setFetchingModels(false)
    }
  }

  /** 把列表里的模型追加回退链（去重、跳过主模型）。 */
  const addFallback = (id: string) => {
    const cur = fallback.split(/[,，\s]+/).map((x) => x.trim()).filter(Boolean)
    if (id === model.trim() || cur.includes(id)) {
      toast.error('已在主模型或回退链里', id)
      return
    }
    setFallback([...cur, id].join(', '))
    toast.success('已加入回退链', id)
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await testAI()
      setTestResult(res)
      if (res.ok) {
        toast.success(
          res.fell_back ? `已回退到 ${res.used_model}` : '模型可用',
          `${res.used_model || res.model} · ${res.elapsed_ms}ms`,
        )
      } else {
        toast.error('模型不可用', res.error ?? '未知错误')
      }
    } catch (err) {
      toast.error('测试失败', errorMessage(err))
    } finally {
      setTesting(false)
    }
  }

  if (loading) return null

  return (
    <div className="space-y-3 rounded-xl border border-border/60 p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <Brain className="size-4" />
            反馈自动诊断
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            用户提交误拦申诉时，先让模型给出「是否误杀 + 建议动作」。
            规则是否命中由程序确定计算，模型只做归因与措辞；采纳仍需你点一次。
          </p>
        </div>
        <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="启用 AI 诊断" />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5 sm:col-span-2">
          <Label className="text-xs">模型地址（OpenAI 兼容）</Label>
          <Input
            className="font-mono"
            placeholder="https://api.deepseek.com/v1"
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
          />
          <p className="text-xs text-muted-foreground">
            地址与 Key 会先保存，再点「获取模型列表」由服务端向该地址的 /models 拉取。
          </p>
        </div>
        <div className="space-y-1.5">
          <Label className="text-xs">主模型名</Label>
          <Input
            className="font-mono"
            placeholder="deepseek-chat"
            value={model}
            onChange={(e) => setModel(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label className="flex items-center gap-1.5 text-xs">
            API Key
            {keySet && <Badge variant="success">已配置</Badge>}
          </Label>
          <Input
            className="font-mono"
            type="password"
            placeholder={keySet ? '留空表示保持不变' : 'sk-...'}
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            autoComplete="new-password"
          />
        </div>
        <div className="space-y-1.5 sm:col-span-2">
          <Label className="text-xs">
            回退模型（按顺序尝试，逗号分隔；主模型失败时自动切换）
          </Label>
          <Input
            className="font-mono"
            placeholder="deepseek-v4-flash-0731, glm-5.3"
            value={fallback}
            onChange={(e) => setFallback(e.target.value)}
          />
          <p className="text-xs text-muted-foreground">
            推理模型可能把 token 全花在「思考」上导致空输出；配一个便宜的非推理模型兜底，
            避免「额度扣了、结论没有」。
          </p>
        </div>
        <div className="space-y-1.5">
          <Label className="text-xs">max_tokens（推理模型建议 ≥4000）</Label>
          <Input
            type="number"
            min={0}
            max={32000}
            step={100}
            value={maxTokens}
            onChange={(e) => setMaxTokens(Number(e.target.value) || 0)}
          />
          <p className="text-xs text-muted-foreground">
            0 = 用服务端默认 4000。太小会让推理模型空返回但照常扣费。
          </p>
        </div>
        <div className="space-y-1.5">
          <Label className="text-xs">触发方式</Label>
          <Select value={triggerMode} onValueChange={(v) => setTriggerMode(v as typeof triggerMode)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="manual">手动（在反馈里点「AI 诊断」）</SelectItem>
              <SelectItem value="on_submit">提交时自动诊断</SelectItem>
              <SelectItem value="off">关闭（保留配置但不跑）</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label className="text-xs">超时（秒）</Label>
            <Input
              type="number"
              min={5}
              max={180}
              value={timeoutSec}
              onChange={(e) => setTimeoutSec(Number(e.target.value) || 30)}
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">每小时上限</Label>
            <Input
              type="number"
              min={0}
              max={500}
              value={maxPerHour}
              onChange={(e) => setMaxPerHour(Number(e.target.value) || 0)}
            />
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={save} loading={saving}>
          <Save />
          保存配置
        </Button>
        <Button variant="secondary" size="sm" onClick={runTest} loading={testing} disabled={!baseURL || !model}>
          <PlugZap />
          测试模型
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => (showModels && models.length ? setShowModels(!showModels) : void fetchModels())}
          loading={fetchingModels}
          disabled={!baseURL}
        >
          <ListChecks />
          获取模型列表
          {showModels && models.length > 0 && <ChevronDown className="rotate-180" />}
        </Button>
        <Button variant="ghost" size="sm" onClick={fetchModels} loading={fetchingModels} disabled={!baseURL}>
          刷新列表
        </Button>
        <span className="text-xs text-muted-foreground">
          测试会真实消耗一次调用
        </span>
      </div>

      {modelsError && (
        <div className="rounded-lg bg-danger/10 px-2.5 py-2 text-xs text-danger">
          模型列表：{modelsError}
        </div>
      )}

      {showModels && models.length > 0 && (
        <div className="space-y-1.5 rounded-lg border border-border/60 p-2.5">
          <p className="text-xs text-muted-foreground">
            共 {models.length} 个模型 · 点击「设为主模型」可直接填入，或加入回退链
          </p>
          <div className="max-h-56 space-y-1 overflow-y-auto">
            {models.map((m) => (
              <div
                key={m.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted/40 px-2 py-1.5"
              >
                <span className="min-w-0 break-all font-mono text-xs">
                  {m.id}
                  {m.id === model.trim() && <Badge variant="success" className="ml-1.5">主模型</Badge>}
                  {fallback.split(/[,，\s]+/).includes(m.id) && (
                    <Badge className="ml-1.5">回退</Badge>
                  )}
                </span>
                <span className="flex shrink-0 gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-8 px-2 text-xs"
                    onClick={() => setModel(m.id)}
                  >
                    设为主模型
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-8 px-2 text-xs"
                    onClick={() => addFallback(m.id)}
                  >
                    加入回退
                  </Button>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {testResult && (
        <div className="space-y-1.5">
          <div
            className={
              testResult.ok
                ? 'flex items-start gap-2 rounded-lg bg-success/10 px-2.5 py-2 text-xs text-success'
                : 'flex items-start gap-2 rounded-lg bg-danger/10 px-2.5 py-2 text-xs text-danger'
            }
          >
            <Sparkles className="mt-0.5 size-3.5 shrink-0" />
            <span className="min-w-0 break-words">
              {testResult.ok
                ? `连通正常：${testResult.used_model || testResult.model} · ${testResult.elapsed_ms}ms`
                : testResult.error}
              {testResult.ok && testResult.fell_back && (
                <span className="mt-1 block text-warning">
                  主模型 {testResult.model} 未能出结论，已回退到 {testResult.used_model}。
                  建议把主模型换成响应稳定的模型，避免每次都多花一轮调用。
                </span>
              )}
            </span>
          </div>
          {testResult.ok && testResult.sample && (
            <div className="rounded-lg bg-muted/40 px-2.5 py-2 text-xs text-muted-foreground">
              <p>
                <span className="text-foreground">模型返回示例：</span>
                {testResult.sample.verdict} / {testResult.sample.action.type}
                {testResult.sample.action.domains?.length
                  ? `（${testResult.sample.action.domains.join('、')}）`
                  : ''}
              </p>
              <p className="mt-1">
                <span className="text-foreground">拟回复：</span>
                {testResult.sample.reply || '（空）'}
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
