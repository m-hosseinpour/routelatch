# Release readiness: history, package, name and audit

Status: **no credential, private pool content or binary is in the repository's
history.** The current tree is clean of absolute personal paths.

The first public release, **v0.1.0**, is tagged on `main` and publishes two Linux
tarballs as assets on the GitHub Release. The history scan, name check and
dependency audit recorded below remain historical evidence gathered before the
release; they are not repeated here. The tag, pushes and release for v0.1.0 were
explicitly authorized by the project owner.

The sections below were originally a dated readiness snapshot (branch
`r8-release`, binary/version `110cce3`, based on `47ec19a`, with `main` then at
`c1e60ea`). Redaction rule used throughout: findings name the commit, path and
kind, never the matched value.

## v0.1.0 release (current)

The first release is tagged `v0.1.0` on `main`. Its two tarballs are attached to
the GitHub Release for that tag:

- `dist/packages/routelatch-v0.1.0-linux-amd64.tar.gz`
- `dist/packages/routelatch-v0.1.0-linux-arm64.tar.gz`

Both carry `bin/routelatch`, the systemd user unit, the license/notice files and
the separate `bin/sing-box` sidecar with `share/doc/routelatch/SINGBOX-INFO`
(pinned sing-box v1.14.4, GPL-3.0-or-later, used for hysteria2/sing-box exits).
RouteLatch itself remains Apache-2.0. The authoritative SHA-256 digests are in
`dist/packages/SHA256SUMS`, which is also attached to the Release; verify a
download with `sha256sum -c SHA256SUMS` from the directory holding the tarballs.

Scope: this release ships **only** the two Linux `.tar.gz` files. Distribution
packages (`.deb`, `.rpm`), Windows/macOS builds, 32-bit builds and additional CPU
architectures are **not** part of v0.1.0 — they are future work. A01 clean-machine
install, arm64 runtime execution and a real GNOME session are not claimed here.
A later, **unpublished** local `.deb` build is recorded in "R12 Phase 1" below;
it does not change the tag, its assets or this release's scope.

## R12 Phase 1: local Debian packages (unpublished)

Phase 1 adds a reproducible, locally verified Debian package to the same
packaging pipeline; it does **not** publish anything, sign anything or add an
apt repository. The agreed trust model is the v0.1.0 one: unsigned artifacts
with `SHA256SUMS` only. The v0.1.0 tag, GitHub Release and assets were not
touched.

`scripts/build.sh` now cross-builds `linux/amd64`, `arm64`, `riscv64` and
`ppc64le`. `scripts/package.sh` writes, for each architecture, the existing
tarball plus a Debian package, and records both in `dist/packages/SHA256SUMS`.
Debian package names use Debian architecture names, so the PowerPC build is
`routelatch_<debian-version>_ppc64el.deb` (Debian calls little-endian 64-bit
PowerPC `ppc64el`); tarballs keep the Go/CPU name `ppc64le`. The `.deb` payload
maps the tarball root to `/usr`: `/usr/bin/routelatch`,
`/usr/share/doc/routelatch/…`, the unchanged unit under
`/usr/share/routelatch/systemd/`, and a generated standard user unit at
`/usr/lib/systemd/user/routelatch.service` whose only change is
`ExecStart=/usr/bin/routelatch run`. Control metadata is `Package: routelatch`,
a Debian-normalised version (leading `v` stripped; a non-digit value gets a
`0.0.0+` prefix), the target Debian architecture, `Depends: ca-certificates`
and a description. This was the Phase 1 version rule; Phase 2 replaced it with the unified version below. There are no maintainer scripts, so installation does not
enable a service or change desktop/user settings. Debian Policy `copyright`
and `changelog.Debian.gz` files are deliberately deferred with the unpublished
local scope.

Evidence from the local build (`VERSION=v0.1.0-r12.1`, `COMMIT=a028469`,
`SOURCE_DATE_EPOCH=1700000000`, Go 1.27.4, `CGO_ENABLED=0`):

- all four binaries built and embedded version/commit/date; only the amd64
  binary was executed;
- `scripts/check-artifact.sh` passed for amd64;
- four tarballs and four `.deb` files were produced (`amd64`, `arm64`,
  `riscv64`, `ppc64el`); `scripts/check-package.sh` passed for every
  architecture: the exact `SHA256SUMS` member set, `sha256sum -c`, tarball
  layout, `dpkg-deb -f` metadata, a control archive containing only
  `control`/`md5sums`, the exact payload file set, `0755 root:root` binary and
  `0644` document modes, `/usr/bin/routelatch` bytes against the dist binary,
  unit checking, and Debian architecture validation against
  `dpkg-architecture`;
