# Release readiness: history, package, name and audit

Status: **no credential, private pool content or binary is in the repository's
history.** The current tree is clean of absolute personal paths.

The first public release, **v0.1.0**, is tagged on `main` and publishes two Linux
tarballs as assets on the GitHub Release. **v0.1.1** added four-architecture
tarballs, Debian packages and RPM packages. **v0.1.2** patched the desktop-proxy
`set` behaviour. **v0.1.3** patched import validation and derived status with
the same 17-asset layout. **v1.0.0** shipped the D26 app/tray runtime,
`autostart`, the opt-in `service` and unit-free packages; its evidence is in
"v1.0.0 release (historical)" below. **v1.0.1** was the first release built and
published by the CI pipeline. **v1.1.0** added the R15 tray control surface,
`status.resume_requires_restart` and the process-level restart lifecycle; its
evidence is in "v1.1.0 — tray control surface (2026-11-26)" below. **v1.2.0**,
the current public release, adds opt-in TUN coverage (A17 = Accepted with
limits); its evidence is in "v1.2.0 — opt-in TUN coverage (2026-11-27)" below.
A **v1.2.1 patch candidate** (the R-28 status warning) is prepared at `2d4da2a`
with evidence at `d36361b`; its tag, CI run and Release are **pending**, so it
is not a public release. The v1.0.1 evidence is in "v1.0.1 — first CI-built
release (2026-11-25)". The v1.0.0, v0.1.3, v0.1.2, v0.1.1 and v0.1.0 sections,
the history scan, name check and dependency audit remain historical evidence;
they are not repeated here. The tags, pushes and releases were authorized by
the project owner (v1.2.0 under the standing authorization recorded in
`AGENTS.md` on 2026-11-26).

**CI release pipeline (D27, 2026-11-24).** From `v1.0.1` on, GitHub Actions
builds all 17 assets and creates the public GitHub Release on an annotated `v*`
tag push; nothing is built or uploaded from a local machine. The workflow is
implemented in `.github/workflows/release.yml`, was exercised by complete
pre-merge dry-runs (see "CI release pipeline (R14 unit a)" below) and is merged
into `main` (head `0183e3f`, 2026-11-24). On 2026-11-25 the project owner
authorized the first CI-built release, **v1.0.1** (patch). Four tag pushes were
made. Attempts 1 and 2 failed in the plan gate with `tag v1.0.1 is not
annotated`; the cause was `actions/checkout` rewriting the local tag ref as a
lightweight ref, not a wrong `github.sha`, and the gate now force-fetches the
real tag object (see "v1.0.1 attempts"). Attempt 3 passed the gates, created a
draft, uploaded all 17 assets, then failed in the upload verification: GitHub's
`GET /releases/tags/{tag}` answers 404 for a **draft**, so the verification
never saw the release it had just built, and the cleanup had the same blind spot
and left a draft with 17 assets (id `457460945`). The publish path now addresses
the draft by its numeric release id, and the guard/cleanup list releases instead
of looking up by tag (see "Third v1.0.1 attempt"). The owner deleted that draft
with explicit authorization on 2026-11-25, and an authenticated listing then
showed no `v1.0.1` release or draft. After independent review of the fix, the
old tag was deleted locally and on both remotes and **v1.0.1** was recreated on
the fixed, CI-green commit; attempt 4 published the release (see the v1.0.1
section). The v1.0.1 and earlier release sections below remain evidence of the
local build/upload process used for `v0.1.0`–`v1.0.0`.

## v1.2.0 — opt-in TUN coverage (2026-11-27)

Tag `v1.2.0` is an annotated tag (tag object
`cda1fe352a2b1144633415f8b3071b2dcf542382`) on `main` commit `a8ee4d3`, pushed
to both remotes. The release adds opt-in TUN coverage (D29/A17) on top of the
v1.1.0 tray control surface. Routing policy, the approved list and the
fail-closed path are unchanged; DNS remains direct and non-DNS UDP/QUIC remains
blocked (D30/D31/D32).

`CI` push run `45984462533` on `a8ee4d3` (attempt 2) was green before the tag
push. `Release` run `45985695347` (tag push) succeeded. The public GitHub
Release is
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.2.0> (release id
`460485888`, title `v1.2.0`, neither a draft nor a prerelease). It carries
exactly 17 assets: four `routelatch-v1.2.0-linux-<arch>.tar.gz` tarballs, four
`routelatch_1.2.0_<debarch>.deb` packages, four `routelatch-<rpmarch>.spec`
files, four `routelatch-1.2.0-1.<rpmarch>.rpm` binary RPMs and `SHA256SUMS`.
Its body starts "RouteLatch v1.2.0 — opt-in TUN coverage" (the annotated tag
message).

The release carries the A14 licensing/source-offer/package evidence already
established for the CI pipeline (Apache-2.0 `LICENSE`, `NOTICE`,
`THIRD-PARTY-NOTICES.md`, the MPL-2.0/GPL source offer, and checksummed
tarball/`.deb`/spec/RPM assets). The v1.2.1 candidate carries the same evidence.
This record does not claim a new security review.

Known limits carried from the R16-H acceptance record:
`spike/tun/H-ACCEPTANCE.md` and
`spike/tray/evidence/r15h-acceptance-2026-11-26.md`. TUN runtime was exercised
on `amd64` only; Wayland/GNOME, a real TUN-active host tray/toggle, panel
contrast, Login/autostart auto-enable and `arm64` runtime remain unverified.
The R-28 off-link path and the start-limit recovery were verified after the
release (see the v1.2.1 candidate record).

