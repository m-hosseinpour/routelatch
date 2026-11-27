#!/usr/bin/env python3
"""R16-H peer fixture: report the source address the peer actually observed.

The application side connects with a plain socket through TUN and reads the
JSON line. A source of the engine's physical address means the flow left
RouteLatch directly; a source of the peer address means it arrived through the
SOCKS exit fixture. Every accept is also appended to a log so a "no direct
fallback" check can compare counts before and after an engine kill.

Test-only: this fixture is allowed to observe and record destinations.
"""
import argparse
import json
import socket
import threading
import time

LOCK = threading.Lock()


def log(path, obj):
    with LOCK:
        with open(path, "a", encoding="utf-8") as f:
            f.write(json.dumps(obj, sort_keys=True) + "\n")


def serve(host, port, family, log_path):
    s = socket.socket(family, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if family == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((host, port))
    s.listen(64)
    while True:
        conn, addr = s.accept()
        threading.Thread(target=handle, args=(conn, addr, log_path, family), daemon=True).start()


def handle(conn, addr, log_path, family):
    try:
        local = conn.getsockname()
        body = json.dumps({
            "peer": addr[0],
            "local": local[0],
            "port": addr[1],
            "family": family.name,
            "ts": time.time(),
        }, sort_keys=True)
        log(log_path, {"event": "accept", "peer": addr[0], "local": local[0], "family": family.name})
        conn.sendall((body + "\n").encode())
    except OSError as exc:
        log(log_path, {"event": "error", "error": str(exc)})
    finally:
        try:
            conn.close()
        except OSError:
            pass


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=7110)
    p.add_argument("--log", default="/work/logs/source.log")
    args = p.parse_args()
    threading.Thread(target=serve, args=("0.0.0.0", args.port, socket.AF_INET, args.log), daemon=True).start()
    threading.Thread(target=serve, args=("::", args.port, socket.AF_INET6, args.log), daemon=True).start()
    log(args.log, {"event": "ready", "port": args.port})
    threading.Event().wait()


if __name__ == "__main__":
    main()
