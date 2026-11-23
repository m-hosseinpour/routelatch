# Packaging and install notes

RouteLatch's own program ships as a single Go binary. Users do not need Go,
Python, Node or a separate Xray installation: the pinned Xray core is compiled
into that binary. A release package can additionally carry `bin/sing-box`, a
**separate, unmodified GPL-3.0 executable** supplied by the release operator (see
"The sing-box sidecar" below). This directory documents the package layout and
the user's install steps. **The build never installs anything** — no service is
enabled, no desktop proxy or network setting is touched, and nothing is
published or downloaded.

**Runtime model (D26, shipped from this revision):** packages built from this
revision contain **no systemd unit, autostart entry or maintainer script**.
Plain `routelatch run` starts the detached app/tray, returns after readiness and
survives closing the terminal; `routelatch run --headless` is the foreground
mode for terminals, SSH, containers and services. `routelatch autostart ...`
and `routelatch service install` are the explicit opt-ins; `service install`
writes the user unit at runtime. App mode needs permitted `pidfd_open` (Linux
≥5.3, not seccomp-blocked; otherwise exit `7` `pidfd_unavailable`, with
`--headless`/the opt-in service as the alternative). The published `v0.1.3`
packages predate this change and still contain the old unit and old foreground
`run`; see "Upgrading from a package with the old unit" below. Requirements:
`docs/requirements.md` D26; contract: `docs/contracts.md` §7.5.

The default answer for "how do I get a package" is `scripts/build.sh` followed
by `scripts/package.sh`. Both are local build steps. The packaging step writes
reproducible `.tar.gz` files, unsigned Debian `.deb` files and one RPM `.spec`
per architecture; the only trust anchor is the generated `SHA256SUMS`, as for
the v0.1.0 tarballs. There is no GPG signing, apt/dnf repository or
package-service integration. Debian and RPM use the **same** `Version` string,
normalised once from `BUILD-INFO` (see "Version stamping"). Binary RPMs are not
built by `scripts/package.sh`: `rpmbuild` is not assumed to be installed, so CI
builds and inspects them from the generated specs.

## Source tree layout

```
scripts/build.sh           static build for linux/amd64, arm64, riscv64, ppc64le,
                           dist/SHA256SUMS, dist/BUILD-INFO
scripts/package.sh         assembles package trees, .tar.gz files, .deb files and RPM specs under dist/packages/
scripts/check-artifact.sh  static-link and empty-environment smoke check for one binary
scripts/check-package.sh   checksum, tarball-layout, .deb metadata/payload and static RPM spec check
dist/singbox/              optional supplied sing-box binaries, official archives + SHA256SUMS (gitignored with dist/)
packaging/README.md        this file; it is shipped inside the package as INSTALL.md
deploy/README.md           app/tray, autostart, opt-in service, migration and crash recovery
```

`dist/` is gitignored build output, not a distributed directory.

## Package layout

`scripts/package.sh` produces this tree per architecture
(`amd64`, `arm64`, `riscv64`, `ppc64le`):

```
routelatch-<version>-linux-<arch>/
  bin/routelatch
  bin/rlch                              short command symlink -> routelatch
  bin/sing-box                          optional: separate GPL-3.0 sidecar (pinned v1.14.4)
  share/doc/routelatch/README.md        what RouteLatch is; quick start; limits
  share/doc/routelatch/INSTALL.md       this file
  share/doc/routelatch/SERVICE.md       app/tray, autostart, opt-in service and migration
  share/doc/routelatch/LICENSE          Apache-2.0 license text
  share/doc/routelatch/NOTICE           project attribution and MPL source pointer
  share/doc/routelatch/THIRD-PARTY-NOTICES.md
                                        dependency inventory, licenses, notices
  share/doc/routelatch/docs/licensing.md
                                        audit method, obligations, evidence limits
  share/doc/routelatch/go.sum           module hashes quoted by the audit
  share/doc/routelatch/BUILD-INFO       version, commit, date, Go version, cgo=0
  share/doc/routelatch/SINGBOX-INFO     optional: sidecar tag/commit and per-arch SHA-256
  share/doc/routelatch/PACKAGE-NOTES    "local build, not published" notice
```

There is no `share/routelatch/systemd/`, no autostart entry and no maintainer
script in the tarball.

