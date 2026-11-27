#!/usr/bin/env python3
"""Locate the RouteLatch tray icon in a panel-window capture and keep a tight crop.

The product delivers a 22x22 logical IconPixmap. On this session the Plasma
scale factor is 1.25, so the panel renders it at about 27-28 physical pixels.
Candidate scales are matched by mean absolute error against the reference
pixmap composited over the *local* panel background (estimated from a ring
around the candidate, because the panel and tray area can use different
colours). Only a crop with a background-only border is accepted. The full
panel capture never leaves the scratch directory.
"""
import json
import sys
from collections import Counter

import numpy as np
from PIL import Image

PANEL, REF, OUT, META = sys.argv[1:5]
panel = Image.open(PANEL).convert("RGB")
a = np.asarray(panel).astype(np.int16)
height, width, _ = a.shape
global_bg = np.array(Counter(map(tuple, a.reshape(-1, 3))).most_common(1)[0][0], dtype=np.float64)
ref = Image.open(REF).convert("RGBA")


def match(bg):
    best = None
    for size in range(25, 31):
        r = np.asarray(ref.resize((size, size), Image.LANCZOS)).astype(np.float64)
        alpha = r[:, :, 3:4] / 255.0
        comp = r[:, :, :3] * alpha + bg * (1.0 - alpha)
        for y in range(0, height - size + 1):
            for x in range(width // 2, width - size + 1):
                mae = np.abs(a[y:y + size, x:x + size].astype(np.float64) - comp).mean()
                if best is None or mae < best[0]:
                    best = (mae, x, y, size)
    return best


def local_bg(x, y, size):
    x0, y0 = max(0, x - 5), max(0, y - 5)
    x1, y1 = min(width, x + size + 5), min(height, y + size + 5)
    region = a[y0:y1, x0:x1]
    mask = np.ones(region.shape[:2], dtype=bool)
    mask[max(0, y - y0):min(region.shape[0], y + size - y0),
         max(0, x - x0):min(region.shape[1], x + size - x0)] = False
    ring = region[mask]
    return np.array(Counter(map(tuple, ring)).most_common(1)[0][0], dtype=np.float64)


first = match(global_bg)
bg = local_bg(first[1], first[2], first[3])
mae, x, y, size = match(bg)

for margin in (6, 5, 4, 3, 2):
    x0, y0 = max(0, x - margin), max(0, y - margin)
    x1, y1 = min(width, x + size + margin), min(height, y + size + margin)
    crop = a[y0:y1, x0:x1]
    border = np.concatenate([crop[0], crop[-1], crop[:, 0], crop[:, -1]])
    near = np.all(np.abs(border - bg) <= 10, axis=1)
    if near.all():
        break

crop_img = panel.crop((x0, y0, x1, y1))
crop_img.save(OUT)
meta = {
    "panel": [width, height],
    "panel_bg_local": [int(v) for v in bg],
    "icon_origin": [x, y],
    "icon_size": size,
    "scale": round(size / 22.0, 4),
    "match_mae": round(float(mae), 3),
    "crop_box": [x0, y0, x1, y1],
    "crop_size": [x1 - x0, y1 - y0],
    "border_margin": margin,
    "border_background_only": bool(near.all()),
}
with open(META, "w", encoding="utf-8") as f:
    json.dump(meta, f, indent=2)
    f.write("\n")
print(json.dumps(meta))
