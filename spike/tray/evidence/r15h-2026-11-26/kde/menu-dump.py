#!/usr/bin/env python3
"""Dump the text form of a `gdbus ... GetLayout` reply, one item per line.

Evidence tooling only: id|enabled|visible|toggle-type|toggle-state|label
"""
import re
import sys

ITEM = re.compile(r"\((\d+), \{([^{}]*)\}")
LABEL = re.compile(r"'label': <'((?:[^'\\]|\\.)*)'>")


def prop(body, key):
    match = re.search(r"'" + key + r"': <([^>]*)>", body)
    if not match:
        return ""
    return match.group(1).strip("'")


def main():
    text = sys.stdin.read()
    for match in ITEM.finditer(text):
        item_id, body = match.group(1), match.group(2)
        label = LABEL.search(body)
        print("|".join([
            item_id,
            prop(body, "enabled"),
            prop(body, "visible"),
            prop(body, "toggle-type"),
            prop(body, "toggle-state"),
            label.group(1) if label else "",
        ]))


if __name__ == "__main__":
    main()
