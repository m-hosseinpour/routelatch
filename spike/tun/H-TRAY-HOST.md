# R16-H host follow-up — real TUN-active tray toggle (2026-11-27)

> **Dated acceptance record.** This run closes the main gap left by
> [`H-TRAY.md`](H-TRAY.md): a real helper installation and a real
> **tray-initiated** enable/disable of system-wide coverage, with `rl-tun0`
> actually present. It was authorized for this host. It is not a release
> decision and does not change the A17 limits in
> [`H-ACCEPTANCE.md`](H-ACCEPTANCE.md).

**Verdict: PASS with limits.** On `6f51554` the real root helper installed,
the product's own tray (no harness tray shim) enabled coverage through its
two-step confirmation, the active menu/icon/status/routes were real, the tray disabled
coverage through its two-step confirmation, the host route/DNS state returned
to the byte-identical baseline, and `tun uninstall` removed the helper. The
active window was about one second; no public traffic was sent.

## Revision, environment, method

| Item | Value |
| --- | --- |
| HEAD | `6f515544e8f51e77fad7f4448dd12cf76cb982a2` (`main`) |
| Binary | `<tmp>/h-tray-host-2026-11-27/bin/routelatch`, Go 1.27.4, `CGO_ENABLED=0`, `-trimpath`, unstamped (`dev`) |
| Binary sha256 | `b45ef36faf5e67425abbf607c510719896932229b331f96489f4486dafd007df` |
| Host | Ubuntu 26.04.1 LTS, kernel 7.0.0-44-generic, systemd 259.9, iproute2 6.19.0 |
| Desktop | Plasma 6.6.7, kwin 6.6.4, X11 `:0`, 1920×1200 |
| Isolated instance | `routelatch --data-dir <tmp>/…/data run`, loopback HTTP/SOCKS `35675`/`45691`, product's own tray child pid `2974115` |
| Helper install | `pkexec /usr/bin/env SUDO_UID=1000 <bin> tun setup --socks 127.0.0.1:45691` (real system units) |
| Active window | helper log `coverage is on` 20:12:28Z, `coverage is off` 20:12:29Z; `rl-tun0` present ≈1 s |
| Live instance | PIDs `2970371/2970384/2970399/2970416` before and after, unchanged |

Method: the product supervisor started the isolated engine and its own tray
child; the tray item was located through `org.kde.StatusNotifierWatcher`. Menu
text came from `com.canonical.dbusmenu.GetLayout` (`menu-dump2.py`, exact
labels) and actions were sent as `Event(id,"clicked")`. No physical panel
click or pointer interaction occurred. The active window contained only local
netlink/Unix-socket/`sha256sum` commands.

The live user instance was never addressed: every CLI call used the isolated
`--data-dir`, and every kill was scoped to that data dir. It had already been
restarted externally (observed start 23:25:49 local, before any host change in
this session; the cause was not investigated), so the baseline was captured
after that restart; its PIDs, args and start times were identical at the end.

## Results

Evidence paths are repository-relative; within a row, `…/` repeats the
preceding directory.

| Check | Result | Evidence |
| --- | --- | --- |
| Real helper install (`tun setup`, root units/binary/config/tmpfiles) | **PASS** | `evidence/h-tray-host-2026-11-27/real/setup.json`, `…/real/02-setup-verify.txt`, `…/real/08-helper-journal.txt` |
| Tray enable (two-step confirm → real coverage) | **PASS** | `evidence/h-tray-host-2026-11-27/real/04-enable-pending.txt`, `…/real/04-active-coverage-final.txt`, `…/real/04-active-tun-status.json` |
| Active icon/state/routes (`Coverage: on`, `rl-tun0`, green badge) | **PASS** | `evidence/h-tray-host-2026-11-27/real/04-active-link.txt`, `…/real/04-active-addr.txt`, `…/real/04-active-routes-all.txt`, `…/real/04-active-routes6-all.txt`, `…/real/icon-on-active.png`, `…/real/icons.txt`, `…/real/tooltips.txt` |
| Tray disable (two-step confirm → off) | **PASS** | `evidence/h-tray-host-2026-11-27/real/05-disable-pending.txt`, `…/real/05-off-coverage.txt`, `…/real/05-off-tun-status.json` |
| Route/DNS restoration to baseline | **PASS** | `evidence/h-tray-host-2026-11-27/real/05-off-route4.txt`, `…/real/05-off-route6.txt`, `…/real/baseline-route4.txt`, `…/real/baseline-route6.txt`, `…/real/05-off-resolv-sha.txt` |
| Helper uninstall (units/files/tmpfiles/socket/link gone) | **PASS** | `evidence/h-tray-host-2026-11-27/real/06-uninstall.json`, `…/real/07-final-artifacts.txt`, `…/real/07-final-post.txt` |
| Live instance untouched | **PASS** | `evidence/h-tray-host-2026-11-27/01-baseline-live-pids.txt`, `…/real/baseline-live-pids.txt`, `…/real/07-final-live-start-with-sidecar.txt` |

Corrected reading of the raw check log: `evidence/h-tray-host-2026-11-27/real/results.txt`
has one `FAIL` for `tun status off after disable` caused by an evidence-tool
JSON-shape bug (`tun status` returns the status object directly under `data`,
not under `data.tun`). The raw `…/real/05-off-tun-status.json` says `state=off`,
`desired=false`; `…/real/results-corrected.txt` and `…/real/CORRECTION.txt`
record the correction. The parser in `run-host.sh` was fixed. The run was not
repeated, because the raw evidence is conclusive and a second host-wide window
would add no information.

