#!/usr/bin/env python3
"""Disposable R16-A SOCKS5 fixture.

It stands in for the RouteLatch engine's loopback SOCKS listener. It records
every CONNECT target so the harness can count TUN loop observations (a target
port that should have gone direct but appears here means the egress bypass
failed). It also maps the canary domains to the peer addresses so the sniffed
domain can be carried through the SOCKS request.

This fixture is test-only and deliberately logs destinations; product logs are
checked separately with the canary grep.
"""
import argparse
import json
import socket
import struct
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
STATE = {"total": 0, "by_port": {}, "entries": []}


def log_line(path, obj):
    with LOCK:
        with open(path, "a", encoding="utf-8") as f:
            f.write(json.dumps(obj, sort_keys=True) + "\n")


def record(target, port, atyp, mapped):
    with LOCK:
        STATE["total"] += 1
        STATE["by_port"][str(port)] = STATE["by_port"].get(str(port), 0) + 1
        STATE["entries"].append({
            "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "target": target,
            "port": port,
            "atyp": atyp,
            "mapped": mapped,
        })
        if len(STATE["entries"]) > 500:
            del STATE["entries"][:250]


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
        self.map = {
            "canary-r16a.invalid": args.peer4,
            "canary6-r16a.invalid": args.peer6,
            "canary-http-r16a.invalid": args.peer4,
        }

    def handle(self, conn, addr):
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

            mapped = self.map.get(host, host)
            log_line(self.args.log, {
                "event": "connect",
                "peer": f"{addr[0]}:{addr[1]}",
                "target": host,
                "port": port,
                "atyp": atyp,
                "mapped": mapped,
            })
            record(host, port, atyp, mapped)

            try:
                fam = socket.AF_INET6 if ":" in mapped else socket.AF_INET
                up = socket.socket(fam, socket.SOCK_STREAM)
                up.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE, self.args.device.encode() + b"\0")
                up.settimeout(8)
                up.connect((mapped, port))
            except OSError as e:
                log_line(self.args.log, {"event": "upstream_error", "target": host, "port": port, "error": str(e)})
                conn.sendall(b"\x05\x05\x00\x01\x00\x00\x00\x00\x00\x00")
                return

            conn.sendall(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
            self.relay(conn, up)
        except (OSError, ConnectionError):
            pass
        finally:
            try:
                conn.close()
            except OSError:
                pass

    @staticmethod
    def relay(a, b):
        # Blocking sockets in two threads; the earlier non-blocking variant
        # returned immediately on EAGAIN and closed every tunnel.
        a.settimeout(None)
        b.settimeout(None)

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

        t = threading.Thread(target=pump, args=(b, a), daemon=True)
        t.start()
        pump(a, b)
        t.join(timeout=30)


def status_server(args):
    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/reset":
                with LOCK:
                    STATE.update({"total": 0, "by_port": {}, "entries": []})
                body = b'{"ok":true}'
            elif self.path == "/counters":
                with LOCK:
                    body = json.dumps(STATE, sort_keys=True).encode()
            else:
                self.send_response(404)
                self.end_headers()
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    ThreadingHTTPServer(("127.0.0.1", args.status_port), H).serve_forever()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--listen", default="127.0.0.1")
    p.add_argument("--port", type=int, default=1080)
    p.add_argument("--status-port", type=int, default=18080)
    p.add_argument("--device", default="eth0")
    p.add_argument("--log", default="/opt/fixture/socks.log")
    p.add_argument("--peer4", default="172.31.77.20")
    p.add_argument("--peer6", default="fd00:77::20")
    args = p.parse_args()

    threading.Thread(target=status_server, args=(args,), daemon=True).start()
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
