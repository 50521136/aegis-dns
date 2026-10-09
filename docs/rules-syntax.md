# 规则语法手册

DNSForge 的规则引擎在 DNS 层工作，因此只支持能在域名与记录类型上表达的规则。
浏览器层的元素隐藏、脚本拦截之类的 Adblock 语法会被**跳过而不是报错**
（订阅源里混有这类行是常态，当成错误会让 `last_status` 满屏 error 反而掩盖真问题）。

---

## 一、支持的语法

### 拦截

| 写法 | 说明 |
|---|---|
| `\|\|ads.example.com^` | Adblock 标准写法，拦截 `ads.example.com` 及其**所有子域** |
| `ads.example.com` | 裸域名，等价于 `\|\|ads.example.com^` |
| `0.0.0.0 ads.example.com` | hosts 文件格式 |
| `127.0.0.1 ads.example.com` | hosts 文件格式（等价于拦截） |
| `address=/ads.example.com/0.0.0.0` | dnsmasq 格式 |
| `\|\|ads.example.com^$dnstype=AAAA` | 只对指定记录类型生效 |
| `\|\|ads.example.com^$dnstype=A\|AAAA` | 多类型用 `\|` 分隔 |

### 白名单（例外）

| 写法 | 说明 |
|---|---|
| `@@\|\|safe.example.com^` | 放行该域及其所有子域，**优先级最高** |
| `@@safe.example.com` | 裸域名形式 |

> 白名单会覆盖一切：即使父域被订阅黑名单拦住，`@@||safe.ads.example.com^`
> 依然能让 `safe.ads.example.com` 正常解析。这是 AdGuard 的核心体验，
> 也是本项目严格保持的行为。

### 改写

| 写法 | 说明 |
|---|---|
| `\|\|old.example.com^$dnsrewrite=1.2.3.4` | A 记录改写为 IPv4 |
| `\|\|old.example.com^$dnsrewrite=2001:db8::1` | AAAA 记录改写为 IPv6 |
| `\|\|old.example.com^$dnsrewrite=new.example.com` | CNAME 改写 |
| `*.dev.local^$dnsrewrite=127.0.0.1` | 通配改写（等价于 `\|\|dev.local^`，含根域） |

改写在**规则构建期**就预生成好 `dns.RR`，查询时零分配直接返回。

**部分改写的语义**：如果规则限定了 `$dnstype=A`，那么 AAAA 查询**不会**命中它，
会继续转发上游。这保持了记录类型的语义完整 —— 把 A 记录塞进 AAAA 应答是非法报文。

### 注释

`#` 或 `!` 开头的行被忽略。`[Adblock Plus 2.0]` 这类订阅头也被忽略。

---

## 二、匹配优先级

```
┌──────────────────────────────────────────────────────────┐
│ 1. 用户自定义 allow（白名单）              ← 最高        │
│ 2. 用户自定义 rewrite（改写）                            │
│ 3. 用户自定义 block（拦截）                              │
│ 4. 订阅 allowlist                                        │
│ 5. 订阅 blocklist                                        │
│ 6. 未命中 → 转发上游                        ← 最低        │
└──────────────────────────────────────────────────────────┘
```

采用**首个命中即返回**（first-match-wins），不做多规则聚合。

设计理由：

- **白名单必须绝对优先** —— 用户显式放行的域名不能被任何订阅规则拦住
- **自定义优先于订阅** —— 用户手动配置的意图强于批量订阅
- **改写优先于拦截** —— 改写是更具体的意图

### 同级内的具体性

同一个优先级内，**更长的域名（更具体）胜出**：

```
规则: ||example.com^          （拦）
规则: @@||safe.example.com^   （放行）

查询 safe.example.com   → 放行（命中更具体的白名单节点）
查询 other.example.com  → 拦截
```

实现方式是后缀 Trie：从 TLD 向主机名逐级下行，记录「最深的一个满足
记录类型约束的命中节点」。域名越长越具体，自然胜出。

**注意**：如果深层节点因为 `$dnstype` 约束不适用，会继续向更浅的节点找，
而不是直接放弃。所以下面这两条规则会同时生效：

```
||example.com^                 拦全部类型
||safe.example.com^$dnstype=AAAA  只拦 AAAA
```

查询 `safe.example.com` 的 A 记录 → 落到 `||example.com^` → 拦截。

---

## 三、通配符

只支持 `*.` 前缀形式的通配：

```
*.dev.local^$dnsrewrite=127.0.0.1
*.doubleclick.net
```

**语义：`*.example.com` 等价于 `||example.com^`，即同时匹配 `example.com` 自身
与其所有子域（含多级）。**

这与 OISD、1Hosts 等主流列表的语义一致 —— OISD 的 `domainswild` 列表头部
明确写着：

```
# Entry: "*.example.com" should block access to "example.com" and
#        "subdomain.example.com" but not "thiseexample.com"
```

