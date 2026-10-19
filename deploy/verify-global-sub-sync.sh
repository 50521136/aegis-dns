#!/usr/bin/env bash
# 验证镜像回退对真实全局订阅生效：触发刷新并轮询结果
# 密码从 .env 读取，全程不打印
set -uo pipefail
# 老机器 locale 常常不是 UTF-8，内联 python 打印中文会 UnicodeEncodeError 直接崩
export PYTHONIOENCODING=utf-8
API=http://127.0.0.1:3000/api/v1

ADM_PASS=$(grep -E '^ADMIN_PASSWORD=' /opt/aegis/.env | cut -d= -f2-)
ADM_USER=$(grep -E '^ADMIN_EMAIL=' /opt/aegis/.env | cut -d= -f2-)
[[ -z "${ADM_USER:-}" ]] && ADM_USER=admin

TOK=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADM_USER\",\"password\":\"$ADM_PASS\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("tokens",{}).get("access_token",""))' 2>/dev/null)
if [[ -z "$TOK" ]]; then
  echo "管理员登录失败"
  exit 1
fi
echo "管理员登录 OK"

echo "=== 当前全局订阅 ==="
curl -s "$API/admin/subscriptions" -H "Authorization: Bearer $TOK" | python3 -c '
import sys,json
items = json.load(sys.stdin).get("items", [])
if not items:
    print("  (无全局订阅)")
for x in items:
    print("  %-22s enabled=%s count=%-6s status=%s" % (x["name"], x["enabled"], x["last_count"], (x["last_status"] or "")[:50]))
    print("    url:", x["url"])
'

: > /tmp/gsub_ids.txt
curl -s "$API/admin/subscriptions" -H "Authorization: Bearer $TOK" | python3 -c '
import sys,json
for x in json.load(sys.stdin).get("items", []):
    print(x["id"])
' > /tmp/gsub_ids.txt

echo "=== 触发刷新 ==="
while read -r sid; do
  [[ -z "$sid" ]] && continue
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/admin/subscriptions/$sid/refresh" \
    -H "Authorization: Bearer $TOK")
  echo "  $sid -> HTTP $code"
done < /tmp/gsub_ids.txt

echo "=== 轮询结果（最多 8 分钟）==="
for i in $(seq 1 96); do
  OUT=$(curl -s "$API/admin/subscriptions" -H "Authorization: Bearer $TOK" | python3 -c '
import sys,json
rows = json.load(sys.stdin).get("items", [])
pend = [x for x in rows if (x["last_status"] or "").startswith("pending")]
for x in rows:
    print("  %-22s count=%-6s status=%s" % (x["name"], x["last_count"], (x["last_status"] or "")[:60]))
print("PENDING=%d" % len(pend))
')
  echo "$OUT" | grep -q 'PENDING=0' && { echo "$OUT"; echo "  同步结束（第 $((i*5)) 秒）"; break; }
  [[ $i -eq 96 ]] && { echo "$OUT"; echo "  超时仍在同步中"; }
  sleep 5
done
rm -f /tmp/gsub_ids.txt
