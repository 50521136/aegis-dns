#!/usr/bin/env bash
# ============================================================================
# aegis-dns 部署自检
#
# 纯 bash，无 Docker 依赖。逐项检查并给出可操作的结论，
# 而不是让人去猜「服务明明是 active 的为什么解析不了」。
#
# 用法：bash /opt/aegis/selfcheck.sh
# ============================================================================
set -uo pipefail

DIR="${AEGIS_DIR:-/opt/aegis}"
CONFIG="${DIR}/config.yaml"

RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BLUE=$'\033[34m'; DIM=$'\033[2m'; NC=$'\033[0m'
PASS=0; WARN=0; FAIL=0

row() { printf '  %-42s %s\n' "$1" "$2"; }
ok()   { row "$1" "${GREEN}✅ $2${NC}"; PASS=$((PASS+1)); }
wn()   { row "$1" "${YELLOW}⚠️  $2${NC}"; WARN=$((WARN+1)); }
bad()  { row "$1" "${RED}❌ $2${NC}"; FAIL=$((FAIL+1)); }

# 极简 YAML 取值：只处理 `key: value` 与两级缩进，够用于自检。
yaml_get() {
  local path="$1" file="$2"
  local section="${path%%.*}" key="${path##*.}"
  if [[ "$section" == "$key" ]]; then
    grep -E "^${key}:" "$file" 2>/dev/null | head -1 | sed -E 's/^[^:]+:[[:space:]]*//; s/^["'\'']//; s/["'\'']$//' | tr -d '\r'
  else
    awk -v sec="$section" -v k="$key" '
      $0 ~ "^"sec":" {insec=1; next}
      insec && /^[^[:space:]]/ {insec=0}
      insec && $0 ~ "^[[:space:]]+"k":" {
        sub("^[[:space:]]+"k":[[:space:]]*", ""); gsub(/^["'\'']|["'\'']$/, ""); print; exit
      }' "$file" 2>/dev/null | tr -d '\r'
  fi
}

echo
echo "${BLUE}┌─────────────────────────────────────────────────────────┐${NC}"
echo "${BLUE}│  AegisDNS 部署自检                                      │${NC}"
echo "${BLUE}└─────────────────────────────────────────────────────────┘${NC}"
echo

# --- 1. 二进制 ---
echo "${DIM}[1/9] 二进制与版本${NC}"
for bin in aegis-dnsd aegis-apid; do
  if [[ -x "${DIR}/${bin}" ]]; then
    v=$("${DIR}/${bin}" -version 2>&1 | head -1)
    ok "$bin" "${v:-（-version 无输出）}"
  else
    bad "$bin" "不存在或不可执行（${DIR}/${bin}）"
  fi
done

# --- 2. 配置 ---
echo
echo "${DIM}[2/9] 配置${NC}"
if [[ -f "$CONFIG" ]]; then
  DOMAIN=$(yaml_get domain "$CONFIG")
  DATA_DIR=$(yaml_get data_dir "$CONFIG")
  ok "config.yaml" "存在"
  if [[ -n "$DOMAIN" ]]; then
    ok "主域名" "$DOMAIN"
  else
    bad "主域名" "未配置（dnsd 会拒绝启动）"
  fi
  [[ -n "$DATA_DIR" ]] && ok "数据目录" "$DATA_DIR"
else
  bad "config.yaml" "不存在：$CONFIG"
  DOMAIN=""
fi