- negative checks confirmed the gate fails on a missing `SHA256SUMS` line, an
  injected maintainer script, an extra payload file and a wrong binary mode;
- running packaging twice over the same build produced byte-identical
  `SHA256SUMS` (tarball and `.deb`), so the `.deb` is reproducible for the same
  inputs;
- the amd64 `.deb` extracted with `dpkg-deb -x` and its `version` output matched
  `BUILD-INFO`;
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` all passed
  (17/17 packages).

Limits: `arm64` remains cross-built and runtime-untested; `riscv64` and
`ppc64le` are **cross-build-only and explicitly runtime-untested**. The
sing-box sidecar is pinned and shipped only for `amd64`/`arm64`; on
`riscv64`/`ppc64le` hysteria2 exits remain unavailable
(`sidecar_unavailable`) while Xray exits are unaffected. No clean-machine
install, real-hardware run, `.rpm`, GPG signature, package repository or
Windows `exe` is part of this phase. RPM is the documented next phase (see below).

## R12 Phase 2: RPM specs and CI-built binary RPMs (unpublished)

Phase 2 keeps Phase 1's trust model unchanged: unsigned artifacts with
`SHA256SUMS` as the only anchor, no GPG signing, no apt/dnf repository, no
AppImage/Flatpak and no Windows executable. It adds one RPM spec per
architecture to the same packaging pipeline and **one unified version** for
both formats. `scripts/package.sh` normalises the `BUILD-INFO` version once in
`scripts/package.sh` (leading `v` stripped, every hyphen becomes a dot:
`v0.1.0-r12.1` -> `0.1.0.r12.1`) and uses that exact string as the `.deb`
`Version` and the spec `Version:`; `scripts/check-package.sh` applies the same
normalisation independently and CI asserts byte-for-byte equality. The local
`.deb` artifacts were regenerated from scratch in an isolated `DIST`; the
`v0.1.0` tag, its GitHub Release assets, `dist/singbox`, Git history and every
unrelated file were left untouched. No versioned package artifact is committed
to Git.

`scripts/package.sh` writes `routelatch-<rpm-arch>.spec` for `x86_64`
(amd64), `aarch64` (arm64), `riscv64` and `ppc64le`. Each spec pins the
matching tarball by SHA-256, sets `Version` to the unified value, `Release: 1`,
`BuildArch` to the RPM arch, `License: Apache-2.0`, `Requires: ca-certificates`
and `AutoReqProv: no`, and has an empty `%build`. Its `%install` puts
`/usr/bin/routelatch`, the standard bundle of documents under
`/usr/share/doc/routelatch/`, `LICENSE`/`NOTICE` under
`/usr/share/licenses/routelatch/` and the unchanged systemd user unit under
`/usr/lib/systemd/user/`; the optional sing-box sidecar (amd64/arm64 only)
goes to `/usr/bin/sing-box`. There are no scriptlets, no service enable and no
desktop/user setting change.

Evidence from the local build (`VERSION=v0.1.0-r12.2`, `COMMIT=6a7580f`,
`SOURCE_DATE_EPOCH=1700000000`, Go 1.27.4, `CGO_ENABLED=0`, isolated `DIST`):

- all four binaries built with matching version/commit/date; only amd64 was
  executed (`scripts/check-artifact.sh` passed);
- four tarballs, four `.deb` files and four specs were produced;
  `scripts/check-package.sh` passed for every architecture, including the
  exact 12-member `SHA256SUMS` set, the spec metadata/source-digest/`%files`
  layout checks and all unchanged Phase 1 `.deb` checks;
- the sidecar path was exercised with the pinned v1.14.4 inputs present and
  absent; the spec includes sidecar entries only on amd64/arm64;
- negative checks confirmed the RPM gate fails on a wrong spec version, wrong
  `BuildArch`, injected `%post`, a wrong `Source0` digest, `LICENSE` in the doc
  directory, sidecar content on a non-sidecar arch and a missing spec line;
- two packaging runs over one build produced byte-identical `SHA256SUMS`
  (tarballs, `.deb` and specs);
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` passed
  (17/17 packages).

