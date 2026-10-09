#!/usr/bin/env bash
# ============================================================================
# aegis-dns 一键安装脚本
#
#   从 GitHub Releases 下载两个二进制 → 装到 /opt/aegis → 生成配置
#   → 注册 systemd → 处理 53 端口占用 → 放行防火墙 → 启动并自检
#
# 用法：
#   sudo bash install.sh --domain example.com --cert /path/cert.pem --key /path/cert.key
#   sudo bash install.sh --domain example.com --cert ... --key ... --version v1.0.0
#
# 参数：
#   --domain   主域名（必填）。用户子域名由此派生。
#   --cert     TLS 证书路径（含证书链）。不给则跳过 DoT/DoH。
#   --key      TLS 私钥路径。
#   --repo     GitHub 仓库，默认 50521136/aegis-dns
#   --version  版本 tag，默认 latest
#   --dir      安装目录，默认 /opt/aegis
#   --admin    管理员用户名，默认 admin
#   --no-ufw   不自动改防火墙
# ============================================================================
set -euo pipefail

REPO="50521136/aegis-dns"
VERSION="latest"
INSTALL_DIR="/opt/aegis"
DOMAIN=""
CERT=""
KEY=""
ADMIN_USER="admin"
CONFIGURE_UFW=1

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BLUE=$'\033[34m'; DIM=$'\033[2m'; NC=$'\033[0m'
info()  { echo "${BLUE}[*]${NC} $*"; }
ok()    { echo "${GREEN}[✓]${NC} $*"; }
warn()  { echo "${YELLOW}[!]${NC} $*"; }
err()   { echo "${RED}[✗]${NC} $*" >&2; }
die()   { err "$*"; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)  DOMAIN="$2"; shift 2 ;;
    --cert)    CERT="$2"; shift 2 ;;
    --key)     KEY="$2"; shift 2 ;;
    --repo)    REPO="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --dir)     INSTALL_DIR="$2"; shift 2 ;;
    --admin)   ADMIN_USER="$2"; shift 2 ;;
    --no-ufw)  CONFIGURE_UFW=0; shift ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) die "未知参数: $1（用 --help 查看用法）" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "请用 root 运行（需要绑定 53/853/443 与写 systemd 单元）"
[[ -n "$DOMAIN" ]] || die "必须指定 --domain（例如 --domain example.com）"

# --- 架构识别 ---
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "不支持的架构: $ARCH" ;;
esac
PLATFORM="linux-${GOARCH}"
info "平台: ${PLATFORM}"

# --- 依赖检查 ---
for cmd in curl tar; do
  command -v "$cmd" >/dev/null 2>&1 || die "缺少命令: $cmd"
done

# --- 解析版本 ---
if [[ "$VERSION" == "latest" ]]; then
  info "查询最新版本..."
  VERSION=$(curl -fsSL --max-time 20 "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\(v[^"]*\)"/\1/') || true
  [[ -n "$VERSION" ]] || die "无法获取最新版本，请显式指定 --version v1.0.0"
fi
VER_NUM="${VERSION#v}"
info "安装版本: ${VERSION}"

# --- 目录 ---
mkdir -p "$INSTALL_DIR/data/runtime" "$INSTALL_DIR/data/certs"
cd "$INSTALL_DIR"

# --- 下载（带重试：GitHub 下载在国内网络下经常间歇性失败）---
download() {
  local name="$1" url="$2" out="$3"
  for i in 1 2 3 4 5; do
    if curl -fsSL --max-time 300 -o "$out" "$url"; then
      return 0
    fi
    warn "下载 $name 第 $i 次失败，重试..."
    sleep 3
  done
  return 1
}

BASE="https://github.com/${REPO}/releases/download/${VERSION}"
WORK=$(mktemp -d /tmp/aegis-install-XXXXXX)
trap 'rm -rf "$WORK"' EXIT