## v1.2.1 — candidate patch (pending tag/CI/Release; not public)

**This version is not released.** The candidate patch is prepared at
`2d4da2a` (with `cd1c848`) and its verification evidence is `d36361b`;
`v1.2.0` remains the current public release until the candidate is tagged and
published.

The fix closes the R-28 status-visibility gap found after v1.2.0: an approved
egress dial that fails with kind `exit_unreachable` records an in-memory
timestamp, and top-level `status.warnings` then reports the fixed sentence
`recent approved egress dial failed; traffic remains fail-closed with no direct fallback`
for 60 seconds. The first successful approved pool dial clears it. There is no
change to health, ranking, cooldown, routing or persistence, and
`tun status`/the tray remain coverage-only, so they do not show this warning.
`handshake_rejected` and `timeout` are deliberately excluded because a
destination-side failure or stall can produce them. A genuine exit-dial timeout
is indistinguishable from a destination stall and is therefore unrecorded.

The independent review passed after the signal was restricted to
`exit_unreachable`; the earlier broader class set was the review finding.
Focused tests cover the warning's appearance and 60 s TTL, clear-on-success,
the excluded destination/transport classes, no health or persisted-state
mutation, and the CLI text rendering
(`TestStatusReportsRecentApprovedEgressDialFailure`,
`TestSuccessfulApprovedDialClearsEgressFailure`,
`TestEgressFailureWarningExpires`,
`TestDestinationFailuresDoNotSetEgressWarning`,
`TestOverBroadTransportKindsDoNotSetEgressWarning`,
`TestEgressFailureDoesNotMutateHealth`,
`TestPrintStatusPrintsEgressWarning`). No CI run exists for the candidate yet.

Post-fix off-link runtime verification at `2d4da2a`: **36 PASS / 0 FAIL /
0 NOTRUN**. The warning appeared after the failed approved dial, was absent at
62.1 s, cleared on the first successful approved dial (429 ms), survived
`tun off/on` inside the TTL, and the approved path stayed fail-closed with no
direct fallback, no loop and no exit CONNECT. The H addendum rerun with the
host VPN off reported **14 PASS / 2 NOTRUN**; IPv6 DNS/53 and the package
checks passed, and start-limit recovery is a recorded PASS (deterministic
20/300 then `start-limit-hit`, recovered by `reset-failed` plus socket start).
Fail-open in-gap status visibility remains **NOT RUN**: status calls block or
time out inside the gap, and no false `on` was observed.

Limits: synthetic local fixtures only; one break shape (`default → dummy0`),
one architecture (`amd64`) and a headless run, so no tray icon was observed;
the warning is process-local and is not expected to survive a restart; one TTL
observation only. Evidence:
`spike/tun/POST-RELEASE-VERIFICATION.md` and
`spike/tun/evidence/post-v1.2.0-verification-2026-11-27/`. The tag, `CI` run and
`Release` for v1.2.1 are still pending.

## v1.1.0 — tray control surface (2026-11-26)

Tag `v1.1.0` is an annotated tag (tag object
`7abe9704f66cbcc78fe6340093e8a365324b9407`) on `main` commit
`532c1697da6140fdfd5258dc98da2c1f7927cc61` (`532c169`), pushed to both remotes.
The release adds the R15 tray control surface, the additive
`status.resume_requires_restart` field and the process-level restart lifecycle.
Routing, pool and fail-closed behaviour are unchanged.

`CI` push run `45796357850` on `532c169` was green before the tag push.
`Release` run `45800238447` (tag push) succeeded in every job (plan, build,
package, RPM, assemble and publish). The public GitHub Release is
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.1.0> (release id
`459257636`, title `v1.1.0`, neither a draft nor a prerelease). It carries
exactly 17 assets: four `routelatch-v1.1.0-linux-<arch>.tar.gz` tarballs, four
`routelatch_1.1.0_<debarch>.deb` packages, four `routelatch-<rpmarch>.spec`
files, four `routelatch-1.1.0-1.<rpmarch>.rpm` binary RPMs and `SHA256SUMS`.
Its body starts "RouteLatch v1.1.0 — tray control surface" (the annotated tag
message). The release used the standing authorization recorded in `AGENTS.md`
and the implementation plan on 2026-11-26; no separate release request was
needed.

The release is bound to `532c169`. The later docs-only policy commit `271964d`
(current HEAD) failed in CI run `45797624302`: the `Test` step of the
"Build, vet and test" job failed while the local full suite was green; the
cause is not confirmed as a known flake.

Known limits carried from the R15-H acceptance record
(`spike/tray/evidence/r15h-acceptance-2026-11-26.md`): no screenshots in that
run; Wayland and GNOME were not tested; real desktop-proxy set/restore and the
autostart toggle were not run live; restore-and-quit/keep-and-quit, proposal
Accept/Recheck/Clear, pool remove/test and desktop set/restore were not clicked
live (unit/race coverage only); only the `amd64` artifact was executed, while
`arm64`, `riscv64` and `ppc64le` were cross-built and remain runtime-untested.

The sections below were originally a dated readiness snapshot (branch
`r8-release`, binary/version `110cce3`, based on `47ec19a`, with `main` then at
`c1e60ea`). Redaction rule used throughout: findings name the commit, path and
kind, never the matched value.

