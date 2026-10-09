# DNSForge · aegis-dns

> 多租户加密 DNS 服务 —— 支持 **DoT / DoH / 普通 UDP·TCP**，用户注册后自动分配专属子域名，
> 规则与订阅按租户完全隔离。前后端分离架构，DNS 引擎与 Web API 双进程解耦。

[![Release](https://img.shields.io/github/v/release/50521136/aegis-dns?label=release)](https://github.com/50521136/aegis-dns/releases)
[![CI](https://github.com/50521136/aegis-dns/actions/workflows/ci.yml/badge.svg)](https://github.com/50521136/aegis-dns/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

---

## 它解决什么问题

自建 DNS 拦截服务的常见形态是「一个 AdGuard Home 实例 + 一张通配证书」。
问题是：**所有人共享同一套规则，没有租户概念**。你想给家人、朋友、自己各一套规则，
就得起多个实例，每个实例一套端口、一套数据库、一套证书。

DNSForge 把「租户」做成第一等公民：

- 每个注册用户拿到一个 `client_id`（10 位 base32 小写，如 `k7m2pqx4ab`），
  专属接入地址是 `<client_id>.<你的域名>`
- **DoT 靠 SNI 首段识别**、**DoH 靠 Host 首段或 URL 路径段识别**、
  **UDP/TCP 靠来源 IP/CIDR 归属识别**，兜底还有 EDNS Option 65001
- 每个租户有独立的规则集、独立的订阅、独立的缓存（缓存 key 含 `user_id`，
  从设计上杜绝跨租户数据泄漏）、独立的统计

### 故障隔离是硬约束

DNS 是基础设施，它不能因为「管理面板挂了」而停摆。所以：

| 约束 | 实现方式 |
|---|---|
| DNS 与 API 之间**零 RPC** | 只通过 `data/runtime/config.json` 单向通信 |
| `dnsd` **运行期不读数据库** | 启动时把配置全载入内存，之后只批量写统计 |
| 53 / 853 / 443 **不依赖反代** | `dnsd` 自己监听、自己做 TLS 终止 |
| 规则匹配失败**必须 fail-open** | 任何解析异常都放行到上游，绝不 fail-closed |
| API 挂了不影响解析 | 实测：`systemctl stop aegis-apid` 后 `dig` 照常返回 |

---

## 快速开始

### 服务器一键安装

```bash
# 下载安装脚本（或从仓库 deploy/ 目录取）
curl -fsSL -o install.sh \
  https://raw.githubusercontent.com/50521136/aegis-dns/main/deploy/install.sh

sudo bash install.sh \
  --domain example.com \
  --cert /path/to/cert.pem \
  --key  /path/to/cert.key
```

脚本会：下载 Release 二进制 → 装到 `/opt/aegis` → 生成密钥与配置 → 注册 systemd
→ 关掉 `systemd-resolved` 的 53 端口占用 → 放行防火墙 → 启动并打印面板地址。

> 证书必须覆盖 `*.<domain>`。注意通配符只覆盖一级：如果证书是 `*.example.com`，
> `--domain` 就该填 `example.com`，而不是 `dns.example.com`。

### 安装后你会得到

```
管理面板    https://<domain>:8443          （443 被 DoH 占用，面板走 8443）
API 文档    https://<domain>:8443/api/v1/docs
DoH         https://<client_id>.<domain>/dns-query
DoT         <client_id>.<domain>:853
明文 DNS    <公网IP>:53                     （需先把来源 IP 登记到账号下）
```

自检：

```bash
bash /opt/aegis/selfcheck.sh
```

它会逐项检查二进制版本、配置、密钥、6 个端口、systemd 状态、证书有效期与 SAN、
UDP 解析、API 健康、内嵌前端、配置快照，并给出**可操作的**结论而不是一句 "failed"。

### 从源码构建

```bash
# 后端（两个二进制）
cd server
CGO_ENABLED=0 go build -o aegis-dnsd ./cmd/dnsd
CGO_ENABLED=0 go build -o aegis-apid ./cmd/apid

# 前端（构建产物会被 apid 用 go:embed 内嵌）
cd ../web && pnpm install && pnpm build
cp -r dist/* ../server/internal/api/webdist/dist/
cd ../server && go build -o aegis-apid ./cmd/apid   # 重新编译以带上前端
```

### 本地联调（不碰特权端口）

```bash
bash deploy/e2e-local.sh
```

一条命令跑完：编译 → 启动 → 注册 → 建规则 → 快照热加载 →
**UDP / TCP / DoT / DoH 四种协议各自的解析验证** → 多租户隔离 → 权限检查 →
统计落库 → 优雅关闭。加 `--keep` 保留 `/tmp/aegis-e2e` 便于排查。

---

## 架构

```
                    ┌─────────────────────────────────────────┐
  UDP/TCP :53 ─────►│                                         │
  DoT     :853 ─────►│   dnsd  （单二进制，无 CGO）             │
  DoH     :443 ─────►│   Registry   租户识别（SNI/Host/IP/EDNS）│
                    │   RuleEngine 后缀 Trie，first-match-wins │
                    │   Resolver   上游池，主 + 竞速 fallback   │
                    │   Cache      16 分片 LRU，按 user_id 隔离 │
                    │   StatsBuffer 内存聚合，60s 批量落库       │
                    └──────────────┬──────────────────────────┘
                                   │ 只读 data/runtime/config.json
                                   │ 只写 query_rollup / top_domains / query_log
                    ┌──────────────┴──────────────────────────┐
  浏览器 ──────────►│   apid  （REST API + go:embed 内嵌前端）  │
  :8080 / :8443     │   chi Router + JWT/PAT 认证               │
                    └──────────────┬──────────────────────────┘
                                   ▼
                            SQLite (WAL)  ./data/aegis.db
```

**数据流（配置变更 → 生效 < 2s）**

```
UI 改规则 → POST /api/v1/rules → apid 写 SQLite → config_version += 1
  → apid 重建完整快照，原子写入 runtime/config.json（写临时文件 → fsync → rename）
  → dnsd 的 fsnotify 收到事件（30s 轮询兜底）
  → 构建新 Registry → atomic.Pointer.Store()
  → 后续查询立即用新规则，在途查询用旧配置跑完，零停机
```

### 目录结构

```
aegis-dns/
├── server/                      Go 后端（一个 module，两个 main）
│   ├── cmd/dnsd/                DNS 引擎入口
│   ├── cmd/apid/                API + 内嵌前端入口
│   ├── migrations/              内嵌 SQL 迁移
│   └── internal/
│       ├── api/                 REST API（chi）+ webdist（go:embed）
│       ├── auth/                bcrypt / JWT / PAT
│       ├── certmgr/             证书加载、热重载、指纹
│       ├── config/              config.yaml 加载（支持 ${ENV} 展开）
│       ├── coord/               配置变更 → 版本递增 → 快照重建
│       ├── dns/                 查询管线、Registry、Cache、DoT/DoH 服务端
│       ├── id/                  UUID v7、client_id、PAT 生成
│       ├── model/               跨进程共享结构（快照格式契约）
│       ├── resolver/            上游池（dnsproxy/upstream）
│       ├── rules/               规则解析 + 后缀 Trie
│       ├── selfupdate/          GitHub Releases 自更新
│       ├── snapshot/            快照构建与原子写
│       ├── stats/               内存聚合 + 环形查询日志
│       ├── store/               SQLite 数据访问（单写者 + 读池）
│       └── subsub/              订阅拉取、格式嗅探、调度
├── web/                         React 18 + TS + Vite + Tailwind 前端
├── deploy/                      systemd 单元、安装脚本、自检脚本、联调脚本、DNS 探针
└── docs/                        设计文档、规则语法、API、部署指南
```

---

## 端口约定

| 端口 | 协议 | 服务 | 进程 | 对外 |
|---|---|---|---|---|
| 53 | UDP/TCP | 普通 DNS | dnsd | 是 |
| 853 | TCP | DoT | dnsd | 是 |
| 443 | TCP | **DoH** | dnsd | 是 |
| 8080 | TCP | API（HTTP） | apid | 建议只对本机/内网 |
| 8443 | TCP | **面板 + API（HTTPS）** | apid | 是 |

> **为什么面板不在 443**：443 被 `dnsd` 的 DoH 占用。DoH 必须占标准端口
> （很多客户端硬编码 443），而面板换端口只是 URL 上多几个字符。
> 两个监听各自直接绑端口、都不经反向代理，所以架构红线（443 不依赖反代）依然成立。

---

## 规则语法

完整说明见 [`docs/rules-syntax.md`](docs/rules-syntax.md)。

```
||ads.example.com^                     拦截该域及其所有子域
@@||safe.example.com^                  白名单例外（优先级最高，覆盖一切）
ads.example.com                        裸域名，等价于 ||ads.example.com^
0.0.0.0 ads.example.com                hosts 文件格式
127.0.0.1 ads.example.com              hosts 文件格式
||ads.example.com^$dnstype=AAAA        仅对指定记录类型生效
||old.example.com^$dnsrewrite=1.2.3.4  A 记录改写
||old.example.com^$dnsrewrite=2001:db8::1   AAAA 改写
||old.example.com^$dnsrewrite=new.example.com  CNAME 改写
*.dev.local^$dnsrewrite=127.0.0.1      通配改写
address=/ads.example.com/0.0.0.0       dnsmasq 格式
# 或 ! 开头                            注释
```

> `*.example.com` 等价于 `||example.com^`（**含根域自身**），
> 与 OISD / 1Hosts 等主流列表的语义一致。详见
> [`docs/rules-syntax.md`](docs/rules-syntax.md#三通配符)。

**匹配优先级**（first-match-wins，不做多规则聚合）：

```
用户 allow  >  用户 rewrite  >  用户 block  >  订阅 allow  >  订阅 block  >  转发上游
```

白名单绝对优先 —— 用户显式放行的域名不会被任何订阅规则拦住。

**拦截应答模式**（`blocking_mode`）：

| 模式 | A 记录应答 | 特点 |
|---|---|---|
| `null_ip`（默认） | `0.0.0.0` | 连接立即失败、无重试，客户端最友好 |
| `nxdomain` | SOA | 语义正确，但某些客户端会重试 |
| `refused` | RCODE=5 | 明确拒绝，部分客户端会降级到其它 DNS |
| `custom_ip` | 自定义 | 可指向内网陷阱主机做拦截统计 |

---

## 订阅系统

支持 **Adblock / hosts / dnsmasq / 纯域名列表** 四种格式，自动嗅探。
内置 8 个推荐订阅（AdGuard、OISD、1Hosts、StevenBlack、Hagezi…），一键添加。

**一条不可动摇的契约**：订阅更新永远不会让「当前生效的规则变空」。

- 网络失败 / HTTP 4xx / 5xx / 格式错误 / 内容为空 → **一律保留旧数据**，
  只更新 `last_status` 供 UI 展示
- 单个订阅的刷新串行化，不同订阅并发上限 4
- 默认 24h ± 30min 随机抖动刷新，支持 `If-None-Match` / `If-Modified-Since`（304 直接跳过）
- 单次拉取上限 20MB、超时 30s

---

## 认证

| 令牌 | 类型 | 有效期 | 存储位置（前端） | 撤销 |
|---|---|---|---|---|
| `access_token` | JWT HS256 | 15 分钟 | 内存 | 不支持（短期自然过期） |
| `refresh_token` | JWT HS256 | 30 天 | localStorage | 支持，且**每次刷新轮转** |
| PAT | `agx_` + 随机串 | 可配置 / 永久 | 用户自行保管 | 支持 |

- 前端走 JWT（axios 拦截器自动刷新，用共享 Promise 防止并发重复刷新）
- 脚本 / 第三方面板走 PAT（服务端只存 SHA-256，原文仅创建时返回一次）
- 密码 bcrypt cost=12，改密码后自动撤销该用户的全部 refresh token
- 登录失败与用户名不存在返回**同一个响应**，并对不存在的用户也跑一次 bcrypt，
  避免通过响应差异或时间差枚举账号

---

## 在线更新

```bash
# 检查（面板设置页也会显示）
curl -H "Authorization: Bearer <token>" https://<domain>:8443/api/v1/update/check

# 应用（管理员）
curl -X POST -H "Authorization: Bearer <admin-token>" -H 'Content-Type: application/json' \
  -d '{"component":"all"}' https://<domain>:8443/api/v1/admin/update/apply
```

- 版本源：GitHub Releases（`releases/latest`，beta 渠道读 prerelease）
- 平台匹配：按 `GOOS-GOARCH` 自动选资产
- **强制校验 `checksums.txt` 里的 SHA-256**，不匹配立即中止
- 替换前把旧二进制备份为 `*.bak`，可手动回滚
- `apid` 更新后自己 `os.Exit(0)` 由 systemd 拉起；`dnsd` 由 apid 替换文件后 `systemctl restart aegis-dnsd`
- 每 6 小时自动检查一次，但**绝不静默自动升级** —— 升级必须由管理员确认

---

## 配置要点

完整示例见 [`server/config.example.yaml`](server/config.example.yaml)。三个最容易踩的坑：

1. **密钥不写进 `config.yaml`**。写 `${JWT_SECRET}`，值放同目录 `.env`。
   未设置的环境变量会保留原文，启动时明确报错，而不是静默用一个空密钥。
   用 `./aegis-apid -gen-secret` 生成。
2. **`api.tls_listen` 不能是 `:443`** —— 那个端口被 `dnsd` 的 DoH 占着。
   两个二进制各自的 `-check` 都会检查这个冲突。
3. **`upstreams` 在国内服务器上别用默认值**。出厂默认是 `1.1.1.1 / 8.8.8.8 /
   dns.google`，国内常常不可达，表现为「服务起来了但解析全超时」。
   用 `223.5.5.5` / `119.29.29.29`。`apid` 首次启动会把 `config.yaml` 里的
   `upstreams` 播种进数据库，之后以管理后台为准。

---

## 运维

```bash
journalctl -u aegis-dnsd -f          # DNS 引擎日志
journalctl -u aegis-apid -f          # API 日志

/opt/aegis/aegis-dnsd -config /opt/aegis/config.yaml -check   # 启动前自检
/opt/aegis/aegis-apid -config /opt/aegis/config.yaml -check

bash /opt/aegis/selfcheck.sh          # 完整部署自检
```

**数据目录**（`data_dir`，默认 `/opt/aegis/data`）：

```
aegis.db                  SQLite（WAL），用户/规则/订阅/统计
aegis.db-wal, -shm        WAL 与共享内存文件
runtime/config.json       配置快照（apid 写、dnsd 读）
certs/fullchain.pem       证书
certs/privkey.pem         私钥（600）
```

**数据保留**：`query_rollup` / `top_domains` 保留 90 天，
`query_log` 默认保留 7 天且总量上限 100 万条，`refresh_tokens` 过期即删。
`apid` 启动 5 分钟后跑一次清理，之后每 24 小时一次。

**内存**（参考，实测值）：`dnsd` 加载 24 万条订阅规则后常驻约 100–150MB。
systemd 单元默认 `MemoryMax=1G`，覆盖文档「中型规模（<500 用户 / <100 万规则）」的估算。

---

## 实现与设计文档的差异

设计文档（[`docs/design.md`](docs/design.md)）是目标形态，实现过程中有几处
按实际约束做了调整，都记录在这里：

| 项 | 文档 | 实现 | 原因 |
|---|---|---|---|
| 面板端口 | `apid` 监听 8080，建议反代到 443 | `apid` 监听 8080 + **8443（自带 TLS）** | 443 被 dnsd 的 DoH 占用；且不引入反代才能守住「443 不依赖反代」这条红线 |
| `client_id` 示例 | 表格写 `^[a-z2-7]{10}$`，示例却是 `k7m2p9xq4a`（含 `9`） | 以字符集规则为准，生成 `[a-z2-7]{10}` | 文档自相矛盾，示例应视为笔误（`9` 属于被排除的易混淆字符） |
| 内置 ACME | `dnsd` 内置 lego 做 DNS-01 自动签发 | 配置项保留，`enabled: true` 会给出明确提示；证书以文件方式提供并热重载 | 部署形态用现成证书（certbot/acme.sh 签发或面板导出）已足够，避免为了一个可选功能把 lego 及其上百个 DNS provider 拖进依赖树 |
| PAT 数据表 | 第十章提到 PAT，但第八章 DDL 没有对应表 | 补 `pat_tokens` 表（只存 SHA-256） | 文档缺漏 |
| `query_log` 落库 | 未明确 | `dnsd` 内存环形缓冲 → 60s 批量写 | 查询路径上不能碰数据库（C2） |
| `blocklist_only` 兜底策略 | 「只应用全局黑名单」 | 取全部启用租户订阅黑名单的并集 | 文档未定义「全局黑名单」的来源 |

---

## 开发

```bash
cd server
go vet ./...
go test -race -count=1 ./...     # 规则引擎、缓存隔离、CIDR、订阅解析、ID 生成
```

测试覆盖的关键不变量：

- 规则优先级顺序（用户 allow > rewrite > block > 订阅 allow > block）
- `$dnstype` 限定与「改写不污染其它记录类型」
- `*.domain` 通配只匹配子域、不匹配根域自身
- 非法规则不中断构建
- **缓存按 `user_id` 隔离**（跨租户泄漏的回归测试）
- 缓存 LRU 淘汰时 map 与链表长度一致（防僵尸条目）
- 带 EDNS Client Subnet 的请求不缓存
- CIDR 最长前缀优先
- 订阅解析「内容为空必须报错」而非返回空列表
- `client_id` 字符集与唯一性、UUID v7 单调递增

---

## 已知限制

- **DoQ（DNS-over-QUIC）未实现**。文档列为二期功能。
- **DNSCrypt 未实现**。
- **DNSSEC 验证未实现**：上游应答不做签名校验，仅透传。
- **内置 ACME 未实现**（见上表）。证书需外部签发后以文件提供；
  `dnsd` 会 fsnotify 监听证书目录并热重载，续期后无需重启。
- **大规模部署未优化**：当前是单实例 + 单 SQLite。
  文档 15.2 的「大型规模」（>5000 用户 / >1000 万规则）需要
  PostgreSQL + 二进制 Trie 索引 + 多实例，这些都在扩展路线里，尚未实现。
- 明文 DNS（53）**不对外提供**：未识别来源的请求走 `fallback_policy`，
  默认 `passthrough` 但不做规则过滤；要做成开放解析器请自行评估滥用风险。

## License

MIT
