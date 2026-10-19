#!/usr/bin/env bash
# 起本地实例并造出两个用户的流量，用于验证管理员排行榜与全局日志
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin GOPROXY=https://goproxy.cn,direct

SRC=/root/aegis-dns/server
RUN=/root/aegis-dns/.admonly
API_PORT=18097; DNS_PORT=15367; DOT_PORT=18867; DOH_PORT=14467; DOH_PLAIN=18067
API="http://127.0.0.1:$API_PORT/api/v1"
PROBE="python3 /root/aegis-dns/deploy/dnsprobe.py"

rm -rf "$RUN"; mkdir -p "$RUN/data"
cd "$SRC" || exit 1
# 内嵌前端
rm -rf internal/api/webdist/dist; mkdir -p internal/api/webdist/dist
cp -r /root/aegis-dns/web/dist/* internal/api/webdist/dist/
CGO_ENABLED=0 go build -o "$RUN/apid" ./cmd/apid || exit 1
CGO_ENABLED=0 go build -o "$RUN/dnsd" ./cmd/dnsd || exit 1

JWT=$("$RUN/apid" -gen-secret)
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
  query_log_ring: 2000
  stats_flush_seconds: 2
api:
  listen: "127.0.0.1:$API_PORT"
  tls_listen: ""
  jwt_secret: "$JWT"
  admin:
    username: "admin"
    password: "AdmOnlyPass123"
upstreams: ["223.5.5.5"]
log:
  level: "warn"
EOF

"$RUN/dnsd" -config "$RUN/config.yaml" > "$RUN/dnsd.log" 2>&1 &
"$RUN/apid" -config "$RUN/config.yaml" > "$RUN/apid.log" 2>&1 &
for i in $(seq 1 40); do curl -fsS -m 2 "$API/healthz" >/dev/null 2>&1 && break; sleep 0.5; done
echo "healthz: $(curl -s "$API/healthz")"

login() { curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["tokens"]["access_token"])'; }

ATOK=$(login admin AdmOnlyPass123)
echo "admin token: ${#ATOK} chars"

# 两个租户：client_id 必须精确取出，拼错 SNI 会让 dnsd 认不出用户、
# 把所有查询归到同一个未知 user_id，排行榜看起来就像坏了。
reg_cid() {
  curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"email\":\"$1@t.local\",\"password\":\"Str0ngPass123\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["user"]["client_id"])'
}
ACID=$(reg_cid alpha); BCID=$(reg_cid beta)
echo "  alpha client_id=$ACID"
echo "  beta  client_id=$BCID"

# 造流量：alpha 40 次，beta 12 次
echo "造流量…"
for i in $(seq 1 40); do
  $PROBE dot 127.0.0.1:$DOT_PORT "a$((i % 5)).example.com" A --sni "$ACID.dns.vg.local" --insecure >/dev/null 2>&1
done
for i in $(seq 1 12); do
  $PROBE dot 127.0.0.1:$DOT_PORT "b$((i % 3)).example.org" A --sni "$BCID.dns.vg.local" --insecure >/dev/null 2>&1
done
sleep 6

echo "=== 排行榜（管理员）==="
curl -s "$API/admin/stats/users?range=1h&limit=10" -H "Authorization: Bearer $ATOK" | python3 -m json.tool 2>/dev/null | head -30

echo "=== 全局日志（前 2 条）==="
curl -s "$API/admin/stats/querylog?limit=2" -H "Authorization: Bearer $ATOK" | python3 -c '
import sys,json
d=json.load(sys.stdin)
print("  total:", d["total"])
for it in d["items"][:2]:
    print("   ", it.get("username"), it["domain"], it["action"])
' 2>&1

echo "=== 普通用户访问管理员统计应 403 ==="
UTOK=$(login alpha Str0ngPass123)
printf "  /admin/stats/users    %s\n" "$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/stats/users" -H "Authorization: Bearer $UTOK")"
printf "  /admin/stats/querylog %s\n" "$(curl -s -o /dev/null -w '%{http_code}' "$API/admin/stats/querylog" -H "Authorization: Bearer $UTOK")"
echo "=== 普通用户的日志只含自己 ==="
curl -s "$API/stats/querylog?limit=100" -H "Authorization: Bearer $UTOK" | python3 -c '
import sys,json
d=json.load(sys.stdin)
print("  total:", d["total"])
' 2>&1

echo "READY"
sleep 3000
