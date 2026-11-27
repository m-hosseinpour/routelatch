# R16-H — tray Coverage surface and icon (2026-11-27)

> **Dated acceptance record.** The final run was made at `7ff128c`; the original
> run at `e4859db` is kept as separate evidence. Both ran on the authorized
> Plasma/X11 session with isolated data dirs and sockets, no host TUN, helper,
> service, autostart or desktop-setting change. This covers the R16-F tray
> surface only; it is not the full A17 acceptance and not a release decision.

**Status: on `7ff128c` the real helper-missing/off/error path passes at the
D-Bus/host-protocol level. Active, starting, conflict, incompatible and unknown
states were rendered by the unchanged product tray against a fake status peer and
are labelled visual-only. A real TUN-active icon or tray-initiated enable, panel
rendering/contrast, and Wayland/GNOME remain untested.**

## Revision, environment, method

| Item | Value |
| --- | --- |
| Final HEAD | `7ff128ced6fc0f39a5301521a92bdd703f79d996` (clean tree at build time) |
| Earlier run | `e4859db` (same harness, kept in `real/` and `fake/`) |
| Binary | `<tmp>/r16h-final/bin/routelatch`, `CGO_ENABLED=0`, built from `7ff128c`, unstamped (`dev`) |
| Desktop | Plasma 6.6.7, kwin 6.6.4, X11 `:0`, session bus style `unix:path=/run/user/<uid>/bus` |
| Host | Ubuntu 26.04.1, kernel 7.0.0-44, systemd 259 |
| Real run | `<tmp>/r16h-final/real-20261127T100354Z`, ports 48907/34959, 10:03:54Z–10:03:58Z |
| Fake run | `<tmp>/r16h-final/fake-20261127T100441Z`, 10:04:41Z–10:06:09Z |
| Live instance | PIDs 4099/4112/4329 untouched; it owns its own bus item and was never addressed |

- **Real run:** `routelatch --data-dir <isolated> run`; the product supervisor
  started its own tray child. No helper is installed, so the real engine reported
  `helper=missing`; `tun on` failed with exit 7 (`tun_helper_unavailable`).
- **Fake run:** the unchanged product `routelatch __app-tray` was launched with a
  parent pidfd, control/event pipes and `ROUTELATCH_TRAY_SOCKET` pointing at a
  Python Unix-socket peer. That peer answered `status` and the two TUN ops with
  driver-supplied JSON and recorded every request. This is a **visual-only fake
  control/status server**, not a real engine.
- Menu layouts were read with `GetLayout` over D-Bus (`menu-dump2.py`, python-dbus,
  exact labels); actions were triggered with `Event(id,"clicked")`. Nothing was
  clicked on the physical panel.
- Between `e4859db` and `7ff128c` the tray code paths (`internal/trayui`,
  `internal/app/tray.go`) are unchanged by `git diff` (source-inspected); the
  control changes did not alter any observed tray label, icon colour or request
  count. The final run ties the reproduced tray behaviour to `7ff128c`.

## Verified on `7ff128c`

| Area | Result | Evidence |
| --- | --- | --- |
| Real helper-missing state | `Coverage: off · desired off`; `Helper: missing · run 'sudo routelatch tun setup' in a terminal`; Enable disabled with the same reason; Disable disabled `coverage is already off`; clicking the disabled Enable made no pending and no job; the tray logged `click rejected: the menu node is disabled` | `final-7ff128c/real/coverage-off-missing.txt`, `layout-after-disabled-enable-click.txt`, `tray-click-rejected.txt` |
| Real desired-on failure | after `tun on` (exit 7, `tun_helper_unavailable`): `Coverage: unavailable · desired on · not active`, `Error: TUN service unavailable`, warning `Service: coverage is not running`, Disable **enabled** | `final-7ff128c/real/status-1-desired.json`, `coverage-desired-on-missing.txt`, `tun-on-cli.out`, `tun-on-cli.err` |
| Real two-step disable (coverage reduction) | `Confirm: Disable system-wide coverage` + `Cancel` in the root; confirm cleared the wish and returned `state=off`; notification body `System-wide coverage off` | `final-7ff128c/real/layout-disable-pending.txt`, `status-2-off.json`, `notifications-summary.txt` |
| Fake active state | `Coverage: on · desired on`, `Interface: rl-tun0`, `Warnings (4)`: DNS-direct, UDP-blocked, desktop-overlap, all-direct; Enable disabled `coverage is already on`; Disable enabled | `final-7ff128c/fake/coverage-on.txt`, `warnings.txt` |
| Fake error/conflict | `Coverage: on · desired off · active without request`, `Error: coverage conflict`, conflict/all-direct/DNS/UDP warnings; Enable disabled `already on`; Disable enabled | `final-7ff128c/fake/coverage-conflict.txt` |
| Fake pending | `Coverage: starting · desired on`; Enable disabled `coverage is starting`; Disable enabled | `final-7ff128c/fake/coverage-starting.txt` |
| Fake helper-incompatible | `Helper: incompatible · rerun 'sudo routelatch tun setup'`; warning `Helper: missing or not usable`; Enable disabled with the incompatible reason | `final-7ff128c/fake/coverage-incompatible.txt` |
| Older payload without `tun` | `Coverage: unknown · status not reported`; both actions disabled `TUN status is unavailable from this engine`; icon has no badge and the tooltip has no TUN suffix | `final-7ff128c/fake/coverage-unknown.txt`, `tooltips.txt` |
| Icons | Exact `IconPixmap` bytes per state: `off` = hollow near-white ring in the bottom-right badge; `on` = green (`46,160,67`); `error` = orange `!` (`219,109,40`); `pending` = blue dash (`88,166,255`); no badge when TUN is unreported. Engine artwork underneath is unchanged | `final-7ff128c/real/icon-*.png`, `final-7ff128c/fake/icon-*.png`, `final-7ff128c/icon-states.png`, `icons.txt` |
| Safe tooltips | `… · TUN on`, `TUN off`, `TUN error`, `TUN starting`; state words only, no detail/error/destination | `final-7ff128c/real/tooltips.txt`, `final-7ff128c/fake/tooltips.txt` |
| Enable confirmation | `Confirm: Enable system-wide coverage (all host traffic)` + `Cancel`; the wording warns that the change is host-wide | `final-7ff128c/fake/layout-enable-pending.txt` |
| Pending lifecycle | Cancel removes the pending and sends nothing; a second action replaces the pending (only the new `Confirm:` remains); after 62 s the pending expires on its own; all three sent zero `tun.enable` requests | `final-7ff128c/fake/layout-pending-replaced.txt`, `layout-after-stale.txt`, `request-counts.txt` |
| No duplicate request | after the fake confirmation, replaying the same confirm id did not send a second `tun.enable`; the run ended with exactly one `tun.enable` and one `tun.disable` | `final-7ff128c/fake/request-counts.txt`, `requests-summary.txt` |
| Output safety (D21) | URL-shaped `interface`, `last_error`, `detail` and warning id injected through the fake status were not rendered: invalid interface row absent, unknown reason rendered as the fixed `Error: coverage error`, unknown warning dropped (`Warnings (0)`); no `canary`/URL in Coverage labels or notification bodies | `final-7ff128c/fake/coverage-inject.txt`, `notifications.log` |

