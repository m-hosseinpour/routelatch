#!/usr/bin/env python3
"""Sample the engine status while a TUN fail-open window is measured.

Each line is one timestamped observation of the same status fields the tray
consumes (tun.state/last_error/warnings/ready/listeners_open). The script keeps
sampling even when the management call blocks or the engine is restarting.
"""
import argparse
import json
import subprocess
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--duration", type=float, default=8)
    p.add_argument("--interval", type=float, default=0.02)
    p.add_argument("--timeout", type=float, default=1.5)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    end = time.time() + args.duration
    while time.time() < end:
        t = time.time()
        rec = {"t": t}
        try:
            r = subprocess.run(
                [args.binary, "--json", "--data-dir", args.data, "status"],
                capture_output=True, text=True, timeout=args.timeout,
            )
            rec["rc"] = r.returncode
            env = None
            for line in reversed((r.stdout or "").strip().splitlines()):
                if line.strip().startswith("{"):
                    try:
                        env = json.loads(line)
                    except json.JSONDecodeError:
                        pass
                    break
            if env is None:
                rec["error"] = "no-json"
                rec["stderr"] = (r.stderr or "")[-200:]
            else:
                data = env.get("data") or {}
                tun = data.get("tun") or {}
                rec.update({
                    "ok": env.get("ok"),
                    "ready": data.get("ready"),
                    "listeners_open": data.get("listeners_open"),
                    "mode": data.get("mode"),
                    "tun_state": tun.get("state"),
                    "tun_last_error": tun.get("last_error"),
                    "tun_detail": tun.get("detail"),
                    "warnings": data.get("warnings"),
                })
        except subprocess.TimeoutExpired:
            rec["error"] = "timeout"
        except OSError as exc:
            rec["error"] = str(exc)
        with open(args.out, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec, sort_keys=True) + "\n")
        time.sleep(args.interval)


if __name__ == "__main__":
    main()
