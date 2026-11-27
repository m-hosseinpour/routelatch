#!/usr/bin/env python3
"""Decode a StatusNotifierItem IconPixmap property reply from `gdbus call`.

Usage: gdbus call ... Properties.Get org.kde.StatusNotifierItem IconPixmap \
         | python3 icon-dump.py OUT.png

KDE StatusNotifierItem pixmaps are ARGB32 in network byte order, so each
pixel is four bytes A,R,G,B. The script also prints the decoded corner and
center pixel values so the evidence can name the colour without a viewer.
"""
import re
import sys

from PIL import Image


def main():
    text = sys.stdin.read()
    match = re.search(r"\((\d+), (\d+), \[byte (.*?)\]\)", text, re.S)
    if not match:
        sys.exit("icon-dump: no (width, height, [byte ...]) tuple in reply")
    width, height = int(match.group(1)), int(match.group(2))
    data = bytes(int(b, 16) for b in re.findall(r"0x([0-9a-fA-F]{2})", match.group(3)))
    if len(data) < width * height * 4:
        sys.exit("icon-dump: short pixmap (%d bytes for %dx%d)" % (len(data), width, height))
    img = Image.new("RGBA", (width, height))
    pixels = img.load()
    for y in range(height):
        for x in range(width):
            a, r, g, b = data[(y * width + x) * 4:(y * width + x) * 4 + 4]
            pixels[x, y] = (r, g, b, a)
    out = sys.argv[1] if len(sys.argv) > 1 else "icon.png"
    img.save(out)
    # The coverage badge is fixed at (17,17) with radius 4; (17,14) samples its
    # top edge (the hollow "off" ring) and (17,17) its fill.
    print("size=%dx%d engine_center=%s badge_center=%s badge_ring=%s" % (
        width, height, pixels[width // 2, height // 2],
        pixels[width - 5, height - 5], pixels[width - 5, height - 8]))


if __name__ == "__main__":
    main()
