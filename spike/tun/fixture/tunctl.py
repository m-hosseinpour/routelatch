#!/usr/bin/env python3
"""Minimal client for the R16-A TUN service control socket."""
import argparse
import json
import socket
import sys


def main():
    p = argparse.ArgumentParser()
    p.add_argument("cmd", choices=["status", "enable", "disable"])
    p.add_argument("--socket", default="/run/routelatch-tun/control.sock")
    args = p.parse_args()

    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(15)
    try:
        s.connect(args.socket)
        s.sendall((json.dumps({"cmd": args.cmd}) + "\n").encode())
        data = b""
        while not data.endswith(b"\n"):
            chunk = s.recv(4096)
            if not chunk:
                break
            data += chunk
    finally:
        s.close()
    print(data.decode().strip())
    try:
        obj = json.loads(data)
    except ValueError:
        sys.exit(2)
    sys.exit(0 if obj.get("ok") else 1)


if __name__ == "__main__":
    main()
