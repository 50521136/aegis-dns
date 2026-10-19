#!/usr/bin/env bash
# 验证全局规则层、配额、证书、反馈的端到端行为
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
export GOPROXY=https://goproxy.cn,direct

SRC=/root/aegis-dns/server
RUN=/root/aegis-dns/.verify-global
API_PORT=18095
DNS_PORT=15365
DOT_PORT=18865
DOH_PORT=14465
DOH_PLAIN=18065
API="http://127.0.0.1:$API_PORT/api/v1"
PROBE="python3 $SRC/../deploy/dnsprobe.py"

PASS=0; FAIL=0
ok()   { echo "  ✅ $*"; PASS=$((PASS+1)); }
bad()  { echo "  ❌ $*"; FAIL=$((FAIL+1)); }
hdr()  { echo; echo "── $* ──"; }

rm -rf "$RUN"; mkdir -p "$RUN/data/runtime" "$RUN/data/certs"
cd "$SRC" || exit 1
GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$RUN/aegis-dnsd" ./cmd/dnsd || exit 1
GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$RUN/aegis-apid" ./cmd/apid || exit 1

JWT=$("$RUN/aegis-apid" -gen-secret)
cat > "$RUN/.env" <<EOF
JWT_SECRET=$JWT
ADMIN_PASSWORD=VerifyPass123
EOF

cat > "$RUN/config.yaml" <<EOF
domain: "dns.vg.local"
data_dir: "$RUN/data"
dns:
  listen_udp: "127.0.0.1:$DNS_PORT"
  listen_tcp: "127.0.0.1:$DNS_PORT"
  listen_dot: "127.0.0.1:$DOT_PORT"
  listen_doh: "127.0.0.1:$DOH_PORT"
  listen_doh_plain: "127.0.0.1:$DOH_PLAIN"
  dev_tls: true
  query_log_ring: 500
  stats_flush_seconds: 3
api:
  listen: "127.0.0.1:$API_PORT"
  tls_listen: ""
  jwt_secret: "\${JWT_SECRET}"
  admin:
    username: "admin"
    password: "\${ADMIN_PASSWORD}"
upstreams: ["223.5.5.5", "119.29.29.29"]
update:
  repo: "50521136/aegis-dns"
  channel: "stable"
log:
  level: "warn"
EOF

set -a; source "$RUN/.env"; set +a
"$RUN/aegis-dnsd" -config "$RUN/config.yaml" > "$RUN/dnsd.log" 2>&1 &
DNSD=$!
"$RUN/aegis-apid" -config "$RUN/config.yaml" > "$RUN/apid.log" 2>&1 &
APID=$!
trap 'kill $DNSD $APID 2>/dev/null; wait 2>/dev/null' EXIT

for _ in $(seq 1 40); do curl -fsS -m 2 "$API/healthz" >/dev/null 2>&1 && break; sleep 0.5; done

login() { curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["access_token"])'; }

ATOK=$(login admin VerifyPass123)
ACID=$(curl -s "$API/me" -H "Authorization: Bearer $ATOK" | python3 -c 'import sys,json;print(json.load(sys.stdin)["client_id"])')

hdr "管理员创建全局规则（拦截 gblock.test）"
CODE=$(curl -s -o /tmp/g.out -w '%{http_code}' -X POST "$API/admin/rules" \
  -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"kind":"block","pattern":"||gblock.test^","value":"","qtypes":[],"enabled":true}')
[[ "$CODE" == "201" ]] && ok "全局规则已创建" || bad "全局规则创建失败 $CODE $(cat /tmp/g.out)"
grep -q '"is_global":true' /tmp/g.out && ok "返回体标记 is_global=true" || bad "返回体缺少 is_global"

hdr "管理员创建全局订阅"
CODE=$(curl -s -o /tmp/gs.out -w '%{http_code}' -X POST "$API/admin/subscriptions" \
  -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"name":"vg-global","url":"https://example.com/never.txt","list_type":"blocklist","format":"auto","enabled":false}')
[[ "$CODE" == "201" ]] && ok "全局订阅已创建" || bad "全局订阅创建失败 $CODE $(cat /tmp/gs.out)"

hdr "普通用户看不到管理员的全局规则"
REG=$(curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"vguser","email":"vg@test.local","password":"Str0ngPass123"}')
UTOK=$(echo "$REG" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["access_token"])')
UCID=$(echo "$REG" | python3 -c 'import sys,json;print(json.load(sys.stdin)["user"]["client_id"])')
UTOT=$(curl -s "$API/rules" -H "Authorization: Bearer $UTOK" | python3 -c 'import sys,json;print(json.load(sys.stdin)["total"])')
[[ "${UTOT:-1}" == "0" ]] && ok "普通用户的个人规则列表为空（全局规则不混入）" || bad "个人规则列表混入了全局规则 total=$UTOT"

