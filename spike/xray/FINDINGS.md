# T1 unit A — embedded Xray boundary and handshake gate

**Status: blocked on the handshake gate (b).** The other five questions were
measured. The exact blocking question and the options with their costs are in
[§9](#9-gate-decision). This document records what was **implemented**,
**source-inspected**, and **runtime-observed**; it does not claim more than the
loopback runs below.

*Pinned core:* `github.com/xtls/xray-core v1.260327.1-0.20261109052709-676eaf66f8f0`
(commit `676eaf66f8f05d4d558ee7a74b4736623d3feeb9`, tag `v26.11.9`, 2026-11-09).
Go 1.27.4. One command: `spike/xray/run.sh`.

No service was installed, no system/desktop proxy or DNS setting was changed,
no public exit was used, and no credential was committed. UUID/password/TLS key
are generated per run; the self-signed TLS certificate lives under `/tmp` and
the client pins its leaf hash (TLS verification is not disabled).

## 0. Pin and build

| Claim | Command | Observed | Limitation / status |
| --- | --- | --- | --- |
| `v26.11.9` cannot be used as a module version | `go get github.com/xtls/xray-core@v26.11.9` | `invalid version: module contains a go.mod file, so module path must match major version ("github.com/xtls/xray-core/v26")` | The tag/`go.mod` mismatch is upstream; runtime-observed. |
| The `v26.11.9` commit is consumable as a pseudo-version | `go get github.com/xtls/xray-core@676eaf66f8f05d4d558ee7a74b4736623d3feeb9` | resolves `v1.260327.1-0.20261109052709-676eaf66f8f0`; build prints the same version | Go's pseudo-version form on the last valid `v1.260327.x` line plus the commit time. Pin in `go.mod`; runtime-observed. |
| The JSON/handler stack needs the distribution registration | first run | `common: *proxyman.InboundConfig is not registered` until `main/distro/all` is imported | `infra/conf/serial` alone builds configs but does not register handlers. Runtime-observed. |
| `core.StartInstance` signature in this commit | source read | `StartInstance(configFormat string, configBytes []byte)`; the code uses `serial.LoadJSONConfig` + `core.New` + `Start` | Recorded for the adapter; avoids relying on a format loader that may not be registered. Source-inspected. |
| `allowInsecure` is removed | config build (source read) | `allowInsecure` returns a removed-feature error; `pinnedPeerCertSha256` is the supported form | Test TLS uses leaf pinning. Source-inspected in `infra/conf/transport_security.go`; the configuration was not run with the rejected field. |
| Freedom blocks private destinations by default | exit log | `proxy/freedom: target blocked by a finalrule: tcp:127.0.0.1:…, blackholing connection for 31s` for a VLESS inbound whose freedom outbound has no allow rule | Test exit adds an explicit `finalRules` allow for `127.0.0.0/8` (fixture exception). This block is server-side and invisible to the caller; source-inspected `defaultBlockPrivateRule` + runtime log. |
| `cnc.Connection` ignores deadlines | source read | `SetDeadline`, `SetReadDeadline`, `SetWriteDeadline` return `nil` and store nothing | A frontend cannot put a read/write timeout on the conn returned by `core.Dial`; it must close the conn or race a goroutine. Source-inspected (`common/net/cnc/connection.go:148–160`), used by the harness. |

## 1. (a) Does a successful pool dial mean the destination connected?

| Claim | Command | Observed | Limitation / status |
| --- | --- | --- | --- |
| `core.Dial` returns before the destination is contacted | `./run.sh` §A1 | Dial returned in **22.7 µs** with `err=nil`; the destination peer accepted **502.05 ms later**; `"ping"` echoed at 502.89 ms | The 502 ms is VLESS's own wait-for-first-payload/header delay, not a readiness wait. Runtime-observed. |
| A dial can “succeed” with a completely dead exit | §A2, fake exit accepts TCP and never speaks VLESS | Dial `err=nil` in 33.8 µs; tracked errors **none**; destination contacts **0**; a read blocked until the harness's 800 ms timeout (800.99 ms); the fake exit saw exactly one TCP accept | The client has no way to tell this from a slow destination. Runtime-observed. |
| A server-side rejection is not a Dial error | §A4, exit with a different UUID | Dial `err=nil`; read `EOF` at 503.54 ms; tracked errors none; destination contacts 0 | Auth rejection and destination failure look the same (EOF). Runtime-observed. |
| A missing outbound tag is not a Dial error | §A5 | Dial `err=nil`; read `EOF` at 151.4 µs; tracked errors none; destination contacts 0; dispatcher log `non existing tag … ghost` | Same shape as a dead exit, but faster and without a transport attempt. Runtime-observed. |
| Direct and pool paths are independent | §A6 | forced `direct` tag: echo `"ping"` in 1.53 ms; destination accepted; no pool involvement | The direct outbound is a separate tag in the same core; it works while pool tags fail. Runtime-observed. |

**Answer (a): no.** A pool dial's return value is not evidence of a destination
connection. It is evidence only that the dispatcher accepted a request.

## 2. (b) When may a CONNECT `2xx` / SOCKS5 `0x00` be sent?

| Claim | Command | Observed | Limitation / status |
| --- | --- | --- | --- |
| There is no positive “destination connected” event in the public API | source read of `core.Dial`, dispatcher, outbound handler, `session` | `session.TrackedConnectionError` is **error-only** (`SubmitError`); no success/connected callback exists; `core.Dial` returns a `cnc` pipe immediately | A frontend can learn about some failures, never about success. Source-inspected across the pinned commit. |
| The VLESS response header is encoded before dispatch but **buffered** | source (`proxy/vless/inbound/inbound.go:609–631`, `common/buf/writer.go`) | `EncodeResponseHeader` writes into a `buf.BufferedWriter`; `SetFlushNext` flushes on the next downlink `WriteMultiBuffer`, i.e. with destination data after dispatch | It is not a wire-level ack of the request; it is effectively a first-byte signal, which is rejected for client-first protocols. Source-inspected; this corrects an earlier draft that called it a pre-dispatch wire ack. |
| Xray's SOCKS server replies success before dispatch | source (`proxy/socks/protocol.go:221`, `server.go:157`) | `writeSocks5Response(statusSuccess)` precedes `DispatchLink` | A SOCKS reply from an Xray exit is not destination confirmation. Source-inspected; runtime §E (`socks5-xray-exit`: refused destination → `EOF`, no tracked error). |
| A conforming SOCKS server can carry destination status | §E `socks5-conforming-exit` | refused destination → tracked error at **1.22 ms**: `proxy/socks: server rejects request: 5`; success dial still returned **1.26 ms before** the destination accept | Even with a conforming server, the **success** is internal to `Process`; `core.Dial` exposes no positive event. Runtime-observed. |
| First-byte gating deadlocks ordinary CONNECT clients | protocol argument + §A2/§E reads | a CONNECT client sends its request only after 2xx (e.g. TLS ClientHello); the destination echo sends nothing until it receives; waiting for destination bytes before 2xx never completes | This is why first-byte readiness is not a general substitute. Not implemented as a product heuristic. |
| The only implementable early point is `core.Dial` return; “exit accepted” is not observable | §A1–A5 | Dial returns in µs; the peer may be a dead TCP acceptor (A2), a rejected UUID (A4), or a live exit with a refused destination (B1); later failure is indistinguishable from a peer close | Early 2xx at Dial return changes the client-visible guarantee and is exactly what the T1 gate forbids unless the user approves it. |

**Answer (b): no defensible moment with the stock core for the primary pool
protocols (VLESS/Trojan).** For VLESS/Trojan the library surfaces neither a
destination-connected event nor a destination-failure error; for SOCKS the
server's success reply is emitted before the destination connect; and waiting
for destination bytes breaks client-speaks-first protocols.

## 3. (c) Which causes are distinguishable at the caller?

All rows below are VLESS/RAW through a healthy local exit unless noted. “Tracked”
is the error delivered through `session.TrackedConnectionError`; “read” is what
the returned conn does.

| Cause | Dial | Tracked error | Read | Status |
| --- | --- | --- | --- | --- |
| Exit TCP refused (§A3) | `nil` | yes, at **2.006 s**: `failed to find an available destination … connection refused` | `io: read/write on closed pipe` at 2.007 s | runtime-observed |
| Caller deadline during pending tunnel (§A7) | `nil` | yes, at **401.47 ms**: `connection ends > context deadline exceeded` | closed pipe at 401.51 ms | runtime-observed; this is a caller-side timeout, not an OS TCP connect timeout |
| Exit auth rejected (§A4) | `nil` | none | `EOF` at 503.54 ms | runtime-observed |
| Destination refused (§B1) | `nil` | none | `EOF` at 1.510 s | runtime-observed |
| Destination DNS failure (§B2, `no-such-host.invalid`) | `nil` | none | `EOF` at 1.523 s | runtime-observed |
| Destination accepts then FIN (§B3) | `nil` | none | `EOF` at 1.525 s | runtime-observed |
| Destination accepts, then FIN after data (§B4) | `nil` | none | `EOF` at 1.025 s | runtime-observed |
| Destination resets after data (§B5, `SO_LINGER=0`) | `nil` | none | `EOF` at 621.34 ms — **no `ECONNRESET` at the caller** | runtime-observed |
| Caller cancels ctx after a working connection (§B6) | `nil` | none (EOF is filtered) | `EOF` at **2.98 ms**; destination EOF observed | runtime-observed |
| Missing/blackhole tag (§A5/A5b) | `nil` | none | `EOF` / closed pipe in <250 µs | runtime-observed |
| Conforming SOCKS exit, destination refused (§E) | `nil` | yes at **1.22 ms**, SOCKS reply code 5 | closed pipe at 1.35 ms | runtime-observed |

Consequences:

* **Transport-level failures** that were actually run — exit TCP unreachable and
  caller deadline — are distinguishable through the tracked-error channel. A TLS
  handshake/pin failure is expected to use the same path (source-inspected) but
  was **not run**; do not count it as measured.
* **Destination-level failures** for VLESS/Trojan all collapse to `EOF` or a
  silent wait. The caller cannot distinguish refused, NXDOMAIN, clean FIN, or
  RST-after-write. A reset is not surfaced as a reset.
* A **delay before `EOF`** carries some information (retry/backoff timing), but
  it is a timing heuristic, not a cause. It is not used as one.
* Xray's own SOCKS exit also collapses destination failures (reply-before-connect).
  A conforming SOCKS server is the only tested protocol that delivers a typed
  destination failure, and only if the exit server is conforming.

**Answer (c): partial.** Transport-level errors are usable; destination-level
causes are not distinguishable on the VLESS/Trojan pool path, which limits A05
diagnostics (a direct-path probe can still classify its own errors).

## 4. (d) Teardown and residue

Command: `./run.sh` §D. Ten independent pooled dials, then close; one pending
dial against a silent exit; one cancel-only after a working dial.

| Measurement | both default | client fast, exit default | client default, exit fast | both fast (`RL_SPIKE_FAST_TEARDOWN=1`) |
| --- | --- | --- | --- | --- |
| close → destination-observed EOF, 10 samples | **1.001–1.003 s** (mean 1.002) | **1.001–1.003 s** (mean 1.002) | **2.48–3.01 ms** (mean 2.75) | **2.46–3.07 ms** (mean 2.88) |
| pending dial (exit TCP accepted, no proxy exchange) → exit-observed close | 2.41 ms | 2.34 ms | 2.80 ms | 2.45 ms |
| cancel-only (ctx) → destination-observed EOF | 1.003 s | 1.003 s | 3.07 ms | 3.01 ms |

The ~1 s default is Xray's `uplinkOnly`/`downlinkOnly` one-way grace (default
1 s in `features/policy`), not a socket leak. **The exit's policy is decisive
for the destination observation**: making only the client fast does not change
the 1.00 s; making only the exit fast drops it to ~2.7 ms. A public exit (which
we do not control) normally keeps the default ~1 s grace, so a mode change
cannot guarantee millisecond-fast teardown of the exit-side destination socket.
Closing the user conn does prevent further user data, but the exit-side socket
can outlive the close by its policy. The caller's own read is unblocked
immediately by close/cancel (B6: 2.98 ms); the 1.00 s rows are the
*destination's* observation at the exit, not a wait on our side.

