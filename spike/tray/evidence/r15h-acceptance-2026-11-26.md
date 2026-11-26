# R15-H — final tray acceptance (2026-11-26)

> **Dated acceptance record.** It covers the authorized R15-H gate on this
> workstation: local checks, one gated Plasma/X11 run on an isolated data
> directory, D21 checks and documentation review. It is not a release decision
> and does not claim Wayland, GNOME, real desktop-proxy set/restore, or service
> installation.

**Status: gated acceptance run passed; required local checks passed; the
working-tree fixes below still need the orchestrator's commit and the final
independent review.** No tag, release, service change, autostart entry or real
desktop-proxy set/restore was performed.

## Revision and binary

| Item | Value |
| --- | --- |
| Base commit | `22cb97d` (`docs(plan): record the red intermediate CI run`) |
| Working tree | one binder ordering fix and two test changes made during this acceptance, see below |
| Binary | `dist-pkg/routelatch-linux-amd64`, static `CGO_ENABLED=0`, Go `1.27.4`, `GOTOOLCHAIN=local`; stamp `v1.0.1-16-g22cb97d-dirty` |
| Source for the run | the final check build (`scripts/build.sh`, `SOURCE_DATE_EPOCH` from the commit), i.e. the tree that the orchestrator will commit |

Three files changed while closing the gate:

- `internal/trayui/backend.go`: click targets are now published before the
  backend render, so a host that exposes a new label during an apply cannot
  resolve the following click against the previous tree. A failed apply still
  clears them.
- `internal/trayui/runner_test.go`: the two tests now wait for the binder commit
  (the same state `Click` reads) instead of racing the backend label.
- `internal/trayui/binder_test.go`: a regression test asserts that `Lookup`
  already returns the new tree while `Backend.Apply` is still running.

Reason: the first required `-race` run failed in
`TestRunnerUnavailableAndRecovery` (`click while unavailable = <nil>`). The
failure reproduced in 3/200 focused runs during the run; that raw loop log was
not archived. The controller's `Trigger` still rejected the action, so no
unavailable management call was made; the defect was the visible-label/
click-target ordering. After the fix, the deterministic binder regression test
(fails against the pre-fix ordering) and the final full race gate (109 s) are
the archived evidence; the separate high-count reruns (`-race -count=300` and
`-count=20`) reported green but their logs were not retained. `go test ./...`
finished in 134 s after the test additions.

## Checks (all run at `22cb97d` + the working-tree changes)

Raw logs: [`checks/`](r15h-2026-11-26/checks/).

| Command | Result | Duration |
| --- | --- | --- |
| `gofmt -l $(git ls-files '*.go')` | pass, no files | <1 s |
| `go vet ./...` | pass | <1 s |
| `go build ./...` | pass | 2 s |
| `go test -count=1 ./...` | pass (all packages, including acceptance) | 134 s |
| `go test -race -count=1 ./internal/trayui/... ./internal/app/... ./internal/control/...` | pass | 109 s |
| `scripts/build.sh` (four arches, isolated `DIST`) | pass; `amd64`, `arm64`, `riscv64`, `ppc64le` static with embedded stamp | 8 s |
| `scripts/check-artifact.sh` on the amd64 binary | pass: statically linked, stamp in human/JSON, `status` with an empty environment, clean SIGTERM exit 0 | 1 s |
| `scripts/package.sh` with `DIST` isolated and `REQUIRE_SINGBOX=1`, `SINGBOX_BIN_DIR=<repo>/dist/singbox` | pass; four tarballs, four `.deb`, four RPM specs, sidecar pinned | 43 s |
| `scripts/check-package.sh` (`REQUIRE_SINGBOX=1`) | pass; checksums, layouts, metadata, payloads, specs consistent | 10 s |

No host install, package install, service enable/start or autostart command was
used. The package step ran on files under `/tmp` only. The test/race commands
were rerun after the regression test and the package was rebuilt after the final
documentation edits; the binary source is the same as the earlier artifact
check.

## Gated KDE run

Environment: the same Plasma 6/X11 session as the R15-A/D2 evidence
(`DISPLAY=:0`, session bus `unix:path=/run/user/1000/bus`; host versions are in
`spike/tray/MENU-FINDINGS.md`). Data dir, XDG config/state/home/cache, ports and
the terminal were isolated; the terminal was a fake `konsole` that only records
argv.

