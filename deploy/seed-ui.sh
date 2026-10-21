#!/usr/bin/env bash
# seed-ui.sh — 起一个带数据的本地实例，用来在手机尺寸下验收前端。
#
# 用法：bash deploy/seed-ui.sh start | stop | status
#   start 构建 apid/dnsd、造管理员+会员用户+规则、打一批查询，输出登录用的 refresh token
#   stop  按 PID 精确停止（不要用 pkill -f，会连自己一起杀）
#
# 端口：API 18081、DNS 15353。端口被占时 start 会直接报错退出，
# 而不是像以前那样让 e2e 悄悄连到别人的实例上、跑出一堆假失败。
set -uo pipefail

RUN=/tmp/aegis-seed
API_PORT=18081
DNS_PORT=15353
ROOT=$(cd "$(dirname "$0")/.." && pwd)
export PATH=$PATH:/usr/local/go/bin
export GOPROXY=https://goproxy.cn,direct

port_busy() {
  ss -ltn 2>/dev/null | grep -q ":$1 " || ss -lun 2>/dev/null | grep -q ":$1 "
}

stop_all() {
  for f in "$RUN/apid.pid" "$RUN/dnsd.pid"; do
    [ -f "$f" ] && kill -9 "$(cat "$f")" 2>/dev/null
    rm -f "$f"
  done
  sleep 1
}

case "${1:-start}" in
  stop)
    stop_all
    echo "已停止本地实例"
    exit 0
    ;;
  status)
    ss -ltnp 2>/dev/null | grep -E ":$API_PORT |:$DNS_PORT " || echo "未运行"
    exit 0
    ;;
esac

# ---- start ----
if port_busy "$API_PORT" || port_busy "$DNS_PORT"; then
  echo "端口 $API_PORT / $DNS_PORT 已被占用，先跑 bash deploy/seed-ui.sh stop" >&2
  exit 1
fi

stop_all
rm -rf "$RUN"
mkdir -p "$RUN/data"

cat > "$RUN/config.yaml" <<EOF
domain: "dns.example.com"
data_dir: "$RUN/data"
api:
  listen: "127.0.0.1:$API_PORT"
  jwt_secret: "seed-only-secret-0123456789abcdef0123456789"
  admin:
    username: "admin"
    password: "admin123"
dns:
  listen_udp: "127.0.0.1:$DNS_PORT"
  listen_tcp: "127.0.0.1:$DNS_PORT"
  listen_dot: ""
  listen_doh: ""
  listen_doh_plain: ""
upstreams: ["223.5.5.5"]
log:
  level: "info"
EOF

# 前端产物要先进 embed 目录，否则 apid 端的是仓库里的占位 index.html
( cd "$ROOT/web" && npm run build >/dev/null 2>&1 ) || { echo "前端构建失败" >&2; exit 1; }
rm -rf "$ROOT/server/internal/api/webdist/dist"
cp -r "$ROOT/web/dist" "$ROOT/server/internal/api/webdist/dist"

( cd "$ROOT/server" && go build -o "$RUN/apid" ./cmd/apid && go build -o "$RUN/dnsd" ./cmd/dnsd ) \
  || { echo "后端构建失败" >&2; exit 1; }

cd "$RUN"
./apid -config config.yaml > apid.log 2>&1 &
echo $! > apid.pid
./dnsd -config config.yaml > dnsd.log 2>&1 &
echo $! > dnsd.pid

for _ in $(seq 1 40); do
  curl -sf -m 2 "http://127.0.0.1:$API_PORT/api/v1/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf -m 3 "http://127.0.0.1:$API_PORT/api/v1/healthz" >/dev/null || {
  echo "apid 没起来，看 $RUN/apid.log" >&2; tail -5 "$RUN/apid.log" >&2; exit 1; }

API="http://127.0.0.1:$API_PORT/api/v1"
jq_py() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)"; }

ADM=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq_py "d['tokens']['access_token']")

# 开关：签到 + 会员相关，让账户页三张卡都渲染出来
curl -s -X PUT "$API/admin/settings" -H "Authorization: Bearer $ADM" -H 'Content-Type: application/json' \
  -d '{"checkin_enabled":true,"checkin_days":1,"checkin_bonus_every":7,"checkin_bonus_days":7,
       "allow_user_invite":true,"user_invite_max_codes":5,"member_reward_days":30}' -o /dev/null

curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d '{"username":"uicheck","password":"uicheck123"}' -o /dev/null

AID=$(curl -s "$API/admin/users" -H "Authorization: Bearer $ADM" | jq_py "
next(u['id'] for u in d['items'] if u['username']=='uicheck')")
curl -s -X POST "$API/admin/users/$AID/member" -H "Authorization: Bearer $ADM" \
  -H 'Content-Type: application/json' -d '{"days":7}' -o /dev/null

UTOK=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"uicheck","password":"uicheck123"}' | jq_py "d['tokens']['access_token']")
for i in 1 2 3; do
  curl -s -X POST "$API/rules" -H "Authorization: Bearer $UTOK" -H 'Content-Type: application/json' \
    -d "{\"pattern\":\"||ad$i.example^\",\"kind\":\"block\",\"enabled\":true}" -o /dev/null
done

# 打一批查询，让统计和趋势有数
for d in www.baidu.com www.qq.com ad1.example www.163.com doubleclick.net; do
  timeout 4 dig +short @127.0.0.1 -p "$DNS_PORT" "$d" A >/dev/null 2>&1
done
for _ in $(seq 1 15); do
  timeout 4 dig +short @127.0.0.1 -p "$DNS_PORT" www.baidu.com A >/dev/null 2>&1
done

RTOK=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"uicheck","password":"uicheck123"}' | jq_py "d['tokens']['refresh_token']")
echo "$RTOK" > "$RUN/refresh.txt"

echo "实例已就绪：$API"
echo "  refresh token → $RUN/refresh.txt（浏览器里 localStorage.setItem('aegis-refresh-token', ...)）"
echo "  停止：bash deploy/seed-ui.sh stop"
