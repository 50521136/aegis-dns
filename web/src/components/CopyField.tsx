import { Fragment } from 'react'
import { Check, Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useCopy } from '@/hooks/useCopy'
import { cn } from '@/lib/utils'

/** 形如 DC:1E:BA:… 的十六进制指纹 */
const HEX_FINGERPRINT = /^(?:[0-9A-Fa-f]{2}:)+[0-9A-Fa-f]{2}$/

/** URL / 主机名：有协议头，或长得像域名（含点、无空格） */
const URL_LIKE = /^[a-z][a-z0-9+.-]*:\/\//i

interface CopyFieldProps {
  value: string
  label?: string
  mono?: boolean
  className?: string
}

/** URL 里允许断行的分隔符 */
const URL_BREAK_CHARS = new Set(['/', '.', '-', '?', '&', '='])

/**
 * 把字符串按指定字符切成若干段，切点保留在段尾。
 *
 * 配合「每段 nowrap + 段间 <wbr>」使用：浏览器只会在 <wbr> 处折行，
 * 断点因此变得确定，不会出现 break-all 那种把 seven-cloud 切成 seve / n-cloud 的断法。
 */
function splitAfter(value: string, chars: Set<string>): string[] {
  const out: string[] = []
  let buf = ''
  for (const ch of value) {
    buf += ch
    if (chars.has(ch)) {
      out.push(buf)
      buf = ''
    }
  }
  if (buf) out.push(buf)
  return out
}

/**
 * URL 的断点：协议头 `https://` 整体不可拆（否则可能断在 `://` 中间），
 * 其余按 / . - ? & = 切开。
 */
function splitURL(value: string): string[] {
  const m = /^[a-z][a-z0-9+.-]*:\/\//i.exec(value)
  if (!m) return splitAfter(value, URL_BREAK_CHARS)
  return [m[0], ...splitAfter(value.slice(m[0].length), URL_BREAK_CHARS)]
}

/** 带复制按钮的只读字段，用于展示 DNS 地址、证书指纹等 */
export function CopyField({ value, label, mono = true, className }: CopyFieldProps) {
  const { copied, copy } = useCopy()

  // 两类值需要「按语义断行」而不是按字符断行：
  //   指纹 DC:1E:BA —— 断在字节对中间会被读成 DC:1 / E:BA，核对证书时极易看错；
  //   URL —— break-all 会把 seven-cloud 切成 seve / n-cloud，很难读。
  // 做法一致：把不可拆的片段包成 nowrap，之间只留 <wbr> 作为唯一断点。
  const segments = HEX_FINGERPRINT.test(value)
    ? splitAfter(value, new Set([':']))
    : URL_LIKE.test(value)
      ? splitURL(value)
      : null

  return (
    <div className={cn('space-y-1.5', className)}>
      {label && <p className="text-xs font-medium text-muted-foreground">{label}</p>}
      {/* min-w-0 必须有：Grid/Flex 子项默认 min-width:auto，长地址会把容器撑爆、
          把右侧复制按钮挤出卡片。 */}
      <div className="flex min-w-0 items-center gap-2 rounded-xl border border-border bg-background p-1 pl-3.5">
        <code
          className={cn(
            'min-w-0 flex-1 break-words text-sm leading-snug',
            mono ? 'font-mono text-[13px]' : 'font-sans',
          )}
          title={value}
        >
          {segments
            ? segments.map((seg, i) => (
                <Fragment key={i}>
                  <span className="whitespace-nowrap">{seg}</span>
                  {i < segments.length - 1 && <wbr />}
                </Fragment>
              ))
            : value}
        </code>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={() => copy(value)}
          aria-label="复制"
          title="复制"
        >
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
      </div>
    </div>
  )
}