for comp in dnsd apid; do
  asset="aegis-${comp}-${VER_NUM}-${PLATFORM}.tar.gz"
  info "下载 ${asset}"
  download "$asset" "${BASE}/${asset}" "${WORK}/${asset}" || die "下载 ${asset} 失败"

  # 解压到独立目录再取二进制。
  # 不要用 `find /tmp -newer <tar.gz>` 找解压结果：tar 保留的是打包时的
  # 文件 mtime，而刚下载的 tar.gz 的 mtime 是「现在」，所以 -newer 永远不成立，
  # 这条路径必然走到 fallback —— 一个只在特定 tar 版本下才暴露的坑。
  mkdir -p "${WORK}/${comp}"
  tar xzf "${WORK}/${asset}" -C "${WORK}/${comp}"
  found=$(find "${WORK}/${comp}" -type f -name "aegis-${comp}" -print -quit)
  [[ -n "$found" ]] || die "压缩包 ${asset} 里没有找到 aegis-${comp}"
  install -m755 "$found" "${INSTALL_DIR}/aegis-${comp}" || die "安装 aegis-${comp} 失败"
  ok "aegis-${comp} 已安装（$("${INSTALL_DIR}/aegis-${comp}" -version 2>&1 | head -1)）"
done

# --- 校验和 ---
if download checksums.txt "${BASE}/checksums.txt" "${WORK}/checksums.txt" 2>/dev/null; then
  info "校验下载完整性（对比 Release 的 checksums.txt）..."
  okcount=0
  for comp in dnsd apid; do
    asset="aegis-${comp}-${VER_NUM}-${PLATFORM}.tar.gz"
    want=$(grep -E "[[:space:]]\*?${asset}$" "${WORK}/checksums.txt" | awk '{print $1}' | head -1)
    [[ -n "$want" ]] || continue
    got=$(sha256sum "${WORK}/${asset}" | awk '{print $1}')
    if [[ "$want" == "$got" ]]; then
      ok "aegis-${comp} 的压缩包 SHA-256 校验通过"
      okcount=$((okcount+1))
    else
      err "aegis-${comp} 校验和不匹配！期望 $want 实际 $got"
      die "下载内容与发布清单不符，已中止安装"
    fi
  done
  [[ $okcount -gt 0 ]] || warn "checksums.txt 里没有当前平台的记录，跳过校验"
fi

# --- 证书 ---
if [[ -n "$CERT" && -n "$KEY" ]]; then
  [[ -f "$CERT" ]] || die "证书文件不存在: $CERT"
  [[ -f "$KEY" ]]  || die "私钥文件不存在: $KEY"
  cp -f "$CERT" "$INSTALL_DIR/data/certs/fullchain.pem"
  cp -f "$KEY"  "$INSTALL_DIR/data/certs/privkey.pem"
  chmod 600 "$INSTALL_DIR/data/certs/privkey.pem"
  CERT_PATH="$INSTALL_DIR/data/certs/fullchain.pem"
  KEY_PATH="$INSTALL_DIR/data/certs/privkey.pem"
  ok "证书已安装"
  # 顺带校验一下证书是否覆盖 *.domain
  if command -v openssl >/dev/null 2>&1; then
    sans=$(openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null | tr -d ' ' || true)
    if echo "$sans" | grep -q "DNS:\*\.${DOMAIN}"; then
      ok "证书包含通配域名 *.${DOMAIN}"
    else
      warn "证书里没看到 *.${DOMAIN}，DoT/DoH 的子域名可能校验失败"
      warn "实际 SAN: ${sans:-（读取失败）}"
    fi
  fi
else
  CERT_PATH=""
  KEY_PATH=""
  warn "未提供证书：DoT 与 DoH 将不可用，只提供 UDP/TCP DNS"
fi

# --- 生成密钥与密码 ---
JWT_SECRET=$("${INSTALL_DIR}/aegis-apid" -gen-secret)
ADMIN_PASSWORD=$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | head -c 20)
UPSTREAM_LINE='  - "223.5.5.5"
  - "119.29.29.29"
  - "1.1.1.1"'

# --- .env ---
umask 077
cat > "${INSTALL_DIR}/.env" <<EOF
# 由 install.sh 生成于 $(date -Iseconds)
# 修改后执行 systemctl restart aegis-apid 生效。
JWT_SECRET=${JWT_SECRET}
ADMIN_PASSWORD=${ADMIN_PASSWORD}
EOF
chmod 600 "${INSTALL_DIR}/.env"
ok ".env 已生成（含 JWT 密钥与初始管理员密码）"

# --- config.yaml ---
if [[ -f "${INSTALL_DIR}/config.yaml" ]]; then
  cp -a "${INSTALL_DIR}/config.yaml" "${INSTALL_DIR}/config.yaml.bak-$(date +%Y%m%d-%H%M%S)"
  warn "已存在的 config.yaml 已备份，本次不覆盖（如需重建请先删除）"
else
  cat > "${INSTALL_DIR}/config.yaml" <<EOF
# 由 install.sh 生成于 $(date -Iseconds)
domain: "${DOMAIN}"
data_dir: "./data"

