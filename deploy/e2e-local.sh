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

# 协议可选：?proto=dot 要生成 TLS 形态（ServerName），不能是 HTTPS
curl -s "$API/me/mobileconfig?proto=dot" -H "Authorization: Bearer $TOKEN" -o "$RUN/e2e-dot.mobileconfig"
grep -q '<string>TLS</string>' "$RUN/e2e-dot.mobileconfig" \
  && ok "proto=dot 生成 TLS 形态" || bad "DoT 描述文件协议不对"
grep -q '<key>ServerName</key>' "$RUN/e2e-dot.mobileconfig" \
  && ok "DoT 描述文件带 ServerName" || bad "缺 ServerName"
grep -q '<key>ServerURL</key>' "$RUN/e2e-dot.mobileconfig" \
  && bad "TLS 形态不该有 ServerURL" || ok "TLS 形态不含 ServerURL"
python3 -c "import plistlib; plistlib.load(open('$RUN/e2e-dot.mobileconfig','rb'))" 2>/dev/null \
  && ok "DoT plist 结构合法" || bad "DoT plist 解析失败"
# 地址绝不能为空：空地址的描述文件装上后系统显示已安装但解析全挂
grep -qE '<key>(ServerURL|ServerName)</key>[[:space:]]*<string></string>' "$RUN/e2e.mobileconfig" "$RUN/e2e-dot.mobileconfig" \
  && bad "描述文件里出现了空地址" || ok "描述文件地址均非空"
# 关掉 DoT 后请求 DoT 必须被拒（409），而不是给一份用不了的文件
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_dot":false}' >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/me/mobileconfig?proto=dot" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "409" ]] && ok "DoT 关闭时拒绝生成 DoT 描述文件（409）" || bad "DoT 关闭仍生成: $code"
# DoH 关闭、不指定 proto 时应回退到 DoT
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_doh":false,"enable_dot":true}' >/dev/null
curl -s "$API/me/mobileconfig" -H "Authorization: Bearer $TOKEN" -o "$RUN/e2e-fallback.mobileconfig"
grep -q '<string>TLS</string>' "$RUN/e2e-fallback.mobileconfig" \
  && ok "DoH 关闭时自动回退 DoT" || bad "没回退到 DoT"
# 两个都关：必须拒绝，不能给空地址文件
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_doh":false,"enable_dot":false,"enable_plain_dns":true}' >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/me/mobileconfig" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "409" ]] && ok "DoH/DoT 全关时拒绝生成（409）" || bad "全关仍生成: $code"
# 恢复
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_doh":true,"enable_dot":true,"enable_plain_dns":true}' >/dev/null
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
# tblocked 断言「被拦截」，不限定拦截形态。
# null_ip 返回 0.0.0.0、nxdomain 返回 NXDOMAIN、refused 返回 REFUSED，
# 三种都是拦截生效；写死 0.0.0.0 会在别的用例改过 blocking_mode 后误报。
tblocked() {
  local desc="$1"; shift 1
  local out
  out=$($PROBE "$@" 2>&1 | tail -1)
  if [[ "$out" == *"0.0.0.0"* || "$out" == *"NXDOMAIN"* || "$out" == *"REFUSED"* ]]; then
    ok "$desc → $out"
  else
    bad "$desc → $out（期望被拦截：0.0.0.0 / NXDOMAIN / REFUSED）"
  fi
}

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
  # 从实际落库的条目里挑一条来验证，而不是写死某个上游域名。
  # 写死的问题：订阅按 max_sub_entries 截断（50000），上游列表顺序一变，
  # 那条域名就可能被截掉，测试随机失败，看起来像拦截坏了。
  PICK=$(python3 - "$RUN/data/aegis.db" "$SUBID" <<'PYEOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
sub = sys.argv[2]
n = 0
for (d,) in db.execute("select domain from sub_entries where sub_id=? order by domain limit 2000", (sub,)):
    d = d.strip().lower().lstrip("*.").lstrip(".")
    if "." in d and not d.startswith("-") and "_" not in d and len(d) < 60:
        print(d)
        n += 1
        if n >= 5:
            break
PYEOF
)
  if [ -z "$PICK" ]; then
    bad "订阅条目为空，无法验证拦截"
  else
    # 多取几条逐个试：单条可能因为订阅按 max_sub_entries 截断、
    # 或者快照还没跟上而查不到，拿一条就判定会让用例随机失败。
    BLOCKED_ONE=""
    for CAND in $PICK; do
      for _ in $(seq 1 30); do
        R=$($PROBE udp 127.0.0.1:15353 "$CAND" 2>/dev/null)
        [[ "$R" == *"0.0.0.0"* || "$R" == *"NXDOMAIN"* ]] && break
        sleep 1
      done
      if [[ "$R" == *"0.0.0.0"* || "$R" == *"NXDOMAIN"* ]]; then
        BLOCKED_ONE="$CAND"
        break
      fi
    done
    if [ -n "$BLOCKED_ONE" ]; then
      ok "订阅拦截生效（取样命中 $BLOCKED_ONE）"
      tblocked "订阅拦截子域继承（x.$BLOCKED_ONE）" udp 127.0.0.1:15353 "x.$BLOCKED_ONE"
    else
      bad "取样的订阅域名一个都没被拦截"
    fi
  fi
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
       "max_id_resets":2,"stats_retention_days":0,"free_max_rules":7,
       "free_max_subs":2,"max_sub_entries":1000,"vip_max_rules":100,"vip_max_subs":5}' >/dev/null
SET_AFTER=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SET_AFTER" | grep -q '"free_max_rules":7' && ok "配额 free_max_rules 已持久化" || bad "配额未持久化"
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
       "max_id_resets":2,"stats_retention_days":0,"free_max_rules":50,
       "free_max_subs":3,"max_sub_entries":50000}' >/dev/null
sleep 4
OUT=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  关: $OUT"
echo "$OUT" | grep -qiE 'REFUSED|拒绝' && ok "关闭时裸域名 → REFUSED" || bad "关闭时裸域名未被拒: $OUT"

# 打开后再查一次：应当正常解析（走全局层 / 上游）
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"domain":"dns.e2e.local","allow_register":true,"enable_query_log":true,
       "allow_root_domain":true,"allow_user_rules":true,"allow_user_subs":true,
       "max_id_resets":2,"stats_retention_days":0,"free_max_rules":50,
       "free_max_subs":3,"max_sub_entries":50000}' >/dev/null
