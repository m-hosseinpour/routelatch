# VPN-off recheck of the TUN/DNS/UDP evidence

**Date:** 2026-11-27
**Repository/revision:** RouteLatch `234315b` (`main`), clean except this recheck's
untracked evidence.
**Scope:** read-only verification. No product, docs, plan, config, service or host
network/DNS/firewall/systemd/desktop change; no commit or staging. The only additions
are this report and `spike/tun/evidence/recheck-vpn-off-2026-11-27/**`.

**Verdict: no conclusion changes.** With the host's other VPN/TUN off, the H, R16-I and
R16-J results reproduce: the same pass/fail outcomes, the same fail-closed behavior and
the same measured orders of magnitude. Differences are ordinary timing variance, listed
below. Every result here is **runtime-observed** in disposable containers with local
synthetic fixtures; public exits, public resolvers, real pools and real credentials were
not used. Nothing below should be read as a real-internet or real-exit claim.

## 0. Host state and container isolation

Host (runtime-observed, `00-host-state.txt`):

| Item | Observed |
| --- | --- |
| Default route | `default via 192.168.1.1 dev wlp0s20f3 src 192.168.1.104 metric 600` |
| IPv6 default | none (`ip -6 route get 2606:4700:4700::1111` → `Network is unreachable`) |
| `tailscale0` | link present, **only** `fe80::…/64`; no `100.x` address and no `100.64/10` or default route; `tailscale status` → `Tailscale is stopped.` |
| `rl-tun0` | absent; zero `198.18/15`, `fc00::/64` or `rl-tun0` host routes |
| Host rules | only the standard `0/5210/5230/5250/5270/32766/32767` rules; table 52 empty |

A disposable two-container network on a dedicated **`--internal`** bridge
(`172.31.91.0/24`, `fd00:91::/64`; no NAT egress) was started (`01-isolation.txt`).
Inside the client container:

- links were only `lo` and `eth0`; no `tailscale0`, no `rl-tun0`, no tun/tap device;
- no `100.64/10`, no host `192.168.1.0/24`, and **no IPv4 or IPv6 default route**;
- local peer connectivity worked (HTTP 200 from `172.31.91.20`, TCP connect OK);
- public attempts were all unreachable: `1.1.1.1:443`, `93.184.216.34:80`,
  `8.8.8.8:53` → `OSError: [Errno 101] Network is unreachable`.

So no container inherited a host VPN/tailscale interface or route, and the isolated
bridge had no public exit. The earlier H/I runs used a NAT bridge, but their test
destinations were on-link synthetic peers and their container default routes pointed at
the peer fixture; any host-mediated egress would have followed the host's only default
(`wlp0s20f3`), because the host had no VPN default route. J used `--internal` or
`--network none` directly. The earlier conclusions therefore did not depend on a host VPN.

## 1. R16-H critical rerun (full 158-check scenario)

Full `spike/tun/h/run-h.sh` at `234315b` inside the disposable systemd container
(`h-tun/`): **SUMMARY passed=158 failed=0 notrun=0 total=158**, identical to the
`7ff128c` acceptance.

| Check | Prior (`7ff128c`) | This run (VPN off) |
| --- | --- | --- |
| Gated `TestTunIntegration` / `TestTunEngineIntegration` | PASS 1.901 s | PASS, 1.794 s (1.54 s + 0.23 s) |
| `tun setup` layout/socket/root refusal; `tun on` dual-stack, non-root `CAP_NET_ADMIN` | PASS | PASS |
| TLS/HTTP through TUN, SNI/Host sniff to SOCKS, approved pool vs unapproved direct | PASS | PASS |
| DNS/53 direct v4 UDP/AAAA/TCP, physical-interface source, system DNS unchanged | PASS | PASS |
| Non-53 UDP and QUIC/443 blocked, zero peer datagrams | PASS | PASS |
| Engine SIGKILL: link/routes stay, EOF after ~1.2 s, no direct fallback, restart adopts | PASS, EOF 1206 ms | PASS, EOF **1208 ms** |
| TUN service restart/lease rebuild, flow recovers | PASS | PASS |
| TUN service SIGKILL fail-open window | PASS, 0.605–1.390 s | PASS, **0.428–1.312 s**, link back 1.318 s |
| D13 mode-change sweep of a held flow | PASS, 1.632 s | PASS, **1.679 s** |
| `tun off`/Quit cleanup; no orphan | PASS | PASS |
| Second engine `tun_conflict`; start-limit bound/reset; socket/link witness | PASS | PASS |
| D21 canary in files/journal/status | PASS, zero hits | PASS, zero hits |

The fail-open window moved to a lower minimum (0.428 s vs 0.605 s) but stayed in the
same sub-1.4 s bound with the link returning and the flow recovering; this is not a
behavior change. The H addendum (IPv6 DNS, package checks, fail-open status sampling) was
**not rerun**; its conclusions are unchanged and its previous limits still apply.

## 2. R16-I DNS-through-pool recheck

Full `spike/tun/r16i/run.sh` (`r16i/`). Results match the recorded evidence:

