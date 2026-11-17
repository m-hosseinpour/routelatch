package routing

import (
	"context"
	"math"
	"slices"

	"routelatch/internal/contract"
)

// Config is the immutable input of one policy snapshot.
type Config struct {
	// Mode selects the routing matrix.
	Mode contract.Mode
	// Rules is the approved-destination list. The slice is copied and each
	// rule is validated; later edits to the caller's slice do not affect the
	// snapshot.
	Rules []contract.Rule
	// SelfPorts lists RouteLatch's own TCP listener ports. A loopback address
	// or localhost name with one of these ports is ClassSelf and is refused;
	// the same address on another port is ClassLocal and stays direct.
	SelfPorts []uint16
}

// Policy is an immutable routing snapshot implementing contract.Router.
// Decide performs no dial, DNS lookup, probe or clock read.
type Policy struct {
	version   contract.PolicyVersion
	mode      contract.Mode
	rules     []contract.Rule
	selfPorts map[uint16]struct{}
}

var _ contract.Router = (*Policy)(nil)

// NewPolicy builds a snapshot at the given externally owned PolicyVersion.
// Callers that derive a new snapshot from an old one should use Rebuild so the
// version only advances when the mode or a routing-relevant rule field changes.
func NewPolicy(version contract.PolicyVersion, cfg Config) (*Policy, error) {
	if !validMode(cfg.Mode) {
		return nil, policyFailure("unknown routing mode")
	}
	rules := make([]contract.Rule, len(cfg.Rules))
	seen := make(map[contract.RuleID]struct{}, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		if err := ValidateRule(rule); err != nil {
			return nil, err
		}
		if _, duplicate := seen[rule.ID]; duplicate {
			return nil, policyFailure("duplicate rule id")
		}
		seen[rule.ID] = struct{}{}
		rules[i] = rule
	}
	ports := make(map[uint16]struct{}, len(cfg.SelfPorts))
	for _, port := range cfg.SelfPorts {
		if port != 0 {
			ports[port] = struct{}{}
		}
	}
	return &Policy{
		version:   version,
		mode:      cfg.Mode,
		rules:     rules,
		selfPorts: ports,
	}, nil
}

// Rebuild returns the next snapshot. Identical routing input returns the same
// policy and version; a mode, rule-identity or self-listener-port change
// advances the version by one. Rule order, Origin, Note and AddedAt are not
// routing changes.
func (p *Policy) Rebuild(cfg Config) (*Policy, error) {
	next, err := NewPolicy(p.version, cfg)
	if err != nil {
		return nil, err
	}
	if p.samePolicy(next) {
		return p, nil
	}
	if p.version == math.MaxUint64 {
		return nil, policyFailure("policy version is exhausted")
	}
	next.version = p.version + 1
	return next, nil
}

