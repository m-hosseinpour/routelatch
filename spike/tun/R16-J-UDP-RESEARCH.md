# R16-J — UDP/QUIC coverage feasibility (research)

**Date:** 2026-11-27
**Revision:** `c1b8454` (`main`), working tree had only concurrent R16-I
research files untracked; this worker changed nothing outside `spike/tun/**`.
**Scope:** R16-J / D30 — feasibility and decision only. No product, docs, plan,
contract or acceptance file was edited. No product UDP support is implied.

**Verdict: Ready for decision. Recommendation: keep non-DNS UDP blocked for the
current product line; do not treat R16-J as an implementation authorization.**
The real-exit failure-rate sub-question is **Not verified**; that does not block
the keep-blocked decision, but it blocks any decision to ship UDP on the claim
that the pool supports it.

The evidence is sufficient to decide *whether* to pursue UDP, but not to
implement it: the engine has no UDP data path, and real-pool UDP support/failure
rates were not measured (no pool/public-exit use was authorized). A positive
decision would need a new decision, contract and acceptance (D31/A18) and a
substantial milestone, not a TUN rule flip.

The single most consequential new finding is that the **shipped TUN sniffing
configuration already adds ~200 ms to every new non-QUIC UDP flow** (measured
203–205 ms vs 1.5–3.1 ms with sniffing off, §5). The same code path runs before
the port-53 rule, so it also affects plain DNS/53 today (source-derived; the
generic UDP delay was measured, DNS latency was not). Fixing it needs a
sniffing-policy change or a small core patch. It was not measured in R16-A/H.

## 0. What was verified, and how

| Class | Meaning |
| --- | --- |
| runtime-observed | actually executed in a disposable Docker container with the pinned binaries; no public exit, resolver, subscription, credential or host network/DNS/firewall change |
| source-inspected | read in the pinned Xray module cache at commit `676eaf66f8f05d4d558ee7a74b4736623d3feeb9` (version 26.11.9) |
| not verified | no evidence; see §10 for the precise blocker |

Fixtures ran with `--network none` (loopback only) or a dedicated `--internal`
Docker bridge with two disposable containers. Local certificates and test
passwords are synthetic. H-ACCEPTANCE and H-TRAY limits (fail-open status/icon,
no host TUN, no real pool) carry into §10. The only public-looking addresses
used were TEST-NET (initially) and `93.184.216.34` bound to `lo`; no packet left
the container. Xray was built from the repository's pinned module; sing-box was
the pinned local `v1.14.4` (`c005f353…`). Versions:
`evidence/r16j-udp-2026-11-27/results/00-versions.txt`.

## 1. Exit support matrix (protocol level)

The matrix answers "can the pinned exit implementation carry UDP at all", with
controlled local servers. It does **not** answer "does a given subscription
node allow UDP"; §10.

