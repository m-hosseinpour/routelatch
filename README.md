# RouteLatch

RouteLatch is a local proxy client with an explicit approved-destination list.
You give it a pool of proxy exits (VLESS, Trojan or SOCKS5 links) and a list of
destinations you approve. In its targeted mode, approved destinations go
through the pool and everything else goes direct. If the pool cannot serve an
approved destination, RouteLatch returns an error instead of silently sending
that traffic direct.

It is a Go program that links the official Xray core into the binary. Users do
not install Go or Xray separately.

**Status:** the Linux v1 is implemented, unit-tested, and covered by an
end-to-end acceptance suite ([`internal/acceptance/`](internal/acceptance/README.md))
that drives the built binary against loopback-only fixture exits and peers. A
run in this environment prints 206 assertion lines and no failures in about two
minutes (125 s) on this machine, over
the documented CLI surface, the four routing modes, pool/import/health,
diagnostics, the launcher and crash/shutdown recovery. The systemd user service
was installed and driven under a real `systemd --user` manager
(start/stop/restart, port collision, SIGKILL recovery), and a gated acceptance
test repeats that supervision on an isolated data dir. What it does **not**
prove: a clean-machine install with no manual runtime (A01), real GNOME/KDE
desktop-proxy changes, login start-up across a reboot/re-login, and public egress
through real exits.

RouteLatch is licensed under the Apache License 2.0; see
[`LICENSE`](LICENSE), [`NOTICE`](NOTICE) and
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md). Publication and distribution
are still not authorized: no release, tag, remote push or package publication is
part of this state, and the release source offer has not been assembled yet.
Treat this as a build that passes its local checks, not a supported release.

## What the Linux v1 does

- Listens on two loopback-only proxies, HTTP (`127.0.0.1:8317`) and SOCKS5
  (`127.0.0.1:8318`). Both addresses are configurable but must stay on
  `127.0.0.1` or `::1`.
- Keeps one approved-destination list with exact domains, domain/subdomain
  rules, IPs and CIDRs.
- Has four states: `targeted` (approved destinations use the pool), `all-proxy`
  (all public destinations use the pool), `all-direct`, and `stopped`.
- Chooses and ranks pool exits, cools down failing ones, and reports whether an
  exit confirmed success or only accepted the request, rather than claiming a
  destination was reached.
- Imports share links, link files, subscription URLs and v2rayN exports, and
  quarantines records it cannot use, with a reason.
- Diagnoses eligible direct failures and writes **proposals**; adding or
  removing a route needs your explicit confirmation and a failed request is
  never replayed.
- Optionally points the GNOME or KDE desktop proxy at the listeners, with an
  explicit command and an ownership-aware restore.
- Offers a CLI, a small interactive menu (`routelatch menu`), JSON output on
  every command (`--json`), and documented exit codes.

Changing state closes connections that no longer match the new routing policy,
including in-flight dials. A mode change is reported successful only after that
sweep completes.

## What the Linux v1 does not cover

- **No TUN, no device-wide capture.** Only traffic that reaches RouteLatch's
  listeners is handled. Programs that ignore `HTTP_PROXY`/`HTTPS_PROXY`/
  `ALL_PROXY`, or that were started outside `routelatch exec`, are not routed by
  RouteLatch.
- **No UDP or QUIC.** SOCKS5 `UDP ASSOCIATE` and `BIND` are refused, and
  RouteLatch does not proxy datagrams.
- **No system DNS change.** Your normal resolver stays in place.
- **No HTTPS interception**, no root CA installation, and no weakening of a
  consumer's security features.
- **No LAN listener and no authentication.** Both listeners are loopback-only
  and have no password. Sharing the proxy across a network is not supported.
- **No Windows, Android or GUI** in v1, and no system-wide "covers everything"
  mode. `all-proxy` still has a local exception: loopback and private/LAN
  destinations stay direct so that local services keep working.
- `stopped` closes the listeners and leaves only the management socket;
  leaving `stopped` requires restarting the process.

## Build from source

Requirements: Linux with Go 1.27.4 (the version pinned in `go.mod`) and a
populated module cache or network access for the pinned modules. Set
`GOTOOLCHAIN=local` so Go does not try to download a different toolchain.

```sh
export GOTOOLCHAIN=local
./scripts/build.sh
```

The script builds static (`CGO_ENABLED=0`), trimmed binaries for `linux/amd64`
and `linux/arm64` into `dist/` (gitignored), with `-buildvcs=false`. It does not
fetch anything itself; `go build` uses the module cache and would download a
missing pinned module. The outputs are:

```
dist/routelatch-linux-amd64
dist/routelatch-linux-arm64
dist/SHA256SUMS
dist/BUILD-INFO
```

`VERSION`, `COMMIT` and `SOURCE_DATE_EPOCH` can be set for a named build; the
same inputs produce identical bytes (`./scripts/build.sh --reproducible`). The
build fails if the stamped values do not show up in `routelatch version`, so a
silently unstamped binary cannot be packaged.

