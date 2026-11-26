# R16-A — separate TUN feasibility gate (pinned Xray + systemd on Linux)

> **Dated evidence snapshot (2026-11-27).** This is a disposable feasibility
> spike, not product code. This worker changed no file outside `spike/tun/**`,
> edited no product/docs code, installed no service on the host, and made no
> commit. Concurrent R16-B edits in `docs/` and `internal/` belong to that
> worker and are not part of this spike. All TUNs, routes, units, DNS servers and UDP sinks existed
> only inside the two containers described below.

**Verdict: Ready.** The D29 architecture (separate small TUN service, non-root
engine, pinned Xray for the TUN inbound, DNS direct, UDP blocked, fail-closed
engine death, lease-based restart) is feasible on this Linux/systemd environment.
The verdict carries two recorded constraints, not silent fallbacks:

1. **D21 log level is a hard invariant:** the tested service runs Xray with
   `logLevel: "none"`. At `info`/`debug`, stock Xray (source-inspected)'s TUN handler logs the flow
   destination (`proxy/tun/handler.go:246`). The service must not expose a
   log-level knob; if the acceptance owner reads “no destination at any level”
   as “even at debug”, stock Xray does not meet that literal reading and needs a
   core log patch/filter. This is recorded in §7 and the source evidence file.
2. **DNS binding uses `autoOutboundsInterface=auto`**, not explicit
   `sockopt.interface` on the DNS freedom outbound. The auto path is the tested,
   contract-permitted equivalent (§10.5) and the peer observed the direct DNS
   source as the physical eth0 address. An explicit-interface variant timed out
   in the non-root service; it is not used.

Everything below distinguishes **runtime-observed** (commands actually run),
**source-inspected** (pinned source read), and **untested**.

## 0. Environment and versions (runtime-observed)

| Item | Value |
| --- | --- |
| Host kernel | `7.0.0-44-generic` (`#44-Ubuntu SMP PREEMPT_DYNAMIC`) |
| Container base | `ubuntu:26.04`, image ID `sha256:f144425ff09be612d6d9ad965196e9cdc23dae1f42110a8a11a3e9a8198759f7` |
| Spike image | `rl-tun-spike:base`, image ID `sha256:cbf0ea967ebe733c1072a3ba64e0816e609098b8e1dd98108d083ef9f909c13a` |
| Systemd | `259.9-0ubuntu0.1` (system manager as PID 1 in the container) |
| iproute2 | `6.19.0` |
| Docker server | `29.10.1` |
| Go | `go1.27.4 linux/amd64` |
| Xray pin | `v1.260327.1-0.20261109052709-676eaf66f8f0`, commit `676eaf66f8f05d4d558ee7a74b4736623d3feeb9`, runtime version `26.11.9` |
| sing-box sidecar | `v1.14.4`, revision `c005f35300ce76427dc285481ec5695f5b179cd2`, archive sha256 `699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968` (verified before use) |

Container creation: `docker run --privileged --cgroupns=private --tmpfs /run
--tmpfs /tmp` plus a dedicated bridge network with IPv4 `172.31.77.0/24` and
IPv6 `fd00:77::/64`. The inside view was `0::/init.scope` and its own cgroup
namespace; no host path, host cgroup root, host DNS, host firewall, host route,
host interface or host systemd was mounted or touched. The **container** is
privileged so systemd can run; the **service unit** is non-root and restricted
to `CAP_NET_ADMIN`. Evidence: `evidence/00-versions.txt`.

## 1. Hardened system unit, non-root TUN/route creation, peer-uid socket

