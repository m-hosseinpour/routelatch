# T1 frontend spike — findings

Scope: can a ready-made HTTP/CONNECT and SOCKS5 frontend honour RouteLatch's
listener guarantees, and where must we own the code? Everything here is an
evidence probe, **not** the production frontend. Loopback sockets only; no
service, credential, DNS setting, proxy or system setting was changed. The SOCKS
probes do query the machine's resolver for `.invalid`/`localhost` names (that is
what proves the library's default resolves before the dial); no destination
outside `127.0.0.1` was dialed, and how the local resolver forwards a query is
outside this test's control.

- **Reproduce everything (one command):** `spike/frontend/run.sh`
  (`go test -count=1 -race -v ./...` with the pinned local toolchain). One test:
  `spike/frontend/run.sh -run TestConnectIsNotMITM`.
- Environment: Go `go1.27.4 linux/amd64`, `GOTOOLCHAIN=local`.
- Candidates inspected from the module cache: `github.com/elazarl/goproxy
  v1.9.4` (BSD-3-Clause) and `github.com/things-go/go-socks5 v0.1.4` (MIT).
- Status tags: **[implemented]** code in this tree, **[runtime]** observed by a
  check here, **[source]** read in the pinned source at the cited locator.

All commands below are run from `spike/frontend`, where `run.sh` sets the
toolchain and module cache. Test names are exact `-run` expressions.

## HTTP/CONNECT — `github.com/elazarl/goproxy v1.9.4`

The probes use `httpfront.New`, our hardened configuration: no registered
CONNECT handlers, `Tr.Proxy=nil`, `Tr.DialContext` = our dialer,
`DisableCompression`, `KeepAcceptEncoding`, `ConnectDialWithReq` = our dialer,
absolute-form `https://` refused.