and `dist/packages/SHA256SUMS` plus, for each architecture,
`routelatch-<version>-linux-<arch>.tar.gz`,
`routelatch_<package-version>_<debian-arch>.deb` and
`routelatch-<rpm-arch>.spec`. Tarballs keep Go/CPU architecture names. The
`.deb` file name and its `Architecture` field use Debian names, which differ in
one case: the `ppc64le` build ships as
`routelatch_<package-version>_ppc64el.deb` (`ppc64el` is Debian's little-endian
64-bit PowerPC architecture). RPM names differ in two cases: `amd64` →
`x86_64` and `arm64` → `aarch64`; `riscv64` and `ppc64le` keep their names. All
twelve artifacts per build (4 tarballs + 4 `.deb` + 4 specs) are listed in
`SHA256SUMS`.

The `.deb` payload maps the tarball root onto `/usr`:

```
/usr/bin/routelatch
/usr/bin/rlch                             symlink -> routelatch (short command)
/usr/bin/sing-box                         optional sidecar, amd64/arm64 only
/usr/share/doc/routelatch/...             same documents as the tarball
```

There is no `/usr/lib/systemd/user` or `/usr/share/routelatch/systemd` payload.
The `.deb` has no `postinst`, `preinst`, `prerm` or `postrm`: it does not enable
a service, create an autostart entry, change a desktop proxy setting or touch
user configuration. Control metadata is `Package: routelatch`, the shared
package version, the target architecture, `Depends: ca-certificates` (the
static binary uses the host CA bundle for TLS) and a description;
`scripts/check-package.sh` asserts these and rejects any unit/autostart path.
The version comes from the single normalisation in "Version stamping", not a
Debian-only rewriting of the build stamp.
The sidecar has to stay next to the main binary for the runtime's lookup, so an
`amd64`/`arm64` `.deb` installs it as `/usr/bin/sing-box`; a system that already
provides that path from another package would conflict and is not a supported
combination.

`DIST=/path/to/output` overrides the `dist/` output directory for
`build.sh`, `package.sh`, `check-artifact.sh` and `check-package.sh`, so an
isolated build does not disturb an existing `dist/` or supplied sidecar inputs.
`ARCHES="amd64"` (or another space-separated subset) limits the four-architecture
loop for a quick local check; the defaults cover all four architectures.

## RPM specs (binary RPMs built in CI)

`scripts/package.sh` writes `routelatch-<rpm-arch>.spec` for all four
architectures and records each spec in `SHA256SUMS`. The spec is generated from
`BUILD-INFO` and from the tarball just produced, and pins that tarball by
SHA-256 so a mismatched source cannot silently become an RPM. Its metadata is
`Name: routelatch`, the unified `Version`, `Release: 1`, `License: Apache-2.0`,
`Requires: ca-certificates`, and `AutoReqProv: no` (the binary is static). The
spec deliberately has **no `BuildArch`**: the architecture comes only from
`rpmbuild --target <rpm-arch>`. A foreign `BuildArch` makes
`rpmbuild --target` fail on a different host with
`error: No compatible architectures found for build`. Its `%build` is empty —
nothing is compiled — and `%install` lays out the prebuilt binary with the
standard RPM paths:

```
/usr/bin/routelatch
/usr/bin/rlch                               symlink -> routelatch (short command)
/usr/share/licenses/routelatch/LICENSE      Apache-2.0 text
/usr/share/licenses/routelatch/NOTICE       project attribution
/usr/share/doc/routelatch/...               all other documents (README, INSTALL,
                                            SERVICE, notices, licensing, go.sum,
                                            BUILD-INFO, PACKAGE-NOTES)
/usr/bin/sing-box                           optional sidecar, amd64/arm64 only
/usr/share/doc/routelatch/SINGBOX-INFO      optional sidecar provenance
```

There is no unit or autostart entry in `%files`. There are no
`%pre`/`%post`/`%preun`/`%postun` scriptlets, no `systemctl` call and no
desktop/user setting change: installing an RPM does not enable a service. The
opt-in unit is created only by `routelatch service install`. The sidecar, when
present, is a separate GPL-3.0 executable and is not linked into RouteLatch.

No `.rpm` file is produced or checksummed on a host without `rpmbuild`: local
verification is static spec validation in `scripts/check-package.sh`. CI
(`rpm-packages` in `.github/workflows/ci.yml`) installs RPM tooling, runs
`rpmbuild --target <rpm-arch> -bb` from each spec and inspects the results with
`rpm` (name, version, release, architecture, license, `Requires: ca-certificates`,
exact installed file list and absence of scriptlets). That CI step has been
green (`rpm-packages` in run `44682457124`). The v0.1.1, v0.1.2 and v0.1.3
release RPMs were built separately from these specs with an extracted, not
installed, rpm 6.0.1 toolchain. The CI-built RPMs carry a CI-only version and are not
published, and the generated specs are not a dnf/yum repository.

