#!/usr/bin/env python3
"""R16-H application probe: connect through TUN and print the peer-observed source."""
import argparse
import json
import socket
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--host", required=True)
    p.add_argument("--port", type=int, default=7110)
    p.add_argument("--timeout", type=float, default=4)
    args = p.parse_args()
    start = time.time()
    try:
        s = socket.create_connection((args.host, args.port), timeout=args.timeout)
        s.settimeout(args.timeout)
        data = s.recv(1024)
        s.close()
        if not data:
            print(json.dumps({"ok": False, "error": "eof",
                              "ms": int((time.time() - start) * 1000)}, sort_keys=True))
            return
        print(json.dumps({"ok": True, "raw": data.decode("ascii", "replace").strip(),
                          "ms": int((time.time() - start) * 1000)}, sort_keys=True))
    except OSError as exc:
        print(json.dumps({"ok": False, "error": str(exc),
                          "ms": int((time.time() - start) * 1000)}, sort_keys=True))


if __name__ == "__main__":
    main()
