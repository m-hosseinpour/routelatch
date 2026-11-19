# Packaging and install notes

RouteLatch's own program ships as a single Go binary. Users do not need Go,
Python, Node or a separate Xray installation: the pinned Xray core is compiled
into that binary. A release package can additionally carry `bin/sing-box`, a
**separate, unmodified GPL-3.0 executable** supplied by the release operator (see
"The sing-box sidecar" below). This directory documents the package layout and
the user's install steps. **The build never installs anything** — no service is
enabled, no desktop proxy or network setting is touched, and nothing is
published or downloaded.

The default answer for "how do I get a package" is `scripts/build.sh` followed
by `scripts/package.sh`. Both are local build steps. The packaging step writes
reproducible `.tar.gz` files and unsigned Debian `.deb` files; the only trust
anchor is the generated `SHA256SUMS`, as for the v0.1.0 tarballs. There is no
GPG signing, apt repository or package-service integration.

## Source tree layout

```
scripts/build.sh           static build for linux/amd64, arm64, riscv64, ppc64le,
                           dist/SHA256SUMS, dist/BUILD-INFO
scripts/package.sh         assembles package trees, .tar.gz files and .deb files under dist/packages/
scripts/check-artifact.sh  static-link and empty-environment smoke check for one binary
scripts/check-package.sh   checksum, tarball-layout and .deb metadata/payload check
dist/singbox/              optional supplied sing-box binaries, official archives + SHA256SUMS (gitignored with dist/)
packaging/README.md        this file; it is shipped inside the package as INSTALL.md
deploy/systemd/routelatch.service
                           the one copy of the systemd user unit (the package copies it)
deploy/README.md           service lifecycle: install, check, uninstall, crash recovery
```

`dist/` is gitignored build output, not a distributed directory.

## Package layout

`scripts/package.sh` produces this tree per architecture
(`amd64`, `arm64`, `riscv64`, `ppc64le`):

```
routelatch-<version>-linux-<arch>/
  bin/routelatch
  bin/sing-box                          optional: separate GPL-3.0 sidecar (pinned v1.14.4)
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
  share/doc/routelatch/SINGBOX-INFO     optional: sidecar tag/commit and per-arch SHA-256
  share/doc/routelatch/PACKAGE-NOTES    "local build, not published" notice
  share/routelatch/systemd/routelatch.service
```

and `dist/packages/SHA256SUMS` plus `routelatch-<version>-linux-<arch>.tar.gz`
and `routelatch_<debian-version>_<debian-arch>.deb` for each architecture.
Tarballs keep Go/CPU architecture names. The `.deb` file name and its
`Architecture` field use Debian names, which differ in one case: the `ppc64le`
build ships as `routelatch_<debian-version>_ppc64el.deb` (`ppc64el` is Debian's
little-endian 64-bit PowerPC architecture).

The `.deb` payload maps the tarball root onto `/usr`:

```
/usr/bin/routelatch
/usr/bin/sing-box                         optional sidecar, amd64/arm64 only
/usr/share/doc/routelatch/...             same documents as the tarball
/usr/share/routelatch/systemd/routelatch.service
                                          the unchanged source-tree unit
/usr/lib/systemd/user/routelatch.service  standard user unit for a .deb install
```

The packaged unit in `/usr/lib/systemd/user` is generated from the one copy in
`deploy/systemd/routelatch.service` with only `ExecStart` changed to
`/usr/bin/routelatch run`, because `%h/.local/bin` is not a package path. The
`.deb` has no `postinst`, `preinst`, `prerm` or `postrm`: it does not enable a
service, change a desktop proxy setting or touch user configuration. Control
metadata is `Package: routelatch`, a Debian-normalised version, the target
architecture, `Depends: ca-certificates` (the static binary uses the host CA
bundle for TLS) and a description; `scripts/check-package.sh` asserts these.
The sidecar has to stay next to the main binary for the runtime's lookup, so an
`amd64`/`arm64` `.deb` installs it as `/usr/bin/sing-box`; a system that already
provides that path from another package would conflict and is not a supported
combination.