| Exit protocol | UDP carried? | Runtime fixture | Source locator | Cost / caveat |
| --- | --- | --- | --- | --- |
| **hysteria2** (sing-box sidecar) | **Yes**, QUIC-native | mixed inbound → hysteria2 client → hysteria2 server → UDP echo: 10-byte and 1200/1400-byte payloads OK | sing-box v1.14.4 runtime; sidecar uses `mixed` inbound and `hysteria2` outbound (`internal/singbox/config.go`) | Best fit: no TCP-in-TCP, no Vision/443 restriction. Requires the server to allow UDP. UDP is carried over QUIC; burst delivery was lossier than VLESS in the fixture (§4). |
| **VLESS** (no flow) | **Yes**, but the default pinned core uses **XUDP** for UDP on ports other than 53/443 | port 9000 (default cone → XUDP): OK; plain UDP with `XRAY_CONE_DISABLED=true`: OK | `proxy/vless/outbound/outbound.go:215-216,246-258,303-320`; cone default in `core/xray.go:195-196` | The product excludes mux/XUDP. A default VLESS exit therefore silently pulls XUDP into the UDP path; either accept that change or pin `XRAY_CONE_DISABLED=true` (process-global) / use port 53/443. |
| **VLESS Vision** (`xtls-rprx-vision`) | **Restricted**: UDP/443 is rejected unless the flow is the `-udp443` variant; other Vision UDP ports use XUDP | source only (TLS/Vision fixture not built) | `outbound.go:249-258,303-320`; product accepts only `xtls-rprx-vision` (`internal/engine/config.go:305-309`) | **This blocks ordinary HTTP/3 (QUIC/443) through Vision exits**, a large part of a typical pool. Supporting it needs the `-udp443` flow, which the importer currently rejects, plus a Vision/TLS fixture. |
| **Trojan** | **Yes**, per-packet over the tunnel | plaintext fixture to isolate UDP: 10-byte and 1200/1400-byte OK | `proxy/trojan/client.go:111-143` (`PacketWriter`/`PacketReader`) | The product uses TLS; the fixture used plaintext only to isolate UDP. No 443 restriction like Vision. |
| **SOCKS5 exit** | **Yes**, UDP ASSOCIATE | sing-box `mixed` inbound as exit → echo: OK | Xray `proxy/socks/client.go:106-160`; sing-box mixed runtime | The exit must implement UDP ASSOCIATE and return a reachable relay address. Xray replaces `0.0.0.0`/`::` with the server address, a deployment-dependent hack. |
| **HTTP CONNECT** | No | — | — | HTTP has no UDP; A12's frontend remains TCP-only. |
| **DNS/53** | Separate path, direct (D30/R16-I) | not re-tested here; H tested direct v4/v6 UDP/TCP | `internal/tun/xrayconfig.go:116` | Keep direct; a UDP switch must not capture 53. |

The Xray SOCKS **outbound** also carries UDP to a downstream SOCKS server; that
is the path the TUN service would use toward the engine's loopback listener.
Fixture: `socks5_exit_udp` and `xray_socks_outbound_udp` both OK
(`results/01-exit-matrix.txt`).

### Exit-side policy note

The pinned core's `freedom` outbound blocks private/documentation destinations
for VLESS/Trojan/VLESS-Vision/Hysteria inbound names
(`proxy/freedom/freedom.go:75-87,165-180`). The fixture initially failed until
the destination was moved off TEST-NET. This is not a UDP limitation, but it
means a UDP exit cannot reach LAN/private destinations; the product's LAN
exception is direct anyway, and a DNS-through-pool resolver would have to be
public.

## 2. TUN UDP flow, NAT, timeout, resources, teardown

Source-inspected in the pinned TUN stack, plus a runtime TUN fixture.

| Property | Observation | Evidence |
| --- | --- | --- |
| Flow key | **source address:port only**; first destination becomes the default, later datagrams to other destinations are allowed (full-cone-ish) | `proxy/tun/udp_fullcone.go:40-60,110-120` |
| Queue | 1024 packets **per flow**; excess is dropped; **no global flow or memory cap** | `udp_fullcone.go:167-168` |
| Idle timeout | `StackOptions.IdleTimeout` is set from policy (`handler.go:171`) but **never read** in the TUN stack; the flow lives until the outbound's inactivity timer closes the link. Default `ConnectionIdle` is **300 s** (`features/policy/policy.go:131`) | `stack.go:39`, `handler.go:171`, `policy.go:131` |
| IPv4/IPv6 | The stack handles both; the TUN link/routes are dual-stack. IPv6 UDP through a pool exit was **not** tested | `stack.go:390-444`; H IPv6 DNS/TCP only |
| MTU / datagram size | TUN MTU 1500 ⇒ ~1472-byte IPv4 payload. Fixture: 1200/1400 OK; **8192 and 60000 timed out for both VLESS and hysteria2** | `results/01-exit-matrix.txt` |
| Teardown | stopping Xray removes `rl-r16j0`, IPv4/IPv6 halves and addresses; no residual route | `results/03-tun-udp.txt` |
| Engine death | TUN stays; UDP to the dead loopback SOCKS times out; **no direct fallback**; peer received nothing new | `results/03-tun-udp.txt` |
| TUN service death | existing fail-open window (0.605–1.390 s in H) applies to UDP too; no permanent kill-switch; status visibility in the gap was not verified | `H-ACCEPTANCE.md` limits 6 |
| Loop risk | The TUN service's own DNS uses `freedom` bound to the physical interface; the engine and sidecar have egress bypass in TUN mode. The fixture's relay used `default_interface=eth0` and did not loop. A **new engine UDP relay must use the same bypass**; no loop-counter test exists for it | `results/03-tun-udp.txt`; contracts §10.6 |