sleep 4
OUT2=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  开: $OUT2"
echo "$OUT2" | grep -qiE 'REFUSED|拒绝' && bad "开启后裸域名仍被拒: $OUT2" || ok "开启后裸域名可解析"

# 子域名不受开关影响。
# 必须从 /me 取当前 ID：前面的「重置接入 ID」「自定义接入 ID」用例已经把它换掉了，
# 用注册时的 $CID 会命中「未知 client_id 直接拒绝」，测出来的根本不是主域名开关。
LIVE_CID=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
OUT3=$($PROBE dot 127.0.0.1:18853 rootcheck.example.com A --sni "$LIVE_CID.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "$OUT3" | grep -qiE 'REFUSED|拒绝' && bad "子域名被误伤: $OUT3" || ok "子域名不受主域名开关影响"

hdr "设置部分更新不得误关开关"
# 这条守着一个大坑：布尔字段曾用值类型接收，导致任何「只改一个字段」的请求
# 都会把没提到的开关按零值写回去 —— 改完 fallback_policy 之后查询日志悄悄停了。
BEFORE=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
QLOG_BEFORE=$(echo "$BEFORE" | grep -o '"enable_query_log":[a-z]*' | cut -d: -f2)
REG_BEFORE=$(echo "$BEFORE" | grep -o '"allow_register":[a-z]*' | cut -d: -f2)
ROOT_BEFORE=$(echo "$BEFORE" | grep -o '"allow_root_domain":[a-z]*' | cut -d: -f2)
# 只传一个字符串字段
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"fallback_policy":"blocklist_only"}' >/dev/null
AFTER=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
QLOG_AFTER=$(echo "$AFTER" | grep -o '"enable_query_log":[a-z]*' | cut -d: -f2)
REG_AFTER=$(echo "$AFTER" | grep -o '"allow_register":[a-z]*' | cut -d: -f2)
ROOT_AFTER=$(echo "$AFTER" | grep -o '"allow_root_domain":[a-z]*' | cut -d: -f2)
[[ "$QLOG_BEFORE" == "$QLOG_AFTER" ]] && ok "部分更新没关掉 enable_query_log（$QLOG_AFTER）" || bad "enable_query_log 被误改：$QLOG_BEFORE -> $QLOG_AFTER"
[[ "$REG_BEFORE" == "$REG_AFTER" ]] && ok "部分更新没关掉 allow_register" || bad "allow_register 被误改：$REG_BEFORE -> $REG_AFTER"
[[ "$ROOT_BEFORE" == "$ROOT_AFTER" ]] && ok "部分更新没关掉 allow_root_domain" || bad "allow_root_domain 被误改：$ROOT_BEFORE -> $ROOT_AFTER"
echo "$AFTER" | grep -q '"fallback_policy":"blocklist_only"' && ok "目标字段确实改到了" || bad "目标字段没改到"
# 显式传 false 仍要生效
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_query_log":false}' >/dev/null
curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -q '"enable_query_log":false' \
  && ok "显式传 false 能关掉（不是忽略）" || bad "显式 false 没生效"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_query_log":true}' >/dev/null

hdr "上游连通性自测"
TU=$(curl -s -X POST "$API/admin/settings/test-upstream" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"upstream":"127.0.0.1:15353"}')
echo "$TU" | grep -q '"ok":true' && ok "能测通本机上游" || bad "自测失败: $(echo "$TU" | head -c 160)"
echo "$TU" | grep -q '"answers"' && ok "返回了解析结果" || bad "没返回 answers"
TU2=$(curl -s -X POST "$API/admin/settings/test-upstream" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"upstream":"127.0.0.1:1"}')
echo "$TU2" | grep -q '"ok":false' && ok "坏上游如实报错（不是静默成功）" || bad "坏上游未报错: $(echo "$TU2" | head -c 160)"
# bootstrap_dns 要能存能读
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"bootstrap_dns":["223.5.5.5","119.29.29.29"]}' >/dev/null
curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -q '"bootstrap_dns":\["223.5.5.5","119.29.29.29"\]' \
  && ok "bootstrap_dns 可持久化" || bad "bootstrap_dns 没存住"