dns:
  listen_udp: ":53"
  listen_tcp: ":53"
  listen_dot: ":853"
  listen_doh: ":443"
  listen_doh_plain: ""
  cert_file: "${CERT_PATH}"
  key_file: "${KEY_PATH}"
  dev_tls: false
  acme:
    enabled: false
    provider: cloudflare
    token_env: CF_API_TOKEN
    email: ""
    directory: ""
  query_log_ring: 1000

api:
  listen: ":8080"
  # 443 被 dnsd 的 DoH 占用，面板走独立端口。
  tls_listen: ":8443"
  cert_file: ""
  key_file: ""
  jwt_secret: "\${JWT_SECRET}"
  admin:
    username: "${ADMIN_USER}"
    password: "\${ADMIN_PASSWORD}"
    email: ""
  trusted_proxies: []
  cors_origins: []

upstreams:
${UPSTREAM_LINE}

update:
  repo: "${REPO}"
  channel: "stable"

log:
  level: "info"
  format: "text"
EOF
  ok "config.yaml 已生成"
fi

# --- 处理 53 端口占用（文档风险 R2，这是最常见的启动失败原因）---
if command -v systemctl >/dev/null 2>&1; then
  if ss -lntu 2>/dev/null | awk '{print $5}' | grep -qE '127\.0\.0\.53:53$'; then
    warn "systemd-resolved 的 stub listener 正在占用 127.0.0.53:53，正在释放..."

    # 关键：必须写进主配置文件 /etc/systemd/resolved.conf。
    #
    # 在 /etc/systemd/resolved.conf.d/ 下放 drop-in 是更「标准」的做法，
    # `systemd-analyze cat-config` 也能看到它被合并进去了，但实测在
    # Ubuntu 22.04 的 systemd 249 上 **resolved 不会采纳 drop-in 里的
    # DNSStubListener**，重启后 53 端口照样被占。
    # 表现为 dnsd 启动日志里 "listen udp :53: bind: address already in use"，
    # 而 DoT/DoH 却正常 —— 很容易误判成端口检查脚本的假阳性。
    #
    # 同一文件内后出现的同名键会覆盖先出现的，所以在文件末尾追加一个
    # 只含该键的 [Resolve] 段即可，不必改写原有内容。
    cp -a /etc/systemd/resolved.conf "/etc/systemd/resolved.conf.bak-$(date +%Y%m%d-%H%M%S)"
    if ! grep -qE '^[[:space:]]*DNSStubListener[[:space:]]*=' /etc/systemd/resolved.conf; then
      printf '\n# 由 aegis-dns 安装脚本追加：把 53 端口让给 dnsd\n[Resolve]\nDNSStubListener=no\n' \
        >> /etc/systemd/resolved.conf
    else
      sed -i -E 's/^[[:space:]]*DNSStubListener[[:space:]]*=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
    fi
    # 同时放一份 drop-in：对将来会采纳它的 systemd 版本算是双保险。
    mkdir -p /etc/systemd/resolved.conf.d
    printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/99-aegis.conf
    chmod 644 /etc/systemd/resolved.conf.d/99-aegis.conf

    systemctl restart systemd-resolved || true
    sleep 2

    if ss -lntu 2>/dev/null | awk '{print $5}' | grep -qE '127\.0\.0\.53:53$'; then
      warn "53 端口仍被 systemd-resolved 占用，改为完全停用它"
      # 兜底：彻底停用。注意此时 /etc/resolv.conf 会失效，
      # 必须立刻写一份静态的，否则这台机器自己就解析不了域名了。
      systemctl disable --now systemd-resolved >/dev/null 2>&1 || true
      if [[ -L /etc/resolv.conf || -f /etc/resolv.conf ]]; then
        cp -a /etc/resolv.conf "/etc/resolv.conf.bak-$(date +%Y%m%d-%H%M%S)" 2>/dev/null || true
      fi
      rm -f /etc/resolv.conf
      cat > /etc/resolv.conf <<'RESOLV'
# 由 aegis-dns 安装脚本生成（systemd-resolved 已停用）
# 首选指向本机 dnsd；它没起来时回落到公共 DNS，避免整机失去解析能力。
nameserver 127.0.0.1
nameserver 223.5.5.5
nameserver 119.29.29.29
options timeout:2 attempts:2
RESOLV
      chattr +i /etc/resolv.conf 2>/dev/null || true
      ok "systemd-resolved 已停用，并写入了静态 /etc/resolv.conf"
    else
      ok "已释放 53 端口（改的是 /etc/systemd/resolved.conf，不是 drop-in）"
    fi

    # 确认整机解析没被搞坏
    if getent hosts example.com >/dev/null 2>&1; then
      ok "本机域名解析正常"
    else
      warn "本机域名解析异常，请检查 /etc/resolv.conf"
    fi
  fi
