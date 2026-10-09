#!/usr/bin/env bash
# ============================================================================
# 对「已部署的实例」做端到端功能验证。
#
# 与 e2e-local.sh 的区别：
#   e2e-local.sh   在开发机上跑，用非特权端口临时起一套服务，验证代码正确性
#   本脚本         在（或对着）一台已经部署好的机器跑，验证「这一套部署能用」
#
# 用法（在目标机器上执行）：
#   bash verify-deployed.sh --api http://127.0.0.1:8080
#   bash verify-deployed.sh --api http://127.0.0.1:8080 --domain example.com \
#        --udp 127.0.0.1:53 --dot 127.0.0.1:853 --doh https://127.0.0.1
#
# 会真实注册一个临时账号并写入规则，测试完自动删除该账号。
# 参数 --keep-user 可保留账号便于人工排查。
# ============================================================================
set -uo pipefail

API="http://127.0.0.1:8080"
DOMAIN=""
UDP_TARGET="127.0.0.1:53"
TCP_TARGET="127.0.0.1:53"
DOT_TARGET="127.0.0.1:853"
DOH_URL="https://127.0.0.1"
KEEP_USER=0
PROBE=""

GREEN=$'\033[32m'; RED=$'\033[31m'; BLUE=$'\033[34m'; DIM=$'\033[2m'; NC=$'\033[0m'
PASS=0; FAIL=0
ok()  { echo "  ${GREEN}✅${NC} $*"; PASS=$((PASS+1)); }
bad() { echo "  ${RED}❌${NC} $*"; FAIL=$((FAIL+1)); }
hdr() { echo; echo "${BLUE}── $* ─────────────────────────────────────────${NC}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --api)   API="$2"; shift 2 ;;
    --domain) DOMAIN="$2"; shift 2 ;;
    --udp)   UDP_TARGET="$2"; shift 2 ;;
    --tcp)   TCP_TARGET="$2"; shift 2 ;;
    --dot)   DOT_TARGET="$2"; shift 2 ;;
    --doh)   DOH_URL="$2"; shift 2 ;;
    --probe) PROBE="$2"; shift 2 ;;
    --keep-user) KEEP_USER=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数: $1"; exit 2 ;;
  esac
done

# 找探针脚本
if [[ -z "$PROBE" ]]; then
  for p in "$(dirname "${BASH_SOURCE[0]}")/dnsprobe.py" /opt/aegis/dnsprobe.py ./dnsprobe.py; do
    [[ -f "$p" ]] && { PROBE="python3 $p"; break; }
  done
fi
[[ -n "$PROBE" ]] || { echo "找不到 dnsprobe.py，请用 --probe 指定"; exit 2; }

command -v curl >/dev/null || { echo "需要 curl"; exit 2; }
command -v python3 >/dev/null || { echo "需要 python3"; exit 2; }

echo
echo "${BLUE}┌─────────────────────────────────────────────────────────┐${NC}"
echo "${BLUE}│  AegisDNS 已部署实例功能验证                             │${NC}"
echo "${BLUE}└─────────────────────────────────────────────────────────┘${NC}"
echo "  API:      $API"
echo "  明文 DNS: $UDP_TARGET (udp) / $TCP_TARGET (tcp)"
echo "  DoT:      $DOT_TARGET"
echo "  DoH:      $DOH_URL"

# --- 1. 基础接口 ---
hdr "[1/8] 基础接口"
H=$(curl -fsS --max-time 8 "$API/api/v1/healthz" 2>/dev/null)
[[ "$H" == *'"ok"'* ]] && ok "healthz: $H" || bad "healthz 异常: ${H:-无响应}"
R=$(curl -fsS --max-time 8 "$API/api/v1/readyz" 2>/dev/null)
[[ "$R" == *'"ready"'* ]] && ok "readyz: $R" || bad "readyz 异常: ${R:-无响应}"
V=$(curl -fsS --max-time 8 "$API/api/v1/version" 2>/dev/null)
echo "  version: $V"
[[ "$V" == *'"version"'* ]] && ok "version 可读" || bad "version 异常"
if [[ "$V" == *'"frontend":true'* ]]; then
  ok "二进制内嵌了真实前端"