The TUN buffer carries each packet's own destination (`udp_fullcone.go:117`);
the SOCKS and XUDP writers honor it (`proxy/socks/protocol.go:419-424`,
`common/xudp/xudp.go:111-121`), so several destinations from one source port can
work there. Plain VLESS UDP keeps the first target fixed. "Full-cone" is
therefore outbound-dependent, not a TUN guarantee.

Runtime TUN test (`results/03-tun-udp.txt`): app UDP → `rl-r16j0` → Xray
`rl-tun-socks` outbound → a loopback SOCKS UDP relay → UDP echo peer → back
through the TUN. Two live datagrams echoed. After killing the relay, the third
datagram timed out and the peer's datagram count stayed at 2 — the dead-relay
case is fail-closed with no direct leak. This is a *fixture* relay, not the
product engine; policy, exit selection and D21 registry were not exercised.
The relay's `missing default interface` log is cosmetic here: the internal
bridge has no default route, and the explicit `default_interface=eth0` binding
still worked (the echo was received).

## 3. Policy under targeted / all-proxy / all-direct / stopped

Any UDP feature needs a policy answer; the current contract has none.

| Mode | If UDP were forwarded | Problem / decision |
| --- | --- | --- |
| `targeted` | approved destinations via pool, others direct | **Approved domains without an IP/CIDR rule cannot be matched by IP alone.** Forwarding would either send them direct (a silent leak of an approved destination, violating D11) or block them. The safe rule is: block UDP unless the destination matches an approved **IP/CIDR** (or a sniffed domain, §6); never direct an approved domain. |
| `all-proxy` | all internet UDP via pool, local/LAN direct | Simplest policy: no domain/IP ambiguity. Still needs per-exit UDP capability and fail-closed when an exit or the pool cannot carry UDP. |
| `all-direct` | direct UDP (if the switch is on) | D29 currently blocks non-DNS UDP even in all-direct. Changing it is a separate user-visible behavior change; keeping it blocked is consistent with A17. |
| `stopped` | no TUN | OS-level UDP is direct because coverage is off; that is expected, not a leak, but the status must not claim UDP coverage. |
| `no sniff` / ECH | IP-only | ECH/unknown QUIC versions expose no usable destination identity; only IP/CIDR rules apply. Do not treat the ECH outer SNI as the real destination. |

**Per-app/per-UID** is out of scope (D29 R-24): the switch is host-wide (all
users/processes). Resource abuse by one local app affects all.

**Per-exit capability** is the hard part: "VLESS/Trojan/SOCKS5 can carry UDP"
does not mean a given node allows it. A per-flow attempt can fail; the product
must fail closed (drop/timeout), not fall back direct. There is no handshake
proof for UDP, so the D22 proof/exit-selection model does not transfer; a new
"UDP flow has no destination proof" rule is needed.

## 4. Cost and failure observations (controlled loopback, not real exits)

Fixtures: UDP echo on a local address; 20 ms one-way delay + 1 % loss on `lo`
(`tc netem`); 200 sequential request/response pairs and a 200-datagram burst.
This is a synthetic link, **not** a measurement of real exit failure rates.