| Item | Observed |
| --- | --- |
| UTC window | 2026-11-26 09:15:04Z → 09:15:40Z |
| Data dir | `/tmp/opencode/r15h/kde-20261126T091503Z/data` (0700) |
| Listeners | `127.0.0.1:42279` (HTTP), `127.0.0.1:58259` (SOCKS) |
| Supervisor / tray | 1663908 / 1663933 |
| Temporary tray item | `org.freedesktop.StatusNotifierItem-1663933-1` |
| Installed instance | PIDs 4765/4803/4955 stayed alive with their own item; no event was sent to it. Its `state.json` was written by its own periodic background at 09:12:32Z, 09:19:24Z and 09:28:32Z while the process start times stayed 2026-11-25; no command in this run addressed that instance. |
| Host desktop proxy | `kioslaverc` still points at the installed `127.0.0.1 8317`; none of the test ports appear in it |
| Installed autostart file | unchanged mtime (2026-11-24); no test autostart entry was created |

The menu was driven with `GetLayout` and `Event` through the item's
`/StatusNotifierMenu`; item ids were resolved from the returned labels, never
parsed back into commands. The drive script and dump helper are in
[`kde/`](r15h-2026-11-26/kde/); layouts are GetLayout-derived dumps
(`id|enabled|visible|toggle-type|toggle-state|label`).

### A16 result matrix

| Area | Result | Evidence |
| --- | --- | --- |
| Status header | pass: `Targeted · 25 usable · 0 open`, then stopped/restarting/unavailable headers | `kde/layout-initial.txt`, `kde/layout-stopped.txt`, `kde/restarting-header.txt`, `kde/unavailable-header.txt` |
| Mode radio | pass: `Targeted` is the only checked radio; choosing `Targeted` after the stopped restart updates the header. All-proxy/all-direct clicks were covered by R15-D2, not this run. | `kde/layout-initial.txt`, `kde/steps.txt` |
| Stop + confirmation | pass: `Stop listening…` → `Confirm: Stop listening`; stopped status `ready=true`, `listeners_open=false`, `mode=stopped`, `resume_requires_restart=true` | `kde/layout-stopped.txt`, `kde/status-stopped.json` |
| Restart engine | pass: confirmation, `Restarting engine…` observed, return only after the new engine; supervisor `engine_restarted` with generation/token | `kde/restart-log.txt`, `kde/restarting-header.txt` |
| Stopped/resume | pass: mode actions disabled while stopped, root `Restart engine to resume…`, after restart `Stopped · choose a mode to resume`, then a mode resumes service | `kde/layout-stopped.txt`, `kde/mode-after-resume.txt` |
| Engine unavailable | pass: engine SIGSTOP produced `Engine unavailable; retrying…`; SIGCONT recovered to `Targeted · …` without a restart | `kde/unavailable-header.txt`, `steps.txt` |
| Rules | pass: 25 rules, page 1/2 → 2/2, confirmed removal to 24 | `kde/rules-page1.txt`, `rules-page2.txt`, `rules-for-removal.txt` |
| Pool | pass: 25 records, `Untested (25)`, page 1/2 → 2/2; offline `Refresh all sources` job ended with `Sources refreshed` | `kde/pool-page1.txt`, `kde/pool-page2.txt`, `kde/layout-after-refresh.txt` |
| Sanitization | pass: injected ESC removed before D-Bus; long label bounded to 80 runes ending `…` | `kde/pool-page2.txt` |
| Proposals | pass: an unapproved canary produced a live pending proposal with Accept/Reject/Recheck; Reject removed it; no file occurrence | `kde/proposals.txt`, `proposal-canary.txt` |
| Desktop proxy (read-only) | pass: status `not applied (kde)`; Refresh status worked; Set/Restore rows were present and **not clicked** | `kde/desktop.txt`, `desktop-after-refresh.txt` |
| Diagnostics | pass: doctor ran, all 15 checks `[ok]`, log path notification, environment rows available | `kde/diagnostics.txt`, `diagnostics-env.txt`, `notifications.log` |
| Data backup | pass: backup written under the isolated data dir | `steps.txt`, `files-after-backup.txt` |
| Settings / autostart | partial: config and paths shown; `Start at login: Off` plus `Start at login unavailable: autostart manages only the default data directory`. The toggle is **not run** because this run is intentionally `--data-dir`. | `kde/settings.txt` |
| Terminal menu | pass with a fake `konsole`: argv was `-e <binary> --data-dir <abs> menu`; no shell or `$TERMINAL` | `kde/terminal-argv.txt` |
| Quit | pass for plain Quit: processes exited, item unregistered, supervisor log records the quit. Restore/keep quit rows were **not** shown because the isolated desktop state is not applied; they are unit/race-covered. | `kde/layout-final.txt`, `steps.txt`, `app.log` |
| Pagination | pass for rules and pool page navigation (20 rows per page) | layouts above |
| Notifications | pass: 12 RouteLatch notifications; bodies contain counts/status and the log path, no canary, URL or credential | `kde/notifications.log` |