## What was observed

- **Off with helper installed:** `Coverage: off · desired off`,
  `Helper: installed`, Enable enabled, Disable disabled with
  `Unavailable: coverage is already off`.
- **Enable confirmation:** the root menu showed
  `Confirm: Enable system-wide coverage (all host traffic)` and `Cancel`; after
  the confirm the Coverage submenu became `Coverage: on · desired on` with
  `Helper: installed`, `Interface: rl-tun0`, `Warnings (2)`
  (`DNS: plain DNS goes direct`, `UDP/QUIC: non-DNS UDP is blocked`), Enable
  disabled with `coverage is already on`, Disable enabled.
- **Active host state:** `tun status` `state=on`, `desired=true`,
  `helper=installed`, `interface=rl-tun0`, `dns=direct`, `udp=blocked`,
  `ipv6=true`, `since=2026-11-27T23:42:28.289+03:30` (20:12:28.289Z).
  `rl-tun0` was `UP`, MTU 1500, with `198.18.0.1/15` and `fc00::1/64`; the
  fixed half routes `0.0.0.0/1`, `128.0.0.0/1`, `::/1`, `8000::/1` were on
  the link. The system default route stayed `via 192.168.1.1 dev wlp0s20f3`,
  and `ip rule`/`ip -6 rule` were unchanged.
- **Icon and tooltip:** 22×22 `IconPixmap` with the green `on` badge
  (center and ring `(46,160,67)`); tooltip
  `Targeted · 0 usable · 0 open · TUN on`.
- **Disable:** the root menu showed `Confirm: Disable system-wide coverage` and
  `Cancel`; after confirm, `Coverage: off · desired off`, `rl-tun0` absent,
  `tun status` `state=off`/`desired=false`, icon returned to the hollow off
  ring (same values as the earlier real off icon), tooltip `… · TUN off`.
- **Restoration:** IPv4/IPv6 route tables and `ip rule` output were byte-equal
  to the pre-setup baseline; `/etc/resolv.conf` sha256 stayed
  `ebdf560272a77357195c39e98340b77e18c8a8ce2025ee950e9e0c7b01467ab8`.
- **Uninstall:** removed `routelatch-tun.service`, `routelatch-tun.socket`,
  `/etc/routelatch/tun.json`, `/usr/libexec/routelatch/tun-service`,
  `/etc/tmpfiles.d/routelatch-tun.conf` and the runtime lease; the socket and
  service were inactive, no unit was loaded, and `/run/routelatch-tun` was
  gone.

## Findings and residuals

1. **Minor product observation.** `tun uninstall` removes the helper files but
   leaves the two now-empty directories `/etc/routelatch` and
   `/usr/libexec/routelatch`; both were absent before setup. They are
   non-functional, but they are a residual host difference. They were removed
   manually with `pkexec rmdir` after the uninstall and are now absent
   (`real/07-final-post.txt`). This is an uninstall-cleanliness observation,
   not a coverage failure.
2. **Evidence tooling only.** The JSON-shape bug described above affected one
   check line and the human-readable active status print; it did not affect the
   product, the tray, the link, or the saved JSON. No product defect is implied.
3. **No product defect found** in the exercised tray enable/disable surface.

## Limits

- Menu actions were D-Bus `Event(id,"clicked")`, not physical panel/pointer
  clicks; there is no panel screenshot. Icon evidence is the exact 22×22
  pixmap delivered to the host, and off/on values match the earlier
  `H-TRAY.md` capture.
- No public traffic or destination flow was sent while coverage was on; TLS/
  HTTP/SOCKS destination behavior, idle HTTP pools and hijacked CONNECT
  teardown were not exercised in this window. The active window was about one
  second, so long-lived/idle behavior is not covered.
- One host, one session, one theme (Plasma 6.6.7/X11; the BreezeDark panel
  contrast result is from the earlier panel-contrast record). Wayland, GNOME
  and light themes remain untested.
- Helper error/conflict/fail-open paths were not exercised; only the real
  install/on/off/uninstall path. No second engine and no restart race.
- The live instance's own restarts are outside this test; the external restart
  before the baseline is recorded rather than attributed to this run.

## Hygiene

The only host changes were the helper units/binary/config/tmpfiles and runtime
directory installed by `tun setup`, the transient `rl-tun0` link and its fixed
routes, and removal of the two empty directories left by uninstall. No DNS,
firewall, desktop, autostart, proxy-service or other system setting was
changed; no product, docs or contract file was edited. The isolated app was
stopped and its tray item unregistered (the watcher list returned to its
pre-run contents). No tag or release was made. Scratch and home paths in the
committed evidence are shown as `<tmp>`/`<home>`.

## Locators

- `evidence/h-tray-host-2026-11-27/01-*.txt` — baseline environment, links,
  routes, rules, DNS hash, units, live PIDs and tray items.
- `evidence/h-tray-host-2026-11-27/real/` — the run: setup verification, off
  menu, enable confirmation, active menu/link/routes/icon/status, disable
  confirmation, off menu, restoration, uninstall and final host checks.
- `evidence/h-tray-host-2026-11-27/real/08-helper-journal.txt` — the helper
  unit's own `coverage is on` / `coverage is off` lines.
- `evidence/h-tray-host-2026-11-27/run-host.sh` — the runbook; `menu-dump2.py`
  and `icon-dump.py` are the menu/pixmap helpers copied from the earlier tray
  evidence.
