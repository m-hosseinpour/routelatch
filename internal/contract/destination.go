package contract

import (
	"net/netip"
	"time"
)

// DestKind is the syntactic kind of a destination or rule operand.
type DestKind string

const (
	// DestDomain is a hostname. It is never resolved locally on the pool
	// path; the exit resolves it (D15, docs/contracts.md §1.7).
	DestDomain DestKind = "domain"
	// DestIP is one IP literal. IPv4-mapped IPv6 is normalized to IPv4.
	DestIP DestKind = "ip"
	// DestCIDR is an IP range. It is valid in rules and unusable as a dial
	// target.
	DestCIDR DestKind = "cidr"
)

// Destination is a normalized dial target.
//
// Normalization (docs/contracts.md §1.2) is the only producer of this type:
// lowercase A-label hostnames without a trailing dot, netip values without a
// zone, and IPv4-mapped addresses unmapped. Port 0 means "not stated by the
// client" and is only valid while parsing; every dial path requires 1..65535
// (docs/contracts.md §1.2 N9). Rules are port-agnostic in v1.
type Destination struct {
	Kind   DestKind     `json:"kind"`
	Host   string       `json:"host,omitempty"`  // DestDomain: normalized hostname
	Addr   netip.Addr   `json:"addr,omitzero"`   // DestIP; DestCIDR: network address
	Prefix netip.Prefix `json:"prefix,omitzero"` // DestCIDR only
	Port   uint16       `json:"port,omitempty"`
}

// DestClass is the policy class of a destination, computed before any rule
// match and with a fixed precedence: Self > Local > LAN > Approved > Other
// (docs/contracts.md §1.4).
type DestClass string

const (
	// ClassSelf is one of RouteLatch's own traffic or management endpoints.
	// It is refused in every mode to prevent a proxy-to-self loop, and no
	// user rule can make it routable.
	ClassSelf DestClass = "self"
	// ClassLocal is loopback, link-local, multicast, broadcast, or a
	// *.localhost name. It is always direct (D17), never pooled. The
	// unspecified addresses 0.0.0.0 and :: are rejected during normalization
	// rather than classified here.
	ClassLocal DestClass = "local"
	// ClassLAN is RFC 1918, IPv6 ULA, CGNAT and other non-global unicast,
	// plus single-label local names. It is always direct in v1; a pool exit
	// cannot reach the user's LAN.
	ClassLAN DestClass = "lan"
	// ClassApproved is a public destination that matches an approved rule.
	ClassApproved DestClass = "approved"
	// ClassOther is any other public destination.
	ClassOther DestClass = "other"
)

// RuleKind is the matching semantics of one approved-destination rule.
type RuleKind string

const (
	// RuleDomain matches one hostname exactly.
	RuleDomain RuleKind = "domain"
	// RuleDomainSuffix matches a hostname and every label-boundary subdomain
	// of it: example.com and api.example.com, never badexample.com
	// (docs/contracts.md §1.5).
	RuleDomainSuffix RuleKind = "domain_suffix"
	// RuleIP matches one IP literal after unmapping.
	RuleIP RuleKind = "ip"
	// RuleCIDR matches an IP inside a masked prefix. It never matches a
	// hostname: RouteLatch does not resolve hostnames to test IP rules.
	RuleCIDR RuleKind = "cidr"
)

// RuleID is stable for the lifetime of a rule; it is not derived from the
// rule's text, so editing text creates a new rule.
type RuleID string

// RuleOrigin records why a rule exists. Only a user decision may create a
// rule; a diagnostic produces a Proposal, never a Rule (D09).
type RuleOrigin string

const (
	RuleOriginUser RuleOrigin = "user"
)

// Rule is one approved destination. Rules are port-agnostic in v1.
type Rule struct {
	ID      RuleID       `json:"id"`
	Kind    RuleKind     `json:"kind"`
	Host    string       `json:"host,omitempty"`  // RuleDomain, RuleDomainSuffix
	Addr    netip.Addr   `json:"addr,omitzero"`   // RuleIP
	Prefix  netip.Prefix `json:"prefix,omitzero"` // RuleCIDR
	Note    string       `json:"note,omitempty"`  // display only; never parsed or executed
	Origin  RuleOrigin   `json:"origin"`
	AddedAt time.Time    `json:"added_at"`
}

// Mode is the routing mode selected by the user (D12). Stopped is the absence
// of a listening service, not a routing table.
type Mode string

const (
	ModeTargeted  Mode = "targeted"
	ModeProxyAll  Mode = "all-proxy"
	ModeDirectAll Mode = "all-direct"
	ModeStopped   Mode = "stopped"
)

// Route is the egress chosen for one destination.
type Route string

const (
	// RouteDirect dials the destination from this process with the system
	// resolver.
	RouteDirect Route = "direct"
	// RoutePool dials through the selected pool exit. Pool failure is an
	// error, never a silent direct dial (D11).
	RoutePool Route = "pool"
	// RouteRefuse refuses locally without touching the network: proxy-to-self,
	// management endpoints, invalid destinations and a stopped service.
	RouteRefuse Route = "refuse"
)

// PolicyVersion is a monotonically increasing generation of the routing
// policy. Every committed change to mode, rules or manual exit selection
// increments it. A connection, probe or dial carries the version that admitted
// it, and the registry uses that value to close exactly the connections a
// change made invalid (D13).
type PolicyVersion uint64

// DecisionReason is a stable machine-readable reason for a Decision. It is
// safe to print: it names the rule of the matrix that fired, not the traffic.
type DecisionReason string

const (
	ReasonLocalException DecisionReason = "local_exception"
	ReasonLANException   DecisionReason = "lan_exception"
	ReasonApprovedRule   DecisionReason = "approved_rule"
	ReasonProxyAll       DecisionReason = "proxy_all"
	ReasonUnapproved     DecisionReason = "unapproved"
	ReasonDirectAll      DecisionReason = "all_direct"
	ReasonSelfLoop       DecisionReason = "self_loop"
	ReasonModeStopped    DecisionReason = "mode_stopped"
	ReasonManualExit     DecisionReason = "manual_exit"
)

// Decision is the router's answer for one destination. It carries the policy
// version that produced it; a decision from an older version must not be used
// to register a connection.
type Decision struct {
	Mode          Mode           `json:"mode"`
	Class         DestClass      `json:"class"`
	Route         Route          `json:"route"`
	Reason        DecisionReason `json:"reason"`
	RuleID        RuleID         `json:"rule_id,omitempty"`
	PolicyVersion PolicyVersion  `json:"policy_version"`
}
