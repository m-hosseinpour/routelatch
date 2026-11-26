# RouteLatch

RouteLatch is a local proxy client for Linux that sends only the destinations
you approve through a pool of proxy exits. It builds the official Xray core
into one static Go binary, so there is no separate Xray install, no TUN device
and no root requirement.

You import share links (VLESS, Trojan or SOCKS5), approve destinations by
domain, domain suffix, IP or CIDR, and pick a mode. In the default `targeted`
mode, approved destinations go through the pool and everything else stays
direct. If no exit can serve an approved destination, the connection fails:
RouteLatch never silently falls back to direct for approved traffic.

## Features

- Loopback-only HTTP (`127.0.0.1:8317`) and SOCKS5 (`127.0.0.1:8318`)
  listeners; the addresses are configurable but must stay on `127.0.0.1` or
  `::1`.
- One approved-destination list with exact domains, domain/subdomain rules, IPs
  and CIDRs.
- Pool ranking with cooldowns, plus an honest distinction between an exit that
  confirmed a destination and one that only accepted the request. HTTP CONNECT
  retries a tunnel that dies before the destination's first byte on a different
  automatic exit (bounded, never a direct fallback, never from a manual pin).
- Share-link, link-file, subscription-URL and v2rayN import. Records that fail
  validation stay quarantined with a reason and are never dialed.
- Diagnostics that write **proposals** only; adding or removing a route needs
  your confirmation, and a failed request is never replayed.
- Opt-in GNOME/KDE desktop-proxy integration with an ownership-aware restore.
- A CLI, `--json` output on every command, a small interactive menu, a tray
  control surface for the running app and documented exit codes.

Changing mode closes connections that no longer match the new policy,
including in-flight dials; the change is reported successful only after that
sweep finishes.

## Requirements

- Linux on `x86_64` or `aarch64` for the supported runtime. Local packaging
  also cross-builds `riscv64` and `ppc64le`; those two are built but
  runtime-untested. The `amd64` build is the only one executed during testing
  so far.
- Go 1.27.4 (the version pinned in `go.mod`) only if you build from source. Set
  `GOTOOLCHAIN=local`.
- No root, TUN, firewall change or system-wide proxy setting. TLS verification
  uses the host CA bundle; RouteLatch does not install a root CA.
- Plain `routelatch run` (app/tray) needs a StatusNotifier tray host and
  permitted `pidfd_open`: Linux ≥5.3, not blocked by seccomp/EPERM. Otherwise
  use `routelatch run --headless` or the opt-in service.
- The optional desktop-proxy commands need a GNOME (`gsettings`) or KDE Plasma
  (`kwriteconfig6`/`kwriteconfig5`) session.

## Install

