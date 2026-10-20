#!/usr/bin/env bash
# ============================================================================
# 本地端到端联调：全部跑在非特权端口上，不依赖任何外部服务。
#
# 验证链路：
#   编译 → 启动 apid+dnsd → 注册 → 建规则 → 重建快照 → dnsd 热加载
#   → UDP / TCP / DoT / DoH 四种协议各自的解析结果
#   → 多租户隔离 → 统计落库 → 管理接口与越权检查
#
# 用法：bash deploy/e2e-local.sh [--keep]
#   --keep  结束后不清理 /tmp/aegis-e2e，便于人工排查
# ============================================================================
set -uo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../server" && pwd)"
DEPLOY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN=/tmp/aegis-e2e
PROBE="python3 ${DEPLOY}/dnsprobe.py"
export PATH=$PATH:/usr/local/go/bin

KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

PASS=0; FAIL=0
GREEN=$'\033[32m'; RED=$'\033[31m'; BLUE=$'\033[34m'; DIM=$'\033[2m'; NC=$'\033[0m'
ok()   { echo "  ${GREEN}✅${NC} $*"; PASS=$((PASS+1)); }
bad()  { echo "  ${RED}❌${NC} $*"; FAIL=$((FAIL+1)); }
hdr()  { echo; echo "${BLUE}── $* ─────────────────────────────────────────${NC}"; }

wait_for() {
  local tries="$1" delay="$2"; shift 2
  for _ in $(seq 1 "$tries"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep "$delay"
  done
  return 1
}

cleanup() {
  kill "$DNSD_PID" "$APID_PID" 2>/dev/null
  wait "$DNSD_PID" "$APID_PID" 2>/dev/null
  if [[ $KEEP -eq 0 ]]; then rm -rf "$RUN"; else echo "（保留运行目录 $RUN）"; fi
}
trap cleanup EXIT

rm -rf "$RUN"; mkdir -p "$RUN/data/runtime" "$RUN/data/certs"

hdr "编译"
cd "$SRC" || exit 1
GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$RUN/aegis-dnsd" ./cmd/dnsd || { echo "dnsd 编译失败"; exit 1; }
GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$RUN/aegis-apid" ./cmd/apid || { echo "apid 编译失败"; exit 1; }
ok "两个二进制编译完成  $("$RUN/aegis-dnsd" -version)"

hdr "生成配置"
JWT=$("$RUN/aegis-apid" -gen-secret)
cat > "$RUN/.env" <<EOF
JWT_SECRET=$JWT
ADMIN_PASSWORD=E2eTestPass123
EOF
cat > "$RUN/config.yaml" <<EOF
domain: "dns.e2e.local"
data_dir: "$RUN/data"
dns:
  listen_udp: "127.0.0.1:15353"
  listen_tcp: "127.0.0.1:15353"
  listen_dot: "127.0.0.1:18853"
  listen_doh: "127.0.0.1:14443"
  listen_doh_plain: "127.0.0.1:18053"
  dev_tls: true
  query_log_ring: 500
  # 联调用 3 秒：默认 60 秒的话，测试跑完了统计还没落库。
  stats_flush_seconds: 3
api:
  listen: "127.0.0.1:18080"
  tls_listen: ""
  jwt_secret: "\${JWT_SECRET}"
  admin:
    username: "admin"
    password: "\${ADMIN_PASSWORD}"
upstreams: ["223.5.5.5", "119.29.29.29", "1.1.1.1"]
update:
  repo: "50521136/aegis-dns"
  channel: "stable"
log:
  level: "info"
EOF
set -a; source "$RUN/.env"; set +a
ok "配置与密钥就绪（上游用国内可达的 223.5.5.5 优先）"

hdr "启动前自检"
"$RUN/aegis-dnsd" -config "$RUN/config.yaml" -check 2>&1 | tail -14
"$RUN/aegis-apid" -config "$RUN/config.yaml" -check 2>&1 | tail -10

hdr "启动服务"
"$RUN/aegis-dnsd" -config "$RUN/config.yaml" > "$RUN/dnsd.log" 2>&1 &
DNSD_PID=$!
"$RUN/aegis-apid" -config "$RUN/config.yaml" > "$RUN/apid.log" 2>&1 &
APID_PID=$!
API=http://127.0.0.1:18080/api/v1

wait_for 40 0.5 curl -fsS --max-time 2 "$API/healthz" && ok "apid 健康检查通过" \
  || { bad "apid 未就绪"; tail -25 "$RUN/apid.log"; }
wait_for 40 0.5 bash -c "$PROBE udp 127.0.0.1:15353 example.com" && ok "dnsd UDP 已可解析" \
  || { bad "dnsd UDP 未就绪"; tail -25 "$RUN/dnsd.log"; }

hdr "全局设置播种（config.yaml 的 upstreams 必须生效）"
ADMTOK=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"E2eTestPass123"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
UP=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -o '"upstreams":\[[^]]*\]')
echo "  当前全局上游: $UP"
if echo "$UP" | grep -q '223.5.5.5'; then ok "config.yaml 的 upstreams 已播种到全局设置"; else bad "upstreams 播种失败：$UP"; fi

