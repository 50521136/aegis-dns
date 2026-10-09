# API 契约

> 运行时可以直接访问 `https://<你的域名>:8443/api/v1/docs` 查看这份说明的
> HTML 版（内嵌在 `apid` 里，不依赖任何外部 CDN，内网也能打开）。

---

## 通用约定

| 项 | 约定 |
|---|---|
| Base URL | `/api/v1`（与前端同源，无需 CORS） |
| 认证 | `Authorization: Bearer <access_token \| agx_...>` |
| 请求体 | `application/json` |
| 响应体 | `application/json` |
| 时间格式 | RFC 3339（`2026-10-10T00:55:00Z`） |
| 分页 | `?page=1&page_size=20`，响应含 `total` / `page` / `page_size` |
| 请求追踪 | 响应头带 `X-Request-ID`，也可由客户端通过同名请求头指定 |

### 统一错误响应

```json
{
  "error": {
    "code": "INVALID_RULE_SYNTAX",
    "message": "规则语法错误：缺少域名",
    "details": { "input": "||^" }
  }
}
```

### 错误码表

| HTTP | code | 说明 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求参数错误 |
| 400 | `INVALID_RULE_SYNTAX` | 规则语法错误 |
| 401 | `UNAUTHORIZED` | 未认证或令牌无效 |
| 401 | `TOKEN_EXPIRED` | 令牌过期（前端据此触发静默刷新） |
| 403 | `FORBIDDEN` | 权限不足（非 admin 访问 `/admin/*`，或账号被禁用） |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `CONFLICT` | 资源冲突（用户名已存在、订阅 URL 重复） |
| 422 | `VALIDATION_FAILED` | 语义校验失败（密码太弱、设置越界等） |
| 429 | `RATE_LIMITED` | 请求过于频繁 |
| 500 | `INTERNAL_ERROR` | 服务器内部错误 |

---

## 一、系统

### GET /healthz

存活探针。只要进程能响应就返回 200，**不检查数据库** ——
「apid 活着但数据库暂时锁住」不该触发编排系统重启整个服务。

```json
{ "status": "ok", "time": "2026-10-10T00:55:00Z" }
```

### GET /readyz

就绪探针。真的去碰数据库与快照文件。

```json
{ "status": "ready" }
```

未就绪时返回 503：

```json
{ "status": "not_ready", "problems": ["配置快照不存在: ..."] }
```

### GET /version

```json
{
  "component": "apid",
  "version": "1.0.0",
  "commit": "a1b2c3d",
  "build_time": "2026-10-10T00:00:00Z",
  "go_version": "go1.22.10",
  "platform": "linux/amd64",
  "frontend": true
}
```

`frontend` 表示二进制里是否真的内嵌了前端构建产物（而不是占位页）。

### GET /docs

零依赖的 API 说明页（内嵌 HTML）。

---

## 二、认证

### POST /auth/register

```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "S3cure!Passw0rd",
  "invite_code": ""
}
```

`invite_code` 仅在管理员开启邀请码时必填。

**201 响应**：

```json
{
  "user": {
    "id": "usr_01a121cc-b836-7000-81b4-38665275f0bc",
    "username": "alice",
    "email": "alice@example.com",
    "role": "user",
    "client_id": "k7m2pqx4ab",
    "enabled": true,
    "created_at": "2026-10-10T00:55:00Z"
  },
  "dns_config": {
    "client_id": "k7m2pqx4ab",
    "domain": "example.com",
    "doh_url": "https://k7m2pqx4ab.example.com/dns-query",
    "doh_url_path": "https://example.com/dns-query/k7m2pqx4ab",
    "dot_host": "k7m2pqx4ab.example.com",
    "dot_port": 853,
    "dot_addr": "k7m2pqx4ab.example.com:853",
    "plain_dns": null,
    "cert_fingerprint_sha256": "AB:CD:EF:...",
    "platform_guides": [
      { "platform": "ios", "steps": ["...", "..."] }
    ]
  },
  "tokens": {
    "access_token": "eyJhbGciOi...",
    "refresh_token": "eyJhbGciOi...",
    "expires_in": 900,
    "token_type": "Bearer"
  }
}
```

