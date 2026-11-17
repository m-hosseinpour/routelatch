// Package contract contains the types and interfaces that RouteLatch's
// packages implement against. It is frozen by docs/contracts.md: later
// packages may add private helpers, but changing a name, a field, an error
// kind or a documented invariant here requires a contract change first.
//
// The package holds no routing, dialing, probing or persistence logic on
// purpose. It exists so that the proxy, engine, pool, diagnostics and CLI
// packages can be built and reviewed in parallel against one vocabulary.
//
// Invariants that every implementation must uphold:
//
//   - A pool dial either returns a connection with a ProofClass or a *Failure;
//     it never falls back to a direct dial and never returns ProofUnknown on
//     success (docs/contracts.md §2.2).
//   - The pool path never sends an HTTP CONNECT 2xx or a SOCKS5 0x00 before
//     the dial's proof class is known, and a ProofAccepted reply is reported
//     as "accepted, destination not confirmed" everywhere (D11, §2.2).
//   - Every user connection is registered with the policy version that
//     admitted it. A policy change must be able to close exactly the
//     connections whose route the new policy no longer allows (D13, §2.6).
//   - Mutable state has one writer. Readers never observe a partially written
//     file; mutations are fsynced and atomically renamed (A09, §4.4).
//   - Destination hostnames reach the exit unresolved; only the direct path
//     uses the local resolver (D15, §1.7).
package contract