hdr "日志与统计清理"
CL=$(curl -fsS -X POST "$API/admin/maintenance/cleanup" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{}')
echo "$CL" | grep -q '"total"' && ok "手动清理接口可用" || bad "清理接口异常: $CL"
# stats_retention_days=0 时绝不能把统计删光（now-0 会把历史全干掉）
# retention=0 时统计清理任务根本不会加入，key 直接不出现（而不是删了 0 行）
echo "$CL" | grep -q '"query_rollup"' && bad "统计被误删: $CL" || ok "统计保留 0 天时不按时间删（保护生效）"

hdr "查询日志只保留最近 1 小时"
# 塞一条 2 小时前、一条 30 分钟前的日志，清理后只该删掉老的那条
python3 - "$RUN/data/aegis.db" <<'PYEOF'
import sqlite3, sys, time
db = sqlite3.connect(sys.argv[1])
now = int(time.time())
for dom, ts in (("e2e-old-2h.example", now - 7200), ("e2e-fresh-30m.example", now - 1800)):
    db.execute("insert into query_log(user_id, ts, domain, qtype, rcode, action, client, latency_ms) values (?,?,?,?,?,?,?,?)",
               ("", ts, dom, "A", "NOERROR", "allowed", "127.0.0.0/24", 1))
db.commit()
PYEOF
CL2=$(curl -fsS -X POST "$API/admin/maintenance/cleanup" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"query_log_retention_min":60}')
echo "$CL2" | grep -q '"log_retention_min":60' && ok "保留期按分钟生效（60 分钟）" || bad "保留期没生效: $CL2"
LEFT=$(python3 - "$RUN/data/aegis.db" <<'PYEOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
rows = [r[0] for r in db.execute("select domain from query_log where domain like 'e2e-%.example'")]
print(",".join(sorted(rows)))
PYEOF
)
echo "$LEFT" | grep -q 'e2e-old-2h' && bad "2 小时前的日志没被删: $LEFT" || ok "超过 1 小时的日志已删除"
echo "$LEFT" | grep -q 'e2e-fresh-30m' && ok "1 小时内的日志保留" || bad "30 分钟前的日志被误删: $LEFT"
# 设置项也要按分钟回读
SETLOG=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SETLOG" | grep -q '"query_log_retention_min"' && ok "日志保留期按分钟配置" || bad "设置项还是天数: $SETLOG"

hdr "用户自助清除缓存"
MFLUSH=$(curl -s -X POST "$API/me/cache/flush" -H "Authorization: Bearer $TOKEN")
echo "$MFLUSH" | grep -q '"ok":true' && ok "普通用户可清除自己的缓存" || bad "自助清除失败: $MFLUSH"
echo "$MFLUSH" | grep -q '"seq"' && ok "返回用户级清除序号" || bad "没返回序号"
# 序号必须落进快照的该用户条目上，dnsd 靠它定向清
sleep 2
USEQ=$(python3 - "$RUN/data/runtime/config.json" <<'PYEOF'
import json, sys
d = json.load(open(sys.argv[1]))
print(sum(1 for u in d.get("users", []) if u.get("cache_flush_seq")))
PYEOF
)
[ "${USEQ:-0}" -ge 1 ] && ok "用户清除序号已写入快照（$USEQ 个用户带序号）" || bad "快照里没有用户级序号"
# 未登录不能清
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/cache/flush")
[[ "$code" == "401" ]] && ok "未登录不能清除缓存（401）" || bad "未登录也能清: $code"

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

hdr "未知 client_id 直接拒绝"
# 带了 client_id 但查不到对应租户：不能放行。放行等于让陌生人白嫖，
# 而用户侧看到的是「能解析但规则不生效」，比明确拒绝更难排查。
OUT=$($PROBE dot 127.0.0.1:18853 nonexistent.example.com A --sni "zzzzzzzzzz.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  $OUT"
echo "$OUT" | grep -qiE 'REFUSED|拒绝' && ok "不存在的 client_id → REFUSED" || bad "未知 client_id 未被拒: $OUT"

hdr "首页公开统计（免认证）"
PS=$(curl -s "$API/public/stats")
echo "$PS" | grep -q '"available":true' && ok "公开统计可用" || bad "公开统计异常: $(echo "$PS" | head -c 120)"
echo "$PS" | grep -q '"window":"24h"' && ok "窗口固定 24h" || bad "窗口不对"
# 不该泄露任何域名/租户信息
echo "$PS" | grep -qiE '"[a-z0-9-]+\.(com|net|cn)"' && bad "公开统计泄露了域名" || ok "公开统计不含域名细节"

hdr "系统备份"
code=$(curl -s -o /tmp/e2e_backup.tgz -w '%{http_code}' "$API/admin/backup" -H "Authorization: Bearer $ADMTOK")
[[ "$code" == "200" ]] && ok "备份接口返回 200" || bad "备份失败：$code"
# 必须是能解开的 tar.gz，且含数据库与说明
if tar tzf /tmp/e2e_backup.tgz >/tmp/e2e_backup.list 2>/dev/null; then
  ok "备份包可解压"
  grep -q '^aegis.db$' /tmp/e2e_backup.list && ok "含 aegis.db" || bad "备份缺数据库"
  grep -q '^README.txt$' /tmp/e2e_backup.list && ok "含 README" || bad "备份缺说明"
else
  bad "备份包损坏"
fi
# 普通用户不能下备份
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/backup" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "普通用户不能下载备份（403）" || bad "越权下载备份：$code"

hdr "出口代理设置"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"proxy_url":"socks5://127.0.0.1:1080","proxy_enabled":true}' >/dev/null
SET=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SET" | grep -q '"proxy_url":"socks5://127.0.0.1:1080"' && ok "代理地址可持久化" || bad "代理没存住"
echo "$SET" | grep -q '"proxy_enabled":true' && ok "代理开关可持久化" || bad "开关没存住"
# 关掉开关后地址仍在（用户回来不用重填）
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"proxy_enabled":false}' >/dev/null
SET2=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SET2" | grep -q '"proxy_url":"socks5://127.0.0.1:1080"' && ok "关开关不清空地址" || bad "关开关把地址清了"
echo "$SET2" | grep -q '"proxy_enabled":false' && ok "开关能关" || bad "开关关不掉"
# 清理：恢复成不启用
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"proxy_url":"","proxy_enabled":false}' >/dev/null

hdr "查询状态三分类（缓存是标记不是状态）"
# 状态只能是 blocked / allowed / handled；「缓存」用 cache_hit 表达。
# 之前 cached 是独立 action，把「允许项」和「已处理」都挤掉了，
# 用户看不出某条是放行规则放过的还是普通解析。
# example.com 是 e2e 里确认会走缓存的那个域名（前面有专门用例），
# 连查几次必然产生 cache_hit 记录；换别的域名可能因为上游 TTL 为 0 而永远不缓存。
# 走 UDP（来源 IP 已登记到 $TOKEN 这个租户），和前面「缓存命中必须能正常应答」
# 用例同一条路径 —— 那条能缓存，这条才一定拿得到 cache_hit。
for _ in 1 2 3; do
  $PROBE udp 127.0.0.1:15353 example.com >/dev/null 2>&1
  sleep 1
done
$PROBE udp 127.0.0.1:15353 ads.example.com >/dev/null 2>&1
sleep 16
QL2=$(curl -fsS "$API/stats/querylog?page=1&page_size=30" -H "Authorization: Bearer $TOKEN")
python3 - "$QL2" <<'PYEOF'
import json, sys
d = json.loads(sys.argv[1])
rows = d.get("items", [])
fails = 0
def check(cond, ok, bad):
    global fails
    print(("  ✅ " if cond else "  ❌ ") + (ok if cond else bad))
    if not cond:
        fails += 1
acts = {r.get("action") for r in rows}
check(acts <= {"blocked", "allowed", "handled"}, f"状态取值只有三种：{sorted(acts)}",
      f"出现了旧状态值：{sorted(acts - {'blocked','allowed','handled'})}")
check("cached" not in acts, "缓存不再是独立状态", "仍出现 action=cached")
hit = [r for r in rows if r.get("cache_hit")]
check(len(hit) >= 1, f"缓存命中用 cache_hit 标记（{len(hit)} 条）", "没有 cache_hit 标记的记录")
if hit:
    r = hit[0]
    check(r.get("action") in ("handled", "allowed"),
          f"缓存命中状态仍是 {r.get('action')}（显示为「已处理/允许项（缓存）」）",
          f"缓存命中状态异常：{r.get('action')}")
blk = [r for r in rows if r.get("action") == "blocked"]
check(len(blk) >= 1, f"拦截记录状态为 blocked（{len(blk)} 条）", "没有 blocked 记录")
if blk:
    check(not blk[0].get("cache_hit"), "拦截记录不带缓存标记（拦截不缓存）", "拦截记录被标成缓存命中")