The current published release, `v1.1.0`, has 17 unsigned assets on the
[GitHub Release](https://github.com/m-hosseinpour/routelatch/releases/tag/v1.1.0):
four Linux tarballs (`amd64`, `arm64`, `riscv64`, `ppc64le`), four `.deb`
packages (Debian names `amd64`, `arm64`, `riscv64`, `ppc64el`), four RPM
`.spec` files, four binary RPMs (`routelatch-1.1.0-1.<arch>.rpm`) and a
`SHA256SUMS` file. `v0.1.1`–`v1.1.0` have the same 17-asset layout; the first
release, `v0.1.0`, published only the `amd64` and `arm64` tarballs plus
`SHA256SUMS`.

`v1.1.0` keeps the `v1.0.0` runtime model: plain `routelatch run` is the
detached app/tray, `run --headless` is the foreground engine, and packages
install no systemd unit, autostart entry or maintainer script. This release
adds the tray control surface, the additive `status.resume_requires_restart`
field and the tray-driven engine restart. **Upgrade from
`v0.1.3` requires stopping and removing its old user unit** (`/usr/lib/systemd/user/routelatch.service`)
before installing; a leftover unit would either lose its main process or start
an app where a service is expected. The opt-in service unit is created only by
`routelatch service install`. A package contains
the binary and the license/notice documents; the opt-in service unit is created
only by `routelatch service install`. The `amd64` and `arm64` tarballs also
ship a second binary, `bin/sing-box`: the unmodified, GPL-3.0 `sing-box` v1.14.4
proxy runtime, distributed as a separate program and used for hysteria2/sing-box
exits. Its per-binary SHA-256 is in `SINGBOX-INFO` and a written source offer is
in the notices. RouteLatch itself stays Apache-2.0; see [License](#license).

From D27 (2026-11-24), releases are built and published by the
`.github/workflows/release.yml` GitHub Actions workflow when an annotated `v*`
tag is pushed; no asset is built or uploaded from a maintainer's machine. It
verifies the pinned sing-box archives, runs the package checkers and publishes
only after every uploaded asset's name, size and SHA-256 digest matches the
build. `v1.0.1` is the first release produced this way; `v1.1.0` is the latest.

Verify a download before unpacking it. From the directory holding the downloaded
file and the release's `SHA256SUMS`:

```sh
sha256sum --check --ignore-missing SHA256SUMS
```

This prints `OK` for each file present in the directory whose digest matches,
skips missing files and exits non-zero if a present file fails. Do not install a
file whose check fails.

From a tarball:

```sh
tar xzf routelatch-v1.1.0-linux-amd64.tar.gz
cd routelatch-v1.1.0-linux-amd64
install -Dm755 bin/routelatch "$HOME/.local/bin/routelatch"
ln -sf routelatch "$HOME/.local/bin/rlch"
```

The last command creates `rlch`, a short alias for `routelatch`. The
`v1.0.0`–`v1.1.0` tarballs and packages ship that link; older published assets
predate it, and the manual `ln -s` works with an older binary too.

Or install a package (all release packages are **unsigned**; the only
integrity check is the generated `SHA256SUMS`):

```sh
sudo apt install ./routelatch_1.1.0_amd64.deb
sudo rpm -i routelatch-1.1.0-1.x86_64.rpm
```

Packages install `/usr/bin/routelatch` and its documents under
`/usr/share/doc/routelatch/` (the RPM keeps `LICENSE` and `NOTICE` in
`/usr/share/licenses/routelatch/`). A package built from this revision also
installs `/usr/bin/rlch` as a symlink to the main binary. It ships no systemd
unit, autostart entry or maintainer script; the package does not start or enable
anything.

## Runtime model in this revision

- **`routelatch run`** starts the app with a tray icon. It detaches, returns
  after real readiness (`status.ready`, plus open listeners unless the saved
  mode is `stopped`), and keeps running after you close the terminal. Quit from
  the tray icon stops it.
- **`routelatch run --headless`** is the foreground engine: no tray, blocks in
  the terminal, Ctrl-C stops it cleanly. Use it for SSH, containers, scripts and
  systemd.
- **`routelatch autostart enable|disable|status`** manages an XDG autostart
  entry for the default instance. All three verbs reject `--data-dir` with exit
  `2`; for a custom data directory use
  `routelatch service install --data-dir <dir>`.
- **`routelatch service install|uninstall|status`** is the opt-in systemd
  **user** unit for a server/VPS. It writes and enables
  `<manager-config-base>/systemd/user/routelatch.service`, whose command is
  `<absolute> [--data-dir <abs>] run --headless`. Without `--data-dir` the
  manager's XDG config/state bases are pinned in the unit. It does not use sudo;
  when lingering is off it prints the `sudo loginctl enable-linger <user>` step.
- **`routelatch autostart` and `routelatch service install` are mutually
  exclusive** for one user. Each setup command refuses while the other managed
  setup exists, and `status` reports the conflict. Pick one.

App mode needs a StatusNotifier tray host and permitted `pidfd_open`: **Linux
≥5.3**, not blocked by seccomp/EPERM (R-21). GNOME without an extension and
SSH/no-display sessions have no tray host. A refused `pidfd_open` exits `7` with
reason `pidfd_unavailable` before anything is spawned. A missing tray host exits
`7` with reason `tray_unavailable`: the preflight rejects it before any engine or
tray child starts, and if a spawned tray child reports it anyway, startup aborts
and stops whatever it already started. Either way no engine or listener is left
behind. `run --headless` and the opt-in service do not need either capability.

### Tray control surface

The tray is a thin client over the same local management socket as the CLI. It
does not open a listener, read or write state files, manage systemd, or contain
routing/pool policy of its own. The menu is in English in this release.

| Menu | What it does |
| --- | --- |
| Status line | Mode, usable exits and open connections; `Stopped`, `Restarting` and `Engine unavailable; retrying…` are explicit states. |
| Mode | Switch between targeted, all-proxy and all-direct; `Stop listening…` closes the traffic listeners. In stopped mode, `Restart engine to resume…` replaces the engine process first. |
| Proposals | Pending suggestions with Accept, Reject and Recheck; `Show decided` and `Clear decided/expired…`. |
| Approved rules | The approved list with per-rule removal and details, 20 rows per page. Add a rule from the Terminal menu. |
| Pool | Counts, auto/pinned exit, groups by health, 20 rows per page, refresh, re-rank, test selected/first 20, and remove with confirmation. Imports stay in the Terminal menu. |
| Desktop proxy | Status and refresh, plus Set/Re-apply and Restore behind a confirmation. Only GNOME (`gsettings`) and KDE Plasma (`kwriteconfig`) are supported. |
| Diagnostics | Run doctor, doctor results, warnings, the log path, and the proxy environment. |
| Data | Back up state now (the backup is written under the data directory). Restore and arbitrary paths stay in the CLI. |
| Settings | Start at login on/off via the XDG autostart entry, and a read-only config/path summary. |
| Advanced | Restart engine with confirmation, and Terminal menu. |
| Last action | Short, sanitized result of the most recent action or job. |
| Quit | Plain quit when the desktop proxy is not applied. If it is applied, quit becomes the explicit restore-and-quit or keep-and-quit choice. Quit never restores the desktop proxy silently. |

A destructive action (stopping, restarting, removing a rule or exit, clearing
proposals, setting/restoring the desktop proxy, restore-and-quit) first shows
`Confirm: …` and `Cancel`. Only one confirmation exists at a time, it expires
after 60 seconds, and a new action replaces it. Operations that need typed
input (adding a rule, importing a pool, diagnose, `config set`, restoring a
backup) stay in the CLI; `Terminal menu…` opens that menu in `konsole`,
`gnome-terminal` or `xterm`, or shows the exact manual command when none is
installed.

Limits: the tray needs a StatusNotifier host, so GNOME without an extension and
SSH/no-display sessions still use `--headless`. Linux Wayland and GNOME were not
tested for this surface; the acceptance run used Plasma 6 on X11. Starting at
login is offered only for the default data directory (a `--data-dir` session
shows why it is unavailable). Commands and lists that need a live engine are
disabled while the engine is unavailable or stopped. Dynamic labels are
sanitized and bounded, and notifications/logs carry counts, modes, short IDs
and paths, never destination hostnames, share URLs or credentials.

### Upgrading from the old packaged unit

Packages up to `v0.1.3` shipped a user unit whose `ExecStart` was the old
foreground `routelatch run`. Stop and remove it before upgrading; a leftover
unit would either lose its main process or start an app where a service is
expected.

```sh
systemctl --user disable --now routelatch.service
sudo rm -f /usr/lib/systemd/user/routelatch.service   # old package unit
rm -f "$HOME/.config/systemd/user/routelatch.service"
rm -f "$HOME/.config/systemd/user/default.target.wants/routelatch.service"
systemctl --user daemon-reload
# upgrade the package or replace the tarball binary, then choose one:
routelatch autostart enable    # or: routelatch service install
```

If the manager's config base differs from your shell's `$XDG_CONFIG_HOME`,
check `systemctl --user show-environment` and remove the unit/symlink there too.
An upgraded package no longer ships the `/usr/lib` unit, but a leftover user copy
would still name the old unit.

During the one-release migration window, a new engine against the **same data
directory** probes the old engine's legacy lock and refuses with exit `4` before
binding, **even if the old engine is in `stopped` mode without listeners**. An
old default engine plus a new `--data-dir` instance are independent and can
collide on ports, so stop the old instance explicitly. A custom
`XDG_RUNTIME_DIR` on an old default engine may be unguessable and invisible to
the probe; always stop the old unit explicitly. `rlch` is unchanged.

Debian and RPM use one unified version string derived from `BUILD-INFO` in a
single step: a leading `v` is stripped and hyphens become dots
(`v0.1.2-8-g42b9e4e` → `0.1.2.8.g42b9e4e`). Both the `.deb` `Version` field and
the spec `Version:` field carry that exact value.

`package.sh` generates and checksums the specs, while a binary RPM needs
`rpmbuild`. CI builds and inspects all four binary RPMs with `rpmbuild`/`rpm`.
The published `v0.1.1`–`v1.0.0` RPMs were built separately from the same specs
with the same extracted rpm 6.0.1 toolchain. From `v1.0.1` the `Release` workflow
builds the published RPMs in CI with the release version, while the
`rpm-packages` job in the regular CI keeps its CI-only stamp and is not shipped.
The spec has no service-enable scriptlets and no `BuildArch`: the architecture
comes from `rpmbuild --target`. There is no dnf/yum repository and no GPG
signing. Windows/macOS builds, 32-bit builds and a package repository remain
future work.

For a tarball install, `~/.local/bin` must be on `PATH`. Start it whichever way
fits the session:

```sh
routelatch run                 # desktop app/tray (detached, survives the terminal)
routelatch run --headless      # foreground, SSH, container or script
routelatch autostart enable    # opt-in login start for the default instance
routelatch service install     # opt-in systemd user service (server/VPS)
```

Autostart/service lifecycle, failure exits, uninstall and migration are
documented in [deploy/README.md](deploy/README.md); package contents and
upgrading from the old unit are described in
[packaging/README.md](packaging/README.md).

From source:

```sh
export GOTOOLCHAIN=local
./scripts/build.sh
```

This produces static `CGO_ENABLED=0` binaries, `SHA256SUMS` and `BUILD-INFO`
under `dist/` (gitignored):

```
dist/routelatch-linux-amd64
dist/routelatch-linux-arm64
dist/routelatch-linux-riscv64
dist/routelatch-linux-ppc64le
dist/SHA256SUMS
dist/BUILD-INFO
```

`VERSION`, `COMMIT` and `SOURCE_DATE_EPOCH` can be set for a named build;
`./scripts/build.sh --reproducible` checks that the same inputs give identical
bytes. `./scripts/package.sh` then writes reproducible tarballs, `.deb` files
and RPM `.spec` files under `dist/packages/`, and `./scripts/check-package.sh`
checks their checksums, layout and metadata (including the RPM specs' version,
architecture, license layout and absence of scriptlets).
`./scripts/check-artifact.sh` verifies the amd64 binary is statically linked and
starts with an empty environment. The arm64, riscv64 and ppc64le artifacts are
compiled and checksummed but cannot be executed on an amd64 host; riscv64 and
ppc64le are cross-build-only and runtime-untested, and the sidecar is pinned
only for amd64/arm64.

## Quick start

Every command below is written as `routelatch`. A build from this revision also
installs `rlch` as a shorter name for the same program and options; use whichever
you prefer.

1. Start RouteLatch. On a desktop, plain `run` starts the app/tray, returns once
   it is ready and keeps running after the terminal closes; use the tray icon to
   Quit. In a terminal/SSH session use the foreground engine, where Ctrl-C stops
   it cleanly:

   ```sh
   routelatch run              # desktop app + tray
   routelatch run --headless   # foreground terminal/SSH/container
   ```

   It prints the two listener addresses. The CLI talks to the running process
   over a private Unix socket; most commands need it. `status` without a running
   instance exits 3 with a clear message, while `doctor`, `backup`, `restore` and
   `desktop proxy restore` also work offline. App mode exits `7` when no tray
   host is available or pidfd is blocked; switch to `--headless` then.

2. Import one or more exits:

   ```sh
   routelatch import link 'vless://<uuid>@203.0.113.10:443?security=tls#name'
   routelatch import file ~/my-nodes.txt
   routelatch import subscription https://example.com/subscription
   routelatch import v2rayn ~/v2rayN-export.json
   ```

   Records that fail validation remain in the pool as `invalid` or
   `unsupported` and are never dialed. A link that only disables TLS
   verification is quarantined as `insecure_tls_removed`; certificate pinning is
   the supported alternative.

3. Approve the destinations that may use the pool:

   ```sh
   routelatch rules add api.example.com
   routelatch rules add 203.0.113.8
   routelatch rules add --kind domain_suffix example.com
   routelatch rules list
   ```

   A rule has no port and covers all ports. An executable that resolves a name
   itself and connects to the IP is matched by IP, not by the domain rule.

4. Select a mode. `targeted` is the default, so this step is only needed after a
   change:

   ```sh
   routelatch mode targeted
   ```

5. Point programs at the proxy. For a single child process:

   ```sh
   routelatch exec -- curl https://api.example.com/
   ```

   `routelatch env` prints the four proxy variables for manual wiring. Programs
   can also be configured with HTTP proxy `http://127.0.0.1:8317` and SOCKS5
   proxy `127.0.0.1:8318`.

6. Optionally point the desktop proxy at the listeners:

   ```sh
   routelatch desktop proxy status
   routelatch desktop proxy set
   routelatch desktop proxy restore
   ```

   `set` and `restore` are explicit commands; starting or stopping the service
   never changes desktop or network settings. `status` reports `conflict` when
   a key was changed outside RouteLatch. You can run `set` again after such an
   edit: it re-applies RouteLatch's values and records the keys you changed by
   hand as their new restore point. Other keys keep their original restore
   values, so `restore` undoes the proxy completely and returns only the edited
   keys to what you had set by hand. A change of desktop environment or
   listener addresses still needs `restore` first.

Check health and configuration with `routelatch status`, `routelatch doctor`,
`routelatch pool list` and `routelatch config get`. The full command list is in
`routelatch help`; the JSON envelope and exit codes are specified in
[docs/contracts.md](docs/contracts.md) §7.

## Modes and the fail-closed rule

| Mode | Behaviour |
| --- | --- |
| `targeted` (default) | Approved destinations use the pool; everything else is direct. |
| `all-proxy` | All public destinations use the pool. Loopback and private/LAN destinations stay direct so local services keep working. |
| `all-direct` | Nothing uses the pool; the listeners stay up. |
| `stopped` | Both traffic listeners close; only the management socket remains. Leave `stopped` by restarting the process. |

The fail-closed rule applies in `targeted` and `all-proxy`: if the pool cannot
serve an approved destination — empty pool, every exit cooling down, or a failed
dial — the request fails with an error. Pool failure does not turn into a direct
connection, and no hidden fallback listener exists.

## Security and privacy

- **Public exits are untrusted.** An exit can observe and modify traffic that
  passes through it and sees destination names and any plaintext HTTP. Do not
  send sensitive traffic through an exit you do not trust.
- **No HTTPS interception.** TLS verification stays on, no root CA is installed
  and no certificate is bypassed. An absolute-form `https://` request is refused
  as `unsupported` rather than proxied or decrypted.
- **Any local process can use the listeners.** They are loopback-only and
  unauthenticated by design. Another process or user on this machine can send
  traffic through your pool; this is not an access-control boundary.
- **Management is separate.** State changes go through a Unix stream socket with
  mode `0600` in a `0700` directory, protected by a same-uid `SO_PEERCRED`
  check. That protects against other users, not against root or another process
  running as you.
- **DNS is unchanged.** Direct destinations use your system resolver. A hostname
  sent through the pool is resolved at the exit (only the exit server's own name
  is resolved locally), so the exit and its resolver see that name.
- **No access log.** Destinations carried through the app are not written to the
  log, state, config, backups or a proposal history. What is stored is the
  approved list, pool records (including credentials), health summaries and
  configuration, in private `0600` files. Proposals live only in the running
  process's memory and disappear on restart.
- **Diagnostics propose, never apply.** Only eligible direct failures create
  proposals; the failed request is not replayed and no rule changes without your
  confirmation.

RouteLatch cannot make you anonymous, and it cannot protect traffic that never
reaches its listeners or log data produced by your OS, resolver, firewall or
other software.

## What it does not cover

- **No TUN and no device-wide capture.** Only traffic sent to RouteLatch's
  listeners is affected. Programs that ignore `HTTP_PROXY`/`HTTPS_PROXY`/
  `ALL_PROXY`, or that were started outside `routelatch exec`, are not routed.
- **No UDP or QUIC.** SOCKS5 `UDP ASSOCIATE` and `BIND` are refused.
- **No system DNS change** and no fake DNS.
- **No LAN listener and no authentication.** Sharing the proxy across a network
  is not supported.
- **No Windows, macOS, Android, 32-bit Linux or GUI** in v1. No apt/dnf
  repository or package signing; the published `.deb` and RPM files are
  unsigned.
- **No system-wide "covers everything" mode**; `all-proxy` keeps the documented
  loopback/private exception.

## Verified status

The current acceptance suite
([`internal/acceptance/`](internal/acceptance/README.md)) drives the shipped
binary as a real process against loopback-only fixture exits and peers. The
R13-E run prints **274 PASS, 0 FAIL and 15 NOTE in about 120 s**. NOTE means a
gated or impossible check, not a pass; the gated tests stay skipped unless a
real desktop or authorized user-manager check is enabled.
`go build ./...`, `go vet ./...` and `go test -count=1 ./...` are green.

Packaging was verified for this revision in the final green release dry-run
`45463989732` (commit `02e3226`, the same workflow blob as the guard-fix run
`45422318159`): four tarballs, four unsigned `.deb` files and four RPM specs
were built in CI, the four binary RPMs were built and inspected there, two
packaging runs produced an identical `SHA256SUMS`, and the final 17-file set
passed `scripts/release-assets.sh verify`. `scripts/check-artifact.sh` passed for
the amd64 artifact, and `scripts/check-package.sh` passed for every architecture
(metadata, payload paths, binary bytes, spec version/arch/layout, exact
checksums, and **no systemd unit, autostart entry or maintainer script**). This
batch also re-ran `package.sh`/`check-package.sh` locally in an isolated `DIST`
for amd64/arm64 in local mode; the release `PACKAGE-NOTES`/spec text is produced
only under `GITHUB_ACTIONS=true`, so that mode is checked in the CI dry-run.
Only `amd64` was executed; `arm64`/`riscv64`/`ppc64le` were cross-built and are
runtime-untested.

Beyond the default suite, the Linux build was historically exercised on real
system components and gated loopback checks: the old packaged unit under a real
`systemd --user` manager (start/stop/restart, port collision, SIGKILL restart),
corrupt state under an isolated temporary unit, real KDE Plasma desktop-proxy
set/restore/conflict/crash recovery, a limited public-exit case across four
exits, WS/gRPC/XHTTP transports over TLS, a gated 30s single-tunnel WS/TLS
reuse check, a gated hysteria2 traffic check plus a separate
`RL_ACCEPT_HYSTERIA2_OBFS_TEST` cell (salamander and gecko), a rerun of both
hysteria2 cells against the `bin/sing-box` sidecar from the published v0.1.2
linux-amd64 tarball (byte-identical to the pinned official archive), an
`x86_64` RPM payload install-and-execute check, and automatic start-up after a
real reboot with state and desktop-proxy settings intact. The old unit
lifecycle/corrupt-state cells were retired with the packaged unit; the opt-in
`service install` path is covered by fake-manager unit tests, and a real
install/uninstall/rollback and linger remain unverified on this host. The
desktop-proxy test changes a real desktop setting and is opt-in.

Three different kinds of gap are kept separate:

- **Outside v1 by design, not untested:** REALITY/ECH, mKCP, HTTPUpgrade and
  mux/XUDP are unsupported by the product; XHTTP over h3/QUIC is excluded by the
  no-UDP/QUIC policy. Windows, UDP/TUN, Android and GUI are later milestones that
  need a new decision.
- **Implemented but not yet exercised:** long hysteria2 sessions.
- **Deferred by user decision (2026-11-21), or not yet proven by us:**
  arm64/riscv64/ppc64le runtime execution (deferred; only amd64 is exercised, the
  others are cross-build only, so near-term product evolution stays on amd64), a
  real GNOME session (deferred, low priority; the desktop test ran on KDE), a
  real install plus running service under an RPM (the payload ran in a disposable
  container without systemd), and real public-exit scale (A04/A06 at scale plus
  the other items in the remaining evidence group), which is authorized as the
  next batch on an isolated instance but not executed yet. A01 clean-machine
  install was accepted on 2026-11-21 from user attestation: they removed the local
  build and installed the released v0.1.2 `.deb`, which worked. We did not run a
  clean-environment test ourselves.

The project is a working Linux v1, not a finished multi-platform product.

## Documentation

The design documents are written in Persian; the public entry points are in
English:

- [Requirements and decisions](docs/requirements.md)
- [Architecture and security design](docs/architecture.md)
- [Technical and security contracts](docs/contracts.md)
- [Research and evidence](docs/research.md)
- [Implementation plan and open work](docs/implementation-plan.md#open-work)
- [Acceptance suite notes](internal/acceptance/README.md)
- [Documentation index](docs/README.md)
- [Contributor guidance](CONTRIBUTING.md) and [security policy](SECURITY.md)
- [Repository rules for work in this tree](AGENTS.md)

## License

RouteLatch is licensed under the **Apache License, Version 2.0**. The full text
is in [`LICENSE`](LICENSE); project attribution is in [`NOTICE`](NOTICE). The
binary also includes third-party Go modules under MIT, BSD-2/3-Clause,
Apache-2.0, MPL-2.0 and LGPL-3.0 licenses. Exact versions, notices and license
texts are in [`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md); the audit
method and the MPL-2.0/LGPL-3.0 obligations are documented in
[docs/licensing.md](docs/licensing.md).

A release package may also include `bin/sing-box`, an unmodified, separately
shipped GPL-3.0-or-later executable (pinned v1.14.4) used as a second proxy
runtime. It is an independent program, not linked into RouteLatch; the full
GPL-3.0 text, the verbatim upstream `LICENSE` with its added naming clause and
the written source offer are in
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md), with the compliance
assessment (not legal advice) in [docs/licensing.md](docs/licensing.md). Shipping
it does not change RouteLatch's Apache-2.0 license.

RouteLatch is not an official Xray client and does not imply affiliation with
XTLS.
