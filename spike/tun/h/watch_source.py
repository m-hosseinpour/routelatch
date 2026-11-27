#!/usr/bin/env python3
"""R16-H timing probe: continuously observe a flow's peer-observed source.

Used around engine/TUN-service kills to measure fail-closed behaviour and the
short fail-open window. Each attempt is a new plain TCP connection, so an
existing pooled connection cannot hide a routing change.
"""
import argparse
import json
import socket
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--host", required=True)
    p.add_argument("--port", type=int, default=7110)
    p.add_argument("--duration", type=float, default=5)
    p.add_argument("--interval", type=float, default=0.02)
    p.add_argument("--timeout", type=float, default=0.6)
    p.add_argument("--out", required=True)
    args = p.parse_args()
    end = time.time() + args.duration
    with open(args.out, "w", encoding="utf-8") as out:
        while time.time() < end:
            t = time.time()
            rec = {"t": round(t, 6), "ok": False}
            try:
                s = socket.create_connection((args.host, args.port), timeout=args.timeout)
                s.settimeout(args.timeout)
                data = s.recv(1024)
                s.close()
                if data:
                    rec["ok"] = True
                    rec["raw"] = data.decode("ascii", "replace").strip()
                else:
                    rec["error"] = "eof"
            except OSError as exc:
                rec["error"] = str(exc)
            out.write(json.dumps(rec, sort_keys=True) + "\n")
            out.flush()
            time.sleep(args.interval)


if __name__ == "__main__":
    main()
