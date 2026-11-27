#!/usr/bin/env python3
"""Visual-only fake control/status server + launcher for the real tray child.

The product tray child is executed unchanged; only its management peer is a
fake Unix socket that answers the documented ops with driver-supplied JSON.
No TUN, route, service, desktop or autostart state is touched.

Layout under BASE:
  fake-status.json   current `status` payload (driver rewrites atomically)
  fake-actions.json  {"enable": <tun object|null>, "disable": <tun object|null>}
  requests.log       one JSON line per management request
  control.log        tray -> supervisor lines (ready/error/quit/...)
  server.log         launcher/peer diagnostics
  run/control.sock   fake management socket (0600)
"""
import json
import os
import signal
import socketserver
import subprocess
import sys
import threading
import time

BASE = os.path.abspath(sys.argv[1])
BIN = sys.argv[2]
DATA = os.path.join(BASE, "data")
SOCK = os.path.join(BASE, "run", "control.sock")
STATE = os.path.join(BASE, "fake-status.json")
ACTIONS = os.path.join(BASE, "fake-actions.json")

os.makedirs(os.path.join(BASE, "run"), mode=0o700, exist_ok=True)
os.makedirs(DATA, mode=0o700, exist_ok=True)

log_lock = threading.Lock()


def log(path, text):
    with log_lock:
        with open(path, "a", encoding="utf-8") as f:
            f.write(text + "\n")


def read_json(path, default):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def write_json(path, value):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(value, f)
        f.write("\n")
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


class Handler(socketserver.StreamRequestHandler):
    def handle(self):
        line = self.rfile.readline(1 << 16)
        if not line:
            return
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            log(os.path.join(BASE, "server.log"), "bad request line")
            return
        op = req.get("op", "")
        log(os.path.join(BASE, "requests.log"), json.dumps({"ts": round(time.time(), 3), "op": op, "req": req}))
        status = read_json(STATE, {})
        data = {}
        if op == "status":
            data = status
        elif op == "rules.list":
            data = {"rules": []}
        elif op == "pool.list":
            data = {"records": [], "counts": {"total": 3, "usable": 3, "healthy": 3, "cooling": 0}, "truncated": False}
        elif op == "proposals.list":
            data = {"proposals": []}
        elif op == "desktop.status":
            data = {"supported": False, "applied": False, "previous_set": False}
        elif op == "env":
            data = {"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "NO_PROXY": "", "notice": ""}
        elif op == "config.get":
            data = {"path": os.path.join(BASE, "config.json"),
                    "values": {"http_listen": "127.0.0.1:59101", "socks_listen": "127.0.0.1:59102", "log_level": "info"}}
        elif op == "config.path":
            data = {"config": os.path.join(BASE, "config.json"),
                    "state": os.path.join(BASE, "state.json"),
                    "socket": SOCK}
        elif op in ("tun.enable", "tun.disable"):
            actions = read_json(ACTIONS, {})
            key = "enable" if op == "tun.enable" else "disable"
            result = actions.get(key)
            if result is None:
                # No transition configured: keep the current view.
                result = status.get("tun", {})
            status["tun"] = result
            write_json(STATE, status)
            log(os.path.join(BASE, "actions.log"), json.dumps({"ts": round(time.time(), 3), "op": op}))
            data = result
        else:
            data = {}
        env = {"schema_version": 1, "ok": True, "command": op, "data": data}
        self.wfile.write((json.dumps(env) + "\n").encode())
        self.wfile.flush()


class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True
    allow_reuse_address = True


def main():
    try:
        os.remove(SOCK)
    except FileNotFoundError:
        pass
    server = Server(SOCK, Handler)
    os.chmod(SOCK, 0o600)
    threading.Thread(target=server.serve_forever, daemon=True).start()

    pidfd = os.pidfd_open(os.getpid())
    control_r, control_w = os.pipe()
    event_r, event_w = os.pipe()
    logf = open(os.path.join(BASE, "tray.log"), "ab", buffering=0)
    env = dict(os.environ)
    env.update({
        "ROUTELATCH_TRAY_PARENT_PIDFD": str(pidfd),
        "ROUTELATCH_TRAY_CONTROL_FD": str(control_w),
        "ROUTELATCH_TRAY_EVENT_FD": str(event_r),
        "ROUTELATCH_PARENT_PID": str(os.getpid()),
        "ROUTELATCH_TRAY_SOCKET": SOCK,
        "ROUTELATCH_DATA_DIR": DATA,
        "ROUTELATCH_LOG_PATH": os.path.join(BASE, "tray.log"),
    })
    child = subprocess.Popen(
        [BIN, "__app-tray"],
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=logf,
        stderr=logf,
        pass_fds=(pidfd, control_w, event_r),
        cwd=BASE,
    )
    os.close(control_w)
    os.close(event_r)
    with open(os.path.join(BASE, "tray.pid"), "w") as f:
        f.write(str(child.pid) + "\n")

    stopping = False

    def stop(signum, frame):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    os.set_blocking(control_r, False)
    while not stopping and child.poll() is None:
        try:
            chunk = os.read(control_r, 4096)
        except BlockingIOError:
            chunk = b""
        except OSError:
            break
        if chunk:
            for line in chunk.decode("utf-8", "replace").splitlines():
                if line:
                    log(os.path.join(BASE, "control.log"), "%s %s" % (round(time.time(), 3), line))
        time.sleep(0.05)

    if child.poll() is None:
        child.terminate()
    try:
        child.wait(timeout=5)
    except subprocess.TimeoutExpired:
        child.kill()
    server.shutdown()
    server.server_close()
    try:
        os.remove(SOCK)
    except FileNotFoundError:
        pass
    log(os.path.join(BASE, "server.log"), "exit code=%s" % child.returncode)


if __name__ == "__main__":
    main()
