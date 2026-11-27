#!/usr/bin/env python3
"""Sample the human `routelatch tun status` output during a fail-open window.

Post-release verification helper (evidence scope only). Records exit code,
stdout and stderr for each timestamped call.
"""
import argparse
import json
import subprocess
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", required=True)
    p.add_argument("--data", required=True)
    p.add_argument("--duration", type=float, default=5)
    p.add_argument("--interval", type=float, default=0.005)
    p.add_argument("--timeout", type=float, default=0.7)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    end = time.time() + args.duration
    with open(args.out, "a", encoding="utf-8") as f:
        while time.time() < end:
            t = time.time()
            rec = {"t": t}
            try:
                r = subprocess.run(
                    [args.binary, "--data-dir", args.data, "tun", "status"],
                    capture_output=True, text=True, timeout=args.timeout,
                )
                rec["rc"] = r.returncode
                rec["stdout"] = (r.stdout or "").strip()[-400:]
                rec["stderr"] = (r.stderr or "").strip()[-400:]
            except subprocess.TimeoutExpired:
                rec["error"] = "timeout"
            except OSError as exc:
                rec["error"] = str(exc)
            rec["t_end"] = time.time()
            rec["ms"] = int((rec["t_end"] - t) * 1000)
            f.write(json.dumps(rec, sort_keys=True) + "\n")
            f.flush()
            time.sleep(args.interval)


if __name__ == "__main__":
    main()
