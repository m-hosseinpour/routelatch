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
  share/doc/routelatch/SERVICE.md       systemd unit lifecycle, uninstall, crash recovery
  share/doc/routelatch/LICENSE          Apache-2.0 license text
  share/doc/routelatch/NOTICE           project attribution and MPL source pointer
  share/doc/routelatch/THIRD-PARTY-NOTICES.md
                                        dependency inventory, licenses, notices
  share/doc/routelatch/docs/licensing.md
                                        audit method, obligations, evidence limits
  share/doc/routelatch/go.sum           module hashes quoted by the audit
  share/doc/routelatch/BUILD-INFO       version, commit, date, Go version, cgo=0
  share/doc/routelatch/PACKAGE-NOTES    "local build, not published" notice
  share/routelatch/systemd/routelatch.service
```

and `dist/packages/SHA256SUMS` plus `routelatch-<version>-linux-<arch>.tar.gz`
for each architecture.

There are no runtime data files in the package. On first run the directories
`$XDG_CONFIG_HOME/routelatch/` and `$XDG_STATE_HOME/routelatch/` are created
(contract §4.1 in the source tree) with `0700` permissions; `config.json` and
`state.json` are written with `0600` on the first change and defaults are used
until then (`doctor` reports `config_file | not created yet`).

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
GNOME/KDE desktop-proxy restore step are documented in `SERVICE.md` (the
source tree's `deploy/README.md`); the desktop proxy is opt-in and is not
changed by installing or starting the service.

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

`scripts/package.sh` packages with a sorted, fixed-mtime tar and `gzip -n` and
pins `umask 022`, so the same build inputs also give the same `.tar.gz` and
`SHA256SUMS` regardless of the builder's shell umask; two runs over one build,
and runs under umask 002 and 022, produced identical tarball checksums. Before
writing the checksums it runs `routelatch version` from the host-architecture
package tree and fails unless the reported version, commit and build date all
match `BUILD-INFO`, so a package cannot silently carry a binary built from
different inputs. An unknown host architecture skips that check with a warning
(the cross-built architecture is already covered by build.sh's embedded-string
check).

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

## Licensing and publication

The project is licensed under **Apache-2.0** (D20, decided 2026-11-18). Every
package carries `LICENSE`, `NOTICE` and `THIRD-PARTY-NOTICES.md` under
`share/doc/routelatch/`; the latter lists the exact linked module versions, their
licenses and the MPL-2.0 source-availability route. The audit and its limits are
in `docs/licensing.md`.

Publication is still not settled. Do not publish, release, tag, push or upload
these packages: that is a separate decision with the project owner, and the
release checklist still requires the MPL-2.0 source offer and the rest of A14.
`PACKAGE-NOTES` in every package says it is a local build for that reason.
