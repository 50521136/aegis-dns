# 部署指南

本文档覆盖从零到可用的完整流程，以及出问题时的排查顺序。

---

## 0. 前置条件

| 项 | 要求 |
|---|---|
| 系统 | Linux（amd64 / arm64）。systemd 用于进程守护，非必需但强烈建议 |
| 权限 | root（需要绑定 53/853/443 特权端口、写 systemd 单元） |
| 域名 | 一个可管理的域名，且能加通配解析 |
| 证书 | 覆盖 `*.<你的域名>` 的证书（见第 2 节） |
| 端口 | 53/udp、53/tcp、853/tcp、443/tcp、8443/tcp 对公网开放 |

**磁盘**：小型部署（<50 用户）90 天数据约 2GB，建议至少 10GB 可用。
**内存**：`dnsd` 常驻约 100–150MB（加载 24 万条订阅规则后实测），
`apid` 约 50–100MB。systemd 单元默认各限 1G。

---

## 1. DNS 解析

把域名（或泛解析 `*`）指向服务器公网 IP：

```
A     example.com       → <服务器公网IP>
A     *.example.com     → <服务器公网IP>
```

> **通配符只覆盖一级**。所以主域名填 `example.com`（子域名是
> `<client_id>.example.com`），而不是 `dns.example.com`（那需要 `*.dns.example.com`）。

验证：

```bash
dig +short example.com
dig +short test.example.com     # 泛解析是否生效
```

---

## 2. 证书

### 方式一：acme.sh（推荐，支持 DNS-01）

```bash
curl https://get.acme.sh | sh -s email=you@example.com

export CF_Token="<Cloudflare API Token>"
~/.acme.sh/acme.sh --issue --dns dns_cf \
  -d example.com -d '*.example.com' \
  --keylength ec-256

~/.acme.sh/acme.sh --install-cert -d example.com --ecc \
  --key-file       /opt/aegis/data/certs/privkey.pem \
  --fullchain-file /opt/aegis/data/certs/fullchain.pem \
  --reloadcmd      "systemctl restart aegis-dnsd aegis-apid"
```

用 DNS-01 而不是 HTTP-01 的原因：不需要开放 80 端口，也不需要证书签发时
有 Web 服务在跑。

### 方式二：certbot

```bash
certbot certonly --manual --preferred-challenges dns \
  -d example.com -d '*.example.com'
```

### 方式三：从面板导出

如果你已经在用宝塔 / 1Panel 之类的面板，直接把它签发的证书拷过来：

```bash
mkdir -p /opt/aegis/data/certs
cp /path/to/cert.pem /opt/aegis/data/certs/fullchain.pem
cp /path/to/cert.key /opt/aegis/data/certs/privkey.pem
chmod 600 /opt/aegis/data/certs/privkey.pem
```

### 验证证书

```bash
openssl x509 -in /opt/aegis/data/certs/fullchain.pem -noout \
  -subject -dates -ext subjectAltName
```

必须看到 `DNS:*.example.com`。**证书热重载**：`dnsd` 用 fsnotify 监听证书目录
（外加 10 分钟兜底轮询），续期后自动生效，不需要重启。

---

## 3. 安装

### 一键安装（推荐）

```bash
curl -fsSL -o install.sh \
  https://raw.githubusercontent.com/50521136/aegis-dns/main/deploy/install.sh

sudo bash install.sh \
  --domain example.com \
  --cert /opt/aegis/data/certs/fullchain.pem \
  --key  /opt/aegis/data/certs/privkey.pem
```

脚本做的事：

1. 识别架构，从 GitHub Releases 下载两个二进制（**带 5 次重试** —— 国内网络下
   GitHub 下载经常间歇性失败）
2. 装到 `/opt/aegis/aegis-dnsd` 与 `/opt/aegis/aegis-apid`
3. 生成 `JWT_SECRET` 与随机管理员密码，写入 `/opt/aegis/.env`（权限 600）
4. 生成 `config.yaml`（带 `--cert/--key` 时自动校验 SAN 是否覆盖 `*.domain`）
5. **处理 53 端口占用**：写 `/etc/systemd/resolved.conf.d/99-aegis.conf`
   关掉 `systemd-resolved` 的 stub listener
6. 注册并 enable `aegis-dnsd` / `aegis-apid`
7. 放行 ufw 的 53/853/443/8443
8. 启动并打印面板地址与管理员密码

### 手动安装