| Claim | Command / locator | Observed | Status |
| --- | --- | --- | --- |
| Unit runs as non-root with only `CAP_NET_ADMIN` | `systemctl show` in `evidence/01-systemd-unit.txt` | `User=rlspike` (uid 1500), `Group=rlspike`, `AmbientCapabilities=cap_net_admin`, `CapabilityBoundingSet=cap_net_admin`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `ProtectKernelTunables/Modules/ControlGroups=yes`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_UNIX`, `UMask=0077` | runtime-observed |
| Binary is a root-owned copy on a root-owned path | `stat` / `/proc/<pid>/exe` | `/usr/libexec/routelatch-spike/tun-service`, `root root 0755`; process uid/gid 1500; `CapEff=0000000000001000` (`CAP_NET_ADMIN` only); `NoNewPrivs=1` | runtime-observed |
| Non-root service can create a TUN | service journal, probe lines | `ip tuntap add dev rl-probe0 mode tun` succeeded; link created | runtime-observed |
| Non-root service can address the TUN and add a route | service journal | `ip addr add 192.0.2.1/32 dev rl-probe0` and `ip route add 192.0.2.2/32 dev rl-probe0` succeeded; `ip route get 192.0.2.2` returned `dev rl-probe0 src 192.0.2.1 uid 1500`; probe link then deleted | runtime-observed |
| Control socket is `0600` and checks peer uid | `stat`, `tunctl.py` as uid 1500 and as root | socket `srw------- rlspike rlspike`; allowed uid status returned `ok:true`; root client got `peer_uid_rejected` and exit 1 | runtime-observed |
| Hardening is meaningful | `systemd-analyze security` | overall exposure `3.6 OK`; UMask check passes; no `AF_PACKET`; no new privileges | runtime-observed |

The probe used only container-local TUN/routes; it was deleted before the real
core started. `PrivateDevices=no` is explicit and permitted by the contract
because a private `/dev` would not contain `/dev/net/tun`; no device was added.

## 2. Pinned Xray TUN: dual-stack, halves, sniffing, loopback SOCKS

Config: [`xray-tun.json`](xray-tun.json) — `rl-tun0`, MTU 1500, gateways
`198.18.0.1/15` + `fc00::1/64`, `autoSystemRoutingTable` halves, sniffing
`http,tls`, `routeOnly=false`, no DNS takeover, static routing to
`rl-tun-socks` (127.0.0.1:1080), `rl-tun-dns` (freedom, direct) and `rl-block`.

| Claim | Command / locator | Observed | Status |
| --- | --- | --- | --- |
| Link and addresses | `ip -br addr show rl-tun0` (`evidence/03-tun-routes.txt`) | `rl-tun0 ... 198.18.0.1/15 fc00::1/64` | runtime-observed |
| IPv4 halves and gateway route | `ip route` | `0.0.0.0/1 dev rl-tun0 metric 1`, `128.0.0.0/1 dev rl-tun0 metric 1`, `198.18.0.0/15 dev rl-tun0 ... src 198.18.0.1`; physical default route remained | runtime-observed |
| IPv6 halves and gateway route | `ip -6 route` | `::/1 dev rl-tun0 metric 1`, `8000::/1 dev rl-tun0 metric 1`, `fc00::/64 dev rl-tun0 ... src fc00::1` | runtime-observed |
| Real app TCP is captured | `app_probe.py tcp` as uid 1500 | `172.31.77.20:7102` echo succeeded (203 ms); payload reached peer only via the fixture | runtime-observed |
| Sniffed TLS domain reaches SOCKS | TLS app with SNI + fixture log | fixture saw `target=canary-r16a.invalid port=7101`; peer saw SNI `canary-r16a.invalid` | runtime-observed |
| IPv6 TLS works and is sniffed | app to `fd00:77::20:7101`, SNI `canary6-r16a.invalid` | TLS 200 (7 ms); fixture saw `canary6-r16a.invalid`; peer saw that SNI over IPv6 | runtime-observed |
| HTTP Host sniffing works | app to `:7104` with Host `canary-http-r16a.invalid` | fixture saw that Host; peer saw it | runtime-observed |
| Stop removes link/routes | `tunctl.py disable` | `cleanup_ms=21`, `link_present=false`, `routes=0`; `ip link`/`ip route` found no `rl-tun0` | runtime-observed |

The peer is on-link in this container network, so the harness added a test-only
`172.31.77.20/32 dev rl-tun0` (and IPv6 `/128`) route to force the app through
TUN. This is a **simulated off-link destination**; real off-link destinations are
captured by the halves without that route. The fixture rewrites the canary
domain canary→peer address; it is test-only and its logs are allowed to contain
destinations.

## 3. DNS/53 direct, UDP non-53 blackholed, system DNS unchanged

| Claim | Command / locator | Observed | Status |
| --- | --- | --- | --- |
| UDP DNS is answered through TUN | `app_probe.py dns` A/AAAA | `203.0.113.201` and `2001:db8::201`, rcode 0 | runtime-observed |
| TCP/53 also works | `app_probe.py dns --tcp` | A answer, rcode 0 | runtime-observed |
| DNS reaches the peer directly on the physical interface | peer dnsmasq log (`evidence/05-dns-udp.txt`) | `query[A] canary-r16a.invalid from 172.31.77.10` (the eth0 address, not the TUN gateway) | runtime-observed |
| Non-53 UDP is blackholed | UDP to peer `:9999` and `:443` | both timed out (~2 s); peer UDP log contains only its `udp_listen` lines, zero `udp_datagram` | runtime-observed |
| System/container DNS is unchanged | `sha256sum /etc/resolv.conf` before/after; `resolvectl` | identical hash `2f762…d010`; `/etc/resolv.conf` still `nameserver 127.0.0.11`; `resolvectl` absent; no systemd-resolved | runtime-observed |
| `autoSystemDnsToGateway` is off | config grep | `"autoSystemDnsToGateway": false` | runtime-observed |
| `autoOutboundsInterface=auto` binds the direct DNS dial | DNS with no explicit `sockopt.interface` | peer saw source `172.31.77.10`; DNS succeeded while the TUN route to the peer existed | runtime-observed |

An explicit `streamSettings.sockopt.interface: eth0` on the DNS outbound was
tried in an earlier development run of the same non-root unit and **timed out**
(peer saw no query), while the auto path succeeded. The final tested config uses
the auto path; the raw earlier log was overwritten by the final run, so this is a
recorded development observation rather than part of the reproducible artifact.
The exact cause was not diagnosed; it is recorded rather than hidden.

## 4. Engine egress bypass and sidecar loop risk

| Claim | Command / locator | Observed | Status |
| --- | --- | --- | --- |
| Direct/probe/fetch-style dial bypasses TUN | `egressprobe -mode direct` as uid 1500, TUN active | echo true, 0 ms, local `172.31.77.10`; fixture loop counter `total:0` | runtime-observed |
| Embedded pinned-Xray pool-style dial bypasses TUN via a dialer controller | `egressprobe -mode xray-bound` | echo true; fixture loop counter `total:0` | runtime-observed |
| The fixture really detects a loop | `egressprobe -mode xray-loop` (same core, no controller) | echo true, but fixture saw `port 7103` (`total:1`) — the dial entered TUN and was proxied | runtime-observed |
| Pinned sing-box supports `route.default_interface` | `sing-box check` v1.14.4 | check 0; direct outbound with `default_interface=eth0` succeeded under TUN with fixture `total:0` | runtime-observed |
| Negative control for the field | same config without `route.default_interface` | request succeeded only by looping: fixture saw `port 7104` (`total:1`) | runtime-observed |
| hysteria2 sidecar is supported, not excluded | local hysteria2 server + client, TUN route + `default_interface=eth0` | baseline (on-link) rc 0; TUN + `default_interface` rc 0 and fixture `total:0`; TUN without `default_interface` rc 28 (timeout, UDP blackholed) | runtime-observed |
| No sidecar loop under TUN | fixture counters | zero in the supported runs; only the deliberate negative controls hit the fixture | runtime-observed |

The hysteria2 test used a local self-signed certificate and a per-run test
password; no public exit or credential was used. The server and client were run
inside the disposable containers (the sidecar itself was root there; its
service hardening is not part of this gate). Config snippets and counters:
`evidence/07-singbox-sidecar.txt`. Contractual fallback (exclude hysteria2 as
`unsupported`/`sidecar_loop_risk`) is **not needed** for v1.14.4: the field is
supported and effective for both direct and hysteria2 QUIC dials.

## 5. Fail-closed, recovery, lease, SIGKILL and `tun off`

| Claim | Command / locator | Observed | Status |
| --- | --- | --- | --- |
| Engine/SOCKS death fails closed | kill the loopback SOCKS fixture, keep TUN/routes | TUN/routes still present; app connect reached the TUN stack but got 0 bytes and EOF after **1205 ms**; peer echo count stayed 5 → 5 (no direct connection) | runtime-observed |
| Engine restart recovers | restart fixture | same app succeeds in 205 ms; peer receives exactly the new request | runtime-observed |
| Service restart rebuilds from lease | `systemctl restart` while TUN on | link absent 14 ms, present 13 ms later; status `on`, lease `desired=on`; app succeeds after the test route is re-added | runtime-observed |
| SIGKILL route/link cleanup | `systemctl kill -s KILL` with `Restart=on-failure` | link down after **22 ms**; systemd auto-restarted and link returned after **1147 ms**; total kill→recovered **1170 ms** — this is the measured fail-open window | runtime-observed |
| SIGKILL leaves no persistent route/DNS | same test with a temporary `Restart=no` drop-in | after 2 s: `active=failed` (link gone in 24 ms), link absent, no `rl-tun0` IPv4/IPv6 routes; direct app and DNS work; lease remains `desired=on`; `/etc/resolv.conf` unchanged | runtime-observed |
| `tun off` explicitly removes coverage | `tunctl.py disable` | `cleanup_ms=21`, link absent, no TUN routes, lease `desired=off`; service can then be stopped (inactive, no link) | runtime-observed |
| Recovery after the orphan test | remove drop-in, start service | lease rebuilt TUN; app succeeds | runtime-observed |

The fail-closed TCP behavior is **accept-then-close/EOF**, not an immediate
`ECONNREFUSED`: this is the documented R-27 userspace-stack behavior. It never
falls back to a direct route. The short fail-open window is the kernel teardown
plus the unit's `RestartSec`; a permanent kill-switch is out of scope.

## 6. Constants and collision

Pinned names/addresses for the implementation:

- interface: `rl-tun0`
- IPv4 gateway: `198.18.0.1/15` (benchmarking range)
- IPv6 gateway: `fc00::1/64` (ULA)
- halves: `0.0.0.0/1`, `128.0.0.0/1`, `::/1`, `8000::/1`, priority 1

| Check | Observed | Status |
| --- | --- | --- |
| Container before TUN | only `lo`, `eth0`; no `rl-tun0`, no `198.18.0.0/15`, no `fc00::/64` | runtime-observed |
| Real host (read-only) | a Wi-Fi interface with a typical `192.168.1.0/24` LAN, a VPN TUN (`<host-vpn-iface>` in evidence), Docker bridges; no `198.18.0.0/15`, no `fc00::/64`, no `rl-tun0` | runtime-observed, read-only host inspection |
| Simulated typical LAN | `192.168.50.0/24 dev eth0`; after TUN it remained direct and a real app to `192.168.50.20:7104` succeeded in 1 ms | simulated container, runtime-observed |
| Simulated external VPN/TUN | `vpn0` TUN with `10.8.0.1/24` and `fd42:0:0:1::1/64`; coexisted with `rl-tun0` (198.18/15, fc00::/64) without collision | simulated container, runtime-observed |
| Conflict report | no collision found in this environment; `198.18.0.0/15` can collide with a benchmarking/VPN network on another host, so setup must keep the documented conflict warning (R-30) | limitation |

The real host's VPN interface is a second TUN; it had only link-local IPv6 and
its routes did not overlap. A second **full-tunnel** VPN would compete for the
halves/default route; that policy interaction is R-30 and was not tested beyond
route coexistence. All simulated interfaces/routes were inside the container
and were removed.

## 7. D21 / canary

Canary values: domain `canary-r16a.invalid` (plus `canary6-` and
`canary-http-` variants) and `203.0.113.201` / `2001:db8::201`.

| Surface | Result |
| --- | --- |
| service journal (`journalctl -u routelatch-tun-spike`) | no canary, no peer fixture address (`grep` count 0) |
| `/run/routelatch-tun/*` (socket, lease) | no canary |
| `/etc/routelatch-tun-spike/*` (service + Xray config) | no canary |
| `tunctl.py status` JSON | no canary; only interface/state/caps/routes counts |
| fixture SOCKS/peer/dns logs | contain the canary/domain by design; these are test-only, not product logs |
| Xray at the configured level | `logLevel: "none"`, `access: "none"`; no Xray log file |

Caveat (source-inspected): the pinned TUN handler contains
`errors.LogInfo(ctx, "processing from ", source, " to ", destination)`
(`proxy/tun/handler.go:246`). At `info`/`debug` it would log destinations; the
negative control is the source line recorded in
`evidence/13b-d21-source-config.txt`. The service config is fixed and root-owned;
it exposes no user log-level input. If the requirement is read literally as “no
destination even at debug”, stock Xray needs a log patch; otherwise the
contracted `none` configuration meets D21.

Sniffing metadata is used only in the flow and in the SOCKS request; no state or
status field was found containing a destination or domain. The fixture is the
only component that logs what it needs.

## 8. What was run / observation scope

One command:
[`run.sh`](run.sh) (`RL_TUN_RUN_ID=<id> ./spike/tun/run.sh`) builds the
binaries, builds/uses the local `ubuntu:26.04` systemd image, creates the two
containers, runs all sections above, writes `evidence/*.txt`, and removes the
containers/network on exit. `RL_TUN_KEEP=1` is for inspection only and leaves a
privileged container; the delivered run was cleaned up (no `rl-tun-spike`
container/network remained).

**Runtime-observed:** systemd unit/caps/binary/socket, TUN/route creation and
cleanup, app TCP/TLS/HTTP/IPv6, sniffed domains at the SOCKS fixture, DNS/53
v4/v6/TCP, non-53 UDP blackhole, engine direct + embedded-Xray bypass and loop
negative control, sing-box direct/hysteria2 `default_interface`, fail-closed
kill/recovery, lease restart, SIGKILL/auto-restart/orphan cleanup, `tun off`,
collision simulation, canary scans.

**Source-inspected only:** the info-level TUN log line; the `SO_BINDTODEVICE`
controller in `proxy/tun/handler.go`; the Xray route/cleanup implementation.

**Untested / out of scope:**

- The real desktop/host network (the host was only read for route inspection);
  no public destinations or public exits were used.
- Real off-link internet traffic; the `/32` `/128` test routes simulate an
  off-link peer inside the container.
- DNS-through-pool and any UDP forwarding (D30 / R16-I/J are separate tasks).
- Per-app/per-UID coverage, Windows/macOS/Android, ECH, QUIC beyond the
  blackhole check, IPv6-only exits, suspend/resume and physical interface
  changes (R-28), and a second full-tunnel VPN's policy interaction (R-30).
- Explicit DNS `sockopt.interface` under the non-root service (timed out; not
  used); sidecar under a hardened systemd unit (the sidecar test was root in the
  disposable container).
- A literal “no destination at Xray debug level” guarantee (stock Xray logs it;
  the service pins `none`).

Evidence index:

- `evidence/00-versions.txt` — image/kernel/systemd/iproute2/Xray/sing-box
- `evidence/01-systemd-unit.txt` — unit, caps, binary, socket, journal, security
- `evidence/03-tun-routes.txt` — link, routes, routes to test destinations
- `evidence/04-app-tcp-sniff.txt` — TCP/TLS/HTTP/IPv6 and fixture domains
- `evidence/05-dns-udp.txt` — DNS answers, UDP blackhole, system DNS
- `evidence/06a-auto-outbounds.txt` — auto-only DNS path
- `evidence/06-engine-egress.txt` — direct/Xray/loop counters
- `evidence/07-singbox-sidecar.txt` — default_interface, hysteria2 runtime
- `evidence/08-fail-closed.txt` — engine kill/EOF/no direct request/recovery
- `evidence/09-restart-lease.txt` — restart gap and rebuild
- `evidence/10-sigkill-failopen.txt` — fail-open 1170 ms and orphan cleanup
- `evidence/11-tun-off-cleanup.txt` — `tun off` and stop cleanup
- `evidence/12-collision.txt` — constants, host read-only, simulated LAN/VPN
- `evidence/13-d21-canary.txt` — canary scans
- `evidence/13b-d21-source-config.txt` — pinned log line and config level
- `evidence/14-cleanup.txt` — final no-link/no-route checks
