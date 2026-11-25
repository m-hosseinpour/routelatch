# R15-A — tray menu capabilities: measurements and R15-D1 refresh decision

> **Dated evidence snapshot (2026-11-25, spike/tray).** This file records what the
> R15 menu prototype actually did with `deedles.dev/tray v0.2.0` on this host. It is
> not product status. Raw outputs are under [`evidence/`](evidence/) with
> `-2026-11-25` names. The R13-A [FINDINGS.md](FINDINGS.md) snapshot was not changed.

**Status: R15-A gate passed; Ready for R15-B.** All menu capabilities the R15 design
needs are supported. Runtime changes appear in an **already-open Plasma menu** for both
property-only and structural changes, so R15-D1 does not need a reopen-only fallback.
Two visual gaps are recorded below (a checkmark screenshot and a third-level submenu
screenshot); the D-Bus evidence covers both, and neither changes the design.

No product code was added or imported. `internal/`, `cmd/`, the root module, services,
autostart, and desktop settings were not touched. The only desktop effects were one
temporary tray item (`routelatch-tray-menu-spike`, magenta icon) per run, opened menus,
and X11 pointer/key events needed to open and close them; every item and process was
removed at the end of its run and no desktop setting was read or written.

## 1. Environment and pins

| Item | Observed |
|---|---|
| Date | 2026-11-25 (UTC) |
| Desktop | KDE Plasma **6.6.7**, `kwin_x11` **6.6.4**, X11 (`DISPLAY=:0`); not Wayland |
| Session bus | `unix:path=/run/user/1000/bus` |
| Tray host | `org.kde.StatusNotifierHost-4374` owned by plasmashell (`:1.24`) |
| Library | `deedles.dev/tray v0.2.0` (`acf85aba49de5147897736198745658a5ed8d157`, R13-A pin) |
| Toolchain | `go1.27.4 linux/amd64` from `.toolchain/`, `GOTOOLCHAIN=local`, `CGO_ENABLED=0` |
| Spike module | `routelatch/spike/tray` (separate module; `git` revision `f243ce4`, tree dirty only by these spike files) |
| Product tray | the installed `routelatch __app-tray` item was running. One early exploratory synthetic click (after the spike had already exited) opened its menu; it was closed with Escape, no item was activated, and no product state changed. |

`XDG_SESSION_TYPE` and `XDG_CURRENT_DESKTOP` were empty in the worker shell; X11 was
established from the running `kwin_x11`, `DISPLAY=:0`, and the `/tmp/.X11-unix/X0`
socket. The live script set only `DISPLAY`/`DBUS_SESSION_BUS_ADDRESS` locally for its
own process and changed no system or desktop configuration.

The prototype is `spike/tray/cmd/menu-spike` (`main.go`, `menu.go`). It builds the
contract-shaped tree (Status, Mode, Proposals, Approved rules, Pool, Desktop proxy,
Settings, Advanced, Experiment, Last action, Quit), with a 176-node pool fixture split
120/24/12/20 and 20 rows per page. A private control object at `/Control`
(`dev.routelatch.MenuSpike.Apply`) on the spike's second bus connection exists only so
the evidence driver can mutate the menu at a known time; it is not a proposed product
interface. The spike icon is magenta to distinguish it from the product's blue icon.

Library facts that shaped the prototype (source-inspected in the pinned module):

- `MenuItem` has no public id accessor. The product must keep its own stable keys and
  handlers; it cannot address an item by the dbusmenu id through the library API.
- The library does not manage radio groups. On a mode click the app must set every
  sibling's `toggle-state` itself (done by the prototype).
- Removing the only child of a submenu leaves `children-display: ''` in the layout
  rather than removing the property. The contract's disabled `No …` child is therefore
  required; an empty submenu must not be used as a state.
- `AboutToShow(id)` reports a dirty flag; a `GetLayout` for that parent clears it.

## 2. Static build, four architectures

Following R13-A's architecture set (`amd64`, `arm64`, `riscv64`, `ppc64le`):

```sh
cd spike/tray
export PATH=<repo>/.toolchain/go/bin:$PATH GOTOOLCHAIN=local
for arch in amd64 arm64 riscv64 ppc64le; do
  for c in menu-spike fake-watcher; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
      -o /tmp/opencode/menu-build-final/$c-$arch ./cmd/$c
  done
done
```

All eight artifacts are `ELF … statically linked`; the amd64 binaries report
`not a dynamic executable` under `ldd`, `CGO_ENABLED=0` in `go version -m`, and only
`deedles.dev/tray`, `godbus/dbus/v5`, `golang.org/x/sys` as linked deps. With
`CGO_ENABLED=0`, `go list -deps -f '{{if .CgoFiles}}…'` prints nothing. `go vet`,
`gofmt -l`, and `go build ./...` are clean. Raw output:
[`evidence/menu-build-2026-11-25.txt`](evidence/menu-build-2026-11-25.txt).