Limits: `rpmbuild`, `rpm` and `rpmspec` are **not installed on this host and
were not installed**; no real binary RPM was built or inspected here. The
`rpm-packages` CI job was added to install RPM tooling, build each RPM with
`rpmbuild --target <arch> -bb` from the generated spec and inspect it with
`rpm` (name, version, release, arch, license, requires, exact file list and no
scriptlets), but that job has not been executed in this environment. The
`arm64`/`riscv64`/`ppc64le` binaries remain cross-build-only and
runtime-untested, the sidecar is pinned only on amd64/arm64, and this phase does
not publish anything or claim a clean-machine install.
## 1. History scan

Method: every reachable object in every ref (`git rev-list --all --objects`),
recorded at `110cce3`: 43 commits over `main`, `r6-transports` and `r8-release`,
438 text blobs read in full, plus `git grep` over all revisions for scheme links,
tokens, keys, webhooks, home paths and private addresses. The scan separates
"present in the current tree" from "only in history".

| Class | Result |
| --- | --- |
| VLESS/Trojan/SS/VMess/Hysteria/TUIC/WireGuard links | 0 real links. Every match is a synthetic fixture in `internal/*_test.go`, `cmd/routelatch/cli_test.go` and `docs/contracts.md`; hosts are `example.com`, `example.net`, `127.0.0.1`, IDs are `1111…` and the test constant `b831381d-…`. No user pool node, domain or UUID appears. |
| Private pool / subscription content | 0. No subscription blob, pool file, copied state, `config.json` or `state.json` was ever committed. No long base64 blob decodes to a proxy payload. |
| Tokens and credentials | 0. No `ghp_`/`github_pat_`/`AKIA`/`sk-`/`xox*`, no bot token, no Discord/Slack/Telegram webhook, no `-----BEGIN … PRIVATE KEY-----`, no certificate. |
| Committed binaries or state | 0 binary blobs of any size; no state, log, database, backup or archive files in history. |
| Non-loopback IPs / private hosts | only documentation ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`), `8.8.8.8`, routing-test samples and `100.64.0.0/10`; no `10/8` or `192.168/16` service address; the only `.local` name is the `spike.local` self-signed test certificate CN, not a service host. |
| Personal machine paths | **finding, fixed in the current tree**: four generated files embedded an absolute home-directory path (home + user + `workspace/routelatch/...`) in an fd map. No credential, but a local path must not ship. |

**Personal-path finding.** Introduced by `1494765` (T1 spike) in
`spike/xray/results/teardown-fast.txt`, `teardown-fast-both.txt`,
`teardown-fast-CLIENT.txt` and `teardown-fast-EXIT.txt`; two lines per file. Fixed
at `110cce3` by replacing the absolute prefix with a repository-relative path
(`spike/xray/results/...`); measured values are unchanged. The same eight strings
**remain in history only** (blobs of `1494765`) and can be removed only by a
history rewrite in a copy before the first push — see the decision below.

Scanning the current tree for absolute home-directory prefixes finds no real
path: the only match is the synthetic `HOME=` fixture in
`internal/platform/launcher_test.go`. No macOS or Windows user-profile path and
no root-home path is tracked.

**Identity exposure (not a secret).** Every commit has author `OpenCode` with
its noreply address and committer `Mahdi Hosseinpour` with the user's personal
address (from the repository's Git configuration); the trailer repeats that
address. This is deliberate attribution, but a public history publishes it.
Decision, not a defect.

## 2. Release package

The v0.1.0 tarballs are built from the tagged `main` commit with the final
packaging script, Go 1.27.4, `CGO_ENABLED=0`,
`-trimpath -buildvcs=false -ldflags "-s -w -X …"`:

- `dist/packages/routelatch-v0.1.0-linux-amd64.tar.gz`
- `dist/packages/routelatch-v0.1.0-linux-arm64.tar.gz`

Their authoritative SHA-256 digests are recorded in `dist/packages/SHA256SUMS`
and verified with `sha256sum -c`. The earlier pre-sidecar R8 snapshot
(`routelatch-110cce3-linux-amd64.tar.gz` / `-linux-arm64.tar.gz`, digests
`2aceb05f…` / `77b2aa8e…`) is historical and is not the shipped release.

Each package carries `bin/routelatch` and, in v0.1.0, `bin/sing-box` plus
`share/doc/routelatch/SINGBOX-INFO`,
`share/routelatch/systemd/routelatch.service`,
and under `share/doc/routelatch/`: `README.md`, `INSTALL.md`, `SERVICE.md`,
`LICENSE`, `NOTICE`, `THIRD-PARTY-NOTICES.md`, `docs/licensing.md`, `go.sum`,
`BUILD-INFO`, `PACKAGE-NOTES`. `dist/packages/SHA256SUMS` verifies with
`sha256sum -c`. The v0.1.0 tarballs include the sidecar, so their digests differ
from the historical pre-sidecar R8 values; the verification flow is unchanged.

Evidence, not assumption:

- version is observable in the artifact: the packaged binary prints
  `RouteLatch 110cce3` / `commit: 110cce3` /
  `build date: 2026-11-18T00:00:00Z`, matching `BUILD-INFO` and `--json version`;
- `scripts/build.sh --reproducible` rebuilt both architectures twice and reported
  identical checksums;
- packaging is umask-independent: `package.sh` pins `umask 022`, all tar
  directory entries are `drwxr-xr-x`, and runs under umask 002 and 022 produced
  identical tarball checksums;
- `scripts/check-artifact.sh dist/routelatch-linux-amd64` passed: static ELF, no
  `PT_INTERP`, stamp matches, empty-environment `run` answered `status` on loopback
  and exited 0 on SIGTERM;
- the new stamp guard fails closed: forcing `commit=deadbeef` in `BUILD-INFO`
  made `scripts/package.sh` stop with `package stamp check FAILED: binary reports
  version '110cce3', commit '110cce3', date '2026-11-18T00:00:00Z'; BUILD-INFO
  says '110cce3', 'deadbeef', …`.

**Changes made for this.** `scripts/package.sh` now ships `SERVICE.md` (the packaged
copy of `deploy/README.md` with a header mapping source-tree paths and references
to the package), because `INSTALL.md` pointed readers at a file that was not in
the package; it pins `umask 022` so directory modes and tarball bytes do not depend
on the builder; and it verifies the host-architecture binary's version, commit and
build date against `BUILD-INFO` before writing checksums. `packaging/README.md`
and `docs/licensing.md` were updated to match.

**MPL-2.0 source offer.** The package ships the written offer in `NOTICE`: exact
versions of `xtls/xray-core`, `xtls/reality` and `layeh.com/gopher-luar`, the
`go mod download <module>@<version>` commands and the upstream repositories, with
the `go.sum` hashes in `docs/licensing.md`. No source tarball is bundled; that
remains an optional alternative, recorded in `docs/licensing.md`.

### R11 sidecar binary: sing-box v1.14.4 (GPL-3.0, separate executable)

R11 adds a **second, separately shipped binary** to the release package:
`bin/sing-box`, the unmodified upstream `sing-box` v1.14.4 executable (tag
`v1.14.4`, commit `c005f35300ce76427dc285481ec5695f5b179cd2`), licensed
GPL-3.0-or-later with an added naming clause. `scripts/build.sh` does not build
it and it is **not linked into** `bin/routelatch`; RouteLatch stays Apache-2.0
and the 68-module audit is unchanged. The aggregate rationale, naming-clause
handling, written source offer and legal-risk disclaimer are in
`docs/licensing.md` and `THIRD-PARTY-NOTICES.md`.

With the sidecar included, each tarball additionally carries:

- `bin/sing-box` — the pinned, unmodified sidecar executable for that architecture;
- `share/doc/routelatch/SINGBOX-INFO` — pinned tag/commit, per-architecture
  SHA-256 and a pointer to the source offer;
- the GPL-3.0 notice section and written source offer in
  `THIRD-PARTY-NOTICES.md` (already shipped in every package).

`scripts/package.sh` pins tag `v1.14.4`, re-verifies each supplied binary against
`dist/singbox/SHA256SUMS` with `sha256sum -c` before copying it, and records the
hash in `SINGBOX-INFO`. It also pins the authoritative SHA-256 digests that GitHub
reports for the two official v1.14.4 release archives (that release has no
separate `SHA256SUMS` asset) and re-verifies an archive when it is supplied;
`REQUIRE_SINGBOX=1` makes the archives mandatory. `BUILD-INFO` stays the RouteLatch
build stamp and is not modified; the existing `dist/packages/SHA256SUMS` →
`sha256sum -c` flow is unchanged and now also covers the tarball containing the
sidecar.

**Manual step before a release build:** the operator must supply the verified
binaries, the official archives and the checksum file under `dist/singbox/`
(see `packaging/README.md` for the authoritative obtain-and-verify procedure and
the pinned archive digests). Without them, a routine build packages without the
sidecar and prints a notice; `REQUIRE_SINGBOX=1` makes any missing input a hard
error. This work does not download, install or publish anything.

## 3. Name check (RouteLatch)

Evidence observed on 2026-11-18:

- `https://routelatch.com/` is a live commercial SaaS ("trade-show control
  tower", event logistics), registered 2026-08-27 per RDAP, actively marketed.
  Same name, unrelated field, no visible ™/® marking on the site.