Residue (VLESS/RAW; `/proc/self/fd`):

| Phase | goroutines | fds | sockets |
| --- | --- | --- | --- |
| process baseline | 2 | 6 | — |
| client core + harness | 8 | 15 | — |
| after 10 closed pool conns + 2 s settle | 8 | 23 | **3** (the three harness listeners) |
| after exits stopped, client core still up | 3 | 14 | — |
| after client core `Stop` | **2** | 14 | **0** |

No socket fds and no goroutines attributable to the core remained after stop.
The residual fds are pipes/`anon_inode` from the child-process plumbing; the
harness, not Xray, owns them. There is no evidence of a socket or goroutine leak
from the **VLESS/RAW teardown path**. This is not a general claim: the gRPC cell
leaves 3 process-global goroutines after `core.Stop` (§5), so the no-residue
result is per-transport and excludes gRPC/XHTTP. The test process was isolated
per section (`all` spawns `gate`, `transports`, `teardown`), so baselines are
not polluted.

**Answer (d): yes, with measured latency.** We initiate close/cancel and the
connection tears down. The exit's policy is decisive: client-fast/exit-default
stays at ~1.00 s, while exit-fast alone drops the destination observation to
~2.7 ms. A public exit's default ~1 s grace cannot be shortened by our client
configuration.

