#!/usr/bin/env python3
"""Locate and click the temporary menu-spike tray icon on X11.

The menu spike draws a magenta icon, distinct from the installed product's blue
one, so the live evidence script can open the real Plasma menu without a human
at the keyboard. Everything here is transient UI input: one left click on the
icon and one Escape to close. It writes nothing outside the given images.

Modes:
  find IMAGE                     print "<x> <y>" or "not-found"
  click X Y                      move the pointer and send a left click
  hover X Y                      move the pointer and wait for a submenu
  escape                         send Escape
  diff A B [X Y W H]             changed pixels, optionally inside a region
  crop IMAGE OUT X Y W H
  find-text IMAGE TEXT [X Y W H] OCR a word; print its centre or "not-found"
"""
import sys

from PIL import Image, ImageChops

TARGET = (0xE0, 0x00, 0xE0)
TOLERANCE = 24
PANEL_HEIGHT = 140


def near(pixel, target=TARGET, tolerance=TOLERANCE):
    return all(abs(a - b) <= tolerance for a, b in zip(pixel[:3], target))


def find_icon(path):
    image = Image.open(path).convert("RGB")
    width, height = image.size
    points = set()
    for y in range(height):
        if PANEL_HEIGHT < y < height - PANEL_HEIGHT:
            continue
        for x in range(width):
            if near(image.getpixel((x, y))):
                points.add((x, y))
    if len(points) < 40:
        return None, len(points), (0, 0, 0, 0)
    # Connected components: the tray icon is one compact blob, while stray
    # wallpaper pixels are scattered.
    best = []
    while points:
        seed = points.pop()
        stack = [seed]
        blob = [seed]
        while stack:
            x, y = stack.pop()
            for nx in (x - 1, x, x + 1):
                for ny in (y - 1, y, y + 1):
                    if (nx, ny) in points:
                        points.remove((nx, ny))
                        stack.append((nx, ny))
                        blob.append((nx, ny))
        if len(blob) > len(best):
            best = blob
    if len(best) < 40:
        return None, len(best), (0, 0, 0, 0)
    xs = [p[0] for p in best]
    ys = [p[1] for p in best]
    bbox = (min(xs), min(ys), max(xs) - min(xs) + 1, max(ys) - min(ys) + 1)
    if not (8 <= bbox[2] <= 48 and 8 <= bbox[3] <= 48):
        return None, len(best), bbox
    return ((bbox[0] + bbox[2] // 2, bbox[1] + bbox[3] // 2), len(best), bbox)


def click(x, y):
    from Xlib import X, display
    from Xlib.ext import xtest

    d = display.Display()
    root = d.screen().root
    root.warp_pointer(x, y)
    d.sync()
    import time

    time.sleep(0.35)
    xtest.fake_input(d, X.ButtonPress, 1)
    d.sync()
    time.sleep(0.05)
    xtest.fake_input(d, X.ButtonRelease, 1)
    d.sync()
    d.close()


def hover(x, y, wait=1.2):
    from Xlib import display
    import time

    d = display.Display()
    root = d.screen().root
    root.warp_pointer(x + 1, y)
    d.sync()
    time.sleep(0.1)
    root.warp_pointer(x, y)
    d.sync()
    time.sleep(wait)
    d.close()


def escape():
    from Xlib import X, XK, display
    from Xlib.ext import xtest

    d = display.Display()
    keycode = d.keysym_to_keycode(XK.string_to_keysym("Escape"))
    xtest.fake_input(d, X.KeyPress, keycode)
    d.sync()
    xtest.fake_input(d, X.KeyRelease, keycode)
    d.sync()
    d.close()


def diff(a, b, region=None):
    first = Image.open(a).convert("RGB")
    second = Image.open(b).convert("RGB")
    if first.size != second.size:
        print(f"size_mismatch {first.size} {second.size}")
        return
    if region:
        box = (region[0], region[1], region[0] + region[2], region[1] + region[3])
        first = first.crop(box)
        second = second.crop(box)
    delta = ImageChops.difference(first, second)
    bbox = delta.getbbox()
    changed = 0
    for pixel in delta.getdata():
        if pixel != (0, 0, 0):
            changed += 1
    print(f"changed_pixels {changed} bbox {bbox}")


def find_text(image_path, needle, region=None):
    """OCR a word and return its center, using tesseract's TSV output."""
    import subprocess

    command = ["tesseract", image_path, "stdout", "--psm", "11", "tsv"]
    if region:
        # Crop first so words outside the menu cannot match.
        import tempfile

        image = Image.open(image_path)
        box = (region[0], region[1], region[0] + region[2], region[1] + region[3])
        cropped = image.crop(box)
        handle = tempfile.NamedTemporaryFile(suffix=".png", delete=False)
        cropped.save(handle.name)
        handle.close()
        command[1] = handle.name
    output = subprocess.run(command, capture_output=True, text=True, check=False).stdout
    for line in output.splitlines()[1:]:
        columns = line.split("\t")
        if len(columns) < 12:
            continue
        word = columns[11]
        if needle.lower() in word.lower():
            left, top, width, height = (int(columns[i]) for i in (6, 7, 8, 9))
            if region:
                left += region[0]
                top += region[1]
            return left + width // 2, top + height // 2, word
    return None


def main():
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    mode = sys.argv[1]
    if mode == "find":
        position, count, bbox = find_icon(sys.argv[2])
        if position is None:
            print(f"not-found pixels={count} bbox={bbox}")
            return 1
        print(f"{position[0]} {position[1]}")
        return 0
    if mode == "click":
        click(int(sys.argv[2]), int(sys.argv[3]))
        return 0
    if mode == "escape":
        escape()
        return 0
    if mode == "hover":
        hover(int(sys.argv[2]), int(sys.argv[3]))
        return 0
    if mode == "diff":
        region = [int(v) for v in sys.argv[4:8]] if len(sys.argv) >= 8 else None
        diff(sys.argv[2], sys.argv[3], region)
        return 0
    if mode == "find-text":
        region = [int(v) for v in sys.argv[4:8]] if len(sys.argv) >= 8 else None
        found = find_text(sys.argv[2], sys.argv[3], region)
        if not found:
            print("not-found")
            return 1
        print(f"{found[0]} {found[1]} word={found[2]}")
        return 0
    if mode == "crop":
        image = Image.open(sys.argv[2])
        x, y, w, h = (int(v) for v in sys.argv[4:8])
        image.crop((x, y, x + w, y + h)).save(sys.argv[3])
        return 0
    print(f"unknown mode {mode}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
