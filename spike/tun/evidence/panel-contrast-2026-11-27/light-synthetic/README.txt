Synthetic light-panel contrast addendum (2026-11-27)

This directory is synthetic. It does not record a light-theme desktop run.

- light_composite.py  composites the retained 22x22 IconPixmap PNGs over
                      #eff0f1 and #fcfcfc at source size, resamples to 28 px
                      (session scale 1.25), and writes the 38x38 synthetic crops
                      plus light-contrast.json/.md.
- composite-*.png    per-background, per-state synthetic crops.
- light-panel-4x.png labelled 4x montage of all 14 composites.
- make-montage.sh    regenerates the montage from the composite crops.

The opaque dark-panel crops in ../real and ../fake cannot be composited
directly without carrying the #202326 background into the light image. The
source pixmaps used here are the exact D-Bus assets from which those crops were
rendered; their core colours match the dark-panel measurements. Ratios use the
product core colours (3:1 non-text threshold).

Result: white mark 1.14:1 (#eff0f1) / 1.03:1 (#fcfcfc), blue body 3.28:1 /
3.65:1, off ring 1.05/1.06, starting badge 2.21/2.46. The white mark is ~1:1
against the light backgrounds, but it is enclosed by the blue body; see the
report addendum for the adjacency caveat.