```bash
VER=v1.0.0
mkdir -p /opt/aegis/data/{runtime,certs} && cd /opt/aegis

# 下载（按实际架构替换 amd64）
base="https://github.com/50521136/aegis-dns/releases/download/$VER"
for c in dnsd apid; do
  curl -fsSL -o /tmp/$c.tar.gz "$base/aegis-$c-${VER#v}-linux-amd64.tar.gz"
  tar xzf /tmp/$c.tar.gz -C /tmp
  install -m755 /tmp/aegis-$c-${VER#v}-linux-amd64/aegis-$c /opt/aegis/aegis-$c
done

# 校验（强烈建议）
curl -fsSL -o /tmp/checksums.txt "$base/checksums.txt"
sha256sum -c --ignore-missing /tmp/checksums.txt

# 配置
cp server/config.example.yaml /opt/aegis/config.yaml   # 然后编辑
./aegis-apid -gen-secret > /tmp/secret                  # 写进 .env 的 JWT_SECRET

# systemd
cp deploy/aegis-{dnsd,apid}.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now aegis-dnsd aegis-apid
```

---

## 4. 配置要点

编辑 `/opt/aegis/config.yaml`，必改项用 ★ 标注：

```yaml
domain: "example.com"          # ★ 主域名，必须与证书的 *.domain 对应
data_dir: "./data"

dns:
  cert_file: "/opt/aegis/data/certs/fullchain.pem"
  key_file:  "/opt/aegis/data/certs/privkey.pem"

api:
  listen: ":8080"              # HTTP，建议只对本机/内网
  tls_listen: ":8443"          # ★ 面板 HTTPS，不要用 :443（被 dnsd 的 DoH 占用）
  jwt_secret: "${JWT_SECRET}"  # ★ 值放 .env，不要写明文
  admin:
    username: "admin"
    password: "${ADMIN_PASSWORD}"

upstreams:                     # ★ 国内服务器别用默认的 1.1.1.1/8.8.8.8
  - "223.5.5.5"
  - "119.29.29.29"
  - "1.1.1.1"

update:
  repo: "50521136/aegis-dns"
```

`/opt/aegis/.env`（权限 600）：

```
JWT_SECRET=<openssl rand -hex 32 的结果>
ADMIN_PASSWORD=<首次启动用的初始密码>
```

### 三个最容易踩的坑

**1. 密钥写进 config.yaml**
不要这么做。用 `${VAR}` 占位符 + `.env`。未设置的环境变量会**保留原文**
（而不是替换成空串），启动时会明确报错，避免静默用一个空密钥导致 JWT 可被伪造。

**2. `api.tls_listen: ":443"`**
443 被 `dnsd` 的 DoH 占用。两个二进制的 `-check` 都会检查这个冲突。

**3. 默认上游不可达**
出厂默认是 `1.1.1.1 / 8.8.8.8 / dns.google`，国内常常不可达，
表现为「服务起来了但解析全超时」。`apid` 首次启动会把 `config.yaml` 里的
`upstreams` 播种进数据库，之后以管理后台为准（播种只在该键不存在时发生，
所以管理员改过设置后重启不会被覆盖）。

---

## 5. 防火墙与安全组

**两层都要放行**：

```bash
# 主机防火墙
ufw allow 53/udp && ufw allow 53/tcp
ufw allow 853/tcp && ufw allow 443/tcp && ufw allow 8443/tcp
```

云厂商控制台的安全组也要放行同样的端口。国内云默认只放 22，
**主机防火墙全开 + 本地监听正常 + 外网超时 ⇒ 一定是安全组**。

验证外网可达（从**另一台机器**，不要在服务器上自己连自己）：

```bash
dig @<服务器公网IP> example.com
```

> 本机 NAT 回环会造成假阳性/假阴性，外部探测更可信。
> 用外部探针时记得**带一个已知关闭的端口作对照**：新增端口报 Open
> **且**对照端口报 Closed，才算证据。

---

## 6. 首次使用

1. 浏览器打开 `https://example.com:8443`
   （自签证书会报警告，用正式证书不会）
2. 用 `admin` + `.env` 里的 `ADMIN_PASSWORD` 登录
3. **立刻改密码**（设置页）
4. 注册一个普通账号 → 首页会直接展示你的专属 DNS 地址
5. 在「订阅」页一键添加推荐订阅（AdGuard / OISD / 1Hosts）
6. 按平台指引配置客户端（面板会给 iOS / Android / Windows / macOS / 路由器
   的具体步骤）