`./scripts/check-artifact.sh` confirms the amd64 artifact is statically linked
and starts with an empty environment and a temporary XDG root. The arm64
artifact cannot run on an amd64 host, so it is built and checksummed here but
not executed.

## Install

Installation is a user action; the build does not install, enable or configure
anything. The binary is statically linked, so it needs no libc or separate Xray
install; TLS verification still reads the host's CA certificate bundle.

```sh
install -Dm755 dist/routelatch-linux-amd64 "$HOME/.local/bin/routelatch"
```

`~/.local/bin` must be on `PATH`. To run it as a systemd user service, install
the unit from `deploy/systemd/routelatch.service` as described in
[deploy/README.md](deploy/README.md); the unit does not require root, TUN or a
firewall change. [packaging/README.md](packaging/README.md) documents the
package layout, the unit's place in it, and how to assemble local packages with
`scripts/package.sh`.

## Quick start

1. Start the service in the foreground (keep this terminal open; Ctrl-C stops
   it cleanly):

   ```sh
   routelatch run
   ```

   It prints the two listener addresses. In another terminal, use the CLI; most
   commands talk to the running service over a private Unix socket (`doctor`,
   `backup`, `restore` and `desktop proxy restore` also work while it is
   stopped). `routelatch status` without a running service exits 3 with a clear
   message.

2. Import a pool exit from a link or a file of links:

   ```sh
   routelatch import link 'vless://<uuid>@203.0.113.10:443?security=tls#name'
   routelatch import file ~/my-nodes.txt
   ```

   `import subscription <url>` fetches a subscription you name and
   `import v2rayn <file>` reads a v2rayN export (a link list or its JSON
   profiles). Records that fail validation stay in the pool as `invalid` or
   `unsupported` (for example skip-verify TLS is quarantined as
   `insecure_tls_removed`) and are never dialed.

3. Approve the destination you want to route through the pool:

   ```sh
   routelatch rules add api.example.com
   routelatch rules add 203.0.113.8
   routelatch rules add --kind domain_suffix example.com
   routelatch rules list
   ```

   A rule has no port; a domain rule covers all ports. An executable that sends
   an IP instead of a hostname is matched against the list by that IP, so a
   domain rule does not cover a client that resolves the name itself.

4. Check state and health:

   ```sh
   routelatch status    # mode, policy version, listeners, pool, connections
   routelatch doctor    # file permissions, listeners, pool, state schema
   ```

5. Point a program at the proxy. For a child process only:

   ```sh
   routelatch exec -- curl https://api.example.com/
   ```

   `routelatch env` prints the four proxy variables for manual wiring. You can
   also configure the program's HTTP proxy as `http://127.0.0.1:8317` and its
   SOCKS5 proxy as `127.0.0.1:8318` yourself.

Other useful commands: `version`, `mode targeted|all-proxy|all-direct|stopped`,
`pool list|add|remove|refresh|test|select|rerank`, `diagnose <host:port>`,
`proposals list|accept|reject|recheck`, `config get|set|path`,
`desktop proxy status|set|restore`, `backup`, `restore <ref>`, and `menu`.
`pool rerank` re-ranks the exits now and reports the automatic selection before
and after with the reason each other exit was skipped; it does not advance the
policy version or close live connections.
`routelatch version` prints the build's version, commit and build date, also as
`--json`; the same values plus the Go version are in `dist/BUILD-INFO`.
`routelatch help` prints the command list. Exit codes and the JSON envelope are
specified in [`docs/contracts.md`](docs/contracts.md) §7. Secrets, share links,
source URLs, request bodies and headers are not printed by these commands.

## Security model and limits

- **Public exits are untrusted.** RouteLatch validates and quarantines records,
  but it cannot make a public pool safe: an exit can observe and modify traffic
  that passes through it, and it sees destination names and plaintext HTTP.
  Do not send sensitive traffic through an exit you do not trust.
- **Proof is not delivery.** For a SOCKS5 exit, `confirmed` means the exit
  reported success. For VLESS/Trojan, `accepted` (unconfirmed) means the engine
  accepted the request; RouteLatch cannot observe the destination handshake
  through those protocols, so neither label means "the site is reachable".
- **Any local process can use the proxy.** The loopback listeners have no
  authentication by design. Another process or user on this machine can send
  traffic through your pool. This is a deliberate trade-off, not an access
  control.
- **Management is separate.** State changes go through a Unix stream socket
  with mode `0600` in a `0700` directory, protected by a same-uid `SO_PEERCRED`
  check. That protects against other users, not against root or another process
  running as you.
