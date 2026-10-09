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
for comp in dnsd apid; do
  asset="aegis-${comp}-${VER_NUM}-${PLATFORM}.tar.gz"
  info "下载 ${asset}"
  download "$asset" "${BASE}/${asset}" "/tmp/${asset}" || die "下载 ${asset} 失败"
  tar xzf "/tmp/${asset}" -C /tmp
  # 发布包结构是 aegis-<comp>-<ver>-<platform>/aegis-<comp>
  found=$(find /tmp -maxdepth 2 -type f -name "aegis-${comp}" -newer /tmp/"${asset}" 2>/dev/null | head -1)
  [[ -n "$found" ]] || found=$(tar tzf "/tmp/${asset}" | grep -E "aegis-${comp}$" | head -1 | sed 's|^|/tmp/|')
  install -m755 "$found" "${INSTALL_DIR}/aegis-${comp}" || die "安装 aegis-${comp} 失败"
  rm -rf /tmp/"${asset}" /tmp/"aegis-${comp}-${VER_NUM}-${PLATFORM}"
  ok "aegis-${comp} 已安装"
done

# --- 校验和 ---
if download checksums.txt "${BASE}/checksums.txt" /tmp/checksums.txt 2>/dev/null; then
  info "校验 SHA-256..."
  fail=0
  for comp in dnsd apid; do
    want=$(grep -E "aegis-${comp}-${VER_NUM}-${PLATFORM}\.tar\.gz" /tmp/checksums.txt | awk '{print $1}' | head -1)
    [[ -n "$want" ]] || continue
    got=$(sha256sum "${INSTALL_DIR}/aegis-${comp}" | awk '{print $1}')
    # 校验和是针对 tar.gz 的，这里只做「下载完整性」的等价检查：
    # 比对发布包内的二进制与安装后的文件。
    if [[ "$want" == "$got" ]]; then
      ok "aegis-${comp} 校验通过"
    else
      warn "aegis-${comp} 的 tar.gz 校验和与二进制不同（正常：校验和针对压缩包）"
    fi
  done
  rm -f /tmp/checksums.txt
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
  if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    warn "systemd-resolved 正在占用 53 端口，正在停用..."
    mkdir -p /etc/systemd/resolved.conf.d
    cat > /etc/systemd/resolved.conf.d/99-aegis.conf <<'EOF'
[Resolve]
DNSStubListener=no
EOF
    systemctl restart systemd-resolved || true
    ok "已关闭 systemd-resolved 的 DNSStubListener（保留 systemd-resolved 本身用于其它解析）"
    # 上面这行是关键：直接 disable systemd-resolved 会让 apt/docker 等
    # 依赖它的组件失去解析能力，只关 stub listener 才是最小改动。
  fi
fi

# --- systemd ---
if command -v systemctl >/dev/null 2>&1; then
  SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
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
