import json
import socket
import ssl
import struct
import urllib.request

API = "http://127.0.0.1:3000/api/v1"
DOMAIN = "adguard.seven-cloud.cn"


def dot_query(sni, qname="www.baidu.com", host="127.0.0.1", port=853, verify=False):
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    if verify:
        ctx.load_default_certs()
    else:
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
    raw = socket.create_connection((host, port), timeout=10)
    conn = ctx.wrap_socket(raw, server_hostname=sni)
    tid = 0x2233
    header = struct.pack(">HHHHHH", tid, 0x0100, 1, 0, 0, 0)
    q = b"".join(bytes([len(p)]) + p.encode() for p in qname.split(".")) + b"\x00"
    pkt = header + q + struct.pack(">HH", 1, 1)
    conn.sendall(struct.pack(">H", len(pkt)) + pkt)
    ln = struct.unpack(">H", conn.recv(2))[0]
    buf = b""
    while len(buf) < ln:
        d = conn.recv(ln - len(buf))
        if not d:
            break
        buf += d
    conn.close()
    rcode = struct.unpack(">H", buf[2:4])[0] & 0x000F
    ancount = struct.unpack(">H", buf[6:8])[0]
    names = {0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP", 5: "REFUSED"}
    return names.get(rcode, "rcode=%d" % rcode), ancount


print("=== 1. 裸域名 DoT（严格校验证书，模拟真机）===")
try:
    rc, an = dot_query(DOMAIN, verify=True)
    print("   严格校验通过 -> %s (答案 %d)" % (rc, an))
except Exception as e:
    print("   失败: %r" % (e,))

print("=== 2. 裸域名 DoT（跳过证书校验，只看 dnsd 是否放行）===")
try:
    rc, an = dot_query(DOMAIN)
    print("   -> %s (答案 %d)" % (rc, an))
    if rc == "REFUSED":
        print("   !!! allow_root_domain 处于关闭状态，裸域名被 dnsd 主动拒绝")
except Exception as e:
    print("   失败: %r" % (e,))

print("=== 3. 对照：DoH 裸域名路径式（https://<域名>/dns-query/<id>）===")
try:
    import random
    import string
    n = "rt" + "".join(random.choice(string.digits) for _ in range(6))
    r = urllib.request.Request(
        API + "/auth/register",
        data=json.dumps({"username": n, "email": n + "@t.local", "password": "Str0ngPass123"}).encode(),
        method="POST",
    )
    r.add_header("Content-Type", "application/json")
    d = json.loads(urllib.request.urlopen(r, timeout=20).read().decode())
    cid = d["user"]["client_id"]
    print("   临时 client_id:", cid)
    print("   面板给的 doh:", d["dns_config"]["doh_url"])
except Exception as e:
    print("   注册失败:", e)
