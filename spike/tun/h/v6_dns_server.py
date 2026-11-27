#!/usr/bin/env python3
"""Controlled IPv6 plain-DNS resolver for the R16-H addendum.

Listens on a supplied IPv6 address at UDP/TCP port 53 and answers a fixed test
name. Each query is logged as one JSON line with protocol, source, qname, qtype
and a running datagram count so the caller can prove which interface carried it.
"""
import argparse
import json
import socket
import struct
import threading
import time

A4 = "203.0.113.7"
A6 = "2001:db8::7"


def dumps(obj):
    print(json.dumps(obj, sort_keys=True), flush=True)


def wire_name(name):
    return b"".join(bytes([len(p)]) + p.encode("ascii") for p in name.rstrip(".").split(".")) + b"\x00"


def parse_question(data):
    if len(data) < 12:
        return None
    qid, _flags, qd, _an, _ns, _ar = struct.unpack("!HHHHHH", data[:12])
    if qd != 1:
        return None
    off = 12
    labels = []
    while off < len(data):
        ln = data[off]
        if ln == 0:
            off += 1
            break
        if ln & 0xC0:
            return None
        labels.append(data[off + 1:off + 1 + ln].decode("ascii", "replace"))
        off += 1 + ln
    if off + 4 > len(data):
        return None
    qtype, qclass = struct.unpack("!HH", data[off:off + 4])
    return qid, ".".join(labels), qtype, qclass, off + 4


def response(data):
    q = parse_question(data)
    if q is None:
        return None
    qid, qname, qtype, qclass, qend = q
    rdata = None
    if qname.rstrip(".").lower() == "canary-h.invalid":
        if qtype == 1:
            rdata = socket.inet_pton(socket.AF_INET, A4)
        elif qtype == 28:
            rdata = socket.inet_pton(socket.AF_INET6, A6)
    an = 1 if rdata is not None else 0
    header = struct.pack("!HHHHHH", qid, 0x8180, 1, an, 0, 0)
    if rdata is None:
        return header + data[12:qend], qname, qtype
    answer = b"\xc0\x0c" + struct.pack("!HHIH", qtype, qclass, 60, len(rdata)) + rdata
    return header + data[12:qend] + answer, qname, qtype


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--listen", required=True)
    p.add_argument("--log", required=True)
    args = p.parse_args()

    lock = threading.Lock()
    counts = {"udp": 0, "tcp": 0}

    def log(event, **kw):
        rec = {"ts": time.time(), "event": event}
        rec.update(kw)
        with open(args.log, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec, sort_keys=True) + "\n")

    def bump(proto):
        with lock:
            counts[proto] += 1
            return counts[proto]

    def udp_loop():
        s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((args.listen, 53))
        log("listen", proto="udp", addr=args.listen)
        while True:
            data, addr = s.recvfrom(4096)
            n = bump("udp")
            out = response(data)
            log("query", proto="udp", source=addr[0], count=n,
                qname=None if out is None else out[1], qtype=None if out is None else out[2])
            if out is not None:
                s.sendto(out[0], addr)

    def tcp_loop():
        s = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((args.listen, 53))
        s.listen(8)
        log("listen", proto="tcp", addr=args.listen)
        while True:
            conn, addr = s.accept()
            with conn:
                hdr = conn.recv(2)
                if len(hdr) < 2:
                    continue
                ln = struct.unpack("!H", hdr)[0]
                data = b""
                while len(data) < ln:
                    chunk = conn.recv(ln - len(data))
                    if not chunk:
                        break
                    data += chunk
                n = bump("tcp")
                out = response(data)
                log("query", proto="tcp", source=addr[0], count=n,
                    qname=None if out is None else out[1], qtype=None if out is None else out[2])
                if out is not None:
                    conn.sendall(struct.pack("!H", len(out[0])) + out[0])

    threading.Thread(target=udp_loop, daemon=True).start()
    threading.Thread(target=tcp_loop, daemon=True).start()
    while True:
        time.sleep(1)


if __name__ == "__main__":
    main()