Debian Policy files (`copyright`, `changelog.Debian.gz`) are deliberately not
generated for these local, unsigned packages; the Apache-2.0 text and the full
third-party notices ship under `/usr/share/doc/routelatch/`. A future apt
repository or public distribution phase must add them.

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
# short command; the same relative link is inside newer tarballs as bin/rlch
ln -sf routelatch "$HOME/.local/bin/rlch"
```

For a tarball install, `~/.local/bin` must be on `PATH` for the `routelatch`
and `rlch` commands to resolve. Or install the local Debian package (unsigned;
verify `SHA256SUMS` first):

```sh
sudo apt install ./routelatch_<debian-version>_amd64.deb
```

The `.deb` installs `/usr/bin/routelatch`, its `/usr/bin/rlch` symlink and the
documents under `/usr/share/doc/routelatch/`. On `amd64` and `arm64` it also
installs the `/usr/bin/sing-box` sidecar described above. It does **not** ship a
systemd unit or start/enable anything.

After installation nothing is running. Choose how to start it:

```sh
# desktop: detached app/tray; returns after readiness and survives the terminal
routelatch run

# foreground/SSH/container/script: blocks without a tray; Ctrl-C stops it
routelatch run --headless

# desktop login start (default instance only)
routelatch autostart enable

# server/VPS supervision: writes and enables an opt-in systemd user unit
routelatch service install
```

App mode needs a StatusNotifier tray host and permitted `pidfd_open` (Linux
≥5.3, not seccomp-blocked). Otherwise it exits `7` with `tray_unavailable` or
`pidfd_unavailable`; use `run --headless` or `service install` there. Autostart
manages only the default instance and rejects `--data-dir`; for a custom data
directory use `routelatch service install --data-dir <dir>`.

Check the result with `routelatch status`, `routelatch doctor`,
`routelatch service status` or `systemctl --user status routelatch.service` when
you installed the service. To remove the package, disable the opt-in setup first
and use the package manager:

```sh
routelatch desktop proxy restore
routelatch service uninstall     # only if installed
routelatch autostart disable     # only if enabled
sudo apt remove routelatch       # or: sudo rpm -e routelatch
```

Tarball uninstall, crash recovery and the GNOME/KDE desktop-proxy restore step
are documented in `SERVICE.md` (the source tree's `deploy/README.md`); the
desktop proxy is opt-in and is not changed by installing or starting anything.

### Upgrading from a package with the old unit

The published `v0.1.1`–`v0.1.3` packages (and the `v0.1.0` tarball) contain a
systemd user unit and an old foreground `run`. Stop and disable it before
upgrading, remove leftover user copies/symlinks, reload the manager, then
upgrade and choose autostart or the service:

```sh
systemctl --user disable --now routelatch.service
sudo rm -f /usr/lib/systemd/user/routelatch.service
rm -f "$HOME/.config/systemd/user/routelatch.service"
rm -f "$HOME/.config/systemd/user/default.target.wants/routelatch.service"
systemctl --user daemon-reload
# upgrade the package or replace the tarball binary, then:
routelatch autostart enable    # or: routelatch service install
```

If the manager config base differs from your shell's `$XDG_CONFIG_HOME`, check
`systemctl --user show-environment` and remove the unit/symlink there too. An
upgraded package no longer contains the `/usr/lib` unit, but a leftover user copy
would still name the old unit. Starting the new engine against the same data
directory while an old engine (even in `stopped` mode) holds its legacy lock
exits `4` before binding; a new `--data-dir` instance is independent of an old
default instance and can collide on ports, so stop the old one explicitly.
A custom `XDG_RUNTIME_DIR` on an old default engine may be unguessable and hence
invisible to the probe; always stop the old unit explicitly. The legacy-lock
window lasts one release. `rlch` is unchanged.

## Architecture and platform

`linux/amd64`, `linux/arm64`, `linux/riscv64` and `linux/ppc64le` are built;
`uname -m` must report `x86_64`, `aarch64`, `riscv64` or `ppc64le`. There are
no 32-bit or non-Linux packages. The build targets every architecture on one
machine, so cross-built packages can be produced without matching hardware —
but they still have to be run on that hardware to be tested. RPM names these
four targets `x86_64`, `aarch64`, `riscv64` and `ppc64le` respectively; the
`.deb` names differ only for PowerPC (`ppc64el`).

Only `amd64` is executed in this build. `arm64` packages are cross-built and
remain runtime-untested; `riscv64` and `ppc64le` are **cross-build-only and
explicitly runtime-untested**. No clean-machine or real-hardware test is
claimed for any of them, and the supported runtime matrix for v1 remains
`amd64`/`arm64`. The optional sing-box sidecar is shipped only for
`amd64`/`arm64` because upstream release binaries are pinned for those two; on
`riscv64`/`ppc64le` RouteLatch still supports Xray exits, but hysteria2 exits
stay `unsupported`/`sidecar_unavailable`.

## The sing-box sidecar (GPL-3.0, separate executable)

`bin/sing-box` is not built by `scripts/build.sh` and is not linked into
RouteLatch. It is an optional input: an **already extracted, unmodified upstream
binary** for each sidecar architecture (`amd64`, `arm64`), supplied by the
release operator under `dist/singbox/` (gitignored together with `dist/`).
`scripts/package.sh` pins
upstream tag `v1.14.4` (commit `c005f35300ce76427dc285481ec5695f5b179cd2`) and the
sidecar's identity is recorded in the package's `SINGBOX-INFO`.

Expected inputs, all under `dist/singbox/`:

```
sing-box-linux-amd64      extracted upstream v1.14.4 binary for amd64
sing-box-linux-arm64      extracted upstream v1.14.4 binary for arm64
SHA256SUMS                lines: <sha256>  sing-box-linux-<arch>
# Optional for a routine build, required for REQUIRE_SINGBOX=1: verify against
# the digests pinned in scripts/package.sh before extracting the binaries above.
sing-box-1.14.4-linux-amd64.tar.gz   official v1.14.4 archive for amd64
sing-box-1.14.4-linux-arm64.tar.gz   official v1.14.4 archive for arm64
```

How to obtain and pin them — done by the operator at release time; **this script
and this change download nothing**:

1. Fetch the **official** `v1.14.4` release archives
   `sing-box-1.14.4-linux-amd64.tar.gz` and `sing-box-1.14.4-linux-arm64.tar.gz`
   from the [upstream release](https://github.com/SagerNet/sing-box/releases/tag/v1.14.4).
   That release has no `SHA256SUMS` asset; the authoritative per-asset digests are
   those GitHub reports, pinned in `scripts/package.sh`:

   | Archive | Authoritative SHA-256 (GitHub asset digest, tag v1.14.4) |
   | --- | --- |
   | `sing-box-1.14.4-linux-amd64.tar.gz` | `699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968` |
   | `sing-box-1.14.4-linux-arm64.tar.gz` | `436441f76ee2f7fe21b5b2d426d3cb261a480455a704865789ad533e4273ec26` |

   Verify each downloaded archive against its pinned digest before extracting.
   `scripts/package.sh` repeats this check when the archive is present and fails
   closed on a mismatch. Do not substitute a mirror or a self-built binary.
2. Extract the `sing-box` executable from each verified archive and place the two
   files at the paths above. Upstream publishes no per-binary digest, so the
   extracted binary can only be anchored to the official release by extracting it
   from an archive that passed step 1; the script cannot prove that provenance on
   its own.
3. In `dist/singbox/`, run `sha256sum sing-box-linux-amd64 sing-box-linux-arm64`
   and save the two lines as `SHA256SUMS`. These are the operator-generated pins
   for the **extracted binaries**; `scripts/package.sh` re-verifies each supplied
   binary with `sha256sum -c` before it is copied into a package and fails closed
   on a mismatch. They are meaningful only when the binaries came from the
   archives verified in step 1.
4. Run `scripts/package.sh`. With the inputs present, every tarball carries
   `bin/sing-box` and `SINGBOX-INFO` (tag, commit, per-architecture SHA-256), the
   `PACKAGE-NOTES` note, and the GPL-3.0 text and written source offer in
   `THIRD-PARTY-NOTICES.md`. For a routine local build, a missing sidecar
   directory, a directory without `SHA256SUMS`, or missing official archives only
   print a notice and packaging continues without the sidecar. For a release
   build set `REQUIRE_SINGBOX=1`: it then requires the sidecar directory,
   `SHA256SUMS`, **and** both official archives with matching pinned digests, and
   fails closed if any is missing or wrong.

`BUILD-INFO` remains the RouteLatch build stamp and is deliberately **not**
changed by the sidecar; the sidecar's identity is recorded separately in
`SINGBOX-INFO`, so the existing stamp check and the copied `BUILD-INFO` stay
consistent. The outer `dist/packages/SHA256SUMS` and its `sha256sum -c`
verification are unchanged and now cover a tarball that contains the sidecar.
Installing a tarball does not install or enable sing-box; it is just a file under
`bin/`. The compliance assessment and source-offer instructions are in
[`docs/licensing.md`](../docs/licensing.md).

## Verification performed

`scripts/build.sh` sets `CGO_ENABLED=0` and uses `-trimpath -buildvcs=false`.
`scripts/check-artifact.sh` confirms, for the amd64 artifact:

- `file` reports `ELF 64-bit LSB executable, x86-64, statically linked`;
- `ldd` reports `not a dynamic executable` and there is no `PT_INTERP` header;
- `env -i … version` and `env -i … --json version` report the version, commit and
  build date recorded in `dist/BUILD-INFO` (the script fails on any mismatch);
- `run --headless` starts with `env -i` and temporary XDG directories, listens
  only on `127.0.0.1`, answers `status` with exit 0, and exits 0 on `SIGTERM`
  (the foreground mode; no tray or pidfd is needed).

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

The `.deb` step pins every payload mtime to `SOURCE_DATE_EPOCH`, passes the same
epoch to `dpkg-deb`, builds with `--root-owner-group -Zgzip -z9` and fixes the
tar ownership, so two packaging runs over one build produce byte-identical
`.deb` files. The generated specs are plain text derived from the same inputs,
so they are reproducible too. `scripts/check-package.sh` then verifies that
`SHA256SUMS` lists exactly the expected tarballs, `.deb` files and specs (so a
dropped line is not a false pass) and runs `sha256sum -c`. For each architecture
it also checks the tarball layout; reads `dpkg-deb -f` metadata (name, unified
version, architecture, `Depends`, description, maintainer, installed size);
validates the architecture name against the host's Debian architecture list;
requires the control archive to contain only `control`/`md5sums` (no maintainer
scripts); compares the exact payload file set, the `0755 root:root` binary mode
and the `0644` document mode against the documented layout; requires `bin/rlch`
in the tarball and `/usr/bin/rlch` in the `.deb` to be relative symlinks to
`routelatch`; compares `/usr/bin/routelatch` and an optional `/usr/bin/sing-box` byte-for-byte
with the dist binaries; and rejects any `.service`, `.desktop`, `/systemd/` or
`/autostart/` path (the package ships no unit/autostart entry). For the RPM spec
it checks the version/architecture/name/release/license/`Requires`/`Source0`
fields, the BUILD-INFO stamp comments, the pinned source digest against the
actual tarball, the `%files` install layout (licenses under `licenses/`, other
docs under `doc/`, no unit), the absence of scriptlets or service enablement,
and sidecar presence only on `amd64`/`arm64`. Negative checks (wrong version,
wrong arch, injected `%post`, bad source digest, license in the wrong directory,
unexpected sidecar, missing spec, injected unit/autostart path) all fail the
gate. It installs nothing and does not run rpmbuild.

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

One normalisation produces the package version for **both** formats: a leading
`v` is stripped and every hyphen becomes a dot (`v0.1.0-r12.1` →
`0.1.0.r12.1`), a string valid in Debian and RPM. A value that would not start
with a digit gets a `0.0.0+` prefix (`a028469` → `0.0.0+a028469`), because both
formats require a version that starts with a digit. Characters outside the
shared set (`A-Z`, `a-z`, `0-9`, `.`, `+`, `~`) make packaging fail rather than
silently rewriting the stamp. The `.deb` `Version:` field and the RPM spec
`Version:` field are set from this single value; CI also asserts that they are
byte-identical.

`VERSION` defaults to `git describe --tags --always --dirty`, so it is the tag
when one names the current commit (for example `v0.1.0` at the tag), a
`tag-N-gHASH` value after later commits, or a bare commit hash when no tag is
reachable. Override `VERSION`, `COMMIT` and `SOURCE_DATE_EPOCH` for a named
build.

## Licensing and publication

The project is licensed under **Apache-2.0** (D20, decided 2026-11-18). Every
package carries `LICENSE`, `NOTICE` and `THIRD-PARTY-NOTICES.md` under
`share/doc/routelatch/`; the latter lists the exact linked module versions, their
licenses and the MPL-2.0 source-availability route. The audit and its limits are
in `docs/licensing.md`. When the sidecar is included, the same notices also carry
the GPL-3.0 text, the verbatim upstream `LICENSE` with its added naming clause,
and the written source offer; it does not change RouteLatch's Apache-2.0 license.

The v0.1.0 release published two tarballs; `v0.1.1`, `v0.1.2` and `v0.1.3` each
published all 17 assets (four tarballs, four `.deb` files, four RPM specs, four
binary RPMs and `SHA256SUMS`). Building a package still does not publish it: do not publish,
release, tag, push or upload new artifacts without a separate decision from the
project owner. `PACKAGE-NOTES` in every tarball/deb says it is a local build for
that reason; the generated spec carries the same "local, unsigned" header.
