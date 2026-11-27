# R16-I — DNS through the pool and an independent DNS switch (research)

**Date:** 2026-11-27
**Repository/revision:** RouteLatch `c1b8454` (`main`), clean before this research.
**Scope:** research only. No product, docs, plan, config, service or host network change; no
stage/commit. The only additions are this report and `spike/tun/r16i/**` plus
`spike/tun/evidence/r16i-2026-11-27/**`.
**Verdict:** **Ready for decision.** DoH over TCP through the existing exit path is feasible
with the pinned Xray core and fails closed when the pool is dead. DoT is not a pinned-Xray
DNS transport; plain DNS-over-TCP is possible but the exit sees plaintext queries. The
remaining work is policy/UX, not transport feasibility. The largest open issue is that the
current engine's `targeted` policy will send an unapproved resolver destination **direct**, so
an explicit DNS-egress exception is required before any “through pool” claim is true.

## 0. What was executed and what was only read

| Kind | Evidence |
| --- | --- |
| Runtime, disposable containers | Two `ubuntu:26.04` containers on an internal IPv4/IPv6 bridge (`172.31.77.0/24`, `fd00:77::/64`): client A with the pinned Xray TUN, peer B with a local DoH server, a local DNS-over-TCP server, a logging SOCKS5 “exit”, and a direct DNS resolver. No public exit, public resolver, private subscription or credential was used. Every TUN/route/socket existed only inside A. |
| Runtime pin | Xray `26.11.9`, commit `676eaf66f8f05d4d558ee7a74b4736623d3feeb9`, pseudo-version `v1.260327.1-0.20261109052709-676eaf66f8f0`; kernel `7.0.0-44-generic`; Ubuntu 26.04.1; iproute2 6.19.0. |
| Source-inspected | The pinned Xray DNS client/server code and the product's TUN/engine/egress code listed in §8. |
| Not executed | The product engine's real `targeted` router with a live pool exit; VLESS/Trojan/hysteria2 exits; public resolvers and their certificates; systemd-resolved/captive-portal environments; status/CLI/tray surfaces. These are explicit “Not verified” items, not silent passes. |

The harness is reproducible with `RL_R16I_RUN_ID=<id> bash spike/tun/r16i/run.sh`. It creates and
removes its containers/network and leaves no host link/route. Final cleanup evidence:
`evidence/r16i-2026-11-27/99-final-cleanup.txt` (no host `rl-tun0`, zero host routes/containers/
networks from the run).

## 1. Transport matrix

The pinned Xray DNS client chooses a transport from the configured nameserver URL
(`app/dns/nameserver.go:48-80`). The relevant cases are:

| Transport | Pinned Xray support | Through a pool exit? | Runtime result | Trust/limits |
| --- | --- | --- | --- | --- |
| **DoH (RFC 8484), `https://`** | Yes: `NewDoHNameServer`, HTTP/2 + uTLS, dials through the routing dispatcher (`nameserver_doh.go:40-110`, `:228-235`) | Yes. It is ordinary TCP/443 through the exit; no UDP needed. | **PASS.** A and AAAA queries entered `rl-tun0`, were answered by a local DoH server, and the SOCKS fixture recorded a TCP connect to the DoH port with a TLS ClientHello (`0x16 0x03`). No query name was visible in the first 64 bytes. IPv4 and IPv6 upstreams both passed. | Exit sees resolver IP/SNI, TLS sizes/timing, not the query. Resolver sees the query and the exit's source IP. This is the only transport that keeps query names off the exit. |
| **DNS-over-TCP, `tcp://`** | Yes: `NewTCPNameServer` dials through the dispatcher (`nameserver_tcp.go:33-60`) | Yes (TCP only). | **PASS.** A/AAAA worked through the SOCKS fixture. The fixture's first bytes contained the plaintext query name. | The exit (and any middlebox on its side) sees the query and answer in clear text. Not acceptable as the privacy-oriented default. |
| **DoT, `tls://`** | **No native scheme.** `nameserver.go` has cases for `https`, `h2c`, `quic+local`, `tcp`, `tcp+local`, `fakedns`; no `tls`/`dot` case. | Not without a separate DoT client. | **NEGATIVE.** `xray run -test` accepts `tls://` because it only parses the address; at runtime it does not create a DoT client. A TUN query using `tls://172.31.77.20:853` timed out, no TCP/853 connection reached the peer, and no 853 listener existed. `common/net/address.go:79` turns the non-IP string into a domain; the unknown scheme then falls through to the UDP classic nameserver path. | A `tls://` value is accepted but misrouted (UDP/53 to a bogus domain); product config validation must reject it rather than trusting Xray's parser. |
| **DoQ (`quic://`)** | Only `quic+local` exists, and its constructor takes no dispatcher (`nameserver_quic.go:39`). | **No.** It is local/direct UDP, and UDP/QUIC is outside v1. | Not run as a positive path; config-only parse accepted it (same parser gap). | Requires R16-J and a policy change; not a candidate. |
| **Plain UDP/53 through pool** | Xray can create a UDP classic nameserver, but the product's SOCKS front end refuses UDP ASSOCIATE (`internal/frontend/socks.go:70-77`, reply `0x07`). | **No** in v1. | Not possible without R16-J. | Would require UDP support, session/NAT and leak policy. |

