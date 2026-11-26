# Contributing to RouteLatch

Thanks for considering a contribution. RouteLatch is a Linux v1 and its scope is
deliberate: it is a loopback proxy client with an approved-destination list, not
a general VPN or a multi-platform product. An optional **TUN coverage layer** is
defined for after v1 (decision D29 / plan R16): it is a coverage path over the
existing modes, uses a separate privileged helper, and does not replace the
approved-destination policy. It is not implemented or shipped in v1.1.0.
Changes that widen the scope beyond that decision need a new design decision
first.

Read [AGENTS.md](AGENTS.md) for the repository rules and
[docs/implementation-plan.md#open-work](docs/implementation-plan.md#open-work) for
current work and authorization boundaries.

## Build and test

Use the Go version pinned in `go.mod` (1.27.4) with `GOTOOLCHAIN=local`. The
build links the official Xray core, so the module cache is large.

```sh
export GOTOOLCHAIN=local
go build ./...
go vet ./...
go test -count=1 ./...
```

The unit and contract tests are the fast loop. The acceptance suite drives the
built binary as a real process and takes about two minutes:

```sh
go test -count=1 -v ./internal/acceptance/
```

It prints one PASS/FAIL/NOTE line per assertion; NOTE means a gated or
environment-limited check, not a pass. The default suite is offline and
loopback-only. Opt-in checks that touch real system state are:

- `ROUTELATCH_DESKTOP_TEST=1` changes the real KDE/GNOME desktop proxy.
- `ROUTELATCH_SYSTEMD_SERVICE_STATUS=1` reads the real user manager's
  `service status` without writing, starting, stopping or reloading anything.
- TUN coverage tests (R16) are gated and may create a TUN interface, routes and a
  temporary system unit; run them only in a disposable VM/container or an
  explicitly authorized host. They must never be part of the default offline
  suite and must not change the real host's DNS/firewall/network.

The old gated "install a temporary systemd unit and run it" cells were retired
with the packaged unit. Service/autostart behavior is covered by fake-manager
unit tests (`go test ./cmd/routelatch ./internal/service`) that never touch a
real systemd manager. Run the gated desktop/status checks only on a machine
where that scope is authorized, and read the prerequisites in
[internal/acceptance/README.md](internal/acceptance/README.md) first. CI runs
the default, offline suite only.

`./scripts/build.sh` produces the static release binaries under `dist/`,
`./scripts/package.sh` writes the tarballs, unsigned `.deb` files and one RPM
`.spec` per architecture, and `./scripts/check-package.sh` validates their
checksums, layout and metadata (including no systemd unit, autostart entry or
maintainer script, and RPM version/arch/license layout). Packages ship no unit;
`routelatch service install` writes the opt-in user unit at runtime.
`./scripts/check-artifact.sh` checks the amd64 artifact with
`run --headless`. Debian and RPM share one `Version` derived from `BUILD-INFO`
(`v0.1.0-r12.1` → `0.1.0.r12.1`). `package.sh` does not build binary RPMs: the
CI `rpm-packages` job builds and inspects them from the generated specs with
`rpmbuild`/`rpm`, and local binary RPM builds used an extracted rpm 6.0.1
toolchain or a disposable container without installing RPM tooling on the host.
See [packaging/README.md](packaging/README.md) for package layout and
[deploy/README.md](deploy/README.md) for app/tray, autostart, the opt-in service
and migration from the old unit.

## Test norms

- Keep the default suite **offline and loopback-only**. Do not add a test that
  dials a public destination, uses a real subscription or credential, installs a
  service, or changes a desktop/network setting. Use local fixtures and
  documentation address ranges (`192.0.2.0/24`, `2001:db8::/32`).
- A test should catch a behaviour change, not assert source text or the
  existence of a mock. Derive expected results independently from the code under
  test.
- Preserve the existing teardown and the private temporary `HOME`/XDG
  directories. Do not clean up development data created for manual inspection.
- When a change affects routing, fail-closed behaviour, credentials, permissions
  or the management socket, add or extend an acceptance assertion. A unit test
  alone is not enough for those paths.
- If a check cannot run in the current environment, record it as a NOTE or in
  the acceptance README's "Cannot be proven" section rather than claiming it
  passed.

## Code and documentation

- Keep changes inside the request. Do not add unrelated cleanup, speculative
  compatibility or a new test framework.
- Reuse public libraries and the pinned engine; do not reimplement transports.
  Imported nodes must not change rules, modes, DNS, environment, management or
  system settings.
- Never disable destination TLS verification. Quarantine configurations that do
  it, and keep a deliberate pin scoped and visible.
- Comments explain why, hidden constraints or invariants, not what the code does.
- Public README text is English. The design documents in `docs/` are Persian;
  update them when a requirement or design changes instead of leaving a stale
  claim. Keep them free of private connection links, tokens, account details and
  machine-specific paths.

## Review and commits

Consequential changes get an independent review before they are called done:
routing and fail-closed behaviour, security boundaries, the CLI/JSON contract,
persisted state, and anything that can delete or rewrite data. Expect review
comments to be checked against the code and requirements rather than applied
blindly.

- Run the checks you report, and report failures or unrun checks honestly.
- Keep commits coherent and scoped to one change, with documentation included.
- Do not add remotes, push, publish packages or create releases without the
  owner's authorization.
- Do not include real share links, subscription URLs, credentials or private
  machine paths in commits, fixtures, logs or reports.

## License

By contributing you agree that your contribution is licensed under the
project's [Apache License 2.0](LICENSE), and that you have the right to submit
it. Third-party code must be compatible with that license and recorded in
`THIRD-PARTY-NOTICES.md`; see [docs/licensing.md](docs/licensing.md).
