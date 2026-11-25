#!/usr/bin/env python3
"""Extract host calls and item signals from a dbus-monitor capture.

Usage:
  dbusmenu-monitor.py host-calls LOG SENDER DEST SINCE
  dbusmenu-monitor.py count-calls LOG SENDER DEST SINCE MEMBER
  dbusmenu-monitor.py menu-events LOG SENDER DEST SINCE
  dbusmenu-monitor.py signals    LOG SENDER      SINCE
  dbusmenu-monitor.py latency    LOG SENDER DEST SINCE MEMBER

LOG is a dbus-monitor text file. SENDER and DEST are unique or well-known bus
names; DEST may be a '|'-separated list. SINCE is an epoch second from
`date +%s.%N`. Each matching line prints an epoch timestamp and member, so a
verdict in MENU-FINDINGS.md can cite the exact monitor line.
"""
import re
import sys

CALL = re.compile(
    r"^method call time=([0-9.]+) sender=(\S+) -> destination=(\S+) .*?member=(\w+)"
)
SIGNAL = re.compile(
    r"^signal time=([0-9.]+) sender=(\S+) -> destination=.*?interface=com\.canonical\.dbusmenu; member=(\w+)"
)


def calls(path):
    with open(path, encoding="utf-8", errors="replace") as handle:
        for line in handle:
            match = CALL.match(line)
            if match:
                yield float(match.group(1)), match.group(2), match.group(3), match.group(4)


def signals(path):
    with open(path, encoding="utf-8", errors="replace") as handle:
        for line in handle:
            match = SIGNAL.match(line)
            if match:
                yield float(match.group(1)), match.group(2), match.group(3)


def events(path):
    lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
    index = 0
    while index < len(lines):
        match = CALL.match(lines[index])
        if not match or match.group(4) != "Event":
            index += 1
            continue
        ident = None
        name = None
        index += 1
        while index < len(lines) and lines[index].startswith(" "):
            arg = re.match(r"\s+int32 (\d+)$", lines[index])
            if arg and ident is None:
                ident = int(arg.group(1))
            arg = re.match(r'\s+string "(.*)"$', lines[index])
            if arg and name is None:
                name = arg.group(1)
            index += 1
        if ident is not None and name is not None:
            yield float(match.group(1)), match.group(2), match.group(3), ident, name


def main():
    if len(sys.argv) < 5:
        print(__doc__, file=sys.stderr)
        return 2
    mode, path, sender = sys.argv[1], sys.argv[2], sys.argv[3]
    if mode == "host-calls":
        dests = set(sys.argv[4].split("|"))
        since = float(sys.argv[5])
        for when, who, dest, member in calls(path):
            if who == sender and dest in dests and when >= since:
                print(f"host_call time={when:.6f} member={member}")
        return 0
    if mode == "count-calls":
        dests = set(sys.argv[4].split("|"))
        since = float(sys.argv[5])
        wanted = sys.argv[6]
        print(sum(1 for when, who, dest, member in calls(path)
                  if who == sender and dest in dests and member == wanted and when >= since))
        return 0
    if mode == "menu-events":
        dests = set(sys.argv[4].split("|"))
        since = float(sys.argv[5])
        for when, who, dest, ident, name in events(path):
            if who == sender and dest in dests and when >= since:
                print(f"menu_event time={when:.6f} id={ident} event={name}")
        return 0
    if mode == "signals":
        since = float(sys.argv[4])
        for when, who, member in signals(path):
            if who == sender and when >= since:
                print(f"item_signal time={when:.6f} member={member}")
        return 0
    if mode == "latency":
        dests = set(sys.argv[4].split("|"))
        since = float(sys.argv[5])
        wanted = sys.argv[6]
        for when, who, dest, member in calls(path):
            if who == sender and dest in dests and member == wanted and when >= since:
                print(f"{(when - since) * 1000:.3f}")
                return 0
        print("none")
        return 0
    print(f"unknown mode {mode}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
