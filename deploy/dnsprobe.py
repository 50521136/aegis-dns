#!/usr/bin/env python3
"""dnsprobe —— 零依赖的 DNS 多协议探针（标准库实现）。

用途：验证 dnsd 的四种协议入口是否都真的能解析，而不只是「端口在监听」。
比 dig/kdig 更可靠的地方在于：
  * 自己构造并解析 DNS 报文，不受 dig 版本差异影响；
  * DoT 用 Python 的 ssl 模块直接做 TLS 握手，不依赖 kdig 是否安装；
  * DoH 支持「Host 首段」与「URL 路径段」两种租户识别方式，可分别验证。

用法：
  dnsprobe.py udp  127.0.0.1:15353  ads.example.com [A]
  dnsprobe.py tcp  127.0.0.1:15353  example.com
  dnsprobe.py dot  127.0.0.1:18853  ads.example.com --sni k7m2pqx4ab.dns.example.com
  dnsprobe.py doh  https://127.0.0.1:14443 --host k7m2pqx4ab.dns.example.com --insecure ads.example.com
  dnsprobe.py doh  https://127.0.0.1:14443 --path /dns-query/k7m2pqx4ab --insecure ads.example.com

退出码：0 = 得到应答（任何 rcode 都算通），1 = 网络层失败，2 = 参数错误。
"""

import argparse
import base64
import http.client
import random
import socket
import ssl
import struct
import sys

QTYPES = {
    "A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "MX": 15,
    "TXT": 16, "AAAA": 28, "SRV": 33, "DS": 43, "RRSIG": 46,
    "DNSKEY": 48, "HTTPS": 65, "SVCB": 64, "ANY": 255,
}
RTYPES = {v: k for k, v in QTYPES.items()}
RCODES = {
    0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN",
    4: "NOTIMP", 5: "REFUSED",
}


def encode_name(name: str) -> bytes:
    out = b""
    for label in name.rstrip(".").split("."):
        if not label:
            continue
        b = label.encode("idna") if any(ord(c) > 127 for c in label) else label.encode()
        if len(b) > 63:
            raise ValueError(f"标签过长: {label}")
        out += bytes([len(b)]) + b
    return out + b"\x00"


def build_query(name: str, qtype: int, do: bool = False) -> tuple[bytes, int]:
    qid = random.randint(1, 0xFFFF)
    flags = 0x0100  # RD
    msg = struct.pack("!HHHHHH", qid, flags, 1, 0, 0, 0)
    msg += encode_name(name) + struct.pack("!HH", qtype, 1)
    if do:
        # EDNS0 OPT，置 DO 位，UDP 载荷 4096
        msg += b"\x00" + struct.pack("!HHIH", 41, 4096, 0x8000, 0)
    return msg, qid


def read_name(data: bytes, off: int, depth: int = 0) -> tuple[str, int]:
    if depth > 20:
        raise ValueError("压缩指针成环")
    labels = []
    while True:
        if off >= len(data):
            raise ValueError("报文截断")
        ln = data[off]
        if ln == 0:
            off += 1
            break
        if ln & 0xC0 == 0xC0:
            ptr = struct.unpack("!H", data[off:off + 2])[0] & 0x3FFF
            sub, _ = read_name(data, ptr, depth + 1)
            labels.append(sub)
            off += 2
            break
        off += 1
        labels.append(data[off:off + ln].decode("latin-1"))
        off += ln
    return ".".join(l for l in labels if l), off


def parse_response(data: bytes) -> dict:
    if len(data) < 12:
        raise ValueError(f"应答过短（{len(data)} 字节）")
    qid, flags, qd, an, ns, ar = struct.unpack("!HHHHHH", data[:12])
    rcode = flags & 0x000F
    tc = bool(flags & 0x0200)
    out = {"id": qid, "rcode": RCODES.get(rcode, str(rcode)), "rcode_num": rcode,
           "truncated": tc, "answers": [], "authority": []}

    off = 12
    for _ in range(qd):
        _, off = read_name(data, off)
        off += 4

    def read_rrs(count, off):
        rrs = []
        for _ in range(count):
            name, off = read_name(data, off)
            if off + 10 > len(data):
                break
            rtype, rclass, ttl, rdlen = struct.unpack("!HHIH", data[off:off + 10])
            off += 10
            rdata = data[off:off + rdlen]
            off += rdlen
            val = ""
            if rtype == 1 and rdlen == 4:
                val = ".".join(str(b) for b in rdata)
            elif rtype == 28 and rdlen == 16:
                val = ":".join(f"{rdata[i]:02x}{rdata[i+1]:02x}" for i in range(0, 16, 2))
            elif rtype in (5, 2, 12):
                val, _ = read_name(data, off - rdlen)
            elif rtype == 16:
                parts = []
                i = 0
                while i < len(rdata):
                    n = rdata[i]
                    parts.append(rdata[i + 1:i + 1 + n].decode("utf-8", "replace"))
                    i += 1 + n
                val = " ".join(parts)
            elif rtype == 15 and rdlen > 2:
                pref = struct.unpack("!H", rdata[:2])[0]
                nm, _ = read_name(data, off - rdlen + 2)
                val = f"{pref} {nm}"
            elif rtype in (6,) and rdlen > 20:
                mname, o2 = read_name(data, off - rdlen)
                rname, _ = read_name(data, o2)
                val = f"{mname} {rname}"
            elif rtype in (64, 65) and rdlen >= 3:
                prio = struct.unpack("!H", rdata[:2])[0]
                tname, _ = read_name(data, off - rdlen + 2)
                val = f"{prio} {tname}"
            else:
                val = f"<{rdlen} 字节>"
            rrs.append({"name": name, "type": RTYPES.get(rtype, str(rtype)),
                        "ttl": ttl, "value": val})
        return rrs, off

    out["answers"], off = read_rrs(an, off)
    out["authority"], off = read_rrs(ns, off)
    out["extra"], _ = read_rrs(ar, off)
    return out