> **设计要点**：注册响应**直接返回完整的 DNS 接入配置**，前端可以立刻展示
> 「你的专属地址」，不需要二次请求。这是首次体验的关键。

**校验规则**：用户名 3–32 字符（字母/数字/`_`/`-`/`.`）；
密码至少 8 字符且同时含字母与数字；常见弱密码与重复/连续字符会被拒绝。

### POST /auth/login

```json
{ "username": "alice", "password": "S3cure!Passw0rd" }
```

响应结构同 register。

> 用户名不存在与密码错误返回**完全相同的响应**，且对不存在的用户也会跑一次
> bcrypt，避免通过响应内容或耗时差异枚举账号。

### POST /auth/refresh

```json
{ "refresh_token": "eyJhbGciOi..." }
```

**200**：

```json
{ "access_token": "...", "refresh_token": "...", "expires_in": 900, "token_type": "Bearer" }
```

**轮转语义**：旧的 `refresh_token` 在本次刷新后立即失效。
前端必须用**共享 Promise** 处理并发 401，否则多个请求同时刷新会互相作废令牌。

### POST /auth/logout

```json
{ "refresh_token": "eyJhbGciOi..." }
```

撤销该 refresh token。**204 No Content**。请求体可为空。

---

## 三、账号

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/me` | 当前用户 |
| PUT | `/me/password` | 修改密码 |
| GET | `/me/dns-config` | DNS 接入配置（含证书指纹与分平台指引） |
| GET | `/me/export` | 导出规则与订阅定义 |
| POST | `/me/import` | 导入配置 |
| GET | `/me/tokens` | 长期 API Token 列表 |
| POST | `/me/tokens` | 创建 API Token |
| DELETE | `/me/tokens/{id}` | 撤销 API Token |
| GET | `/me/ips` | 来源 IP 归属列表 |
| POST | `/me/ips` | 登记来源 IP / CIDR |
| DELETE | `/me/ips?cidr=` | 删除来源 IP |

### PUT /me/password

```json
{ "old_password": "S3cure!Passw0rd", "new_password": "N3w!Passw0rd" }
```

**204**。改密码会**撤销该用户的全部 refresh token** —— 否则旧令牌还能换出新
access token，改密码就形同虚设。

### POST /me/tokens

```json
{ "name": "ci-script", "expires_in_days": 0 }
```

`expires_in_days` 为 0 表示永不过期。

**201**：

```json
{
  "id": "pat_01a121cc-...",
  "name": "ci-script",
  "prefix": "agx_a1b2c3",
  "token": "agx_a1b2c3d4e5...",
  "expires_at": "",
  "warning": "请立即保存，该令牌只会显示这一次"
}
```

**原文只返回一次**。服务端只保存 SHA-256，之后无法再展示。

### POST /me/ips

```json
{ "cidr": "203.0.113.7" }
```

裸 IP 会自动补成 `/32` 或 `/128`，并规范化成网络地址。

**201**：`{ "cidr": "203.0.113.7/32" }`

> 明文 DNS（53）没有 SNI / Host 可依据，只能靠来源 IP 归属识别租户。
> 不登记的话请求会落到 `fallback_policy`。

---

## 四、规则

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | 列表 |
| POST | `/rules` | 新建 |
| POST | `/rules/import` | 批量导入文本 |
| POST | `/rules/validate` | 只校验语法 |
| PUT | `/rules/{id}` | 更新 |
| DELETE | `/rules/{id}` | 删除 |
| DELETE | `/rules` | 批量删除 |

### GET /rules

查询参数：`page`、`page_size`（上限 200）、`q`（模糊匹配 pattern 与 value）、
`kind`、`enabled`。

```json
{
  "items": [
    {
      "id": "rul_01a121cc-...",
      "kind": "block",
      "pattern": "||ads.example.com^",
      "value": "",
      "qtypes": [],
      "enabled": true,
      "created_at": "2026-10-10T00:55:00Z"
    }
  ],
  "total": 1,
  "page": 1,
  "page_size": 20
}
```

### POST /rules

```json
{
  "kind": "block",
  "pattern": "||ads.example.com^",
  "value": "",
  "qtypes": ["A", "AAAA"],
  "enabled": true
}
```

`kind` 取值：`block` / `allow` / `rewrite_a` / `rewrite_aaaa` / `rewrite_cname`。
`qtypes` 为空表示适用所有记录类型。

**201** 返回规则对象。语法非法返回 **400 `INVALID_RULE_SYNTAX`**。

### PUT /rules/{id}

部分更新：未提供的字段保持原值。`value` 例外（总是采用请求值，因为改写目标
需要支持被清空）。

### POST /rules/import

```json
{
  "content": "||ads.example.com^\n@@||safe.example.com^\n0.0.0.0 tracker.net\n",
  "format": "auto",
  "enabled": true
}
```

**200**：

```json
{ "created": 3, "skipped": 0, "errors": [] }
```

- 自动嗅探格式（`adblock` / `hosts` / `dnsmasq` / `domain`）
- 单次上限 **50000 条**
- `errors` 最多 20 条
- pattern 会被**规范化**：`0.0.0.0 ads.example.com` 存成 `||ads.example.com^`

### POST /rules/validate

```json
{ "kind": "block", "pattern": "||^", "value": "", "qtypes": [] }
```

**200**（注意：校验失败也是 200，因为它不是请求错误）：

```json
{ "valid": false, "error": "规则缺少域名" }
```

---

## 五、订阅

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/subscriptions` | 列表 |
| POST | `/subscriptions` | 新建（会立即拉取一次） |
| GET | `/subscriptions/presets` | 推荐订阅源 |
| POST | `/subscriptions/refresh-all` | 刷新我的全部订阅 |
| PUT | `/subscriptions/{id}` | 更新 |
| DELETE | `/subscriptions/{id}` | 删除 |
| POST | `/subscriptions/{id}/refresh` | 立即刷新 |

