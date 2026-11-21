# Release readiness: history, package, name and audit

Status: **no credential, private pool content or binary is in the repository's
history.** The current tree is clean of absolute personal paths.

The first public release, **v0.1.0**, is tagged on `main` and publishes two Linux
tarballs as assets on the GitHub Release. **v0.1.1** added four-architecture
tarballs, Debian packages and RPM packages. **v0.1.2** patched the desktop-proxy
`set` behaviour. The current release, **v0.1.3**, is a patch of import
validation and derived status with the same 17-asset layout; its evidence is in
"v0.1.3 release (current)" below. The v0.1.2, v0.1.1 and v0.1.0 sections, the
history scan, name check and dependency audit remain historical evidence; they
are not repeated here. The tags, pushes and releases were explicitly authorized
by the project owner.

The sections below were originally a dated readiness snapshot (branch
`r8-release`, binary/version `110cce3`, based on `47ec19a`, with `main` then at
`c1e60ea`). Redaction rule used throughout: findings name the commit, path and
kind, never the matched value.

## v0.1.3 release (current)

Tag `v0.1.3` is an annotated tag (tag object `6f226688c8e48ecbc7570383b6e86df259b7b58c`) on
`main` commit `a32b033daca0ed2e964ad4870f5511ff0bc0a2c7` (`a32b033`). The
commit and tag were pushed to both `github` and `origin` (forgejo); both remotes
report the same tag object and the same peeled commit. The public GitHub Release
is <https://github.com/m-hosseinpour/routelatch/releases/tag/v0.1.3>; the
repository is public and the release is neither a draft nor a prerelease. This
patch rejects a `hysteria2` exit that sets `obfs: gecko` without an
`obfs-password`, and derives stored records in that shape to
`unsupported`/`missing_obfs_password` instead of handing them to the pool or the
sidecar runtime. It adds no user-facing capability and changes no public
contract or stored-state format. The cycle also added gated acceptance cells for
long-duration transport reuse, hysteria2 obfs (including the negative cases),
corrupt state under a temporary `systemd --user` unit, the shipped-sidecar proof
against the published v0.1.2 tarball, and amd64 RPM payload execution.

The release publishes **17 unsigned assets** in the same layout as v0.1.1 and
v0.1.2: four Linux tarballs, four `.deb` files, four RPM specs, four binary RPMs
(`routelatch-0.1.3-1.<arch>.rpm`) and one `SHA256SUMS`.

Evidence: the tarballs, `.deb` files and specs were built from a clean
`git archive a32b033` export in an isolated `DIST` with `VERSION=v0.1.3`,
`COMMIT=a32b033`, `SOURCE_DATE_EPOCH=1700000000`, Go 1.27.4 and
`CGO_ENABLED=0`; `scripts/check-artifact.sh` passed for amd64 and
`scripts/check-package.sh` passed once over all four architectures. A second
packaging run produced a byte-identical `SHA256SUMS`
(`7658f5a07be21fc2bcbe27b5c24eb58e6b273900c4c75197a298cfb74e314588`; both
runs), and `sha256sum -c` passed for `dist/SHA256SUMS` and
`dist/packages/SHA256SUMS`. A separate packaging run without the sidecar inputs
also passed `scripts/check-package.sh`, and the shipped tarballs carry
`bin/sing-box` plus `SINGBOX-INFO` only on amd64/arm64. The pinned sing-box
v1.14.4 archives were verified against the digests pinned in
`scripts/package.sh` (amd64 `699a1b51…c24968`, arm64 `436441f7…3ec26`), and each
archive was extracted and compared byte for byte with the supplied binary
before use. The four binary RPMs were built from the v0.1.3 specs inside a
disposable `ubuntu:26.04` container with rpm 6.0.1 (nothing installed on the
host, where `rpmbuild`/`rpm` remain absent), using `rpmbuild --target <arch>
-bb`. Each was inspected for `Name`, `Version=0.1.3`, `Release=1`, the expected
`Arch`, `License=Apache-2.0`, `Requires: ca-certificates`, the exact file list
(14 files with the sidecar on x86_64/aarch64, 13 without it on
riscv64/ppc64le), no scriptlets, no triggers and no `/etc/systemd` entry; the
x86_64 RPM was installed with `rpm -i --nodeps` in the same container and its
`/usr/bin/routelatch version` reported `v0.1.3`/`a32b033`, with
`/usr/bin/sing-box version` reporting 1.14.4/`c005f353`, before removal. Green
GitHub Actions run `44883729124` on `a32b033` (all six jobs, including
`rpm-packages`) is the CI gate; its own artifacts carry the CI-only `0.0.0.ci`
stamp and were not shipped.

