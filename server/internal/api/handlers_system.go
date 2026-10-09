package api

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/50521136/aegis-dns/server/internal/api/webdist"
	"github.com/50521136/aegis-dns/server/internal/version"
)

// handleHealthz 是存活探针（文档 9.8）。
//
// 只要进程还能响应就返回 200 —— 不检查数据库、不检查 dnsd，
// 因为「apid 活着但数据库暂时锁住」不该触发编排系统重启整个服务。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// handleReadyz 是就绪探针。
//
// 与 healthz 的区别：这里真的去碰数据库与快照文件，
// 因为「就绪」意味着能正常处理业务请求。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	problems := []string{}

	if _, err := s.store.CountUsers(ctx); err != nil {
		problems = append(problems, "数据库不可用: "+err.Error())
	}
	if _, err := os.Stat(s.cfg.SnapshotPath()); err != nil {
		problems = append(problems, "配置快照不存在: "+err.Error())
	}

	if len(problems) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "not_ready",
			"problems": problems,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := version.Get()
	v.Component = "apid"
	writeJSON(w, http.StatusOK, map[string]any{
		"component":  v.Component,
		"version":    v.Version,
		"commit":     v.Commit,
		"build_time": v.BuildTime,
		"go_version": v.GoVersion,
		"platform":   v.Platform,
		"frontend":   frontendEmbedded(),
	})
}

// frontendEmbedded 报告二进制里是否带上了真实前端。
//
// 抽成变量便于测试替换；默认走 webdist 的探测。
var frontendEmbedded = func() bool { return webdist.HasIndex() }

// handleDocs 提供一个零依赖的 API 说明页（文档第十二章）。
//
// 用内嵌的静态 HTML 而不是 Swagger UI：后者需要额外 CDN 资源，
// 在内网/受限网络下会加载失败，反而变成一个新的故障面。
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(docsHTML))
}

const docsHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>DNSForge API</title>
<style>
 :root{color-scheme:dark}
 body{margin:0;padding:40px 24px;background:#0B1020;color:#E5E7EB;
   font:400 14px/1.7 Inter,"PingFang SC","Microsoft YaHei",system-ui,sans-serif}
 .wrap{max-width:900px;margin:0 auto}
 h1{font-size:30px;font-weight:700;margin:0 0 4px}
 .sub{color:#94A3B8;font-size:13px;margin-bottom:28px}
 h2{font-size:20px;font-weight:600;margin:32px 0 12px;padding-bottom:8px;border-bottom:1px solid #1E293B}
 .ep{background:#141A2E;border:1px solid #1E293B;border-radius:12px;padding:12px 16px;margin:8px 0;
   display:flex;gap:12px;align-items:baseline;flex-wrap:wrap}
 .m{font-weight:600;font-size:12px;padding:2px 8px;border-radius:8px;min-width:56px;text-align:center}
 .get{background:#10B98122;color:#34D399}.post{background:#6366F122;color:#818CF8}
 .put{background:#F59E0B22;color:#FBBF24}.del{background:#EF444422;color:#F87171}
 code{font-family:"JetBrains Mono",ui-monospace,Menlo,monospace;font-size:13px;color:#A5B4FC}
 .d{color:#94A3B8;font-size:13px;flex:1 1 260px}
 .auth{color:#FBBF24;font-size:11px;border:1px solid #FBBF2455;border-radius:6px;padding:1px 6px}
 a{color:#818CF8}
</style></head><body><div class="wrap">
<h1>DNSForge API</h1>
<div class="sub">Base URL: <code>/api/v1</code> · 认证：<code>Authorization: Bearer &lt;token&gt;</code> ·
 前端走 JWT（自动刷新），脚本与第三方调用走 <code>agx_</code> 前缀的长期 API Token</div>

<h2>系统</h2>
<div class="ep"><span class="m get">GET</span><code>/healthz</code><span class="d">存活探针</span></div>
<div class="ep"><span class="m get">GET</span><code>/readyz</code><span class="d">就绪探针（检查数据库与快照）</span></div>
<div class="ep"><span class="m get">GET</span><code>/version</code><span class="d">版本信息</span></div>
<div class="ep"><span class="m get">GET</span><code>/update/check</code><span class="d">检查更新</span><span class="auth">需认证</span></div>

<h2>认证</h2>
<div class="ep"><span class="m post">POST</span><code>/auth/register</code><span class="d">注册，返回用户 + 专属 DNS 配置 + 令牌</span></div>
<div class="ep"><span class="m post">POST</span><code>/auth/login</code><span class="d">登录</span></div>
<div class="ep"><span class="m post">POST</span><code>/auth/refresh</code><span class="d">刷新令牌（轮转，旧的立即失效）</span></div>
<div class="ep"><span class="m post">POST</span><code>/auth/logout</code><span class="d">撤销刷新令牌</span></div>

<h2>账号</h2>
<div class="ep"><span class="m get">GET</span><code>/me</code><span class="d">当前用户</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m put">PUT</span><code>/me/password</code><span class="d">修改密码（会撤销全部令牌）</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/me/dns-config</code><span class="d">DNS 接入配置 + 证书指纹 + 分平台指引</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/me/export</code><span class="d">导出规则与订阅定义</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/me/import</code><span class="d">导入配置</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/me/tokens</code><span class="d">长期 API Token 列表</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/me/tokens</code><span class="d">创建 API Token（原文仅返回一次）</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m del">DEL</span><code>/me/tokens/{id}</code><span class="d">撤销 API Token</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/me/ips</code><span class="d">来源 IP 归属（UDP/TCP 识别用）</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/me/ips</code><span class="d">登记来源 IP / CIDR</span><span class="auth">需认证</span></div>

<h2>规则</h2>
<div class="ep"><span class="m get">GET</span><code>/rules</code><span class="d">列表（page / page_size / q / kind / enabled）</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/rules</code><span class="d">新建规则</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/rules/import</code><span class="d">批量导入文本，自动识别格式</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/rules/validate</code><span class="d">只校验语法，不保存</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m put">PUT</span><code>/rules/{id}</code><span class="d">更新规则</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m del">DEL</span><code>/rules/{id}</code><span class="d">删除规则</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m del">DEL</span><code>/rules</code><span class="d">批量删除 {ids:[...]}</span><span class="auth">需认证</span></div>

<h2>订阅</h2>
<div class="ep"><span class="m get">GET</span><code>/subscriptions</code><span class="d">列表</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/subscriptions</code><span class="d">新建（会立即拉取一次）</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/subscriptions/presets</code><span class="d">推荐订阅源</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/subscriptions/{id}/refresh</code><span class="d">立即刷新</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m post">POST</span><code>/subscriptions/refresh-all</code><span class="d">刷新我的全部订阅</span><span class="auth">需认证</span></div>

<h2>统计</h2>
<div class="ep"><span class="m get">GET</span><code>/stats/summary</code><span class="d">?range=24h</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/stats/timeseries</code><span class="d">?range=7d&amp;interval=1h</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/stats/top</code><span class="d">?range=24h&amp;limit=10&amp;type=all|blocked</span><span class="auth">需认证</span></div>
<div class="ep"><span class="m get">GET</span><code>/stats/querylog</code><span class="d">?limit&amp;offset&amp;domain&amp;action</span><span class="auth">需认证</span></div>

<h2>管理（需 admin 角色）</h2>
<div class="ep"><span class="m get">GET</span><code>/admin/users</code><span class="d">用户列表</span><span class="auth">admin</span></div>
<div class="ep"><span class="m put">PUT</span><code>/admin/users/{id}</code><span class="d">启用/禁用/改角色</span><span class="auth">admin</span></div>
<div class="ep"><span class="m del">DEL</span><code>/admin/users/{id}</code><span class="d">删除用户</span><span class="auth">admin</span></div>
<div class="ep"><span class="m get">GET</span><code>/admin/settings</code><span class="d">全局设置</span><span class="auth">admin</span></div>
<div class="ep"><span class="m put">PUT</span><code>/admin/settings</code><span class="d">修改全局设置</span><span class="auth">admin</span></div>
<div class="ep"><span class="m post">POST</span><code>/admin/snapshot/rebuild</code><span class="d">强制重建配置快照</span><span class="auth">admin</span></div>
<div class="ep"><span class="m get">GET</span><code>/admin/system</code><span class="d">系统状态</span><span class="auth">admin</span></div>
<div class="ep"><span class="m post">POST</span><code>/admin/update/apply</code><span class="d">应用更新 {component}</span><span class="auth">admin</span></div>
<div class="ep"><span class="m get">GET</span><code>/admin/audit</code><span class="d">审计日志</span><span class="auth">admin</span></div>

<h2>错误格式</h2>
<pre style="background:#141A2E;border:1px solid #1E293B;border-radius:12px;padding:16px;overflow-x:auto"><code>{
  "error": {
    "code": "INVALID_RULE_SYNTAX",
    "message": "规则语法错误：缺少域名",
    "details": { "input": "||^" }
  }
}</code></pre>

<h2>规则语法</h2>
<pre style="background:#141A2E;border:1px solid #1E293B;border-radius:12px;padding:16px;overflow-x:auto"><code>||ads.example.com^                     拦截该域及其所有子域
@@||safe.example.com^                  白名单例外（优先级最高）
ads.example.com                        裸域名，等价于 ||ads.example.com^
0.0.0.0 ads.example.com                hosts 格式
||ads.example.com^$dnstype=AAAA        仅拦截指定记录类型
||old.example.com^$dnsrewrite=1.2.3.4  改写 A 记录
*.dev.local^$dnsrewrite=127.0.0.1      通配改写
# 或 ! 开头                             注释</code></pre>

<p style="color:#94A3B8;font-size:13px;margin-top:32px">
  DNSForge —— 多租户加密 DNS 服务 · DoT / DoH / UDP·TCP
</p>
</div></body></html>`