func (p *Policy) samePolicy(other *Policy) bool {
	if p.mode != other.mode || len(p.rules) != len(other.rules) {
		return false
	}
	if len(p.selfPorts) != len(other.selfPorts) {
		return false
	}
	for port := range p.selfPorts {
		if _, ok := other.selfPorts[port]; !ok {
			return false
		}
	}
	a := make([]string, len(p.rules))
	b := make([]string, len(other.rules))
	for i := range p.rules {
		a[i] = ruleKey(p.rules[i])
		b[i] = ruleKey(other.rules[i])
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// PolicyVersion reports the exact version carried by every Decision.
func (p *Policy) PolicyVersion() contract.PolicyVersion { return p.version }

// Mode reports the snapshot's routing mode.
func (p *Policy) Mode() contract.Mode { return p.mode }

// Rules returns a copy of the snapshot's approved-destination list.
func (p *Policy) Rules() []contract.Rule {
	out := make([]contract.Rule, len(p.rules))
	copy(out, p.rules)
	return out
}

// Decide implements contract.Router. It applies class precedence before the
// mode matrix and returns RoutePool for every matrix cell that says pool, even
// though the snapshot has no pool knowledge and may be used with an empty pool.
// Port 0 ("not stated while parsing") is accepted and is never ClassSelf; the
// dialer remains responsible for requiring 1..65535 before dialing.
func (p *Policy) Decide(ctx context.Context, dst contract.Destination) (contract.Decision, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return contract.Decision{}, &contract.Failure{
				Kind:    contract.Canceled,
				Path:    contract.PathLocal,
				Op:      "routing.decide",
				Message: "routing decision canceled",
				Err:     err,
			}
		}
	}
	if err := validateDst(dst); err != nil {
		return contract.Decision{}, err
	}
	if dst.Kind == contract.DestCIDR {
		return contract.Decision{}, &contract.Failure{
			Kind:    contract.InvalidDestination,
			Path:    contract.PathLocal,
			Op:      "routing.decide",
			Message: "CIDR is a rule operand, not a dial target",
		}
	}

	class := classify(dst, p.selfPorts)
	matched, hasRule := Match(p.rules, dst)
	if class == contract.ClassOther && hasRule {
		class = contract.ClassApproved
	}

	decision := contract.Decision{
		Mode:          p.mode,
		Class:         class,
		PolicyVersion: p.version,
	}
	if class == contract.ClassApproved {
		decision.RuleID = matched.ID
	}

	// Stopped is the absence of a listener; no matrix cell can complete.
	if p.mode == contract.ModeStopped {
		decision.Route = contract.RouteRefuse
		decision.Reason = contract.ReasonModeStopped
		return decision, nil
	}

	switch class {
	case contract.ClassSelf:
		decision.Route = contract.RouteRefuse
		decision.Reason = contract.ReasonSelfLoop
		return decision, nil
	case contract.ClassLocal:
		decision.Route = contract.RouteDirect
		decision.Reason = contract.ReasonLocalException
		return decision, nil
	case contract.ClassLAN:
		decision.Route = contract.RouteDirect
		decision.Reason = contract.ReasonLANException
		return decision, nil
	case contract.ClassApproved:
		switch p.mode {
		case contract.ModeTargeted:
			decision.Route = contract.RoutePool
			decision.Reason = contract.ReasonApprovedRule
		case contract.ModeProxyAll:
			decision.Route = contract.RoutePool
			decision.Reason = contract.ReasonProxyAll
		default: // ModeDirectAll
			decision.Route = contract.RouteDirect
			decision.Reason = contract.ReasonDirectAll
		}
		return decision, nil
	default: // ClassOther
		switch p.mode {
		case contract.ModeTargeted:
			decision.Route = contract.RouteDirect
			decision.Reason = contract.ReasonUnapproved
		case contract.ModeProxyAll:
			decision.Route = contract.RoutePool
			decision.Reason = contract.ReasonProxyAll
		default: // ModeDirectAll
			decision.Route = contract.RouteDirect
			decision.Reason = contract.ReasonDirectAll
		}
		return decision, nil
	}
}

func validMode(mode contract.Mode) bool {
	switch mode {
	case contract.ModeTargeted, contract.ModeProxyAll, contract.ModeDirectAll, contract.ModeStopped:
		return true
	default:
		return false
	}
}

func validateDst(dst contract.Destination) error {
	bad := func(msg string) error {
		return &contract.Failure{
			Kind:    contract.InvalidDestination,
			Path:    contract.PathLocal,
			Op:      "routing.decide",
			Message: msg,
		}
	}
	switch dst.Kind {
	case contract.DestDomain:
		if dst.Host == "" || dst.Addr.IsValid() || dst.Prefix.IsValid() {
			return bad("domain destination is inconsistent")
		}
	case contract.DestIP:
		addr := dst.Addr.Unmap()
		if !dst.Addr.IsValid() || dst.Addr.Zone() != "" || addr.IsUnspecified() ||
			dst.Host != "" || dst.Prefix.IsValid() {
			return bad("IP destination is inconsistent")
		}
	case contract.DestCIDR:
		if !dst.Prefix.IsValid() || dst.Host != "" {
			return bad("CIDR destination is inconsistent")
		}
	default:
		return bad("unknown destination kind")
	}
	return nil
}
