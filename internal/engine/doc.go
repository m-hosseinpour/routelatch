// Package engine is RouteLatch's adapter to the pinned Xray core. It owns the
// process's single core instance, the exit table and the per-tag dial that
// carries a proof class.
//
// # Boundary
//
// The core is started once from a configuration this package generates from
// typed Record fields. It has no routing app, no balancer, no DNS app, no API,
// no environment, no inbound, no policy, no stats and no direct/freedom
// outbound. The only root keys are "log" and "outbounds", every exit has an
// explicit stable tag (rl-<recordID>), and the first outbound is a blackhole so
// the dispatcher's default handler fails closed (spike/xray/FINDINGS.md §6).
// Imported share links never reach the core as configuration.
//
// # Proof classes
//
// VLESS and Trojan travel through the core and always return ProofAccepted:
// core.Dial returned before the exit was contacted and the core exposes no
// positive destination event, so nothing stronger is honest (§2.1–2.3).
// SOCKS5 exits are dialed by this package's own thin client, which parses the
// exit's reply: 0x00 is ProofConfirmed, 0x05 is DestinationRefused, and every
// other code is HandshakeRejected. A hostname is sent unresolved (ATYP=0x03).
// HTTP CONNECT exits are outside v1 and are refused, not implemented.
//
// # Failures
//
// Failures before dispatch — no exit selected, unknown or removed exit, stale
// policy version, unsupported record — are typed *contract.Failure values and
// never open a socket. Once a core-backed conn exists, its Read and Write
// surface the honest kind: our cancel (Canceled), a caller deadline (Timeout),
// a tracked core transport error (ExitUnreachable), or a stream that ended
// before any downlink byte without attributable evidence
// (DestinationUnconfirmed). A destination refused connection, a DNS failure, a
// reset and a clean FIN all collapse into DestinationUnconfirmed on the core
// path because the pinned core cannot tell them apart (T1 §3).
//
// # Lifecycle
//
// There is no in-process restart. Stop closes the connections this package
// returned and then the core. gRPC and XHTTP keep warm transport state that the
// pinned core cannot close and that survives Stop; WarmTransports reports those
// exits so a mode change can show them as "engine transports, outside the
// registry". Only a process restart clears them (§2.7, R-4).
//
// Two known limits, both documented rather than papered over:
//
//   - The core path has no observable dial phase: a deadline on the session
//     context would become a tunnel deadline, which §3.4 forbids. Only the
//     SOCKS5 adapter, whose handshake blocks, applies PoolDialDeadline.
//   - The core's logger is process-global and the log app writes warnings to
//     stdout unless the service installs its own handler. The engine emits no
//     credentials and disables access logging, but log wiring belongs to the
//     service.
//   - The conn returned for a VLESS/Trojan exit is the core's cnc.Connection,
//     which accepts SetDeadline/SetReadDeadline and ignores them (T1 §0); the
//     SOCKS5 adapter conn honors them. Teardown on both paths is Close/context,
//     never a read deadline, which matches §3.4's "no read deadline on a user
//     tunnel". A future prober must not assume the core path can time out a
//     read.
//
// Teardown order: the service should close the registry before calling Stop so
// client-facing copy loops observe Canceled; Stop itself also closes every
// connection the engine returned (and an exit socket still inside a SOCKS5
// handshake), then the core.
package engine