### D21 and secret checks

- The canary `rl-canary-c47f6145672bd684.invalid:443` was sent once through the
  isolated HTTP listener, producing HTTP 502 and a live proposal. Before it was
  rejected, a recursive grep of the isolated data dir and XDG bases found no
  occurrence; after reject and after rule removal the same scan was empty.
- Final scan covered data, config, state, backup, launch/supervisor log, XDG
  files and notification bodies. Result: `none` for the canary; no
  `http(s)://`, `/home/...` or credential-shaped token in the log. The only
  `token=` strings are the numeric lifecycle sequence `token=1`/`token=2` in
  `supervisor.engine_restarted`, not a credential.
- The tracked diff `6026fec..HEAD` plus the modified working tree (55 files) was
  scanned for private home paths, user/account strings, private keys,
  bearer/API/password values and share links. No match. The new evidence files
  were checked separately for personal paths; no match. URL-shaped strings in
  the diff are test fixtures on reserved `.example`/`.invalid` names and
  `127.0.0.1`; see
  [`checks/secret-scan-diff.txt`](r15h-2026-11-26/checks/secret-scan-diff.txt).
  Limits: this is a pattern scan of the diff, not a whole-history credential
  audit; it cannot prove the absence of an unknown secret shape.

## Not run / limits (no stronger claim)

- **No screenshots in this run.** The menu was driven through D-Bus, not a
  pointer; the panel holds an installed product icon with the same empty
  `IconName`, so a safe visual locator for the temporary icon was not
  established. Radio/checkbox *property* state and depth-3 nodes are covered by
  the layout dumps; R15-A's screenshots remain the host-menu visual reference.
  A visual checkmark capture is still missing.
- **Wayland and GNOME were not tested.** This host is Plasma 6 on X11.
- **Real desktop-proxy set/restore was not run.** The isolated in-memory desktop
  view was `not applied`; Set/Restore were never clicked, and the host proxy was
  left pointing at the installed instance's 8317.
- **Autostart toggle was not run live.** A `--data-dir` instance correctly
  disables it with a reason; `enable/disable` behavior is covered by unit/race
  tests but not by this gated run. No autostart file was written.
- **Restore-and-quit / keep-and-quit were not run live**, because the isolated
  desktop state was not applied. Plain Quit passed; the applied/unknown variants
  are unit/race-covered.
- **Proposal Accept/Recheck/Clear, pool remove/test, and desktop set/restore
  were not clicked live.** Reject, rule removal, refresh, doctor, backup and the
  mode/stop/restart lifecycle were. The remaining actions are covered by the
  R15-E/F/G tests.
- Only the `amd64` artifact was executed. `arm64`, `riscv64` and `ppc64le` were
  cross-built and stamped but remain runtime-untested; riscv64/ppc64le remain
  outside the v1 runtime support matrix.
- No public exit or approved-destination traffic was used; the only request was
  a `.invalid` canary through loopback.

## Locators

- Check logs: [`r15h-2026-11-26/checks/`](r15h-2026-11-26/checks/) (`summary.txt`,
  per-check logs, `secret-scan-diff.txt`).
- Gated run: [`r15h-2026-11-26/kde/`](r15h-2026-11-26/kde/) (`run-env.txt`,
  `steps.txt`, layouts, status JSON, notifications, `app.log`).
- Drive script: [`r15h-2026-11-26/kde/r15h-kde-drive.sh`](r15h-2026-11-26/kde/r15h-kde-drive.sh);
  dump helper: [`r15h-2026-11-26/kde/menu-dump.py`](r15h-2026-11-26/kde/menu-dump.py).
- Committed check logs and the drive script have the workstation repo path
  replaced with `<repo>`; `/tmp` run paths are shown as-is.
- Contract: `docs/contracts.md §7.3`; requirements A16/D21/D28; prior R15-A/D2
  evidence in `spike/tray/MENU-FINDINGS.md` and
  `spike/tray/evidence/r15d2-kde-2026-11-26.txt`.
