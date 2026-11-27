#!/usr/bin/env python3
"""R16-I test-only SOCKS5 exit that logs the CONNECT target and the first bytes.

The first bytes are what a real exit can observe before relaying. For DoH they
are a TLS ClientHello; for DNS-over-TCP they contain the plaintext query name.
The fixture is the only component allowed to record test destinations.
"""
import argparse
import json
import socket
import struct
import threading
import time

LOCK = threading.Lock()


def log_line(path, obj):
    with LOCK:
        with open(path, "a", encoding="utf-8") as f:
            f.write(json.dumps(obj, sort_keys=True) + "\n")


def recv_exact(sock, n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("eof")
        buf += chunk
    return buf


class Fixture:
    def __init__(self, args):
        self.args = args

    def handle(self, conn, addr):
        target = None
        try:
            conn.settimeout(10)
            ver, nmethods = recv_exact(conn, 2)
            if ver != 5:
                return
            recv_exact(conn, nmethods)
            conn.sendall(b"\x05\x00")
            ver, cmd, _rsv, atyp = recv_exact(conn, 4)
            if ver != 5:
                return
            if atyp == 1:
                host = socket.inet_ntop(socket.AF_INET, recv_exact(conn, 4))
            elif atyp == 3:
                ln = recv_exact(conn, 1)[0]
                host = recv_exact(conn, ln).decode("ascii", "replace")
            elif atyp == 4:
                host = socket.inet_ntop(socket.AF_INET6, recv_exact(conn, 16))
            else:
                conn.sendall(b"\x05\x08\x00\x01\x00\x00\x00\x00\x00\x00")
                return
            port = struct.unpack("!H", recv_exact(conn, 2))[0]
            if cmd != 1:
                conn.sendall(b"\x05\x07\x00\x01\x00\x00\x00\x00\x00\x00")
                return
            target = host
            try:
                fam = socket.AF_INET6 if ":" in host else socket.AF_INET
                up = socket.socket(fam, socket.SOCK_STREAM)
                up.settimeout(8)
                up.connect((host, port))
            except OSError as exc:
                log_line(self.args.log, {"event": "upstream_error", "target": host, "port": port, "error": str(exc)})
                conn.sendall(b"\x05\x05\x00\x01\x00\x00\x00\x00\x00\x00")
                return
            conn.sendall(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
            conn.settimeout(1.0)
            try:
                first = conn.recv(64)
            except OSError:
                first = b""
            conn.settimeout(None)
            log_line(self.args.log, {
                "event": "connect", "peer": f"{addr[0]}:{addr[1]}",
                "target": host, "port": port, "atyp": atyp,
                "first_hex": first.hex(), "first_ascii": "".join(chr(b) if 32 <= b < 127 else "." for b in first),
                "first_len": len(first), "ts": time.time(),
            })
            if first:
                up.sendall(first)

            def pump(src, dst):
                try:
                    while True:
                        data = src.recv(65536)
                        if not data:
                            try:
                                dst.shutdown(socket.SHUT_WR)
                            except OSError:
                                pass
                            return
                        dst.sendall(data)
                except OSError:
                    return

            t = threading.Thread(target=pump, args=(up, conn), daemon=True)
            t.start()
            pump(conn, up)
            t.join(timeout=30)
            up.close()
        except (OSError, ConnectionError):
            pass
        finally:
            try:
                conn.close()
            except OSError:
                pass


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--listen", default="0.0.0.0")
    p.add_argument("--port", type=int, default=1080)
    p.add_argument("--log", default="/work/logs/socks.log")
    args = p.parse_args()
    fx = Fixture(args)
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((args.listen, args.port))
    srv.listen(128)
    log_line(args.log, {"event": "listening", "addr": f"{args.listen}:{args.port}"})
    while True:
        conn, addr = srv.accept()
        threading.Thread(target=fx.handle, args=(conn, addr), daemon=True).start()


if __name__ == "__main__":
    main()