**Exit matrix.** The supported pool protocols are `vless`, `trojan`, `socks5` and `hysteria2`
(`internal/contract/pool.go:29-41`); HTTP exits are unsupported in v1. All four carry
arbitrary TCP streams, so DoH is not an “exit feature”: it is TCP/443 to a resolver. There is
no per-exit DNS capability to negotiate. Runtime evidence here used a local SOCKS5 exit only;
per-protocol runtime evidence (VLESS/Trojan/hysteria2) is **not verified** in this task.
Hysteria2 is carried by the sing-box sidecar, whose generated config has no DNS section and
preserves destination hostnames (`internal/singbox/doc.go:1-12`), so a DoH stream to a
resolver IP is compatible at the transport layer.

**Bootstrap.** The tested path used an IP-literal DoH URL, so no system DNS was needed:
Docker's embedded resolver in A was given an unreachable upstream `203.0.113.99`, `getent`
failed, and DoH still worked. The certificate had IP SANs for the test addresses. A
hostname-based DoH URL was **not** tested; without a pinned IP/static mapping it needs a
bootstrap resolution (system DNS, `/etc/hosts`, or exit-side resolution), and in `targeted`
mode the resolver hostname may not be approved. Recommendation: require an IP-literal resolver
URL (or a separately validated pinned mapping) and reject hostname-only configurations until a
bootstrap design is agreed.

**TUN loop.** The pinned TUN config uses `autoOutboundsInterface: "auto"`
(`internal/tun/xrayconfig.go:90-120`), which R16-A proved binds the direct DNS path to the
physical interface. In this run, the DoH/TCP upstream dials reached the peer from
`172.31.77.10`/`fd00:77::10` and the direct resolver did not see them. The TUN service's own
outbound is the loopback engine SOCKS (exempt from the bypass), and the engine's own pool dial
is bound by `internal/egress` (§10.6). No loop was observed. The runtime evidence does not
exercise the product engine's pool dial, only a local SOCKS fixture.

## 2. Policy options

The switch can only affect DNS packets captured by TUN. It cannot change system DNS (D15/D29)
and does not see apps that use a loopback stub resolver or their own DoH. With that boundary:

| Option | Behaviour | Cost/risk | Fit with D30 |
| --- | --- | --- | --- |
| **A. All captured queries → pool** | When on, every DNS/53 packet captured by TUN goes to the configured DoH resolver through the pool; when off, exactly the phase-one direct path. | Simple and independent of destination mode. In `targeted`, names for unapproved destinations also go to the resolver/exit, revealing them to the exit even though their connections are direct. In `all-direct`, DNS would use the pool while traffic is direct; surprising unless labelled. Local/split-horizon names break; no direct fallback. | Best literal fit: one independent switch, not coupled to mode/rules. |
| **B. Approved domains → pool, others direct** | Policy-aware split: approved `domain`/`domain_suffix` names use DoH through the pool; everything else uses the direct resolver. | Needs dynamic policy in the DNS path, a policy-version change path, and a strict no-fallback rule for approved names. Still leaks the set of approved names to the exit and can diverge from the connection's eventual routing. More code and more tests. | Couples DNS to the routing list, so it is not fully independent of mode/policy. |
| **C. Mode-coupled** | `all-proxy`: all queries pool; `targeted`: approved-only; `all-direct`: direct. | Predictable, but not the independent switch D30 describes and more states to test. | Weakest fit with D30’s wording. |
| **D. Plain DNS-over-TCP only** | Use `tcp://` through the pool; no DoH. | No TLS/resolver trust decision and no IP-SAN issue, but the exit sees every query in clear text. | Technically simplest; privacy cost is material. |

**Recommendation:** choose **A** for the first switch, with explicit warnings, *if* the product
is willing to say that a resolved name for an unapproved destination is visible to the chosen
resolver/exit while the switch is on. If that is unacceptable, choose **B** and accept the
policy coupling and extra implementation. Do not implement B by adding a fallback local
nameserver: the pinned DNS client falls back to other nameservers unless
`disableFallback: true` or the direct client has `skipFallback` (`app/dns/dns.go:309-380`).
That is a silent direct-DNS path and violates D30.

**Relations to modes.** The switch should be stored independently of `Mode`. Its *actual*
effect should be reported as `direct` when TUN is off/stopped or the switch is off, and as
`pool` when the path is configured and active. `all-direct` + switch on is technically
possible but should require an explicit warning/confirmation; whether to refuse it is a user
decision (see §6). In `targeted`, the current router will make the resolver connection direct
unless the resolver is approved. That is not a working “through pool” implementation and needs
the explicit DNS-egress exception described in §3.

**Empty/dead pool.** Tested: with the SOCKS exit stopped, a new query timed out at ~6.0 s;
the direct resolver received **zero** queries for that name (`canary-r16i-dead` count 0); the
DoH server received no query; no direct fallback occurred. After restarting the exit, the next
query succeeded in 58 ms. The product must map this to a clear DNS error/status
(`dns_pool_unavailable` or equivalent) and never to direct DNS. A cached answer may still be
returned until its TTL expires; that is not a bypass, but the status should not claim “DNS
healthy” during that window. Do not enable `serveStale` for this path: stale answers would mask
a dead pool instead of producing the agreed clear error.

**Cache/retry/latency.** The pinned DNS client caches per nameserver and uses the response TTL
by default (`app/dns/dns.go:90-160`); `timeoutMs` defaults to 4000 ms. A single nameserver with
`disableFallback: true` gives one deterministic path; `enableParallelQuery` can query A/AAAA in
parallel. In the local fixture, first queries were 26–79 ms (TLS/HTTP2 setup included) and
cached answers were 1–2 ms; these numbers are not an internet latency claim. Recommended
defaults for a first implementation: cache on, serve-stale off, single resolver, bounded
timeout (suggest 3000–4000 ms), no cross-resolver retry. A total DNS budget and whether to
retry once on a fresh connection are user decisions.

**System DNS.** `/etc/resolv.conf` hash was unchanged before/after every scenario
(`c33c13…f77c`) and the TUN config has `autoSystemDnsToGateway: false`. The switch must not
rewrite `/etc/resolv.conf`, `resolvectl`, `nsswitch.conf` or the desktop proxy.

## 3. Architecture gap: the current engine will send an unapproved resolver direct

The runtime test used a local SOCKS5 fixture in place of the product engine, so it proved the
TUN service can carry DoH to a SOCKS exit. It did **not** prove the product path. In the
current product, the TUN service hands TCP to the engine's loopback SOCKS listener; the engine
then applies `targeted`/`all-proxy`/`all-direct`. A DoH resolver IP that is not in the approved
list is an ordinary unapproved destination and is sent direct under `targeted` (A02/A03). A
resolver **hostname** can also be resolved by the direct path. Consequences:

- `all-proxy`: the resolver connection naturally uses the pool.
- `all-direct`: it naturally uses direct, contradicting “DNS via pool”.
- `targeted`: it uses the pool only if the resolver is approved; otherwise the switch silently
  does not do what its name says.

Therefore implementation needs one of:

1. **Engine-owned DNS egress:** a small DNS proxy in the engine uses the pool dialer directly
   (bypassing the approved-destination filter) with explicit fail-closed semantics, and the TUN
   service forwards captured DNS to it over loopback. This keeps policy, exit selection and
   D21 in one owner.
2. **Dedicated DNS SOCKS endpoint:** a second loopback-only, protected listener in the engine
   that always uses the pool for resolver dials; the TUN service's DoH outbound points there.
   This is a new local listener and needs its own peer protection.
3. **Require the resolver in the approved list:** no new engine path, but it changes the
   policy meaning, fails in `all-direct`, and makes the switch dependent on a rule. Not
   recommended.

This is the main “before implementation” decision. Without it, a `dns_via_pool` switch would
be misleading in `targeted` and impossible in `all-direct`.

## 4. Switch, CLI and D28 boundary

**Recommended data model.** Add an additive boolean `dns_via_pool` to `contract.Config`
(default `false`, always emitted, schema version stays 1; same pattern as `tun_enabled`,
`internal/contract/state.go:18-31`). It is a wish, not proof of the active path. Add the
resolver URL as a separate text setting (e.g. `dns.resolver`) only if the user chooses DoH.
Old config and old backups without the key decode to `false`; backup/restore already carries
the config file.

**CLI (text; D28).** Keep resolver URL/type changes in the CLI:
`routelatch config set dns.resolver https://<ip>/dns-query`, `routelatch config get`, and a
small explicit toggle such as `routelatch tun dns on|off|status` (or
`config set dns_via_pool true|false`). Validation must reject unknown schemes, hostname-only
bootstrap without a pin, non-loopback/unsafe values and credentials in URLs. The TUN service's
root-owned config must not accept an arbitrary resolver/destination; if the resolver is part of
that config, it belongs in `tun setup`/root-owned provisioning, not in the unprivileged engine.

**Tray (D28).** The tray may show `DNS: Direct` / `DNS: Pool (DoH)` / `DNS: Pool unavailable`
and a toggle, after a two-step confirmation. It must not show the resolver URL, a query, a
domain, a destination or an exit name in a label, tooltip, notification or log. The full text
configuration stays behind `Terminal menu…`/CLI. This matches D28’s “text operations stay in
CLI; tray is a thin client”.

**Default and lifecycle.** Default `false`; switch off must be byte-for-byte phase one (direct
DNS, no extra listener). If the switch is on while TUN is off/stopped, store the wish but
report `dns=direct` plus an actionable warning such as `tun_dns_pool_requires_tun`; do not
start a listener or change system DNS. On `tun off`/Quit/`mode stopped`, close the DNS path
with TUN. On `tun on`, either apply the stored desire or report a clear pending/error state.
The current TUN service starts a fresh Xray core per enable (R-4) and reads a root-owned fixed
config (`internal/tun/config.go:66-100`); changing a switch while TUN is already on cannot be
done by editing Xray's routing in place. Options: (a) disable/re-enable TUN, documented, which
has the measured fail-open window of the service restart (R16-H: 0.605–1.390 s); (b) an
engine-owned DNS forwarder that can be toggled without restarting the TUN core; (c) a new
validated service command that rebuilds only the DNS core. Option (b) is the least disruptive
but needs the engine DNS proxy from §3. This is a user/design decision; do not hide a TUN
restart or silently keep the old path after the switch changes.

**Status contract.** Today `status.tun.dns` is pinned to `direct` and any other value is
normalized back (`internal/control/ops_tun.go:120-135`, `internal/contract/tun.go:31-41`).
A switch needs a new contract section: a `direct|pool` actual value (and possibly a separate
`desired` value), a stable warning/reason such as `tun_dns_pool_unavailable`, and no resolver
address, domain, query or exit in status. Do not overload the existing field with an unknown
value or hide a failed switch behind `direct`.

## 5. D21 and trust boundary