| Path | Clean p50 | Netem p50 | Sequential OK | Burst unique replies |
| --- | --- | --- | --- | --- |
| VLESS (XUDP path) | 0.2 ms | 121.5 ms | 190/200 | 190/200 |
| hysteria2 | 0.4 ms | 121.9 ms | 183/200 | **121/200** |
| SOCKS5 direct | 0.1 ms | 80.9 ms | 185/200 | 194/200 |

With a fresh SOCKS association per datagram (closer to a new QUIC flow), the
setup cost is visible: VLESS 162.8 ms, hysteria2 122.5 ms, SOCKS 81.4 ms
(`results/01-exit-matrix.txt`). VLESS's extra ~40 ms is the extra TCP+VLESS
setup round trip; steady-state within one association is comparable.

Interpretation: VLESS/Trojan UDP over TCP retransmits lost segments, so more
datagrams arrive, but a lost segment blocks later ones (head-of-line). Hysteria2
carries UDP with QUIC DATAGRAM-like unreliable semantics, and its burst delivery
was worse in this fixture; real QUIC applications may retransmit at their own
layer. These are trade-offs, not pass/fail numbers. **Real-pool failure rate
remains Not verified.**

## 5. The 200 ms sniff cost (new finding, affects DNS today)

The shipped TUN config enables sniffing (`http,tls`) and Xray sniffs **every**
flow before routing (`app/dispatcher/default.go:324-375`). For UDP, the sniffer
tries QUIC; a non-QUIC datagram is not classified, so the dispatcher waits for
more data until its 200 ms cache deadline (`default.go:391,419`).

Measured through the TUN fixture with the shipped sniffing config vs
`sniffing.enabled=false`:

| Config | First-datagram RTT (three new flows) |
| --- | --- |
| sniffing on (`http,tls`, as shipped) | 205.0, 203.9, 202.9 ms |
| sniffing off | 3.1, 1.7, 1.5 ms |

This is per **new UDP flow**, including plain DNS/53 by the same pre-routing
code path (DNS latency itself was not measured). The first datagram is buffered
and replayed, so it is latency, not loss. It was not measured in R16-A/H. A
normal single-packet QUIC Initial is recognized quickly, so actual QUIC flows
usually do not pay this delay; a split/incomplete Initial can still wait. The
cost is on all other UDP. A UDP feature that keeps TCP sniffing needs either a
per-network sniff switch (small core patch) or an explicit decision to accept
the delay. This also matters to R16-I: the current direct-DNS path can add
~200 ms per new source port. Disabling sniffing entirely is not an option: TCP
domain policy depends on it.

## 6. QUIC/HTTP3 and domain identity

The pinned Xray has a **QUIC Initial sniffer** (`common/protocol/quic/sniff.go`,
registered for UDP in `app/dispatcher/sniffer.go:42`); its upstream tests pass
for QUIC v1/v2/draft29, split ClientHello, and incomplete SNI
(`results/06-quic-sniff-test.txt`). Therefore a TUN UDP flow *can* recover the
SNI and hand a domain to the SOCKS relay by adding `quic` to `destOverride`.

Limits that make this unsafe as an automatic solution:

- **ECH**: the sniffed name is the outer/cover SNI, not the real destination.
  Treating it as the destination identity could match a domain rule for the
  cover name. ECH must remain IP-only (or blocked), requiring ECH detection that
  this path does not provide.
- Unknown/future QUIC versions are not sniffed; IP-only.
- The sniff runs before routing and has the 200 ms cost for non-QUIC UDP (§5).
- QUIC/443 through **VLESS Vision** is rejected by the client (`outbound.go:257`)
  unless the non-default `-udp443` flow is accepted. This is the dominant
  HTTP/3 case.
- Even with a sniffed domain, the engine must implement UDP policy/exit
  selection; the TUN service cannot decide.

Without QUIC sniffing, only IP/CIDR can match; a domain-approved QUIC
destination must be blocked, not sent direct.

