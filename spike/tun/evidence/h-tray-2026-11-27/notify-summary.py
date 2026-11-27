#!/usr/bin/env python3
"""Extract org.freedesktop.Notifications Notify bodies from a dbus-monitor log.

Usage: notify-summary.py <notifications.log>
Output: one Python-dict line per Notify call, e.g.
  {'app': 'RouteLatch', 'summary': 'RouteLatch', 'body': 'System-wide coverage off'}
`dbus-monitor` prints the arguments as indented `string "..."` lines; the
desktop-entry hint follows later and is not read. Evidence tooling only.
"""
import re
import sys


def main():
    lines = open(sys.argv[1], encoding="utf-8", errors="replace").read().splitlines()
    out = []
    for i, line in enumerate(lines):
        if "member=Notify" not in line:
            continue
        values = []
        for follow in lines[i + 1:i + 30]:
            match = re.match(r'\s+string "(.*)"$', follow)
            if match:
                values.append(match.group(1))
            if len(values) == 4:
                break
        if len(values) >= 4:
            out.append(repr({"app": values[0], "summary": values[2], "body": values[3]}))
    print("\n".join(out))


if __name__ == "__main__":
    main()