**No `v0.1.4` exists.** An annotated `v0.1.4` tag (tag object `8e224a7`) and its
public GitHub Release (id `453655267`, 15 of 17 assets uploaded) were created on
2026-11-21 for the documentation-and-evidence group-C batch (`1be8e9f`,
`79c0e53`) and withdrawn the same day at the project owner's request, because a
release that adds no user-facing value must not be published. The release was
deleted through the API (HTTP 204) and the tag was deleted locally and on both
remotes. `git tag -l v0.1.4` is empty, neither remote lists the tag, both `main`
heads point at `79c0e53`, and `v0.1.0`–`v0.1.3` are unchanged. That batch was
pushed only; **`v1.0.0` was the current release until `v1.0.1`, which
`v1.1.0` and then `v1.2.0` superseded.**

## v1.0.1 — first CI-built release (2026-11-25)

Tag `v1.0.1` is an annotated tag (tag object
`4462cc869110c0605eaa1a593fb8f82b05a04547`) on `main` commit
`d14da10d27e321e289e6bf5891142d76b71d649c` (`d14da10`). The old tag was deleted
locally and on both remotes after the earlier attempts, and this tag was created
on the fixed, CI-green commit and pushed. `git ls-remote` reports the same tag
object and peeled commit on forgejo and GitHub. The annotated message is the
released body.

`CI` push run `45517940277` on `d14da10` was green in all six jobs before the
tag push. `Release` run `45521099138` (tag push) succeeded in all five jobs; the
publish job `138895476159` created the draft, uploaded all 17 assets, verified
their name, size and digest by release id, and published. The public GitHub
Release is
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.0.1> (release id
`457516096`, title `v1.0.1`, neither a draft nor a prerelease).

Verification through the public API without authentication: the release has
exactly 17 assets with the names `scripts/release-assets.sh` expects (four
`routelatch-v1.0.1-linux-<arch>.tar.gz`, four
`routelatch_1.0.1_<debarch>.deb`, four `routelatch-<rpmarch>.spec`, four
`routelatch-1.0.1-1.<rpmarch>.rpm` and `SHA256SUMS`) and no extras. For all 16
checksummed assets the API `sha256:` digest equals the line in the downloaded
`SHA256SUMS`; `sha256sum` of `SHA256SUMS` itself is
`3f683f7044c9bf2e4252b35235d4b105528115fa645f933a4eb87ed607dab7d8` (also its API
digest), and every API size is positive. The four specs, `SHA256SUMS` and the
arm64 `.deb` were downloaded completely and pass `sha256sum -c`; the larger
assets could not be fully downloaded through the available network path (the
release-asset host reset large transfers), so their byte-level check rests on
the CI `sha256sum -c`/digest gate and the API digest, not on a local full
download. The optional amd64 tarball binary check
(`bin/routelatch version`, `bin/sing-box version`) was skipped for the same
transfer limit. The release body equals the annotated tag message exactly (the
fixed notes path).

Limits carried from the attempt section: a failure between draft creation and id
resolution can leave an orphan draft for manual by-id cleanup; `check` assumes
`per_page=100` rather than following `Link`; the live failure-and-cleanup path
and `workflow_dispatch` remain unexercised; the binary RPMs use the runner's
`rpm` version; and the CI `Test` step showed intermittent failures earlier
(`45494443321`, `45496529871`), so the release gate used the green push run
`45517940277` rather than a retry. No local build or upload was used for this
release.

## v1.0.0 release (historical)

Tag `v1.0.0` is an annotated tag (tag object `e6515c47d47f28510b942d7e2fc7e662bc82cc3c`)
on `main` commit `782e243de01bb4753fc73ec0099553fb6b24c10b` (`782e243`). The
commit and tag were pushed to both `github` and `origin` (forgejo); both remotes
report the same tag object and peeled commit. The public GitHub Release is
<https://github.com/m-hosseinpour/routelatch/releases/tag/v1.0.0> (release id
`456148592`); the repository is public and the release is neither a draft nor a
prerelease.

This is the D26 runtime release: plain `routelatch run` is the detached
app/tray, `run --headless` is the foreground engine, `autostart` is
default-instance-only, and `service install` is the opt-in systemd user unit.
The change is breaking relative to v0.1.3: the default `run` meaning changed and
packages no longer ship the old foreground unit, so the documented stop/remove
step for `/usr/lib/systemd/user/routelatch.service` is required before upgrade.
The `rlch` symlink remains.

The release publishes **17 unsigned assets**: four Linux tarballs, four `.deb`
files, four RPM specs, four binary RPMs (`routelatch-1.0.0-1.<arch>.rpm`) and
one `SHA256SUMS`. Evidence: the tarballs, `.deb` files and specs were built from
a clean `git archive 782e243` export in an isolated `DIST` with `VERSION=v1.0.0`,
`COMMIT=782e243`, `SOURCE_DATE_EPOCH=1700000000`, Go 1.27.4 and
`CGO_ENABLED=0`; `scripts/check-artifact.sh` passed for amd64 and
`scripts/check-package.sh` passed once over all four architectures. A second
packaging run produced a byte-identical `SHA256SUMS`
(`5b6964a1c4637ce861e25cea92ff2bb6d2eb991bb546809aa67044edf464aed7`; both
runs), and `sha256sum -c` passed 16/16. The pinned sing-box v1.14.4 archives
were re-verified against the digests pinned in `scripts/package.sh` before use;
`bin/sing-box` and `SINGBOX-INFO` are present only in amd64/arm64 packages.