## 5. (e) Can our frontend hold and close conflicting connections?

| Claim | Command | Observed | Status |
| --- | --- | --- | --- |
| Every user connection is a `net.Conn` we own; closing it tears down | §D | pending close → fake-exit EOF in 2.45 ms; working close → destination EOF in ~1 s (policy) / ~2.7 ms (fast policy) | runtime-observed |
| VLESS/RAW, TLS, WS/TLS, Trojan/RAW: one transport per user connection, no hidden pool | §E, transparent counting hop in front of the exit | 3 user dials → **3** transport accepts; **0** open after close at 2.5 s; 3 destination EOFs | runtime-observed |
| gRPC/TLS reuses one transport connection and keeps it open | §E | 3 user dials → **0** new transport accepts (reuse); **1** transport conn still open 2.5 s after all user conns closed; 0 forwarder closes | runtime-observed |
| XHTTP/TLS multiplexes user streams and keeps transport connections open | §E | 3 user dials → **1** new transport accept; **3** active 2.5 s after close (1 new + 2 warm from earlier tests); 0 forwarder closes | runtime-observed |
| `core.Stop` leaves process-global gRPC dialer state | §E + source | 2 goroutines before the gRPC cell, **5 after its `core.Stop`**, and 5 persists into every later cell in the same process; gRPC and XHTTP keep package-level `globalDialerMap`s (`transport/internet/grpc/dial.go`, `splithttp/dialer.go`) | runtime + source-inspected; restarting the core does not clear the maps. |
| The outbound manager exposes handler `Close()`, but it does not own the global transport maps | source read | `outbound.Manager.ListHandlers`/`GetHandler(tag)` and `Handler.Close()` exist; `Handler.Close` closes the proxy/mux, not the package-level dialer maps | Not tried at runtime. The earlier “no public close hook” claim was too broad; the practical warm-pool conclusion still holds. |

