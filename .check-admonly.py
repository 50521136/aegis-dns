import json
import urllib.error
import urllib.request

API = "http://127.0.0.1:18097/api/v1"


def post(path, payload, token=None):
    req = urllib.request.Request(
        API + path,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    if token:
        req.add_header("Authorization", "Bearer " + token)
    return json.load(urllib.request.urlopen(req, timeout=10))


def get(path, token=None):
    req = urllib.request.Request(API + path)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        return json.load(urllib.request.urlopen(req, timeout=10)), 200
    except urllib.error.HTTPError as e:
        return json.loads(e.read() or b"{}"), e.code


atok = post("/auth/login", {"username": "admin", "password": "AdmOnlyPass123"})["tokens"][
    "access_token"
]

print("=== 排行榜 ===")
d, code = get("/admin/stats/users?range=1h&limit=10", atok)
print("  HTTP", code, " range:", d.get("range"), " 用户数:", d.get("total_users"))
for it in d.get("items", []):
    print(
        "   {:<8} total={:<5} blocked={:<4} block_rate={:.2f}".format(
            it["username"] or "(已删)", it["total"], it["blocked"], it["block_rate"]
        )
    )

print("=== 全局日志 ===")
d, code = get("/admin/stats/querylog?limit=3", atok)
print("  HTTP", code, " total:", d.get("total"))
for it in d.get("items", []):
    print(
        "   {:<8} {:<24} {}".format(
            it.get("username") or "(已删)", it["domain"], it["action"]
        )
    )

print("=== 普通用户访问管理员接口 ===")
utok = post("/auth/login", {"username": "alpha", "password": "Str0ngPass123"})["tokens"][
    "access_token"
]
for p in ["/admin/stats/users", "/admin/stats/querylog", "/admin/users", "/admin/settings"]:
    _, c = get(p, utok)
    print("  {:<26} {}".format(p, c))

print("=== 普通用户自己的日志只含自己 ===")
d, c = get("/stats/querylog?limit=100", utok)
mine = {it.get("username") for it in d.get("items", [])}
print("  HTTP", c, " total:", d.get("total"), " 出现的用户:", mine or "{}")