Three operational pitfalls were hit and corrected without weakening a gate. The
direct `go.dev` toolchain download from this network returned an HTML 404 page
instead of the archive, so Go 1.27.4 came from the aliyun mirror and its
SHA-256 matched the digest published at `go.dev/dl` (`7226736c…b24840`). A
first `rpmbuild` invocation pointed `_topdir` at `/work/rpmbuild` while the
disposable container mounts this directory at `/work`, which made `%setup`
report the existing source as missing; it was corrected to the mount root, and
no RPM from the failed invocation was used. The first asset upload attempt was
rejected with HTTP 422 because curl defaulted to
`Content-Type: application/x-www-form-urlencoded`; it created no assets. A
single proxied upload stream sustained only about 40 KB/s, so the remaining
assets were uploaded over direct connections four at a time; one 31.9 MB RPM
returned HTTP 408 ("Upload body timed out due to inactivity") and succeeded on
its second attempt, and a failed attempt left no asset behind.

The final public API check confirmed all 17 asset names, sizes and `sha256:`
digests match the local files exactly, with no extras, and that the annotated
tag points at `a32b033`.

Authoritative digests (also in the uploaded `SHA256SUMS`, whose own SHA-256 is
`936da687019ce3339fbb518d5a7e43e8c7b16810d7c06340d17619118d351df8`):

```
e75785a12553b79e7c5efb79c4c58afdbdbbc66c1a538520ab33a720d4ed5e86  routelatch-0.1.3-1.aarch64.rpm
901072a4353e9c2c0788efcaa65ab710951f0ab83f1cfcaaff73d4508199b34d  routelatch-0.1.3-1.ppc64le.rpm
fb26c4a34e7e8b4b0f335110cdb2d8182c48c20d6b17c7bd77db02fe1db16f39  routelatch-0.1.3-1.riscv64.rpm
29fd4487ab2563e43a53aa74b0875eaf0c550b8d6469ea57f999e12d1d09456c  routelatch-0.1.3-1.x86_64.rpm
58511d9357442d0e5fe8631a2a20b6f088d6ff04574e128f1d3b8b65543feb9f  routelatch-aarch64.spec
1d3cf6d905d78ae0ec4b56f91ed36d0b728aaa0ed546e249f92bf4288670c717  routelatch-ppc64le.spec
519c42e3fc5632cb7a232a1ba6f4bdda633bfd9af29a7a3550db3df02db3e3f8  routelatch-riscv64.spec
ba493b8d1279b0591b624042446e8128dcc5832759e3c2ad1abace404ebd3b83  routelatch-v0.1.3-linux-amd64.tar.gz
a67e41f9939fbadd7aeed8e86d1451d1a1bcab9d44ae0ded7188b706f7739189  routelatch-v0.1.3-linux-arm64.tar.gz
8cf955b6812fe990482492440002e5859c31b432d216df2868b6489800f98bca  routelatch-v0.1.3-linux-ppc64le.tar.gz
ffbe0015e1646a8ea36bcfaaf822b091599be147b9c7db4060c38f87d7ea8d7e  routelatch-v0.1.3-linux-riscv64.tar.gz
831fbf8b54c7f4eaf272c820bc44946ff6cc3e3af9d2900827b398a71a4376c8  routelatch-x86_64.spec
3bfee2b4942df60c35573723a7c3677ccc0bca10e28762dc98121ab9b6532196  routelatch_0.1.3_amd64.deb
e11787f25f5235b169d25d3ec85d87430b2040046e398d2a7eb78b7dcd9717ce  routelatch_0.1.3_arm64.deb
df9497c91df59515de3b7bb38817d2c8c3ef2a966c20925ae4c7ec7a0eac832f  routelatch_0.1.3_ppc64el.deb
e7c16ce5a713e4aa4ed37c42db55951bb4cdcb864b5c6b5e0d13b057ea331a68  routelatch_0.1.3_riscv64.deb
```