## 3. Offline harness

`scripts/menu-offline.sh` starts `dbus-run-session`, the R13-A `fake-watcher`, a
`dbus-monitor`, and `menu-spike`; gdbus/busctl play the host. It records the initial
layout, `AboutToShow` before/after changes, property and structural changes, clicks,
pagination, confirmation insertion, empty-submenu conversion, and control-free
Unicode text. Raw output:
[`evidence/menu-offline-2026-11-25.txt`](evidence/menu-offline-2026-11-25.txt),
monitor: [`evidence/menu-offline-monitor-2026-11-25.txt`](evidence/menu-offline-monitor-2026-11-25.txt).

Confirmed offline (all raw layouts linked from the log):

| Check | Observed |
|---|---|
| Tree shape | 163 items at depth 3; separators 8; radio 5; checkmark 1; `visible=false` 1 |
| Nested levels | root → Pool → Healthy (120) → 20 rows; root → Proposals → proposal → Accept/Reject/Recheck |
| Dirty protocol | `AboutToShow(0)` `false` after `GetLayout`; `true` after relabel/add; `false` again after `GetLayout` |
| Label update | `ItemsPropertiesUpdated`; no `LayoutUpdated` |
| Add/remove | `LayoutUpdated`; new root item placed before status with `MoveBefore` |
| visible/enabled | `ItemsPropertiesUpdated`; layout shows `visible=true/false`, `enabled=false` |
| Radio/checkmark | programmatic `SetProps` changes all intended items; click handler updates all three mode items |
| Pagination | page 2/6 and clamp 99→6; 20 rows + page label + separator + prev/next |
| Long/Unicode label | 131-rune mixed Persian/Greek/Latin label round-trips; control-free multilingual label has no ESC byte |
| Empty submenu | last child removed → `children-display: <''>`; refill → `<submenu>` |
| Handler cost | click handler returned in 85 µs in the archived offline run |

Per-page layouts:
[initial](evidence/menu-offline-layout-initial-2026-11-25.txt),
[page 1](evidence/menu-offline-layout-pool-page1-2026-11-25.txt),
[page 2](evidence/menu-offline-layout-pool-page2-2026-11-25.txt),
[page 99 clamped to 6](evidence/menu-offline-layout-pool-page99-2026-11-25.txt),
[radio click](evidence/menu-offline-layout-mode-clicked-2026-11-25.txt),
[confirmation](evidence/menu-offline-layout-confirm-2026-11-25.txt),
[empty submenu](evidence/menu-offline-layout-experiment-empty-2026-11-25.txt).

## 4. Live KDE run

Command (one temporary item; the script kills it and verifies the watcher list):

```sh
DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus DISPLAY=:0 \
  scripts/kde-menu-evidence.sh /tmp/opencode/menu-build-final/menu-spike-amd64 \
  /tmp/opencode/kde-evidence
```

Canonical run: **2026-11-25T17:14:32Z** (all D-Bus and live-update screenshots below).
A second run **2026-11-25T17:05:05Z** supplied the vivid Pool-submenu screenshot and
the submenu interaction excerpt; its monitor lines are raw D-Bus from the same host.
Raw logs:
[`evidence/menu-kde-2026-11-25.txt`](evidence/menu-kde-2026-11-25.txt),
[`evidence/menu-kde-monitor-2026-11-25.txt`](evidence/menu-kde-monitor-2026-11-25.txt),
[`evidence/menu-kde-layout-initial-2026-11-25.txt`](evidence/menu-kde-layout-initial-2026-11-25.txt),
[`evidence/menu-kde-layout-raw-label-2026-11-25.txt`](evidence/menu-kde-layout-raw-label-2026-11-25.txt),
[`evidence/menu-kde-submenu-2026-11-25.txt`](evidence/menu-kde-submenu-2026-11-25.txt).

Visual evidence (X11 root screenshots, cropped to the menu region):

- [root menu open](evidence/menu-kde-open-2026-11-25.png)
- [live relabel in the open menu](evidence/menu-kde-open-relabel-2026-11-25.png)
- [live mode change in the open menu](evidence/menu-kde-open-radio-2026-11-25.png)
- [live structural add in the open menu](evidence/menu-kde-open-add-2026-11-25.png)
- [root → Pool submenu with radio items and group submenus](evidence/menu-kde-pool-submenu-2026-11-25.png)