`DIST=/path/to/output` overrides the `dist/` output directory for
`build.sh`, `package.sh`, `check-artifact.sh` and `check-package.sh`, so an
isolated build does not disturb an existing `dist/` or supplied sidecar inputs.
`ARCHES="amd64"` (or another space-separated subset) limits the four-architecture
loop for a quick local check; the defaults cover all four architectures.

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

# optional systemd user service; same unit as deploy/systemd/routelatch.service
install -Dm644 share/routelatch/systemd/routelatch.service \
    "$HOME/.config/systemd/user/routelatch.service"
systemctl --user daemon-reload
systemctl --user enable --now routelatch.service
```

For a tarball install, `~/.local/bin` must be on `PATH` for the `routelatch`
command to resolve. Or install the local Debian package (unsigned; verify
`SHA256SUMS` first):

```sh
sudo apt install ./routelatch_<debian-version>_amd64.deb
```

The `.deb` installs `/usr/bin/routelatch`, the documents under
`/usr/share/doc/routelatch/`, and a standard systemd user unit. On `amd64` and
`arm64` it also installs the `/usr/bin/sing-box` sidecar described above. It
does not start or enable anything; opt in with:

```sh
systemctl --user daemon-reload
systemctl --user enable --now routelatch.service
```

For a `.deb` install, `/usr/bin` is already on `PATH`, so `routelatch` resolves
without further setup. To remove the package, disable the service first and use
the package manager, which also removes its documents and unit:

```sh
systemctl --user disable --now routelatch.service
sudo apt remove routelatch
```

Without the service, run the foreground command in your own terminal instead:

```sh
routelatch run
```

Check the result with `routelatch status`, `routelatch doctor`, or
`systemctl --user status routelatch.service`. Tarball uninstall, crash recovery
and the GNOME/KDE desktop-proxy restore step are documented in `SERVICE.md`
(the source tree's `deploy/README.md`); the desktop proxy is opt-in and is not
changed by installing or starting the service.

## Architecture and platform

`linux/amd64`, `linux/arm64`, `linux/riscv64` and `linux/ppc64le` are built;
`uname -m` must report `x86_64`, `aarch64`, `riscv64` or `ppc64le`. There are
no 32-bit or non-Linux packages. The build targets every architecture on one
machine, so cross-built packages can be produced without matching hardware —
but they still have to be run on that hardware to be tested.

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

The `.deb` step pins every payload mtime to `SOURCE_DATE_EPOCH`, passes the same
epoch to `dpkg-deb`, builds with `--root-owner-group -Zgzip -z9` and fixes the
tar ownership, so two packaging runs over one build produce byte-identical
`.deb` files. `scripts/check-package.sh` then verifies that `SHA256SUMS` lists
exactly the expected tarballs and `.deb` files (so a dropped line is not a false
pass) and runs `sha256sum -c`. For each architecture it also checks the tarball
layout; reads `dpkg-deb -f` metadata (name, Debian version, architecture,
`Depends`, description, maintainer, installed size); validates the architecture
name against the host's Debian architecture list; requires the control archive
to contain only `control`/`md5sums` (no maintainer scripts); compares the exact
payload file set, the `0755 root:root` binary mode and the `0644` document mode
against the documented layout; compares `/usr/bin/routelatch` and an optional
`/usr/bin/sing-box` byte-for-byte with the dist binaries; and verifies the
generated user unit. It installs nothing.

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

The `.deb` `Version` field is derived deterministically from that value: a
leading `v` is stripped (`v0.1.0` → `0.1.0`) and a value that would not start
with a digit gets a `0.0.0+` prefix (`a028469` → `0.0.0+a028469`), because
Debian requires a version that starts with a digit. Any other character outside
the Debian version set makes packaging fail rather than silently rewriting the
stamp.

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

The v0.1.0 tarballs are published; the `.deb` files from this step are not.
Do not publish, release, tag, push or upload these packages: that is a separate
decision with the project owner. `PACKAGE-NOTES` in every package says it is a
local build for that reason.