**Answer (e): partial.** For per-connection transports we hold the connection
and close it; for gRPC/XHTTP the core keeps warm transport connections that we
cannot close via the public API. A mode change must therefore (i) close all user
conns, (ii) bind each policy generation to a distinct outbound tag so a warm pool
cannot be reused for a new decision, and (iii) tolerate the idle transport
sockets or isolate the core in a fresh **process**. Restarting `core.Instance`
in the same process is not sufficient for gRPC/XHTTP because the global dialer
maps survive `Stop`; `handler.Close()` may close handler-owned state but was not
tested and does not clear those maps (source). This is a concrete D13 cost, not a
hidden direct-path violation.

## 6. (f) No direct fallback on a missing/dead pool tag

| Case | Dial | Tracked | Destination contacts | Result |
| --- | --- | --- | --- | --- |
| forced tag missing (`ghost`) | `nil` | none | **0** | `EOF`; no direct or default outbound was used (§A5) |
| routing rule names a missing tag (`ghost`) | `nil` | none | **0** | `EOF`; dispatcher logs `non existing outTag: ghost` and closes; no default (§C) |
| routing rule does not match (default handler is direct) | `nil` | none | **1** | `"ping"` works; **the default handler was used** (§C) |
| empty core balancer (selector matches no outbound) | `nil` | none | **1** | `failed to pick route … balancing strategy returns empty tag`; dispatcher falls through to the **default handler**, which was direct in this config (§C) |
| pool exit TCP refused | `nil` | typed error at 2.0 s | 0 | error path, no direct (§A3) |
| direct tag, explicitly selected | `nil` | — | 1 | direct works independently (§A6) |