Limits: `amd64` is runtime-exercised; `arm64` is cross-built and
runtime-untested; `riscv64` and `ppc64le` are cross-build-only and
runtime-untested. No GNOME session was proven. Real-scale pool and public-exit
proofs remain deferred to the later group-C batch. The sidecar is pinned only
for amd64/arm64. No clean-machine install, GPG signature or package repository
is claimed.

## v0.1.2 release (historical)

Tag `v0.1.2` is an annotated tag (tag object `3d1c181a9d163f90f3074a7a32bb876fa630e4a4`) on
`main` commit `88d04b1a17058cd3680c9f7d3666662dc48a00d2` (`88d04b1`). The commit and
tag were pushed to both `github` and `origin` (forgejo). The public GitHub Release
is <https://github.com/m-hosseinpour/routelatch/releases/tag/v0.1.2>; the repository
is public and the release is neither a draft nor a prerelease. This patch fixes
`desktop proxy set` after a manual desktop-proxy edit: it re-applies, records only
the user-edited keys as new restore points, and leaves untouched keys with their
original values so `desktop proxy restore` still undoes the whole proxy.

The release publishes **17 unsigned assets** in the same layout as v0.1.1: four
Linux tarballs, four `.deb` files, four RPM specs, four binary RPMs
(`routelatch-0.1.2-1.<arch>.rpm`) and one `SHA256SUMS`.

Evidence: the tarballs, `.deb` files and specs were built in an isolated `DIST`
with `VERSION=v0.1.2`, `COMMIT=88d04b1`, `SOURCE_DATE_EPOCH=1700000000`,
Go 1.27.4 and `CGO_ENABLED=0`; `scripts/check-artifact.sh` and
`scripts/check-package.sh` passed, a second packaging run produced a byte-identical
`SHA256SUMS`, and `sha256sum -c` passed. The pinned sing-box v1.14.4 sidecar was
verified against the official archive digests and is included only for
amd64/arm64. The four binary RPMs were built from the v0.1.2 specs with an
**extracted, not installed** rpm 6.0.1 toolchain under `/tmp/opencode` and
inspected for `Name`, `Version=0.1.2`, `Release=1`, `Arch`, `License=Apache-2.0`,
`Requires: ca-certificates`, the exact file list and the absence of scriptlets.
Green GitHub Actions run `44736109624` on `88d04b1` (all six jobs, including
`rpm-packages`) is the CI gate; its own artifacts carry the CI-only `0.0.0.ci`
stamp and were not shipped. The first asset upload via Python's `urllib` timed out
before any asset was stored; the release was kept and all 17 assets were then
uploaded with streaming `curl` (the failed attempt created no partial asset set).
The final public API check confirmed all 17 asset names, sizes and `sha256:`
digests match the local files exactly, with no extras, and that the annotated tag
points at `88d04b1`.

Authoritative digests (also in the uploaded `SHA256SUMS`, whose own SHA-256 is
`83b51b5b4f4b4d963b259de26c292963fb22276237df32908bc16ab1ea503bf2`):

```
fedcfbe29e66366603f1548aa03234a3dc7797465429db1d8d8c45366cab44c6  routelatch-0.1.2-1.aarch64.rpm
b10d9075d462ffcf4092998869532dfeafb07c600c1628025c1f3b458e706ca8  routelatch-0.1.2-1.ppc64le.rpm
b9d9af54080f45bbed0caf38e08e5379bd680a0b30590d6e04a30205ef6daeb2  routelatch-0.1.2-1.riscv64.rpm
5ed731097096a91a8dddd5fcf831cdf94061f6efb2e4d864ebf4115bb47756a1  routelatch-0.1.2-1.x86_64.rpm
9d1ffeeedfb585752a06f5acadeb3e3d3dc78224379cda0cc6211d78f7384899  routelatch-aarch64.spec
c3cdc123c135085e4e80c32df6d1436c18bca248fe93621f264ab84b0a343d92  routelatch-ppc64le.spec
cb9fcb93af83aaa6d8f1a9f20939c8cf53e67b65dde930f60b81f11115bef380  routelatch-riscv64.spec
fd6158bb7bb8973a9350e8419f6826066c276673c539e28ad7388d24ecb5de6e  routelatch-v0.1.2-linux-amd64.tar.gz
e14119d5f69728ac8a89a2a0360bc89b3e71f5b570eb9d4c4842326379ffebb0  routelatch-v0.1.2-linux-arm64.tar.gz
40799f0a09118cc3e3b534dd82c43a2a3e73441b58558d3e965b750eaca5e108  routelatch-v0.1.2-linux-ppc64le.tar.gz
5cbd780ccc5454288bec4c94f22e4a2ae6feeb4f049a4ae6eb066b7885630bb7  routelatch-v0.1.2-linux-riscv64.tar.gz
79196d11b063f9a1c671aec8881eb378a430243549c4d589f55015e6fa5ae7a7  routelatch-x86_64.spec
aa5f7d16020fdf0de852d879d6dfdcdebca6c436f97e0216a4e2c9e5c1fa7b15  routelatch_0.1.2_amd64.deb
df524e16b3c2e1206681681dc977617686e02c72ff32b192991838e71ace4f1b  routelatch_0.1.2_arm64.deb
9217ec9f5ec5948598aee2a143ec9a7516ce4b838e12337750eb2f2f920e7f98  routelatch_0.1.2_ppc64el.deb
15a8c85e31e037f427622d0c2c5f5b0c3a3a84bf4fae7604a25fbe97ba0586af  routelatch_0.1.2_riscv64.deb
```