The four binary RPMs were built from the v1.0.0 specs inside a disposable
`ubuntu:26.04` container with rpm 6.0.1 (nothing installed on the host, where
`rpmbuild`/`rpm` remain absent), using `rpmbuild --target <arch> -bb`. Each was
inspected for `Name=routelatch`, `Version=1.0.0`, `Release=1`, the expected
`Arch`, `License=Apache-2.0`, `Requires: ca-certificates`, `/usr/bin/routelatch`
and `/usr/bin/rlch`, no scriptlets/triggers and no systemd/autostart path. The
x86_64 RPM was installed with `rpm -i --nodeps` in the same container; its
`/usr/bin/routelatch version` reported `v1.0.0`/`782e243`, then it was removed.
Green GitHub Actions run `45293072576` on `782e243` (all six jobs, including
`rpm-packages`) is the CI gate. The upload had one operational retry: the amd64
tarball upload was cut off by a workstation restart; its partial `starter`
asset was deleted and the file was uploaded again. The final public API check
confirmed all 17 asset names, sizes and `sha256:` digests match the local files
exactly, with no extras, and that the annotated tag points at `782e243`.

Authoritative digests (also in the uploaded `SHA256SUMS`, whose own SHA-256 is
`5b6964a1c4637ce861e25cea92ff2bb6d2eb991bb546809aa67044edf464aed7`):

```
1fcb6c3d55be77662bb7ed32c5c1dd4341fc762cc7f1943e1f040edd363cd1ed  routelatch-1.0.0-1.aarch64.rpm
2026252dc1a1d838dece6fa677a8af32362abd5d7ba0d539cec66c37b8d7a9a2  routelatch-1.0.0-1.ppc64le.rpm
1d93076379df76c539e3c84bb85dc1cccdbe20b59f63ea49444d79f8c6c7d578  routelatch-1.0.0-1.riscv64.rpm
59f6aae7e8ca7f5407fc13693f5af3316656a39d2b5102fea86921cf9ceab6aa  routelatch-1.0.0-1.x86_64.rpm
24683e339d23346b9130c25f54ca22c1dd272d9309500bb86c99e8aff6f35f2f  routelatch-aarch64.spec
914a718919a0317881a0c7ee33cae78b3619663060917939906ddb3437bb455b  routelatch-ppc64le.spec
7a669aadfce4772aeea655ca8237ae4b05ecb33799b1bc4d9f93cd08b8da1dc6  routelatch-riscv64.spec
fc99e117cb2ed853a9292d052c408e69811d549aaeb74817ca308ba12d201e2e  routelatch-v1.0.0-linux-amd64.tar.gz
8c941d9834b148d47cc6462115c995ea4860125288a961f76a8761b84b2e11ec  routelatch-v1.0.0-linux-arm64.tar.gz
76f83b3516f0a7428558e395d610821a4b7d20a8948dd8aed47bbc97c6044620  routelatch-v1.0.0-linux-ppc64le.tar.gz
a88f1e4ec51004a6e9edbe5a240e9de5e7a4827833899e70fe68547b6bd341e7  routelatch-v1.0.0-linux-riscv64.tar.gz
a36e9b499e70ccae378d149a50d13057cb7bb136d4724710a0cd00cd96a9f6a8  routelatch-x86_64.spec
5fd9e9edc315ac765d72f2f2e5819bdf63f4a518f906b04270244fdccf66d614  routelatch_1.0.0_amd64.deb
a4142d6c40bd28af84524b9a51fad9674989e439a9765b1d0dbc80fc5bd5c51b  routelatch_1.0.0_arm64.deb
b416e4d3dbe11a15681736b84cd911712676eebb023f24270884ed279e8e3eb3  routelatch_1.0.0_ppc64el.deb
016ab50e670962468a655d0075a0814f719fb171e077a5922c09159a1f158f8f  routelatch_1.0.0_riscv64.deb
```

Limits: `amd64` is runtime-exercised; `arm64` is cross-built and
runtime-untested; `riscv64` and `ppc64le` are cross-build-only. Real
`service install`/uninstall/rollback/linger, the stale `/usr/lib` unit upgrade,
a tray Quit click, GNOME/SSH, pidfd `ENOSYS`/`EPERM`, non-amd64 runtime and the
default+`--data-dir` coexistence cell remain unverified. The sidecar is pinned
only for amd64/arm64. No clean-machine install, GPG signature or package
repository is claimed.

## CI release pipeline (R14 unit a, 2026-11-24)

`.github/workflows/release.yml` implements D27. An annotated `v*` tag push is
the production trigger; the dry-run path is a push to the permanent
`release-dryrun` branch. The workflow is now on the default branch (`main`), so
the `workflow_dispatch` input (`publish=false`) is available; it has not been
invoked, and the branch trigger is the path actually exercised.

Gates before any build: the ref must be an annotated `v*` tag, the tag must point
at the pushed commit, its message must be non-empty, the commit must be an
ancestor of `main`, and the `CI` workflow must have a successful push run on
`main` for that exact commit. `github.sha` is a commit on both branch and tag
pushes; the plan still peels it once, defensively, and uses that commit for the
tag comparison, the on-main check, the CI query and the `commit` output. Because
`actions/checkout` fetches a tag event's ref as `+<commit>:refs/tags/<tag>` — a
lightweight ref even when the pushed tag is annotated — the plan force-fetches
`+refs/tags/<tag>:refs/tags/<tag>` from `origin` before the annotated check, then
requires `git cat-file -t` on that ref to be `tag`, reads the message from that
exact object, peels it, and fails closed if the fetch or any check fails. The
workflow reads that CI conclusion instead of re-running the test suite; the build
and verification jobs below repeat the package, artifact and RPM checks. The
gates are not weakened for a dry run — they are not applicable to a branch run,
which never publishes.