hdr "注册用户"
REG=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"e2etest","email":"e2e@test.local","password":"Str0ngPass123"}')
CID=$(echo "$REG" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
TOKEN=$(echo "$REG" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
REFRESH=$(echo "$REG" | grep -o '"refresh_token":"[^"]*"' | head -1 | cut -d'"' -f4)
[[ -n "$CID" && -n "$TOKEN" ]] && ok "注册成功 client_id=$CID" || bad "注册失败: $REG"
echo "  DoH(子域名)  https://$CID.dns.e2e.local/dns-query"
echo "  DoH(路径式)  https://dns.e2e.local/dns-query/$CID"
echo "  DoT          $CID.dns.e2e.local:18853"

hdr "令牌流程"
NEWPAIR=$(curl -fsS -X POST "$API/auth/refresh" -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}")
NEWTOK=$(echo "$NEWPAIR" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
[[ -n "$NEWTOK" ]] && ok "refresh 换发新令牌成功" || bad "refresh 失败: $NEWPAIR"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/auth/refresh" -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}")
[[ "$code" == "401" ]] && ok "旧 refresh token 已失效（轮转生效）" || bad "轮转未生效，旧令牌返回 $code"
TOKEN=$NEWTOK
PAT=$(curl -fsS -X POST "$API/me/tokens" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-script"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
[[ "$PAT" == agx_* ]] && ok "PAT 创建成功" || bad "PAT 创建失败"
curl -fsS "$API/me" -H "Authorization: Bearer $PAT" | grep -q '"username":"e2etest"' \
  && ok "PAT 可用于鉴权" || bad "PAT 鉴权失败"

hdr "iOS 描述文件（.mobileconfig）"
MC_HDR=$(curl -s -D - -o "$RUN/e2e.mobileconfig" "$API/me/mobileconfig" -H "Authorization: Bearer $TOKEN")
echo "$MC_HDR" | grep -qi 'content-type: application/x-apple-aspen-config' \
  && ok "MIME 是 x-apple-aspen-config（iOS 才认）" || bad "MIME 不对: $(echo "$MC_HDR" | grep -i content-type)"
echo "$MC_HDR" | grep -qi 'attachment; filename="dnsforge-.*\.mobileconfig"' \
  && ok "Content-Disposition 带文件名" || bad "缺 Content-Disposition"
grep -q 'com.apple.dnsSettings.managed' "$RUN/e2e.mobileconfig" \
  && ok "载荷类型是 com.apple.dnsSettings.managed" || bad "载荷类型不对"
grep -q '<string>HTTPS</string>' "$RUN/e2e.mobileconfig" \
  && ok "DNSProtocol 是 HTTPS" || bad "DNSProtocol 不对"
# 描述文件里的 ServerURL 必须和 /me/dns-config 主推的地址一致 ——
# 装一个连不上的地址比不给还糟：用户会以为是自己设备的问题。
MC_URL=$(grep -o '<string>https://[^<]*' "$RUN/e2e.mobileconfig" | head -1 | sed 's/<string>//')
[[ "$MC_URL" == "https://dns.e2e.local/dns-query/$CID" ]] \
  && ok "ServerURL 是路径式地址（当前证书无通配符）" || bad "ServerURL 不对: $MC_URL"
# plist 必须是合法 XML，否则 iOS 会直接拒绝安装
python3 -c "import plistlib,sys; plistlib.load(open('$RUN/e2e.mobileconfig','rb'))" 2>/dev/null \
  && ok "plist 可被解析（结构合法）" || bad "plist 解析失败"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/me/mobileconfig")
[[ "$code" == "401" ]] && ok "未认证下载 → 401" || bad "未认证下载返回 $code"

hdr "添加规则"
add_rule() {
  local body="$1" code
  code=$(curl -s -o /tmp/rule.out -w '%{http_code}' -X POST "$API/rules" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body")
  if [[ "$code" == "201" ]]; then ok "201  $body"; else bad "失败 $code $(cat /tmp/rule.out)"; fi
}
add_rule '{"kind":"block","pattern":"||ads.example.com^","value":"","qtypes":[]}'
add_rule '{"kind":"allow","pattern":"||safe.ads.example.com^","value":"","qtypes":[]}'
add_rule '{"kind":"rewrite_a","pattern":"||nas.home^","value":"192.168.1.10","qtypes":["A"]}'
add_rule '{"kind":"block","pattern":"||onlyaaaa.test^","value":"","qtypes":["AAAA"]}'
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/rules" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"kind":"block","pattern":"||^","value":""}')
[[ "$code" == "400" ]] && ok "非法规则被拒绝（400）" || bad "非法规则未被拒绝：$code"

hdr "批量导入"
curl -fsS -X POST "$API/rules/import" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"content":"||import1.test^\n0.0.0.0 import2.test\n@@||import3.test^\n# 注释\n||^\n","format":"auto","enabled":true}'; echo

hdr "登记来源 IP（UDP/TCP 靠它识别租户）"
# 设计如此：明文 DNS 没有 SNI/Host 可依据，只能靠来源 IP 归属。
# 不登记的话请求会落到 fallback 策略（默认 passthrough，不做规则过滤）。
code=$(curl -s -o /tmp/ip.out -w '%{http_code}' -X POST "$API/me/ips" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"cidr":"127.0.0.1"}')
if [[ "$code" == "201" ]]; then ok "已登记来源 IP 127.0.0.1 → $(cat /tmp/ip.out)"; else bad "登记来源 IP 失败 $code $(cat /tmp/ip.out)"; fi

hdr "等快照热加载（轮询探测而非猜时间）"
loaded=0
for i in $(seq 1 40); do
  if [[ "$($PROBE udp 127.0.0.1:15353 ads.example.com 2>/dev/null)" == *"0.0.0.0"* ]]; then loaded=1; break; fi
  sleep 0.5
done
[[ $loaded -eq 1 ]] && ok "新规则已在 dnsd 生效（热加载成功）" || bad "规则未在 20 秒内生效"
grep "配置快照已加载" "$RUN/dnsd.log" | tail -3 | sed 's/^/  /'

hdr "缓存命中必须能正常应答（连续查同一域名）"
# 回归测试：缓存副本带着首次请求的报文 ID，若不重置会让客户端丢弃应答，
# 表现为「第一次能查、之后全超时」。这里连查 5 次，必须次次成功。
cacheok=1
for i in 1 2 3 4 5; do
  if [[ "$($PROBE udp 127.0.0.1:15353 example.com 2>/dev/null)" != *"NOERROR"* ]]; then
    cacheok=0; bad "第 $i 次查询 example.com 失败（疑似缓存命中未重置报文 ID）"; break
  fi
done
[[ $cacheok -eq 1 ]] && ok "连续 5 次查询（含缓存命中）全部成功"

hdr "UDP 解析"
t() {
  local desc="$1" want="$2"; shift 2
  local out
  out=$($PROBE "$@" 2>&1 | tail -1)
  if [[ "$want" == "EMPTY" ]]; then
    if [[ "$out" == *"(无应答记录)"* || "$out" == *"NXDOMAIN"* ]]; then ok "$desc → $out"; else bad "$desc → $out（期望无记录）"; fi
  else
    if [[ "$out" == *"$want"* ]]; then ok "$desc → $out"; else bad "$desc → $out（期望含 $want）"; fi
  fi
}

t "自定义 block"          "0.0.0.0"      udp 127.0.0.1:15353 ads.example.com
t "子域继承"              "0.0.0.0"      udp 127.0.0.1:15353 x.ads.example.com
t "白名单优先"            "NOERROR"      udp 127.0.0.1:15353 safe.ads.example.com
t "改写 A"                "192.168.1.10" udp 127.0.0.1:15353 nas.home
t "改写不污染 AAAA"       "EMPTY"        udp 127.0.0.1:15353 nas.home AAAA
# AAAA 拦截返回 :: （不是 0.0.0.0）——IPv6 的「黑洞地址」语义
t "dnstype 限定拦 AAAA"   "0000:0000"    udp 127.0.0.1:15353 onlyaaaa.test AAAA
# 限定 AAAA 的规则不该影响 A 查询：域名不存在，所以是 NXDOMAIN 而不是被拦成 0.0.0.0
t "dnstype 限定放行 A"    "NXDOMAIN"     udp 127.0.0.1:15353 onlyaaaa.test A
t "导入规则生效"          "0.0.0.0"      udp 127.0.0.1:15353 import1.test
t "导入 hosts 行"         "0.0.0.0"      udp 127.0.0.1:15353 import2.test
t "正常域名转发"          "NOERROR"      udp 127.0.0.1:15353 example.com
t "不存在的域名"          "EMPTY"        udp 127.0.0.1:15353 nonexistent-xyz-12345.test

hdr "TCP 解析"
t "TCP 拦截"              "0.0.0.0"      tcp 127.0.0.1:15353 ads.example.com
t "TCP 正常"              "NOERROR"      tcp 127.0.0.1:15353 example.com

hdr "DoT 解析（SNI 识别租户）"
t "DoT 带正确 SNI"        "0.0.0.0"      dot 127.0.0.1:18853 ads.example.com A --sni "$CID.dns.e2e.local" --insecure
t "DoT 无 SNI 走兜底"     "NOERROR"      dot 127.0.0.1:18853 example.com A --insecure

hdr "DoH 解析"
t "DoH Host 首段识别"     "0.0.0.0"      doh https://127.0.0.1:14443 --host "$CID.dns.e2e.local" --insecure ads.example.com
t "DoH 路径段识别"        "192.168.1.10" doh https://127.0.0.1:14443 --path "/dns-query/$CID" --insecure nas.home
t "明文 DoH（调试端口）"  "0.0.0.0"      doh http://127.0.0.1:18053 --path "/dns-query/$CID" ads.example.com
t "DoH 正常域名"          "NOERROR"      doh https://127.0.0.1:14443 --host "$CID.dns.e2e.local" --insecure example.com

hdr "缓存与统计"
$PROBE udp 127.0.0.1:15353 example.org >/dev/null 2>&1
$PROBE udp 127.0.0.1:15353 example.org >/dev/null 2>&1
# 等一个落库周期（配置里设成 3 秒）
for i in $(seq 1 20); do
  TOT=$(curl -fsS "$API/stats/summary?range=24h" -H "Authorization: Bearer $TOKEN" | grep -o '"total":[0-9]*' | cut -d: -f2)
  [[ "${TOT:-0}" -gt 0 ]] && break
  sleep 1
done
SUM=$(curl -fsS "$API/stats/summary?range=24h" -H "Authorization: Bearer $TOKEN")
echo "  summary: $SUM"
if [[ "${TOT:-0}" -gt 0 ]]; then ok "统计已落库并可通过 API 查询（total=$TOT）"; else bad "统计未落库（total=$TOT）"; fi
echo "  top:      $(curl -fsS "$API/stats/top?range=24h&limit=3" -H "Authorization: Bearer $TOKEN" | head -c 400)"
QL=$(curl -fsS "$API/stats/querylog?limit=3" -H "Authorization: Bearer $TOKEN")
echo "  querylog: $(echo "$QL" | head -c 400)"
echo "$QL" | grep -q '"domain"' && ok "查询日志已落库" || bad "查询日志为空"

hdr "订阅系统"
echo "  预置订阅数: $(curl -fsS "$API/subscriptions/presets" -H "Authorization: Bearer $TOKEN" | grep -o '"name"' | wc -l)"
SUB=$(curl -s -X POST "$API/subscriptions" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"OISD Basic","url":"https://big.oisd.nl/domainswild","list_type":"blocklist","format":"auto","enabled":true}')
SUBID=$(echo "$SUB" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
SUBCOUNT=$(echo "$SUB" | grep -o '"last_count":[0-9]*' | cut -d: -f2)
# 新建订阅后的拉取走后台任务（避免慢线路上下载 60~90 秒时被客户端断开砍掉），
# 接口立刻返回 pending。这里要轮询等它真正同步完，不能拿第一次响应判断。
if [[ -n "$SUBID" && "${SUBCOUNT:-0}" -le 1000 ]]; then
  for _ in $(seq 1 240); do
    SROW=$(curl -s "$API/subscriptions" -H "Authorization: Bearer $TOKEN")
    SUBCOUNT=$(echo "$SROW" | grep -o '"last_count":[0-9]*' | head -1 | cut -d: -f2)
    SUBSTAT=$(echo "$SROW" | grep -o '"last_status":"[^"]*"' | head -1 | cut -d'"' -f4)
    [[ "${SUBCOUNT:-0}" -gt 1000 ]] && break
    [[ "$SUBSTAT" == error* ]] && break
    sleep 1
  done
fi
if [[ -n "$SUBID" && "${SUBCOUNT:-0}" -gt 1000 ]]; then
  ok "订阅拉取成功，条目数 $SUBCOUNT"
else
  # 报错要打轮询后的真实状态，而不是创建时的响应（那样看不出到底卡在哪）
  bad "订阅拉取异常: 条目=${SUBCOUNT:-0} 状态=${SUBSTAT:-未知} 响应=$(echo "${SROW:-$SUB}" | head -c 240)"
fi
if [[ -n "$SUBID" ]]; then
  # OISD 的列表里没有 *.doubleclick.net（只有具体子域），所以挑一条确实存在的
  # 通配条目 *.accounts.doubleclick.net 来验证：
  #   查询 accounts.doubleclick.net  —— 验证「*.domain 也匹配根域自身」
  #   查询 x.accounts.doubleclick.net —— 验证子域继承
  for i in $(seq 1 60); do
    if $PROBE udp 127.0.0.1:15353 accounts.doubleclick.net 2>/dev/null | grep -c '0.0.0.0' >/dev/null 2>&1; then break; fi
    sleep 1
  done
  t "订阅拦截通配根域（accounts.doubleclick.net）" "0.0.0.0" udp 127.0.0.1:15353 accounts.doubleclick.net
  t "订阅拦截子域（x.accounts.doubleclick.net）"   "0.0.0.0" udp 127.0.0.1:15353 x.accounts.doubleclick.net
fi

hdr "多租户隔离"
REG2=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"e2etest2","email":"e2e2@test.local","password":"Str0ngPass456"}')
CID2=$(echo "$REG2" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
TOKEN2=$(echo "$REG2" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  第二个用户 client_id=$CID2（没有任何规则）"
sleep 2
OUT=$($PROBE doh https://127.0.0.1:14443 --path "/dns-query/$CID2" --insecure ads.example.com 2>&1 | tail -1)
if [[ "$OUT" != *"0.0.0.0"* ]]; then ok "用户2 未被用户1 的规则影响 → $OUT"; else bad "跨租户规则泄漏：$OUT"; fi
N=$(curl -fsS "$API/rules" -H "Authorization: Bearer $TOKEN2" | grep -o '"total":[0-9]*' | cut -d: -f2)
[[ "$N" == "0" ]] && ok "用户2 规则列表为空（数据隔离）" || bad "用户2 看到了 $N 条规则"
RID=$(curl -fsS "$API/rules?page_size=1" -H "Authorization: Bearer $TOKEN" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/rules/$RID" -H "Authorization: Bearer $TOKEN2")
[[ "$code" == "404" ]] && ok "跨租户删规则返回 404" || bad "跨租户删规则返回 $code"

hdr "权限检查"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/users" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "普通用户访问 /admin/* → 403" || bad "越权：$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/rules")
[[ "$code" == "401" ]] && ok "未认证访问 → 401" || bad "未认证：$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/rules" -H "Authorization: Bearer bogus.token.value")
[[ "$code" == "401" ]] && ok "伪造令牌 → 401" || bad "伪造令牌：$code"

hdr "管理员接口"
echo "  system:  $(curl -fsS "$API/admin/system" -H "Authorization: Bearer $ADMTOK" | head -c 400)"
echo "  users:   $(curl -fsS "$API/admin/users" -H "Authorization: Bearer $ADMTOK" | head -c 260)"
echo "  rebuild: $(curl -fsS -X POST "$API/admin/snapshot/rebuild" -H "Authorization: Bearer $ADMTOK")"
echo "  audit:   $(curl -fsS "$API/admin/audit?limit=3" -H "Authorization: Bearer $ADMTOK" | head -c 260)"
echo "  update:  $(curl -fsS "$API/update/check" -H "Authorization: Bearer $ADMTOK" | head -c 300)"

hdr "管理员全局统计与日志"
UT=$(curl -fsS "$API/admin/stats/users?range=24h" -H "Authorization: Bearer $ADMTOK")
echo "$UT" | grep -q '"username"' && ok "排行榜带用户名" || bad "排行榜缺少 username: $(echo "$UT" | head -c 200)"
echo "$UT" | grep -q '"total"' && ok "排行榜带请求量" || bad "排行榜缺少 total"
echo "$UT" | grep -q '"block_rate"' && ok "排行榜带拦截率" || bad "排行榜缺少 block_rate"

GL=$(curl -fsS "$API/admin/stats/querylog?limit=5" -H "Authorization: Bearer $ADMTOK")
echo "$GL" | grep -q '"username"' && ok "全局日志带用户名" || bad "全局日志缺少 username"
echo "$GL" | grep -q '"user_id"' && ok "全局日志带 user_id" || bad "全局日志缺少 user_id"

# 这两个接口是管理员专属，普通租户必须拿不到
for p in /admin/stats/users /admin/stats/querylog; do
  c=$(curl -s -o /dev/null -w '%{http_code}' "$API$p" -H "Authorization: Bearer $TOKEN")
  [[ "$c" == "403" ]] && ok "普通用户访问 $p → 403" || bad "越权 $p：$c"
done

hdr "接入地址按证书能力选择形态"
# 测试实例用的是自签证书（SAN 只有 * 和 localhost，没有 *.dns.e2e.local），
# 所以必须给出路径式 DoH + 裸域名 DoT —— 给子域名形态的话客户端握手就会被拒。
DC=$(curl -fsS "$API/me/dns-config" -H "Authorization: Bearer $TOKEN")
echo "$DC" | grep -q '"doh_url_path":"https://dns.e2e.local/dns-query/' && ok "给出路径式 DoH 地址" || bad "缺路径式 DoH"
DOUH=$(echo "$DC" | grep -o '"doh_url":"[^"]*"' | cut -d'"' -f4)
DOUP=$(echo "$DC" | grep -o '"doh_url_path":"[^"]*"' | cut -d'"' -f4)
[[ "$DOUH" == "$DOUP" ]] && ok "无通配符证书时主推地址 = 路径式（$DOUH）" || bad "主推地址仍是子域名形态：$DOUH"
DOTA=$(echo "$DC" | grep -o '"dot_addr":"[^"]*"' | cut -d'"' -f4)
DOTP=$(echo "$DC" | grep -o '"dot_port":[0-9]*' | cut -d: -f2)
[[ "$DOTA" == "dns.e2e.local:$DOTP" ]] && ok "无通配符证书时 DoT 用裸域名（$DOTA）" || bad "DoT 地址不对：$DOTA"
# 证书无通配符时，指南里不能出现子域名 DoH 地址
echo "$DC" | grep -q "https://[a-z0-9]*\\.dns\\.e2e\\.local/dns-query" && bad "指南里仍有子域名 DoH 地址" || ok "指南文案已跟着换成可用地址"

hdr "用户对象暴露新字段"
ME=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN")
for f in id_reset_count rules_enabled subs_enabled use_global_rules; do
  echo "$ME" | grep -q "\"$f\"" && ok "/me 含 $f" || bad "/me 缺 $f: $(echo "$ME" | head -c 200)"
done
UL=$(curl -fsS "$API/admin/users" -H "Authorization: Bearer $ADMTOK")
echo "$UL" | grep -q '"rules_enabled"' && ok "用户列表含 rules_enabled" || bad "用户列表缺 rules_enabled"
echo "$UL" | grep -q '"id_reset_count"' && ok "用户列表含 id_reset_count" || bad "用户列表缺 id_reset_count"

hdr "设置持久化与租户开关"
# 之前配额五项只写不读，后台改了不生效。这里验证一轮往返。
SET_BEFORE=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"domain":"dns.e2e.local","allow_register":true,"enable_query_log":true,
       "allow_root_domain":false,"allow_user_rules":false,"allow_user_subs":true,
       "max_id_resets":2,"stats_retention_days":0,"max_rules_per_user":7,
       "max_subs_per_user":2,"max_sub_entries":1000,"max_rules_global":100,"max_subs_global":5}' >/dev/null
SET_AFTER=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SET_AFTER" | grep -q '"max_rules_per_user":7' && ok "配额 max_rules_per_user 已持久化" || bad "配额未持久化"
echo "$SET_AFTER" | grep -q '"max_sub_entries":1000' && ok "配额 max_sub_entries 已持久化" || bad "配额未持久化"
echo "$SET_AFTER" | grep -q '"allow_root_domain":false' && ok "主域名开关已持久化（能存 false）" || bad "主域名开关未持久化"
echo "$SET_AFTER" | grep -q '"max_id_resets":2' && ok "重置次数上限已持久化" || bad "重置次数未持久化"

# 全局关掉用户加规则后，普通用户必须被拒
code=$(curl -s -o /tmp/e2e_feat.json -w '%{http_code}' -X POST "$API/rules" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"kind":"block","pattern":"||feat.test^","value":"","qtypes":[],"enabled":false}')
[[ "$code" == "403" ]] && ok "关闭后普通用户建规则 → 403" || bad "关闭后仍可建规则：$code"
grep -q 'FEATURE_DISABLED' /tmp/e2e_feat.json && ok "错误码是 FEATURE_DISABLED" || bad "错误码不对"
# 订阅总开关还是开的，应该能建
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/subscriptions" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"t","url":"https://example.com/x.txt","list_type":"blocklist","format":"auto","enabled":false}')
[[ "$code" == "201" ]] && ok "订阅开关独立生效（仍可建）" || bad "订阅被误伤：$code"

hdr "重置接入 ID"
R1=$(curl -s -X POST "$API/me/reset-client-id" -H "Authorization: Bearer $TOKEN")
NEWCID=$(echo "$R1" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
[[ -n "$NEWCID" && "$NEWCID" != "$CID" ]] && ok "自助重置成功，新 ID=$NEWCID" || bad "重置失败: $R1"
echo "$R1" | grep -q '"remaining":1' && ok "剩余次数 1（上限 2）" || bad "剩余次数不对: $R1"
R2=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/reset-client-id" -H "Authorization: Bearer $TOKEN")
[[ "$R2" == "200" ]] && ok "第 2 次重置成功" || bad "第 2 次重置失败：$R2"
R3=$(curl -s -o /tmp/e2e_r3.json -w '%{http_code}' -X POST "$API/me/reset-client-id" -H "Authorization: Bearer $TOKEN")
[[ "$R3" == "403" ]] && ok "超限后第 3 次被拒（403）" || bad "超限未拦住：$R3"
grep -q '重置次数已用完' /tmp/e2e_r3.json && ok "超限提示明确" || bad "超限提示不对"

hdr "管理员改密码与批量删除"
UTOK=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"e2etest","password":"Str0ngPass123"}' | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
TID=$(curl -fsS "$API/me" -H "Authorization: Bearer $UTOK" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/admin/users/$TID/password" \
  -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' -d '{"password":"NewStr0ngPass1"}')
[[ "$code" == "200" ]] && ok "管理员改密码成功" || bad "管理员改密码失败：$code"
curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"e2etest","password":"NewStr0ngPass1"}' | grep -q access_token \
  && ok "新密码可登录" || bad "新密码登录失败"
# 改密后旧 refresh token 必须失效
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/auth/refresh" -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}")
[[ "$code" == "401" ]] && ok "改密后旧 refresh token 已失效" || bad "旧令牌仍可用：$code"

# 批量删除：造两个临时用户再一起删
B1=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"bulk1","email":"b1@t.local","password":"Str0ngPass123"}' | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
B2=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"bulk2","email":"b2@t.local","password":"Str0ngPass123"}' | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
BD=$(curl -fsS -X POST "$API/admin/users/bulk-delete" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d "{\"ids\":[\"$B1\",\"$B2\"]}")
echo "$BD" | grep -q '"deleted":2' && ok "批量删除 2 个用户" || bad "批量删除异常: $BD"

hdr "主域名开关（DNS 解析层）"
# 先把开关关掉：裸域名 SNI 必须被 REFUSED
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"domain":"dns.e2e.local","allow_register":true,"enable_query_log":true,
       "allow_root_domain":false,"allow_user_rules":true,"allow_user_subs":true,
       "max_id_resets":2,"stats_retention_days":0,"max_rules_per_user":50,
       "max_subs_per_user":3,"max_sub_entries":50000,"max_rules_global":500,"max_subs_global":20}' >/dev/null
sleep 4
OUT=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  关: $OUT"
echo "$OUT" | grep -qiE 'REFUSED|拒绝' && ok "关闭时裸域名 → REFUSED" || bad "关闭时裸域名未被拒: $OUT"

# 打开后再查一次：应当正常解析（走全局层 / 上游）
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"domain":"dns.e2e.local","allow_register":true,"enable_query_log":true,
       "allow_root_domain":true,"allow_user_rules":true,"allow_user_subs":true,
       "max_id_resets":2,"stats_retention_days":0,"max_rules_per_user":50,
       "max_subs_per_user":3,"max_sub_entries":50000,"max_rules_global":500,"max_subs_global":20}' >/dev/null
sleep 4
OUT2=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  开: $OUT2"
echo "$OUT2" | grep -qiE 'REFUSED|拒绝' && bad "开启后裸域名仍被拒: $OUT2" || ok "开启后裸域名可解析"

# 子域名不受开关影响
OUT3=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "$CID.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "$OUT3" | grep -qiE 'REFUSED|拒绝' && bad "子域名被误伤: $OUT3" || ok "子域名不受主域名开关影响"

hdr "日志与统计清理"
CL=$(curl -fsS -X POST "$API/admin/maintenance/cleanup" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{}')
echo "$CL" | grep -q '"total"' && ok "手动清理接口可用" || bad "清理接口异常: $CL"
# stats_retention_days=0 时绝不能把统计删光（now-0 会把历史全干掉）
# retention=0 时统计清理任务根本不会加入，key 直接不出现（而不是删了 0 行）
echo "$CL" | grep -q '"query_rollup"' && bad "统计被误删: $CL" || ok "统计保留 0 天时不按时间删（保护生效）"

hdr "订阅强制刷新"
# 条目上限/解析规则改完后，缓存过的订阅会一直走 304 不重算，
# 管理员改了设置却看不到变化。?force=1 给一个明确的「现在就重算」入口。
# 用管理员令牌：/admin/* 对普通用户是 403，拿普通令牌测只会测出越权保护
code=$(curl -s -o /tmp/e2e_force.json -w '%{http_code}' -X POST "$API/admin/subscriptions/refresh-all?force=1" \
  -H "Authorization: Bearer $ADMTOK")
[[ "$code" == "202" || "$code" == "200" ]] && ok "refresh-all 接受 force 参数（$code）" || bad "force 参数无效：$code"
# 单个刷新也要支持
SUBID=$(curl -fsS "$API/admin/subscriptions" -H "Authorization: Bearer $ADMTOK" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -n "$SUBID" ]; then
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/admin/subscriptions/$SUBID/refresh?force=1" \
    -H "Authorization: Bearer $ADMTOK")
  [[ "$code" == "202" || "$code" == "200" ]] && ok "单个刷新接受 force 参数（$code）" || bad "单个 force 无效：$code"
fi

hdr "自定义接入 ID（一次机会）"
# 必须从 /me 取当前 ID：前面的「重置接入 ID」用例已经把注册时的 $CID 换掉了，
# 拿旧变量去比会误判成「值不同」，进而把这一次机会白白用掉。
CUR=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  当前 client_id: $CUR"
# 太短
code=$(curl -s -o /tmp/e2e_cid1.json -w '%{http_code}' -X POST "$API/me/client-id" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"client_id":"abcde"}')
[[ "$code" == "400" ]] && ok "5 位被拒（400）" || bad "短 ID 未拦住：$code"
# 非法字符
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/client-id" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"client_id":"home-dns"}')
[[ "$code" == "400" ]] && ok "含连字符被拒（400）" || bad "非法字符未拦住：$code"
# 与主域名首段冲突（域名为 dns.e2e.local，首段是 dns）
code=$(curl -s -o /tmp/e2e_cid2.json -w '%{http_code}' -X POST "$API/me/client-id" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"client_id":"dns"}')
[[ "$code" == "400" ]] && ok "与主域名首段冲突被拒（400）" || bad "主域名首段未拦：$code"
# 和当前 ID 相同
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/client-id" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"client_id\":\"$CUR\"}")
[[ "$code" == "400" ]] && ok "与当前 ID 相同被拒（400，不消耗机会）" || bad "同值未拦：$code"

# 正常设置
NEWID="myhome$RANDOM"
SET=$(curl -s -X POST "$API/me/client-id" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{\"client_id\":\"$NEWID\"}")
echo "$SET" | grep -q "\"client_id\":\"$NEWID\"" && ok "自定义成功：$NEWID" || bad "自定义失败: $SET"
echo "$SET" | grep -q "\"old_client_id\":\"$CUR\"" && ok "返回了旧 ID" || bad "未返回旧 ID"

# 改完之后 DNS 层必须立刻认新 ID（快照要重建）
sleep 3
OUT=$($PROBE dot 127.0.0.1:18853 customcheck.example.com A --sni "$NEWID.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "$OUT" | grep -qiE 'NOERROR|无应答' && ok "新 ID 在 DoT 上立刻生效" || bad "新 ID 未生效: $OUT"
OUT=$($PROBE dot 127.0.0.1:18853 customcheck.example.com A --sni "$CUR.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  旧 ID 现在: $OUT"

# 第二次必须被拒
code=$(curl -s -o /tmp/e2e_cid3.json -w '%{http_code}' -X POST "$API/me/client-id" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"client_id":"another01"}')
[[ "$code" == "403" ]] && ok "第二次自定义被拒（403）" || bad "第二次仍成功：$code"
grep -q 'CUSTOM_ID_USED' /tmp/e2e_cid3.json && ok "错误码是 CUSTOM_ID_USED" || bad "错误码不对"

# /me 上要能读到 custom_id_used
curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -q '"custom_id_used":true' \
  && ok "/me 反映 custom_id_used=true" || bad "/me 未反映已用"

# 占用冲突：另造一个用户占掉一个 id，再用管理员给第三个用户设同一个
BID="taken$RANDOM"
B1=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"cid1$RANDOM\",\"email\":\"c1$RANDOM@t.local\",\"password\":\"Str0ngPass123\"}" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
B2=$(curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"cid2$RANDOM\",\"email\":\"c2$RANDOM@t.local\",\"password\":\"Str0ngPass123\"}" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
curl -fsS -X POST "$API/admin/users/$B1/client-id" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d "{\"client_id\":\"$BID\"}" >/dev/null
code=$(curl -s -o /tmp/e2e_cid4.json -w '%{http_code}' -X POST "$API/admin/users/$B2/client-id" \
  -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' -d "{\"client_id\":\"$BID\"}")
[[ "$code" == "409" ]] && ok "重复 ID 冲突（409）" || bad "冲突未拦住：$code"
grep -q 'CLIENT_ID_TAKEN' /tmp/e2e_cid4.json && ok "错误码是 CLIENT_ID_TAKEN" || bad "错误码不对"
# 管理员设置不消耗用户的次数
curl -fsS "$API/admin/users?q=cid2" -H "Authorization: Bearer $ADMTOK" | grep -q '"custom_id_used":false' \
  && ok "管理员设置不消耗用户机会" || bad "管理员设置消耗了用户机会"

hdr "内嵌前端"
# 用 /version 的 frontend 字段判断，而不是抓 HTML 前几百字节 ——
# 真实 index.html 开头是一段内联主题脚本，assets/ 引用在后面，
# 按字节截断判断会得到假阴性。
FE=$(curl -fsS http://127.0.0.1:18080/api/v1/version 2>/dev/null | grep -o '"frontend":[a-z]*' | cut -d: -f2)
if [[ "$FE" == "true" ]]; then
  ok "已内嵌真实前端（/version 的 frontend=true）"
  # 静态资源必须能真的取到，否则页面白屏
  JS=$(curl -fsS http://127.0.0.1:18080/ 2>/dev/null | grep -o 'assets/index-[A-Za-z0-9_-]*\.js' | head -1)
  if [[ -n "$JS" ]]; then
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080/$JS")
    [[ "$code" == "200" ]] && ok "入口 JS 可访问（$JS → 200）" || bad "入口 JS 取不到：$JS → $code"
    # 内容哈希命名的资源必须带强缓存头
    cc=$(curl -sI "http://127.0.0.1:18080/$JS" | grep -i '^cache-control' | tr -d '\r')
    [[ "$cc" == *"immutable"* ]] && ok "静态资源带 immutable 强缓存（$cc）" || bad "静态资源缓存头异常：$cc"
  else
    bad "index.html 里没找到入口 JS 引用"
  fi
else
  echo "  ${DIM}当前是占位页（二进制未内嵌 web/dist）${NC}"
fi
# 注意：不要写成 `curl ... | grep -q PATTERN`。
# grep -q 找到匹配就立刻退出，大响应会让 curl 收到 SIGPIPE 而非零退出，
# 在 `set -o pipefail` 下整条管道被判定为失败 —— 一个纯粹的假阴性。
DOCS=$(curl -fsS http://127.0.0.1:18080/api/v1/docs 2>/dev/null)
if [[ "$DOCS" == *"DNSForge API"* ]]; then ok "API 文档页可访问"; else bad "API 文档页异常"; fi
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/app/rules)
[[ "$code" == "200" ]] && ok "SPA 路由回落正常（/app/rules → 200）" || bad "SPA 回落失败：$code"
# API 404 必须是 JSON 而不是 SPA 的 index.html
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/nope)
[[ "$code" == "404" ]] && ok "未知 API 路径返回 404" || bad "未知 API 路径返回 $code"
# 安全头
HDRS=$(curl -sI http://127.0.0.1:18080/ | tr -d '\r')
[[ "$HDRS" == *"Content-Security-Policy"* ]] && ok "响应带 CSP 头" || bad "缺少 CSP 头"

hdr "统计落库"
sleep 3
python3 - "$RUN/data/aegis.db" <<'PY'
import sqlite3, os, sys
p = sys.argv[1]
if not os.path.exists(p):
    print("  数据库不存在"); raise SystemExit
c = sqlite3.connect(p)
for t in ("users","rules","subscriptions","sub_entries","query_rollup","top_domains","query_log","pat_tokens","meta"):
    try:
        n = c.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0]
        print(f"  {t:16s} {n}")
    except Exception as e:
        print(f"  {t:16s} 读取失败 {e}")
PY

hdr "优雅关闭"
kill -TERM "$DNSD_PID" "$APID_PID" 2>/dev/null
wait "$DNSD_PID" 2>/dev/null
wait "$APID_PID" 2>/dev/null
grep -q "已退出" "$RUN/dnsd.log" && ok "dnsd 优雅退出" || bad "dnsd 未正常退出"
grep -q "已退出" "$RUN/apid.log" && ok "apid 优雅退出" || bad "apid 未正常退出"

echo
echo "════════════════════════════════════════════════════"
echo "  结果：${GREEN}$PASS 通过${NC} / ${RED}$FAIL 失败${NC}"
echo "════════════════════════════════════════════════════"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