sys.exit(1 if fails else 0)
PYEOF
[ $? -eq 0 ] && ok "查询状态三分类断言全部通过" || bad "查询状态三分类断言失败"

hdr "查询日志详情字段"
# 详情弹窗要展示协议/ECS/上游/应答/追踪器 —— 这些只在解析时能采集，
# 事后补不回来。老记录允许为空，但新记录必须有。
# 重新取一次 ID：前面的「自定义接入 ID」用例又换过一次，
# 用旧值会命中「未知 client_id 直接拒绝」，日志记在空用户下，
# 自然查不到 —— 测出来的不是详情字段有没有落库。
DET_CID=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  当前接入 ID: $DET_CID"
echo "  探测 doubleclick.net: $($PROBE dot 127.0.0.1:18853 doubleclick.net A --sni "$DET_CID.dns.e2e.local" --insecure 2>&1 | tail -1)"
echo "  探测 detail-probe:    $($PROBE dot 127.0.0.1:18853 detail-probe.example.com A --sni "$DET_CID.dns.e2e.local" --insecure 2>&1 | tail -1)"
sleep 16   # 等统计落库（间隔 10s，留一点余量）
QL=$(curl -fsS "$API/stats/querylog?page=1&page_size=20" -H "Authorization: Bearer $TOKEN")
# 用 python 解析而不是 grep：JSON 里 "proto": "dot" 带空格，
# 写死 '"proto":"dot"' 会永远匹配不上，测出来的失败是假的。
python3 - "$QL" <<'PYEOF'
import json, sys
d = json.loads(sys.argv[1])
rows = {it.get("domain"): it for it in d.get("items", [])}

def check(cond, okmsg, badmsg):
    # 返回失败计数：成功返回 0，失败返回 1。
    # 之前这里 return cond，于是「全过」也会把 fails 累加成正数，退出码反了。
    print(("  \u2705 " if cond else "  \u274c ") + (okmsg if cond else badmsg))
    return 0 if cond else 1

fails = 0
probe = rows.get("detail-probe.example.com")
if probe is None:
    fails += check(False, "", "查不到刚解析的日志（统计可能没落库）")
else:
    print("  \u2705 新日志可查到")
    fails += check(probe.get("proto") == "dot", "记录了接入协议（dot）", "proto 不对: %r" % probe.get("proto"))
    fails += check(bool(probe.get("upstream")), "记录了应答上游", "upstream 缺失")

# answer 要用一个真有应答记录的域名验：detail-probe 本身无应答，
# 拿它断言 answer 非空是测错了对象。
trk = rows.get("doubleclick.net")
if trk is None:
    fails += check(False, "", "查不到 doubleclick.net 的日志")
else:
    fails += check(bool(trk.get("answer")), "记录了应答内容", "answer 缺失")
    fails += check(trk.get("tracker") == "doubleclick.net", "识别出已知追踪器", "追踪器未识别: %r" % trk.get("tracker"))

sys.exit(1 if fails else 0)
PYEOF
if [[ $? -eq 0 ]]; then ok "日志详情字段齐全"; else bad "日志详情字段有缺失"; fi

hdr "会员档位与两档配额"
# 新注册用户必须是 free 档
MEM=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN")
echo "$MEM" | grep -q '"tier":"free"' && ok "新用户默认 free 档" || bad "默认档位不对: $(echo "$MEM" | head -c 120)"
FREE_RULES=$(echo "$MEM" | python3 -c "import json,sys; print(json.load(sys.stdin)['quota']['max_rules'])")
# 管理员给 +30 天
MUID=$(echo "$MEM" >/dev/null; curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
SETM=$(curl -s -X POST "$API/admin/users/$MUID/member" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"days":30}')
echo "$SETM" | grep -q '"tier":"vip"' && ok "加 30 天后变 vip" || bad "加会员失败: $SETM"
echo "$SETM" | grep -q '"member_days":30' && ok "剩余天数正确（30）" || bad "天数不对: $SETM"
# 快照要跟着更新，配额读出来应是 vip 档
sleep 3
MEM2=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN")
VIP_RULES=$(echo "$MEM2" | python3 -c "import json,sys; print(json.load(sys.stdin)['quota']['max_rules'])")
echo "$MEM2" | grep -q '"tier":"vip"' && ok "配额按 vip 档算" || bad "档位没切换"
[[ "$VIP_RULES" -gt "$FREE_RULES" ]] && ok "会员规则上限更大（$FREE_RULES → $VIP_RULES）" \
  || bad "会员配额没变大：$FREE_RULES → $VIP_RULES"
# 再 +7 天应叠加而不是覆盖
SETM2=$(curl -s -X POST "$API/admin/users/$MUID/member" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"days":7}')
echo "$SETM2" | grep -q '"member_days":37' && ok "连续加会员天数会叠加（37）" || bad "天数被覆盖: $SETM2"
# 取消
curl -fsS -X POST "$API/admin/users/$MUID/member/revoke" -H "Authorization: Bearer $ADMTOK" >/dev/null
sleep 2
MEM3=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN")
echo "$MEM3" | grep -q '"tier":"free"' && ok "取消会员后立刻降级" || bad "取消没生效"

hdr "邀请码与拉新奖励"
NEWCODE=$(curl -s -X POST "$API/admin/invite-codes" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"note":"e2e","reward_days":10,"max_uses":1}' | grep -o '"code":"[^"]*"' | cut -d'"' -f4)
[[ -n "$NEWCODE" ]] && ok "邀请码已创建（$NEWCODE）" || bad "创建邀请码失败"
# 关掉注册限制，用码注册一个新用户
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"allow_register":true,"invite_code":""}' >/dev/null
INV=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"invited$$\",\"email\":\"invited$$@e2e.local\",\"password\":\"Passw0rd!23\",\"invite_code\":\"$NEWCODE\"}")
echo "$INV" | grep -q '"access_token"' && ok "带邀请码可注册" || bad "注册失败: $(echo "$INV" | head -c 140)"
# 邀请人（管理员）应拿到 10 天
sleep 2
ADMMEM=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $ADMTOK")
ADMDAYS=$(echo "$ADMMEM" | python3 -c "import json,sys; print(json.load(sys.stdin)['member_days'])")
[[ "$ADMDAYS" -ge 10 ]] && ok "邀请人获得 10 天奖励（当前 $ADMDAYS 天）" || bad "奖励没发: $ADMDAYS"
# 次数用完后再用应被拒
INV2=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"invited2$$\",\"email\":\"invited2$$@e2e.local\",\"password\":\"Passw0rd!23\",\"invite_code\":\"$NEWCODE\"}")
echo "$INV2" | grep -qiE '次数已用完|不存在' && ok "次数用完的码被拒" || bad "超次数仍可用: $(echo "$INV2" | head -c 120)"