| # | Claim | Command | Observed | Kind |
|---|---|---|---|---|
| C1 | MITM is off and no request input can turn it on. | `./run.sh -run TestConnectIsNotMITM` | The client's TLS peer certificate was byte-identical to the origin's; `Proxy-Connection`, `Upgrade: h2c` and `X-Mitm: true` changed nothing. MITM exists only when a registered `HttpsHandler` returns `MitmConnect` (`actions.go:47`; registration `dispatcher.go:268`; MITM branch `https.go:459`; the handler list is private and empty here). | runtime + source |
| C2 | CONNECT authority reaches our dialer unresolved, once. | `./run.sh -run TestConnectKeepsHostnameUnresolved` | Dialer received exactly `no-such-name.invalid:443`, was called **once**, and the client got `502` carrying the dialer's `pool unavailable` sentinel; no `lookup`/`no such host` text. | runtime |
| C3 | Absolute-form plain HTTP keeps the hostname too. | `./run.sh -run TestAbsoluteFormKeepsHostnameUnresolved` | `Tr.DialContext` received `plain-name.invalid:80`; body `plain-ok`. This works only after replacing the stock transport, which has no `DialContext` (`proxy.go:176`). | runtime + source |
| C4 | `200 Connection established` waits for the upstream dial; failure before the reply is a proxy error. | `./run.sh -run TestConnectReplyWaitsForDial` | No byte for 400 ms while the dial was blocked; `200` after release; a failing dial produced `HTTP/1.1 502` with the sentinel text and no `200` first. Source: dial at `https.go:362`, 200 at `https.go:401`. | runtime + source |
| C5 | The reply trigger is the dial return value; there is no hook to wait for a readiness proof delivered *after* it. | `./run.sh -run TestConnectReplyTriggerIsDialReturnValue` | A dialer returning one end of a `net.Pipe` that never reaches a destination still produced `200`: the value of the returned conn, not a proof, triggers the reply. Source confirms the 200 is written immediately after `connectDial` returns, before any copy (`https.go:401`). Consequence: the gate must live inside the dialer; if the adapter's proof needs client-first bytes, `ConnectAccept` deadlocks because it replays buffered client bytes only after the 200 (`https.go:401-407`). | runtime + source |
| C6 | `ConnectHijack` controls the reply but drops bytes already buffered with the CONNECT. | `./run.sh -run 'TestConnectHijackDropsEarlyClientBytes|TestConnectAcceptReplaysEarlyClientBytes'` | Hijack handler's first read was empty (`""`), later bytes were `LATER!`; the ConnectAccept control delivered `EARLY!` to the upstream. A workaround (wrap `ResponseWriter`, capture the hijacked `bufio.Reader`, replay it) recovers the bytes (`TestConnectHijackWorkaroundRecoversEarlyBytes`) but relies on plumbing outside goproxy's contract; its own early-bytes tests only cover ConnectAccept/MITM (`connect_early_bytes_test.go`). | runtime + source |
| C7 | Stock defaults violate routing policy; hardened config fixes it. | `./run.sh -run 'TestDefaultConfigUsesEnvironmentProxy|TestAbsoluteHTTPSIsFetcherPath'` | With stock `NewProxyHttpServer`, a plain `GET` went to `HTTP_PROXY` (child process observed at a sentinel listener), and an absolute `GET https://…` made the proxy open its own TLS connection (failed `500 x509…`, i.e. a fetcher path with TLS verification on). Hardened: absolute `https` → `403`. `ConnectDial` also comes from `HTTPS_PROXY` (`proxy.go:179`). | runtime + source |
| C8 | Response status/headers/body are **not** byte-for-byte on the plain-HTTP path. | `./run.sh -run 'TestPlainHTTPResponseIsRewritten|TestChunkedFramingIsRewritten'` | Origin `200 WAT` became `200 OK`; `x-custom-lower` became `X-Custom-Lower`; `Date` and a sniffed `Content-Type: text/plain; charset=utf-8` were added; body `hello` survived. Chunk extension `6;ext=yes` was dropped and the body re-chunked as one `9`-byte chunk; the trailer value survived. Header order also changed in the capture but is not asserted (Go does not promise an order). | runtime |
| C9 | HEAD, POST body and redirects are not mangled. | `./run.sh -run 'TestHeadAndPostPassThrough|TestRedirectIsNotFollowed'` | HEAD: `200`, `Content-Length: 5`, no body. POST: origin received `hello world` exactly. 302 stayed 302 with `Location: /elsewhere` (not followed). | runtime |
| C10 | Streaming is not flushed per write except for SSE. | `./run.sh -run 'TestNonSSEStreamStallsMidBody|TestSmallBinaryStreamDeltaIsBuffered|TestSSEStreamFlushes|TestChunkedBodyCompletes'` | With the origin still open, 512 of 4096 body bytes reached the client, then it stalled; a head plus a 5-byte delta stayed buffered for >900 ms. `text/event-stream` flushed immediately. An ending chunked body arrived intact (4096 `b` + `secnd`). Source: the flush branch checks `text/event-stream` or a `transfer-encoding` header (`http.go:100-106`), but the transport strips that header, so effectively SSE-only. | runtime + source |
| C11 | Idempotent requests are replayed by the stock transport. | `./run.sh -run TestPlainHTTPRetriesIdempotentGET` | Origin saw `{/one:1, /two:2}`: it closed a reused keep-alive connection after reading `/two`, and Go's transport silently retried it on a new connection; the client saw one `200`. Source: `shouldRetryRequest` (`net/http/transport.go:844-889`) + `isReplayable` (`net/http/request.go:1561-1575`). | runtime + source |
| C12 | Cancellation/teardown are not exposed by the library; owning the registry makes them work. | `./run.sh -run 'TestClientDisconnectDoesNotCancelDial|TestOwnedCancelStopsDial|TestModeSwitchClosesLiveTunnel'` | Closing the client during a dial did **not** cancel it (400 ms). `ConnectDialWithReq` receives the request but no cancellable context of its own (`proxy.go:47-49`, `https.go:223-232`), and the request context is not cancelled once the connection has been hijacked. An owned context aborted the dial and answered `502`. Closing the live tunnel from another goroutine required closing both the accepted client conn (our listener wrapper) and the dialed upstream conn (our dial registry, because the library returns no handle to it); reads then returned a real teardown error (timeouts are rejected), with goroutines `4→4` and socket fds `2→2` across five rounds. | runtime + source |
| C13 | After `2xx`, a failure can only be a teardown, never an error page. | `./run.sh -run TestAfterReplyFailureClosesTunnel` | Client read `200`, then EOF; no injected `502` text inside the tunnel. | runtime |
| C14 | goproxy has no UDP path. | `./run.sh -run TestConnectToUDPPortIsNotUDP` | CONNECT to a UDP-only loopback port gave `502` (TCP refused) and the UDP socket received no datagram. | runtime |

