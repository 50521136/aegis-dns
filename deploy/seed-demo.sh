#!/usr/bin/env bash
# 生成推广截图用的演示数据（全部脱敏：example.com 域名、alice/bob/carol 用户）
# 用法：bash deploy/seed-demo.sh
set -uo pipefail

API=http://127.0.0.1:18081/api/v1
DB=/tmp/aegis-seed/data/aegis.db

tok() { curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" \
  | python3 -c "import json,sys;print(json.load(sys.stdin)['tokens']['access_token'])"; }

ATOK=$(tok admin admin123)

# --- 设置：三协议全开、签到开启、域名用示例域名 ---
curl -s -X PUT "$API/admin/settings" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"domain":"dns.example.com","enable_doh":true,"enable_dot":true,"enable_plain_dns":true,
       "checkin_enabled":true,"daily_report_enabled":true}' >/dev/null

# --- 演示用户 ---
for n in alice bob carol; do
  curl -s -X POST "$API/auth/register" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$n\",\"email\":\"$n@example.com\",\"password\":\"DemoPass123\"}" >/dev/null
done

# --- 全局规则 ---
for pair in "block:||ads.example.com^" "block:||tracker.example.net^" "allow:safe.example.org"; do
  K=${pair%%:*}; P=${pair#*:}
  curl -s -X POST "$API/admin/rules" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
    -d "{\"kind\":\"$K\",\"pattern\":\"$P\",\"value\":\"\",\"qtypes\":[],\"enabled\":true}" >/dev/null
done

# --- alice 自己的规则 ---
ALICE=$(tok alice DemoPass123)
for pair in "block:||my-ads.example^" "block:||game-tracker.example^" "allow:work.example.com"; do
  K=${pair%%:*}; P=${pair#*:}
  curl -s -X POST "$API/rules" -H "Authorization: Bearer $ALICE" -H 'Content-Type: application/json' \
    -d "{\"kind\":\"$K\",\"pattern\":\"$P\",\"value\":\"\",\"qtypes\":[],\"enabled\":true}" >/dev/null
done

# --- 统计与日志 ---
python3 - "$DB" <<'PY'
import sqlite3, sys, time, random
db = sys.argv[1]
c = sqlite3.connect(db)
random.seed(7)
now = int(time.time()); off = 8*3600
day_start = ((now+off)//86400)*86400 - off
buckets = int((now - day_start)//300)

users = {u: uid for uid, u in c.execute("select id, username from users where role='user'")}
base = {"alice": 260, "bob": 150, "carol": 90}

# rollup
for name, uid in users.items():
    b = base.get(name, 30)
    for i in range(buckets):
        ts = day_start + i*300
        total = max(3, int(b + random.gauss(0, b*0.35)))
        blocked = int(total * random.uniform(0.10, 0.22))
        cached = int(total * random.uniform(0.35, 0.55))
        c.execute("insert or replace into query_rollup(user_id,ts,total,blocked,cached,allowed,latency_sum) values(?,?,?,?,?,?,?)",
                  (uid, ts, total, blocked, cached, total-blocked-cached, total*random.randint(9,26)))
c.commit()

# top_domains：每个域名的 blocked 按比例分配，合计必须小于全站总拦截量
total_blocked = c.execute("select coalesce(sum(blocked),0) from query_rollup where ts>=?", (day_start,)).fetchone()[0]
domains = [("doubleclick.net",0.30),("www.googletagmanager.com",0.24),("graph.facebook.com",0.18),
           ("configsvr.msf.3g.qq.com",0.12),("hm.baidu.com",0.08),("api.miwifi.com",0.05),
           ("i.snssdk.com",0.03),("sentry-cdn.com",0.02)]
c.execute("delete from top_domains")
blist = list(c.execute("select user_id, ts, total from query_rollup where ts>=?", (day_start,)))
for d, share in domains:
    total_hits = int(total_blocked * share) * 5
    per = max(1, total_hits // max(1, len(blist)))
    for uid, ts, total in blist:
        hits = max(1, int(per * (total/200)))
        c.execute("insert or replace into top_domains(user_id,ts,domain,hits,blocked) values(?,?,?,?,?)",
                  (uid, ts, d, hits, hits//5))

# query_log（详情弹窗要能点开）
acts = [("handled",0),("blocked",0),("handled",1),("allowed",1),("allowed",0)]
trackers = {"configsvr.msf.3g.qq.com":"QQ International（客户交互）","hm.baidu.com":"百度统计（统计）",
            "www.googletagmanager.com":"Google Tag Manager（统计）","doubleclick.net":"DoubleClick（广告）"}
doms = ["www.baidu.com","api.weixin.qq.com","configsvr.msf.3g.qq.com","hm.baidu.com",
        "www.googletagmanager.com","doubleclick.net","i.snssdk.com","api.miwifi.com"]
for name, uid in users.items():
    for i in range(40):
        a, ch = random.choices(acts, weights=[55,16,22,5,2])[0]
        d = random.choice(doms)
        ans = "0.0.0.0" if a == "blocked" else "www.example-cdn.com. 300 IN A 93.184.216.34"
        c.execute("""insert into query_log(user_id,ts,domain,qtype,rcode,action,cache_hit,client,latency_ms,
                     proto,ecs,upstream,answer,tracker,client_name) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
                  (uid, now-i*3, d, "A", "NOERROR", a, ch, "203.0.113.0/24", random.randint(3,28),
                   random.choice(["dot","doh"]), "", "223.5.5.5:53", ans, trackers.get(d,""), ""))

# 会员：alice 30 天、bob 7 天；alice 今天已签到
for name, days in (("alice",30),("bob",7)):
    c.execute("update users set member_until=? where id=?", (now+days*86400, users[name]))
today = (now+off)//86400
c.execute("insert or replace into checkins(user_id,day,streak,days_granted,created_at) values(?,?,?,?,?)",
          (users["alice"], today, 6, 6, now))
c.commit()

print(f"  统计：{c.execute('select sum(total) from query_rollup').fetchone()[0]:,} 次查询，"
      f"{total_blocked:,} 次拦截")
print(f"  日志：{c.execute('select count(*) from query_log').fetchone()[0]} 条")
names = [r[0] for r in c.execute("select username from users where role='user'")]
print("  用户：" + ", ".join(names))
PY

echo "演示数据就绪（admin / alice / bob / carol，密码 DemoPass123）"