hdr "普通用户自建邀请码"
# 拉新激励的关键：用户自己能生成码和链接。只有管理员能发码的话，
# 这套东西就退化成「管理员发福利」，用户没有动力分享。
MYIC=$(curl -s -X POST "$API/me/invite-codes" -H "Authorization: Bearer $TOKEN")
UCODE=$(echo "$MYIC" | grep -o '"code":"[^"]*"' | head -1 | cut -d'"' -f4)
[[ -n "$UCODE" ]] && ok "普通用户能生成邀请码（$UCODE）" || bad "生成失败: $(echo "$MYIC" | head -c 140)"
echo "$MYIC" | grep -q '"link":"https://[^"]*/register?invite=' \
  && ok "返回了可直接分享的邀请链接" || bad "缺邀请链接"
GLOBAL_REWARD=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" \
  | grep -o '"member_reward_days":[0-9]*' | cut -d: -f2)
echo "$MYIC" | grep -q "\"reward_days\":$GLOBAL_REWARD" \
  && ok "奖励天数取全局值（$GLOBAL_REWARD）" || bad "奖励天数不对（期望 $GLOBAL_REWARD）: $(echo "$MYIC" | grep -o '\"reward_days\":[0-9]*')"
# 列表里能看到自己的码
curl -fsS "$API/me/invite-codes" -H "Authorization: Bearer $TOKEN" | grep -q "$UCODE" \
  && ok "自己的码出现在列表里" || bad "列表里没有自己的码"
# 用用户的码注册，奖励要给到该用户
REC_BEFORE=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['member_days'])")
curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"viacode$$\",\"email\":\"viacode$$@e2e.local\",\"password\":\"Passw0rd!23\",\"invite_code\":\"$UCODE\"}" \
  | grep -q '"access_token"' && ok "用用户的码能注册" || bad "用用户码注册失败"
sleep 2
REC_AFTER=$(curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['member_days'])")
[[ "$REC_AFTER" -gt "$REC_BEFORE" ]] && ok "邀请人会员天数增加（$REC_BEFORE → $REC_AFTER）" \
  || bad "奖励没给到邀请人：$REC_BEFORE → $REC_AFTER"
# 别人的码不能删
ADMCODE=$(curl -s -X POST "$API/admin/invite-codes" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"note":"admin-only"}' | grep -o '"code":"[^"]*"' | cut -d'"' -f4)
code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/me/invite-codes/$ADMCODE" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "不能删别人的邀请码（403）" || bad "越权删除：$code"
# 能删自己的
code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/me/invite-codes/$UCODE" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "204" ]] && ok "能删自己的邀请码" || bad "删自己的失败：$code"
curl -fsS -X DELETE "$API/admin/invite-codes/$ADMCODE" -H "Authorization: Bearer $ADMTOK" >/dev/null
# 关掉开关后不能再建
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"allow_user_invite":false}' >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/invite-codes" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "关闭后用户不能自建（403）" || bad "关闭后仍可建：$code"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"allow_user_invite":true}' >/dev/null

hdr "公告：静默 / 弹窗 / 已读"
ANN=$(curl -s -X POST "$API/admin/announcements" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"title":"e2e 静默公告","body":"只进公告栏","level":"info","mode":"silent"}')
AID=$(echo "$ANN" | grep -o '"id":[0-9]*' | cut -d: -f2)
[[ -n "$AID" ]] && ok "静默公告已发布（id=$AID）" || bad "发布失败: $ANN"
MY=$(curl -fsS "$API/me/announcements" -H "Authorization: Bearer $TOKEN")
echo "$MY" | grep -q "e2e 静默公告" && ok "用户能看到公告" || bad "用户看不到公告"
echo "$MY" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sys.exit(0 if d['unread']>=1 else 1)" && ok "未读数 ≥ 1（红点来源）" || bad "未读数不对"
curl -fsS -X POST "$API/me/announcements/$AID/read" -H "Authorization: Bearer $TOKEN" >/dev/null
MY2=$(curl -fsS "$API/me/announcements" -H "Authorization: Bearer $TOKEN")
echo "$MY2" | python3 -c "
import json,sys
d=json.load(sys.stdin)
me=[a for a in d['items'] if a['id']==$AID][0]
sys.exit(0 if me['read_at']>0 else 1)" && ok "标记已读生效" || bad "已读没记上"
# 弹窗公告 + 不再弹出
ANN2=$(curl -s -X POST "$API/admin/announcements" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"title":"e2e 弹窗","body":"会弹","mode":"popup","dismissible":true}')
AID2=$(echo "$ANN2" | grep -o '"id":[0-9]*' | cut -d: -f2)
curl -fsS -X POST "$API/me/announcements/$AID2/dismiss" -H "Authorization: Bearer $TOKEN" >/dev/null
curl -fsS "$API/me/announcements" -H "Authorization: Bearer $TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
me=[a for a in d['items'] if a['id']==$AID2][0]
# 不再弹出必须隐含已读，否则公告栏会一直挂着红点
sys.exit(0 if me['dismissed_at']>0 and me['read_at']>0 else 1)" \
  && ok "不再弹出同时记为已读" || bad "dismiss 没隐含已读"
# 清理
curl -fsS -X DELETE "$API/admin/announcements/$AID" -H "Authorization: Bearer $ADMTOK" >/dev/null
curl -fsS -X DELETE "$API/admin/announcements/$AID2" -H "Authorization: Bearer $ADMTOK" >/dev/null

hdr "邮件服务（未启用时如实报错）"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"smtp_enabled":false,"email_verify_register":false}' >/dev/null
EC=$(curl -s -X POST "$API/auth/email/send-code" -H 'Content-Type: application/json' \
  -d '{"email":"nobody@e2e.local","purpose":"register"}')
echo "$EC" | grep -q 'FEATURE_DISABLED' && ok "未启用邮件时发码被拒（FEATURE_DISABLED）" || bad "未启用仍尝试发信: $EC"
# 开着注册验证但没配邮件，必须存不进去
BAD=$(curl -s -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"email_verify_register":true}')
echo "$BAD" | grep -qi '邮件' && ok "未配邮件时不允许开启注册验证" || bad "错误配置被接受: $BAD"
# 密码永远不回传
curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -q '"smtp_pass":"' \
  && bad "settings 回传了 SMTP 明文密码" || ok "SMTP 密码不回传"
curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -q '"smtp_pass_set"' \
  && ok "只暴露是否已设置" || bad "缺 smtp_pass_set"

hdr "全局层不再有配额上限"
GQ=$(curl -fsS "$API/admin/rules?page=1&page_size=1" -H "Authorization: Bearer $ADMTOK")
echo "$GQ" | grep -q '"limit":0' && ok "全局规则 limit=0（不限）" || bad "全局规则仍有上限: $(echo "$GQ" | grep -o '\"limit\":[0-9]*')"
GS=$(curl -fsS "$API/admin/subscriptions" -H "Authorization: Bearer $ADMTOK")
echo "$GS" | grep -q '"limit":0' && ok "全局订阅 limit=0（不限）" || bad "全局订阅仍有上限"

hdr "每日请求量上限（会员档）"
# 先把会员撤掉：前面的邀请奖励让这个用户变成了会员，
# 而会员档的每日请求量是不限的 —— 不撤销就永远测不出免费档的上限。
Q_UID=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
curl -fsS -X POST "$API/admin/users/$Q_UID/member/revoke" -H "Authorization: Bearer $ADMTOK" >/dev/null
sleep 2
curl -fsS "$API/me/membership" -H "Authorization: Bearer $TOKEN" | grep -q '"tier":"free"' \
  && ok "已降为免费档，可测请求量上限" || bad "撤销会员失败，测不了免费档上限"

# 把免费档压到极小，验证超额被 REFUSED。
# 注意不能只 sleep 几秒：dnsd 的兜底轮询是 30s（fsnotify 正常情况下会更快），
# 固定 sleep 会变成「有时过有时不过」的假失败。这里改成边探边等，
# 一旦出现 REFUSED 就算生效，超时才判失败。
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"free_daily_queries":3}' >/dev/null
QUOTA_CID=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  测试接入 ID: $QUOTA_CID"
GOT_REFUSED=0
LAST=""
for i in $(seq 1 45); do
  LAST=$($PROBE dot 127.0.0.1:18853 "quota$i.example.com" A --sni "$QUOTA_CID.dns.e2e.local" --insecure 2>&1 | tail -1)
  if echo "$LAST" | grep -qiE 'REFUSED|拒绝'; then GOT_REFUSED=1; echo "  第 $i 次查询被拒"; break; fi
  sleep 1
done
[[ "$GOT_REFUSED" == "1" ]] && ok "超过每日上限后返回 REFUSED" || bad "45 秒内未被拦截，最后一次: $LAST"
# 恢复
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"free_daily_queries":50000}' >/dev/null

hdr "注册赠送会员天数"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"signup_member_days":5,"allow_register":true,"invite_code":""}' >/dev/null
GIFT=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"gift$$\",\"email\":\"gift$$@e2e.local\",\"password\":\"Passw0rd!23\"}")
GTOK=$(echo "$GIFT" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
GDAYS=$(echo "$GIFT" | python3 -c "import json,sys; print(json.load(sys.stdin)['user']['member_days'])" 2>/dev/null)
[[ "${GDAYS:-0}" == "5" ]] && ok "注册即送 5 天会员" || bad "注册赠送没生效: member_days=$GDAYS"
echo "$GIFT" | grep -q '"tier":"vip"' && ok "赠送后档位是 vip" || bad "档位没跟着变"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"signup_member_days":0}' >/dev/null

hdr "每日签到"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"checkin_enabled":true,"checkin_days":2,"checkin_bonus_every":3,"checkin_bonus_days":5}' >/dev/null
CK0=$(curl -fsS "$API/me/checkin" -H "Authorization: Bearer $GTOK")
echo "$CK0" | grep -q '"checked_today":false' && ok "签到状态初始为未签到" || bad "初始状态不对: $CK0"
CK1=$(curl -s -X POST "$API/me/checkin" -H "Authorization: Bearer $GTOK")
echo "$CK1" | grep -q '"granted":2' && ok "首次签到得 2 天" || bad "签到没发天数: $CK1"
echo "$CK1" | grep -q '"streak":1' && ok "连续天数从 1 开始" || bad "连签计数不对"
# 累计口径：累计天数要单独统计，奖励按累计算而不是连续
echo "$CK1" | grep -q '"total_days":1' && ok "累计签到天数为 1" || bad "累计天数不对: $CK1"
# 累计门槛未到（1/3）不该发奖励
echo "$CK1" | grep -q '"bonus":0' && ok "未到累计门槛不发奖励" || bad "不该有奖励: $CK1"
# 设置项按新键名回读
SETCK=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SETCK" | grep -q '"checkin_bonus_every":3' && ok "累计门槛设置已生效（新键名）" || bad "累计门槛没生效: $SETCK"
echo "$SETCK" | grep -q '"checkin_bonus_days":5' && ok "累计奖励天数已生效" || bad "累计奖励没生效"
# 老键名仍然兼容（老脚本/回滚场景）
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"checkin_streak_days":4,"checkin_streak_bonus":6}' >/dev/null
SETCK2=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$SETCK2" | grep -q '"checkin_bonus_every":4' && ok "老键名 checkin_streak_days 仍能生效" || bad "老键名失效: $SETCK2"
# 重复签到必须被拒
code=$(curl -s -o /tmp/e2e_ck.json -w '%{http_code}' -X POST "$API/me/checkin" -H "Authorization: Bearer $GTOK")
[[ "$code" == "409" ]] && ok "同一天重复签到被拒（409）" || bad "重复签到没拦住: $code"
grep -qi '已经签到' /tmp/e2e_ck.json && ok "重复签到提示明确" || bad "提示不对"
curl -fsS "$API/me/checkin" -H "Authorization: Bearer $GTOK" | grep -q '"checked_today":true' \
  && ok "签到状态已落库" || bad "状态没更新"
# 关掉签到后不能签
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"checkin_enabled":false}' >/dev/null
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/checkin" -H "Authorization: Bearer $GTOK")
[[ "$code" == "403" ]] && ok "关闭签到后不能签（403）" || bad "关闭后仍可签: $code"

