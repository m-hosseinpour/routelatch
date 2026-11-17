# Packaging and install notes

RouteLatch ships as a single Go binary. Users do not need Go, Python, Node or a
separate Xray installation: the pinned Xray core is compiled into the binary.
This directory documents the package layout and the user's install steps. **The
build never installs anything** — no service is enabled, no desktop proxy or
network setting is touched, and nothing is published.

The default answer for "how do I get a package" is `scripts/build.sh` followed
by `scripts/package.sh`. Both are local build steps.

## Source tree layout

```
scripts/build.sh           linux/amd64 + linux/arm64 static build, dist/SHA256SUMS, dist/BUILD-INFO
scripts/package.sh         assembles the package trees and .tar.gz files under dist/packages/
scripts/check-artifact.sh  static-link and empty-environment smoke check for one binary
packaging/README.md        this file; it is shipped inside the package as INSTALL.md
deploy/systemd/routelatch.service
                           the one copy of the systemd user unit (the package copies it)
deploy/README.md           service lifecycle: install, check, uninstall, crash recovery
```

`dist/` is gitignored build output, not a distributed directory.

## Package layout

`scripts/package.sh` produces this tree per architecture:

```
routelatch-<version>-linux-<amd64|arm64>/
  bin/routelatch
  share/doc/routelatch/README.md        what RouteLatch is; quick start; limits
  share/doc/routelatch/INSTALL.md       this file
  share/doc/routelatch/BUILD-INFO       version, commit, date, Go version, cgo=0
  share/doc/routelatch/PACKAGE-NOTES    "local build, not distributable" notice
  share/routelatch/systemd/routelatch.service
```

and `dist/packages/SHA256SUMS` plus `routelatch-<version>-linux-<arch>.tar.gz`
for each architecture.

There are no runtime data files in the package. Config and state are created on
first run under `$XDG_CONFIG_HOME/routelatch/` and
`$XDG_STATE_HOME/routelatch/` (see `docs/contracts.md` §4.1), with `0700`
directories and `0600` files.

## Install (a user action)

```sh
tar xzf routelatch-<version>-linux-amd64.tar.gz
cd routelatch-<version>-linux-amd64

# binary for this user only
install -Dm755 bin/routelatch "$HOME/.local/bin/routelatch"

# optional systemd user service; same unit as deploy/systemd/routelatch.service
install -Dm644 share/routelatch/systemd/routelatch.service \
    "$HOME/.config/systemd/user/routelatch.service"
systemctl --user daemon-reload
systemctl --user enable --now routelatch.service
```

`~/.local/bin` must be on `PATH` for the `routelatch` command to resolve.
Without the service, run the foreground command in your own terminal instead:

```sh
routelatch run
```

Check the result with `routelatch status`, `routelatch doctor`, or
`systemctl --user status routelatch.service`. Uninstall, crash recovery and the
GNOME/KDE desktop-proxy restore step are documented in `deploy/README.md`; the
desktop proxy is opt-in and is not changed by installing or starting the
service.

## Architecture and platform

Only `linux/amd64` and `linux/arm64` are built. `uname -m` must report
`x86_64` or `aarch64`; there are no 32-bit or non-Linux packages. The build
targets both architectures on one machine, so an arm64 package can be produced
without an arm64 host — but it also has to be run on arm64 hardware to be
tested. Only the amd64 artifact was executed during this build.

## Verification performed

`scripts/build.sh` sets `CGO_ENABLED=0` and uses `-trimpath -buildvcs=false`.
`scripts/check-artifact.sh` confirms, for the amd64 artifact:

- `file` reports `ELF 64-bit LSB executable, x86-64, statically linked`;
- `ldd` reports `not a dynamic executable` and there is no `PT_INTERP` header;
- `env -i … version` and `env -i … --json version` report the version, commit and
  build date recorded in `dist/BUILD-INFO` (the script fails on any mismatch);
- `run` starts with `env -i` and temporary XDG directories, listens only on
  `127.0.0.1`, answers `status` with exit 0, and exits 0 on `SIGTERM`.

Reproducibility: `scripts/build.sh --reproducible` builds each architecture
twice with the same inputs and compares the checksums. Same toolchain, source,
`VERSION`, `COMMIT` and `SOURCE_DATE_EPOCH` give identical bytes.

## Version stamping

`scripts/build.sh` passes
`-ldflags "-s -w -X main.version=… -X main.commit=… -X main.buildDate=…"`. The
symbols are declared in `cmd/routelatch/version.go` and printed by
`routelatch version` (human and `--json`):

```
$ routelatch version
RouteLatch 80158d2-dirty
commit: 80158d2
build date: 2026-11-17T00:00:00Z

$ routelatch --json version
{"schema_version":1,"ok":true,"command":"version","data":{"version":"80158d2-dirty","commit":"80158d2","build_date":"2026-11-17T00:00:00Z"}}
```

The Go linker ignores `-X` for an unknown symbol silently, so the build does not
trust the flags. After linking each architecture, `scripts/build.sh` requires the
three strings to be embedded in the binary and, on the host architecture, runs
`version` and `--json version` and compares them with the intended values. Any
mismatch — including a binary still reporting the `dev` default — fails the
build. A build with `VERSION=dev` or `COMMIT=unknown` is refused, so the check
cannot pass on fallback values. `scripts/check-artifact.sh` repeats the stamp
assertion against `dist/BUILD-INFO` for a shipped artifact.

`dist/BUILD-INFO` carries the same values plus the Go version and `cgo=0`, and
is copied into the package docs.

`VERSION` defaults to `git describe --tags --always --dirty`, which is a commit
hash while the repository has no tags. Override `VERSION`, `COMMIT` and
`SOURCE_DATE_EPOCH` for a named build.

## Licensing and publication are not settled

Do not distribute these packages. The project license is undecided (D20) and
the release audit (A14) is open: third-party notices, a real dependency/license
review including the pinned Xray core's MPL-2.0 obligations, and a
corresponding-source bundle are required before publication. That source offer
is intentionally absent here rather than guessed, which is why `PACKAGE-NOTES`
in every package says it is a local build. Publishing, releasing, tagging or
pushing is a separate decision with the project owner.