**Answer (f): safe only when the adapter forces an explicit pool tag.** A forced
or routing-selected tag that has no handler closes the link and does **not**
fall back to direct (destination contacts 0). But the dispatcher's default-handler
path is real: a routing miss or an **empty core balancer** falls through to
`GetDefaultHandler()`; with direct first, that silently reaches the destination.
So the product must (i) do its own pool selection and force the selected tag,
(ii) never make a direct/freedom outbound the default in a way a pool-path dial
can reach, and (iii) not use the core balancer without an explicit reject
fallback. The engine does not provide a typed error for a missing tag either:
`core.Dial` returns `nil` and the read gives an immediate `EOF`, so the adapter
must pre-check tag existence/health and must not map `EOF` to success.

## 7. Transport matrix actually exercised

All cells below were run end-to-end against loopback exits; TLS cells pin the
self-signed leaf; no `allowInsecure`:

| Transport | Exit/inbound | Readiness event observed | Dial → destination accept | Destination refused |
| --- | --- | --- | --- | --- |
| VLESS / RAW TCP | Xray VLESS | none | 501.8 ms | `EOF`, no tracked error |
| VLESS / TLS | Xray VLESS + leaf pin | none | 509.1 ms | `EOF`, no tracked error |
| VLESS / WS over TLS | Xray VLESS | none | 505.2 ms | `EOF`, no tracked error |
| VLESS / gRPC over TLS | Xray VLESS | none | 505.5 ms | `EOF`, no tracked error |
| VLESS / XHTTP over TLS | Xray VLESS | none | 507.3 ms | `EOF`, no tracked error |
| Trojan / RAW TCP | Xray Trojan | none | 102.6 ms | `EOF`, no tracked error |
| SOCKS5 to Xray SOCKS exit | Xray SOCKS | server replies success before dispatch | 0.51 ms | `EOF`, no tracked error |
| SOCKS5 to conforming test server | minimal RFC 1928 peer | server replies after connect; success not surfaced by core | 1.26 ms | typed SOCKS reply 5 at 1.22 ms |

The deprecation warnings are real: this version marks WS and gRPC as
“deprecated, not recommended” and points at XHTTP. XHTTP and gRPC were still
functional. The pinned version removed the old `http` and `quic` transports
(config build returns removed-feature errors).

**Untested:** REALITY/ECH, mKCP, HTTPUpgrade, XHTTP over h3/QUIC, mux/XUDP,
multiple embedded cores in one process (process-global state), TLS session
resumption, an OS-level TCP connect timeout (SYN drop; loopback-only, no packet
filter was authorized), public exits, UDP, Windows, Android/TUN, and the actual
HTTP/SOCKS frontend libraries and real browsers/clients. The absence of a
finding in those areas is not evidence of their behavior.

## 8. Harness corrections (important for trusting the numbers)

Three initial artefacts were harness bugs, not Xray findings; they were fixed
before the results above:

1. The exit child's Xray log goes to **stdout**, so the first line after start
   can be a deprecation warning, not `READY`. The readiness reader now forwards
   non-`READY` lines to stderr and waits for `READY`.
2. The transparent TCP counter/forwarder initially did not propagate a FIN from
   the exit back to the client, which made every destination failure look like a
   read timeout. It now half-closes each direction toward the other side; the
   final runs show the real `EOF`s.