| Surface | What exists | Result/decision |
| --- | --- | --- |
| Xray/TUN log | TUN handler logs source→destination at info/debug (`proxy/tun/handler.go:246`); DoH DNS logs the queried name at info (`nameserver_doh.go:134-135`); proxy/dns logs queries in some paths. | Service pins `logLevel/access/error: "none"`; no user log-level knob. Runtime scan of A's product-like paths (`/work/a-logs`, `/etc/resolv.conf`, `/run`) found **0** canary matches. The source caveat remains: a debug build would log destinations/names; the “no destination even at debug” guarantee is not met by stock Xray. |
| State/backup | No DNS cache/query store is planned; config holds only the boolean/resolver setting. | Keep cache in memory only; never persist queries or domains. Backup includes the switch value, not query history. |
| Status/tray | `status.tun` and tray labels carry state only. | New `dns` value and warning must be fixed enum/text; no resolver URL/domain. |
| Exit (SOCKS/Freedom/… ) | DoH: sees resolver IP/SNI, TLS record sizes/timing, not query names. DNS-over-TCP: sees plaintext query/answer. | In the test, the SOCKS fixture saw a TLS ClientHello for DoH and the canary name for TCP. The exit is not a DNS-trust boundary for DoH content, but it is for metadata and for plain TCP. |
| Resolver | Sees every query name, qtype, timing and the exit's source IP; may log/retain/use ECS. | The resolver is the real trust boundary. A public resolver is a third party; a private resolver may be the user's own. Do not ship a silent default or claim anonymity. If `client_ip`/ECS is configured, document it; default none. |
| Destination SNI | The exit already sees the destination SNI/Host for proxied TLS/HTTP, independent of DNS. | DNS-through-pool hides names from the local resolver/network; it does **not** hide the destination from the exit. For unapproved destinations in `targeted` + all-queries (option A), it actually adds a leak of those names to the exit. State this in the user-facing docs. |

The tested DoH path also proves the exit-side source: the local DoH server saw `172.31.77.20`
(the exit container), not `172.31.77.10` (the client), so the resolver sees the exit, not the
host. This is the intended “exit only sees resolver” property for DoH content; it is not a
claim about the resolver's own logging.

## 6. Compatibility

| Case | Behaviour with switch off | Behaviour with switch on | Status |
| --- | --- | --- | --- |
| Phase-one direct DNS | Direct DNS/53 v4/v6 UDP/TCP; system DNS unchanged | N/A | **PASS** in this harness and H (`H-ACCEPTANCE.md` DNS row); off-path is unchanged. |
| System DNS (`/etc/resolv.conf`) | Unchanged | Unchanged; no `resolvectl`/systemd-resolved edits | **PASS** (hash identical). |
| systemd-resolved / `127.0.0.53` stub | Queries go to loopback and are not TUN-captured | Switch has **no effect**; the stub's upstream is outside TUN | **Not covered.** To cover it we would have to change system DNS or integrate with resolved, both outside D15/D29. |
| Captive portal / local resolver | Works through the local resolver | Captured queries may go to the pool; portal/local names may not resolve; no direct fallback | **Not tested.** Document: turn the switch off for captive portals; do not add a silent fallback. |
| Split-horizon/private zones | Work if resolver is local | May break; a public DoH resolver cannot see private zones | Not tested; same limitation. A domain-exception list would be a separate policy feature. |
| Browser DoH | Browser's TCP/443 follows mode/policy | Unchanged by this switch; the browser resolves its own names | Source-level; not tested. In `targeted`, an unapproved browser DoH endpoint is direct. |
| App hostname resolution | App uses system resolver | Only captured plain DNS changes; apps using their own DoH or loopback stub are unaffected | Source-level; not tested. |
| IPv6 | DNS/53 v4/v6 direct (H addendum PASS) | DoH over an IPv6 resolver and A/AAAA through TUN **PASS** in this harness; the exit must have IPv6 reachability or the query fails clearly | Runtime local; public IPv6 exit not tested. |
| All-direct | Direct DNS | Pool DNS while traffic is direct; surprising | Decision needed; recommend warning/confirmation or refuse. |