### POST /subscriptions

```json
{
  "name": "OISD Basic",
  "url": "https://big.oisd.nl/domainswild",
  "list_type": "blocklist",
  "format": "auto",
  "enabled": true
}
```

`list_type`：`blocklist` / `allowlist`。
`format`：`auto` / `adblock` / `hosts` / `dnsmasq` / `domain`。

**201** 返回订阅对象（含 `last_count`、`last_status`、`last_fetched`）。
新建后**立即拉取一次**，让用户马上看到条目数，而不是等 24 小时。
拉取失败不算创建失败 —— 订阅已经建好，失败原因写在 `last_status` 里。

### POST /subscriptions/{id}/refresh

**200**：

```json
{
  "id": "sub_01a121cc-...",
  "last_count": 240335,
  "last_fetched": "2026-10-10T00:56:00Z",
  "last_status": "ok",
  "duration_ms": 1842,
  "not_modified": false
}
```

**刷新失败也返回 200**，并在 `last_status` 与 `error` 字段里说明原因 ——
这是「操作已执行但结果失败」，前端要展示的是订阅状态而不是一个红色 toast。

> **核心契约**：订阅更新永远不会让「当前生效的规则变空」。
> 网络失败 / 4xx / 5xx / 格式错误 / 内容为空 → 一律保留旧数据。

### GET /subscriptions/presets

```json
{
  "presets": [
    {
      "name": "AdGuard DNS Filter",
      "url": "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
      "list_type": "blocklist",
      "description": "AdGuard 官方综合广告与追踪拦截列表，误杀率低，推荐首选",
      "recommended": true
    }
  ]
}
```

---