Limits: `arm64` is cross-built and runtime-untested; `riscv64` and `ppc64le` are
cross-build-only and runtime-untested. The sidecar is pinned only for
amd64/arm64. No clean-machine install, GPG signature or package repository is
claimed.

**Local RPM build and payload run (post-v0.1.2, 2026-11-21).** At commit
`42b9e4e`, an `x86_64` RPM `routelatch-0.1.2.8.g42b9e4e-1.x86_64.rpm` was built inside a
disposable `ubuntu:26.04` container with `rpmbuild` 6.0.1; RPM tooling was not
installed on the host, where `rpmbuild`/`rpm` remain absent. The package (14
files, no scriptlets, no service enablement) was inspected, its payload binary
executed (`routelatch version` → `v0.1.2-8-g42b9e4e`, `doctor` exit 0), and
`rpm -i --nodeps` succeeded inside the container. Not exercised: a real systemd
service start under an RPM install and every non-amd64 architecture; the package
is unsigned.

## v0.1.1 release (historical)

Tag `v0.1.1` is an annotated tag (tag object `28d1cf89`) on `main` commit
`2f6381430be9b39aeb736cd77566986a16354fa7` (`2f63814`). The commit and tag were
pushed to both `github` and `origin` (forgejo). The public GitHub Release is
<https://github.com/m-hosseinpour/routelatch/releases/tag/v0.1.1>; the repository
is public and the release is neither a draft nor a prerelease. The tag still
points at the intended commit.

The release publishes **17 unsigned assets**: four Linux tarballs
(`amd64`, `arm64`, `riscv64`, `ppc64le`), four `.deb` files (`amd64`, `arm64`,
`riscv64`, `ppc64el`), four RPM specs (`x86_64`, `aarch64`, `riscv64`, `ppc64le`),
four binary RPMs (`routelatch-0.1.1-1.<arch>.rpm`) and one `SHA256SUMS`.

Evidence: the tarballs, `.deb` files and specs were built in an isolated `DIST`
with `VERSION=v0.1.1`, `COMMIT=2f63814`, `SOURCE_DATE_EPOCH=1700000000`,
Go 1.27.4 and `CGO_ENABLED=0`; `scripts/check-artifact.sh` and
`scripts/check-package.sh` passed, and `sha256sum -c` passed for both dist and
package checksum files. The pinned sing-box v1.14.4 sidecar was verified against
the official archive digests and is included only for amd64/arm64. The four binary
RPMs were built from those v0.1.1 specs with an **extracted, not installed**
rpm 6.0.1 toolchain in `/tmp/opencode` (five Debian packages downloaded with
`apt-get download`, unpacked under `/tmp/opencode/rpm-tools`); each was inspected
for `Name`, `Version=0.1.1`, `Release=1`, `Arch`, `License=Apache-2.0`,
`Requires: ca-certificates`, the exact file list and the absence of scriptlets.
Green GitHub Actions run `44682457124` on `d443bd0` (all six jobs, including
`rpm-packages`) is the CI gate behind the specs; its artifacts carry the CI-only
stamp `0.0.0.ci`, so those validation RPMs were **not** shipped as release assets.

