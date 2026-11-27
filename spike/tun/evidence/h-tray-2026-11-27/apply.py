#!/usr/bin/env python3
"""Copy one named fixture from states.json into the fake server's two files.

Usage: apply.py states.json BASE NAME
Writes BASE/fake-status.json and BASE/fake-actions.json atomically.
"""
import json
import os
import sys


def write(path, value):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(value, f)
        f.write("\n")
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


def main():
    states_path, base, name = sys.argv[1], sys.argv[2], sys.argv[3]
    with open(states_path, encoding="utf-8") as f:
        states = json.load(f)
    if name not in states:
        sys.exit("apply: unknown state %r" % name)
    entry = states[name]
    write(os.path.join(base, "fake-status.json"), entry["status"])
    write(os.path.join(base, "fake-actions.json"), {"enable": entry.get("enable"), "disable": entry.get("disable")})


if __name__ == "__main__":
    main()
