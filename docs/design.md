# DNSForge 系统设计文档

> 多租户加密 DNS 服务 —— 支持 DoT / DoH / 普通 UDP·TCP，用户注册后自动分配专属子域名，规则与订阅按租户完全隔离。
> 前后端分离架构，DNS 引擎与 Web API 双进程解耦。

| 项 | 内容 |
|---|---|
| 文档版本 | v1.0 |
| 项目代号 | DNSForge |
| 后端 | Go 1.21 |
| 前端 | React 18 + TypeScript + Vite 5 + Tailwind CSS + shadcn/ui |
| 存储 | SQLite（WAL 模式） |
| 部署 | GitHub Releases 多平台二进制 + systemd 本机直接运行 |

---

## 目录

1. [需求与目标](#一需求与目标)
2. [总体架构](#二总体架构)
3. [多租户子域名识别机制](#三多租户子域名识别机制)
4. [DNS 查询管线](#四dns-查询管线)
5. [规则引擎设计](#五规则引擎设计)
6. [订阅系统设计](#六订阅系统设计)
7. [配置快照与热重载](#七配置快照与热重载)
8. [数据库设计](#八数据库设计)
9. [REST API 契约](#九rest-api-契约)
10. [认证与安全](#十认证与安全)
11. [前端设计规范](#十一前端设计规范)
12. [UI 框架选型](#十二ui-框架选型)
13. [发布与部署架构](#十三发布与部署架构)
14. [在线更新机制](#十四在线更新机制)
15. [技术风险与对策](#十五技术风险与对策)
16. [扩展路线](#十六扩展路线)

---

## 一、需求与目标

### 1.1 核心需求

| # | 需求 | 说明 |
|---|---|---|
| R1 | 多协议 DNS 服务 | 支持 DoT、DoH、普通 UDP/TCP，参考 AdGuard Home 机制 |
| R2 | 自定义规则 | 用户可配置拦截、放行、A/CNAME 改写规则 |
| R3 | 白/黑名单订阅 | 支持订阅外部规则列表，定时自动更新 |
| R4 | 全新 UI | WorkBuddy App 风格，同时适配 PC 与手机 |
| R5 | 前后端分离 | 后端处理一切，前端仅调 REST API |
| R6 | 用户注册登录 | 前端注册，后端分配专属 ID 作为子域名 |
| R7 | 故障隔离 | **前端/API 挂了，DNS 必须照常解析** |
| R8 | 在线更新 | 后端与前端均支持在线更新 |

### 1.2 非功能目标

| 指标 | 目标 |
|---|---|
| 单机解析能力 | ≥ 10,000 QPS（缓存命中场景） |
| 规则匹配延迟 | < 1ms（单次查询） |
| 配置生效延迟 | < 2s（从 API 落库到 DNS 生效） |
| 故障隔离 | API 停机对解析可用性影响 = 0 |
| 冷启动时间 | < 3s（含配置加载） |

### 1.3 设计原则

> **P1 故障隔离优先**：DNS 解析路径上不允许存在任何可选的、外部的、会阻塞的依赖。
>
> **P2 运行期零 IO**：`dnsd` 启动时把配置全部载入内存，运行期不读数据库、不读配置文件、不发同步 RPC。
>
> **P3 单写者原则**：每份数据只有一个写入者，避免并发写冲突。
>
> **P4 优雅降级**：任何外部依赖（订阅源、上游 DNS）失败时，保留最后一次成功状态，不清空。

---

## 二、总体架构

### 2.1 组件拓扑

```
┌───────────────────────────── 宿主机（Linux / macOS / Windows）──────────────────────────┐
│                                                                                         │
│   DNS 客户端                     dnsd（单二进制，下载即运行 ./dnsd）                       │
│ ┌──────────────┐                ┌─────────────────────────────────────┐                  │
│ │ UDP/TCP :53  │───────────────►│  Registry   用户识别                 │                  │
│ │ DoT     :853 │───────────────►│  RuleEngine 后缀 Trie 匹配           │                  │
│ │ DoH    :443* │───────────────►│  Resolver   上游池转发               │                  │
│ └──────────────┘                │  Cache      响应缓存                 │                  │
│                                 │  StatsBuffer 内存聚合                │                  │
│   浏览器                        └───────────────┬─────────────────────┘                  │
│ ┌──────────────┐                                │ 只读 runtime/config.json                │
│ │ https://dns  │   apid（单二进制，内嵌前端）    │ 只写 stats 批量落库                      │
│ │  .example.com│  ┌─────────────────────────────┴──┐                                     │
│ ├─────────────►│  │ chi Router + Token 认证          │                                     │
│ │ /api/*       │  │ 注册 / 登录 / 规则 / 订阅 / 统计  │                                     │
│ │ / (SPA)      │  │ go:embed 内嵌 web/dist 静态资源  │                                     │
│ └──────────────┘  └──────────────┬─────────────────┘                                     │
│                                  ▼                                                       │
│                            ┌───────────┐     数据目录 ./data/                            │
│                            │  SQLite   │     ├── aegis.db（WAL）                          │
│                            └───────────┘     ├── runtime/config.json                      │
│                                              └── certs/（通配证书，可选 ACME 自动签）      │
└─────────────────────────────────────────────────────────────────────────────────────────┘

                 上游 DNS: 1.1.1.1 / 8.8.8.8 / DoH / DoT
```

> **发布方式**：GitHub Actions 在打 tag 时自动交叉编译多平台二进制并发布到 GitHub Releases，服务器上 `wget` 下载 → `chmod +x` → 运行，**无需 Docker、无需编译环境**。
> **DoH 直连 443**：`dnsd` 自带 TLS（加载通配证书，可选内置 ACME via DNS-01 自动续期），不需要 Caddy/Nginx；`apid` 的 HTTPS 由 `apid` 自身或可选反代提供。

### 2.2 进程职责划分

| 进程 | 二进制 | 职责 | 权限 |
|---|---|---|---|
| `dnsd` | `cmd/dnsd` | DNS 解析、规则匹配、订阅加载、统计采集、DoH TLS 终止 | 读快照、写统计、监听 53/853/443 |
| `apid` | `cmd/apid` | 用户管理、规则/订阅 CRUD、统计查询、快照生成、**托管内嵌前端** | 读写 SQLite、写快照、监听 8080 |
| （可选）反代 | Caddy/Nginx | 仅在需要多站点共享 443 或自动 HTTPS 时使用 | 监听 80/443 |

> **前端如何交付**：前端构建产物 `web/dist` 通过 `go:embed` 打进 `apid` 二进制。发布一个版本 = Release 里两个二进制文件，**前端"部署"随 apid 启动自动完成**，彻底消灭"前端挂了"这个故障面。

### 2.3 解耦约束（强制）

> ⚠️ **架构红线**：以下约束在实现中必须严格遵守，任何一条被破坏都会导致 R7 需求失效。

| 编号 | 约束 | 违反后果 |
|---|---|---|
| C1 | `dnsd` 与 `apid` **禁止**任何形式的 RPC / HTTP 互调 | API 宕机直接导致解析失败 |
| C2 | `dnsd` **运行期禁止**查询 SQLite | 数据库锁导致解析阻塞 |
| C3 | `dnsd` **运行期禁止**同步等待外部网络（订阅、证书除外，均异步） | 网络抖动导致解析超时 |
| C4 | 53/853/443 端口**禁止**依赖任何反代（dnsd 直接监听） | 反代重启导致 DNS 中断 |
| C5 | 规则匹配失败**必须** fail-open（放行到上游），不得 fail-closed | 规则异常导致全网 DNS 瘫痪 |

### 2.4 故障影响矩阵

| 故障组件 | DNS 解析 | API | Web UI | 恢复方式 |
|---|---|---|---|---|
| `apid` 停止 | ✅ 正常 | ❌ 不可用 | ❌ 页面 404（apid 同时托管前端） | 重启 apid / systemd 自动拉起 |
| 反代（可选）停止 | ✅ 53/853/443 正常 | ⚠️ 需直连 8080 | ⚠️ 需直连 8080 | 重启反代 |
| `dnsd` 停止 | ❌ 中断 | ✅ 正常 | ✅ 正常 | 重启 dnsd |
| SQLite 锁定 | ✅ 正常 | ⚠️ 写操作失败 | ⚠️ 部分功能异常 | 等待锁释放 |
| 上游 DNS 全挂 | ⚠️ 缓存命中可用，未命中 SERVFAIL | ✅ 正常 | ✅ 正常 | 上游恢复 |
| 订阅源不可达 | ✅ 使用上次规则 | ✅ 正常 | ✅ 正常 | 下次调度重试 |

### 2.5 数据流

**写入路径（配置变更）**

```
用户在 UI 上修改规则
    │
    ▼
[前端] POST /api/v1/rules
    │
    ▼
[apid] 校验 → 写入 SQLite (rules 表)
    │
    ▼
[apid] config_version += 1  (SQLite meta 表)
    │
    ▼
[apid] 重新生成完整配置快照，原子写入 runtime/config.json
       (写 config.json.tmp → fsync → rename)
    │
    ▼
[dnsd] fsnotify 监听到 WRITE 事件（或 30s 轮询发现版本变化）
    │
    ▼
[dnsd] 解析 JSON → 构建新的 RuleSet → atomic.Pointer.Store()
    │
    ▼
新规则对后续查询立即生效（约 < 2s）
```

**读取路径（DNS 查询）**

```
DNS 查询到达 (UDP/DoT/DoH)
    │
    ▼
识别用户 (SNI / Host / 来源 IP / EDNS)
    │
    ▼
查缓存 → 命中则直接返回（缓存按用户隔离）
    │
    ▼
规则引擎匹配 (Trie)
    │
    ├─ 命中 allow  → 放行上游
    ├─ 命中 rewrite→ 构造改写应答
    ├─ 命中 block  → 按 blocking_mode 返回
    └─ 未命中      → 转发上游
    │
    ▼
写入内存统计缓冲（异步，不阻塞应答）
```

---

## 三、多租户子域名识别机制

这是本系统的核心设计。管理员配置一个主域名（如 `dns.example.com`），每个注册用户获得一个 `client_id`，其专属接入地址由该 ID 派生。

### 3.1 client_id 生成规则

| 属性 | 值 |
|---|---|
| 生成时机 | 用户注册时 |
| 长度 | 10 字符 |
| 字符集 | 小写 a-z + 数字 2-7（base32 无填充后截断，天然避开 0/1/8/9 混淆） |
| 空间 | 32^10 ≈ 1.1 × 10^15 |
| 唯一性 | 数据库 UNIQUE 约束 + 冲突重试（最多 5 次） |
| 示例 | `k7m2p9xq4a` |
| 校验规则 | `^[a-z2-7]{10}$`，长度 4-32 |

> **为什么用 base32 而非 base62**：base32 无大小写歧义，可直接用于 DNS 标签（不区分大小写）、SNI、Host，且在日志/截图中不易混淆。

### 3.2 各协议识别方式

用户 `k7m2p9xq4a` 的专属接入地址：

| 协议 | 专属地址 | 识别字段 | 识别位置 |
|---|---|---|---|
| **DoH** | `https://k7m2p9xq4a.dns.example.com/dns-query` | `Host` 请求头首段 | `dnsd` 的 HTTP handler |
| **DoT** | `k7m2p9xq4a.dns.example.com:853` | TLS **SNI** 首段 | `tls.Config.GetCertificate` 回调 |
| **UDP/TCP 53** | 无域名（用户绑定来源 IP） | 来源 IP / CIDR | `Registry.byIP` 前缀树 |
| **DoH 路径式** | `https://dns.example.com/dns-query/k7m2p9xq4a` | URL 路径段 | HTTP handler（兼容不能改 Host 的客户端） |
| **EDNS 兜底** | 任意地址 + EDNS0 Option 65001 | option 载荷 | 查询解析阶段 |

**识别流程**

```go
// 伪代码
func IdentifyUser(req) *UserRuntime {
    // 1. DoH: Host 首段
    if host := req.Header.Get("Host"); host != "" {
        if uid := registry.LookupBySubdomain(host); uid != nil {
            return uid
        }
    }
    // 2. DoH 路径式: /dns-query/{client_id}
    if seg := pathClientID(req.URL.Path); seg != "" {
        if uid := registry.LookupByClientID(seg); uid != nil {
            return uid
        }
    }
    // 3. DoT: 连接建立时已通过 SNI 存入 context
    if uid := userFromContext(req.Context()); uid != nil {
        return uid
    }
    // 4. UDP/TCP: 来源 IP
    if uid := registry.LookupByIP(req.RemoteAddr); uid != nil {
        return uid
    }
    // 5. EDNS Option 65001
    if uid := registry.LookupByEDNS(dnsMsg); uid != nil {
        return uid
    }
    // 6. 兜底：默认策略集
    return registry.Fallback
}
```

### 3.3 内存注册表结构

```go
// Registry 保存所有用户的运行时配置，是 dnsd 的核心数据结构。
// 通过 atomic.Pointer 整体替换实现无锁读 + 原子热更新。
type Registry struct {
    current atomic.Pointer[registrySnapshot]
}

type registrySnapshot struct {
    version    int64
    byClientID map[string]*UserRuntime   // "k7m2p9xq4a" -> runtime
    byIP       *cidr.Trie                // 来源 IP -> *UserRuntime
    fallback   *UserRuntime              // 默认策略
    upstreams  []*upstream.Upstream      // 全局默认上游池
    loadedAt   time.Time
}

type UserRuntime struct {
    UserID    string
    ClientID  string
    Enabled   atomic.Bool
    Rules     atomic.Pointer[RuleSet]    // 规则快照，可独立替换
    Upstreams atomic.Pointer[[]string]   // 用户自定义上游
    Stats     *StatsBucket               // 统计缓冲
}
```

**读路径无锁**：所有查询先 `registry.current.Load()` 拿到快照指针，之后只读。热更新时构建全新快照再 `Store()`，旧快照由 GC 回收。

### 3.4 通配证书方案

| 项 | 说明 |
|---|---|
| 证书内容 | `dns.example.com` + `*.dns.example.com` |
| 签发方式 | **DNS-01 challenge**（无需开放 80 端口验证），两种途径：① `dnsd` 内置 ACME（lego 库）自动申请续期；② 用户自行申请后配置证书路径 |
| 支持的 DNS 服务商 | Cloudflare、阿里云、DNSPod、AWS Route53 等（lego 原生支持 100+） |
| 存储路径 | `data/certs/`（内置 ACME 自动写入；手动模式由用户指定） |
| 加载方式 | `dnsd` 直接读取本地证书文件，fsnotify 监听变化，通过 `tls.Config.GetCertificate` 动态加载，**无需重启** |
| 热加载 | `dnsd` 用 fsnotify 监听证书目录，变化时通过 `tls.Config.GetCertificate` 动态加载，**无需重启** |
| 兜底 | 若证书不存在，`dnsd` 仅启动 UDP/TCP，DoT/DoH 打日志警告并跳过 |

> **重要提示**：若 DNS 服务商不支持 API，则需手动申请证书并以文件方式挂载，此时证书续期需人工介入。文档中明确标注此限制。

### 3.5 未识别请求的兜底策略

| 策略 | 行为 | 适用场景 |
|---|---|---|
| `passthrough`（默认） | 使用全局默认上游，不做任何规则过滤 | 公共开放解析 |
| `refused` | 直接返回 REFUSED | 私有部署，只允许注册用户使用 |
| `blocklist_only` | 只应用全局黑名单，不应用任何用户规则 | 混合模式 |

策略由管理员在全局设置中配置，可通过 API 修改并实时生效。

---

## 四、DNS 查询管线

### 4.1 管线阶段

```
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 0  协议接收                                                    │
│   UDP :53  │  TCP :53  │  DoT :853  │  DoH :8053                    │
│   统一转换为 *dns.Msg                                                │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 1  用户识别                                                    │
│   SNI → Host → Path → 来源 IP → EDNS → Fallback                     │
│   输出: *UserRuntime                                                 │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 2  前置校验                                                    │
│   - 用户是否启用 (enabled)                                           │
│   - 限流检查 (rate_limit_qps)                                        │
│   - 请求合法性 (QDCOUNT, 递归标志)                                    │
│   - 特殊域名 (localhost / 反查 PTR / 探针域名)                        │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 3  缓存查询                                                    │
│   Key = (user_id, qname_lower, qtype)                                │
│   命中且未过期 → 返回                                                │
│   命中但已过期 + optimistic → 返回并异步刷新                          │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 4  规则匹配  (Trie)                                            │
│   allow  → 放行                                                      │
│   rewrite→ 构造应答                                                  │
│   block  → 拦截应答                                                  │
│   miss   → 继续                                                      │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 5  上游解析                                                    │
│   用户自定义上游 ?? 全局上游                                          │
│   并发模式 (parallel) / 负载均衡 (fastest)                            │
│   超时 → 重试 → fallback                                             │
└──────────────────────────┬──────────────────────────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Stage 6  响应后处理                                                  │
│   - 写入缓存 (TTL 受 cache_min/max_ttl 约束)                          │
│   - 统计计数 (total / blocked / cached / allowed)                     │
│   - 查询日志 (环形缓冲，可开关)                                        │
│   - 异步批量落库 (每 60s)                                             │
└─────────────────────────────────────────────────────────────────────┘
```

### 4.2 并发模型

| 项 | 设计 |
|---|---|
| 单查询处理 | 一个 goroutine |
| 并发上限 | `max_goroutines`（默认 300），通过带缓冲 channel 实现信号量 |
| 超时 | 总超时 10s（可配置），单上游超时 3s |
| Panic 保护 | 每个查询 handler 外层 `defer recover()`，panic 后返回 SERVFAIL 并记录 |
| UDP 写回 | 使用 `dns.ResponseWriter`，天然并发安全 |

### 4.3 缓存设计

```go
type CacheKey struct {
    UserID string   // 用户隔离
    QName  string   // 小写规范化
    QType  uint16
}

type CacheEntry struct {
    Msg       *dns.Msg
    StoredAt  time.Time
    ExpiresAt time.Time   // 由最小 TTL 决定，受 min/max 约束
}

// 分片 LRU，降低锁竞争
type Cache struct {
    shards [16]*shard     // 按 hash(key) % 16 分片
    size   int            // 总容量上限
}

type shard struct {
    mu    sync.RWMutex
    items map[CacheKey]*list.Element
    lru   *list.List
}
```

**缓存策略**

| 项 | 值 |
|---|---|
| 容量 | `cache_size`（默认 10000 条） |
| TTL 取值 | `clamp(min(所有 RR 的 TTL), cache_min_ttl, cache_max_ttl)` |
| 负缓存 | NXDOMAIN / NODATA 也缓存，TTL 固定 60s |
| 淘汰 | LRU |
| 隔离 | 按 `user_id` 隔离，防止跨租户数据泄漏 |
| 不缓存 | 含 EDNS Client Subnet 的请求、DS/DNSKEY 记录 |

### 4.4 拦截应答模式

```go
type BlockingMode string

const (
    // 返回 NXDOMAIN（域名不存在）
    BlockingNXDOMAIN BlockingMode = "nxdomain"
    // 返回 0.0.0.0 / :: （推荐，客户端友好）
    BlockingNullIP   BlockingMode = "null_ip"
    // 返回 REFUSED
    BlockingRefused  BlockingMode = "refused"
    // 返回自定义 IP
    BlockingCustomIP BlockingMode = "custom_ip"
)
```

| 模式 | A 记录应答 | AAAA 记录应答 | 特点 |
|---|---|---|---|
| `nxdomain` | SOA | SOA | 符合语义，但某些客户端会重试 |
| `null_ip` | `0.0.0.0` | `::` | **推荐**，连接立即失败，无重试 |
| `refused` | RCODE=5 | RCODE=5 | 明确拒绝，但部分客户端降级到其他 DNS |
| `custom_ip` | 自定义 | 自定义 | 可指向内网陷阱主机，做拦截统计 |

---

## 五、规则引擎设计

### 5.1 支持的规则语法

| 语法 | 类型 | 说明 |
|---|---|---|
| `\|\|ads.example.com^` | block | 拦截该域及其所有子域 |
| `@@\|\|safe.example.com^` | allow | 白名单例外，优先级最高 |
| `ads.example.com` | block | 裸域名，等价于 `\|\|ads.example.com^` |
| `0.0.0.0 ads.example.com` | block | hosts 文件格式 |
| `127.0.0.1 ads.example.com` | block | hosts 文件格式 |
| `\|\|ads.example.com^$dnstype=AAAA` | block | 仅拦截指定记录类型（逗号分隔多个） |
| `\|\|old.example.com^$dnsrewrite=1.2.3.4` | rewrite_a | A 记录改写为指定 IPv4 |
| `\|\|old.example.com^$dnsrewrite=2001:db8::1` | rewrite_aaaa | AAAA 记录改写 |
| `\|\|old.example.com^$dnsrewrite=new.example.com` | rewrite_cname | CNAME 改写 |
| `*.dev.local^$dnsrewrite=127.0.0.1` | rewrite_a | 通配改写 |
| `#` 或 `!` 开头 | 注释 | 忽略 |

### 5.2 数据结构

```go
// RuleSet 是单个用户的完整规则集，构建后只读。
type RuleSet struct {
    // 后缀 Trie：键为反转域名段
    // "ads.example.com" -> 从根下行 com -> example -> ads
    // 节点标记决定匹配到该节点时（含其所有子域）的行为
    trie *TrieNode

    // 精确匹配表：O(1) 兜底，处理 Trie 未覆盖的边界情况
    exact map[string]Action

    // 改写表：预构建好 dns.RR，查询路径零分配
    // key 为 "domain/qtype"
    rewrites map[string][]dns.RR

    // 订阅规则合并后的快捷索引
    subAllow *TrieNode
    subBlock *TrieNode

    Stats RuleStats   // 规则条数统计，用于 UI 展示
}

type TrieNode struct {
    children map[string]*TrieNode  // 按域名标签分段
    action   Action                // 0=none 1=allow 2=block
    qtypes   uint16Bitmask         // 限定记录类型，0 表示全部
}

type Action uint8
const (
    ActionNone  Action = 0
    ActionAllow Action = 1
    ActionBlock Action = 2
)
```

**Trie 匹配示意**

```
规则: ||ads.example.com^

Trie 结构（根 → 叶）:
    [root]
      └── "com"
            └── "example"
                  └── "ads"  ← action=BLOCK

查询 "tracker.ads.example.com":
    分割为 ["com","example","ads","tracker"]
    反转遍历: com → example → ads  ← 命中 BLOCK，立即返回
    无需继续到 "tracker"
```

**复杂度**：O(标签数)，典型域名 3-4 次 map 查找，可忽略不计。

### 5.3 匹配优先级

```
┌────────────────────────────────────────────────────────────┐
│ 优先级 1: 用户自定义 allow（白名单）              ← 最高     │
│ 优先级 2: 用户自定义 rewrite（改写）                        │
│ 优先级 3: 用户自定义 block（拦截）                          │
│ 优先级 4: 订阅 allowlist                                   │
│ 优先级 5: 订阅 blocklist                                   │
│ 优先级 6: 未命中 → 转发上游                       ← 最低     │
└────────────────────────────────────────────────────────────┘
```

采用**首个命中即返回**（first-match-wins）策略，不做多规则聚合。

**设计理由**：

- 白名单必须绝对优先——用户显式放行的域名不能被任何订阅规则拦住（这是 AdGuard 的核心体验）
- 自定义规则优先于订阅——用户手动配置的意图强于批量订阅
- 改写优先于拦截——改写是更具体的意图

### 5.4 规则构建流程

```
apid 读取 DB (rules + sub_entries)
    │
    ▼
逐条解析 pattern → 规范化域名
    │
    ├─ 校验合法性（非法规则跳过并记录，不中断构建）
    │
    ▼
按 kind 分组，按优先级顺序插入
    │
    ├─ allow  → 插入 allowTrie
    ├─ block  → 插入 blockTrie
    └─ rewrite→ 构造 dns.RR，插入 rewrites map
    │
    ▼
序列化为 config.json 的 rules 段
    │
    ▼
dnsd 加载 → 构建 TrieNode（一次性，启动/更新时）
    │
    ▼
atomic.Pointer.Store() 原子替换
```

### 5.5 改写规则实现

改写规则在**构建期**预先生成 `dns.RR`，查询时直接返回：

```go
// 构建期：为每个改写规则预生成应答记录
func buildRewrite(domain string, target string) []dns.RR {
    if ip := net.ParseIP(target); ip != nil {
        if ip.To4() != nil {
            // A 记录
            return []dns.RR{&dns.A{
                Hdr: dns.RR_Header{Name: dns.Fqdn(domain), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
                A:   ip.To4(),
            }}
        }
        // AAAA 记录
        return []dns.RR{&dns.AAAA{...}}
    }
    // CNAME 记录
    return []dns.RR{&dns.CNAME{
        Hdr:    dns.RR_Header{Name: dns.Fqdn(domain), Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
        Target: dns.Fqdn(target),
    }}
}
```

**部分改写的处理**：若规则仅限定 `dnstype=A`，则查询 AAAA 时**不应用该规则**，继续转发上游（保持记录类型语义完整）。

### 5.6 规则集的序列化格式

`config.json` 中的规则段（`dnsd` 加载的格式）：

```json
{
  "version": 42,
  "generated_at": "2026-10-10T00:55:00Z",
  "defaults": {
    "domain": "dns.example.com",
    "upstreams": ["udp://1.1.1.1:53", "udp://8.8.8.8:53"],
    "blocking_mode": "null_ip",
    "custom_ip": "",
    "fallback_policy": "passthrough"
  },
  "users": [
    {
      "user_id": "usr_01H8X...",
      "client_id": "k7m2p9xq4a",
      "enabled": true,
      "upstreams": [],
      "ips": ["203.0.113.0/24"],
      "rules": [
        { "kind": "allow",   "pattern": "||safe.example.com^", "value": "", "qtypes": [] },
        { "kind": "block",   "pattern": "||ads.example.com^",  "value": "", "qtypes": [] },
        { "kind": "rewrite_a", "pattern": "||nas.home^", "value": "192.168.1.10", "qtypes": ["A"] }
      ],
      "sub_allow": ["good.example.org"],
      "sub_block": ["tracker.example.net", "analytics.example.io"]
    }
  ]
}
```

> **性能说明**：订阅条目可能达数十万条，因此快照中只存域名数组（紧凑），`dnsd` 加载时构建 Trie。若单用户订阅超 100 万条，考虑改为独立的二进制索引文件（见扩展路线）。

---

## 六、订阅系统设计

### 6.1 支持的订阅源格式

| 格式 | 示例 | 嗅探特征 |
|---|---|---|
| **Adblock** | `\|\|ads.example.com^`、`@@\|\|safe.com^` | 含 `\|\|` 或 `^` |
| **Hosts** | `0.0.0.0 ads.example.com` | 行首为 IP |
| **Domain list** | 每行一个域名 | 纯域名字符串 |
| **DNSMasq** | `address=/ads.example.com/0.0.0.0` | 含 `address=/` |
| **AdGuard** | `\|\|ads.com^$dnstype=A` | 含 `$` 修饰符 |

**自动嗅探算法**：读取前 50 行非注释行，统计各格式特征命中率，取最高者。

### 6.2 拉取与更新流程

```
定时调度器 (每 24h ± 随机 30min，防惊群)
    │
    ▼
检查 enabled = 1 的订阅
    │
    ▼
HTTP GET (带 If-None-Match / If-Modified-Since)
    ├─ 304 Not Modified → 跳过，更新 last_status
    ├─ 200 OK → 继续
    └─ 错误/超时 → 保留旧数据，记录 last_status = "error: xxx"
    │
    ▼
流式解析（逐行，限 20MB，超时 30s）
    │
    ▼
格式嗅探 → 逐行解析 → 域名规范化（小写、去尾点、去通配）
    │
    ▼
与现有 sub_entries 做增量 diff
    ├─ 新增 → INSERT
    ├─ 消失 → DELETE
    └─ 保留 → 不动
    │
    ▼
事务提交（单事务，保证原子性）
    │
    ▼
更新 subscriptions 元信息（etag / last_fetched / last_count / last_status）
    │
    ▼
bump config_version → 触发快照重建
```

### 6.3 调度器设计

```go
type Scheduler struct {
    store     *store.Store
    notifier  *snapshot.Writer
    interval  time.Duration  // 默认 24h
    jitter    time.Duration  // 默认 30min
}

func (s *Scheduler) Run(ctx context.Context) {
    for {
        // 加入抖动，避免所有实例同时请求
        delay := s.interval + time.Duration(rand.Int63n(int64(s.jitter)))
        select {
        case <-time.After(delay):
            s.refreshAll(ctx)
        case <-ctx.Done():
            return
        }
    }
}
```

**并发控制**：单个订阅的刷新操作串行化（同一 `sub_id` 加锁），不同订阅可并行（限并发 4）。

### 6.4 失败处理策略

| 场景 | 行为 |
|---|---|
| 网络超时 | 保留旧条目，`last_status = "error: timeout"` |
| HTTP 4xx | 保留旧条目，`last_status = "error: 404"`，连续失败 5 次标记告警 |
| HTTP 5xx | 保留旧条目，下次调度重试 |
| 内容格式错误 | 丢弃整个响应（不部分导入），保留旧条目 |
| 内容为空 | 视为异常，保留旧条目 |
| 内容过大 | 截断到 20MB 并记录警告 |

> **核心原则**：**订阅更新永远不能导致"当前生效的规则变空"**。宁可保留过期数据，也不能清空。这是 fail-safe 设计的体现。

### 6.5 内置推荐订阅

系统预置若干公共订阅源供用户一键添加（存储在代码常量中，非数据库）：

| 名称 | 类型 | 说明 |
|---|---|---|
| AdGuard DNS Filter | blocklist | 综合广告拦截 |
| StevenBlack Hosts | blocklist | 经典 hosts 合集 |
| AdAway | blocklist | 移动端广告 |
| OISD Basic | blocklist | 误杀率低 |
| 1Hosts Lite | blocklist | 轻量 |
| AdGuard Allowlist | allowlist | 官方白名单，防误杀 |

---

## 七、配置快照与热重载

### 7.1 为什么用文件而不是消息队列

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| Redis Pub/Sub | 实时性好 | 引入额外依赖；消息丢失无兜底；网络分区风险 | ❌ |
| gRPC 推送 | 类型安全、实时 | 引入 RPC，违反 C1 约束（apid 挂了 dnsd 也受影响） | ❌ |
| **文件 + fsnotify + 轮询** | 零依赖、天然持久化、重启可恢复、无 RPC | 延迟略高（< 2s） | ✅ **采用** |

**关键优势**：文件本身即是持久化状态。`dnsd` 重启后直接读文件即可恢复，不依赖 `apid` 在线。

### 7.2 原子写入

```go
// 原子写入：先写临时文件，fsync 后 rename。
// rename 在同一文件系统内是原子操作，保证读者要么看到旧文件要么看到新文件，
// 绝不会读到写了一半的内容。
func atomicWrite(path string, data []byte) error {
    tmp := path + ".tmp"
    f, err := os.Create(tmp)
    if err != nil {
        return err
    }
    defer os.Remove(tmp)

    if _, err := f.Write(data); err != nil {
        f.Close()
        return err
    }
    if err := f.Sync(); err != nil {   // 确保落盘
        f.Close()
        return err
    }
    if err := f.Close(); err != nil {
        return err
    }
    return os.Rename(tmp, path)        // 原子替换
}
```

### 7.3 双通道变更检测

```
通道 A（主）: fsnotify 监听文件所在目录
    ├─ 监听 WRITE / CREATE / RENAME 事件
    ├─ rename 会触发 CREATE 事件（新文件出现）
    ├─ 事件去抖：100ms 内的重复事件合并
    └─ 防抖窗口：500ms 内最多重载一次

通道 B（兜底）: 定时轮询
    ├─ 每 30s 读取文件头部 512 字节提取 version 字段
    ├─ 或 stat 文件的 mtime / size
    ├─ 若 version 变化 → 触发重载
    └─ 覆盖场景：inotify 句柄耗尽、跨容器 volume 事件不传递
```

> **为什么需要双通道**：NFS / 某些 overlay / 网络盘下，inotify 事件可能不可靠。轮询兜底是生产环境的必要保险。

### 7.4 重载流程

```go
func (l *Loader) Reload() error {
    // 1. 读取并解析新快照（失败则保留旧配置，记录错误）
    data, err := os.ReadFile(l.path)
    if err != nil { return err }

    var snap Snapshot
    if err := json.Unmarshal(data, &snap); err != nil {
        log.Error("快照解析失败，保留旧配置", "err", err)
        return err
    }

    // 2. 校验版本（防止重复加载）
    if snap.Version <= l.currentVersion() {
        return nil
    }

    // 3. 构建新的 Registry（Trie 构建最耗时，但不阻塞查询）
    newReg, err := buildRegistry(&snap)
    if err != nil {
        log.Error("规则构建失败，保留旧配置", "err", err)
        return err
    }

    // 4. 原子替换（此前所有查询仍用旧配置，无缝切换）
    l.registry.Store(newReg)

    log.Info("配置已重载", "version", snap.Version, "users", len(snap.Users))
    return nil
}
```

**重载期间的可用性**：构建新 Registry 的整个过程（可能数百毫秒），旧 Registry 持续服务查询，**零停机**。

### 7.5 快照构建优化

对大规模订阅（数十万条）的构建耗时优化：

| 优化 | 说明 |
|---|---|
| 预分配 map 容量 | 依据条目数估算，减少 rehash |
| 复用节点 | 相同后缀的域名合并到同一 Trie 节点 |
| 并行构建 | 按用户分片并行构建 Trie，最后合并 |
| 增量重建 | 仅变更用户重建（`snap.ChangedUsers` 字段标记） |
| 二进制索引 | 超 100 万条时改用预编译的二进制 Trie 文件（扩展路线） |

**增量重建设计**：

```go
type Snapshot struct {
    Version      int64    `json:"version"`
    GeneratedAt  time.Time `json:"generated_at"`
    Defaults     Defaults  `json:"defaults"`
    ChangedUsers []string  `json:"changed_users,omitempty"` // 为空表示全量
    Users        []UserSnap `json:"users"`
}
```

`dnsd` 若发现 `ChangedUsers` 非空，只重建这些用户的 RuleSet，其余复用旧对象（`atomic.Pointer` 指向旧 RuleSet）。

---

## 八、数据库设计

### 8.1 选型说明

| 候选 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| **SQLite (WAL)** | 零依赖、单文件、部署极简、读写并发尚可 | 写并发受限（单写者） | ✅ **采用** |
| PostgreSQL | 高并发写、强一致 | 需独立容器、运维成本 | 扩展路线 |
| MySQL | 生态成熟 | 同上 | ❌ |

**SQLite 适用性分析**：

- 写压力来源：`apid` 的配置变更（低频）、`dnsd` 的统计落库（每 60s 一次批量）
- 两者写频率都很低，WAL 模式下读不阻塞写、写不阻塞读，完全够用
- 若用户数 > 1000 且统计写入频繁，再迁移 PostgreSQL

**关键 PRAGMA**

```sql
PRAGMA journal_mode = WAL;        -- 读写并发
PRAGMA busy_timeout = 5000;       -- 写锁等待 5s 而非立即失败
PRAGMA synchronous = NORMAL;      -- WAL 下性能与安全平衡
PRAGMA foreign_keys = ON;         -- 启用外键约束
```

### 8.2 ER 关系

```
users ──┬──< user_ips          (1:N) 来源 IP 归属
        ├──< rules             (1:N) 自定义规则
        ├──< subscriptions ──< sub_entries  (1:N:N) 订阅及其条目
        └──< refresh_tokens    (1:N) 令牌撤销

query_rollup / top_domains / query_log   (按 user_id 关联，无外键以便快速清理)
meta / app_versions                       (全局)
```

### 8.3 完整 DDL

```sql
-- === 用户 ===
CREATE TABLE users (
  id            TEXT PRIMARY KEY,              -- UUID v7（时间有序）
  username      TEXT NOT NULL UNIQUE,
  email         TEXT,
  password_hash TEXT NOT NULL,                 -- bcrypt cost=12
  role          TEXT NOT NULL DEFAULT 'user',  -- admin | user
  client_id     TEXT NOT NULL UNIQUE,          -- 子域名标识，10 位 base32
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    INTEGER NOT NULL               -- Unix 秒
);
CREATE INDEX idx_users_client_id ON users(client_id);

-- === 来源 IP 归属（UDP/TCP 53 场景）===
CREATE TABLE user_ips (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  cidr    TEXT NOT NULL,                       -- "203.0.113.0/24" 或 "198.51.100.7/32"
  PRIMARY KEY (user_id, cidr)
);
CREATE INDEX idx_user_ips_cidr ON user_ips(cidr);

-- === 自定义规则 ===
CREATE TABLE rules (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,                    -- block|allow|rewrite_a|rewrite_aaaa|rewrite_cname
  pattern    TEXT NOT NULL,                    -- ||ads.com^ / example.com / *.dev.local
  value      TEXT NOT NULL DEFAULT '',         -- 改写目标
  qtypes     TEXT NOT NULL DEFAULT '',         -- "A,AAAA"，空表示全部
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_rules_user ON rules(user_id, enabled);

-- === 订阅 ===
CREATE TABLE subscriptions (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  list_type     TEXT NOT NULL,                 -- blocklist | allowlist
  name          TEXT NOT NULL,
  url           TEXT NOT NULL,
  format        TEXT NOT NULL DEFAULT 'auto',  -- auto|adblock|hosts|dnsmasq
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_etag     TEXT NOT NULL DEFAULT '',
  last_modified TEXT NOT NULL DEFAULT '',
  last_fetched  INTEGER NOT NULL DEFAULT 0,
  last_count    INTEGER NOT NULL DEFAULT 0,
  last_status   TEXT NOT NULL DEFAULT '',      -- ok | error:xxx
  created_at    INTEGER NOT NULL,
  UNIQUE(user_id, url)
);
CREATE INDEX idx_subs_user ON subscriptions(user_id, enabled);
CREATE INDEX idx_subs_enabled ON subscriptions(enabled);

-- === 订阅条目（解析后扁平化）===
CREATE TABLE sub_entries (
  sub_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  domain TEXT NOT NULL,                        -- 规范化域名（小写、无尾点）
  kind   TEXT NOT NULL,                        -- block | allow
  PRIMARY KEY (sub_id, domain)
);
CREATE INDEX idx_sub_entries_domain ON sub_entries(domain);

-- === 统计聚合（5 分钟粒度）===
CREATE TABLE query_rollup (
  user_id TEXT NOT NULL,
  ts      INTEGER NOT NULL,                    -- 5 分钟对齐的 Unix 秒
  total   INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  cached  INTEGER NOT NULL DEFAULT 0,
  allowed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, ts)
);
CREATE INDEX idx_rollup_ts ON query_rollup(ts);

-- === TOP 域名 ===
CREATE TABLE top_domains (
  user_id TEXT NOT NULL,
  ts      INTEGER NOT NULL,
  domain  TEXT NOT NULL,
  hits    INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, ts, domain)
);
CREATE INDEX idx_top_ts ON top_domains(ts);

-- === 查询日志 ===
CREATE TABLE query_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    TEXT NOT NULL,
  ts         INTEGER NOT NULL,
  domain     TEXT NOT NULL,
  qtype      TEXT NOT NULL DEFAULT '',
  rcode      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL DEFAULT '',          -- allowed|blocked|cached|rewritten
  client     TEXT NOT NULL DEFAULT '',          -- 来源 IP（脱敏后的 /24）
  latency_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_qlog_user_ts ON query_log(user_id, ts DESC);

-- === 元信息 ===
CREATE TABLE meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
-- 关键键：config_version / schema_version / app_version

-- === 在线更新清单 ===
CREATE TABLE app_versions (
  channel     TEXT PRIMARY KEY,                 -- stable | beta
  version     TEXT NOT NULL,
  url         TEXT NOT NULL,
  sha256      TEXT NOT NULL,
  notes       TEXT NOT NULL DEFAULT '',
  released_at INTEGER NOT NULL DEFAULT 0
);

-- === Refresh Token 撤销表 ===
CREATE TABLE refresh_tokens (
  jti        TEXT PRIMARY KEY,                  -- JWT ID
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_rt_user ON refresh_tokens(user_id);
CREATE INDEX idx_rt_expires ON refresh_tokens(expires_at);
```

### 8.4 数据清理策略

| 表 | 保留期 | 清理方式 |
|---|---|---|
| `query_rollup` | 90 天 | 每天定时删除 `ts < now - 90d` |
| `top_domains` | 90 天 | 同上 |
| `query_log` | 7 天（可配） | 每天定时删除 + 总量上限 100 万条 |
| `refresh_tokens` | 过期即删 | 每小时清理 `expires_at < now` |

### 8.5 迁移方案

采用**内嵌 SQL + 版本号**的极简迁移：

```
migrations/
├── 0001_init.sql
├── 0002_add_user_settings.sql
└── ...

meta.schema_version 记录当前版本
启动时扫描 migrations 目录，按序执行未应用的迁移
每个迁移在单事务内执行，失败回滚
```

---

## 九、REST API 契约

### 9.1 通用约定

| 项 | 约定 |
|---|---|
| Base URL | `https://dns.example.com/api/v1` |
| 认证 | `Authorization: Bearer <access_token>` |
| 请求体 | `application/json` |
| 响应体 | `application/json` |
| 时间格式 | RFC 3339（`2026-10-10T00:55:00Z`） |
| 分页 | `?page=1&page_size=20`，响应含 `total` / `page` / `page_size` |
| 错误格式 | 见下 |

**统一错误响应**

```json
{
  "error": {
    "code": "INVALID_RULE_SYNTAX",
    "message": "规则语法错误：缺少域名",
    "details": { "line": 3, "input": "||^" }
  }
}
```

**错误码表**

| HTTP | code | 说明 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求参数错误 |
| 400 | `INVALID_RULE_SYNTAX` | 规则语法错误 |
| 401 | `UNAUTHORIZED` | 未认证或 Token 失效 |
| 401 | `TOKEN_EXPIRED` | Token 过期（前端应触发刷新） |
| 403 | `FORBIDDEN` | 权限不足 |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `CONFLICT` | 资源冲突（如用户名已存在） |
| 422 | `VALIDATION_FAILED` | 语义校验失败 |
| 429 | `RATE_LIMITED` | 请求过于频繁 |
| 500 | `INTERNAL_ERROR` | 服务器内部错误 |

### 9.2 认证接口

#### POST /auth/register

注册新用户，后端分配 `client_id`。

**请求**

```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "S3cure!Passw0rd"
}
```

**响应 201**

```json
{
  "user": {
    "id": "usr_01H8XYZABC",
    "username": "alice",
    "email": "alice@example.com",
    "role": "user",
    "client_id": "k7m2p9xq4a",
    "enabled": true,
    "created_at": "2026-10-10T00:55:00Z"
  },
  "dns_config": {
    "client_id": "k7m2p9xq4a",
    "doh_url": "https://k7m2p9xq4a.dns.example.com/dns-query",
    "doh_url_path": "https://dns.example.com/dns-query/k7m2p9xq4a",
    "dot_host": "k7m2p9xq4a.dns.example.com",
    "dot_port": 853,
    "dot_addr": "k7m2p9xq4a.dns.example.com:853"
  },
  "tokens": {
    "access_token": "eyJhbGciOi...",
    "refresh_token": "eyJhbGciOi...",
    "expires_in": 900,
    "token_type": "Bearer"
  }
}
```

> **设计要点**：注册响应**直接返回完整的 DNS 配置**，前端可立即展示"你的专属 DNS 地址"，无需二次请求。这是提升首次体验的关键。

#### POST /auth/login

```json
// 请求
{ "username": "alice", "password": "S3cure!Passw0rd" }

// 响应 200 —— 结构同 register
```

#### POST /auth/refresh

```json
// 请求
{ "refresh_token": "eyJhbGciOi..." }

// 响应 200
{ "access_token": "...", "refresh_token": "...", "expires_in": 900, "token_type": "Bearer" }
```

#### POST /auth/logout

撤销当前 refresh token。

```json
// 请求
{ "refresh_token": "eyJhbGciOi..." }

// 响应 204 No Content
```

### 9.3 用户接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/me` | 当前用户信息 |
| PUT | `/me/password` | 修改密码 |
| GET | `/me/dns-config` | 获取 DNS 接入配置（含二维码数据） |
| GET | `/me/export` | 导出个人配置（规则+订阅，JSON） |
| POST | `/me/import` | 导入配置 |

#### GET /me/dns-config

**响应 200**

```json
{
  "client_id": "k7m2p9xq4a",
  "domain": "dns.example.com",
  "doh_url": "https://k7m2p9xq4a.dns.example.com/dns-query",
  "doh_url_path": "https://dns.example.com/dns-query/k7m2p9xq4a",
  "dot_host": "k7m2p9xq4a.dns.example.com",
  "dot_port": 853,
  "plain_dns": null,
  "cert_fingerprint_sha256": "AB:CD:EF:...",
  "platform_guides": [
    { "platform": "ios",     "steps": ["设置", "无线局域网", "配置 DNS", "手动", "添加服务器"] },
    { "platform": "android", "steps": ["设置", "网络和互联网", "私人 DNS", "输入主机名"] },
    { "platform": "windows", "steps": ["设置", "网络和 Internet", "以太网", "DNS 服务器分配"] },
    { "platform": "macos",   "steps": ["系统设置", "网络", "DNS", "加密 DNS"] }
  ]
}
```

### 9.4 规则接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | 规则列表（分页 + 搜索 + 类型筛选） |
| POST | `/rules` | 新建单条规则 |
| POST | `/rules/import` | 批量导入（文本，自动识别格式） |
| PUT | `/rules/{id}` | 更新规则 |
| DELETE | `/rules/{id}` | 删除规则 |
| DELETE | `/rules` | 批量删除（按 ID 数组） |
| POST | `/rules/validate` | 校验规则语法（不保存） |

#### POST /rules

```json
// 请求
{
  "kind": "block",
  "pattern": "||ads.example.com^",
  "value": "",
  "qtypes": [],
  "enabled": true
}

// 响应 201
{
  "id": "rul_01H8XYZ",
  "kind": "block",
  "pattern": "||ads.example.com^",
  "value": "",
  "qtypes": [],
  "enabled": true,
  "created_at": "2026-10-10T00:55:00Z"
}
```

#### POST /rules/import

```json
// 请求
{
  "content": "||ads.example.com^\n@@||safe.example.com^\n0.0.0.0 tracker.net\n",
  "format": "auto",
  "enabled": true
}

// 响应 200
{
  "created": 3,
  "skipped": 0,
  "errors": []
}
```

### 9.5 订阅接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/subscriptions` | 订阅列表 |
| POST | `/subscriptions` | 新建订阅 |
| PUT | `/subscriptions/{id}` | 更新订阅 |
| DELETE | `/subscriptions/{id}` | 删除订阅 |
| POST | `/subscriptions/{id}/refresh` | 立即刷新 |
| POST | `/subscriptions/refresh-all` | 刷新全部 |
| GET | `/subscriptions/presets` | 获取推荐订阅列表 |

#### POST /subscriptions

```json
// 请求
{
  "name": "AdGuard DNS Filter",
  "url": "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
  "list_type": "blocklist",
  "format": "auto",
  "enabled": true
}

// 响应 201
{
  "id": "sub_01H8XYZ",
  "name": "AdGuard DNS Filter",
  "url": "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
  "list_type": "blocklist",
  "format": "auto",
  "enabled": true,
  "last_count": 0,
  "last_status": "",
  "created_at": "2026-10-10T00:55:00Z"
}
```

#### POST /subscriptions/{id}/refresh

```json
// 响应 200
{
  "id": "sub_01H8XYZ",
  "last_count": 54213,
  "last_fetched": "2026-10-10T00:56:00Z",
  "last_status": "ok",
  "duration_ms": 1842
}
```

### 9.6 统计接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/stats/summary?range=24h` | 总览统计 |
| GET | `/stats/timeseries?range=7d&interval=1h` | 时间序列 |
| GET | `/stats/top?range=24h&limit=10&type=all\|blocked` | TOP 域名 |
| GET | `/stats/querylog?limit=100&offset=0&domain=&action=` | 查询日志 |

#### GET /stats/summary

```json
{
  "range": "24h",
  "total": 125430,
  "blocked": 18234,
  "cached": 98765,
  "allowed": 8431,
  "block_rate": 0.1454,
  "cache_hit_rate": 0.7874,
  "avg_latency_ms": 12,
  "unique_domains": 4213,
  "top_blocked_category": "ads"
}
```

### 9.7 管理接口（需 admin 角色）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/users` | 用户列表 |
| PUT | `/admin/users/{id}` | 修改用户（启用/禁用/角色） |
| DELETE | `/admin/users/{id}` | 删除用户 |
| GET | `/admin/settings` | 全局设置 |
| PUT | `/admin/settings` | 修改全局设置 |
| POST | `/admin/snapshot/rebuild` | 强制重建配置快照 |
| GET | `/admin/system` | 系统状态（版本/运行时长/连接数） |

### 9.8 系统接口

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/healthz` | 否 | 健康检查（liveness） |
| GET | `/readyz` | 否 | 就绪检查（readiness） |
| GET | `/version` | 否 | 版本信息 |
| GET | `/update/check` | 是 | 检查更新 |
| GET | `/docs` | 否 | API 文档页 |

#### GET /update/check

```json
{
  "current": "1.0.0",
  "latest": "1.1.0",
  "update_available": true,
  "notes": "新增 DoQ 支持；修复缓存越界问题",
  "url": "https://releases.example.com/dnsforge-1.1.0-linux-amd64.tar.gz",
  "sha256": "abc123...",
  "released_at": "2026-10-01T00:00:00Z"
}
```

### 9.9 中间件链

```
Request
  │
  ▼
[1] Recover         —— panic 捕获，返回 500 并记录堆栈
  │
  ▼
[2] RequestID       —— 生成/透传 X-Request-ID
  │
  ▼
[3] Logger          —— 结构化访问日志
  │
  ▼
[4] CORS            —— 跨域处理（生产环境限制 Origin）
  │
  ▼
[5] RateLimit       —— 单 IP 每分钟请求上限
  │
  ▼
[6] Auth (JWT)      —— 校验 Token；/auth/*、/healthz、/docs 白名单放行
  │
  ▼
[7] RequireRole     —— 管理接口校验 admin 角色
  │
  ▼
Handler
```

---

## 十、认证与安全

### 10.1 令牌设计

| 项 | Access Token | Refresh Token |
|---|---|---|
| 类型 | JWT (HS256) | JWT (HS256) |
| 有效期 | 15 分钟 | 30 天 |
| 存储位置（前端） | 内存（变量） | `httpOnly` Cookie 或 `localStorage` |
| 载荷 | `sub`(user_id), `role`, `exp`, `iat` | `sub`, `jti`, `exp`, `iat` |
| 撤销 | 不支持（短期自然过期） | 支持（`refresh_tokens` 表） |
| 用途 | 每次 API 请求 | 仅用于换取新 access token |

**长期 API Token（PAT）**

| 项 | 说明 |
|---|---|
| 用途 | 前后端对接的兜底方式：脚本、CLI、第三方面板直接调 API，免去 JWT 刷新流程 |
| 形态 | `agx_` 前缀随机串（32 字节），服务端只存 SHA-256 |
| 权限 | 继承所属用户权限；可单独撤销 |
| 展示 | 仅创建时返回一次 |

**前后端对接总结**：Web 前端走 JWT（自动刷新）；所有非浏览器调用走 PAT。两者都是 `Authorization: Bearer <token>`，apid 中间件统一校验，**前端不需要 Cookie / Session**。

**为什么 access token 短且不撤销**：减少数据库查询；15 分钟窗口内即使泄露风险也可控。撤销能力集中在 refresh token 上。

### 10.2 密码策略

| 项 | 策略 |
|---|---|
| 哈希算法 | bcrypt，cost = 12 |
| 最小长度 | 8 字符 |
| 复杂度要求 | 至少包含字母与数字 |
| 弱密码黑名单 | 拒绝常见密码（top 10000） |
| 传输 | 仅 HTTPS |
| 存储 | 永不出现在日志、API 响应中（`json:"-"`） |

### 10.3 安全措施清单

| 类别 | 措施 |
|---|---|
| **传输安全** | 全站 HTTPS（dnsd/apid 内置 TLS 或反代）；HSTS 头；DoT/DoH 加密 |
| **认证** | JWT + bcrypt；refresh token 轮转；登录失败限速 |
| **授权** | 资源归属校验（每个查询强制 `WHERE user_id = ?`）；admin 角色隔离 |
| **注入防护** | 全部使用参数化 SQL；规则内容做转义 |
| **XSS** | React 默认转义；CSP 头；避免 `dangerouslySetInnerHTML` |
| **CSRF** | Token 放 Header 而非 Cookie（天然免疫）；同源策略 + CORS 白名单 |
| **限流** | API 层按 IP；DNS 层按用户（`rate_limit_qps`） |
| **DoS 防护** | 请求体大小限制；订阅拉取大小限制（20MB）；查询超时 |
| **日志脱敏** | 密码、Token、完整 IP 不入日志 |
| **依赖安全** | 定期 `govulncheck` + `npm audit` |
| **进程安全** | systemd 加固（`ProtectSystem=strict` 等）；二进制静态编译；最小权限（仅 dnsd 需 `CAP_NET_BIND_SERVICE`） |
| **密钥管理** | JWT secret 通过环境变量注入；不写入镜像 |

### 10.4 多租户隔离要点

> ⚠️ **这是本系统最需要警惕的安全边界。**

| 隔离维度 | 实现 |
|---|---|
| 数据隔离 | 所有表带 `user_id`；所有查询强制过滤；禁止跨用户查询接口 |
| 规则隔离 | 每个用户独立 `RuleSet`；Trie 不共享 |
| 缓存隔离 | 缓存 key 包含 `user_id`，防止 A 用户命中 B 用户的改写结果 |
| 统计隔离 | `query_rollup` / `top_domains` 按 `user_id` 分区 |
| 识别隔离 | 识别失败一律落到 fallback，**绝不猜测归属** |
| 管理接口 | 非 admin 访问 `/admin/*` 返回 403 并记录审计日志 |

**必须避免的反模式**：

- ❌ 缓存 key 不含 `user_id` → 跨租户数据泄漏
- ❌ 用 client_id 直接作为 user_id → 泄露内部标识
- ❌ 规则合并到全局 Trie → 所有用户共享规则，失去隔离

---

## 十一、前端设计规范

### 11.1 页面结构

```
/                     Landing          落地页：介绍 + 特性 + CTA
/login                Login            登录
/register             Register         注册（成功后展示专属地址）
/app                  Dashboard        总览：统计卡 + 图表 + 最近拦截
/app/setup            DnsSetup         我的 DNS 配置（地址/二维码/分平台指引）
/app/rules            Rules            规则管理
/app/subscriptions    Subscriptions    订阅管理
/app/stats            Stats            统计分析
/app/logs             QueryLog         查询日志
/app/settings         Settings         账号设置 + 在线更新
/admin                Admin            管理后台（仅 admin）
```

### 11.2 组件树

```
App
├── ThemeProvider            (暗色/浅色)
├── QueryClientProvider      (TanStack Query)
├── AuthProvider             (登录态 + Token 刷新)
└── RouterProvider
    ├── PublicLayout
    │   ├── Landing
    │   ├── Login
    │   └── Register
    └── AppLayout
        ├── Desktop: Sidebar + TopBar
        ├── Mobile:  TopBar + BottomTabBar
        └── <Outlet />
            ├── Dashboard
            ├── DnsSetup
            ├── Rules
            ├── Subscriptions
            ├── Stats
            ├── QueryLog
            └── Settings
```

### 11.3 设计令牌（Design Tokens）

**配色**

| Token | 浅色 | 暗色 | 用途 |
|---|---|---|---|
| `--background` | `#F6F7FB` | `#0B1020` | 页面背景 |
| `--foreground` | `#111827` | `#E5E7EB` | 主文字 |
| `--card` | `#FFFFFF` | `#141A2E` | 卡片背景 |
| `--card-foreground` | `#111827` | `#E5E7EB` | 卡片文字 |
| `--primary` | `#6366F1` | `#818CF8` | 主色（靛蓝紫） |
| `--primary-foreground` | `#FFFFFF` | `#0B1020` | 主色上的文字 |
| `--accent` | `#22D3EE` | `#22D3EE` | 辅助色（青） |
| `--muted` | `#F3F4F6` | `#1E293B` | 弱化背景 |
| `--muted-foreground` | `#6B7280` | `#94A3B8` | 次要文字 |
| `--border` | `#E5E7EB` | `#1E293B` | 边框 |
| `--success` | `#10B981` | `#34D399` | 成功/放行 |
| `--warning` | `#F59E0B` | `#FBBF24` | 警告 |
| `--danger` | `#EF4444` | `#F87171` | 危险/拦截 |

**圆角与阴影**

| Token | 值 | 用途 |
|---|---|---|
| `--radius-sm` | 8px | 小元素（标签、徽章） |
| `--radius-md` | 12px | 按钮、输入框 |
| `--radius-lg` | 16px | 卡片 |
| `--radius-xl` | 24px | 大容器、弹窗 |
| `--shadow-sm` | `0 1px 2px rgba(0,0,0,.05)` | 静息卡片 |
| `--shadow-md` | `0 4px 12px rgba(0,0,0,.08)` | hover 卡片 |
| `--shadow-lg` | `0 12px 32px rgba(0,0,0,.12)` | 弹窗 |

**间距节奏**：4 / 8 / 12 / 16 / 24 / 32 / 48（px），卡片内边距 20-24px，卡片间距 16-24px。

**字体**

| 用途 | 字号 / 字重 / 行高 |
|---|---|
| 页面主标题 | 30px / 700 / 1.2 |
| 区块标题 | 20px / 600 / 1.3 |
| 卡片标题 | 16px / 600 / 1.4 |
| 正文 | 14px / 400 / 1.6 |
| 辅助文字 | 13px / 400 / 1.5 |
| 数字大屏 | 32px / 700 / 1.1（tabular-nums） |
| 等宽（域名/地址） | 13px / 400（`JetBrains Mono`） |

字体族：`Inter, "PingFang SC", "Microsoft YaHei", system-ui, sans-serif`

### 11.4 组件规范

**卡片**

```
rounded-2xl  bg-card  p-6  shadow-sm  border border-border/50
transition-all duration-200
hover: shadow-md  -translate-y-0.5
```

**按钮**

| 变体 | 样式 |
|---|---|
| primary | `bg-primary text-white rounded-xl h-10 px-5 hover:bg-primary/90 active:scale-[0.98]` |
| secondary | `bg-muted text-foreground rounded-xl h-10 px-5 hover:bg-muted/80` |
| ghost | `hover:bg-muted rounded-xl h-10 px-4` |
| danger | `bg-danger text-white rounded-xl h-10 px-5 hover:bg-danger/90` |
| icon | `size-10 rounded-xl hover:bg-muted` |

**输入框**

```
rounded-xl  h-10  px-3.5  border border-border  bg-background
focus: ring-2 ring-primary/30  border-primary  outline-none
transition-shadow duration-150
```

**开关（Switch）**：`w-11 h-6 rounded-full`，滑块 `size-5` 带 spring 动画（Framer Motion）。

**统计卡**

```
┌─────────────────────────────┐
│  [图标]  今日查询            │   ← 13px muted
│                             │
│  125,430                    │   ← 32px bold tabular-nums
│  ↑ 12.3% 较昨日              │   ← 12px success/danger
└─────────────────────────────┘
```

### 11.5 动效规范

| 场景 | 动效 | 时长 / 缓动 |
|---|---|---|
| 页面进入 | `opacity 0→1` + `translateY(8px)→0` | 150ms / ease-out |
| 卡片 hover | `shadow-sm→md` + `translateY(0→-2px)` | 200ms / ease-out |
| 按钮点击 | `scale(1→0.98→1)` | 120ms |
| 统计数字 | count-up 数字滚动 | 800ms / ease-out |
| 列表项进入 | 逐项 stagger 30ms | 200ms / ease-out |
| 弹窗 | `opacity` + `scale(0.96→1)` | 200ms / spring |
| 开关切换 | 滑块 spring 位移 | spring(stiffness 500, damping 30) |
| 图表入场 | 路径渐进绘制 | 600ms / ease-out |

> **可访问性**：所有动效必须尊重 `prefers-reduced-motion`，命中时禁用位移与缩放，仅保留透明度变化。

### 11.6 响应式策略

**断点**

| 名称 | 宽度 | 布局变化 |
|---|---|---|
| `sm` | ≥ 640px | 统计卡 2 列 |
| `md` | ≥ 768px | **导航切换点**：底部 Tab → 左侧 Sidebar |
| `lg` | ≥ 1024px | 侧栏展开，内容区加宽 |
| `xl` | ≥ 1280px | 统计卡 4 列，图表并排 |

**导航差异**

| 维度 | 移动端（< 768px） | 桌面端（≥ 768px） |
|---|---|---|
| 位置 | 底部固定 Tab Bar | 左侧固定 Sidebar |
| 图标 | 20px + 12px 标签 | 18px + 14px 标签 |
| 项数 | 5 个主入口 + "更多" | 全部可见 |
| 安全区 | `pb-[env(safe-area-inset-bottom)]` | 无 |
| 折叠 | 不支持 | 支持，状态持久化 |

**内容自适应**

```html
<!-- 统计卡：1 → 2 → 4 列 -->
<div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">

<!-- 表格窄屏降级为卡片列表 -->
<table class="hidden md:table"> ... </table>
<div class="md:hidden space-y-3">
  <!-- 卡片形式渲染同一份数据 -->
</div>

<!-- 双栏在窄屏堆叠 -->
<div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
  <div class="lg:col-span-2">图表</div>
  <div>侧栏</div>
</div>
```

**移动端细节**

| 项 | 要求 |
|---|---|
| 触摸目标 | ≥ 44 × 44px |
| 输入框字号 | ≥ 16px（防 iOS 自动缩放） |
| 弹窗 | 移动端从底部滑入（Drawer），桌面端居中（Dialog） |
| 表格 | 横向滚动 + 首列固定 |
| 手势 | 支持下拉刷新（统计页）、左滑删除（规则项） |
| 视口 | `viewport-fit=cover` + `env(safe-area-inset-*)` |

---

## 十二、UI 框架选型

### 12.1 结论

| 层 | 选型 |
|---|---|
| **前端 UI 框架** | **shadcn/ui（Radix UI + Tailwind CSS）** |
| **后端 UI** | **不需要任何 UI 框架** |

> 后端是纯 REST API，只输出 JSON。唯一的"可视化"是一个零依赖的 `/docs` 接口说明页（Go 内嵌静态 HTML）。

### 12.2 候选方案对比

| 方案 | 颜值 | 定制自由度 | 移动端 | 包体积 | 结论 |
|---|---|---|---|---|---|
| **shadcn/ui + Tailwind** | ★★★★ | ★★★★★ | ★★★★★ | 按需（≈0 运行时） | ✅ **首选** |
| HeroUI（原 NextUI） | ★★★★★ | ★★★★ | ★★★★ | ~65KB | 备选，默认最好看 |
| Mantine | ★★★★ | ★★★ | ★★★★ | ~50KB | 可考虑 |
| Chakra UI | ★★★★ | ★★★ | ★★★★ | ~55KB | 可考虑 |
| Ant Design | ★★★ | ★★ | ★★ | 较大 | ❌ 审美不符 |
| MUI | ★★★ | ★★★ | ★★★ | 最大 | ❌ 风格固化 |

### 12.3 为什么排除 Ant Design

| 维度 | 分析 |
|---|---|
| **审美冲突** | AntD 是「B 端后台」审美：紧凑表格、蓝色主色、高信息密度。与目标风格（大圆角 + 大量留白 + 柔和配色）气质相反 |
| **定制成本** | v5 用 CSS-in-JS，底层 DOM 结构复杂，深度魔改样式难度极高 |
| **移动端割裂** | 主要面向 PC 宽屏，移动端需另装 AntD Mobile，两套代码不统一 |
| **识别度问题** | 不深度定制时，一眼就能看出是 AntD 项目，缺乏品牌辨识度 |

### 12.4 为什么选择 shadcn/ui

| 优势 | 说明 |
|---|---|
| **源码在手** | 不是 npm 黑盒依赖，组件源码复制到项目 `components/ui/`，可任意修改任何一行 |
| **零锁死** | 不存在"库升级导致破坏性变更"问题，永远不用担心被卡脖子 |
| **零运行时开销** | 不像 CSS-in-JS 有运行时样式计算，性能最优 |
| **Tailwind 原生** | 样式即类名，改样式就是改类名，直观且无学习成本 |
| **无障碍完备** | 底层 Radix 处理键盘导航、焦点管理、ARIA 属性——这些自研极易出错 |
| **移动端友好** | 布局全靠 Tailwind 断点，**同一套代码响应式切换**，无需另装移动端库 |
| **生态活跃** | 事实标准，文档与社区示例丰富，遇到问题易搜索 |
| **主题灵活** | CSS 变量驱动，暗色模式切变量即可，无需重新构建 |

**唯一的代价**：组件更新需手动 diff（因为是复制而非依赖）。但对于本项目，这换来的完全掌控权非常值得。

### 12.5 前端完整技术栈

| 层 | 选型 | 版本 | 用途 |
|---|---|---|---|
| 框架 | React | 18 | UI 库 |
| 语言 | TypeScript | 5.x | 类型安全 |
| 构建 | Vite | 5 | 开发服务器 + 打包 |
| 样式 | Tailwind CSS | 3.4 | 原子化 CSS |
| 组件 | shadcn/ui | latest | 基础组件 |
| 无障碍原语 | Radix UI | — | shadcn 底层 |
| 图标 | Lucide React | — | 图标库 |
| 路由 | React Router | 6 | SPA 路由 |
| 服务端状态 | TanStack Query | 5 | 缓存/重试/失效 |
| HTTP | Axios | 1.x | 请求 + 拦截器 |
| 表单 | React Hook Form | 7 | 表单状态 |
| 校验 | Zod | 3 | Schema 校验 |
| 图表 | Recharts | 2 | 折线/环形/柱状 |
| 动画 | Framer Motion | 11 | 过渡动效 |
| 二维码 | qrcode.react | — | 生成配置二维码 |
| 剪贴板 | 原生 API | — | 复制地址 |

### 12.6 Axios 拦截器设计（Token 自动刷新）

```typescript
// 请求拦截：注入 access token
api.interceptors.request.use((config) => {
  const token = tokenStore.getAccess();
  if (token) config.headers.Authorization = `Bearer ${token}`;
  return config;
});

// 响应拦截：401 时自动刷新并重放
let refreshing: Promise<string> | null = null;

api.interceptors.response.use(
  (res) => res,
  async (error) => {
    const original = error.config;
    if (error.response?.status === 401 && !original._retry) {
      original._retry = true;
      // 并发请求共享同一个刷新 Promise，避免重复刷新
      refreshing ??= doRefresh().finally(() => { refreshing = null; });
      try {
        const newToken = await refreshing;
        original.headers.Authorization = `Bearer ${newToken}`;
        return api(original);
      } catch {
        tokenStore.clear();
        router.navigate('/login');
        return Promise.reject(error);
      }
    }
    return Promise.reject(error);
  }
);
```

> **关键点**：用共享 Promise 防止并发 401 触发多次刷新（会导致 refresh token 轮转冲突）。

---

## 十三、发布与部署架构

> **核心变化**：不再使用 Docker Compose。改为 **GitHub Releases 托管多平台二进制 + 服务器本地直接运行**。数据库 SQLite 单文件，前端内嵌进 `apid`，前后端通过 REST API + Token 对接。

### 13.1 发布产物

每次打 tag（如 `v1.0.0`）触发 GitHub Actions，产出并发布到 GitHub Releases：

| 产物 | 说明 |
|---|---|
| `aegis-dnsd_<ver>_linux_amd64.tar.gz` | DNS 引擎（含 DoT/DoH/UDP/TCP） |
| `aegis-apid_<ver>_linux_amd64.tar.gz` | API + 内嵌前端（go:embed） |
| 同上 `_linux_arm64` / `_darwin_amd64` / `_darwin_arm64` / `_windows_amd64` | 多平台交叉编译 |
| `checksums.txt` | 所有产物的 SHA-256 |

**目标平台矩阵**

| OS | Arch | 说明 |
|---|---|---|
| Linux | amd64 / arm64 | 主力：VPS、树莓派、NAS |
| macOS | amd64 / arm64 | 本地开发/测试（53 端口需 sudo） |
| Windows | amd64 | 作为服务运行（NSSM/WinSW 包装） |

### 13.2 GitHub Actions 工作流

```yaml
# .github/workflows/release.yml
name: Release
on:
  push:
    tags: ["v*"]

jobs:
  build-web:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: pnpm/action-setup@v4
        with: { version: 9 }
      - uses: actions/setup-node@v4
        with: { node-version: 22, cache: pnpm }
      - run: cd web && pnpm install --frozen-lockfile && pnpm build
      - uses: actions/upload-artifact@v4
        with: { name: web-dist, path: web/dist }

  release:
    needs: build-web
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - { goos: linux,   goarch: amd64 }
          - { goos: linux,   goarch: arm64 }
          - { goos: darwin,  goarch: amd64 }
          - { goos: darwin,  goarch: arm64 }
          - { goos: windows, goarch: amd64 }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22" }
      - uses: actions/download-artifact@v4
        with: { name: web-dist, path: server/internal/api/webdist }
      - run: |
          VER=${GITHUB_REF_NAME#v}
          cd server
          for bin in dnsd apid; do
            CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
            go build -trimpath -ldflags "-s -w -X main.version=$VER" \
              -o ../out/aegis-$bin-${{ matrix.goos }}-${{ matrix.goarch }}/aegis-$bin ./cmd/$bin
          done
      - run: |
          cd out
          for d in */; do tar czf "${d%/}.tar.gz" "${d%/}"; done
          sha256sum *.tar.gz > checksums.txt
      - uses: softprops/action-gh-release@v2
        with:
          files: |
            out/*.tar.gz
            out/checksums.txt
          generate_release_notes: true
```

要点：

- **`CGO_ENABLED=0` + `modernc.org/sqlite`（纯 Go）**：无 CGO 依赖，交叉编译零障碍
- **版本号注入**：`-X main.version=$VER`，运行时 `--version` 可查，自更新比对用
- **前端内嵌**：`web-dist` 下载到 `internal/api/webdist`，由 `embed.go`（`//go:embed webdist`）打进 `apid`
- Windows 产物实际打包为 `.zip`（工作流中按后缀判断）

### 13.3 服务器部署步骤（3 分钟）

```bash
# 1. 下载（以 linux amd64 为例）
VER=v1.0.0
mkdir -p /opt/aegis && cd /opt/aegis
wget https://github.com/<you>/aegis-dns/releases/download/$VER/aegis-dnsd-1.0.0-linux-amd64.tar.gz
wget https://github.com/<you>/aegis-dns/releases/download/$VER/aegis-apid-1.0.0-linux-amd64.tar.gz
tar xzf aegis-dnsd-*.tar.gz && tar xzf aegis-apid-*.tar.gz

# 2. 校验
sha256sum -c <(grep linux-amd64 checksums.txt)   # 可选但推荐

# 3. 写配置
cp config.example.yaml config.yaml && vim config.yaml

# 4. 首次运行 apid（自动迁移建库 + 创建管理员 + 释放内嵌前端）
sudo ./aegis-apid --config config.yaml

# 5. 注册 systemd 服务并自启
sudo cp deploy/aegis-apid.service deploy/aegis-dnsd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now aegis-dnsd aegis-apid

# 6. 验证
dig @127.0.0.1 example.com                       # UDP 53
kdig +tls @127.0.0.1 +tls-host=k7m2.dns.example.com example.com   # DoT
curl -s https://dns.example.com/api/v1/healthz   # API
curl -sI https://dns.example.com/                # 内嵌前端
```

### 13.4 systemd 单元示例

```ini
# /etc/systemd/system/aegis-dnsd.service
[Unit]
Description=AegisDNS Engine (dnsd)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/aegis/aegis-dnsd --config /opt/aegis/config.yaml
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/opt/aegis/data
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`aegis-apid.service` 同理（去掉 `CAP_NET_BIND_SERVICE`，监听 8080 非特权端口）。

### 13.5 配置文件（本地运行形态）

```yaml
# config.yaml —— 同一份数据目录，两个进程共用
domain: dns.example.com            # 管理员配置的主域名
data_dir: ./data                   # SQLite + 快照 + 证书 全在这一个目录
dns:
  listen_udp: ":53"
  listen_tcp: ":53"
  listen_dot: ":853"
  listen_doh: ":443"               # dnsd 直接做 TLS 终止
  cert_file: ./data/certs/fullchain.pem    # 通配证书
  key_file:  ./data/certs/privkey.pem
  acme:                            # 可选：内置自动签发续期（免 Caddy）
    enabled: false
    provider: cloudflare           # lego 支持 100+ DNS provider
    token_env: CF_API_TOKEN
api:
  listen: ":8080"
  jwt_secret: ${JWT_SECRET}
  admin:
    username: admin
    password: ${ADMIN_PASSWORD}    # 首次启动创建，之后入库
upstreams: ["1.1.1.1", "8.8.8.8", "https://dns.google/dns-query"]
update:
  repo: <you>/aegis-dns            # GitHub Releases 自更新来源
  channel: stable                  # stable | beta
```

### 13.6 TLS/证书方案（本机运行形态）

| 场景 | 方案 |
|---|---|
| 有域名 + DNS provider 支持 API | `dnsd` 内置 ACME（lego 库，DNS-01）自动申请并续期 `*.dns.example.com`，证书落 `data/certs/`，DoT/DoH 共用 |
| 已有证书（如从面板导出） | 配置 `cert_file/key_file` 路径，`dnsd` 监听文件变化热加载 |
| 仅内网/测试 | `--dev-tls` 自签证书，客户端加 `-k` |

### 13.7 前后端对接（API + Token）

```
前端 (内嵌 SPA 或独立托管)                apid (Go)
┌──────────────────────┐    HTTPS    ┌──────────────────────┐
│ 登录 → 拿到 Token      │───────────►│ Authorization 校验    │
│ 所有请求带 Bearer Token│◄───────────│ 返回 JSON            │
└──────────────────────┘             └──────────────────────┘
```

- **对接契约**：全部 REST + JSON，见第九章
- **Token**：登录返回 `access_token`（JWT 15min）+ `refresh_token`（30d）；另支持用户在设置页生成**长期 API Token**（PAT，用于脚本/第三方调用，可随时撤销）
- **前端可以不在本机**：SPA 只是 Token 的消费者，即使把 `dist` 拷到 CDN/对象存储改个 API 地址也能用；前端彻底挂掉不影响 DNS 与 API

### 13.8 端口与防火墙清单

| 端口 | 协议 | 服务 | 进程 | 对外 |
|---|---|---|---|---|
| 53 | UDP/TCP | 普通 DNS | dnsd | 是 |
| 853 | TCP | DoT | dnsd | 是 |
| 443 | TCP | DoH（dnsd TLS 终止） | dnsd | 是 |
| 8080 | TCP | API + 内嵌前端 | apid | 建议反代到 443 或加防火墙 |
| 80 | TCP | ACME HTTP-01（可选）/ 跳转 | — | 是 |

> Ubuntu 仍需处理 `systemd-resolved` 占用 53 的问题（见 R2）。

### 13.9 部署自检脚本

`deploy/selfcheck.sh`（纯 bash，无 Docker 依赖）在服务器上运行，输出彩色报告：

```
┌─────────────────────────────────────────────────────┐
│  AegisDNS 部署自检                                   │
├─────────────────────────────────────────────────────┤
│ [1/8] 检查二进制与版本 (--version)           ✅ 1.0.0 │
│ [2/8] 检查端口占用 (53/853/443)              ⚠️ 53被占 │
│ [3/8] 检查 systemd 服务状态                   ✅ 2/2  │
│ [4/8] 检查证书有效性                          ✅ 87天  │
│ [5/8] 检查 DoT 连通性 (kdig)                 ✅ 通过  │
│ [6/8] 检查 DoH 连通性 (curl)                 ✅ 通过  │
│ [7/8] 检查 API 健康 (/healthz)               ✅ 通过  │
│ [8/8] 端到端测试（注册 → 拿子域名 → 解析）     ✅ 通过  │
├─────────────────────────────────────────────────────┤
│  结果: 7 通过 / 1 警告 / 0 失败                      │
└─────────────────────────────────────────────────────┘
```

---

## 十四、在线更新机制

> **核心变化**：更新源直接使用 **GitHub Releases**，不再需要自建版本清单服务。

### 14.1 后端更新（GitHub Releases 自更新）

**流程**

```
[定时] 每 6h 调用 GET https://api.github.com/repos/<you>/aegis-dns/releases/latest
    │                      （或 /releases?per_page=1，beta 渠道用预发布 tag）
    ▼
比对 semver（当前版本 --version 注入值 vs 最新 tag）
    │
    ├─ 无更新 → 静默
    └─ 有更新 → 记录到 app_versions 表，UI 显示"有可用更新"
    │
    ▼
[管理员] 点击"立即更新" → POST /api/v1/admin/update/apply
    │
    ▼
按 runtime.GOOS/GOARCH 选择 Release 资产（如 aegis-dnsd-1.1.0-linux-amd64.tar.gz）
    │
    ▼
下载到临时文件 → SHA-256 与 Release 的 checksums.txt 比对
    ├─ 不匹配 → 中止，记录告警
    │
    ▼
minio/selfupdate 原子替换当前二进制
    │
    ▼
优雅关闭（等待在途查询完成，最长 10s）
    │
    ▼
systemd Restart=always 自动拉起新版本
```

**要点**

| 项 | 实现 |
|---|---|
| 版本源 | GitHub API `releases/latest`（免费、自带 CDN、无需自建服务） |
| 平台匹配 | `GOOS_GOARCH` 后缀自动选择资产，平台不符则跳过并提示 |
| 完整性 | 强制校验 `checksums.txt` 中对应条目的 SHA-256 |
| 升级范围 | `dnsd` 与 `apid` 各自独立自更新，UI 分别提示 |
| 限速 | GitHub API 匿名 60 次/小时，6h 一次轮询足够；可配 `GITHUB_TOKEN` 提升至 5000 |
| 回滚 | 保留上一版本二进制为 `*.bak`，UI 提供"回滚到上一版"按钮 |
| 签名（二期） | Release 附 minisign 签名，更新前验签 |

### 14.2 前端更新

前端已内嵌进 `apid` 二进制，因此**前端更新 = apid 自更新**，无独立流程。SPA 内部的资源级更新仍保留：

| 环节 | 实现 |
|---|---|
| 资源命名 | `[name]-[hash].js` 内容哈希 |
| `index.html` | `Cache-Control: no-cache` |
| 静态资源 | `Cache-Control: max-age=31536000, immutable` |
| 检测 | SPA 启动 + 每 10 分钟 `fetch('/api/v1/version')`，与启动时版本比对 |
| 提示 | 版本变化 → Toast「服务已更新，点击刷新」 |
| 应用 | 用户点击 → `location.reload()` |

> 若选择把 `dist` 外置到 CDN（13.7 的可选形态），则前端更新退化为"上传新 dist"，刷新即得新版。

### 14.3 版本兼容性

| 场景 | 处理 |
|---|---|
| `apid` 版本 > `dnsd` 版本 | 快照格式向后兼容（新增字段有默认值），`dnsd` 忽略未知字段 |
| `dnsd` 版本 > `apid` 版本 | `dnsd` 拒绝加载 `version` 高于自身支持的快照，保留旧配置并告警 |
| 快照格式大版本变更 | 快照中带 `schema` 字段，`dnsd` 校验大版本号 |
| Release tag 不符 semver | 跳过该 Release，取下一个符合的 |

## 十五、技术风险与对策

| # | 风险 | 影响 | 概率 | 对策 |
|---|---|---|---|---|
| R1 | 通配证书申请失败 | DoT/DoH 不可用 | 中 | 文档提供手动证书方案；`dnsd` 无证书时降级只启 UDP/TCP |
| R2 | **Ubuntu systemd-resolved 占用 53 端口** | 服务无法启动 | **高** | 部署文档明确步骤；启动时检测端口占用并给出明确错误提示 |
| R3 | 订阅源拉取超时/超大 | 规则不更新 | 中 | 保留旧数据；20MB 上限；30s 超时；失败不影响现有规则 |
| R4 | SQLite 写锁冲突 | 统计丢失或 API 报错 | 中 | WAL 模式 + `busy_timeout=5s` + 批量写；监控锁等待 |
| R5 | **大规模订阅导致 Trie 构建慢** | 重载延迟 | 中 | 增量重建；预分配 map；超 100 万条改二进制索引 |
| R6 | inotify 事件丢失（NFS/某些挂载） | 配置不生效 | 中 | **双通道**：fsnotify + 30s 轮询兜底 |
| R7 | 多租户缓存 key 遗漏 user_id | **跨租户数据泄漏** | 低 | 代码审查重点；单元测试覆盖；key 结构强制含 user_id |
| R8 | 规则误杀导致用户无法上网 | 用户体验受损 | 中 | 默认低误杀订阅；一键暂停拦截；白名单优先 |
| R9 | 上游 DNS 全部不可用 | 解析失败 | 低 | 多上游并发 + fallback 链 + 缓存兜底 |
| R10 | 二进制自更新被中间人篡改 | **安全风险** | 低 | HTTPS 下载 + SHA-256 强制校验；二期加 minisign 验签 |
| R11 | 内存占用随订阅增长 | OOM | 中 | 限制单用户订阅条数；监控内存；可配上限 |
| R12 | Refresh Token 泄露 | 账号被盗 | 低 | 轮转机制；撤销表；异常登录检测 |

### 15.1 性能预估

| 场景 | 预估 |
|---|---|
| 缓存命中查询 | < 0.5ms |
| 缓存未命中 + 规则匹配 | 1-3ms（不含上游） |
| 规则匹配（10 万条 Trie） | < 1ms |
| 快照加载（100 用户 / 50 万条） | 300-800ms |
| 增量重载（单用户 1 万条） | 20-50ms |
| 内存占用（100 用户 / 50 万条规则） | 约 150-250MB |

### 15.2 容量规划

| 规模 | 用户数 | 规则总数 | 内存 | CPU | 磁盘（90天） |
|---|---|---|---|---|---|
| 小型 | < 50 | < 10 万 | 256MB | 0.5 核 | 2GB |
| 中型 | < 500 | < 100 万 | 1GB | 1 核 | 20GB |
| 大型 | < 5000 | < 1000 万 | 4GB+ | 2-4 核 | 200GB+ |

> 大型规模建议：改用 PostgreSQL（统计）+ 二进制 Trie 索引（规则）+ 多实例 + 负载均衡。

---

## 十六、扩展路线

### 16.1 二期功能

| 功能 | 说明 |
|---|---|
| **DoQ（DNS-over-QUIC）** | 基于 `quic-go`，走 SNI 识别，与 DoT 共用证书 |
| **DNSCrypt** | 兼容 dnscrypt-proxy 客户端 |
| **DNSSEC 验证** | 上游响应签名校验，防止投毒 |
| **HTTP/3 支持 DoH** | 降低移动网络延迟 |
| **自定义拦截页** | 命中拦截时返回可配置的 HTTP 页面（需 DoH 配合） |
| **客户端设备管理** | 用户可见设备列表，按设备应用不同规则 |

### 16.2 架构演进

| 阶段 | 触发条件 | 演进方向 |
|---|---|---|
| 单机 SQLite | 默认 | — |
| 主从复制 | 读压力大 | SQLite → PostgreSQL + 只读副本 |
| 多实例 | 单机容量不足 | `dnsd` 横向扩展；快照改由对象存储分发 |
| 分布式缓存 | 缓存命中率下降 | 引入 Redis 做共享缓存层 |
| 二进制规则索引 | 规则 > 1000 万条 | Trie 预编译为 mmap 文件，零加载时间 |

### 16.3 运维增强

| 功能 | 说明 |
|---|---|
| Prometheus 指标 | `/metrics` 暴露 QPS、延迟、命中率、规则数 |
| 结构化日志 | JSON 格式，便于 ELK/Loki 采集 |
| 分布式追踪 | OpenTelemetry 接入 |
| 告警 | 上游失败率、证书到期、磁盘水位 |
| 备份 | SQLite 在线备份 + 快照文件版本化 |
| 灰度发布 | 按用户分桶应用新规则 |

---

## 附录 A：技术栈速查

| 层 | 选型 | 版本 |
|---|---|---|
| 后端语言 | Go | 1.21+ |
| DNS 库 | `github.com/miekg/dns` | latest |
| 上游转发 | `github.com/AdguardTeam/dnsproxy/upstream` | latest |
| HTTP 路由 | `github.com/go-chi/chi/v5` | v5 |
| 数据库 | `modernc.org/sqlite`（纯 Go，无 CGO） | latest |
| JWT | `github.com/golang-jwt/jwt/v5` | v5 |
| 密码 | `golang.org/x/crypto/bcrypt` | latest |
| 文件监听 | `github.com/fsnotify/fsnotify` | latest |
| 自更新 | `github.com/minio/selfupdate` | latest |
| ACME 证书 | `github.com/go-acme/lego/v4`（DNS-01，内置到 dnsd） | v4 |
| 配置 | `gopkg.in/yaml.v3` | v3 |
| 限流 | `golang.org/x/time/rate` | latest |
| 前端框架 | React | 18 |
| 构建 | Vite | 5 |
| 样式 | Tailwind CSS | 3.4 |
| 组件 | shadcn/ui (Radix UI) | latest |
| 状态 | TanStack Query | 5 |
| 图表 | Recharts | 2 |
| 动画 | Framer Motion | 11 |

## 附录 B：目录结构规划

```
aegis-dns/
├── README.md
├── docs/
│   ├── design.md                # 本文档
│   ├── rules-syntax.md          # 规则语法手册
│   ├── api.md                   # API 说明
│   └── deploy.md                # 部署指南（下载 Release → systemd）
├── .github/workflows/
│   ├── release.yml              # 打 tag → 多平台编译 → GitHub Releases
│   └── ci.yml                   # PR → go test / pnpm build
├── server/                      # Go 后端
│   ├── go.mod
│   ├── cmd/
│   │   ├── dnsd/main.go
│   │   └── apid/main.go
│   ├── internal/
│   │   ├── config/              # 配置加载
│   │   ├── model/               # 领域对象
│   │   ├── store/               # SQLite 数据访问
│   │   ├── auth/                # 认证（JWT + PAT）
│   │   ├── rules/               # 规则引擎
│   │   ├── subsub/              # 订阅系统
│   │   ├── dns/                 # DNS 引擎（含 DoH TLS 终止）
│   │   ├── resolver/            # 上游解析
│   │   ├── snapshot/            # 配置快照
│   │   ├── stats/               # 统计聚合
│   │   ├── api/
│   │   │   ├── ...              # REST API
│   │   │   └── webdist/         # 构建时填充 web/dist，go:embed 内嵌
│   │   └── selfupdate/          # GitHub Releases 自更新
│   ├── migrations/
│   └── config.example.yaml
├── web/                         # React 前端（构建产物内嵌进 apid）
│   ├── package.json
│   ├── vite.config.ts
│   ├── tailwind.config.js
│   └── src/
│       ├── api/                 # axios + Bearer Token
│       ├── components/
│       │   ├── ui/              # shadcn 组件
│       │   └── layout/
│       ├── pages/
│       ├── hooks/
│       └── styles/
└── deploy/
    ├── aegis-dnsd.service       # systemd 单元
    ├── aegis-apid.service
    ├── config.example.yaml
    └── selfcheck.sh             # 无 Docker 依赖的自检脚本
```

---

*文档结束 —— DNSForge v1.0 设计文档*
