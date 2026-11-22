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
- A CLI, `--json` output on every command, a small interactive menu and
  documented exit codes.

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
- The optional desktop-proxy commands need a GNOME (`gsettings`) or KDE Plasma
  (`kwriteconfig6`/`kwriteconfig5`) session.

## Install

The current release, `v0.1.3`, publishes 17 unsigned assets on the
[GitHub Release](https://github.com/m-hosseinpour/routelatch/releases/tag/v0.1.3):
four Linux tarballs (`amd64`, `arm64`, `riscv64`, `ppc64le`), four `.deb`
packages (Debian names `amd64`, `arm64`, `riscv64`, `ppc64el`), four RPM
`.spec` files, four binary RPMs (`routelatch-0.1.3-1.<arch>.rpm`) and a
`SHA256SUMS` file. `v0.1.1` and `v0.1.2` have the same 17-asset layout; the
first release, `v0.1.0`, published only the `amd64` and `arm64` tarballs plus
`SHA256SUMS`.
Local packaging produces the same layout with `scripts/package.sh`, which writes
the specs but not the binary RPMs. A package contains the binary, the systemd
user unit and the license/notice files. The `amd64` and `arm64` tarballs also
ship a second binary, `bin/sing-box`: the unmodified, GPL-3.0 `sing-box` v1.14.4
proxy runtime, distributed as a separate program and used for hysteria2/sing-box
exits. Its per-binary SHA-256 is in `SINGBOX-INFO` and a written source offer is
in the notices. RouteLatch itself stays Apache-2.0; see [License](#license).

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
tar xzf routelatch-v0.1.3-linux-amd64.tar.gz
cd routelatch-v0.1.3-linux-amd64
install -Dm755 bin/routelatch "$HOME/.local/bin/routelatch"
ln -sf routelatch "$HOME/.local/bin/rlch"
```

The last command creates `rlch`, a short alias for `routelatch`. Package builds
from this revision also ship that link; the manual `ln -s` works with an older
binary too. The published v0.1.3 assets predate the alias unless you create it
yourself.

Or install a package (all release packages are **unsigned**; the only
integrity check is the generated `SHA256SUMS`):

```sh
sudo apt install ./routelatch_0.1.3_amd64.deb
sudo rpm -i routelatch-0.1.3-1.x86_64.rpm
```

Packages install `/usr/bin/routelatch`, its documents under
`/usr/share/doc/routelatch/` (the RPM keeps `LICENSE` and `NOTICE` in
`/usr/share/licenses/routelatch/`) and a systemd user unit. A package built from
this revision also installs `/usr/bin/rlch` as a symlink to the main binary.
Packages do not start or enable the service; opt in with
`systemctl --user enable --now routelatch`.

Debian and RPM use one unified version string derived from `BUILD-INFO` in a
single step: a leading `v` is stripped and hyphens become dots
(`v0.1.2-8-g42b9e4e` → `0.1.2.8.g42b9e4e`). Both the `.deb` `Version` field and
the spec `Version:` field carry that exact value.

`package.sh` generates and checksums the specs, while a binary RPM needs
`rpmbuild`. CI builds and inspects all four binary RPMs with `rpmbuild`/`rpm`;
the published `v0.1.1`, `v0.1.2` and `v0.1.3` RPMs were built separately from the
same specs with the same extracted rpm 6.0.1 toolchain, while the CI-built RPMs
carry a CI-only stamp and are not shipped. The spec has no service-enable scriptlets
and no `BuildArch`: the architecture comes from `rpmbuild --target`. There is no
dnf/yum repository and no GPG signing. Windows/macOS builds, 32-bit builds and a
package repository remain future work.

For a tarball install, `~/.local/bin` must be on `PATH`. The systemd user
service is optional; install it only if you want the service supervised:

```sh
install -Dm644 share/routelatch/systemd/routelatch.service \
    "$HOME/.config/systemd/user/routelatch.service"
systemctl --user daemon-reload
systemctl --user enable --now routelatch.service
```

Without the service, run the process in a terminal with `routelatch run`.
Service lifecycle, failure exits and uninstall are documented in
[deploy/README.md](deploy/README.md); package contents are described in
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

1. Start the service in the foreground and keep the terminal open (Ctrl-C stops
   it cleanly):

   ```sh
   routelatch run
   ```

   It prints the two listener addresses. The CLI talks to the running process
   over a private Unix socket; most commands need it. `status` without a running
   service exits 3 with a clear message, while `doctor`, `backup`, `restore` and
   `desktop proxy restore` also work offline.

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
binary as a real process against loopback-only fixture exits and peers. A full
run at `15d2acb` prints **222 PASS, 0 FAIL and 13 NOTE in about 122 s**. The 13
NOTE lines are gated or impossible checks, not passes; the gated tests stay
skipped unless a real desktop or `systemd --user` session is available.
`go build ./...`, `go vet ./...` and `go test -count=1 ./...` are green.

Local packaging was also verified: four tarballs, four unsigned `.deb` files,
four RPM `.spec` files and four binary RPMs were built,
`scripts/check-package.sh` passed for every architecture (metadata, payload
paths, binary bytes, spec version/arch/layout and `SHA256SUMS`), and two
packaging runs produced identical checksums. Negative checks confirmed the RPM
gate fails on a wrong version, wrong architecture, injected `%post`, bad source
digest, wrong license location, unexpected sidecar content and a missing spec.
For `x86_64`/`amd64`, an RPM built from the generated spec was installed in a
disposable container with no scriptlets and no service enablement (the
container lacked `ca-certificates`, so the dependency check was skipped), and
its `routelatch` and `sing-box` payload binaries were executed. Only `amd64`
was executed; `arm64`/`riscv64`/`ppc64le` were cross-built and are
runtime-untested.

Beyond the default suite, the Linux build was exercised on real system
components and gated loopback checks: a real `systemd --user` service
lifecycle (start/stop/restart, port collision, SIGKILL restart), a corrupt
state under an isolated temporary `systemd --user` unit (truncated JSON, a
newer schema and an unreadable file each fail closed with exit 10 and no
listener; the file stays untouched and the documented `restore`/`chmod`
recovery works), real KDE
Plasma desktop-proxy set/restore/conflict/crash recovery, a limited
public-exit case across four exits, WS/gRPC/XHTTP transports over TLS, a
gated 30s single-tunnel WS/TLS reuse check, a gated hysteria2 traffic check
plus a separate `RL_ACCEPT_HYSTERIA2_OBFS_TEST` cell that covers both `obfs`
types (salamander and gecko), a rerun of both gated hysteria2 cells against
the `bin/sing-box` sidecar from the published v0.1.2 linux-amd64 tarball
(byte-identical to the pinned official archive), an `x86_64` RPM payload
install-and-execute check, and automatic start-up after a real reboot with
state and desktop-proxy settings intact. The desktop-proxy
test changes a real desktop setting and is opt-in, like the systemd test.

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