def recv_tcp(sock) -> bytes:
    hdr = b""
    while len(hdr) < 2:
        chunk = sock.recv(2 - len(hdr))
        if not chunk:
            raise ConnectionError("连接被关闭")
        hdr += chunk
    n = struct.unpack("!H", hdr)[0]
    body = b""
    while len(body) < n:
        chunk = sock.recv(n - len(body))
        if not chunk:
            raise ConnectionError("连接被关闭")
        body += chunk
    return body


def send_tcp(sock, msg: bytes) -> None:
    sock.sendall(struct.pack("!H", len(msg)) + msg)


def query_udp(host, port, msg, qid, timeout=5.0):
    sock = socket.socket(socket.AF_INET6 if ":" in host else socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        sock.sendto(msg, (host, port))
        # 可能有无关报文先到，按 ID 过滤，避免误判为「ID 不匹配」。
        deadline = timeout
        while deadline > 0:
            data, _ = sock.recvfrom(4096)
            got = struct.unpack("!H", data[:2])[0]
            if got == qid:
                return data
            deadline -= 0.5
        raise TimeoutError(f"等待 ID={qid} 的应答超时（收到过其它 ID 的报文）")
    finally:
        sock.close()


def query_tcp(host, port, msg, timeout=5.0, tls=False, sni=None, insecure=False):
    raw = socket.create_connection((host, port), timeout=timeout)
    try:
        if tls:
            ctx = ssl.create_default_context()
            if insecure:
                ctx.check_hostname = False
                ctx.verify_mode = ssl.CERT_NONE
            raw = ctx.wrap_socket(raw, server_hostname=sni or host)
        raw.settimeout(timeout)
        send_tcp(raw, msg)
        return recv_tcp(raw)
    finally:
        try:
            raw.close()
        except Exception:
            pass


def query_doh(url, host_header, path, msg, insecure, timeout=8.0):
    from urllib.parse import urlparse
    u = urlparse(url)
    is_tls = u.scheme == "https"
    port = u.port or (443 if is_tls else 80)
    if is_tls:
        ctx = ssl.create_default_context()
        if insecure:
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE
        conn = http.client.HTTPSConnection(u.hostname, port, timeout=timeout, context=ctx)
    else:
        conn = http.client.HTTPConnection(u.hostname, port, timeout=timeout)

    hdrs = {"Content-Type": "application/dns-message",
            "Accept": "application/dns-message"}
    if host_header:
        hdrs["Host"] = host_header
    p = path or (u.path if u.path and u.path != "/" else "/dns-query")
    conn.request("POST", p, body=msg, headers=hdrs)
    resp = conn.getresponse()
    body = resp.read()
    status = resp.status
    conn.close()
    if status != 200:
        raise ConnectionError(f"HTTP {status}: {body[:200]!r}")
    return body


def main() -> int:
    ap = argparse.ArgumentParser(description="DNS 多协议探针")
    ap.add_argument("proto", choices=["udp", "tcp", "dot", "doh"])
    ap.add_argument("target", help="host:port（doh 用完整 URL）")
    ap.add_argument("qname")
    ap.add_argument("qtype", nargs="?", default="A")
    ap.add_argument("--sni", default=None, help="DoT 的 SNI（租户子域名）")
    ap.add_argument("--host", default=None, help="DoH 的 Host 头（租户子域名）")
    ap.add_argument("--path", default=None, help="DoH 的 URL 路径（路径式识别）")
    ap.add_argument("--insecure", action="store_true", help="跳过 TLS 证书校验")
    ap.add_argument("--json", action="store_true", help="输出 JSON")
    args = ap.parse_args()

    qtype = QTYPES.get(args.qtype.upper())
    if qtype is None:
        print(f"未知记录类型: {args.qtype}", file=sys.stderr)
        return 2

    msg, qid = build_query(args.qname, qtype)

    try:
        if args.proto == "udp":
            host, port = args.target.rsplit(":", 1)
            data = query_udp(host, int(port), msg, qid)
        elif args.proto == "tcp":
            host, port = args.target.rsplit(":", 1)
            data = query_tcp(host, int(port), msg)
        elif args.proto == "dot":
            host, port = args.target.rsplit(":", 1)
            data = query_tcp(host, int(port), msg, tls=True, sni=args.sni, insecure=args.insecure)
        else:
            data = query_doh(args.target, args.host, args.path, msg, args.insecure)
    except Exception as e:
        print(f"FAIL {args.proto} {args.qname} {args.qtype}: {type(e).__name__}: {e}", file=sys.stderr)
        return 1

    try:
        r = parse_response(data)
    except Exception as e:
        print(f"FAIL 解析应答失败: {e}", file=sys.stderr)
        return 1

    if args.json:
        import json
        print(json.dumps({"proto": args.proto, "qname": args.qname, "qtype": args.qtype.upper(),
                          "rcode": r["rcode"], "answers": r["answers"]}, ensure_ascii=False))
    else:
        vals = ", ".join(a["value"] for a in r["answers"]) or "(无应答记录)"
        print(f"OK {args.proto} {args.qname} {args.qtype.upper()} -> {r['rcode']} {vals}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