## 六、统计

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/stats/summary?range=24h` | 总览 |
| GET | `/stats/timeseries?range=7d&interval=1h` | 时间序列 |
| GET | `/stats/top?range=24h&limit=10&type=all\|blocked` | TOP 域名 |
| GET | `/stats/querylog?limit=100&offset=0&domain=&action=` | 查询日志 |

`range` 支持 `1h` / `24h` / `7d` / `30d` / `90d`，默认 `24h`。
`interval` 支持 `5m` / `1h` / `6h` / `1d`，未指定时按 range 自动选。

### GET /stats/summary

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
  "unique_domains": 4213
}
```

> 统计是**每 60 秒批量落库**的（`dns.dns.stats_flush_seconds` 可调）。
> 所以刚发生的查询最多要等一个落库周期才会出现在这里 —— 这是设计，
> 不是 bug：查询路径上不能碰数据库。

### GET /stats/querylog

```json
{
  "items": [
    {
      "id": 12345,
      "ts": 1791569298,
      "time": "2026-10-10T00:55:00Z",
      "domain": "ads.example.com",
      "qtype": "A",
      "rcode": "NOERROR",
      "action": "blocked",
      "client": "203.0.113.0/24",
      "latency_ms": 1
    }
  ],
  "total": 1,
  "limit": 100,
  "offset": 0
}
```

`action` 取值：`allowed` / `blocked` / `cached` / `rewritten` / `upstream`。
`client` 是**脱敏后的网段**（IPv4 到 /24，IPv6 到 /48）—— 排障够用，
不需要精确到单个设备。

---

## 七、管理（需 admin 角色）

非 admin 访问 `/admin/*` 返回 **403** 并写审计日志。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/users?page&page_size&q` | 用户列表（含规则数与 IP 数） |
| PUT | `/admin/users/{id}` | 启用/禁用/改角色/改邮箱 |
| DELETE | `/admin/users/{id}` | 删除用户（级联删除其规则、订阅、IP、令牌） |
| GET | `/admin/settings` | 全局设置 |
| PUT | `/admin/settings` | 修改全局设置 |
| POST | `/admin/snapshot/rebuild` | 强制重建配置快照 |
| GET | `/admin/system` | 系统状态 |
| GET | `/admin/audit?limit=100` | 审计日志 |
| POST | `/admin/update/apply` | 应用在线更新 |

### PUT /admin/users/{id}

```json
{ "enabled": false, "role": "user", "email": "new@example.com" }
```

**安全护栏**：

- 不能修改自己的角色，不能禁用自己
- 不能删除自己
- 系统必须**至少保留一个启用的管理员**（否则会把自己锁在门外）

### GET /admin/settings

```json
{
  "domain": "example.com",
  "upstreams": ["223.5.5.5", "119.29.29.29"],
  "blocking_mode": "null_ip",
  "custom_ip": "",
  "fallback_policy": "passthrough",
  "cache_size": 10000,
  "cache_min_ttl": 60,
  "cache_max_ttl": 86400,
  "rate_limit_qps": 0,
  "max_inflight": 300,
  "query_timeout_ms": 10000,
  "query_log_size": 10000,
  "enable_query_log": true,
  "sub_interval_hours": 24,
  "query_log_retention_days": 7,
  "allow_register": true,
  "invite_code": "",
  "update_repo": "50521136/aegis-dns",
  "update_channel": "stable",
  "config_domain": "example.com",
  "dns_listen": { "udp": ":53", "tcp": ":53", "dot": ":853", "doh": ":443" }
}
```

`config_domain` 是 `config.yaml` 里的值，用于发现「设置与配置不一致」。

### PUT /admin/settings

**部分更新**：只提交要改的字段。未提交的字段保持原值 ——
避免前端漏传一个字段就把设置重置成零值（例如漏传 `cache_size` 会让缓存直接失效）。

校验规则：

| 字段 | 约束 |
|---|---|
| `blocking_mode` | `nxdomain` / `null_ip` / `refused` / `custom_ip` |
| `custom_ip` | `blocking_mode=custom_ip` 时必填 |
| `fallback_policy` | `passthrough` / `refused` / `blocklist_only` |
| `cache_max_ttl` | 不能小于 `cache_min_ttl` |
| `max_inflight` | 10 – 100000 |
| `query_timeout_ms` | 500 – 60000 |
| `sub_interval_hours` | 1 – 720 |
| `upstreams` | 至少一个 |

### GET /admin/system

```json
{
  "component": "apid",
  "version": "1.0.0",
  "uptime_seconds": 3600,
  "users": 12,
  "config_version": 42,
  "snapshot_version": 42,
  "snapshot_at": "2026-10-10T00:55:00Z",
  "snapshot_error": "",
  "snapshot_users": 12,
  "snapshot_rules": 240342,
  "sub_entries": 240335,
  "memory_alloc_mb": 24.7,
  "memory_sys_mb": 102.9,
  "goroutines": 17,
  "cert_days_left": 89,
  "cert_fingerprint": "AB:CD:...",
  "domain": "example.com",
  "listen": { "api": ":8080", "api_tls": ":8443", "dns_udp": ":53", "dns_tcp": ":53", "dns_dot": ":853", "dns_doh": ":443" }
}
```

`config_version` 与 `snapshot_version` 不一致 → 快照没跟上，
可 `POST /admin/snapshot/rebuild` 手动重建。

### POST /admin/update/apply

```json
{ "component": "all" }
```

`component`：`apid` / `dnsd` / `all`。

```json
{
  "applied": ["dnsd", "apid"],
  "message": "dnsd 已更新到 1.1.0 并重启；apid 已更新到 1.1.0，进程即将重启",
  "restarting": true
}
```

更新**强制校验 SHA-256**，不匹配立即中止。旧二进制备份为 `*.bak`。

### GET /update/check

```json
{
  "current": "1.0.0",
  "latest": "1.1.0",
  "update_available": true,
  "notes": "新增 DoQ 支持；修复缓存越界问题",
  "url": "https://github.com/.../aegis-apid-1.1.0-linux-amd64.tar.gz",
  "sha256": "abc123...",
  "published_at": "2026-10-01T00:00:00Z",
  "platform": "linux-amd64"
}
```

检查失败也返回 **200**，`update_available: false` + `reason` 说明原因
（网络受限的部署环境很常见，不该当成错误）。

---

## 八、中间件链

```
Request
  │
  ▼
