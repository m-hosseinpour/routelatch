#!/usr/bin/env python3
"""Synthetic light-panel composite of the evidence icons.

The committed crops are opaque captures over the dark panel (#202326), so
re-compositing the crop pixels directly would carry the dark background into
the light image. This script therefore uses the retained 22x22 IconPixmap PNGs
(the exact D-Bus source rendered into those crops) at the same 1.25 scale, and
composites them over representative Breeze light backgrounds.

Ratios use the unchanged product core colours. At 28 px the one-pixel off ring
and badge edges are heavily resampled, so exact-core counts are reported but are
not required for the ratio. This is synthetic evidence: not a light-theme run.
"""
import json
from collections import Counter
from pathlib import Path

import numpy as np
from PIL import Image

EV = Path(__file__).resolve().parent.parent
OUT = Path(__file__).resolve().parent
BACKGROUNDS = {
    "eff0f1": (0xEF, 0xF0, 0xF1),  # Breeze Light window/panel-like background
    "fcfcfc": (0xFC, 0xFC, 0xFC),  # near-white upper bound
}
STATES = [
    ("real-off-missing", "real off / helper missing", EV / "real/icon-off-helper-missing.png", "off"),
    ("real-error-desired", "real error / desired on", EV / "real/icon-error-desired-missing.png", "error"),
    ("fake-off", "fake off", EV / "fake/icon-off.png", "off"),
    ("fake-error", "fake error", EV / "fake/icon-error.png", "error"),
    ("fake-on", "fake on", EV / "fake/icon-on.png", "on"),
    ("fake-starting", "fake starting", EV / "fake/icon-starting.png", "starting"),
    ("fake-conflict", "fake conflict", EV / "fake/icon-conflict.png", "conflict"),
]
ENGINE = (0x2F, 0x81, 0xF7)
WHITE = (0xFF, 0xFF, 0xFF)
BADGES = {
    "off": (0xF0, 0xF6, 0xFC),
    "on": (0x2E, 0xA0, 0x43),
    "starting": (0x58, 0xA6, 0xFF),
    "error": (0xDB, 0x6D, 0x28),
    "conflict": (0xDB, 0x6D, 0x28),
}
TOL = 4
SIZE = 28  # 22 logical px at the session scale factor 1.25
MARGIN = 5
THRESHOLD = 3.0


def lin(c):
    c = c / 255.0
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def luminance(rgb):
    r, g, b = (lin(float(v)) for v in rgb)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def ratio(fg, bg):
    a, b = luminance(fg), luminance(bg)
    hi, lo = max(a, b), min(a, b)
    return (hi + 0.05) / (lo + 0.05)


rows = []
for bg_tag, bg in BACKGROUNDS.items():
    bg_arr = np.array(bg, dtype=np.uint8)
    for tag, label, icon_path, state in STATES:
        ref = Image.open(icon_path).convert("RGBA")
        # Composite over the light background at the source size, then resample
        # to the panel's rendered size. This keeps the edge blend light instead
        # of carrying transparent-black pixels into a dark fringe.
        base = Image.alpha_composite(Image.new("RGBA", ref.size, tuple(bg) + (255,)), ref).convert("RGB")
        comp = np.asarray(base.resize((SIZE, SIZE), Image.LANCZOS)).astype(np.float64)
        canvas = np.empty((SIZE + 2 * MARGIN, SIZE + 2 * MARGIN, 3), dtype=np.uint8)
        canvas[:, :] = bg_arr
        canvas[MARGIN:MARGIN + SIZE, MARGIN:MARGIN + SIZE] = np.rint(comp).astype(np.uint8)
        name = "composite-%s-%s.png" % (bg_tag, tag)
        Image.fromarray(canvas).save(OUT / name)

        elements = {"engine": ENGINE, "mark": WHITE, "badge": BADGES[state]}
        state_rows = []
        for element, nominal in elements.items():
            n = np.array(nominal, dtype=np.int16)
            exact = int(np.all(canvas == n, axis=2).sum())
            near_mask = np.all(np.abs(canvas.astype(np.int16) - n) <= TOL, axis=2)
            near = int(near_mask.sum())
            observed = None
            if exact:
                observed = list(nominal)
            state_rows.append({
                "element": element,
                "nominal_rgb": list(nominal),
                "nominal_hex": "#%02x%02x%02x" % nominal,
                "observed_exact": observed,
                "exact_pixels": exact,
                "pixels_within_tolerance": near,
                "contrast_vs_background": round(ratio(nominal, bg), 2),
                "pass": bool(ratio(nominal, bg) >= THRESHOLD),
            })
        rows.append({
            "background": bg_tag,
            "background_hex": "#%02x%02x%02x" % bg,
            "state_tag": tag,
            "state_label": label,
            "state": state,
            "composite": name,
            "elements": state_rows,
            "min_core_contrast": min(r["contrast_vs_background"] for r in state_rows),
            "pass": all(r["pass"] for r in state_rows),
        })

result = {
    "synthetic": True,
    "real_light_theme_run": False,
    "note": "IconPixmap composited over light backgrounds; not a desktop/theme run.",
    "scale": 1.25,
    "rendered_size_px": SIZE,
    "threshold_non_text": THRESHOLD,
    "backgrounds": [{"tag": k, "hex": "#%02x%02x%02x" % v, "rgb": list(v)} for k, v in BACKGROUNDS.items()],
    "states": rows,
    "overall_pass": all(r["pass"] for r in rows),
}
(OUT / "light-contrast.json").write_text(json.dumps(result, indent=2) + "\n")

lines = [
    "| background | state | engine | white mark | badge | min | result |",
    "| --- | --- | --- | --- | --- | --- | --- |",
]
for r in rows:
    e = {x["element"]: x for x in r["elements"]}
    lines.append("| %s | %s | %s %.2f:1 | %s %.2f:1 | %s %.2f:1 | %.2f:1 | %s |" % (
        r["background_hex"], r["state_label"],
        e["engine"]["nominal_hex"], e["engine"]["contrast_vs_background"],
        e["mark"]["nominal_hex"], e["mark"]["contrast_vs_background"],
        e["badge"]["nominal_hex"], e["badge"]["contrast_vs_background"],
        r["min_core_contrast"], "PASS" if r["pass"] else "FAIL"))
lines.append("")
lines.append("Synthetic check only; ratios use product core colours, not a real light-theme run.")
lines.append("White mark is enclosed by the blue body (white-vs-blue 3.75:1); the ratios above are direct foreground-versus-light-background values.")
(OUT / "light-contrast.md").write_text("\n".join(lines) + "\n")
print("\n".join(lines))