Every build uses `VERSION` (the tag name, or `v0.0.0-dryrun` for a dry run),
`COMMIT`, `CGO_ENABLED=0` and `SOURCE_DATE_EPOCH=1700000000`. The package job runs
`scripts/build.sh`, `scripts/check-artifact.sh`, downloads the official sing-box
v1.14.4 amd64/arm64 archives and verifies them against the digests pinned in
`scripts/package.sh`, runs `scripts/package.sh` twice and requires identical
`SHA256SUMS`, runs `sha256sum -c` in `dist` and `dist/packages`, and runs
`RELEASE_BUILD=1 scripts/check-package.sh` (which also rejects a release package
whose `PACKAGE-NOTES` still says "local build"). The RPM job builds the four
binary RPMs from the generated specs and inspects name/version/release/arch/
license/`Requires`, the exact payload (with `sing-box` and `SINGBOX-INFO` on
amd64/arm64), the `rlch` symlink and the absence of scriptlets or units.
`scripts/release-assets.sh` joins the 12 package files and four RPMs, extends
`SHA256SUMS` to all 16 files and verifies the exact 17-file set.

The publish job is the only job with `contents: write`; it runs only for a tag
ref whose plan says `publish=true`. It re-verifies the assets, force-fetches the
real annotated tag object, and reads the release notes from that object rather
than from the checkout's lightweight tag ref. It then refuses if the tag already
carries a public release **or** any draft (the release listing, which includes
drafts), naming the existing release id and telling the operator to resolve a
leftover draft by hand; it never adopts or deletes a release it did not create.
Only after that guard passes does it create a **draft** and resolve its numeric
release id from the authenticated listing, because GitHub's
`GET /releases/tags/{tag}` answers 404 for a draft. It uploads all 17 assets,
polls `GET /releases/{id}` (bounded) until the release is this tag's draft and
every asset reports a size and `sha256:` digest, compares every uploaded name,
size and digest with the built files, and publishes the draft. A failure after
the draft id is resolved deletes that draft id only: a `draft-created` step
output gates the cleanup, and the cleanup re-reads the id and requires
`.draft == true` and the expected `.tag_name` immediately before deleting by id.
A guard error or an unreadable API state therefore cannot reach a delete. A
failure between `gh release create` and id resolution has no id to delete, so
that narrow window still leaves the draft for a manual by-id cleanup (the guard
then refuses a retry until it is gone).

Evidence:

- Green dry-run `45397329232` on commit `57e162c` (`release-dryrun`): the plan,
  package, RPM and assemble jobs succeeded, the publish job was skipped, and the
  releases API listed only the five existing releases before and after; no
  public release appeared. The unauthenticated releases listing cannot show
  drafts, so this run does not evidence the absence of a draft.
- Controlled failure `45399453678` on commit `37208c8`: a temporary step
  corrupted one built tarball before `check-package.sh`; the checker failed the
  job, the RPM/assemble/publish jobs were skipped, and the releases API again
  listed only the five existing releases, with no public release (same listing
  limitation for drafts).
- Final green dry-run of the first round `45400769014` on commit `2cdf05f` — the
  committed workflow without the injection, plus a step that lists the 17 names
  and 16 hashes: the same four jobs succeeded, publish was skipped, and no
  public release appeared. The workflow blob at that commit is
  `6be8e479777c78578dd1ec9687ae00d8698acffa`; a later documentation-only commit
  (`6b18145`, green run `45404439737`) ran the same blob.
- Green dry-run `45422318159` on commit `952276e` (guard/cleanup fix): the plan,
  package, RPM and assemble jobs succeeded — including the second packaging run
  with an identical `SHA256SUMS`, `sha256sum -c` in both directories, the four
  inspected RPMs and `scripts/release-assets.sh verify` over exactly the
  17-asset set. Publish was skipped and the releases/tags API still listed only
  the five existing releases and five tags. The workflow blob is
  `e723d83a83a9e86bf3c5c0d3e1ae7abf5fffd2c3`.
- Executable-bit fix `2c8fa1c` (mode `100644` → `100755` on
  `scripts/release-verify-upload.sh`, blob `79cd521` unchanged): Release
  `45427473470` and CI `45427473102` were green on that commit; publish was
  skipped and the workflow blob stayed
  `e723d83a83a9e86bf3c5c0d3e1ae7abf5fffd2c3`.
- Final green dry-run `45463989732` on commit `02e3226` (the
  verification-failure message now names missing or extra assets;
  `scripts/release-verify-upload.sh` blob `105ad6e`): the plan, package, RPM and
  assemble jobs succeeded over exactly the 17-asset set, publish was skipped,
  and CI `45463989826` was green. The workflow blob is still
  `e723d83a83a9e86bf3c5c0d3e1ae7abf5fffd2c3`, so every run since the
  guard/cleanup fix has exercised the same workflow; the commits after
  `02e3226` are documentation-only and change no workflow or script content.