hdr "普通用户直接访问 /admin/rules 应被拒绝"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/rules" -H "Authorization: Bearer $UTOK")
[[ "$CODE" == "403" ]] && ok "普通用户访问全局规则 403" || bad "普通用户访问全局规则返回 $CODE（期望 403）"

# 探针可能自己报错（如 SNI 非法），那种情况绝不能算「没被拦」，
# 所以统一用 probe_blocked / probe_allowed 判断，先排除探针自身的 FAIL。
probe() {
  $PROBE dot 127.0.0.1:$DOT_PORT "$1" A --sni "$UCID.dns.vg.local" --insecure 2>&1 | tail -1
}
probe_is() {  # $1=域名 $2=blocked|allowed
  local out; out=$(probe "$1")
  case "$out" in
    FAIL*|*UnicodeEncodeError*|*Traceback*) echo "PROBE_ERROR:$out"; return 2 ;;
  esac
  if [[ "$2" == "blocked" ]]; then
    echo "$out" | grep -q '0.0.0.0' && { echo "BLOCKED"; return 0; } || { echo "ALLOWED:$out"; return 1; }
  else
    echo "$out" | grep -q '0.0.0.0' && { echo "BLOCKED:$out"; return 1; } || { echo "ALLOWED"; return 0; }
  fi
}
wait_probe() {  # $1=域名 $2=blocked|allowed
  local i r
  for i in $(seq 1 40); do
    r=$(probe_is "$1" "$2"); local rc=$?
    [[ $rc -eq 0 ]] && { echo "$r"; return 0; }
    [[ $rc -eq 2 ]] && { echo "$r"; return 2; }
    sleep 0.5
  done
  echo "$r"; return 1
}

hdr "等快照热加载"
OUT=$(wait_probe gblock.test blocked)
echo "  $OUT"
case "$OUT" in
  BLOCKED) ok "全局规则对普通用户生效（被拦到 0.0.0.0）" ;;
  PROBE_ERROR*) bad "探针自身报错，无法判定: $OUT" ;;
  *) bad "全局规则未对普通用户生效: $OUT" ;;
esac

hdr "用户加自己的白名单 → 应覆盖全局拦截"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/rules" \
  -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
  -d '{"kind":"allow","pattern":"@@||gblock.test^","value":"","qtypes":[],"enabled":true}')
[[ "$CODE" == "201" ]] && ok "用户白名单已创建" || bad "用户白名单创建失败 $CODE"
OUT=$(wait_probe gblock.test allowed)
echo "  $OUT"
case "$OUT" in
  ALLOWED) ok "用户 allow 覆盖了全局 block" ;;
  PROBE_ERROR*) bad "探针自身报错，无法判定: $OUT" ;;
  *) bad "用户 allow 未覆盖全局 block: $OUT" ;;
esac

hdr "用户关闭全局规则 → 全局拦截不应再命中，但自己的规则仍生效"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/me/preferences" \
  -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
  -d '{"use_global_rules":false}')
[[ "$CODE" == "200" ]] && ok "偏好已关闭全局规则" || bad "关闭全局规则失败 $CODE"

# 先删掉用户自己的白名单，这样关闭全局后该域名应恢复正常解析
RID=$(curl -s "$API/rules" -H "Authorization: Bearer $UTOK" | python3 -c 'import sys,json;print(json.load(sys.stdin)["items"][0]["id"])')
curl -s -o /dev/null -X DELETE "$API/rules/$RID" -H "Authorization: Bearer $UTOK"
OUT=$(wait_probe gblock.test allowed)
echo "  $OUT"
case "$OUT" in
  ALLOWED) ok "关闭全局后全局规则不再生效" ;;
  PROBE_ERROR*) bad "探针自身报错，无法判定: $OUT" ;;
  *) bad "关闭全局后全局拦截仍生效: $OUT" ;;
esac

hdr "配额：用户规则上限 50"
for i in $(seq 1 50); do
  curl -s -o /dev/null -X POST "$API/rules" -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
    -d "{\"kind\":\"block\",\"pattern\":\"||q$i.test^\",\"value\":\"\",\"qtypes\":[],\"enabled\":false}"
done
CODE=$(curl -s -o /tmp/q.out -w '%{http_code}' -X POST "$API/rules" \
  -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
  -d '{"kind":"block","pattern":"||over.test^","value":"","qtypes":[],"enabled":false}')