The same labels and icon colours were produced by the original `e4859db` run
(`real/`, `fake/`); the final directory is the authoritative evidence for the
current revision.

## Real vs fake vs untested

- **Real runtime on `7ff128c`:** the real engine and supervisor, isolated
  data-dir, no helper. Verified `helper=missing`, `state=off`/`desired=false`,
  the typed `tun on` failure (`exit 7`, `tun_helper_unavailable`), the resulting
  `unavailable · desired on` Coverage menu, the real two-step disable, the real
  `Notify` body, the real disabled-item rejection, and the three real icons
  (off, error, back to off).
- **Fake status (visual-only):** active/`on`, `starting`, `conflict`,
  `helper=incompatible` and the older payload without `tun`. The **active TUN
  icon**, green `on` badge, enable success path and `Warnings (4)` combination
  came from the fake peer. The four warnings were deliberately shown together
  for visual coverage; that is not a valid runtime combination and proves
  rendering, not TUN behaviour. `helper=incompatible` was fake; the real host
  reports `missing`. Notifications were verified as D-Bus `Notify` bodies, not
  visually.
- **Untested:** see below.

## Not verified / untested

- **No real TUN-active icon or tray toggle.** Host helper installation/service
  enablement was not authorized; a real `TUN on` from the tray and a real
  active-coverage disable were not shown. This still requires installing the
  helper and is the main remaining gap.
- **No panel screenshot and no pointer interaction.** During the final run the
  lock screen was gone and plasmashell windows were present, but no panel
  capture or physical click was performed; icon evidence is the exact 22×22
  pixmap delivered to the host and menu evidence is the `GetLayout` tree.
  Badge contrast on the actual panel theme was therefore not observed.
- No radio/checkmark or submenu rendering check, no hover behaviour, no
  notification presentation, no rate-limit behaviour.
- Wayland, GNOME and other tray hosts (unchanged R-16 limit).
- DNS/UDP/IPv6/desktop-proxy overlap behaviour is out of scope here; only their
  warning labels were rendered.
- Real engine restart/fail-closed/lease and A17 network scenarios are other
  R16-H gates, not this run.

## Defects

- Product: none found in the exercised tray surface at `7ff128c`.
- Evidence tooling only: the R15 text-based `menu-dump.py` truncates/drops labels
  containing an apostrophe (the helper label appeared empty). `menu-dump2.py`
  reads the same tree through python-dbus and reports exact labels. The real-run
  notification summary used to grep labels that `dbus-monitor` does not print;
  it now uses `notify-summary.py`, and the summaries in this directory were
  regenerated from the raw logs. No product change is implied.

## Hygiene

No `tun setup/uninstall`, systemd unit/socket, autostart toggle, desktop-proxy,
DNS, firewall or network change; no root action. The live instance's PIDs and bus
item were untouched. Both temporary tray children were stopped and their bus
items unregistered (the registered-item list returned to its pre-run contents);
the fake sockets were removed. No commit, staging, or product/docs/plan edit;
only this evidence directory and this file were changed.

## Locators

- `real/` — original `e4859db` real-run records; `real-run.sh` is the runbook.
- `fake/` — original `e4859db` fake-status records; `fake-run.sh`,
  `fake-tray.py`, `states.json`, `apply.py` are the harness.
- `final-7ff128c/real/` — real helper-missing/off/error records for `7ff128c`.
- `final-7ff128c/fake/` — fake-status visual records for `7ff128c`; request
  counts and raw notification logs.
- `final-7ff128c/icon-states.png` — 10× nearest-neighbour composite of the final
  captured pixmaps; `final-7ff128c/README.txt` records the run details.
- Helpers: `icon-dump.py` (IconPixmap → PNG), `menu-dump2.py` (exact layout),
  `notify-summary.py` (Notify body extraction). The run scripts accept
  `REPO`/`WORK`/`BIN`/`EV` overrides so the same harness writes either evidence
  set; scratch paths are shown as `<tmp>`.