- GitHub: `https://github.com/routelatch` is 404 and the user search returns 0;
  `https://github.com/search?q=routelatch&type=repositories` returns two 0-star
  repos (`orlandophillipss/routelatch`, LLM workflow routing;
  `G-Kondor/RouteLatch`, GPX navigation). The handle is free.
- `https://registry.npmjs.org/routelatch`, `https://pypi.org/pypi/routelatch/json`,
  `https://crates.io/api/v1/crates/routelatch`,
  `https://rubygems.org/api/v1/gems/routelatch.json` and
  `https://proxy.golang.org/github.com/routelatch/routelatch/@v/list` return
  404/no match; Docker Hub (`https://hub.docker.com/`) and `pkg.go.dev` were
  blocked (403), so Docker is unverified.
- Trademark registers (USPTO/Justia, TMview, UK IPO, WIPO) were all blocked by
  CAPTCHA/403 from this network. **No trademark clearance was performed.**

Recommendation: **keep the name with mitigation** — use a distinguishing tagline
("proxy routing for Linux"), avoid implying affiliation, and register a free
domain (`.org` verified unregistered; `.dev`/`.io` likely but unverified). Before a
public announcement, obtain a real clearance search in classes 9 and 42 from an
unblocked network. A rename is not required by the reachable evidence and must not
be done without authorization.