[[ "$CODE" == "403" ]] && ok "第 51 条被拒（403）" || bad "第 51 条返回 $CODE（期望 403）"
grep -q 'QUOTA_EXCEEDED' /tmp/q.out && ok "错误码是 QUOTA_EXCEEDED" || bad "错误码不对: $(cat /tmp/q.out)"

hdr "配额：批量导入同样受限"
CODE=$(curl -s -o /tmp/qi.out -w '%{http_code}' -X POST "$API/rules/import" \
  -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
  -d '{"content":"||a.test^\n||b.test^","format":"auto","enabled":true}')
[[ "$CODE" == "403" ]] && ok "导入也返回 403（不能绕过配额）" || bad "导入返回 $CODE（期望 403）"

hdr "证书状态与上传"
CODE=$(curl -s -o /tmp/t.out -w '%{http_code}' "$API/admin/tls" -H "Authorization: Bearer $ATOK")
[[ "$CODE" == "200" ]] && ok "GET /admin/tls 可用" || bad "GET /admin/tls 返回 $CODE"
python3 -c 'import json;d=json.load(open("/tmp/t.out"));print("  available=",d["available"],"self_signed=",d["self_signed"],"days_left=",d["days_left"])' 2>/dev/null

# 用实例自己的自签证书回灌，验证「校验并热替换」链路
CERT="$RUN/data/certs/fullchain.pem"; KEY="$RUN/data/certs/privkey.pem"
if [[ ! -f "$CERT" ]]; then
  # dev_tls 的自签证书只在内存里，不落盘；这里自己造一对专门测上传链路
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
    -keyout "$KEY" -out "$CERT" \
    -subj "/CN=*.vg.local" -addext "subjectAltName=DNS:*.vg.local,DNS:vg.local" \
    >/dev/null 2>&1
fi
if [[ -f "$CERT" && -f "$KEY" ]]; then
  python3 - "$CERT" "$KEY" > /tmp/tls_payload.json <<'PY'
import json,sys
print(json.dumps({"cert_pem": open(sys.argv[1]).read(), "key_pem": open(sys.argv[2]).read()}))
PY
  CODE=$(curl -s -o /tmp/tu.out -w '%{http_code}' -X POST "$API/admin/tls" \
    -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' --data-binary @/tmp/tls_payload.json)
  [[ "$CODE" == "200" ]] && ok "上传证书成功并热加载" || bad "上传证书返回 $CODE $(head -c 200 /tmp/tu.out)"
else
  bad "找不到自签证书文件，跳过上传测试"
fi

# 配对错误必须被拒（把证书当私钥传）
CODE=$(curl -s -o /tmp/tb.out -w '%{http_code}' -X POST "$API/admin/tls" \
  -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"cert_pem":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----","key_pem":"not a key"}')
[[ "$CODE" == "422" || "$CODE" == "400" ]] && ok "坏证书被拒（$CODE）" || bad "坏证书返回 $CODE（期望 4xx）"

hdr "反馈：用户提交 → 管理员回复 → 用户可见"
CODE=$(curl -s -o /tmp/f.out -w '%{http_code}' -X POST "$API/feedback" \
  -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
  -d '{"domain":"gblock.test","kind":"wrong_block","message":"这个域名被误拦了"}')
[[ "$CODE" == "201" ]] && ok "用户提交反馈成功" || bad "提交反馈失败 $CODE $(cat /tmp/f.out)"
FID=$(python3 -c 'import json;print(json.load(open("/tmp/f.out"))["id"])' 2>/dev/null)

OPEN=$(curl -s "$API/admin/feedback" -H "Authorization: Bearer $ATOK" | python3 -c 'import sys,json;print(json.load(sys.stdin)["open_count"])')
[[ "${OPEN:-0}" -ge 1 ]] && ok "管理员看到待处理 $OPEN 条" || bad "管理员 open_count=$OPEN"

CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/admin/feedback/$FID" \
  -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"reply":"已确认误拦，正在调整规则"}')
[[ "$CODE" == "200" ]] && ok "管理员回复成功" || bad "管理员回复失败 $CODE"

REPLY=$(curl -s "$API/feedback" -H "Authorization: Bearer $UTOK" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["items"][0]["admin_reply"], d["items"][0]["status"])' 2>/dev/null)
echo "  用户看到: $REPLY"
echo "$REPLY" | grep -q '已确认误拦' && ok "用户能看到管理员回复" || bad "用户看不到回复: $REPLY"
echo "$REPLY" | grep -q 'replied' && ok "状态自动变为 replied" || bad "状态未变 replied: $REPLY"

echo
echo "════════════════════════════════════════"
echo "  结果：$PASS 通过 / $FAIL 失败"
echo "════════════════════════════════════════"
[[ $FAIL -eq 0 ]] || exit 1