**Contract note:** R-26 / contracts §10.7 currently state that a domain is not
recoverable for QUIC. The pinned core's QUIC Initial sniffer qualifies that
statement for v1/v2/draft29; ECH and unknown versions remain unavailable. If
QUIC sniffing is ever enabled, R-26 must be restated and the ECH/cover-SNI rule
made explicit. It must not silently become "QUIC is always IP-only" or "the
outer SNI is the destination".

## 7. D21, privacy and security

| Surface | Finding |
| --- | --- |
| Shipped TUN Xray | `logLevel/access/error = none`; the info-level destination log at `proxy/tun/handler.go:246` is suppressed. No log-level knob. |
| QUIC/other sniffing | domains live in flow memory only; the dispatcher has an info-level "sniffed domain" log, suppressed by `none`. D21 permits in-memory live metadata, not persistence. |
| Engine frontend | `internal/frontend/socks.go:69-79` refuses UDP ASSOCIATE with `0x07`, partly because the library's default UDP handler opens a real relay and its error logs include destinations. A UDP implementation **must not** simply enable that handler. |
| Engine core | The embedded Xray core runs at the configured log level (default warning) with access/error `none` (`internal/engine/config.go:535`). New UDP dial/exit errors (e.g. VLESS Vision UDP/443 rejection) must be checked for destination-bearing text and redacted before they reach a product log, exactly as TCP errors must be. |
| Sidecar | sing-box log is generated with `disabled: true` (`internal/singbox/config.go`), so its per-connection destination logs are discarded. |
| Per-flow registry | UDP flows have no connection lifetime; a new in-memory registry would be allowed by D21 only if it is never persisted and is bounded/expired. The live tray view must not persist it. |
| Local abuse | The listener is unauthenticated loopback (D14). A SOCKS UDP relay must bind the association to the TCP peer and validate the source, or any local user could use it as an open UDP relay. |

**Leak results:** dead relay → timeout, peer count unchanged, no direct traffic
(runtime). Dead engine → same fail-closed path by construction. The TUN-service
fail-open window still allows direct packets during restart; with UDP this is a
more visible exposure, and the in-window status/icon visibility was not verified
(H limit 6). No permanent kill-switch exists (R-25).

## 8. Switch/policy options and their costs

| Option | What it means | Cost / risk |
| --- | --- | --- |
| **A. Keep blocked (recommended)** | no code change; D30 remains; QUIC apps fall back to TCP or fail | No new risk. The current 200 ms sniff cost for direct DNS remains an R16-I item. |
| **B. Separate opt-in switch** (default off, independent of mode and DNS) | user enables "UDP through pool" only with TUN on | Needs new D31 + A18, config/status fields (a new `udp` value is a contract change, not an additive field), warning vocabulary, a UDP-capable engine data path, per-mode policy, caps/timeouts, D13 sweep, D21 review, and exit capability handling. Largest scope; best control. |
| **C. Bundled with TUN (always on when TUN is on)** | simplest UX | Changes A17's guarantee for everyone, no separate consent, and breaks domain-only approvals; not recommended. |
| **D. Narrow pilot: all-proxy only** | UDP forwarded only in all-proxy, where all internet destinations use the pool | Avoids the targeted domain/IP leak ambiguity; still needs the full data path and caps. A reasonable first increment if UDP is approved. |
| **E. Narrow pilot: hysteria2 exits only** | only hysteria2 exits carry UDP; others block | Avoids Vision/443 and XUDP problems and TCP-in-TCP; limited by hysteria2 availability. |

The engine data path is the dominant cost under any option. The current frontend
is a custom `go-socks5` server whose UDP handler is refused; the engine's
policy/registry/ranking layer is built around TCP `net.Conn` and destination
handshake proof. A UDP relay needs:

- a UDP-capable frontend (or a separate loopback UDP listener) with the same
  destination normalization, policy, registry and D21 rules;
- per-flow exit selection with no handshake proof and no direct fallback;
- exit UDP capability (attempt + fail closed, or an explicit per-exit field);
- closing UDP flows on mode/rule change (D13) and on engine shutdown;
- bounded flow tables and idle expiry (the TUN side alone allows unbounded
  source-keyed flows for 300 s);
