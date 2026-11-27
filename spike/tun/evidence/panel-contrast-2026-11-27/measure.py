#!/usr/bin/env python3
"""Measure panel contrast for one RouteLatch tray-icon crop.

Inputs: crop PNG, reference IconPixmap PNG, semantic state name, output JSON.
The panel background is the modal crop colour (the crop border is validated as
background-only by locate_crop.py). Foreground colours are the exact product
colours from internal/app/tray.go. At the session's 1.25 scale a one-pixel ring
can be resampled to 239,246,252 instead of 240,246,252, so each expected colour
is matched within a tolerance of 4 and the observed modal colour is reported.
"""
import json
import math
import os
import sys
from collections import Counter

import numpy as np
from PIL import Image

CROP, REF, STATE, OUT = sys.argv[1:5]

ENGINE = (0x2F, 0x81, 0xF7)
WHITE = (0xFF, 0xFF, 0xFF)
BADGES = {
    "off": (0xF0, 0xF6, 0xFC),
    "on": (0x2E, 0xA0, 0x43),
    "starting": (0x58, 0xA6, 0xFF),
    "pending": (0x58, 0xA6, 0xFF),
    "error": (0xDB, 0x6D, 0x28),
    "conflict": (0xDB, 0x6D, 0x28),
    "unknown": None,
    "incompatible": (0xF0, 0xF6, 0xFC),
}
TOL = 4


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


def lab(rgb):
    r, g, b = (lin(float(v)) for v in rgb)
    x = (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047
    y = 0.2126 * r + 0.7152 * g + 0.0722 * b
    z = (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883

    def f(t):
        return t ** (1 / 3) if t > 0.008856 else 7.787 * t + 16 / 116

    fx, fy, fz = f(x), f(y), f(z)
    return 116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)


def de76(a, b):
    return math.sqrt(sum((x - y) ** 2 for x, y in zip(lab(a), lab(b))))


crop = np.asarray(Image.open(CROP).convert("RGB"))
Image.open(REF).convert("RGBA")  # existence/readability check for the reference
bg = tuple(int(v) for v in Counter(map(tuple, crop.reshape(-1, 3))).most_common(1)[0][0])
badge = BADGES.get(STATE, BADGES["unknown"])

colors = {"engine": ENGINE, "mark": WHITE}
if badge is not None:
    colors["badge"] = badge

rows = []
for name, nominal in colors.items():
    n = np.array(nominal, dtype=np.int16)
    mask = np.all(np.abs(crop.astype(np.int16) - n) <= TOL, axis=2)
    count = int(mask.sum())
    observed = None
    if count:
        observed = tuple(int(v) for v in Counter(map(tuple, crop[mask])).most_common(1)[0][0])
    rgb = observed if observed is not None else nominal
    rows.append({
        "element": name,
        "nominal_rgb": list(nominal),
        "nominal_hex": "#%02x%02x%02x" % nominal,
        "observed_rgb": list(rgb),
        "observed_hex": "#%02x%02x%02x" % rgb,
        "pixels_within_tolerance": count,
        "contrast_vs_panel_bg": round(ratio(rgb, bg), 2),
    })

found = all(r["pixels_within_tolerance"] > 0 for r in rows)
core = min(r["contrast_vs_panel_bg"] for r in rows if r["pixels_within_tolerance"] > 0)
result = {
    "state": STATE,
    "crop": os.path.basename(CROP),
    "crop_size": [int(crop.shape[1]), int(crop.shape[0])],
    "panel_bg_rgb": list(bg),
    "panel_bg_hex": "#%02x%02x%02x" % bg,
    "elements": rows,
    "all_expected_colours_present": found,
    "min_core_contrast": core,
    "threshold_non_text": 3.0,
    "pass": bool(found and core >= 3.0),
    "badge_rgb": list(badge) if badge else None,
    "badge_vs_engine_contrast": round(ratio(badge, ENGINE), 2) if badge else None,
    "badge_vs_engine_delta_e76": round(de76(badge, ENGINE), 1) if badge else None,
}
with open(OUT, "w", encoding="utf-8") as f:
    json.dump(result, f, indent=2)
    f.write("\n")
print("%s bg=%s min=%.2f found=%s pass=%s" % (STATE, result["panel_bg_hex"], core, found, result["pass"]))