The dry-runs proved: all 12 package artifacts and four RPMs were built and
inspected; `check-artifact.sh` and `check-package.sh` passed; the second
packaging run produced the same `SHA256SUMS`; `sha256sum -c` passed; both pinned
sing-box archive digests matched; and the exact 17-file set passed
`scripts/release-assets.sh verify`. The logs API needs repository
authentication, so this evidence is the public jobs API (job and step names with
their conclusions) plus the public releases API for the absence of a release.
`scripts/release-assets.sh` was also exercised locally on a synthetic
package/RPM set; its extra-file and corrupted-checksum negative cases failed as
intended.

Limits: the publish job has now run end to end on the `v1.0.1` tag: it created
the draft, uploaded all 17 assets, verified them by numeric id and published the
release (run `45521099138`; see "v1.0.1 — first CI-built release (2026-11-25)").
The failure paths were exercised only with stubbed `curl`/`gh` and a disposable
local git remote (see "Third v1.0.1 attempt"), not against the live API: a live
failure-and-cleanup cycle, the API's transient null `.digest` behaviour and a
live draft delete remain untested. Two known limits are accepted: a failure
between `gh release create` and resolving the draft id leaves a draft that needs
a manual by-id cleanup (and the guard refuses the next attempt until it is
gone); and `release-guard.sh check` stops at the first page shorter than 100,
which assumes the API honours `per_page=100` rather than following the `Link`
header (the create step uses `gh api --paginate`, which does). `workflow_dispatch`
has not been invoked: before merge it required the workflow file on the default
branch, and after merge it still requires an authenticated dispatch that this
session did not perform. A dry run does not exercise the CI-green or on-main
gates, which apply only to tags. The binary RPMs are built with the runner's
unpinned `rpm` package, so only the tarballs, `.deb` files and specs are
deterministic for the same inputs; the run records the RPM tool versions and
`SHA256SUMS` is the binding record of the published RPMs. The `release-dryrun`
branch trigger is permanent, so a push to that branch normally costs a full
build without publishing. A docs-only push whose head commit message carries
`[skip ci]` is skipped: the documentation commit `e85b9b7` produced no workflow
run, while the earlier documentation commit `f99aa71` without the marker
triggered Release
`45425595274` and CI `45425595160`.

## v1.0.1 attempts (2026-11-25): gate, draft and notes fixes

The first two attempts stopped in the plan gate before any build and published
nothing.

