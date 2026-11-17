// Package routing owns RouteLatch's destination model, rule matching and the
// routing policy matrix (docs/contracts.md §1 and §2.5).
//
// NormalizeDestination and NormalizeRule are the only producers of normalized
// hostnames, addresses and prefixes. NormalizeRuleAs adds an explicit rule kind
// because the frozen NormalizeRule signature has no kind parameter and cannot
// express the opt-in subdomain rule; it shares the same normalization path.
//
// A Policy is an immutable snapshot of mode, rules and the listener ports that
// identify RouteLatch's own endpoints. Its Decide method implements
// contract.Router: it is pure with respect to the network (no dial, no DNS, no
// clock) and it never substitutes a direct route for an approved destination
// when the pool is empty.
package routing
