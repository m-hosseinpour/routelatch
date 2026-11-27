#!/usr/bin/env python3
"""Aggregate the per-state contrast measurements into one report table."""
import json
import sys
from pathlib import Path

EV = Path(sys.argv[1])
ORDER = [
    ("real", "off-helper-missing", "off (real, helper missing)"),
    ("real", "error-desired-missing", "error (real, desired on / helper missing)"),
    ("fake", "off", "off (fake)"),
    ("fake", "error", "error (fake)"),
    ("fake", "on", "on (fake)"),
    ("fake", "starting", "starting (fake)"),
    ("fake", "conflict", "conflict (fake)"),
]
STATES = []

for group, tag, label in ORDER:
    path = EV / group / ("contrast-%s.json" % tag)
    if not path.exists():
        continue
    d = json.loads(path.read_text())
    d["label"] = label
    d["group"] = group
    d["tag"] = tag
    STATES.append(d)

overall = bool(STATES) and all(s["pass"] for s in STATES)
summary = {
    "threshold_non_text": 3.0,
    "session": {},
    "states": STATES,
    "overall_pass": overall,
}
session_file = EV / "session.txt"
if session_file.exists():
    for line in session_file.read_text().splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            summary["session"][k] = v
(EV / "contrast.json").write_text(json.dumps(summary, indent=2) + "\n")

lines = [
    "| state | panel bg | engine | mark | badge | min core | pass |",
    "| --- | --- | --- | --- | --- | --- | --- |",
]
for s in STATES:
    el = {e["element"]: e for e in s["elements"]}
    def cell(name):
        e = el.get(name)
        if not e:
            return "-"
        return "%s %.2f:1" % (e["observed_hex"], e["contrast_vs_panel_bg"])
    lines.append("| %s | %s | %s | %s | %s | %.2f:1 | %s |" % (
        s["label"], s["panel_bg_hex"], cell("engine"), cell("mark"), cell("badge"),
        s["min_core_contrast"], "PASS" if s["pass"] else "FAIL"))
lines.append("")
lines.append("Overall: **%s** (WCAG non-text threshold 3:1)." % ("PASS" if overall else "FAIL"))
(EV / "contrast.md").write_text("\n".join(lines) + "\n")
print("\n".join(lines))
print("overall", "PASS" if overall else "FAIL")