## 4. Dependency audit refresh

Re-ran the audit path at the current `go.mod`/`go.sum` (unchanged since `7792cac`,
`git diff` empty):

- `go list -deps` with `GOOS=linux`, `CGO_ENABLED=0`: **68 third-party modules**
  for both `amd64` and `arm64`;
- `go version -m` on both built binaries: **68 modules** each, same versions;
- `go.mod`: 73 requirements; the 5 unlinked (4 Windows-only, `kr/text`) are listed
  with reasons in `THIRD-PARTY-NOTICES.md`;
- the 68 `(module, version)` pairs in the notices table match `go list` exactly;
  6 embedded components are documented;
- `go mod verify` → `all modules verified`; `go test ./internal/licensing/` → ok.

No drift; no notices change was needed.

## 5. Checks run

`go build ./...` ok, `go vet ./...` ok, `go test -count=1 ./...` ok (all 16
packages; `internal/acceptance` 115.7 s, `internal/licensing` ok). The gated
systemd/desktop acceptance runs were not re-executed; they remain gated and were
last verified in R1/R2. Package and artifact checks are in §2. This paragraph is
the v0.1.0 snapshot; the R12 Phase 1 re-run is recorded above and passed 17/17
packages.

## Historical pre-push gates (resolved for v0.1.0)

These were the gates before the v0.1.0 push. v0.1.0 proceeds with the local path
leak accepted (a local path, no credential or pool data); packaging built from
the current `main`; the name kept as RouteLatch, with no trademark clearance
claimed; the committer email taken from the repository's Git configuration; and
the verified sing-box v1.14.4 sidecar supplied under `dist/singbox/`. The final
item remains a nice-to-have and is out of scope for v0.1.0. The original list is
kept below as the decision record.

1. **Decide the history path leak.** Accept it (the string is a local path and
   username, no credential and no pool data) or rewrite history in a fresh copy of
   the repository before the first push. Do not rewrite the shared branch.
2. **Merge `r8-release` into the release branch and rebuild.** `main` moved to
   `c1e60ea` (public README, CI, policies) while this branch was based on
   `47ec19a`; the inspected package therefore carries the pre-rewrite README. Build
   and package again after the merge, then re-check the tarball list and version.
3. **Confirm the name decision** and obtain real trademark clearance before
   announcing; Docker Hub remains unchecked.
4. **Decide on the committer email** in public history.
5. **Supply and verify the sing-box v1.14.4 binary** under `dist/singbox/` before
   building a release package, using the procedure in `packaging/README.md`;
   confirm the pinned checksum comes from the official release and that
   `REQUIRE_SINGBOX=1` packaging succeeds. No binary is included in this change.
6. Nice-to-have, outside this change: pin GitHub Actions to commit SHAs, and add
   this file to `docs/README.md` and the plan's status record.
