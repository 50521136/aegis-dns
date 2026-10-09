import { Link } from 'react-router-dom'
import { motion } from 'framer-motion'
import {
  ArrowRight,
  BarChart3,
  Blocks,
  Globe2,
  Lock,
  RefreshCw,
  ShieldCheck,
  Zap,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/hooks/useAuth'

const features = [
  {
    icon: Globe2,
    title: '三种接入协议',
    desc: 'DoH / DoT / 普通 UDP·TCP 53，一个专属子域名全部搞定，无需额外配置。',
  },
  {
    icon: ShieldCheck,
    title: '规则引擎',
    desc: '拦截、放行、A/AAAA/CNAME 改写，Trie 后缀匹配，单次查询 < 1ms。',
  },
  {
    icon: RefreshCw,
    title: '订阅自动更新',
    desc: '支持 Adblock / Hosts / DNSMasq 等格式，定时拉取，失败自动保留旧规则。',
  },
  {
    icon: Lock,
    title: '加密与隔离',
    desc: '每个用户独立规则与缓存，多租户完全隔离，通配证书自动签发续期。',
  },
  {
    icon: BarChart3,
    title: '实时统计',
    desc: '查询总量、拦截率、缓存命中率、TOP 域名与逐条查询日志一目了然。',
  },
  {
    icon: Zap,
    title: '故障隔离',
    desc: 'DNS 引擎与 API 双进程解耦，API 宕机也不影响解析，稳定可用。',
  },
]

const container = {
  hidden: {},
  show: { transition: { staggerChildren: 0.05 } },
}
const item = {
  hidden: { opacity: 0, y: 12 },
  show: { opacity: 1, y: 0, transition: { duration: 0.3, ease: 'easeOut' } },
}

export default function Landing() {
  const { isAuthenticated } = useAuth()

  return (
    <div className="mx-auto w-full max-w-6xl px-4 md:px-6">
      {/* Hero */}
      <section className="flex flex-col items-center py-16 text-center md:py-24">
        <motion.div
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, ease: 'easeOut' }}
          className="inline-flex items-center gap-2 rounded-full border border-border bg-card px-4 py-1.5 text-xs font-medium text-muted-foreground shadow-sm"
        >
          <span className="flex size-2 rounded-full bg-success" />
          开源 · 自托管 · 无需 Docker
        </motion.div>

        <motion.h1
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, delay: 0.05, ease: 'easeOut' }}
          className="mt-6 max-w-3xl text-4xl font-bold leading-tight tracking-tight md:text-5xl"
        >
          你的专属加密 DNS，
          <br className="hidden sm:block" />
          <span className="bg-gradient-to-r from-primary to-accent bg-clip-text text-transparent">
            干净、快速、可控
          </span>
        </motion.h1>

        <motion.p
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, delay: 0.1, ease: 'easeOut' }}
          className="mt-5 max-w-2xl text-base text-muted-foreground md:text-lg"
        >
          注册即获得一个专属子域名，通过 DoT / DoH / 普通 DNS 接入，自定义拦截规则与订阅列表，
          全平台加密解析，远离广告与追踪。
        </motion.p>

        <motion.div
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, delay: 0.15, ease: 'easeOut' }}
          className="mt-8 flex flex-col gap-3 sm:flex-row"
        >
          {isAuthenticated ? (
            <Button asChild size="lg">
              <Link to="/app">
                进入控制台
                <ArrowRight />
              </Link>
            </Button>
          ) : (
            <>
              <Button asChild size="lg">
                <Link to="/register">
                  免费开始
                  <ArrowRight />
                </Link>
              </Button>
              <Button asChild size="lg" variant="outline">
                <Link to="/login">已有账号，登录</Link>
              </Button>
            </>
          )}
        </motion.div>

        <div className="mt-12 w-full max-w-2xl">
          <div className="rounded-2xl border border-border/50 bg-card p-1 shadow-md">
            <div className="flex items-center gap-1.5 px-3 py-2">
              <span className="size-3 rounded-full bg-danger/70" />
              <span className="size-3 rounded-full bg-warning/70" />
              <span className="size-3 rounded-full bg-success/70" />
              <span className="ml-2 text-xs text-muted-foreground">终端</span>
            </div>
            <pre className="overflow-x-auto rounded-xl bg-muted/50 p-4 text-left font-mono text-xs leading-relaxed md:text-sm">
              <code>
                <span className="text-muted-foreground">$ </span>dig @k7m2p9xq4a.dns.example.com example.com
                {'\n'}
                <span className="text-success">;; -&gt;&gt; HEADER&lt;&lt;- opcode: QUERY, status: NOERROR</span>
                {'\n'}
                <span className="text-muted-foreground">;; ANSWER SECTION:</span>
                {'\n'}example.com.  300  IN  A  93.184.216.34
              </code>
            </pre>
          </div>
        </div>
      </section>

      {/* Features */}
      <section className="pb-20">
        <div className="mb-10 text-center">
          <h2 className="text-3xl font-bold tracking-tight">为什么选择 DNSForge</h2>
          <p className="mt-2 text-sm text-muted-foreground">为自托管场景打造的现代 DNS 服务</p>
        </div>

        <motion.div
          variants={container}
          initial="hidden"
          whileInView="show"
          viewport={{ once: true, margin: '-60px' }}
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3"
        >
          {features.map((f) => {
            const Icon = f.icon
            return (
              <motion.div
                key={f.title}
                variants={item}
                className="rounded-2xl border border-border/50 bg-card p-6 shadow-sm transition-all duration-200 hover:-translate-y-0.5 hover:shadow-md"
              >
                <div className="flex size-11 items-center justify-center rounded-xl bg-primary/10 text-primary">
                  <Icon className="size-5" />
                </div>
                <h3 className="mt-4 text-base font-semibold">{f.title}</h3>
                <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{f.desc}</p>
              </motion.div>
            )
          })}
        </motion.div>
      </section>

      {/* CTA */}
      <section className="pb-20">
        <div className="relative overflow-hidden rounded-3xl border border-border/50 bg-gradient-to-br from-primary/10 via-card to-accent/10 p-8 text-center md:p-14">
          <div className="mx-auto flex size-14 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-md">
            <Blocks className="size-7" />
          </div>
          <h2 className="mt-6 text-3xl font-bold tracking-tight">三步开启你的加密 DNS</h2>
          <p className="mx-auto mt-3 max-w-xl text-sm text-muted-foreground">
            注册账号 → 复制专属地址 → 在设备中填入。全程不到一分钟。
          </p>
          <Button asChild size="lg" className="mt-8">
            <Link to={isAuthenticated ? '/app/setup' : '/register'}>
              {isAuthenticated ? '查看我的 DNS 配置' : '立即注册'}
              <ArrowRight />
            </Link>
          </Button>
        </div>
      </section>
    </div>
  )
}
