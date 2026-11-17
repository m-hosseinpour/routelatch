package contract

import "context"

// Router decides the egress for one destination under the policy snapshot it
// was built from.
//
// Invariants:
//   - It is pure with respect to the network: no dial, no resolve, no probe,
//     no clock-dependent flapping. The same destination and version always
//     produce the same Decision.
//   - It applies the class precedence Self > Local > LAN > Approved > Other
//     before the mode matrix, so no rule can pool loopback, link-local, LAN or
//     a management endpoint (docs/contracts.md §1.4).
//   - RoutePool is returned whenever the mode matrix says pool, including when
//     the pool is empty; the missing exit is then a pre-reply PoolUnavailable
//     from PoolDialer, never a direct dial and never a router-side guess about
//     which exit is healthy.
//   - It reports the exact PolicyVersion in the Decision; a decision with a
//     stale version must be rejected at Register, not repaired.
type Router interface {
	Decide(ctx context.Context, dst Destination) (Decision, error)
	PolicyVersion() PolicyVersion
}
