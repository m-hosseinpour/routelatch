#!/usr/bin/env python3
"""Sample `routelatch tun status --json` and record every contract field.

Post-release verification helper (evidence scope only). Each line is one
timestamped call, including calls that time out while the TUN helper restarts.
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
                    [args.binary, "--json", "--data-dir", args.data, "tun", "status"],
                    capture_output=True, text=True, timeout=args.timeout,
                )
                rec["rc"] = r.returncode
                env = None
                for line in reversed((r.stdout or "").strip().splitlines()):
                    if line.strip().startswith("{"):
                        try:
                            env = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        break
                if env is None:
                    rec["error"] = "no-json"
                    rec["stdout_tail"] = (r.stdout or "")[-200:]
                    rec["stderr_tail"] = (r.stderr or "")[-200:]
                else:
                    data = env.get("data") or {}
                    if isinstance(data, dict) and "tun" in data:
                        data = data["tun"]
                    rec.update({
                        "ok": env.get("ok"),
                        "state": data.get("state"),
                        "desired": data.get("desired"),
                        "helper": data.get("helper"),
                        "interface": data.get("interface"),
                        "last_error": data.get("last_error"),
                        "detail": data.get("detail"),
                    })
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
