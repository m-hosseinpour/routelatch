# Repository guidance

## Start here

Read [the open-work and continuation record](docs/implementation-plan.md#open-work)
before selecting work. It owns task status, blockers, checks, and the next step. Do not
copy task lists, progress, versions, or temporary blockers into this file.

On resumption, compare the record with the current request and `git status`; verify
only the code, artifacts, or environment that could have changed. A request to start
the open work authorizes the applicable unblocked task within its documented scope,
not every later task or system/network action. A question about status is not a request
to implement it.

## Documentation authority

- [Requirements](docs/requirements.md) own user decisions and acceptance criteria.
- [Architecture](docs/architecture.md) owns the design and technical boundaries.
- [Research](docs/research.md) owns evidence, source versions, and investigation limits.
- [Implementation plan](docs/implementation-plan.md) owns sequencing and progress.
- [Documentation index](docs/README.md) explains how to navigate these records.

Keep one current progress record: update the plan when work starts, becomes blocked,
or finishes. Record checks actually run, their results, relevant revision/environment,
and the next step. Distinguish implemented, source-inspected, and runtime-tested.
Update all affected live documents when a requirement or design changes; preserve
research snapshots as historical evidence rather than silently making them current.

Public-facing README text is English; design documents may remain Persian. Keep
documentation free of private connection links, tokens, account details, and personal
machine paths.

## Product invariants

- RouteLatch is independent of previous agent skills and web-search projects. They
  are research references only; optional imports must not modify their source data.
- Reuse public libraries and the official proxy engine. Do not reimplement transports
  or bypass a failed feasibility gate by silently weakening an agreed guarantee.
- Routing policy belongs to the application, not to subscription providers. Imported
  nodes must not alter rules, modes, DNS, environment, management, or system settings.
- In targeted mode, approved destinations use the pool and other destinations are
  direct. Pool failure must never silently route approved traffic directly.
- All-proxy keeps the documented local-network exception; all-direct is an explicit
  user choice and keeps the listener available. Stopping is a different operation.
- Mode changes must close conflicting live and pending connections. Account for idle
  HTTP pools and hijacked CONNECT channels, not just the current routing table.
- Diagnostics produce proposals, not active rules. Adding or removing an approved
  route requires user confirmation. Never replay a user's request to diagnose it.
- No HTTPS interception, root-CA installation, or disabling consumer security features.
  Do not claim protection for traffic or DNS lookups outside the application's control.
- Keep traffic listeners loopback-only under the agreed policy. Unauthenticated local
  traffic does not imply trusted callers; management must have separate protection.
- Quarantine configurations that disable TLS verification. A deliberate exception
  must be scoped, visible, and revocable; it is not permission to weaken destination TLS.

## Engineering and security

- Get external schemas and engine behavior from authoritative docs, current source,
  or a controlled execution. Pin dependencies; record applicability of evidence.
- A successful API return or route-selection event is not proof that the destination
  connected. Preserve the handshake and teardown verification gates in the plan.
- Preserve destination hostnames until the intended resolver handles them; a custom
  SOCKS dial hook alone does not prove that local resolution has been avoided.
- Treat subscriptions, node names, and opaque transport parameters as untrusted input.
  Validate before engine construction; configuration builders are not sandboxes.
- Use one owner for mutable state. Protect credentials and management sockets, use
  safe atomic updates, redact logs, and prevent terminal control-sequence injection.
- Do not assume multiple embedded engine instances are isolated; check process-global
  state before introducing concurrent probe instances or native Go runtimes.
- Keep direct egress independent of pool health and avoid unnecessary global engine
  restarts during ranking or routine pool changes.

## Verification, permissions, and delivery

Use the project's established checks once present. Keep automated tests controlled
and offline; exercise real user flows and failures in authorized local environments.
Do not install a new test workflow or repair unrelated failures without a reason.

Preserve unrelated work and manual-inspection data. Do not reset files, delete stored
data, kill unrelated processes, or modify existing proxy services. Installing/enabling
services, changing desktop/network settings, using private credentials, or testing
through real public exits requires authorization for that scope.

Review consequential behavior, security, public contracts, and persisted-data changes
independently before declaring completion; follow the applicable review guidance.
Report unverified behavior and unresolved findings rather than calling them passed.

Deliver a completed batch: once a group of related commits is verified — for example
the units of one orchestrated session — push to the configured remotes without waiting
for a separate request. This is a user decision dated 2026-11-21. Tag and release only
when the batch changes what users receive: program behaviour, a capability, packaging
or artifacts, security, or a user-visible contract. A batch that only adds documentation
or evidence is pushed and not released, and a release must carry user-facing value: if
its notes would have to say there is no code change, do not release. Version semantics
apply only to batches that are released: patch for fixes and internal corrections that
still change the program or artifacts; minor for a new user-facing capability or a
compatible contract extension; major for a breaking contract, data-format, or behaviour
change; a mixed batch takes the highest applicable level. Publication still requires the
existing gates: a green CI run on the tagged commit, reproducible artifacts with
checksums, updated notes and notices, no secrets or private connection data, and no new
remotes or change of publication destination. **Standing authorization (user decision
2026-11-26):** once those gates are met, tag and release a qualifying batch without
waiting for a separate per-release request; do not ask for release permission again.
This does not authorize a new remote, a publication-destination change, or a release
that fails the gates above.

Commit coherent, verified local changes, including documentation, with the applicable
harness attribution rules. Obtain a valid user committer email if unavailable; do not
guess one. Do not add remotes, change the publication destination, or select a release
license without authorization. Check the plan's publication gates before distributing
artifacts.