### 明文 DNS（53）怎么用

明文 DNS 没有 SNI 或 Host 可依据，只能靠**来源 IP 归属**识别租户。
在「我的 DNS 配置」页登记你的出口 IP 或 CIDR（如 `203.0.113.7` 或 `203.0.113.0/24`），
之后从该 IP 发来的查询就会套用你的规则。

不登记的话请求会落到 `fallback_policy`：

| 策略 | 行为 |
|---|---|
| `passthrough`（默认） | 用全局上游，不做任何规则过滤 |
| `refused` | 直接返回 REFUSED（私有部署，只允许注册用户使用） |
| `blocklist_only` | 只应用全部租户订阅黑名单的并集 |

---

## 7. 验证

### 自检脚本

```bash
bash /opt/aegis/selfcheck.sh
```

覆盖 9 项：二进制版本、配置、密钥、6 个端口监听、systemd 状态（含重启次数）、
证书有效期与 SAN、UDP 解析、API 健康与就绪、内嵌前端、配置快照。

### 手工验证四种协议

仓库自带一个零依赖的多协议探针（只用 Python 标准库，不需要装 dig/kdig）：

```bash
# UDP
python3 dnsprobe.py udp 127.0.0.1:53 example.com

# TCP
python3 dnsprobe.py tcp 127.0.0.1:53 example.com

# DoT（SNI 决定租户）
python3 dnsprobe.py dot <client_id>.example.com:853 example.com --sni <client_id>.example.com

# DoH（Host 首段识别）
python3 dnsprobe.py doh https://example.com --host <client_id>.example.com ads.example.com

# DoH（URL 路径段识别，兼容不能改 Host 的客户端）
python3 dnsprobe.py doh https://example.com --path /dns-query/<client_id> ads.example.com
```

### 端到端验证

```bash
bash deploy/e2e-local.sh
```

在非特权端口上完整跑一遍：注册 → 建规则 → 快照热加载 → 四种协议解析 →
多租户隔离 → 权限检查 → 统计落库 → 优雅关闭。

### 「前端挂了不影响 DNS」这条要真的验

```bash
systemctl stop aegis-apid
dig @127.0.0.1 example.com     # 必须仍然正常
curl -s https://example.com:8443/   # 这时才应该失败
systemctl start aegis-apid
```

---

## 8. 运维

### 日志

```bash
journalctl -u aegis-dnsd -f
journalctl -u aegis-apid -f
journalctl -u aegis-dnsd -n 100 --no-pager
```

把 `log.format` 改成 `json` 可以对接 Loki / ELK。

### 数据目录

```
/opt/aegis/data/
├── aegis.db               SQLite（WAL）
├── aegis.db-wal           WAL（正在写入时存在）
├── aegis.db-shm           共享内存
├── runtime/config.json    配置快照（apid 写、dnsd 读）
└── certs/
    ├── fullchain.pem
    └── privkey.pem        权限 600
```

**备份**：直接拷 `aegis.db`（WAL 模式下用 `sqlite3 aegis.db ".backup"` 更稳妥）
与 `config.yaml` + `.env`。证书可以重新签发，不必备份。

### 数据保留

| 表 | 保留期 | 清理方式 |
|---|---|---|
| `query_rollup` | 90 天 | apid 启动 5 分钟后跑一次，之后每 24h |
| `top_domains` | 90 天 | 同上 |
| `query_log` | 可配（默认 7 天） | 同上，另叠 100 万条总量上限 |
| `refresh_tokens` | 过期即删 | 同上 |

### 在线更新

```bash
# 检查
curl -s -H "Authorization: Bearer <admin-token>" \
  https://example.com:8443/api/v1/update/check

# 应用（component: apid | dnsd | all）
curl -s -X POST -H "Authorization: Bearer <admin-token>" \
  -H 'Content-Type: application/json' -d '{"component":"all"}' \
  https://example.com:8443/api/v1/admin/update/apply
```

更新会**强制校验 SHA-256**，不匹配立即中止。旧二进制备份为 `*.bak` 可手动回滚：

```bash
systemctl stop aegis-dnsd
mv /opt/aegis/aegis-dnsd.bak /opt/aegis/aegis-dnsd
systemctl start aegis-dnsd
```

---

## 9. 排障

按这个顺序查，不要跳步：

### 服务起不来

```bash
/opt/aegis/aegis-dnsd -config /opt/aegis/config.yaml -check
journalctl -u aegis-dnsd -n 50 --no-pager
```

