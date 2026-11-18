# Release readiness: history, package, name and audit

Status: **no credential, private pool content or binary is in the repository's
history.** The current tree is clean of absolute personal paths. Publication still
needs the decisions listed under "Before a public push"; nothing here authorizes a
push, tag, release or upload.

This is the readiness record for the future public GitHub release (A14's remaining
gates). It is a dated snapshot: branch `r8-release` (binary/version `110cce3`), based
on `47ec19a`, with `main` at `c1e60ea` (four later docs/CI commits from a parallel
worker). Later commits on this branch fix the packaging script and this record;
they add no history content. Redaction rule used throughout: findings name the
commit, path and kind, never the matched value.

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

Built from the `110cce3` binary with the final packaging script, Go 1.27.4,
`SOURCE_DATE_EPOCH=1794960000`
(2026-11-18), `CGO_ENABLED=0`, `-trimpath -buildvcs=false -ldflags "-s -w -X …"`:

- `dist/packages/routelatch-110cce3-linux-amd64.tar.gz`
  `2aceb05fbe679a977cc67f7235685a98cc7f459390f53839ca66bd1852ad8315`
- `dist/packages/routelatch-110cce3-linux-arm64.tar.gz`
  `77b2aa8e5c0d84626d15eceb43fcd3257569c7949cd2baecb793c3ab35c09df6`

Each package carries `bin/routelatch` (and, when the R11 sidecar is supplied,
`bin/sing-box` plus `share/doc/routelatch/SINGBOX-INFO`),
`share/routelatch/systemd/routelatch.service`,
and under `share/doc/routelatch/`: `README.md`, `INSTALL.md`, `SERVICE.md`,
`LICENSE`, `NOTICE`, `THIRD-PARTY-NOTICES.md`, `docs/licensing.md`, `go.sum`,
`BUILD-INFO`, `PACKAGE-NOTES`. `dist/packages/SHA256SUMS` verifies with
`sha256sum -c`. The checksums above are the historical R8 snapshot without the
sidecar; rebuilding with the sidecar produces new tarball checksums and the same
verification flow.

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
last verified in R1/R2. Package and artifact checks are in §2.

## Before a public push

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
