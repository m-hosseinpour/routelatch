"""Minimal SOCKS5 UDP ASSOCIATE client used only by the R16-J fixture."""
import socket, struct, sys, json, time

def recv_exact(s, n):
    buf = b""
    while len(buf) < n:
        c = s.recv(n - len(buf))
        if not c:
            raise EOFError("short read")
        buf += c
    return buf

def associate(proxy_host, proxy_port, timeout=5.0):
    t = socket.create_connection((proxy_host, proxy_port), timeout=timeout)
    t.sendall(b"\x05\x01\x00")
    ver, method = recv_exact(t, 2)
    if ver != 5 or method != 0:
        raise RuntimeError(f"greeting failed ver={ver} method={method}")
    # UDP ASSOCIATE, 0.0.0.0:0
    t.sendall(b"\x05\x03\x00\x01" + b"\x00\x00\x00\x00" + b"\x00\x00")
    ver, rep, rsv, atyp = recv_exact(t, 4)
    if rep != 0:
        raise RuntimeError(f"associate refused rep={rep}")
    if atyp == 1:
        addr = socket.inet_ntoa(recv_exact(t, 4))
    elif atyp == 4:
        addr = socket.inet_ntop(socket.AF_INET6, recv_exact(t, 16))
    elif atyp == 3:
        ln = recv_exact(t, 1)[0]
        addr = recv_exact(t, ln).decode()
    else:
        raise RuntimeError(f"bad atyp {atyp}")
    port = struct.unpack("!H", recv_exact(t, 2))[0]
    if addr in ("0.0.0.0", "::"):
        addr = proxy_host
    u = socket.socket(socket.AF_INET6 if ":" in addr else socket.AF_INET, socket.SOCK_DGRAM)
    u.settimeout(timeout)
    return t, u, (addr, port)

def send_recv(u, relay, dest, payload, timeout=5.0):
    host, port = dest
    pkt = b"\x00\x00\x00\x01" + socket.inet_aton(host) + struct.pack("!H", port) + payload
    t0 = time.monotonic()
    u.sendto(pkt, relay)
    data, _ = u.recvfrom(65535)
    rtt = (time.monotonic() - t0) * 1000.0
    if len(data) < 10:
        raise RuntimeError("short udp reply")
    # 00 00 00 ATYP ADDR PORT payload
    atyp = data[3]
    off = 4
    if atyp == 1:
        off += 4
    elif atyp == 4:
        off += 16
    elif atyp == 3:
        off += 1 + data[4]
    off += 2
    return data[off:], rtt

def one(proxy, dest, payload):
    t, u, relay = associate(*proxy)
    try:
        body, rtt = send_recv(u, relay, dest, payload)
        return {"ok": body == payload, "len": len(body), "rtt_ms": round(rtt, 2), "relay": relay[0]}
    finally:
        u.close(); t.close()

if __name__ == "__main__":
    host, port = sys.argv[1].split(":")
    dh, dp = sys.argv[2].split(":")
    payload = sys.argv[3].encode() if len(sys.argv) > 3 else b"hello-r16j"
    print(json.dumps(one((host, int(port)), (dh, int(dp)), payload)))
