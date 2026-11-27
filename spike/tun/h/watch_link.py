#!/usr/bin/env python3
"""R16-H link watcher: record rl-tun0 presence transitions with timestamps."""
import argparse
import json
import subprocess
import time


def present():
    return subprocess.run(["ip", "link", "show", "rl-tun0"], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL).returncode == 0


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--duration", type=float, default=5)
    p.add_argument("--interval", type=float, default=0.005)
    p.add_argument("--out", required=True)
    args = p.parse_args()
    end = time.time() + args.duration
    last = None
    with open(args.out, "w", encoding="utf-8") as out:
        while time.time() < end:
            now = present()
            if now != last:
                out.write(json.dumps({"t": time.time(), "present": now}) + "\n")
                out.flush()
                last = now
            time.sleep(args.interval)


if __name__ == "__main__":
    main()