fi

# --- 运维脚本 ---
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
for f in selfcheck.sh dnsprobe.py; do
  if [[ -f "${SCRIPT_DIR}/${f}" ]]; then
    install -m755 "${SCRIPT_DIR}/${f}" "${INSTALL_DIR}/${f}"
  else
    curl -fsSL --max-time 60 -o "${INSTALL_DIR}/${f}" \
      "https://raw.githubusercontent.com/${REPO}/${VERSION}/deploy/${f}" 2>/dev/null \
      && chmod +x "${INSTALL_DIR}/${f}" \
      || warn "未能获取 ${f}（自检脚本），可稍后从仓库 deploy/ 目录手动拷贝"
  fi
done
[[ -x "${INSTALL_DIR}/selfcheck.sh" ]] && ok "自检脚本已就位（bash ${INSTALL_DIR}/selfcheck.sh）"

# --- systemd ---
if command -v systemctl >/dev/null 2>&1; then
  if [[ -f "${SCRIPT_DIR}/aegis-dnsd.service" ]]; then
    cp -f "${SCRIPT_DIR}/aegis-dnsd.service" "${SCRIPT_DIR}/aegis-apid.service" /etc/systemd/system/
  else
    # 单文件运行（curl | bash）时从仓库取。
    for u in aegis-dnsd aegis-apid; do
      curl -fsSL --max-time 60 -o "/etc/systemd/system/${u}.service" \
        "https://raw.githubusercontent.com/${REPO}/${VERSION}/deploy/${u}.service" \
        || warn "下载 ${u}.service 失败，请手动放置"
    done
  fi
  systemctl daemon-reload
  systemctl enable aegis-dnsd aegis-apid >/dev/null 2>&1 || true
  ok "systemd 单元已注册并设为开机自启"
fi

# --- 防火墙 ---
if [[ $CONFIGURE_UFW -eq 1 ]] && command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  for p in 53/udp 53/tcp 853/tcp 443/tcp 8443/tcp; do
    ufw allow "$p" >/dev/null 2>&1 || true
  done
  ok "已放行 53/853/443/8443"
  warn "别忘了在云厂商控制台的安全组里也放行同样的端口"
fi

# --- 启动 ---
if command -v systemctl >/dev/null 2>&1; then
  systemctl restart aegis-dnsd aegis-apid
  sleep 2
  for u in aegis-dnsd aegis-apid; do
    if systemctl is-active --quiet "$u"; then
      ok "$u 已启动"
    else
      err "$u 启动失败，日志："
      journalctl -u "$u" -n 25 --no-pager || true
    fi
  done
fi

PUBLIC_IP=$(curl -fsS --max-time 10 https://ifconfig.me 2>/dev/null || echo "<本机公网IP>")

cat <<EOF

${GREEN}============================================================${NC}
${GREEN} 安装完成${NC}
${GREEN}============================================================${NC}

  ${BLUE}管理面板${NC}   https://${DOMAIN}:8443
  ${BLUE}管理员账号${NC} ${ADMIN_USER}
  ${BLUE}管理员密码${NC} ${ADMIN_PASSWORD}
                ${DIM}（同时保存在 ${INSTALL_DIR}/.env，登录后请立即修改）${NC}

  ${BLUE}API 文档${NC}   https://${DOMAIN}:8443/api/v1/docs

  ${BLUE}DoH${NC}        https://<你的client_id>.${DOMAIN}/dns-query
  ${BLUE}DoT${NC}        <你的client_id>.${DOMAIN}:853
  ${BLUE}明文 DNS${NC}   ${PUBLIC_IP}:53（需先把来源 IP 登记到账号下）

  ${DIM}注册后每个账号会拿到自己的 client_id，面板首页会直接给出可用地址。${NC}

  自检：  bash ${INSTALL_DIR}/selfcheck.sh
  日志：  journalctl -u aegis-dnsd -f
          journalctl -u aegis-apid -f

${YELLOW} 提醒：${NC}如果外网解析不通，先检查云厂商安全组是否放行 53/853/443。
${GREEN}============================================================${NC}
EOF
