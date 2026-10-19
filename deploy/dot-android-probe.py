#!/usr/bin/env python3
"""模拟 Android「私人 DNS」的 DoT 行为：一条 TLS 连接上连续发多次查询。

Android 的 Private DNS 会长期复用同一条连接（还会做连接保活），
如果服务端只处理一次查询就关闭，Android 侧就表现为「连不上」或时断时续。
"""
import socket
import ssl
import struct
import sys
import time

HOST = sys.argv[1] if len(sys.argv) > 1 else "4ckl4zgpv5.520342.xyz"
IP = sys.argv[2] if len(sys.argv) > 2 else "117.72.155.43"
PORT = 853


def build_query(name, qid=0x1234):
    b = struct.pack("!HHHHHH", qid, 0x0100, 1, 0, 0, 0)
    for label in name.split("."):
        b += bytes([len(label)]) + label.encode()
    return b + b"\x00" + struct.pack("!HH", 1, 1)


def main():
    ctx = ssl.create_default_context()
    ctx.set_alpn_protocols(["dot"])
    raw = socket.create_connection((IP, PORT), timeout=10)
    tls = ctx.wrap_socket(raw, server_hostname=HOST)
    print(f"握手: {tls.version()} ALPN={tls.selected_alpn_protocol()}")

    names = ["example.com", "cloudflare.com", "github.com",
             "wikipedia.org", "example.org"]
    ok = 0
    for i, name in enumerate(names, 1):
        try:
            d = build_query(name, 0x1000 + i)
            tls.sendall(struct.pack("!H", len(d)) + d)
            hdr = tls.recv(2)
            if len(hdr) < 2:
                print(f"  第{i}次 {name}: 服务端关闭了连接（只收到 {len(hdr)} 字节）")
                break
            ln = struct.unpack("!H", hdr)[0]
            body = b""
            while len(body) < ln:
                chunk = tls.recv(ln - len(body))
                if not chunk:
                    break
                body += chunk
            rcode = body[3] & 0x0F if len(body) > 3 else -1
            ancount = struct.unpack("!H", body[6:8])[0] if len(body) >= 8 else -1
            print(f"  第{i}次 {name}: {len(body)} 字节 rcode={rcode} 答案数={ancount}")
            ok += 1
        except Exception as e:
            print(f"  第{i}次 {name}: 异常 {type(e).__name__}: {e}")
            break

    print(f"同一条连接上成功完成 {ok}/{len(names)} 次查询")

    # 空闲 5 秒后再查一次：Android 会保持空闲连接，服务端不该把它掐掉
    print("空闲 5 秒后再查一次（模拟 Android 的连接保活）...")
    time.sleep(5)
    try:
        d = build_query("example.com", 0x9999)
        tls.sendall(struct.pack("!H", len(d)) + d)
        hdr = tls.recv(2)
        if len(hdr) >= 2:
            ln = struct.unpack("!H", hdr)[0]
            body = b""
            while len(body) < ln:
                chunk = tls.recv(ln - len(body))
                if not chunk:
                    break
                body += chunk
            print(f"  空闲后仍可用: {len(body)} 字节")
        else:
            print("  空闲后连接已被关闭 —— Android 会认为服务器不稳定")
    except Exception as e:
        print(f"  空闲后异常: {type(e).__name__}: {e}")

    tls.close()


if __name__ == "__main__":
    main()