- **DNS.** System DNS is unchanged. Direct destinations are resolved by your
  machine's resolver; hostnames that go through the pool are sent to the exit
  and resolved there, which means the exit and its resolver see the name. The
  exit server's own hostname must be resolved locally to open the tunnel; that
  lookup is not a destination leak but it is still a local lookup. RouteLatch
  cannot fix a program that resolves a name itself, and ECH makes hostname
  inspection unreliable, so no name-based guarantee is possible in those cases.
- **No MITM.** TLS verification stays on. RouteLatch does not install a root CA
  or intercept HTTPS; an absolute-form `https://` request is refused as
  `unsupported` (HTTP 501) instead of being forwarded or decrypted. The
  supported path for an exit whose certificate is not in the trust store is a
  certificate pin: `pinnedPeerCertSha256` (or `verifyPeerCertByName`) is
  accepted with verification still on and narrows acceptance to that
  certificate or name. Links that only disable verification (`allowInsecure`)
  are quarantined as `unsupported` (`insecure_tls_removed`) and are never
  dialed; a link that carries both a pin and `allowInsecure` keeps the pin,
  stays usable, and reports that `allowInsecure` was ignored.
- **No silent bypass.** If the pool cannot serve an approved destination, the
  connection fails with an error. Pool failure never turns into a direct
  connection for approved traffic.
- **Diagnostics propose, never apply.** Only eligible direct failures create
  proposals, the failed request is not replayed, and no rule changes without
  your confirmation.
- Files holding config, state and logs are `0600` in `0700` directories. The
  service log is `$XDG_STATE_HOME/routelatch/log/routelatch.log`, rotated at
  4 MiB into `routelatch.log.1`–`.3`. `doctor` reports permission problems; a
  state directory others can read makes the service refuse to start rather than
  expose credentials.

## Privacy

RouteLatch keeps no access log. A destination that passes through the app is
never written to the log file, state, config, backups or a proposal history; the
approved-destination list is the only destination data stored, and backups copy
that list. Operational log lines record failure kinds and counts, not the
destination of a carried connection.

What is stored: the approved rules, pool records (server, port, protocol and
credentials), health summaries (status, latency average, counters, cooldown and
last success/failure times) and configuration, in private `0600` state/config
files. Proposals and their evidence live only in the memory of the running
service: `proposals list` works only while that service runs, and a restart
discards them. This is deliberate; there is no proposal history to recover.

Probe targets you configure are your own choice and are stored as configuration.
`pool test --target` sends one probe to the target you name for that invocation
and is not recorded; neither is a record of the traffic RouteLatch carried.

Limits: the loopback proxy and the management socket are reachable by any
process running as you, so a local process can use the pool or ask the service
for status. The exit you choose sees the destination it carries, and DNS for a
proxied hostname is resolved at the exit (the exit server's own name is resolved
here). Your OS, resolver, firewall or other software may log traffic regardless
of RouteLatch, and RouteLatch cannot make you anonymous.

## Platform support

| Platform | Status |
| --- | --- |
| Linux `amd64` (`x86_64`) | Built and run here |
| Linux `arm64` (`aarch64`) | Built here, not executed (no arm64 host available) |
| GNOME desktop proxy | Supported via `gsettings` (opt-in) |
| KDE Plasma desktop proxy | Supported via `kwriteconfig6`/`kwriteconfig5` (opt-in) |
| Other desktop environments | Unsupported; an explicit message, never a "whole-system proxy" claim |
| Windows, macOS, Android, 32-bit Linux | Not supported in v1 |

The desktop proxy is changed only by `routelatch desktop proxy set` and
restored by `routelatch desktop proxy restore`, which reverts only the keys
RouteLatch wrote. Starting or stopping the service does not touch desktop or
network settings.

## Documentation

The design documents are in Persian:

- [Requirements and decisions](docs/requirements.md)
- [Architecture and security design](docs/architecture.md)
- [Technical and security contracts](docs/contracts.md)
- [Research and evidence](docs/research.md)
- [Implementation plan and open work](docs/implementation-plan.md#open-work)
- [Acceptance suite notes](internal/acceptance/README.md)
- [Documentation index](docs/README.md)
- [Contributor guidance](AGENTS.md)

## License and publication

RouteLatch is licensed under the **Apache License, Version 2.0**. The full text is
in [`LICENSE`](LICENSE); project attribution is in [`NOTICE`](NOTICE). The binary
also includes third-party Go modules under MIT, BSD-2/3-Clause, Apache-2.0,
MPL-2.0 and LGPL-3.0 licenses. Their exact versions, copyright notices and license
texts are in [`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md). The audit method,
the MPL-2.0 source-availability obligation, the LGPL-3.0 static-linking exception
and the remaining evidence limits are documented in
[`docs/licensing.md`](docs/licensing.md).

Publication is not authorized. There is no release, tag, remote push, package
publication or source distribution yet; before any artifact leaves the build
machine, the source offer for the MPL-2.0 modules must be assembled and the
release checklist in `docs/implementation-plan.md` must pass.

RouteLatch is not an official Xray client and does not imply affiliation with
XTLS.
