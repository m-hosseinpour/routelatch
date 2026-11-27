# Panel contrast — tray icon on the real Plasma panel (2026-11-27)

> **Dated verification record.** Product HEAD `3f124dfa006e6701cdf83345f0ef983ea9b3f194`
> (`3f124df`), temporary unstamped build, authorized Plasma/X11 session, isolated
> data dirs. This closes the H-TRAY "panel screenshot/contrast" limit for the
> current theme only. It is not the full A17 acceptance and not a release or
> support decision.

**Status: PASS (measured).** On the current dark Plasma panel, every core
foreground of the RouteLatch tray icon is at least **4.21:1** against the
adjacent panel background `#202326`; the lowest value is the blue engine body.
The helper-missing/off, error, active/on, starting and conflict icon states were
all rendered and measured. The white inner mark reaches 15.79:1 and the near-white
off ring 14.48:1. No tested state was indistinguishable from the panel.

A later **synthetic** light-panel composite is recorded in the
[addendum](#synthetic-light-panel-composite-2026-11-27-addendum). It is not a
real light-theme run and does not change the dark-theme result above.

The screen was locked during the run, so the evidence is the panel's mapped X11
window content, not a desktop screenshot and not a human visual observation.
The panel re-rendered for each state (the expected exact badge colours changed
between captures), which shows the captures are live rather than a frozen
buffer. See [Limits](#limits-and-not-verified).

## Revision and environment

| Item | Value |
| --- | --- |
| Product HEAD | `3f124dfa006e6701cdf83345f0ef983ea9b3f194` |
| Temporary binary | `<scratch>/bin/routelatch`, `CGO_ENABLED=0`, go1.27.4, `-trimpath`, unstamped (`dev`), sha256 `915b694c…` |
| Desktop | Plasma 6.6.7, kwin 6.6.4, X11 `:0` |
| Screen / panel | 1920x1200, bottom panel `1920x38+0+1163`, window `0x160003c` |
| Scale | 1.25 (`kdeglobals [KScreen] ScaleFactor`); 22 logical px icon rendered at 28 physical px |
| Theme | BreezeDark colour scheme, `org.kubuntudark.desktop` look-and-feel, Papirus-Dark icons |
| Panel background | `#202326` = RGB 32,35,38 (same as `[Colors:Window] BackgroundNormal`) |
| Run window | 2026-11-27 17:36:20Z–17:36:47Z |
| Lock state | `org.freedesktop.ScreenSaver.GetActive` = true during the run |

## Method

1. **Session detection.** The run read the existing desktop session (`DISPLAY=:0`,
   session bus `unix:path=<runtime>/bus`, `XDG_SESSION_TYPE=x11`, KDE) and found
   the mapped bottom panel through `xwininfo`; no panel or theme setting was
   changed.
2. **Real helper-missing path.** The unchanged product `routelatch --data-dir
   <isolated> run` started its own tray child; the host has no TUN helper, so
   `status` reported `helper=missing`, `state=off`, `desired=false`. The tray
   registered as a StatusNotifier item and the panel rendered the off icon. A
   real `routelatch tun on` then failed with exit 7
   (`tun_helper_unavailable`), the menu changed to
   `Coverage: unavailable · desired on · not active`, and the error icon was
   captured.
3. **Fake visual states.** The unchanged product `routelatch __app-tray` was run
   against the existing Python fake management peer from the H-TRAY harness
   (`fake-tray.py` / `apply.py` / `states.json`). It rendered off, error, on
   (active TUN), starting and conflict. These are **visual-only** states; the fake
   peer is not an engine.
4. **Capture.** Each state was captured with ImageMagick `import -window
   <panel-window>` into a scratch file. The 22px product pixmap was read over
   D-Bus at the same time. A scale-aware template match (`locate_crop.py`) found
   the icon in the panel and wrote only a **38x37 px tight crop** containing the
   icon and adjacent panel background. The full panel capture was then deleted.
   No full-screen image was taken or committed.
5. **Measurement.** The panel background is the modal crop colour, with the crop
   border checked to contain background only. Foreground elements are the exact
   colours from `internal/app/tray.go`; `measure.py` verifies their presence in
   the crop (the off ring is rounded to `239,246,252` by the 1.25 resampling) and
   computes WCAG 2.1 relative-luminance ratios against the panel background. The
   **3:1** non-text threshold (WCAG SC 1.4.11) is used as the check.

## Results

All values are foreground versus the adjacent panel background `#202326`. The
crop is 38x37 physical pixels at the session's 1.25 scale; it contains no other
tray icon.

| State | Engine `#2f81f7` | Inner mark `#ffffff` | Badge | Min core | Result |
| --- | --- | --- | --- | --- | --- |
| real off / helper missing | 4.21:1 | 15.79:1 | ring `#eff6fc` 14.48:1 | 4.21:1 | PASS |
| real error / desired on, helper missing | 4.21:1 | 15.79:1 | orange `#db6d28` 4.68:1 | 4.21:1 | PASS |
| fake off | 4.21:1 | 15.79:1 | ring `#eff6fc` 14.48:1 | 4.21:1 | PASS |
| fake error | 4.21:1 | 15.79:1 | orange `#db6d28` 4.68:1 | 4.21:1 | PASS |
| fake on (active TUN) | 4.21:1 | 15.79:1 | green `#2ea043` 4.68:1 | 4.21:1 | PASS |
| fake starting | 4.21:1 | 15.79:1 | light blue `#58a6ff` 6.25:1 | 4.21:1 | PASS |
| fake conflict | 4.21:1 | 15.79:1 | orange `#db6d28` 4.68:1 | 4.21:1 | PASS |

Observed exact panel pixels matched the product colours: engine `47,129,247`,
white `255,255,255`, green `46,160,67`, orange `219,109,40`, light blue
`88,166,255`, off ring `239,246,252` (nominal `240,246,252`). The icon origin was
`(1594,5)` and the matched rendered size 28 px for every state.

### State visibility

- **Off / helper missing:** the near-white hollow ring is the brightest element
  (14.48:1 against the panel, 3.44:1 against the blue engine). Clearly visible.
- **Error / conflict:** the orange badge is 4.68:1 against the panel and the white
  `!` inside it is 15.79:1. Clearly visible.
- **On:** the solid green badge is 4.68:1 against the panel. It is luminance-close
  to the blue engine (1.11:1) but strongly hue-separated (ΔE76 125) and differs in
  shape from the off ring, the starting dash and the error `!`.
- **Starting:** the light-blue badge with white dash is 6.25:1 against the panel;
  the dash itself is white. Visible, although the badge-to-engine luminance
  contrast is low (1.48:1, ΔE76 23.5) because both are blue; the shape cue is the
  dash.
- The badges are small by design: 9 logical px (about 11 physical px at 1.25).
  The 1:1 and 4x crops are the legibility evidence at normal panel scale; no
  formal colour-vision or user study was performed.

## Synthetic light-panel composite (2026-11-27 addendum)

> **Synthetic only.** These images are the retained `IconPixmap` PNGs
> composited over light backgrounds at the same 1.25 scale. No light theme was
> selected, no panel or desktop setting was changed, and no real light-panel
> screenshot exists. The committed crops are opaque captures over the dark
> panel, so their pixels cannot be cleanly re-composited; the source pixmaps are
> the exact D-Bus assets from which those crops were rendered and their core
> colours match the dark-panel measurements.

**Synthetic light-panel result: FAIL for the white mark, off ring and starting
badge; the blue silhouette itself passes.** The white mark measures **1.14:1**
on `#eff0f1` and **1.03:1** on `#fcfcfc` — effectively 1:1, so it would not meet
the 3:1 non-text threshold if judged against a light panel. The blue body
remains 3.28:1 / 3.65:1, so the icon silhouette does not disappear entirely.

| State | Engine (blue) | White mark | Badge |
| --- | --- | --- | --- |
| real off / helper missing | 3.28 / 3.65 | 1.14 / 1.03 | off ring `#f0f6fc` 1.05 / 1.06 |
| real error / desired on | 3.28 / 3.65 | 1.14 / 1.03 | orange `#db6d28` 2.95 / 3.29 |
| fake off | 3.28 / 3.65 | 1.14 / 1.03 | off ring `#f0f6fc` 1.05 / 1.06 |
| fake error | 3.28 / 3.65 | 1.14 / 1.03 | orange `#db6d28` 2.95 / 3.29 |
| fake on | 3.28 / 3.65 | 1.14 / 1.03 | green `#2ea043` 2.96 / 3.29 |
| fake starting | 3.28 / 3.65 | 1.14 / 1.03 | light blue `#58a6ff` 2.21 / 2.46 |
| fake conflict | 3.28 / 3.65 | 1.14 / 1.03 | orange `#db6d28` 2.95 / 3.29 |

Values are `#eff0f1 / #fcfcfc` contrast ratios against the light background;
threshold 3:1. Full per-element values are in `light-synthetic/light-contrast.json`.

Parts and states:

- **Blue body:** passes on both backgrounds (3.28:1 and 3.65:1).
- **White inner mark:** fails on both (1.14:1 and 1.03:1). It is enclosed by the
  blue body, so its actual adjacent contrast is white versus blue (3.75:1), not
  white versus the panel. The icon therefore remains identifiable as a blue disc
  with a lighter centre, but the white mark itself does not provide light-panel
  contrast.
- **Off ring:** fails against the light panel (1.05:1 / 1.06:1); over the blue
  body the ring is 3.44:1. It would be much weaker on a light panel.
- **Error / conflict orange:** 2.95:1 on `#eff0f1` (just below 3:1) and 3.29:1 on
  `#fcfcfc`. Marginal on the Breeze-light background; passes only on near-white.
- **On green:** 2.96:1 on `#eff0f1` (marginal fail) and 3.29:1 on `#fcfcfc`.
- **Starting light blue:** fails both backgrounds (2.21:1 / 2.46:1).
- The on, error/conflict and starting badges also have low luminance contrast
  against the blue body (roughly 1.1–1.5:1), relying on hue and shape; the off
  ring is the exception (3.44:1 against blue). That is a separate
  state-distinction limit, not the panel-background ratio measured here.

Method and limits:

- The 22x22 RGBA `IconPixmap` was composited over `#eff0f1` and `#fcfcfc` at
  source size, then resampled to 28 px with Lanczos and written as a 38x38 px
  synthetic crop. Ratios use the product core colours; at 28 px the one-pixel
  off ring and badge edges are heavily resampled, so exact-core pixel counts are
  also recorded in the JSON.
- This is a colour/geometry test, not a real Breeze-Light panel rendering. The
  actual light panel may use a translucent or tinted background, different border
  or hover treatment, and theme-provided icon recolouring; none of those were
  tested. The result supports "white mark is ~1:1 on light backgrounds", not a
  claim about a specific light desktop.
- No product change is proposed here; a light-theme fix would need theme-aware
  icon colours and is outside this verification.

## Limits and not verified

- **Locked screen.** The session was locked for the whole run. The panel window
  was captured directly from X11 while the locker covered the screen, so this is
  rendered panel content, not a photograph of the unlocked desktop. The
  per-state colour changes prove it was live. A person present at an unlocked
  panel was not asked to confirm the icons by eye, and no pointer hover/click was
  performed.
- **Current theme only.** The icon colours are hard-coded in `internal/app/tray.go`,
  not theme-aware. This PASS applies to the measured BreezeDark/Papirus-Dark
  panel (`#202326`). A light panel was not run; the synthetic addendum measures
  the white mark at 1.14:1 on `#eff0f1` and 1.03:1 on `#fcfcfc` (effectively 1:1)
  and the blue body at 3.28:1 / 3.65:1. The icon silhouette remains visible, but
  the white mark, off ring and starting badge do not meet 3:1 against the light
  backgrounds. That is a remaining product/theme limit, not a failure of the
  dark-theme check.
- **Fake states are visual-only.** Active/on, starting and conflict were produced
  by the fake status peer. A real TUN-active icon still requires installing the
  host helper, which was not authorized. The real run exercised only
  helper-missing/off and desired-on/error.
- **Host scope.** Wayland, GNOME and other StatusNotifier hosts were not tested.
  Panel hover/pressed states, menu presentation, notifications, high-contrast
  accessibility themes and colour-blind palettes were not tested.
- The 3:1 check is a WCAG non-text threshold, not a claim of complete
  accessibility compliance or of usability at every viewing distance.

## Hygiene

No helper install/uninstall, systemd unit/socket, autostart, panel, theme,
desktop-proxy, DNS, firewall or network change; no root action. Both runs used
isolated data/XDG directories under the scratch root. The live instance
(`/usr/bin/routelatch`, PIDs 4099/4112/4329 and its bus item
`StatusNotifierItem-4329-1`) was not addressed or stopped. The temporary
supervisor/tray and fake peer were stopped; the StatusNotifier watcher list after
the run is byte-identical to the list before it (except no temporary item
remains). Full panel captures were deleted; only tight crops and derived
montages are committed. No product code or other document was edited.

## Evidence

All paths are under `spike/tun/evidence/panel-contrast-2026-11-27/`.

| Path | Contents |
| --- | --- |
| `session.txt` | detected display/bus/desktop, panel window/geometry, scale, theme, lock state |
| `revision.txt` | product head, binary build/sha256, run window |
| `real/` | real helper-missing/off and error/desired-on: menu labels, status, `tun on` exit 7, icon pixmaps, tight crops, per-state contrast JSON |
| `fake/` | fake off, error, on, starting, conflict: labels, icon pixmaps, tight crops, per-state contrast JSON |
| `contrast.json`, `contrast.md` | aggregate measurements and PASS table |
| `panel-crops-1x.png` | seven labelled tight crops at 1:1 |
| `panel-crops-4x.png` | same crops at 4x nearest-neighbour |
| `light-synthetic/` | synthetic light-background composites (`#eff0f1`, `#fcfcfc`), `light-contrast.json`/`.md`, 4x montage |
| `panel-run.sh`, `locate_crop.py`, `measure.py`, `summarize.py` | reproducible harness |
| `bus-before.txt`, `bus-after.txt` | StatusNotifier item list before/after cleanup |

Raw coverage labels are in `real/coverage-*.txt` and `fake/coverage-*.txt`;
the real status summary records `helper=missing` and `tun on` records `exit=7`.