[1] Recover          捕获 panic，返回 500（否则一个空指针会让 apid 退出，
  │                  而 apid 同时托管前端 → 页面直接 404）
  ▼
[2] RequestID        生成或透传 X-Request-ID
  │
  ▼
[3] Logger           结构化访问日志（静态资源不记，避免被 chunk 请求冲掉）
  │
  ▼
[4] CORS             默认同源；只有显式配置 api.cors_origins 才放开
  │
  ▼
[5] RateLimit        单 IP 令牌桶，默认 600/分钟、突发 120
  │                  healthz / readyz 不受限流
  ▼
[6] Auth             校验 JWT 或 PAT，把用户放进 context
  │                  白名单：/auth/*、/healthz、/readyz、/version、/docs
  ▼
[7] RequireRole      管理接口校验 admin 角色，越权写审计日志
  │
  ▼
Handler
```

---

## 九、用 curl 调用的例子

```bash
API=https://example.com:8443/api/v1

# 注册并拿到 client_id
curl -s -X POST $API/auth/register -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"Str0ngPass123"}' | python3 -m json.tool

# 登录拿令牌
TOKEN=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"Str0ngPass123"}' \
  | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)

# 建规则
curl -s -X POST $API/rules -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"block","pattern":"||ads.example.com^"}'

# 加订阅
curl -s -X POST $API/subscriptions -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"OISD Basic","url":"https://big.oisd.nl/domainswild","list_type":"blocklist"}'

# 看统计
curl -s "$API/stats/summary?range=24h" -H "Authorization: Bearer $TOKEN"

# 生成一个长期 API Token 给脚本用（原文只返回一次）
curl -s -X POST $API/me/tokens -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"ci"}'
```