| Check | Prior | This run (VPN off) |
| --- | --- | --- |
| DoH A / AAAA through the SOCKS exit, v4 resolver | 26–79 ms first, 1–2 ms cached | PASS, A 60 ms, AAAA 1 ms |
| DoH to an IPv6 resolver destination / IPv6 upstream | PASS | PASS, 0 ms / 62 ms |
| Exit sees a TLS ClientHello, not the query name; `direct_hits` | 0 | 0 |
| DNS-over-TCP through the exit | PASS, plaintext name visible to exit | PASS, plaintext name visible; `direct_hits=0` |
| `tls://` DoT negative | timeout, no 853 connection | timeout 3002 ms, no 853 listener |
| Dead pool: query fails, no direct query for the name, recovery | ~6.0 s timeout, 0 direct hits, 58 ms recovery | 6006 ms timeout, 0 direct hits, 20 ms recovery |
| `/etc/resolv.conf` hash unchanged; D21 canary matches | `c33c13…f77c`, 0 | same hash, 0 |

The answer addresses are the same synthetic values (`203.0.113.201`,
`2001:db8::201`) and the direct resolver again received zero DoH/DoT names. No
conclusion changes: DoH-over-TCP is feasible and fails closed; DoT is not a pinned-Xray
transport; `tcp://` exposes plaintext names to the exit.

## 3. R16-J UDP recheck

Fixtures copied unchanged from `evidence/r16j-udp-2026-11-27/fixtures/` and run with the
pinned binaries (`02-r16j-latency.txt`, `03-r16j-tun-udp.txt`,
`04-r16j-exit-matrix.txt`).

**TUN UDP sniff latency** first-datagram RTT, three new flows each:

| Config | Prior | This run |
| --- | --- | --- |
| Sniffing on (`http,tls`, shipped) | 205.0 / 203.9 / 202.9 ms | 205.5 / 203.8 / 205.0 ms |
| Sniffing off | 3.1 / 1.7 / 1.5 ms | 1.4 / 1.6 / 1.6 ms |

The ~200 ms per-new-non-QUIC-UDP-flow cost is reproduced; DNS/53 shares the same
pre-routing path (source-derived, DNS latency itself still not measured here).

**TUN UDP path:** two live datagrams echoed through the SOCKS relay; killing the relay
made the next datagram time out and the peer's datagram count stayed at 2 (fail-closed,
no direct leak); stopping Xray removed `rl-r16j0` and all halves with no residual route.

**Exit-matrix spot check** (`--network none`, loopback only): SOCKS5, Xray SOCKS
outbound, VLESS, Trojan and hysteria2 all carried UDP; 1200/1400-byte payloads
succeeded and 8192/60000-byte payloads timed out for both VLESS and hysteria2. Netem
(20 ms, 1 % loss, 50 fresh associations) p50 was VLESS 163.2 ms, hysteria2 122.7 ms,
SOCKS5 81.4 ms; the prior `01-exit-matrix.txt` recorded 162.8 / 122.5 / 81.4 ms for the
same 50-sample test, so this reproduces. (The research table's 121.5 ms VLESS figure is
the separate 200-pair `02-bench.txt` test, not this one.)

No conclusion changes: non-DNS UDP remains blockable and fail-closed, the sniff delay is
real, and the documented per-transport UDP support/limits stand.

## 4. What was not run / residual limits

- The H addendum (IPv6 DNS, package checks, in-gap fail-open status) was not rerun.
- H-TRAY and the real desktop/TUN icon were not run (no desktop session).
- Real public exits, public resolvers, real pool/subscription nodes, and real
  VLESS/Trojan/hysteria2 exits were not used. R16-J's real-pool UDP failure rate remains
  **not verified**.
- Only synthetic local fixtures and test-only certificates were used; no public
  destination was contacted and no credential was read or used.
- System DNS, host routes, firewall, systemd and desktop settings were not changed.

## 5. Evidence and cleanup

Directory `spike/tun/evidence/recheck-vpn-off-2026-11-27/`:

- `00-host-state.txt` — host links/addresses/routes/rules/tailscale/rl-tun0.
- `01-isolation.txt` — isolated-bridge container view, peer connectivity, no public exit.
- `02-r16j-latency.txt` — sniffing-on vs sniffing-off first-datagram RTT.
- `03-r16j-tun-udp.txt` — live UDP, dead-relay fail-closed, teardown.
- `04-r16j-exit-matrix.txt` — five transports, payload sizes, netem spot check.
- `05-final-host-check.txt` — post-run host and Docker state.
- `h-tun/` — full 158-check H scenario, gated tests, host cleanup.
- `r16i/` — full R16-I DNS harness and final cleanup.

All `rl-h-*`, `rl-r16i-*`, `r16j-*` and `rl-recheck-*` containers and networks were
removed. The host has no `rl-tun0`, no `198.18/15` or `fc00::/64` route, and its only
default is via `wlp0s20f3`; Tailscale remains stopped. The repository has only this
untracked evidence directory and this report.
