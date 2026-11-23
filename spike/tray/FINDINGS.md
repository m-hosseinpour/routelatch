# R13-A — pure-Go tray spike: measurements and pin decision

> **Dated evidence snapshot (2026-11-23, spike/tray).** This file records what was
> implemented, source-inspected, and runtime-observed for one candidate comparison
> round. It is not the current product status. Raw outputs are under
> [`evidence/`](evidence/); the spike module is separate from the product module and
> nothing here is imported by `cmd/` or `internal/`.

**Status: completed, one candidate pinned for R13-B (`deedles.dev/tray v0.2.0`).**
Both required candidates pass the hard constraints when the application adds the
small D-Bus readiness/host probe that D26 already requires. Visual rendering was not
observed by a human, so icon/menu *appearance* remains unconfirmed.

No service, autostart entry, desktop setting, or existing tray item was changed. The
only desktop effects were: one tray item named `routelatch-tray-spike` per prototype
run, removed at exit, and two notifications per run labelled "RouteLatch spike".
Every prototype process was killed at the end; see [§9](#9-cleanup).

## 1. Toolchain, pins, and provenance

The host has no Go in `PATH`. `https://go.dev/dl/` and its JSON index were reachable,
but the download host (`dl.google.com/go/go1.27.4.linux-amd64.tar.gz`) returned
**HTTP 404** for every version tried, through the machine proxy and directly. The
official toolchain **module** zip was fetched instead from `proxy.golang.org` and its
content was verified against the public checksum database:

| Claim | Command | Observed |
|---|---|---|
| Toolchain version | `go version` | `go version go1.27.4 linux/amd64` |
| Toolchain zip authenticity | SHA-256 dirhash of the zip compared with `https://sum.golang.org/lookup/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64` | computed `h1:8Tl0rBb7KdISSdPkZBjlpZxsUBb+004GI/SM2XbmSqI=`, matches the sumdb record |
| Go module pins | `git ls-remote` for tags, `proxy.golang.org` `@latest` | `gogpu/systray v0.3.0` = `a71468fe4afe783a2b70c8af29ef7c9954bc96f7` (2026-08-30); `deedles.dev/tray v0.2.0` = `acf85aba49de5147897736198745658a5ed8d157` (2026-10-28) |
| Runtime dependencies | `go list -m all`, `go.sum` | `godbus/dbus/v5 v5.2.4`, `golang.org/x/sys v0.49.0`; gogpu additionally requires `go-webgpu/goffi v0.6.3` in its module graph, imported only by its Darwin files |

The toolchain was extracted under `/tmp/opencode`; no system package was installed.
The failed `dl.google.com` path is an environment limitation of this run, not evidence
that the official tarball is absent.

## 2. Candidates

| Candidate | License | Linked deps | SNI implementation | Watcher-absence behavior |
|---|---|---|---|---|
| `gogpu/systray v0.3.0` | MIT | godbus (BSD-2), x/sys (BSD-3) | SNI + dbusmenu + notifications; re-registers when the watcher restarts | **Silent**: `New()`/`Create()` return no error; failed registration is only an `slog.Warn` |
| `deedles.dev/tray v0.2.0` | MIT | godbus (BSD-2), x/sys (BSD-3) | SNI + dbusmenu; no notification API; no watcher-restart handling | `New()` returns an error naming both failed registrations |

No third candidate was added: the other libraries already surveyed
(`fyne-io/systray`, `getlantern/systray`) require cgo/GTK, and notification-only
libraries do not provide a tray item. Adding one would not change the decision.

**Connection leak in `deedles.dev/tray` (source-inspected and reproduced):** when
`New()` fails after connecting to the bus (for example, no watcher), it returns the
error without closing that connection; five failed attempts grew the process's file
descriptors from 4 to 11. Therefore R13-B must not retry `New()` in a loop while
waiting for the watcher; the probe-first pattern here and D26's
restart-by-process-replacement avoid it. gogpu has no error to retry because it hides
the failure.

## 3. Static build, four architectures

```sh
cd spike/tray
GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=<arch> \
  go build -trimpath -o /tmp/opencode/tray-build/<cmd>-<arch> ./cmd/<cmd>
```

All five commands (`systray-spike`, `tray-spike`, `supervisor`, `lifecycle-child`,
`fake-watcher`) built for `amd64`, `arm64`, `riscv64`, and `ppc64le`. All 20 artifacts
were checked with `file`; the two **amd64** tray binaries were additionally checked
with `ldd` and `go version -m`:

| Check | Scope | Result |
|---|---|---|
| `file` | all 20 artifacts | `ELF … statically linked` (20/20) |
| `ldd` | amd64 tray binaries | `not a dynamic executable` |
| `go version -m` | amd64 tray binaries | `CGO_ENABLED=0`; deps are exactly godbus + x/sys + the candidate; goffi is absent from the Linux build metadata |
| cgo sources | all commands | `go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./cmd/...` → empty |

Full output: [`evidence/build.txt`](evidence/build.txt).

## 4. Real KDE: registration, icon, menu, notification

Environment: KDE Plasma **6.6.7** (`plasmashell` on `:0`, `kded6` owns
`org.kde.StatusNotifierWatcher`, plasmashell owns `org.kde.StatusNotifierHost` and
the notification server). For each candidate, `scripts/kde-evidence.sh` started one
item, read it back with `gdbus`/`busctl`, clicked both menu entries through
`com.canonical.dbusmenu.Event`, and verified unregistration. Full logs:
[`evidence/kde-gogpu.txt`](evidence/kde-gogpu.txt),
[`evidence/kde-deedles.txt`](evidence/kde-deedles.txt).

| Check | gogpu/systray | deedles.dev/tray |
|---|---|---|
| Appears in `RegisteredStatusNotifierItems` | yes, `org.kde.StatusNotifierItem-<pid>-2/StatusNotifierItem` | yes, `org.freedesktop.StatusNotifierItem-<pid>-1/StatusNotifierItem` |
| `Id` property | bus name (`org.kde.StatusNotifierItem-…`), **ignores the requested id** | `routelatch-tray-spike` (honored) |
| `Title` / `Status` | `RouteLatch tray spike` / `Active` | same |
| `IconPixmap` | non-empty, `(22, 22, …)` ARGB | non-empty, `(22, 22, …)` ARGB |
| `Menu` path + `GetLayout` | `/MenuBar`, both labels + separator | `/StatusNotifierMenu`, both labels + separator |
| Menu click `Send test notification` | handler ran; both startup and click `Notify` calls observed | handler ran; startup `Notify` observed; the immediate click `Notify` was rate-limited by Plasma (`id=0`, error) |
| Notification path | built-in `ShowNotification`, **errors not observable** | one `godbus` `Notify` call; startup returned a server id |
| Menu click `Quit prototype` | handler ran, process exited | handler ran, process exited |
| Watcher after exit | item removed | item removed |
| Real host read the item | plasmashell called `GetAll` and `GetLayout` | same |

`dbus-monitor` saw two `org.freedesktop.Notifications.Notify` calls per run (startup
and menu click) carrying app name `routelatch-tray-spike`; recordings:
[`evidence/notify-monitor-gogpu.txt`](evidence/notify-monitor-gogpu.txt),
[`evidence/notify-monitor-deedles.txt`](evidence/notify-monitor-deedles.txt).
On the deedles run the second, nearly simultaneous notification was refused by
Plasma with `Created too many similar notifications in quick succession`; that is
server-side rate limiting, not a library failure.

Plasmashell's own queries are the strongest non-visual integration evidence:
[`evidence/host-queries-gogpu.txt`](evidence/host-queries-gogpu.txt) and
[`evidence/host-queries-deedles.txt`](evidence/host-queries-deedles.txt) show
`member=GetAll` against `/StatusNotifierItem` and `member=GetLayout` against the menu
path, sent by `:1.24` (plasmashell) immediately after registration.

**Defect observed in gogpu/systray:** `Remove()` is not idempotent. Calling it from
the menu callback and again during shutdown panicked inside `Destroy` with
`dbus: connection closed by user` while setting properties on the closed connection.
The prototype now guards cleanup with `sync.Once`; a product wrapper must do the same.

## 5. Absent watcher and absent host

`dbus-run-session` without a watcher, and the fake watcher with
`IsStatusNotifierHostRegistered=false`, produce explicit errors and non-zero exits;
nothing hangs. Both binaries run the same probe: own-name check for
`org.kde.StatusNotifierWatcher` then `org.freedesktop.StatusNotifierWatcher`, then the
watcher's `IsStatusNotifierHostRegistered` property.

| Scenario | gogpu/systray | deedles.dev/tray |
|---|---|---|
| No watcher, `-check-watcher` | exit **3**, `event=no_watcher` | exit **3**, `event=no_watcher` |
| Watcher, no host, `-check-watcher` | exit **5**, `event=no_tray_host` | exit **5**, `event=no_tray_host` |
| Watcher + host, `-check-watcher` | exit 0 | exit 0 |
| Library's own behavior with no watcher (`-raw-create`) | **no error returned**; only a warning log, exit 3 comes from the app probe | `New()` returns a joined error for both watcher names, exit 1 |

Evidence: [`evidence/no-watcher.txt`](evidence/no-watcher.txt),
[`evidence/host-detection.txt`](evidence/host-detection.txt).

Consequence: the app must own the watcher/host probe in either case, because D26 also
needs it for `status.ready` and tray-loss detection. For gogpu the probe is the *only*
detection layer; for deedles it duplicates (and strengthens) `New()`'s check. Property
reads try the bus name and both conventional watcher interfaces, so a watcher whose
name differs from its interface still reports "no host" rather than an error.

## 6. Parent death and child lifetime

Mechanism prototyped: the supervisor opens a **pidfd on itself before forking**
and hands it to the child through `ExtraFiles`; the child polls that descriptor and
therefore observes parent death even if the supervisor dies before the child arms
its watchdog. Without an inherited descriptor (standalone runs only) the child opens
a pidfd on its current parent and, when an expected supervisor pid is provided,
refuses to watch a different process. Pidfd setup failures (`ENOSYS`/`EPERM`/seccomp)
are explicit errors. The supervisor detects child exit with `wait(2)` on its direct
child. No library involvement.

| Measurement | Command | Observed |
|---|---|---|
| Supervisor `SIGKILL` → child exits (D-Bus-free) | `dbus-run-session -- scripts/parent-death.sh` | child gone after **14 ms** (external polling); `event=parent_died` 21 ms after `supervisor_ready` |
| Child self-exit → supervisor detects | same script | `event=child_exited detected_by=waitpid code=0 elapsed_ms=2002` |
| Supervisor exits **before** the child arms (arm-late race) | same script, case 3 (`-abandon-after=50ms`, `-arm-delay=500ms`) | child was reparented (`ppid=1897`), inherited pidfd became readable; gone **453 ms** after supervisor exit, at the arm attempt; no error, no silent watch of the new parent |
| Supervisor `SIGKILL` → real tray child exits and unregisters | live KDE, `supervisor + systray-spike -watch-parent` | child gone after **19 ms**; item removed from `RegisteredStatusNotifierItems` |
| Tray child self-exit → supervisor detects and item disappears | live KDE, `supervisor + tray-spike -seconds 2` | exit detected after 2020 ms, watcher list clean |
| pidfd unavailable | not executable here | error path implemented; `ENOSYS`/`EPERM` not simulated |

Evidence: [`evidence/parent-death.txt`](evidence/parent-death.txt),
[`evidence/live-parent-death.txt`](evidence/live-parent-death.txt),
[`evidence/live-child-exit.txt`](evidence/live-child-exit.txt).

`PR_SET_PDEATHSIG` was deliberately not used: Go may deliver it when the forking
thread exits rather than the process, so it is not the primary guarantee.

## 7. License and transitive dependencies

| Module | Version | License | Linked into Linux binary |
|---|---|---|---|
| `deedles.dev/tray` | v0.2.0 | MIT | only in `tray-spike` |
| `github.com/gogpu/systray` | v0.3.0 | MIT | only in `systray-spike` |
| `github.com/godbus/dbus/v5` | v5.2.4 | BSD-2-Clause | both |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | both |
| `github.com/go-webgpu/goffi` | v0.6.3 | MIT | **no** (Darwin-only import; module-graph only) |

No GPL/AGPL code is linked; all licenses are compatible with Apache-2.0
distribution. `go.sum` records 5 modules. `deedles.dev/tray`'s `go.mod` carries a
`tool` directive for golangci-lint, so `go list -m all` expands to 216 graph nodes and
`go mod graph` to 227 edges, but none of those are build dependencies or downloaded
checksums (`go list -deps` shows only the three modules above). Full listing:
[`evidence/licenses-deps.txt`](evidence/licenses-deps.txt).

Activity at pin time: `gogpu/systray` pushed 2026-11-22, 84 stars, latest release
2026-08-30; `DeedleFake/tray` pushed 2026-10-28, 10 stars, latest release 2026-10-28.
Both are maintained, both are small projects.

## 8. Decision

**Pin `deedles.dev/tray v0.2.0`** (`acf85aba49de5147897736198745658a5ed8d157`) for
the R13-B implementation, with the application owning:

- the watcher + host probe and registration verification (needed for readiness and
  tray-loss anyway),
- desktop notification through one `godbus` `Notify` call (the library has none),
- idempotent cleanup and process-level restart on watcher loss (the library does not
  re-register after a watcher restart; D26 already requires restart by replacing the
  tray process).

Why it wins over gogpu: it returns explicit errors instead of silently ignoring a
failed registration, honors the requested `Id` (desktop/notification identity), and
exposes errors from every operation. Both candidates link exactly godbus and x/sys,
so dependency size is not a differentiator; the tie-breaker is failure transparency
and identity, not a claimed library feature. Its one observed defect is the leaked
session connection when `New()` fails (see §2), which the no-retry design avoids.

**Documented alternative:** `gogpu/systray v0.3.0` if automatic re-registration after
a watcher/panel restart is valued above those properties. It also passes all runtime
checks here, but requires the app probe for watcher absence and a `sync.Once` around
`Remove()` (observed panic) and returns no errors from `New`/`Show`/`SetIcon`/
`ShowNotification`.

This decision selects a pin for R13-B; it does not add the dependency to the product
`go.mod`, which is R13-B's job.

## 9. Cleanup and limitations

Cleanup at the end of the run: `pgrep -af` found no `systray-spike`, `tray-spike`,
`supervisor`, `lifecycle-child`, or `fake-watcher` processes, and
`RegisteredStatusNotifierItems` contained no `routelatch-tray-spike` item.

Not verified:

- **Visual confirmation** of icon shape/menu rendering/notification popup by a human.
  All evidence above is D-Bus-level or process-level.
- GNOME without an extension, SSH/headless, and other desktops. "No watcher" and
  "watcher without host" were proven on an isolated bus with a fake watcher, not on a
  real GNOME session.
- Runtime behavior on `riscv64`/`ppc64le` (cross-build only, per project policy).
- Survival of a real `kded`/plasmashell restart while the item is running (source
  inspection only: gogpu re-registers on `NameOwnerChanged`, deedles does not).
- pidfd denial (`ENOSYS`/`EPERM`) and notification-server failures beyond the rate
  limit seen above.
- The fallback path used only for standalone runs (no inherited pidfd, no expected
  supervisor pid) still has a small arm-late window; app mode always uses the
  supervisor-provided pidfd, which case 3 covers.
- The fake watcher now updates `RegisteredStatusNotifierItems`, but only the probe
  branches were exercised; it is not a full desktop host.
- Long-run stability, leaks, and integration into the product package (`run`,
  readiness, locks); those belong to R13-B.

## 10. Reproduction

```sh
export PATH=/tmp/opencode/go/bin:$PATH GOTOOLCHAIN=local
cd spike/tray
CGO_ENABLED=0 go build ./...
for arch in amd64 arm64 riscv64 ppc64le; do
  for c in systray-spike tray-spike supervisor lifecycle-child fake-watcher; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
      -o /tmp/opencode/tray-build/$c-$arch ./cmd/$c
  done
done
dbus-run-session -- ./scripts/host-detection.sh /tmp/opencode/tray-build
dbus-run-session -- ./scripts/parent-death.sh /tmp/opencode/tray-build
DBUS_SESSION_BUS_ADDRESS=$DBUS_SESSION_BUS_ADDRESS \
  ./scripts/kde-evidence.sh /tmp/opencode/tray-build/systray-spike-amd64 gogpu
DBUS_SESSION_BUS_ADDRESS=$DBUS_SESSION_BUS_ADDRESS \
  ./scripts/kde-evidence.sh /tmp/opencode/tray-build/tray-spike-amd64 deedles
```