else
  bad "二进制未内嵌前端（frontend=false），面板会显示占位页"
fi

# --- 2. 管理员登录 ---
hdr "[2/8] 管理员登录"
ADM_USER="${AEGIS_ADMIN_USER:-admin}"
ADM_PASS="${AEGIS_ADMIN_PASSWORD:-}"
if [[ -z "$ADM_PASS" && -f /opt/aegis/.env ]]; then
  ADM_PASS=$(grep -E '^ADMIN_PASSWORD=' /opt/aegis/.env | cut -d= -f2-)
fi
ADMTOK=""
if [[ -n "$ADM_PASS" ]]; then
  ADMTOK=$(curl -s --max-time 8 -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADM_USER\",\"password\":\"$ADM_PASS\"}" \
    | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
fi
if [[ -n "$ADMTOK" ]]; then
  ok "管理员 $ADM_USER 登录成功"
  SYS=$(curl -fsS --max-time 8 "$API/api/v1/admin/system" -H "Authorization: Bearer $ADMTOK")
  echo "  system: $(echo "$SYS" | head -c 500)"
  if [[ -z "$DOMAIN" ]]; then
    DOMAIN=$(echo "$SYS" | grep -o '"domain":"[^"]*"' | head -1 | cut -d'"' -f4)
  fi
  ok "主域名 = ${DOMAIN:-未配置}"
else
  bad "管理员登录失败（可用 AEGIS_ADMIN_PASSWORD 环境变量提供密码）"
fi

# --- 3. 注册临时租户 ---
hdr "[3/8] 注册租户并获取专属接入配置"
U="verify$RANDOM$RANDOM"
P="Ver1fy!${RANDOM}"
REG=$(curl -s --max-time 15 -X POST "$API/api/v1/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$U\",\"email\":\"$U@verify.local\",\"password\":\"$P\"}")
CID=$(echo "$REG" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
TOKEN=$(echo "$REG" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
UID=$(echo "$REG" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
if [[ -n "$CID" && -n "$TOKEN" ]]; then
  ok "注册成功 username=$U client_id=$CID"
  [[ -z "$DOMAIN" ]] && DOMAIN=$(echo "$REG" | grep -o '"domain":"[^"]*"' | head -1 | cut -d'"' -f4)
else
  bad "注册失败: $(echo "$REG" | head -c 300)"
  exit 1
fi
echo "  DoH  $DOH_URL/dns-query  (Host: $CID.${DOMAIN:-<domain>})"
echo "  DoT  $DOT_TARGET          (SNI:  $CID.${DOMAIN:-<domain>})"

# --- 4. 规则与来源 IP ---
hdr "[4/8] 规则与来源 IP 归属"
add_rule() {
  local code
  code=$(curl -s -o /tmp/vr.out -w '%{http_code}' -X POST "$API/api/v1/rules" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$1")
  [[ "$code" == "201" ]] && ok "创建规则 $code" || bad "创建规则失败 $code $(cat /tmp/vr.out)"
}
add_rule '{"kind":"block","pattern":"||verify-block.test^","value":"","qtypes":[]}'
add_rule '{"kind":"allow","pattern":"||safe.verify-block.test^","value":"","qtypes":[]}'
add_rule '{"kind":"rewrite_a","pattern":"||verify-rewrite.test^","value":"10.99.99.99","qtypes":["A"]}'

# UDP/TCP 没有 SNI/Host，只能靠来源 IP 识别租户。
code=$(curl -s -o /tmp/vr.out -w '%{http_code}' -X POST "$API/api/v1/me/ips" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"cidr":"127.0.0.1"}')
[[ "$code" == "201" ]] && ok "登记来源 IP 127.0.0.1 → $(cat /tmp/vr.out)" || bad "登记来源 IP 失败 $code"

# --- 5. 等快照热加载 ---
hdr "[5/8] 等配置快照热加载到 dnsd"
loaded=0
for i in $(seq 1 30); do
  if [[ "$($PROBE udp "$UDP_TARGET" verify-block.test 2>/dev/null)" == *"0.0.0.0"* ]]; then loaded=1; break; fi
  sleep 1
done
[[ $loaded -eq 1 ]] && ok "新规则已在 dnsd 生效（热加载成功）" || bad "规则未在 30 秒内生效"

# --- 6. 四协议验证 ---
hdr "[6/8] 四种协议各自的解析结果"
t() {
  local desc="$1" want="$2"; shift 2
  local out
  out=$($PROBE "$@" 2>&1 | tail -1)
  if [[ "$out" == *"$want"* ]]; then ok "$desc → $out"; else bad "$desc → $out（期望含 $want）"; fi
}

t "UDP 拦截"        "0.0.0.0"   udp "$UDP_TARGET" verify-block.test
t "UDP 子域继承"    "0.0.0.0"   udp "$UDP_TARGET" x.verify-block.test
t "UDP 白名单放行"  "NOERROR"   udp "$UDP_TARGET" safe.verify-block.test
t "UDP 改写"        "10.99.99.99" udp "$UDP_TARGET" verify-rewrite.test
t "UDP 正常转发"    "NOERROR"   udp "$UDP_TARGET" example.com
t "TCP 拦截"        "0.0.0.0"   tcp "$TCP_TARGET" verify-block.test
t "TCP 正常转发"    "NOERROR"   tcp "$TCP_TARGET" example.com

if [[ -n "$DOMAIN" ]]; then
  t "DoT 拦截（SNI 识别租户）" "0.0.0.0" dot "$DOT_TARGET" verify-block.test A --sni "$CID.$DOMAIN" --insecure
  t "DoT 正常转发"             "NOERROR" dot "$DOT_TARGET" example.com A --sni "$CID.$DOMAIN" --insecure
  t "DoH 拦截（Host 首段）"    "0.0.0.0" doh "$DOH_URL" --host "$CID.$DOMAIN" --insecure verify-block.test
  t "DoH 改写（URL 路径段）"   "10.99.99.99" doh "$DOH_URL" --path "/dns-query/$CID" --insecure verify-rewrite.test
  t "DoH 正常转发"             "NOERROR" doh "$DOH_URL" --host "$CID.$DOMAIN" --insecure example.com
else
  echo "  ${DIM}未获取到主域名，跳过 DoT/DoH 的子域名识别测试${NC}"
fi

# 缓存命中：连查 5 次必须次次成功（报文 ID 未重置会导致第二次起全超时）
cacheok=1
for i in 1 2 3 4 5; do
  [[ "$($PROBE udp "$UDP_TARGET" example.com 2>/dev/null)" == *"NOERROR"* ]] || { cacheok=0; break; }
done
[[ $cacheok -eq 1 ]] && ok "连续 5 次查询（含缓存命中）全部成功" || bad "缓存命中后应答异常"

# --- 7. 订阅与统计 ---
hdr "[7/8] 订阅与统计"
PRESETS=$(curl -fsS --max-time 8 "$API/api/v1/subscriptions/presets" -H "Authorization: Bearer $TOKEN" | grep -o '"name"' | wc -l)
[[ "$PRESETS" -gt 0 ]] && ok "推荐订阅可用（$PRESETS 个）" || bad "推荐订阅为空"
SUB=$(curl -s --max-time 120 -X POST "$API/api/v1/subscriptions" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"OISD Basic","url":"https://big.oisd.nl/domainswild","list_type":"blocklist","format":"auto","enabled":true}')
SUBCOUNT=$(echo "$SUB" | grep -o '"last_count":[0-9]*' | cut -d: -f2)
if [[ "${SUBCOUNT:-0}" -gt 1000 ]]; then
  ok "订阅拉取成功，条目数 $SUBCOUNT"
  # OISD 里确实存在的通配条目，验证「*.domain 也匹配根域自身」
  for i in $(seq 1 60); do
    [[ "$($PROBE udp "$UDP_TARGET" accounts.doubleclick.net 2>/dev/null)" == *"0.0.0.0"* ]] && break
    sleep 1
  done
  t "订阅拦截通配根域" "0.0.0.0" udp "$UDP_TARGET" accounts.doubleclick.net
  t "订阅拦截子域"     "0.0.0.0" udp "$UDP_TARGET" x.accounts.doubleclick.net
else
  bad "订阅拉取异常: $(echo "$SUB" | head -c 300)"
fi

for i in $(seq 1 70); do
  TOT=$(curl -fsS --max-time 8 "$API/api/v1/stats/summary?range=24h" -H "Authorization: Bearer $TOKEN" 2>/dev/null | grep -o '"total":[0-9]*' | cut -d: -f2)
  [[ "${TOT:-0}" -gt 0 ]] && break
  sleep 1
done
[[ "${TOT:-0}" -gt 0 ]] && ok "统计已落库（total=$TOT）" || bad "统计未落库（total=${TOT:-0}）"
echo "  summary: $(curl -fsS --max-time 8 "$API/api/v1/stats/summary?range=24h" -H "Authorization: Bearer $TOKEN" | head -c 250)"
echo "  top:     $(curl -fsS --max-time 8 "$API/api/v1/stats/top?range=24h&limit=3" -H "Authorization: Bearer $TOKEN" | head -c 250)"

# --- 8. 隔离与权限 ---
hdr "[8/8] 多租户隔离与权限"
REG2=$(curl -s --max-time 15 -X POST "$API/api/v1/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"${U}b\",\"email\":\"${U}b@verify.local\",\"password\":\"$P\"}")
CID2=$(echo "$REG2" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
TOKEN2=$(echo "$REG2" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
N2=$(curl -fsS --max-time 8 "$API/api/v1/rules" -H "Authorization: Bearer $TOKEN2" | grep -o '"total":[0-9]*' | cut -d: -f2)
[[ "$N2" == "0" ]] && ok "第二个租户看不到别人的规则（total=0）" || bad "跨租户数据泄漏：total=$N2"
if [[ -n "$DOMAIN" && -n "$CID2" ]]; then
  OUT=$($PROBE doh "$DOH_URL" --path "/dns-query/$CID2" --insecure verify-block.test 2>&1 | tail -1)
  [[ "$OUT" != *"0.0.0.0"* ]] && ok "第二个租户不受别人规则影响 → $OUT" || bad "跨租户规则泄漏：$OUT"
fi
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/admin/users" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "普通租户访问 /admin/* → 403" || bad "越权：$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/rules")
[[ "$code" == "401" ]] && ok "未认证访问 → 401" || bad "未认证：$code"

# --- 清理 ---
hdr "清理测试账号"
if [[ $KEEP_USER -eq 1 ]]; then
  echo "  ${DIM}--keep-user 已指定，保留账号 $U 与 ${U}b${NC}"
elif [[ -n "$ADMTOK" ]]; then
  for id in "$UID" "$(echo "$REG2" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"; do
    [[ -z "$id" ]] && continue
    code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/api/v1/admin/users/$id" -H "Authorization: Bearer $ADMTOK")
    [[ "$code" == "204" ]] && ok "已删除测试账号 $id" || bad "删除 $id 失败：$code"
  done
else
  echo "  ${DIM}无管理员令牌，未清理（账号名以 verify 开头，可手动删除）${NC}"
fi

echo
echo "════════════════════════════════════════════════════"
echo "  结果：${GREEN}$PASS 通过${NC} / ${RED}$FAIL 失败${NC}"
echo "════════════════════════════════════════════════════"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