## SOCKS5 — `github.com/things-go/go-socks5 v0.1.4`

Details and all commands are in `socksfront/NOTES.md`; the decisive results:

| # | Claim | Command | Observed | Kind |
|---|---|---|---|---|
| S1 | By default the library resolves the name before our dialer. | `./run.sh -run TestLibResolvesDomainBeforeDial` | `socks-target.invalid:443` produced reply `05 04 …` after 2 ms and **zero** dial calls; `localhost:443` reached the dialer as `127.0.0.1:443`. Source: `handle.go:54-65` mutates `RawDestAddr.IP`. | runtime + source |
| S2 | A no-lookup resolver preserves the hostname end to end. | `./run.sh -run TestLibHostnamePreservedWithResolverHack` | `WithResolver` returning `(ctx,nil,nil)` + `WithDialAndRequest`: dial target `socks-target.invalid:443`, `RawDestAddr.FQDN` intact, `DestAddr.IP=nil`, tunnel works, 11 early bytes sent in the same write as the request arrived intact. `AddrSpec.String()` falls back to FQDN when IP is empty (`statute/addr.go:19-26`). | runtime + source |
| S3 | Success is sent only after the dial returns; error codes are heuristic. | `./run.sh -run TestLibReplyOnlyAfterDial` | No reply in 250 ms while blocked; `0x00` after release. `refused`→`0x05`, `network is unreachable`→`0x03`, otherwise `0x04` by substring matching (`handle.go:136-147`); after `0x00` only a stream close. | runtime + source |
| S4 | Default UDP ASSOCIATE succeeds and opens a UDP relay. | `./run.sh -run TestLibUDPAssociateDefaultOpensListener` | Reply `05 00 00 01 7f 00 00 01 <port>`, process UDP sockets `1→2`, and datagrams were relayed. `PermitAll` allows all commands (`ruleset.go:28-33`); `handle.go:180-224`. | runtime + source |
| S5 | With the recommended resolver, BIND/UDP are refused explicitly with no lookup. | `./run.sh -run TestLibStubResolverRefusesBindAndUDPWithoutLookup` | Domain-form BIND and UDP ASSOCIATE both got `05 07 …`; the configured resolver hook did no I/O, dials = 0, UDP socket count unchanged. This closes the default-resolver ordering gap (which returned `0x04` after a real lookup). | runtime |
| S6 | The library cannot cancel a dial, but an owned registry gives teardown. | `./run.sh -run 'TestLibBlockedDialNotCancelledByClientClose|TestLibCloseLiveTunnelFromAnotherGoroutine|TestLibFiveConnectCloseRounds'` | 300 ms after client close the dial was still blocked, the accepted conn open, `ctx.Err()=nil` (the library uses `context.Background()`, `handle.go:54`). Closing the accepted client conn from another goroutine gave both peers EOF. Five rounds: goroutines `4→4`, socket fds `2→2`. | runtime + source |
| S7 | A CONNECT-only own frontend is viable and its scope is measured. | `./run.sh -run TestMinimal` | All of: verbatim unresolved host, early data in one write, one dial on failure (`05 01`) with no fallback, cancel in ~0 s, both peers EOF on external close, `0x07` for BIND/UDP and `0x08` for unknown ATYP with zero UDP sockets, resources `4→4`/`2→2`. `minimal.go` = **390 lines (301 non-comment)**, experiment only. | implemented + runtime |
| S8 | No user/pass auth is offered. | `./run.sh -run TestLibAuthIsNoAuthOnly` | Client offering only method `0x02` got `05 ff`. | runtime |

## Recommendation

**Product library set (pin these; do not float):**

