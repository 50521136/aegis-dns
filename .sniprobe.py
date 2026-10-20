import socket, ssl, threading

PORT = 39555
CERT = "/opt/aegis/data/certs/fullchain.pem"
KEY = "/opt/aegis/data/certs/privkey.pem"

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(CERT, KEY)

srv = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("::", PORT))
srv.listen(16)
print("SNI-PROBE LISTENING %d" % PORT, flush=True)


def handle(raw, addr):
    try:
        raw.settimeout(20)
        c = ctx.wrap_socket(raw, server_side=True)
        print("OK   sni=%-32s from %s" % (c.server_hostname, addr[0]), flush=True)
        c.close()
    except Exception as e:
        print("FAIL sni=<unknown> from %s: %r" % (addr[0], e), flush=True)
    try:
        raw.close()
    except Exception:
        pass


while True:
    raw, addr = srv.accept()
    print("ACCEPT from %s" % (addr[0],), flush=True)
    threading.Thread(target=handle, args=(raw, addr), daemon=True).start()