**Attempt 1.** The start commit `fb969fb` ("docs(plan): start first CI release
v1.0.1", no `[skip ci]`) was pushed to both remotes. GitHub Actions CI run
`45490941645` (`push`, `main`, `fb969fb`) completed green with all six jobs.
Annotated tag `v1.0.1` (tag object
`da6307e4d28fd0d09bbafae0888506c6858ea691`, tagger Mahdi Hosseinpour) was created
on `fb969fb` (peeled commit `fb969fb767f60e1894e7071ba808c5dd0ac87901`) and
pushed to both remotes. Release run `45492100825` failed in its first job, "Check
the release gates and resolve the version" (job `138792821952`), about six
seconds after it started. The package, RPM, assemble and publish jobs were all
skipped, no release or draft was created, and the public releases API then listed
`v1.0.0` (id `456148592`) as the newest release.

**Attempt 2.** After the first gate change (`4f282a4`), commit `5ebbf57` was
pushed to record the next CI retry after the flaky `Test` failures described
below. Tag `v1.0.1` was recreated (tag object
`3b9a86204fc2616897a8f40bdc96a36e35aee702`) on `5ebbf57` and pushed. Release run
`45502745805` failed in the same plan job (`138829983146`) with the authenticated
log line `release gate FAILED: tag v1.0.1 is not annotated` and `SHA:
5ebbf57...` (the commit). No release or draft was created; the public releases
then ended at `v1.0.0`.

**Cause (both attempts).** `actions/checkout@v4` with `fetch-depth: 0` first
fetches all branches and tags, then checks whether the tag ref resolves to
`github.sha`. `git rev-parse refs/tags/v1.0.1` returns the tag object for an
annotated tag, which differs from the commit, so checkout fetches again with the
targeted refspec `+<commit>:refs/tags/v1.0.1`. Git stores that as a lightweight
ref pointing at the commit; the `[tag update] ... -> v1.0.1` line in the log is
that forced update. `git cat-file -t refs/tags/v1.0.1` then returns `commit`, so
the annotated gate failed even though the pushed tag was annotated. The checkout
source states the behaviour: `getRefSpec` for a `refs/tags/*` ref with a commit
returns `+${commit}:${ref}` (actions/checkout v4.2.2, `src/ref-helper.ts`). The
`release-dryrun` branch path never runs the tag gate, which is why no dry-run
caught it.

The earlier diagnosis was wrong. This document previously said `github.sha` is
the tag object and that the line-78 `^{commit}`-versus-`$SHA` comparison failed;
the public check-run annotation named the step, not the failing comparison, and
it was read as the comparison. Attempt 2's authenticated log shows the real
error, and attempt 1's real failure was the same annotated check. The change in
`4f282a4` (peel `$SHA` once, use the commit everywhere) is harmless and remains
as a defensive guard, but it did not address the cause: `$SHA` is already the
commit on a tag push. The earlier local harness that "reproduced" the failure by
setting `$SHA` to the tag object tested a shape GitHub does not produce.

The failed first two attempts left the tag in place as evidence; its message
claimed a publication that did not happen, so the tag was later deleted and
recreated before the successful attempt (that fix did not create, delete or move
any tag). No release was edited or deleted, and nothing was built or uploaded
outside CI.

### Gate fix (2026-11-25)

In the tag branch of the plan gate, `.github/workflows/release.yml` now restores
the real tag object before any tag check:

```
git fetch --force --quiet origin "+refs/tags/$tag:refs/tags/$tag" ||
  fail "could not fetch tag $tag from origin"
```

It then requires `git cat-file -t refs/tags/$tag` to be `tag` — the failure names
the actual type — reads the message from that exact ref with `git for-each-ref
--format='%(contents)'`, peels the object to its commit, and compares that commit
with the defensively peeled `$SHA`. The peeled commit is still used for the
on-main check, the CI `head_sha` query, the `commit` output and `COMMIT`. The
`$SHA` peel, the `v*` pattern, the annotated and non-empty checks, the
tag↔commit check, the on-main and CI-green requirements, the dry-run and
`workflow_dispatch` behaviour, and every permission are unchanged; a failed
fetch, a missing ref or a non-tag ref now has its own fail-closed message. No
build or publish step changed, and the workflow was not restructured.

Local verification (2026-11-25, disposable `/tmp/opencode/rl-gate2`): the plan
`run:` block is extracted from the workflow with PyYAML and run against a bare
remote plus a clone in which `refs/tags/v1.0.1` is lightweight, the checkout
shape; `curl` is stubbed and its URL recorded. 14/14 cases behaved as required:

- annotated remote tag with the lightweight local ref: passed, restored the local
  ref to a tag object, produced `version=v1.0.1`, `publish=true`, `tag=v1.0.1`
  and the peeled commit, and queried CI with `head_sha=<peeled commit>`;
- the same case under the pre-fix gate failed with `release gate FAILED: tag
  v1.0.1 is not annotated`, reproducing the reported message; the same setup
  with an annotated local ref passed the old gate, which isolates the ref shape
  as the cause;
- a direct checkout simulation (all-history fetch, then `git fetch --no-tags
  origin +<commit>:refs/tags/v1.0.1`, then the gate's force-fetch) showed the ref
  type go `tag` → `commit` → `tag` with the correct peeled commit;
- rejected: lightweight remote tag (`is not annotated (refs/tags/v1.0.1 is a
  commit)`), empty message, whitespace-only message, tag moved to another commit
  (message names the tag object and both commits), tag commit off main, and no
  successful CI push run;
- a missing remote tag reported `could not fetch tag v1.0.1 from origin` and did
  not pass as annotated; a missing local tag ref was recreated by the explicit
  fetch and passed; `$SHA` set to a tag object still peeled to the commit;
- a non-`v*` tag was rejected; the dry-run branch produced
  `v0.0.0-dryrun`/`publish=false` with no CI query; `workflow_dispatch` on a
  branch with `publish=true` was rejected; on the tag with `publish=false`, the
  tag gates ran and publish stayed `false`.

`bash -n` on the extracted block and `yaml.safe_load` on the workflow passed.
This is local evidence only: no tag push has run the new gate at the time of that
test, and `v1.0.1` remained unpublished.

### CI Test retries and next step

The first CI run on the earlier gate change (`45494443321`, commit `4f282a4`)
passed five of six jobs but failed the `Test` step of "Build, vet and test"; the
step log needs repository authentication and is not available here. The diff from
the last green commit (`fb969fb`, CI `45490941645`) contains no Go files — only
the workflow and documents — so the failure is not attributable to that change.
The full suite passed locally on 2026-11-25 with the pinned Go 1.27.4
(`CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...`: all 22 packages, including
`internal/acceptance` in 118s). Follow-up run `45496529871` (commit `0131688`)
failed the same `Test` step while the other five jobs stayed green, and a further
retry was recorded at `5ebbf57`; attempt 2's failure happened earlier, in the
annotated gate, so the CI-green gate was not evaluated. That gate still requires a
green push run on the eventual tag commit.

State after the gate fix (historical): tag `v1.0.1` (object `cce34dcb`) pointed
at `4121481` and had to be recreated on the fixed, CI-green commit after the
workflow fix was reviewed and merged; no public release existed and the public
releases ended at `v1.0.0`. Attempt 3 did create a draft, which the old cleanup
could not see and delete; see "Third v1.0.1 attempt" for its id and resolution.
The final tag was later recreated as object `4462cc86` on `d14da10` and released;
see "v1.0.1 — first CI-built release (2026-11-25)". `workflow_dispatch` has not
been invoked (it still needs an authenticated dispatch that this session did not
perform; the branch-triggered dry run is the path actually exercised). Residual
risk: the gates trust the remote tag at run time, so someone with write access who
moves the tag to another on-main, CI-green commit between the tag push and the
fetch would release that re-pointed tag; the tag↔commit check still binds the
release to the commit the tag names. A dry run exercises neither the tag gates nor
the publish job.

### Third v1.0.1 attempt (2026-11-25): draft created, then two blind spots

Commit `4121481` (the gate fix) was pushed and its CI push run was green — the
plan gate requires that — so tag `v1.0.1` was recreated (tag object
`cce34dcb02cf354f2574d42a02c10bb2d9020b85`) on `4121481` and pushed. Release run
`45510002182` passed the plan, package, RPM and assemble jobs; its publish job
`138857993885` created the draft, uploaded all 17 assets, then failed in the step
"Verify name, size and digest of every uploaded asset". Nothing was published.

**Cause 1 — drafts have no tag endpoint.** `scripts/release-verify-upload.sh`
polled `GET /repos/{repo}/releases/tags/v1.0.1`. GitHub documents that this
endpoint returns a release by tag name; for a **draft** it answers 404 (the draft
is not addressable by tag). The verification therefore failed on its first
request, far below its 12×5 s poll budget, even though the release and all 17
assets existed. The cleanup step (`delete-draft`) then made the same lookup, saw
404 and reported "nothing to delete", so the draft was left behind. This
corrects the earlier statement that no draft existed: attempt 3 left one, but
unauthenticated checks cannot see it. The project owner identified the leftover
draft as id `457460945` and deleted it with explicit authorization on
2026-11-25, outside this change (which neither inspects nor deletes it). An
authenticated listing then showed no `v1.0.1` release or draft before the tag
was recreated.

**Cause 2 — notes came from the commit.** The publish job read the release notes
from its own checkout with `git tag -l --format='%(contents)' "$TAG"`. As in the
plan gate (see above), `actions/checkout` stores a tag-push ref as a lightweight
ref pointing at the commit, so `%(contents)` returned the **commit** message. The
draft's body was therefore the commit message, not the annotated tag message
(observed on the leftover draft). The plan gate had validated the annotated
message, but the publish job re-read the wrong ref.

**Fix.** The create, verification and cleanup steps now address the draft by its
numeric release id; the upload and publish steps still take the tag
(`gh release upload`/`gh release edit`) and rely on gh's own resolution of the
draft by tag name:

- the create step lists releases with `gh api --paginate --slurp` (the
  authenticated listing includes drafts), retries a transient listing failure a
  bounded five times, requires exactly one draft with this `tag_name`, emits its
  `id`, and only then emits `created=true`; failure to resolve the id fails
  closed, and a draft left in that narrow window needs a manual by-id cleanup;
- `release-verify-upload.sh` takes `RELEASE_ID`, polls
  `GET /releases/{id}`, and requires `.tag_name == $TAG`, `.draft == true`, the
  exact 17-asset set, a size and a `sha256:` digest for every asset; 404/5xx and
  transport failures are retried inside the bounded budget and anything else
  fails closed. No tag-endpoint lookup remains;
- `release-guard.sh check` reads every page of `GET /releases` (drafts included)
  and refuses when any release carries the tag, naming its id and draft/public
  state; a listing error, non-array page or unreadable JSON fails closed. A
  leftover draft from an earlier run is now refused instead of missed;
- `release-guard.sh delete-draft` takes `RELEASE_ID`, re-reads
  `GET /releases/{id}`, requires `.draft == true` and `.tag_name == $TAG`, and
  only then issues `DELETE /releases/{id}` (204 expected). A public release,
  another tag, a 404 or any API error leaves it untouched; deletion by tag name
  is gone;
- the publish job's notes step force-fetches
  `+refs/tags/$TAG:refs/tags/$TAG`, requires `git cat-file -t` to be `tag`, reads
  `%(contents)` from that object, keeps the non-empty check, and logs the tag
  object the notes came from. The release is created from the same tag name and
  the plan job validated the same annotated message; a tag moved between plan and
  publish remains the pre-existing residual risk noted above.

No trigger, permission, plan gate or publish gate changed.

**Tests (2026-11-25, disposable `/tmp/opencode/rl-draft-id`).** A stubbed
`curl`/`gh` mimics the API: the tag endpoint 404s for a draft, the listing returns
it (including a second page after 100 releases), and deletes are allowed only by
id. 31/31 checks passed:

- verification by id passes with 17 matching assets and fails on a missing asset,
  an extra asset, a digest mismatch, a null digest after polling, a wrong tag, a
  public release, persistent 5xx, and retries a one-off 404 before passing;
- `check` refuses a draft found via the listing, refuses a public release, passes
  when the tag has no release, finds a draft on page 2, and fails closed on a 500;
- `delete-draft` requires an id and deletes only that draft; a public release, an
  unknown id, a 500 and a different tag are left untouched;
- the old scripts reproduce the incident: with a draft plus a 404 tag endpoint,
  old verification fails with "cannot read the draft release", old `check` wrongly
  says "no release exists", and old cleanup finds "nothing to delete";
- the create step emits `id=4242` and `created=true`, retries a transient
  listing failure, and fails closed without them when the listing cannot confirm
  the draft;
- against a local bare remote and a clone whose tag ref is lightweight (the
  checkout shape), the old notes step writes the commit message, the new one
  writes the annotated tag message from the tag object, an empty tag message and a
  lightweight remote tag are rejected.

`sh -n` on both scripts, `bash -n` on every extracted step, and
`yaml.safe_load` on the workflow passed. These are local, stubbed checks: the
real API's transient digest behaviour, a live listing page and a live draft
delete remain untested.

**Next step (historical).** The leftover draft was confirmed gone through an
authenticated listing, this fix was reviewed and merged, green CI ran on the new
tip, the annotated tag was recreated on that commit, and the release ran. The
result is recorded in "v1.0.1 — first CI-built release (2026-11-25)".

## v0.1.3 release (historical)

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
error. This work does not download, install or publish anything. (Historical:
the `Release` workflow now performs these download-and-verify steps itself for
a tag push; the local procedure above still applies to a manual build.)

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