**Important scoping statement.** “DNS via pool” here means “plain DNS packets captured by the
TUN interface are resolved through the chosen DoH resolver via the pool”. It is not a promise
that every hostname lookup on the machine goes through the pool. That would require changing
system DNS or hooking the system resolver, which D15/D29 currently forbid. The switch’s CLI
help/status must say this.

## 7. Untested items and residual uncertainty

- **Product engine path:** no run with the real engine's `targeted` router and a live pool
  exit. The local SOCKS fixture bypasses the engine policy. The explicit DNS-egress exception
  in §3 is a design requirement, not yet implemented or tested.
- **Per-exit protocols:** no runtime DoH through VLESS/Trojan/hysteria2; source says all carry
  TCP. A specific exit may block TCP/443 to the resolver, reject the resolver IP, or have no
  IPv6; that is not proven either way.
- **Public/private resolver:** no public resolver was contacted. IP-SAN certificates,
  resolver retention/ECS, and public resolver reachability through exits are unverified.
- **Hostname bootstrap:** only IP-literal DoH was tested. A hostname URL needs a pinned IP or
  static mapping; otherwise it needs system DNS or exit-side resolution, and the targeted
  policy may treat it as unapproved.
- **DoT:** no native Xray support; a custom DoT client would be new code. DoQ is local-only and
  UDP-bound. Neither is recommended now.
- **Status/CLI/tray:** no product UI was changed or tested. The proposed contract changes
  (`dns=pool`, warning, desired/actual) need review.
- **Cache/TTL:** only a fresh and a dead-pool query were measured; cache expiry, stale cache,
  negative cache, retry behavior and latency budgets are not measured at scale.
- **Systemd-resolved / captive portal / split DNS / browser DoH:** not executed; compatibility
  table is source/reasoning-based.
- **D21 at debug:** stock Xray logs query names/destinations at info/debug; the service's
  `none` level is the tested configuration. A literal “no query at any log level” needs a
  patch/filter, as already recorded by R16-A.

## 8. Source locators

Pinned Xray (module cache):
`github.com/xtls/xray-core@v1.260327.1-0.20261109052709-676eaf66f8f0`
(commit `676eaf66f8f05d4d558ee7a74b4736623d3feeb9`):

- `app/dns/nameserver.go:48-80` — scheme dispatch: `https` (remote DoH), `tcp` (remote
  DNS-over-TCP), `quic+local` (local only), no `tls`/DoT.
- `app/dns/nameserver_doh.go:40-110` — DoH HTTP/2 client, dispatcher dial, uTLS;
  `:134-135` query-name log; `:228-235` POST request.
- `app/dns/nameserver_tcp.go:33-60` — TCP nameserver dispatcher dial.
- `app/dns/nameserver_quic.go:39` — QUIC constructor takes no dispatcher.
- `app/dns/dns.go:90-160` — nameserver/cache/serve-stale configuration;
  `:309-380` — `sortClients`, `disableFallback`/`skipFallback` behavior.
- `proxy/dns/dns.go:141-176,255-274,313` — DNS outbound hijack path, query parsing and
  per-query `LookupIP`.
- `proxy/tun/handler.go:246` — info-level source→destination log.
- `common/net/address.go:79-102` — non-IP strings become domain addresses; explains why
  `tls://` is accepted but not a DoT client.
- `spike/tun/FINDINGS.md` §3 and `spike/tun/evidence/05-dns-udp.txt` — R16-A direct DNS,
  `autoOutboundsInterface=auto`, system DNS unchanged.
- `spike/tun/H-ACCEPTANCE.md` DNS row and IPv6 addendum — phase-one direct DNS acceptance.

RouteLatch:

- `internal/tun/xrayconfig.go:90-130` — fixed TUN document: sniffing, `autoOutboundsInterface:
  auto`, `autoSystemDnsToGateway: false`, port-53 direct rule, TCP→SOCKS, non-DNS UDP block.
- `internal/tun/config.go:66-100` — root-owned service config has no interface/route/domain/
  policy input; only allowed uid and loopback SOCKS target.
