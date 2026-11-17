// Package conns implements contract.Registry: the single owner of live user
// connections and of dials that have not completed yet.
//
// The registry exists so that a mode or rule change can close exactly the
// connections the new policy no longer allows, before that change is reported
// as successful (D13, docs/contracts.md §2.6). It therefore owns three things:
//
//   - The policy-version fence. Register is atomic with a sweep: once a new
//     version is committed, a spec whose Decision.PolicyVersion is older is
//     rejected with Kind Canceled and is never repaired to the new version.
//   - The keep/close decision. CloseConflicting asks the caller's keep
//     predicate — the only acceptable reason to keep a connection — and closes
//     everything else, including dials that have not returned yet.
//   - Teardown evidence. A sweep does not return until every connection it
//     closed has both sockets closed and its dial context canceled. The local
//     side is bounded by contract.LocalCloseDeadline; the remote exit's own
//     observation is outside our control (docs/contracts.md §3.4).
//
// What this package deliberately does not do: it does not dial, route, rank,
// decide a proof class, read a byte of tunnel traffic or count bytes. The
// frontend owns the copy loops and registers each connection before it starts a
// dial; v1 reports no per-connection byte counters (docs/contracts.md §7.1).
package conns