如果按「`*` 只匹配子域」的严格 glob 语义处理，`*.doubleclick.net` 这类条目
永远不会拦住 `doubleclick.net` 本身，整个列表等于失效，而用户只会看到
「订阅加了但没效果」。

其它位置的 `*`（如 `ad*.example.com`）无法在 DNS 层表达，会被**跳过**。

> 不会误伤后缀相同的域名：`*.dev.local` 不匹配 `notdev.local` 或 `xdev.local`。
> 因为匹配是在标签边界上做的（后缀 Trie 按 `.` 分段），不是字符串后缀比较。

---

## 四、不支持 / 被忽略的语法

| 语法 | 处理 | 原因 |
|---|---|---|
| `$client=...`、`$ctag=...` | 忽略修饰符，保留规则 | 需要请求上下文（客户端标识），纯 DNS 层拿不到 |
| `$app=...` | 同上 | 需要识别发起请求的应用 |
| `$important` | 忽略 | 本实现是 first-match-wins，没有优先级叠加语义 |
| `$denyallow=...` | 忽略 | 需要地址集合匹配 |
| `$badfilter` | 忽略 | 规则去重由数据库唯一性约束承担 |
| 元素隐藏规则（`##.ad`） | 跳过 | 浏览器层概念，DNS 无对应能力 |
| 含 `/` 的路径规则 | 截断路径，只保留域名部分 | DNS 查询里没有路径 |
| 未知修饰符 | 忽略修饰符，保留规则本体 | 订阅源引入新语法时不该让大量规则失效 |

> 设计取向：**宁可少拦，不可误杀**。一条看不懂的规则被忽略，
> 代价是少拦一个域名；被错误地当成通配拦截，代价是用户上不了网。

---

## 五、订阅源的格式嗅探

订阅支持 4 种格式，`format: auto` 时自动识别：

| 格式 | 特征 | 示例 |
|---|---|---|
| `adblock` | 含 `\|\|`、`^` 或 `$` | `\|\|ads.example.com^` |
| `hosts` | 行首是 IP | `0.0.0.0 ads.example.com` |
| `dnsmasq` | 含 `address=/` | `address=/ads.example.com/0.0.0.0` |
| `domain` | 纯域名，每行一个 | `ads.example.com` |

**嗅探算法**：读前 50 行非注释行，统计各格式特征命中数，取最高者。

只扫前 50 行的理由：十万行的文件全扫一遍纯属浪费，而格式在文件内通常统一。

**订阅里的改写规则会被跳过**：改写属于用户自定义能力，订阅只承载「拦/放」两类。

---

## 六、拦截应答模式

命中 block 后返回什么，由全局设置 `blocking_mode` 决定：

| 模式 | A 记录应答 | AAAA 应答 | 其它类型 | 特点 |
|---|---|---|---|---|
| `null_ip`（默认） | `0.0.0.0` | `::` | NODATA | **推荐**：连接立即失败、无重试 |
| `nxdomain` | RCODE=3 + SOA | 同 | 同 | 语义正确，但某些客户端会重试 |
| `refused` | RCODE=5 | 同 | 同 | 明确拒绝，但部分客户端会降级到其它 DNS |
| `custom_ip` | 自定义 IP | `::` | NODATA | 可指向内网陷阱主机做拦截统计 |

**HTTPS / SVCB 记录一律返回 NODATA**，不返回 `0.0.0.0` ——
把 `0.0.0.0` 塞进 HTTPS 记录会干扰浏览器的连接复用逻辑。

`custom_ip` 配了一个非法值时会**降级为 `null_ip`**，而不是返回空应答 ——
空应答会让客户端看到「域名存在但没有记录」而走更长的重试路径。

---

## 七、常见配置示例

### 只拦广告，尽量不误杀

```
blocking_mode: null_ip
订阅: AdGuard DNS Filter + OISD Basic + AdGuard Allowlist
```

### 家里有内网服务，用自定义域名访问

```
||nas.home^$dnsrewrite=192.168.1.10
||router.home^$dnsrewrite=192.168.1.1
*.dev.local^$dnsrewrite=127.0.0.1
```

### 屏蔽某个 App 的遥测（保留正常功能）

先查它的域名，再精确拦截：

```
||telemetry.vendor.com^
||analytics.vendor.com^
```

### 某个域名的 AAAA 有问题，只屏蔽 AAAA

```
||problematic.example.com^$dnstype=AAAA
```

这样 IPv4 访问正常，IPv6 客户端会回落到 IPv4。

---

## 八、批量导入

面板的规则页支持粘贴文本批量导入，`format` 选 `auto` 即可。
`POST /api/v1/rules/import` 的行为：

- 自动嗅探格式
- 单次上限 **50000 条**（更多内容请用「订阅」功能）
- 返回 `{created, skipped, errors}`，`errors` 最多 20 条（避免一个坏文件产生几十万条错误）
- 导入的 pattern 会被**规范化**：`0.0.0.0 ads.example.com` 存成 `||ads.example.com^`，
  这样列表、去重、导出都按同一套写法比较