| Component | Pin | Licence | Use |
|---|---|---|---|
| SOCKS5 listener | `github.com/things-go/go-socks5 v0.1.4` | MIT | Yes, with the adapters below |
| HTTP listener | Go 1.27.4 stdlib `net/http` (`Server`, `Hijack`, `ReadRequest`) | Go | Yes; own the CONNECT and forwarding handlers |
| `github.com/elazarl/goproxy v1.9.4` | — | BSD-3-Clause | **No** — evaluated and rejected for the product; stays only as spike evidence |

`golang.org/x/net v0.50.0` is pulled only by the spike's goproxy dependency and
drops out of the product module if goproxy is not added.

**Where a ready-made library fails and we must own the code:**

1. **HTTP reply timing and early bytes.** goproxy writes the 200 as soon as the
   dial returns (C4) and offers no post-dial gate (C5); its only controllable
   path drops CONNECT-pipelined bytes (C6). Our handler must hijack, dial until
   the adapter can prove the destination/agreed alternative, then write
   `200`, then replay the buffered client bytes. Estimated **~120–180 LOC** of
   handler + tests.
2. **Plain-HTTP forwarding.** The library converts the request to a fetcher:
   it rewrites reason phrases/headers/chunking (C8), stalls non-SSE streams
   (C10), and replays idempotent requests on a pooled connection (C11). We must
   own a minimal forwarder that parses the request head (stdlib), writes an
   origin-form request to the dialed upstream, and copies the response bytes
   back verbatim, with no pool or an owned pool keyed to policy generation.
   Estimated **~250–350 LOC** + tests. (The probe proves the failure; the raw
   copy itself is not implemented here.)
3. **Connection registry, cancel and teardown.** Neither candidate exposes
   handles: a client disconnect does not cancel a dial, and neither candidate
   returns a handle to the dialed upstream conn (C12, S6). We own an accept wrapper, per-connection
   context, dial registry and `CloseAll` used by mode/rule changes.
   **~100–150 LOC**, shared by both listeners.
4. **SOCKS5 adapters.** On `go-socks5 v0.1.4`: a no-I/O resolver, a
   `WithDialAndRequest` that dials `RawDestAddr`, `WithBindHandle` and
   `WithAssociateHandle` returning `0x07`, plus the registry above and our own
   dial context. **~120–160 LOC**. Residual gaps to state in T2: reply codes
   come from the library's substring map (`0x03/0x04/0x05`, never a chosen
   `0x01`), and hostname preservation relies on the public nil-IP fallback of
   `AddrSpec.String`; a version bump must re-run S2. If those are unacceptable,
   the own CONNECT-only dispatch in `socksfront/minimal.go` is **~300
   non-comment LOC** and meets every listed guarantee (S7) — it is the fallback,
   not the recommendation, because architecture §1 prefers library protocol
   parsing.
5. **Explicit UDP refusal, loopback binding.** Both candidates accept whatever
   listener they are given and goproxy has no UDP concept; the SOCKS library
   opens a real UDP relay by default (S4). Bind `127.0.0.1` explicitly, override
   the UDP/BIND handlers (S5), and treat any future UDP work as a new gate.

**Method boundary.** "No local lookup" is evidenced by the custom dialer
receiving the hostname (C2/C3, S2) and by the default resolver failing on
`.invalid` before the dialer (S1); no system resolver was instrumented. The
handshake gate is proven only against loopback peers and sentinel dial errors —
the real Xray adapter's readiness proof is unit A's evidence, and this frontend
must not treat dial return as readiness unless that unit proves it.

## Not examined / open risks

- No real Xray dial, pool failure or public exit; all errors are loopback
  sentinels. A slow/reset/refused real pool path is T1 unit A.
- HTTP/2 listener, IPv6 destinations, TLS readiness, and replay of the
  destination handshake were not exercised. The v1 listener is HTTP/1.1 and
  loopback-only.
- Byte-fidelity forwarding is recommended, not built; the probe only shows the
  library's rewriting.
- 300–900 ms negative windows are bounded heuristics; the resource trend is
  socket fds and goroutines, not total fds (Go's splice pipe pool grows and is
  GC-reclaimed).
- `go-socks5`'s resolver stub and reply-code mapping depend on v0.1.4 internals
  documented above; re-run S2/S3/S5 before any version bump.
- goproxy's `:80` default for a portless CONNECT and its WebSocket/h2 paths were
  not tested; they are outside the chosen design.