- bypass/loop registration for the relay's own UDP dials and the sidecar;
- IPv6 and QUIC/443 handling;
- a real exit sample to measure support and failure rates.

## 9. Effect on v1, A12/A17, D29/D30

- **A12** says UDP is explicitly rejected in v1. Any UDP support changes the
  frontend contract (UDP ASSOCIATE no longer `0x07` when the switch is on) and
  needs either an A12 scope change or a new A18 that applies only to the opt-in
  feature. A12 remains true for the default/off state.
- **A17** promises "non-DNS UDP blocked and QUIC leak-free". If UDP becomes
  optional, A17 must be restated as "when the UDP switch is off (the default)".
  A new A18 is needed for the opt-in behavior, including fail-closed on a dead
  pool, no direct fallback, and the status/tray contract.
- **D29** defines TUN phase one with UDP blocked; **D30** says UDP stays blocked
  until the R16-J verdict. A positive verdict does not itself enable UDP; a new
  decision (D31) is required.
- **D24/v1 no-UDP/QUIC policy**: user-flow UDP is distinct from supporting h3
  proxy transports. Enabling UDP does not add XHTTP/h3 transport support, but it
  does weaken the "no QUIC" user-facing stance and must be stated explicitly.
- **D21** is unchanged and is a hard gate for the new logs/registry.
- **D13** (mode changes close conflicting connections) has no UDP equivalent
  today; UDP flows would otherwise keep the old path after a mode/rule change.

## 10. Untested / not verified (precise blockers)

1. **Real exit/pool UDP support and failure rate — Not verified.** No
   authorization to use the user's pool, a public exit or a public resolver. The
   protocol matrix is controlled-fixture evidence only. A decision that depends
   on a real failure rate needs an authorized, small, read-only pool sample with
   synthetic UDP destinations, under R4 rules.
2. **Engine UDP data path — not implemented, not tested.** No product code
   exists; the fixture used a plain SOCKS relay. Policy, ranking, cooldown,
   registry, D13 sweep and D21 behavior are unexercised.
3. **QUIC sniffing through the TUN — source + upstream unit tests only.** The
   end-to-end override (domain in the SOCKS UDP header) and ECH behavior were not
   run. No `quic` override was enabled in the shipped config.
4. **VLESS Vision + UDP/443 — source only.** The `-udp443` variant is rejected
   by the importer; no Vision/TLS fixture was built.
5. **VLESS default cone/XUDP — source + one cone-disabled fixture.** The
   default path's XUDP use is source-derived; the product's mux/XUDP exclusion
   would need an explicit decision.
6. **IPv6 UDP through an exit — not tested.** Only IPv4 UDP was carried; H
   tested IPv6 TCP and direct IPv6 DNS.
7. **Resource/soak behavior — not tested.** No flood, many-source-port, memory
   cap, 300 s expiry, or TUN-service restart-under-UDP test.
8. **TUN-service fail-open visibility for UDP — not tested.** The 0.6–1.4 s
   window and status/icon behavior remain the H limits.
9. **Per-app/per-UID, second VPN, suspend/resume — out of scope** (D29 R-24,
   R-28, R-30), unchanged.

## 11. Decisions needed before implementation

1. **Pursue UDP at all?** Recommended: **no** for the current line; close R16-J
   as research with UDP blocked. If yes, approve a new milestone with D31 and
   A18; do not weaken D29/D30 silently.
2. **If yes, scope:** separate opt-in switch (recommended) vs bundled with TUN;
   and whether the first increment is all-proxy-only or hysteria2-only.
3. **Targeted-mode rule for domain-only approvals:** block UDP unless an
   approved IP/CIDR (or a deliberately enabled QUIC sniff) matches. Direct
   routing of an approved domain's QUIC must be explicitly forbidden.
4. **QUIC sniffing and ECH:** authorize `destOverride: ["quic"]` with the ECH
   cover-SNI limitation, or keep QUIC IP-only/blocked.
