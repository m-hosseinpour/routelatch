Panel-contrast evidence (2026-11-27)

Purpose: measure the RouteLatch tray icon against the actual Plasma panel
background, using the real helper-missing product path and the existing
visual-only fake-status states. See ../../PANEL-CONTRAST.md for the report.

Contents
- session.txt        detected desktop session, panel window/geometry, theme, lock state
- revision.txt       product head and temporary binary identity
- real/              real engine + supervisor on an isolated data dir, no helper:
                     off/helper-missing and error/desired-on icons, menu labels
- fake/              unchanged product tray child against the visual-only fake
                     management peer: off, error, on, starting, conflict
- locate_crop.py     locates the 22px pixmap in the panel capture (scale aware)
                     and writes only a tight crop + JSON geometry
- measure.py         WCAG relative-luminance contrast of product colours vs the
                     local panel background, with observed-colour checks
- summarize.py       aggregate contrast.json / contrast.md
- panel-run.sh       reproducible run script; BIN/W/REPO overrides accepted
- panel-crops-1x.png / panel-crops-4x.png  seven tight crops, labelled
- bus-before.txt / bus-after.txt           StatusNotifier item list before/after

Build the temporary binary into the scratch tree first, then run the script:
  W=${TMPDIR:-/tmp}/routelatch-panel-contrast
  mkdir -p "$W/bin"
  (cd <repo> && CGO_ENABLED=0 .toolchain/go/bin/go build -trimpath -o "$W/bin/routelatch" ./cmd/routelatch)
  <repo>/spike/tun/evidence/panel-contrast-2026-11-27/panel-run.sh

Only tight 38x37 px crops around the icon are stored here. Full panel images
were captured under the scratch directory, cropped, and deleted; they were
never copied into the repository or the evidence set. Scratch paths are shown
as <scratch> in committed text.