- `internal/contract/state.go:18-31` — additive config pattern and `tun_enabled`.
- `internal/contract/tun.go:31-41,96` — `dns`/`udp` are fixed phase-one values.
- `internal/control/ops_tun.go:120-135` — status normalization pins `dns=direct`.
- `internal/frontend/socks.go:70-77` — UDP ASSOCIATE/BIND refused with `0x07`.
- `internal/egress/egress.go:1-120` — engine dials/resolver bind to the physical interface
  while TUN is active.
- `docs/requirements.md` D15/D29/D30/A17; `docs/architecture.md` §9; `docs/contracts.md`
  §10.2, §10.5, §10.7; `docs/implementation-plan.md` R16-I.
- `internal/singbox/doc.go:1-12` — sidecar has no DNS section and preserves destination
  hostnames.

## 9. Exact user decisions needed before implementation

1. **Policy scope:** A) all TUN-captured queries through the pool when the switch is on,
   independent of `targeted`/`all-proxy`/`all-direct`; B) approved domains only; or C)
   mode-coupled. Recommendation: A for the first switch, with warnings; B only if targeted
   consistency/exit privacy is required. Note that A reveals unapproved names to the exit in
   `targeted`.
2. **`all-direct` + switch on:** allow with a confirmation and warning, refuse it, or force
   the switch off. Recommendation: allow only with an explicit warning, because D30 says the
   switch is independent; do not silently ignore it.
3. **Resolver/trust:** choose the DoH resolver policy: user-supplied only (no default),
   public default, or private resolver with a user-provided CA/pin. Recommendation:
   user-supplied IP-literal DoH URL, no built-in public default, no credentials, TLS
   verification always on. This decides who sees every name and whether a private CA is
   installed.
4. **Engine egress exception:** approve an engine-owned DNS proxy/dedicated loopback DNS
   egress that always uses the pool and fails closed (required for `targeted` and
   `all-direct`), or restrict the switch to `all-proxy`. This is the main implementation
   boundary; without it the switch is not truthful in `targeted`.
5. **Apply mechanics:** allow a live switch via an engine-owned DNS forwarder, or require
   `tun off/on` and accept the measured TUN restart fail-open window; define the status
   `desired` vs actual behavior and the warning text.
6. **Coverage wording:** accept that systemd-resolved/loopback stubs, browser DoH, captive
   portals and split DNS are outside the switch unless D15/D29 changes. Recommendation:
   accept and document; do not change system DNS.

## 10. Evidence index

Directory `spike/tun/evidence/r16i-2026-11-27/`:

- `00-versions.txt` — kernel/OS/Xray/iproute2.
- `01-peer-setup.txt` — local DoH/TCP-DNS/SOCKS/direct-DNS listeners.
- `02-client-setup.txt` — client routes, CA, system resolver hash.
- `03-config-parse.txt` — DoH/TCP/DoT/DoQ parse matrix; runtime `tls://` behavior.
- `04-direct-control.txt` — phase-one direct A/AAAA/IPv6/UDP/TCP; direct resolver source;
  system DNS negative check.
- `05-doh-pool.txt` — DoH A/AAAA/IPv6-destination/TCP-client; HTTP/2 DoH log; SOCKS target
  and TLS first bytes; `direct_hits=0`; system DNS hash; D21 canary `matches=0`.
- `05b-doh-ipv6.txt` — IPv6 DoH upstream; SOCKS ATYP=4; `direct_hits=0`.
- `06-dns-tcp-pool.txt` — DNS-over-TCP through the exit; plaintext canary in the exit's
  first bytes; `direct_hits=0`.
- `06b-dot-negative.txt` — `tls://` query timeout; no 853 listener/connection.
- `07-dead-pool.txt` — dead pool timeout; no direct query for the dead name; recovery.
- `08-repro.txt` — generated configs and fixture source pointers.
- `99-final-cleanup.txt` — no host TUN/route/container/network left.
- Harness: `spike/tun/r16i/run.sh`, `spike/tun/r16i/dnssrv/main.go`,
  `spike/tun/r16i/socks_peek.py`.