hdr "邮箱后缀白名单"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"email_domain_whitelist":"qq.com, 163.com"}' >/dev/null
SETWL=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" | grep -o '"email_domain_whitelist":"[^"]*"')
echo "$SETWL" | grep -q 'qq.com,163.com' && ok "白名单已规范化（去空格/小写/去重）" || bad "白名单没规范化: $SETWL"
WL=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"wl$$\",\"email\":\"wl$$@tempmail.io\",\"password\":\"Passw0rd!23\"}")
echo "$WL" | grep -qi '后缀不在允许范围' && ok "白名单外的邮箱被拒" || bad "白名单没生效: $(echo "$WL" | head -c 120)"
WL2=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"wl2$$\",\"email\":\"wl2$$@qq.com\",\"password\":\"Passw0rd!23\"}")
echo "$WL2" | grep -q '"access_token"' && ok "白名单内的邮箱可注册" || bad "白名单内被误拒: $(echo "$WL2" | head -c 120)"

# 注册页要把白名单下发给前端，否则后缀下拉框无从渲染
PDOMAINS=$(curl -fsS "$API/public/config" | grep -o '"email_domains":\[[^]]*\]')
echo "$PDOMAINS" | grep -q 'qq.com' && ok "公开配置下发邮箱后缀白名单" || bad "没下发后缀: $PDOMAINS"
echo "$PDOMAINS" | grep -q '163.com' && ok "后缀列表包含全部配置项" || bad "后缀不全: $PDOMAINS"

hdr "接入协议开关"
# 关掉 DoT：用户端不该再拿到 DoT 地址，Android 指引应标「暂不支持」
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_dot":false}' >/dev/null
CFG=$(curl -fsS "$API/me/dns-config" -H "Authorization: Bearer $TOKEN")
echo "$CFG" | grep -q '"enable_dot":false' && ok "DoT 开关状态已下发" || bad "开关没下发: $(echo "$CFG" | head -c 120)"
echo "$CFG" | grep -q '"dot_host":""' && ok "关闭后不下发 DoT 主机名" || bad "DoT 主机名仍在下发"
echo "$CFG" | grep -q '"dot_port":0' && ok "关闭后 DoT 端口归零" || bad "DoT 端口仍在"
echo "$CFG" | grep -q '"platform":"android","supported":false' && ok "Android 指引标记为暂不支持" || bad "Android 没标记: $(echo "$CFG" | grep -o '"platform":"android"[^}]*')"
echo "$CFG" | grep -q '"platform":"ios","supported":true' && ok "iOS 指引仍可用" || bad "iOS 被误标不可用"
# 三个全关必须被拒
code=$(curl -s -o /tmp/e2e_proto.json -w '%{http_code}' -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"enable_doh":false,"enable_dot":false,"enable_plain_dns":false}')
[[ "$code" == "422" ]] && ok "三个协议全关被拒（422）" || bad "全关没拦住: $code"
grep -qi '至少要保留一个' /tmp/e2e_proto.json && ok "全关提示明确" || bad "提示不对"
# 恢复
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_doh":true,"enable_dot":true,"enable_plain_dns":true}' >/dev/null

hdr "无自有规则的用户也必须受全局层拦截"
# 回归：GlobalLayer 抽成共享后，BuildWithGlobal 在 Build 之后才挂上共享层，
# Stats 里的全局计数恒为 0 → rs.Empty() 为真 → handler 跳过整个规则匹配。
# 表现是「只有全局层」的租户拦截率恒为 0，而自带订阅的租户一切正常。
GLOBAL_ONLY_USER="gonly$$"
curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$GLOBAL_ONLY_USER\",\"email\":\"gonly@qq.com\",\"password\":\"Str0ngPass123\"}" -o /tmp/e2e_gonly.json >/dev/null
GOTOK=$(grep -o '"access_token":"[^"]*"' /tmp/e2e_gonly.json | cut -d'"' -f4)
GOCID=$(grep -o '"client_id":"[^"]*"' /tmp/e2e_gonly.json | head -1 | cut -d'"' -f4)
if [ -n "$GOTOK" ]; then
  ok "新建无规则用户（client_id=$GOCID）"
  GRESP=$(curl -s -X POST "$API/rules" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
    -d '{"pattern":"||global-only-probe.test^","kind":"block","is_global":true,"enabled":true}')
  GRID=$(echo "$GRESP" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
  # 拦截应答可能是 0.0.0.0（null_ip）也可能是 NXDOMAIN，
  # 两种都算生效 —— 只认 0.0.0.0 会在 blocking_mode 被前面的用例改掉时误报。
  R=""
  for _ in $(seq 1 40); do
    RAW=$(timeout 4 dig @127.0.0.1 -p 15353 "x.global-only-probe.test" A 2>/dev/null)
    if echo "$RAW" | grep -q 'status: NXDOMAIN' || echo "$RAW" | grep -q '0\.0\.0\.0'; then
      R=$(echo "$RAW" | grep -q 'status: NXDOMAIN' && echo NXDOMAIN || echo 0.0.0.0)
      break
    fi
    sleep 1
  done
  [ -n "$R" ] && ok "无自有规则的用户被全局层拦截（$R）" || bad "全局层对该用户没生效（既非 NXDOMAIN 也非 0.0.0.0）"
  [ -n "$GRID" ] && curl -s -X DELETE "$API/rules/$GRID" -H "Authorization: Bearer $ADMTOK" >/dev/null 2>&1
else
  bad "无规则用户注册失败"
fi
rm -f /tmp/e2e_gonly.json

hdr "清除缓存"
FLUSH=$(curl -s -X POST "$API/admin/cache/flush" -H "Authorization: Bearer $ADMTOK")
echo "$FLUSH" | grep -q '"ok":true' && ok "清除缓存接口可用" || bad "清除失败: $FLUSH"
echo "$FLUSH" | grep -q '"seq"' && ok "返回清除序号" || bad "没返回序号"
# 序号必须落进快照，dnsd 靠它判断要不要清
sleep 1
FSEQ=$(grep -o '"cache_flush_seq":[0-9]*' "$RUN/data/runtime/config.json" | head -1)
[ -n "$FSEQ" ] && ok "清除序号已写入快照（$FSEQ）" || bad "快照里没有清除序号"
# 普通用户不该能清全站缓存
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/admin/cache/flush" -H "Authorization: Bearer $TOKEN")
[[ "$code" == "403" ]] && ok "普通用户无法清除缓存（403）" || bad "普通用户能清缓存: $code"

hdr "管理员不收公告"
# 管理员不参与普通用户功能，公告接口对他没意义（前端也不再渲染铃铛）
ACOUNT=$(curl -fsS "$API/me/announcements" -H "Authorization: Bearer $ADMTOK" | grep -o '"unread":[0-9]*' | head -1)
[ -n "$ACOUNT" ] && ok "公告接口对管理员仍可用（前端隐藏）" || bad "公告接口异常"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"email_domain_whitelist":""}' >/dev/null