**Correction to the first upload.** The first upload of this release was
incomplete/wrong: it contained four `0.0.0.ci` validation RPMs, and a mid-upload
observation showed the riscv64 tarball and the `.deb` set as missing. The five
wrong assets (four CI RPMs and `SHA256SUMS`) were deleted from this release only;
the four v0.1.1 RPMs and a regenerated `SHA256SUMS` were uploaded in their place,
and the still-uploading tarballs/`.deb` files were allowed to finish. The final
public API check confirmed all 17 asset names, sizes and `sha256:` digests match
the local set exactly, with no extra assets; the release note was corrected to
remove the CI-version claim. No v0.1.0 tag, release or asset was touched.

Authoritative digests (also in the uploaded `SHA256SUMS`, whose own SHA-256 is
`812c6898f930c025d782b9b2df8f055d198f7aa499df82a3b7bf6e24626919f7`):

```
451744c7bd64ba3a3db4708e285855bdfbfbaa374a31cae0374e7adc3dbf29a2  routelatch-0.1.1-1.aarch64.rpm
a657bb49cb97d182721bc7f7033fba774861836915a139973e5857bfc13f16b7  routelatch-0.1.1-1.ppc64le.rpm
a8cb5c7ee679a9a5f68a728d4ed916e98340ed48b03001867a0f25b59ff2fb5c  routelatch-0.1.1-1.riscv64.rpm
a3af653cabc7a4725b350a23f9a26d628226ba9fca73e7b62e866d09f56b1750  routelatch-0.1.1-1.x86_64.rpm
7992590e8e5e5d96e75885f7cb2ee7798c6874630667ffe27854f01cae1ddead  routelatch-aarch64.spec
b83f8d278114240b46caf51daa74c2ca7caf55579225ddaabe21462168e79e8a  routelatch-ppc64le.spec
b4cabb155302da0297823e3404e2f8e9c6af0104c7d99d27f8e0375ee939e029  routelatch-riscv64.spec
dfda483bc24109991fe7a63edfc037289545c0d6e0a8d70ba586b6775306f48b  routelatch-v0.1.1-linux-amd64.tar.gz
8ee99120323c66f38a5a60f04515043e1a397806d8772cd2c51a4d3727dbbf7f  routelatch-v0.1.1-linux-arm64.tar.gz
d7c395eac912bbd549612e653ad0d73cc5fd24fd37bade8aa26a27e9ebaf1490  routelatch-v0.1.1-linux-ppc64le.tar.gz
936107cb18e9747de5858169faac2e5279a5f6922782231c0cd575186a0ef526  routelatch-v0.1.1-linux-riscv64.tar.gz
895745b76dff016646a592b0157ccc6166fe1c52bdcbc4c4877b9aafda88ebf2  routelatch-x86_64.spec
9576b1c27257c5fdc9328330dea323c595b9d8556e11c275a7f897adb70be657  routelatch_0.1.1_amd64.deb
bf54b4f8c8224107af127fb4c9535654aadecefbcee180eb6573f7a5602cb1ad  routelatch_0.1.1_arm64.deb
7965016798381240092940e504f36d1e2741c0018f46461384d130509a7830ce  routelatch_0.1.1_ppc64el.deb
e12643b109cef6ba51626dff3a6745fb7ff75a2e7259af7e372d03df54570d89  routelatch_0.1.1_riscv64.deb
```

Limits: `arm64` is cross-built and runtime-untested; `riscv64` and `ppc64le` are
cross-build-only and runtime-untested. The sidecar is pinned only for
amd64/arm64. No clean-machine install, GPG signature or package repository is
claimed.

## v0.1.0 release (historical)

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

## R12 Phase 1: local Debian packages (historical snapshot; unpublished at the time)

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

## R12 Phase 2: RPM specs and CI-built binary RPMs (historical snapshot; unpublished at the time)

Snapshot of Phase 2 as it stood on 2026-11-19 (commit `6a7580f`). The limits in
this section describe that state; later local and CI RPM builds and the published
v0.1.1/v0.1.2 RPMs are recorded above and in the implementation plan.

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
`/usr/share/licenses/routelatch/` and the systemd user unit under
`/usr/lib/systemd/user/` with `ExecStart` rewritten to `/usr/bin/routelatch run`
(the same adaptation the `.deb` uses); the optional sing-box sidecar (amd64/arm64 only)
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
