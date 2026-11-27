#!/usr/bin/env python3
"""Dump a dbusmenu layout through python-dbus (exact labels, no text parsing).

Usage: menu-dump2.py <bus-name> [parent-id] [depth]
Output: id|enabled|visible|toggle-type|toggle-state|label, depth-first.
Evidence tooling only.
"""
import sys

import dbus

DEFAULT_DEPTH = -1


def walk(node, out):
    item_id, props, children = node
    out.append("|".join([
        str(item_id),
        str(bool(props.get("enabled", True))).lower(),
        str(bool(props.get("visible", True))).lower(),
        str(props.get("toggle-type", "")),
        str(props.get("toggle-state", "")),
        str(props.get("label", "")),
    ]))
    for child in children:
        walk(child, out)


def main():
    bus_name = sys.argv[1]
    parent = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    depth = int(sys.argv[3]) if len(sys.argv) > 3 else DEFAULT_DEPTH
    bus = dbus.SessionBus()
    obj = bus.get_object(bus_name, "/StatusNotifierMenu")
    iface = dbus.Interface(obj, "com.canonical.dbusmenu")
    _, root = iface.GetLayout(dbus.UInt32(parent), depth, dbus.Array([], signature="s"))
    out = []
    walk(root, out)
    print("\n".join(out))


if __name__ == "__main__":
    main()
