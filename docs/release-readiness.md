# Release readiness: history, package, name and audit

Status: **no credential, private pool content or binary is in the repository's
history.** The current tree is clean of absolute personal paths. Publication still
needs the decisions listed under "Before a public push"; nothing here authorizes a
push, tag, release or upload.

This is the readiness record for the future public GitHub release (A14's remaining
gates). It is a dated snapshot: branch `r8-release` at `110cce3`, based on
`47ec19a`, with `main` at `c1e60ea` (four later docs/CI commits from a parallel
worker). Redaction rule used throughout: findings name the commit, path and kind,
never the matched value.

## 1. History scan

Method: every reachable object in every ref (`git rev-list --all --objects`, 43
commits over `main`, `r6-transports` and `r8-release`), every text blob read in
full (438 blobs), plus `git grep` over all revisions for scheme links, tokens,
keys, webhooks, home paths and private addresses. The scan separates "present in
the current tree" from "only in history".

| Class | Result |
| --- | --- |
| VLESS/Trojan/SS/VMess/Hysteria/TUIC/WireGuard links | 0 real links. Every match is a synthetic fixture in `internal/*_test.go`, `cmd/routelatch/cli_test.go` and `docs/contracts.md`; hosts are `example.com`, `example.net`, `127.0.0.1`, IDs are `1111…` and the test constant `b831381d-…`. No user pool node, domain or UUID appears. |
| Private pool / subscription content | 0. No subscription blob, pool file, copied state, `config.json` or `state.json` was ever committed. No long base64 blob decodes to a proxy payload. |
| Tokens and credentials | 0. No `ghp_`/`github_pat_`/`AKIA`/`sk-`/`xox*`, no bot token, no Discord/Slack/Telegram webhook, no `-----BEGIN … PRIVATE KEY-----`, no certificate. |
| Committed binaries or state | 0 binary blobs of any size; no state, log, database, backup or archive files in history. |
| Non-loopback IPs / private hosts | only documentation ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`), `8.8.8.8`, routing-test samples and `100.64.0.0/10`; no `10/8` or `192.168/16` service address, no `*.local`/`*.lan` host. |
| Personal machine paths | **finding, fixed in the current tree**: four generated files embedded `/home/<user>/workspace/routelatch/...` in an fd map. No credential, but a local path must not ship. |

**Personal-path finding.** Introduced by `1494765` (T1 spike) in
`spike/xray/results/teardown-fast.txt`, `teardown-fast-both.txt`,
`teardown-fast-CLIENT.txt` and `teardown-fast-EXIT.txt`; two lines per file. Fixed
at `110cce3` by replacing the absolute prefix with a repository-relative path
(`spike/xray/results/...`); measured values are unchanged. The same eight strings
**remain in history only** (blobs of `1494765`) and can be removed only by a
history rewrite in a copy before the first push — see the decision below.

`git grep -n "/home/"` on the current tree now returns only
`internal/platform/launcher_test.go` with the synthetic fixture `HOME=/home/example`.
No `/Users/`, `C:\Users` or `/root/` path exists in the tree.

**Identity exposure (not a secret).** Every commit has author `OpenCode
<noreply@opencode.ai>` and committer `Mahdi Hosseinpour
<mahdihosseinpour1996@gmail.com>`, and the trailer repeats that address. This is
deliberate attribution, but a public history publishes the address. Decision, not
a defect.

## 2. Release package

Built from `110cce3` with Go 1.27.4, `SOURCE_DATE_EPOCH=1794960000`
(2026-11-18), `CGO_ENABLED=0`, `-trimpath -buildvcs=false -ldflags "-s -w -X …"`:

- `dist/packages/routelatch-110cce3-linux-amd64.tar.gz`
  `a90029bd43451393b24b2d3f36916dcc72e042b52012c24d9197f8cd579d8e30`
- `dist/packages/routelatch-110cce3-linux-arm64.tar.gz`
  `45b30b034f927abcf57af6a9eb9c4a59fcd79028a1caa155c64f216d0726b718`

Each package carries `bin/routelatch`, `share/routelatch/systemd/routelatch.service`,
and under `share/doc/routelatch/`: `README.md`, `INSTALL.md`, `SERVICE.md`,
`LICENSE`, `NOTICE`, `THIRD-PARTY-NOTICES.md`, `docs/licensing.md`, `go.sum`,
`BUILD-INFO`, `PACKAGE-NOTES`. `dist/packages/SHA256SUMS` verifies with
`sha256sum -c`.

Evidence, not assumption:

- version is observable in the artifact: the packaged binary prints
  `RouteLatch 110cce3` / `commit: 110cce3` /
  `build date: 2026-11-18T00:00:00Z`, matching `BUILD-INFO` and `--json version`;
- `scripts/build.sh --reproducible` rebuilt both architectures twice and reported
  identical checksums;
- two packaging runs over one build produced identical tarball checksums;
- `scripts/check-artifact.sh dist/routelatch-linux-amd64` passed: static ELF, no
  `PT_INTERP`, stamp matches, empty-environment `run` answered `status` on loopback
  and exited 0 on SIGTERM;
- the new stamp guard fails closed: forcing `version=deadbeef` in `BUILD-INFO`
  made `scripts/package.sh` stop with
  `package stamp check FAILED: binary reports '47ec19a-dirty', BUILD-INFO says 'deadbeef'`
  (run before the sanitization commit, when the tree was dirty).

**Changes made for this.** `scripts/package.sh` now ships `SERVICE.md` (the packaged
copy of `deploy/README.md` with a header mapping source-tree paths to the package),
because `INSTALL.md` pointed readers at a file that was not in the package; and it
verifies the host-architecture binary's stamp against `BUILD-INFO` before writing
checksums. `packaging/README.md` and `docs/licensing.md` were updated to match.

**MPL-2.0 source offer.** The package ships the written offer in `NOTICE`: exact
versions of `xtls/xray-core`, `xtls/reality` and `layeh.com/gopher-luar`, the
`go mod download <module>@<version>` commands and the upstream repositories, with
the `go.sum` hashes in `docs/licensing.md`. No source tarball is bundled; that
remains an optional alternative, recorded in `docs/licensing.md`.

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
5. Nice-to-have, outside this change: pin GitHub Actions to commit SHAs, and add
   this file to `docs/README.md` and the plan's status record.
