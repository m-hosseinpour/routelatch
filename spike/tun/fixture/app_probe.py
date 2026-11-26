#!/usr/bin/env python3
"""Disposable R16-A local application probe (runs in the TUN container).

It is intentionally a plain application: it does not know about RouteLatch,
does not use a proxy, and its traffic is expected to be captured by rl-tun0.
"""
import argparse
import json
import random
import socket
import ssl
import struct
import sys
import time


def out(obj, code=0):
    print(json.dumps(obj, sort_keys=True))
    sys.exit(code)


def tcp(args):
    start = time.time()
    try:
        s = socket.create_connection((args.host, args.port), timeout=args.timeout)
    except OSError as e:
        out({"ok": False, "stage": "connect", "error": str(e), "ms": int((time.time() - start) * 1000)}, 1)
    result = {"ok": False, "stage": "connected", "ms": 0}
    try:
        if args.mode == "tls":
            ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE
            s = ctx.wrap_socket(s, server_hostname=args.sni or args.host)
            s.sendall(b"GET / HTTP/1.1\r\nHost: " + (args.sni or args.host).encode() + b"\r\nConnection: close\r\n\r\n")
            data = s.recv(4096)
            result.update({"ok": bool(data), "stage": "tls", "bytes": len(data), "first": data[:32].decode("latin1")})
        elif args.mode == "http":
            req = b"GET / HTTP/1.1\r\nHost: " + args.host_header.encode() + b"\r\nConnection: close\r\n\r\n"
            s.sendall(req)
            data = s.recv(4096)
            result.update({"ok": bool(data), "stage": "http", "bytes": len(data), "first": data[:32].decode("latin1")})
        else:
            nonce = "%08x" % random.getrandbits(32)
            s.sendall(b"PROBE:" + nonce.encode())
            data = s.recv(4096)
            result.update({"ok": data == b"ECHO:PROBE:" + nonce.encode(), "stage": "echo",
                           "bytes": len(data), "nonce": nonce})
    except OSError as e:
        result["error"] = str(e)
        result["stage"] = "io"
    finally:
        s.close()
    result["ms"] = int((time.time() - start) * 1000)
    out(result, 0 if result["ok"] else 1)


def build_query(qid, name, qtype):
    header = struct.pack("!HHHHHH", qid, 0x0100, 1, 0, 0, 0)
    q = b"".join(bytes([len(p)]) + p.encode("ascii") for p in name.rstrip(".").split(".")) + b"\x00"
    return header + q + struct.pack("!HH", qtype, 1)


def skip_name(msg, off):
    while True:
        ln = msg[off]
        if ln == 0:
            return off + 1
        if ln & 0xC0 == 0xC0:
            return off + 2
        off += 1 + ln


def dns(args):
    qid = random.getrandbits(16)
    qtype = 1 if args.qtype == "A" else 28
    packet = build_query(qid, args.name, qtype)
    start = time.time()
    try:
        if args.tcp:
            s = socket.create_connection((args.server, 53), timeout=args.timeout)
            s.sendall(struct.pack("!H", len(packet)) + packet)
            hdr = s.recv(2)
            if len(hdr) < 2:
                out({"ok": False, "stage": "dns_tcp", "error": "short length prefix", "ms": int((time.time() - start) * 1000)}, 1)
            ln = struct.unpack("!H", hdr)[0]
            data = b""
            while len(data) < ln:
                data += s.recv(ln - len(data))
            s.close()
        else:
            s = socket.socket(socket.AF_INET6 if ":" in args.server else socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(args.timeout)
            s.sendto(packet, (args.server, 53))
            data, _ = s.recvfrom(4096)
            s.close()
    except OSError as e:
        out({"ok": False, "stage": "dns", "error": str(e), "ms": int((time.time() - start) * 1000)}, 1)
    rid, flags, qd, an, _ns, _ar = struct.unpack("!HHHHHH", data[:12])
    off = 12
    for _ in range(qd):
        off = skip_name(data, off) + 4
    answers = []
    for _ in range(an):
        off = skip_name(data, off)
        rtype, _rclass, _ttl, rdlen = struct.unpack("!HHIH", data[off:off + 10])
        off += 10
        rdata = data[off:off + rdlen]
        off += rdlen
        if rtype == 1 and rdlen == 4:
            answers.append(socket.inet_ntop(socket.AF_INET, rdata))
        elif rtype == 28 and rdlen == 16:
            answers.append(socket.inet_ntop(socket.AF_INET6, rdata))
    out({"ok": rid == qid and (flags & 0xF) == 0, "stage": "dns", "rcode": flags & 0xF,
         "answers": answers, "ms": int((time.time() - start) * 1000)}, 0 if answers else 1)


def udp(args):
    start = time.time()
    fam = socket.AF_INET6 if ":" in args.host else socket.AF_INET
    s = socket.socket(fam, socket.SOCK_DGRAM)
    s.settimeout(args.timeout)
    try:
        s.sendto(args.payload.encode(), (args.host, args.port))
        data, _ = s.recvfrom(4096)
        out({"ok": True, "stage": "udp", "reply": data[:64].decode("latin1"), "ms": int((time.time() - start) * 1000)})
    except socket.timeout:
        out({"ok": False, "stage": "udp", "error": "timeout", "ms": int((time.time() - start) * 1000)}, 1)
    except OSError as e:
        out({"ok": False, "stage": "udp", "error": str(e), "ms": int((time.time() - start) * 1000)}, 1)
    finally:
        s.close()


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)

    t = sub.add_parser("tcp")
    t.add_argument("--host", required=True)
    t.add_argument("--port", type=int, required=True)
    t.add_argument("--mode", choices=["tls", "http", "echo"], default="echo")
    t.add_argument("--sni")
    t.add_argument("--host-header", default="canary-http-r16a.invalid")
    t.add_argument("--timeout", type=float, default=5)
    t.set_defaults(func=tcp)

    d = sub.add_parser("dns")
    d.add_argument("--server", required=True)
    d.add_argument("--name", required=True)
    d.add_argument("--qtype", choices=["A", "AAAA"], default="A")
    d.add_argument("--tcp", action="store_true")
    d.add_argument("--timeout", type=float, default=3)
    d.set_defaults(func=dns)

    u = sub.add_parser("udp")
    u.add_argument("--host", required=True)
    u.add_argument("--port", type=int, required=True)
    u.add_argument("--payload", default="RLSPIKE-UDP")
    u.add_argument("--timeout", type=float, default=2)
    u.set_defaults(func=udp)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
