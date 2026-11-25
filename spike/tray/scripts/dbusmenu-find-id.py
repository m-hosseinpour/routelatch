#!/usr/bin/env python3
"""Inspect the text form of a `gdbus call ... GetLayout` reply.

The spike scripts need item ids to send com.canonical.dbusmenu.Event without
depending on the library's internal id assignment. gdbus prints each item as
`(<id>, {'prop': <value>, ...}, [children])`, so the id is recoverable from
the label the application set. This helper is evidence tooling only.
"""
import re
import sys

ITEM = re.compile(r"\((\d+), \{([^{}]*)")
LABEL = re.compile(r"'label': <'((?:[^'\\]|\\.)*)'>")


def items(text):
    for match in ITEM.finditer(text):
        label = LABEL.search(match.group(2))
        yield int(match.group(1)), label.group(1) if label else ""


def main():
    if len(sys.argv) < 2:
        print("usage: dbusmenu-find-id.py id LABEL | count-prefix PREFIX | count-all | has-label LABEL",
              file=sys.stderr)
        return 2
    command = sys.argv[1]
    value = sys.argv[2] if len(sys.argv) > 2 else ""
    text = sys.stdin.read()
    found = list(items(text))
    if command == "count-all":
        print(sum(1 for _ in found))
        return 0
    if len(sys.argv) != 3:
        print("usage: dbusmenu-find-id.py id LABEL | count-prefix PREFIX | count-all | has-label LABEL",
              file=sys.stderr)
        return 2
    if command == "id":
        for item_id, label in found:
            if label == value:
                print(item_id)
                return 0
        return 1
    if command == "count-prefix":
        print(sum(1 for _, label in found if label.startswith(value)))
        return 0
    if command == "has-label":
        return 0 if any(label == value for _, label in found) else 1
    print(f"unknown command {command!r}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