# --- 3. .env ---
echo
echo "${DIM}[3/9] 密钥${NC}"
if [[ -f "${DIR}/.env" ]]; then
  # shellcheck disable=SC1091
  set -a; source "${DIR}/.env"; set +a
  if [[ -n "${JWT_SECRET:-}" && ${#JWT_SECRET} -ge 32 ]]; then
    ok "JWT_SECRET" "已设置（长度 ${#JWT_SECRET}）"
  else
    bad "JWT_SECRET" "未设置或过短（需 ≥32 字符）"
  fi
  [[ -n "${ADMIN_PASSWORD:-}" ]] && ok "ADMIN_PASSWORD" "已设置" || wn "ADMIN_PASSWORD" "未设置（管理员已创建时不影响）"
else
  wn ".env" "不存在：${DIR}/.env"
fi

# --- 4. 端口 ---
echo
echo "${DIM}[4/9] 端口监听${NC}"
check_listen() {
  local proto="$1" port="$2" name="$3"
  if command -v ss >/dev/null 2>&1; then
    if ss -lntu 2>/dev/null | awk '{print $1, $5}' | grep -qE "^${proto}.*:${port}$"; then
      ok "$name (:${port}/${proto})" "监听中"
    else
      if [[ "$name" == "UDP DNS" || "$name" == "TCP DNS" ]]; then
        bad "$name (:${port}/${proto})" "未监听"
      else
        wn "$name (:${port}/${proto})" "未监听（无证书时属预期）"
      fi
    fi
  else
    wn "ss 命令" "不可用，跳过端口检查"
  fi
}
check_listen udp 53 "UDP DNS"
check_listen tcp 53 "TCP DNS"
check_listen tcp 853 "DoT"
check_listen tcp 443 "DoH"
check_listen tcp 8443 "面板 HTTPS"
check_listen tcp 8080 "API HTTP"

# --- 5. systemd ---
echo
echo "${DIM}[5/9] systemd 服务${NC}"
if command -v systemctl >/dev/null 2>&1; then
  for u in aegis-dnsd aegis-apid; do
    if systemctl is-active --quiet "$u"; then
      since=$(systemctl show "$u" -p ActiveEnterTimestamp --value)
      restarts=$(systemctl show "$u" -p NRestarts --value)
      ok "$u" "active（自 ${since}，重启 ${restarts} 次）"
    else
      bad "$u" "$(systemctl is-failed "$u" 2>/dev/null || echo inactive)"
      journalctl -u "$u" -n 8 --no-pager 2>/dev/null | sed 's/^/      /'
    fi
  done
else
  wn "systemctl" "不可用"
fi

# --- 6. 证书 ---
echo
echo "${DIM}[6/9] 证书${NC}"
CERT_PATH=$(yaml_get dns.cert_file "$CONFIG" 2>/dev/null || true)
[[ -n "${CERT_PATH:-}" ]] || CERT_PATH="${DIR}/data/certs/fullchain.pem"
if [[ -f "$CERT_PATH" ]] && command -v openssl >/dev/null 2>&1; then
  end=$(openssl x509 -in "$CERT_PATH" -noout -enddate 2>/dev/null | cut -d= -f2)
  if [[ -n "$end" ]]; then
    end_ts=$(date -d "$end" +%s 2>/dev/null || echo 0)
    now_ts=$(date +%s)
    days=$(( (end_ts - now_ts) / 86400 ))
    san=$(openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null | tail -1 | tr -d ' ')
    if [[ $days -lt 0 ]]; then
      bad "证书" "已过期 ${days#-} 天（${end}）"
    elif [[ $days -lt 14 ]]; then
      wn "证书" "剩余 ${days} 天（${end}）—— 请尽快续期"
    else
      ok "证书" "剩余 ${days} 天（${end}）"
    fi
    if [[ -n "${DOMAIN:-}" ]] && echo "$san" | grep -q "DNS:\*\.${DOMAIN}"; then
      ok "证书覆盖 *.${DOMAIN}" "SAN 正确"
    elif [[ -n "${DOMAIN:-}" ]]; then
      wn "证书 SAN" "未覆盖 *.${DOMAIN}：${san:0:80}"
    fi
  fi
else
  wn "证书" "未找到（${CERT_PATH}）—— DoT/DoH 不可用"
fi

# --- 7. DNS 解析 ---
echo
echo "${DIM}[7/9] DNS 解析${NC}"
if command -v dig >/dev/null 2>&1; then
  if dig +short +time=3 +tries=1 @127.0.0.1 example.com A >/dev/null 2>&1; then
    ok "UDP 53 解析" "通过"
  else
    bad "UDP 53 解析" "失败（服务未监听或上游不可达）"
  fi
else
  wn "dig 命令" "不可用（apt install dnsutils）"
fi

# --- 8. API ---
echo
echo "${DIM}[8/9] API${NC}"
api_port=8080
if command -v curl >/dev/null 2>&1; then
  h=$(curl -fsS --max-time 5 "http://127.0.0.1:${api_port}/api/v1/healthz" 2>/dev/null || true)
  if [[ -n "$h" ]]; then
    ok "GET /api/v1/healthz" "通过"
  else
    bad "GET /api/v1/healthz" "无响应（apid 未运行？）"
  fi
  r=$(curl -fsS --max-time 5 "http://127.0.0.1:${api_port}/api/v1/readyz" 2>/dev/null || true)
  if echo "$r" | grep -q '"ready"'; then
    ok "GET /api/v1/readyz" "通过"
  else
    wn "GET /api/v1/readyz" "${r:-无响应}"
  fi
  # 前端是否真的内嵌了
  body=$(curl -fsS --max-time 5 "http://127.0.0.1:${api_port}/" 2>/dev/null || true)
  if echo "$body" | grep -q 'assets/'; then
    ok "内嵌前端" "已带上构建产物"
  else
    wn "内嵌前端" "返回的是占位页（二进制未内嵌 web/dist）"
  fi
else
  wn "curl 命令" "不可用"
fi

# --- 9. 快照与数据 ---
echo
echo "${DIM}[9/9] 配置快照${NC}"
SNAP="${DIR}/data/runtime/config.json"
if [[ -f "$SNAP" ]]; then
  size=$(stat -c%s "$SNAP" 2>/dev/null || echo 0)
  ver=$(head -c 64 "$SNAP" 2>/dev/null | grep -o '"version":[0-9]*' | head -1 | cut -d: -f2)
  users=$(grep -o '"user_id"' "$SNAP" 2>/dev/null | wc -l | tr -d ' ')
  ok "config.json" "version=${ver:-?} 用户=${users} 大小=$((size/1024))KB"
  mtime=$(stat -c%Y "$SNAP" 2>/dev/null || echo 0)
  age=$(( $(date +%s) - mtime ))
  if [[ $age -gt 604800 ]]; then
    wn "快照更新时间" "已 ${age} 秒未更新（一周以上没配置变更属正常）"
  fi
else
  bad "config.json" "不存在：${SNAP}（apid 尚未写入快照）"
fi

if [[ -f "${DIR}/data/aegis.db" ]]; then
  ok "SQLite" "$(du -h "${DIR}/data/aegis.db" | cut -f1)"
fi

# --- 汇总 ---
echo
echo "${BLUE}┌─────────────────────────────────────────────────────────┐${NC}"
printf "${BLUE}│${NC}  结果：${GREEN}%d 通过${NC} / ${YELLOW}%d 警告${NC} / ${RED}%d 失败${NC}\n" "$PASS" "$WARN" "$FAIL"
echo "${BLUE}└─────────────────────────────────────────────────────────┘${NC}"
echo

if [[ $FAIL -gt 0 ]]; then
  echo "排障顺序建议："
  echo "  1. journalctl -u aegis-dnsd -n 50 --no-pager"
  echo "  2. 53 端口被占：systemctl status systemd-resolved"
  echo "  3. 外网不通但本机通：检查云厂商安全组"
  echo
  exit 1
fi
exit 0