Opening the menu was done with XTest (one left click on the magenta icon), and the
script accepted the open only when plasmashell called `AboutToShow` on this item's bus
name. Closing was one Escape key. Across the runs only submenu parents (Pool, Healthy,
Mode, Settings) were targeted; no action item in the menu was clicked and no
notification was sent.

## 5. Per-capability verdicts

| # | Capability | Verdict | Raw evidence locator |
|---|---|---|---|
| 1 | Nested submenus (≥2 levels) and separators | **works** | Depth-3 layouts in `menu-offline-layout-initial` and `menu-kde-layout-initial`; host opened Pool (id 45) and Healthy (id 50) with `AboutToShow` + `Event opened` + `GetLayout(45/50,1)` in `menu-kde-submenu-2026-11-25.txt`; Pool submenu screenshot |
| 2 | Radio/checkmark items and programmatic state change | **works** (radio rendered and live-updated; checkmark protocol/host confirmed, visual checkmark not captured) | Pool submenu screenshot shows a filled auto-select radio and empty pinned radio; `menu-kde` radio stage: 5×`ItemsPropertiesUpdated`, open menu status line changes to `All proxy…`; checkmark `toggle-type`/`toggle-state` in both initial layouts and `SetProps` updates |
| 3 | Visible/enabled toggling of items | **works** (D-Bus + host delivery; visual hide/grey not captured) | Offline `visibility` stage: `visible=true`, `enabled=false` in `menu-offline` log; live KDE `visibility` stage: 2×`ItemsPropertiesUpdated`, no `GetLayout` |
| 4 | Add/remove/relabel at runtime; open menu vs next open | **works in an already-open menu** (no reopen needed) | `menu-kde-open-relabel` shows the new status text; `menu-kde-open-add` shows `Job: pool refresh running` at the top of the open menu; host `GetLayout` 13.3 ms after the apply request and 0.31 ms after the first `LayoutUpdated` (raw timestamps in `menu-kde-2026-11-25.txt`); remove triggers `GetLayout` but records no latency; no `closed` event after the structural add |
| 5 | 20–25-item page from 176-item pool + switching/clamping + long/Unicode label | **works** | Page size 20; groups 120/24/12/20 = 176; page 2/6 and clamp 99→6 in `menu-kde` log and `menu-kde-layout-pool-page2/99`; 131-rune mixed-script label in the initial layout; host fetched page rows with `GetLayout(50,1)` |
| 6 | Host event behavior, timing, rate/latency | **recorded** (see §6) | `menu-kde-monitor-2026-11-25.txt`; counts in `menu-kde-2026-11-25.txt` |

### What the open-menu evidence actually shows

The relabel screenshot was taken after a property-only `SetProps` while the menu was
open: the top line reads `Live relabel #1 · 155 usable · 4 open`, and the menu is still
open. The next screenshot was taken after a root-item insertion while the menu was
still open: the disabled `Job: pool refresh running` row appears above the status line.
In both cases plasmashell applied the change without the menu being reopened. For the
structural add, the host called `GetLayout(0,1)` **13.3 ms after the apply request**
(`host_GetLayout_latency_ms=13.342`); the raw log dates the first `LayoutUpdated` at
1795626875.730906 and that `GetLayout` at 1795626875.731219, so the
`LayoutUpdated`→`GetLayout` gap is **0.31 ms**. Property-only changes produced no
`GetLayout`, only `ItemsPropertiesUpdated`.

**Consequence for R15-D1:** the binder may diff/apply the live tree; a reopen-only
refresh strategy is not required. Because the host reacts to *each* `LayoutUpdated`
with its own `GetLayout`, a 20-row page replacement produced **51** `GetLayout` calls
for parent 50 within ~6 ms (46 in the clamp step, within ~5 ms). This is local and fast but is an
amplification to design around: reuse rows where possible, or accept the round-trips
rather than rebuilding more than one page at a time. Property-only updates (relabel,
state, enabled/visible) are cheap and were applied in place.

## 6. Host behavior and timing

Measured on the live session; plasmashell sender `:1.24`, item bus
`org.freedesktop.StatusNotifierItem-…`:

| Measurement | Observed |
|---|---|
| Host `GetLayout` after registration | **17.7 ms** from run start to the first host `GetLayout(0,1)` (measured before the process launch) |
| Open sequence | `AboutToShow(0)` then `Event(0,"opened",<''>,0)`; then `GetLayout(0,1)` |
| Submenu open | `AboutToShow(id)`, `Event(id,"opened")`, `GetLayout(id,1)`; observed ids 45 (Pool) and 50 (Healthy) |
| Close | `Event(0,"closed")` on Escape; submenu close sends `Event(id,"closed")` |
| Property-only change | `ItemsPropertiesUpdated` only; no host `GetLayout`; rendered live |
| Structural change | `LayoutUpdated` (+ sometimes `ItemsPropertiesUpdated`); for the add, host `GetLayout` **13.3 ms after the apply request** and 0.31 ms after the first `LayoutUpdated`; the remove stage shows a host `GetLayout` with no recorded latency, and visibility/radio produced none, so no remove-latency bound is claimed; menu stayed open |
| Click handler | 85 µs inside the spike in the archived offline run; D-Bus `Event` reply returned normally |
| Page 2 rebuild | 51 host `GetLayout(parent=50)` calls within ~6 ms (one per `LayoutUpdated`) |
| Host rate limit | none observed for dbusmenu methods/signals in these runs; the only Plasma rate limit seen in R13-A was for notifications, which this prototype does not use |
| `AboutToShow` dirty flag | `false` after a fresh layout; `true` after any property or structural change; `false` after the corresponding `GetLayout` |
| Host-originated `Event clicked` | **not observed** in the live runs because no menu action was clicked; synthetic `Event(id,"clicked")` reached the handlers and changed state (offline + `mode-after-click` layout). Only host `opened`/`closed` events were observed. |
| `SetProps` observation | `SetProps` is a library API, not a D-Bus method; its effect is the `ItemsPropertiesUpdated` signal, which plasmashell consumed without a `GetLayout`. |

The host never spontaneously closed the menu after the structural add in these runs;
the close was our Escape. It did close submenus quickly around screenshot/click
sequences (see limitations).

## 7. Limits and what remains untested

- **Visual gaps:** no screenshot captured the checkmark state inside the Settings
  submenu (the host closed the submenu before the capture), and no screenshot captured
  the Healthy page rows inside the third-level submenu even though the host opened it
  and fetched `GetLayout(50,1)` (excerpt). Menu text/rendering was not inspected by a
  human; the screenshots above are the machine-captured evidence.
- **Submenu interaction is host-timing sensitive:** a screenshot between two clicks
  was observed to close the open submenu, and hovering did not reliably open submenus
  (clicking did). The script therefore opens the second level immediately using the
  observed row offset; that run's screenshots missed the third level.
- **One run was inconclusive:** a later run could not find the magenta icon at all;
  the bottom panel was not visible in its screenshots while a fullscreen Jitsi window
  was active. That is a host/UI-state limitation of the attempt, not a menu failure; it
  was not used for any verdict.
- **One exploratory click hit the product item:** after the spike process had exited,
  a synthetic click at the last known icon position opened the installed product's
  tray menu. It was closed with Escape immediately; no menu item was activated and no
  product state changed. Scripted evidence never targeted the product bus name.
- **Panel layout can move the icon** when tray items appear/disappear; the script
  retries up to three times and verifies `AboutToShow` on the correct bus name before
  accepting an open.
- **Wayland/GNOME/other desktops** were not tested (this host is X11 Plasma). R13-A
  already recorded the same limit for registration.
- **Runtime on `riscv64`/`ppc64le`** was cross-build only.
- **Long-label rendering:** the >80-rune label round-trips at the D-Bus level; whether
  Plasma truncates/ellipsizes it visually was not measured. The contract's 80-rune cap
  is still the application's responsibility.
- **Notifications, Quit/restore actions, real management requests, restart events,
  and job/refresh behavior** are R15-B..G work, not this spike. No action was executed.
- **Library restart/watcher-loss behavior** was R13-A scope and was not re-tested.
- **Script revision:** the archived live runs used slightly earlier revisions of
  `scripts/kde-menu-evidence.sh`; later edits refined submenu navigation (click instead
  of hover, row-offset fallback) and were not re-run after the panel became unavailable.
  The raw D-Bus logs and screenshots above are the evidence; the script's core
  create/query/update/remove path is the same one that produced them.

## 8. Reproduction

```sh
cd spike/tray
export PATH=<repo>/.toolchain/go/bin:$PATH GOTOOLCHAIN=local

# build (four architectures; see §2)
CGO_ENABLED=0 go build ./cmd/menu-spike ./cmd/fake-watcher

# offline, isolated bus
scripts/menu-offline.sh /tmp/opencode/menu-build-final /tmp/opencode/menu-evidence

# live KDE (authorized for R15-A; one temporary item, removed at exit)
DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus DISPLAY=:0 \
  scripts/kde-menu-evidence.sh /tmp/opencode/menu-build-final/menu-spike-amd64 \
  /tmp/opencode/kde-evidence
```

The live script requires `gdbus`, `busctl`, `dbus-monitor`, ImageMagick `import`,
`tesseract`, Python 3 with Pillow and python-xlib, and a running X11 KDE session. It
does not install anything, does not change desktop settings, and removes its item.