3. `core.Dial`'s `cnc.Connection` ignores deadlines; the harness measures timeouts
   with a goroutine and a caller-side timer, not `SetReadDeadline`.

The per-dial first-byte/retry delays in the observations (≈0.5 s VLESS/≈0.1 s
Trojan before the exit sees the request, ≈1 s Xray retry on a refused exit) are
engine behavior and are called out where they appear; they are not used as
readiness evidence. The ≈0.5 s VLESS delay is **per new tunnel** while the
frontend withholds client bytes until 2xx; it is a readiness-gate latency cost,
not a transient startup artefact.

## 9. Gate decision

**The handshake gate cannot be proven for the primary pool protocols with the
pinned library as-is.** The rest of T1's unit A is measured above. This is a
blocked gate, not a reason to weaken D11 silently.

**Precise question for the user:** for a pool-routed CONNECT, do we

1. **Send `2xx`/`0x00` as soon as `core.Dial` returns, and close on later
   destination failure.** This is the only implementable moment, and it is weaker
   than “the exit accepted”: A2 shows a `nil` Dial with a dead exit and **0**
   destination contacts, and the VLESS response header is buffered until
   destination data. Cost: changes the client-visible contract; the T1 gate
   explicitly rejects “Dial returned” and “later textual error”; a client sees a
   successful tunnel and then a close, which is not D11's clear pool error; A05
   diagnostics remain limited to transport-level causes. It is not a silent
   direct fallback, but it does not satisfy the handshake requirement.
2. **Restrict the first release's pool to confirmation-capable exits** (a
   conforming SOCKS5 server, tested with a minimal RFC 1928 peer; HTTP CONNECT
   is not tested, or our own relay) and add a thin, version-pinned adapter that
   surfaces the handshake result. Cost: most public VLESS/Trojan subscriptions
   would be unsupported, conflicting with D07's first-version VLESS/Trojan
   scope; a custom outbound is an extra maintained surface, and the stock core
   still exposes no positive event (the adapter must own the handshake).
3. **Use destination first-byte as the readiness gate.** Cost: deadlocks
   client-speaks-first protocols (HTTP/TLS), so it is not viable for general
   CONNECT; it is also a heuristic. Not recommended.

Any gate that withholds client bytes until the destination is confirmed pays
≈0.5 s per new VLESS tunnel (≈0.1 s for Trojan) before the exit contacts the
destination. This applies to option 3, and to any readiness gate on a
VLESS/Trojan pool; it does not apply to option 1 (early 2xx lets the client
send immediately) or to a SOCKS/HTTP-confirmed pool, where the server handshake
is immediate.

If option 1 is chosen, the user is changing the handshake guarantee knowingly;
if option 2 is chosen, the pool scope/D07 decision must be revisited. Option 3
should be rejected. No option was selected in this spike.

## 10. Reproduction

```sh
cd spike/xray
./run.sh
# individual sections
#   <bin> gate | transports | teardown
# policy isolation (2x2):
RL_SPIKE_FAST_CLIENT=1 <bin> teardown   # client fast, exit default
RL_SPIKE_FAST_EXIT=1   <bin> teardown   # client default, exit fast
RL_SPIKE_FAST_TEARDOWN=1 <bin> teardown # both fast
```

`run.sh` builds `cmd/spike` and runs all sections in separate child processes.
The captured evidence from the last run is `results/last-run.txt`; the policy
2×2 evidence is `results/teardown-fast-CLIENT.txt`,
`results/teardown-fast-EXIT.txt`, and `results/teardown-fast-both.txt`. The
harness environment is:

```sh
export PATH=<repo>/.toolchain/go/bin:$PATH
export GOTOOLCHAIN=local
export GOMODCACHE=<repo>/.toolchain/modcache
export GOPROXY=https://proxy.golang.org,direct
```

Cold-cache caveat: `proxy.golang.org` redirects module zips to
`storage.googleapis.com`, which is blocked here. The warm cache used for these
runs needs no zip; on a cold cache use `GOPROXY=direct` (github.com is reachable)
rather than bypassing the block.