hdr "自助更换邮箱（未配邮件时如实拒绝）"
code=$(curl -s -o /tmp/e2e_em.json -w '%{http_code}' -X POST "$API/auth/email/send-code" \
  -H "Authorization: Bearer $GTOK" -H 'Content-Type: application/json' \
  -d '{"email":"new@e2e.local","purpose":"change"}')
[[ "$code" == "503" ]] && ok "未启用邮件时换邮箱被拒（503）" || bad "未启用仍发信: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/me/email" \
  -H "Authorization: Bearer $GTOK" -H 'Content-Type: application/json' \
  -d '{"email":"new@e2e.local","code":"000000"}')
[[ "$code" == "400" ]] && ok "没有验证码时换邮箱被拒" || bad "无码也能换: $code"

hdr "DNS 设置：EDNS 与 DNSSEC 开关"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_ecs":true,"enable_dnssec":true}' >/dev/null
DS=$(curl -fsS "$API/admin/settings" -H "Authorization: Bearer $ADMTOK")
echo "$DS" | grep -q '"enable_ecs":true' && ok "ECS 可持久化" || bad "ECS 没存住"
echo "$DS" | grep -q '"enable_dnssec":true' && ok "DNSSEC 可持久化" || bad "DNSSEC 没存住"
# 开启后解析必须仍然正常（改报文不能把查询搞坏）
sleep 6
DNCID=$(curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" | grep -o '"client_id":"[^"]*"' | head -1 | cut -d'"' -f4)
EDNSOUT=$($PROBE dot 127.0.0.1:18853 www.baidu.com A --sni "$DNCID.dns.e2e.local" --insecure 2>&1 | tail -1)
echo "  $EDNSOUT"
echo "$EDNSOUT" | grep -qiE 'NOERROR|OK' && ok "开启 ECS+DNSSEC 后仍能正常解析" || bad "改报文把查询搞坏了: $EDNSOUT"
curl -fsS -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADMTOK" -H 'Content-Type: application/json' \
  -d '{"enable_ecs":false,"enable_dnssec":false}' >/dev/null

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

hdr "用户详情与配额覆盖"
# 详情接口：会员状态、生效配额、档位默认、使用量都得有
U1=$(curl -fsS "$API/admin/users?page=1&page_size=50" -H "Authorization: Bearer $ADMTOK" \
  | python3 -c "import json,sys;print(next(u['id'] for u in json.load(sys.stdin)['items'] if u['username']=='e2etest'))")
DET=$(curl -fsS "$API/admin/users/$U1" -H "Authorization: Bearer $ADMTOK")
for k in is_member member_days tier quota quota_default custom usage today_queries; do
  if echo "$DET" | grep -q "\"$k\""; then ok "详情含 $k"; else bad "详情缺 $k"; fi
done

# 手填覆盖：规则 777、订阅 -1（不限）、请求 123456
curl -fsS -X PUT "$API/admin/users/$U1/quota" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' \
  -d '{"max_rules":777,"max_subs":-1,"daily_queries":123456}' >/dev/null
Q=$(curl -fsS "$API/admin/users/$U1" -H "Authorization: Bearer $ADMTOK" \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['quota']['max_rules'],d['quota']['max_subs'],d['quota']['daily_queries'])")
set -- $Q
[[ "$1" == "777" ]] && ok "配额覆盖生效（规则 777）" || bad "规则覆盖失败：$1"
# -1 表示不限，而 Quota 里「不限」就是 0，所以生效值必须是 0，不能是 -1
[[ "$2" == "0" ]] && ok "订阅 -1 归一成不限（0）" || bad "订阅 -1 没归一：$2"
[[ "$3" == "123456" ]] && ok "请求量覆盖生效" || bad "请求量覆盖失败：$3"

# 恢复默认：0 = 跟随档位
curl -fsS -X PUT "$API/admin/users/$U1/quota" -H "Authorization: Bearer $ADMTOK" \
  -H 'Content-Type: application/json' -d '{"max_rules":0,"max_subs":0,"daily_queries":0}' >/dev/null
C=$(curl -fsS "$API/admin/users/$U1" -H "Authorization: Bearer $ADMTOK" \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['custom']['max_rules'],d['custom']['max_subs'],d['custom']['daily_queries'])")
[[ "$C" == "0 0 0" ]] && ok "恢复默认后不再覆盖" || bad "恢复默认失败：$C"

hdr "用户清空日志不影响管理员"
total_of() { python3 -c "import json,sys;print(json.load(sys.stdin).get('total',0))"; }
$PROBE udp 127.0.0.1:15353 clear-probe.example.com >/dev/null 2>&1
sleep 2
UBEFORE=$(curl -fsS "$API/stats/querylog?page=1&page_size=5" -H "Authorization: Bearer $TOKEN" | total_of)
ABEFORE=$(curl -fsS "$API/admin/stats/querylog?page=1&page_size=5" -H "Authorization: Bearer $ADMTOK" | total_of)
[[ "${UBEFORE:-0}" -gt 0 ]] && ok "清空前用户能看到日志（$UBEFORE 条）" || bad "清空前用户日志为空"
[[ "${ABEFORE:-0}" -gt 0 ]] && ok "清空前管理员能看到日志（$ABEFORE 条）" || bad "清空前管理员日志为空"

curl -fsS -X POST "$API/logs/clear" -H "Authorization: Bearer $TOKEN" >/dev/null
sleep 1
UAFTER=$(curl -fsS "$API/stats/querylog?page=1&page_size=5" -H "Authorization: Bearer $TOKEN" | total_of)
AAFTER=$(curl -fsS "$API/admin/stats/querylog?page=1&page_size=5" -H "Authorization: Bearer $ADMTOK" | total_of)
[[ "${UAFTER:-0}" -eq 0 ]] && ok "用户清空后自己看不到旧日志" || bad "用户清空后仍看到 $UAFTER 条"
# 这条是需求要害：用户清的是自己的视图，不能把管理员的审计记录一起清掉
[[ "${AAFTER:-0}" -gt 0 ]] && ok "管理员仍能看到全部日志（$AAFTER 条）" || bad "管理员日志被误清（$AAFTER 条）"

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