| 症状 | 原因 | 处理 |
|---|---|---|
| `address already in use` on 53 | `systemd-resolved` 占用 | 见下方 |
| `配置项 domain 为空` | `config.yaml` 没填 `domain` | 填上主域名 |
| `api.jwt_secret 仍是未展开的占位符` | `.env` 没导出或没被 `EnvironmentFile` 读到 | 检查 `/opt/aegis/.env` |
| `api.jwt_secret 长度仅 N` | 密钥太短 | `./aegis-apid -gen-secret` 重新生成 |
| `api 监听地址 :443 与 dnsd 的 DoH 端口冲突` | `api.tls_listen` 配成了 443 | 改成 `:8443` |
| `没有任何 DNS 协议成功启动` | 所有端口都被占 | 逐个 `ss -lntup \| grep <端口>` 查 |

**53 端口被 systemd-resolved 占用**（最常见）：

```bash
# 最小改动：只关掉 stub listener，不要停用整个服务
# （直接 disable 会让 apt/docker 等依赖它的组件失去解析能力）
mkdir -p /etc/systemd/resolved.conf.d
printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/99-aegis.conf
systemctl restart systemd-resolved

# 如果 /etc/resolv.conf 变成坏掉的软链，重建它
ls -l /etc/resolv.conf
printf 'nameserver 223.5.5.5\nnameserver 119.29.29.29\n' > /etc/resolv.conf
```

### 本机能解析，外网不行

按层排查，顺序固定：

```bash
# 1. 服务是否真的在监听
ss -lntup | grep -E ':53|:853|:443'

# 2. 主机防火墙
ufw status
iptables -L INPUT -n

# 3. 云厂商安全组（控制台）
```

前两层都正常但外网超时 ⇒ **一定是安全组**。

### 解析超时或变慢

```bash
# 直接测上游是否可达
for u in 223.5.5.5 119.29.29.29 1.1.1.1; do
  echo -n "$u: "; dig +time=3 +tries=1 @$u example.com +short | head -1 || echo 失败
done
```

上游不可达就在管理后台改「全局设置 → 上游 DNS」。

`dnsd` 的取上游策略是 **Happy Eyeballs**：先只问主上游，700ms 没答就把其余
上游并发拉起来竞速。所以主上游故障时额外延迟约 700ms，而不是等满 3 秒超时。

### 规则不生效

1. 确认规则是**启用**状态
2. 看 `dnsd` 日志有没有 `配置快照已加载 version=N`：
   ```bash
   journalctl -u aegis-dnsd -n 20 --no-pager | grep 快照
   ```
3. 对比数据库里的版本与快照里的版本：
   ```bash
   curl -s -H "Authorization: Bearer <token>" https://example.com:8443/api/v1/admin/system \
     | grep -o '"config_version":[0-9]*\|"snapshot_version":[0-9]*'
   ```
   两者不一致 → 手动重建：`POST /api/v1/admin/snapshot/rebuild`
4. 检查识别路径：明文 DNS 需要登记来源 IP，DoT 需要 SNI 是 `<client_id>.<域名>`，
   DoH 需要 Host 首段或路径段是 `<client_id>`

### 订阅不更新

```bash
curl -s -H "Authorization: Bearer <token>" https://example.com:8443/api/v1/subscriptions \
  | python3 -m json.tool | grep -E 'last_status|last_count|last_fetched'
```

`last_status` 会写明原因。订阅刷新失败时**旧规则会保留**（这是设计契约），
所以「规则没更新」通常不是「规则丢了」，看 `last_count` 是否还是老值即可。

### 内存增长

`dnsd` 的内存主要由订阅规则量决定。24 万条约 100–150MB。
若接近 `MemoryMax`（默认 1G）：

- 减少订阅数量，或改用更轻量的列表（`1Hosts Lite`、`Hazezi Light`）
- 单用户订阅超 100 万条时，考虑文档扩展路线里的「二进制 Trie 索引」

---

## 10. 卸载

```bash
systemctl disable --now aegis-dnsd aegis-apid
rm -f /etc/systemd/system/aegis-{dnsd,apid}.service
systemctl daemon-reload

# 数据留着，确认不需要了再删
rm -rf /opt/aegis

# 恢复 systemd-resolved 的 stub listener
rm -f /etc/systemd/resolved.conf.d/99-aegis.conf
systemctl restart systemd-resolved
```