5. **200 ms sniff delay:** accept it for non-QUIC UDP/DNS, or authorize a
   per-network sniff switch/core patch before shipping UDP; R16-I should measure
   the DNS impact either way.
6. **VLESS Vision/443 and XUDP:** authorize accepting `xtls-rprx-vision-udp443`
   and/or `XRAY_CONE_DISABLED`, or accept that those exits cannot carry
   QUIC/HTTP3/UDP.
7. **Real-pool evidence:** authorize a bounded, read-only UDP capability sample
   (synthetic destinations) or accept "exit failure rate unknown" and design
   fail-closed per flow.
8. **Resource/ops contract:** choose flow/idle caps, mode-switch sweep, and
   status/tray wording for UDP; confirm the host-wide (no per-UID) scope.

## Evidence index

All paths are under `spike/tun/evidence/r16j-udp-2026-11-27/`.

- `results/00-versions.txt` — Xray/sing-box versions, HEAD, kernel, Docker.
- `results/01-exit-matrix.txt` — all exit protocols, 1200/1400/8192/60000-byte
  datagrams, fresh-association and netem runs.
- `results/02-bench.txt` — clean and 20 ms/1 % loopback benchmarks.
- `results/03-tun-udp.txt` — TUN link/routes, live UDP, dead-relay fail-closed,
  cleanup.
- `results/04-tun-latency.txt` — sniffing on vs off first-datagram RTT.
- `results/05-vless-plain-udp-cone-off.txt` — plain VLESS UDP with cone disabled.
- `results/06-quic-sniff-test.txt` — pinned QUIC sniffer unit tests.
- `results/07-source-locators.txt` — exact source locators for the pinned core
  and product.
- `fixtures/` — configs and Python/shell fixtures; `conf/` has no private key
  (generate it with `fixtures/gen-cert.sh` before running).
- `fixtures/run_tun_udp.sh`, `run_tun_latency.sh` — TUN fixtures; require
  `WORK=<fixture dir>` and a privileged disposable container.

Source locators (pinned Xray `676eaf66…`):

- `proxy/tun/udp_fullcone.go:40-60,110-120,167-168` — source-keyed full-cone
  map, destination rewrite, 1024 queue.
- `proxy/tun/stack.go:39,390-444`; `proxy/tun/handler.go:171,246` — UDP stack,
  unused `IdleTimeout`, destination log line.
- `proxy/socks/client.go:106-160` — SOCKS outbound UDP ASSOCIATE and relay.
- `proxy/vless/outbound/outbound.go:215-216,246-258,303-320` — VLESS UDP, Vision
  UDP/443 rejection, XUDP/cone.
- `proxy/trojan/client.go:111-143` — Trojan UDP packet writer/reader.
- `app/dispatcher/sniffer.go:42`; `app/dispatcher/default.go:324-375,391,419` —
  UDP/QUIC sniffing and the 200 ms cache deadline.
- `common/protocol/quic/sniff.go:69` — QUIC Initial sniffer.
- `proxy/freedom/freedom.go:75-87,165-180` — private/documentation block.
- `features/policy/policy.go:131`; `core/xray.go:195-196` — 300 s idle, cone
  default.
- Product: `internal/frontend/socks.go:69-79` (UDP refused),
  `internal/tun/xrayconfig.go:113-125` (DNS direct/UDP block),
  `internal/contract/tun.go:37-41` (`udp=blocked`),
  `internal/control/ops_tun.go:129-136` (pinned status),
  `internal/engine/config.go:305-309` (Vision-only flow).
- Contracts/requirements: `docs/contracts.md §10.1, §10.5, §10.7`,
  `docs/architecture.md §9`, `docs/requirements.md` A12/A17/D29/D30.

Fixture limitations: loopback and a two-container internal bridge only; no
public exit, resolver, pool, credential or real destination. The 1 % loss /
20 ms delay is synthetic. The TUN relay was a stand-in, not the product engine.
