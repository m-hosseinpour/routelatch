# RouteLatch

A planned, self-contained targeted proxy client: send approved destinations through a
VLESS/Trojan pool and keep everything else direct. A failed pool must not silently send
approved traffic directly.

**Status: design and implementation plan only. No application, executable, installer,
or network service has been implemented or tested.**

## Agreed direction

- Independent of any agent skill or web-search service.
- Go application, reusing the official Xray core and suitable public libraries rather
  than reimplementing proxy transports.
- Linux first; Windows next; Android and TUN are later milestones.
- First release: TCP through HTTP CONNECT/plain HTTP proxy and SOCKS5, a CLI, and a
  small interactive terminal menu. No heavyweight terminal dashboard.
- One destination list for exact domains, domain/subdomain rules, IPs, and CIDRs.
- User-supplied pool links, files, subscription URLs, and optional v2rayN import.
- Automatic diagnostics produce proposals; the user approves routing changes.
- Four user-visible states: targeted, all-proxy, all-direct, and stopped.
- Self-contained release packages: users do not install Go, Python, Node, or Xray
  separately.

## Important boundaries

The first release controls only traffic delivered to its proxy listeners, not every
connection on the device. Unknown public IP destinations remain direct unless approved.
System DNS is not changed in the first release. A program's own DNS lookups cannot be
undone by a proxy. There is no HTTPS interception or root-CA installation.

The local traffic listener will be loopback-only and unauthenticated by user choice.
Other local processes can use it. Management must use a separate protected interface.
Public exits are untrusted; RouteLatch cannot make them safe for sensitive traffic.

## Documentation

The design documents are in Persian, following the design discussion. They distinguish
user decisions, source evidence, proposals, and runtime checks still required.

- [Requirements and decision record](docs/requirements.md)
- [Architecture and security design](docs/architecture.md)
- [Research and evidence](docs/research.md)
- [Implementation plan and acceptance gates](docs/implementation-plan.md)
- [Open work and continuation](docs/implementation-plan.md#open-work)
- [Contributor and agent guidance](AGENTS.md)
- [Documentation index](docs/README.md)

## License and publication

The project's license has **not** been selected. No public release is authorized by
this design task. Before publishing source or binaries, select a license and review
the actual pinned dependency/build set, including Xray's MPL-2.0 obligations, notices,
and availability of corresponding covered source. A dependency's top-level license
alone is not a complete distribution-license audit.

RouteLatch is not an official Xray client and does not imply affiliation with XTLS.
