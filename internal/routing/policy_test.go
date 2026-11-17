package routing

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"sync"
	"testing"

	"routelatch/internal/contract"
)

func mustNormalize(t *testing.T, raw string) contract.Destination {
	t.Helper()
	dst, err := NormalizeDestination(raw)
	if err != nil {
		t.Fatalf("test setup: normalize destination %q: %v", raw, err)
	}
	return dst
}

func matrixPolicy(t *testing.T, mode contract.Mode, version contract.PolicyVersion) *Policy {
	t.Helper()
	rules := []contract.Rule{
		ruleWithID(t, contract.RuleDomainSuffix, "example.com", "r-domain"),
		ruleWithID(t, contract.RuleIP, "203.0.113.8", "r-ip"),
		ruleWithID(t, contract.RuleCIDR, "198.51.100.0/24", "r-cidr"),
	}
	p, err := NewPolicy(version, Config{
		Mode:      mode,
		Rules:     rules,
		SelfPorts: []uint16{8317, 8318},
	})
	if err != nil {
		t.Fatalf("test setup: NewPolicy: %v", err)
	}
	return p
}

func TestDecisionMatrix(t *testing.T) {
	type want struct {
		route  contract.Route
		reason contract.DecisionReason
	}
	common := map[contract.DestClass]want{
		contract.ClassSelf:     {contract.RouteRefuse, contract.ReasonSelfLoop},
		contract.ClassLocal:    {contract.RouteDirect, contract.ReasonLocalException},
		contract.ClassLAN:      {contract.RouteDirect, contract.ReasonLANException},
		contract.ClassApproved: {contract.RoutePool, contract.ReasonApprovedRule},
		contract.ClassOther:    {contract.RouteDirect, contract.ReasonUnapproved},
	}
	proxyAll := map[contract.DestClass]want{
		contract.ClassSelf:     {contract.RouteRefuse, contract.ReasonSelfLoop},
		contract.ClassLocal:    {contract.RouteDirect, contract.ReasonLocalException},
		contract.ClassLAN:      {contract.RouteDirect, contract.ReasonLANException},
		contract.ClassApproved: {contract.RoutePool, contract.ReasonProxyAll},
		contract.ClassOther:    {contract.RoutePool, contract.ReasonProxyAll},
	}
	directAll := map[contract.DestClass]want{
		contract.ClassSelf:     {contract.RouteRefuse, contract.ReasonSelfLoop},
		contract.ClassLocal:    {contract.RouteDirect, contract.ReasonLocalException},
		contract.ClassLAN:      {contract.RouteDirect, contract.ReasonLANException},
		contract.ClassApproved: {contract.RouteDirect, contract.ReasonDirectAll},
		contract.ClassOther:    {contract.RouteDirect, contract.ReasonDirectAll},
	}
	stopped := map[contract.DestClass]want{
		contract.ClassSelf:     {contract.RouteRefuse, contract.ReasonModeStopped},
		contract.ClassLocal:    {contract.RouteRefuse, contract.ReasonModeStopped},
		contract.ClassLAN:      {contract.RouteRefuse, contract.ReasonModeStopped},
		contract.ClassApproved: {contract.RouteRefuse, contract.ReasonModeStopped},
		contract.ClassOther:    {contract.RouteRefuse, contract.ReasonModeStopped},
	}

	destinations := map[contract.DestClass][]string{
		contract.ClassSelf: {
			"127.0.0.1:8317",
			"[::1]:8318",
			"localhost:8317",
			"api.localhost:8318",
		},
		contract.ClassLocal: {
			"127.0.0.1:9999",
			"[::1]:9999",
			"localhost:9999",
			"not.localhost",
			"169.254.1.1",
			"224.0.0.1",
			"255.255.255.255",
		},
		contract.ClassLAN: {
			"10.0.0.1",
			"192.168.1.10",
			"100.64.0.1",
			"fd00::1",
			"example",
		},
		contract.ClassApproved: {
			"api.example.com",
			"203.0.113.8",
			"198.51.100.7",
		},
		contract.ClassOther: {
			"other.example.net",
			"8.8.8.8",
			"203.0.114.1",
		},
	}

	modes := []struct {
		mode contract.Mode
		want map[contract.DestClass]want
	}{
		{contract.ModeTargeted, common},
		{contract.ModeProxyAll, proxyAll},
		{contract.ModeDirectAll, directAll},
		{contract.ModeStopped, stopped},
	}

	for _, mode := range modes {
		t.Run(string(mode.mode), func(t *testing.T) {
			policy := matrixPolicy(t, mode.mode, 42)
			for class, raws := range destinations {
				t.Run(string(class), func(t *testing.T) {
					expected := mode.want[class]
					for _, raw := range raws {
						dst := mustNormalize(t, raw)
						baseClass := classify(dst, policy.selfPorts)
						if class == contract.ClassApproved {
							if baseClass != contract.ClassOther {
								t.Fatalf("test setup: %q classified %s, want other before rule promotion", raw, baseClass)
							}
						} else if baseClass != class {
							t.Fatalf("test setup: %q classified %s, want %s", raw, baseClass, class)
						}
						decision, err := policy.Decide(context.Background(), dst)
						if err != nil {
							t.Fatalf("Decide(%q): %v", raw, err)
						}
						if decision.Mode != mode.mode {
							t.Errorf("%q: mode = %s, want %s", raw, decision.Mode, mode.mode)
						}
						if decision.Class != class {
							t.Errorf("%q: class = %s, want %s", raw, decision.Class, class)
						}
						if decision.Route != expected.route {
							t.Errorf("%q: route = %s, want %s", raw, decision.Route, expected.route)
						}
						if decision.Reason != expected.reason {
							t.Errorf("%q: reason = %s, want %s", raw, decision.Reason, expected.reason)
						}
						if decision.PolicyVersion != 42 {
							t.Errorf("%q: version = %d, want 42", raw, decision.PolicyVersion)
						}
						if class == contract.ClassApproved {
							if decision.RuleID == "" {
								t.Errorf("%q: approved decision has no rule id", raw)
							}
						} else if decision.RuleID != "" {
							t.Errorf("%q: non-approved decision carries rule id %s", raw, decision.RuleID)
						}
					}
				})
			}
		})
	}
}

func TestApprovedNeverFallsBackToDirectWithNoPool(t *testing.T) {
	// The router has no pool provider at all. A pool route must still be
	// returned; PoolDialer is responsible for PoolUnavailable (D11, §2.5 row 5).
	targeted := matrixPolicy(t, contract.ModeTargeted, 9)
	for _, raw := range []string{
		"api.example.com:443",
		"x.deep.api.example.com:443",
		"203.0.113.8:443",
		"198.51.100.200:443",
	} {
		decision, err := targeted.Decide(context.Background(), mustNormalize(t, raw))
		if err != nil {
			t.Fatalf("Decide(%q): %v", raw, err)
		}
		if decision.Class != contract.ClassApproved {
			t.Fatalf("%q: class = %s, want approved", raw, decision.Class)
		}
		if decision.Route != contract.RoutePool {
			t.Fatalf("%q: route = %s, want pool (no direct fallback)", raw, decision.Route)
		}
	}

	allProxy := matrixPolicy(t, contract.ModeProxyAll, 9)
	for _, raw := range []string{
		"api.example.com:443",
		"203.0.113.8:443",
		"198.51.100.200:443",
		"other.example.net:443",
		"8.8.8.8:443",
	} {
		decision, err := allProxy.Decide(context.Background(), mustNormalize(t, raw))
		if err != nil {
			t.Fatalf("Decide(%q): %v", raw, err)
		}
		if decision.Route != contract.RoutePool {
			t.Fatalf("%q: route = %s, want pool", raw, decision.Route)
		}
	}
}

func TestRuleCannotOverrideLocalOrSelfClass(t *testing.T) {
	// Defense in depth: even a malformed snapshot that contains a Local/LAN
	// rule (NewPolicy would reject it) must not pool those destinations.
	policy := &Policy{
		version: 1,
		mode:    contract.ModeTargeted,
		selfPorts: map[uint16]struct{}{
			8317: {},
		},
		rules: []contract.Rule{
			{ID: "bad-localhost", Kind: contract.RuleDomainSuffix, Host: "localhost"},
			{ID: "bad-loopback", Kind: contract.RuleIP, Addr: mustAddr(t, "127.0.0.1")},
			{ID: "bad-lan", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "10.0.0.0/8")},
		},
	}
	tests := []struct {
		raw   string
		class contract.DestClass
		route contract.Route
	}{
		{"localhost:9999", contract.ClassLocal, contract.RouteDirect},
		{"api.localhost:9999", contract.ClassLocal, contract.RouteDirect},
		{"127.0.0.1:9999", contract.ClassLocal, contract.RouteDirect},
		{"10.1.2.3:443", contract.ClassLAN, contract.RouteDirect},
		{"127.0.0.1:8317", contract.ClassSelf, contract.RouteRefuse},
	}
	for _, tc := range tests {
		decision, err := policy.Decide(context.Background(), mustNormalize(t, tc.raw))
		if err != nil {
			t.Fatalf("Decide(%q): %v", tc.raw, err)
		}
		if decision.Class != tc.class || decision.Route != tc.route {
			t.Fatalf("%q: got class=%s route=%s, want class=%s route=%s",
				tc.raw, decision.Class, decision.Route, tc.class, tc.route)
		}
	}
}

func TestPolicyVersionOnlyChangesOnRoutingChange(t *testing.T) {
	ruleA := ruleWithID(t, contract.RuleDomainSuffix, "example.com", "r-a")
	ruleB := ruleWithID(t, contract.RuleIP, "203.0.113.8", "r-b")
	cfg := Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{ruleA, ruleB}, SelfPorts: []uint16{8317}}

	policy, err := NewPolicy(7, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PolicyVersion() != 7 {
		t.Fatalf("version = %d, want 7", policy.PolicyVersion())
	}

	// Same rules in another order are not a routing change.
	reordered := Config{Mode: cfg.Mode, Rules: []contract.Rule{ruleB, ruleA}, SelfPorts: cfg.SelfPorts}
	next, err := policy.Rebuild(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if next != policy || next.PolicyVersion() != 7 {
		t.Fatalf("reorder changed policy/version: %p -> %p, %d", policy, next, next.PolicyVersion())
	}

	// Origin, note and added_at are metadata, not routing input. Origin is
	// always "user" in a valid snapshot, so this exercises note/added_at.
	metadata := ruleA
	metadata.Note = "renamed"
	metadata.AddedAt = metadata.AddedAt.AddDate(1, 0, 0)
	metaCfg := Config{Mode: cfg.Mode, Rules: []contract.Rule{ruleB, metadata}, SelfPorts: cfg.SelfPorts}
	next, err = policy.Rebuild(metaCfg)
	if err != nil {
		t.Fatal(err)
	}
	if next != policy || next.PolicyVersion() != 7 {
		t.Fatalf("metadata change bumped version: %d", next.PolicyVersion())
	}

	// Listener ports decide Self versus Local, so changing them changes a
	// route (loopback to our own port) and must advance the version.
	changedPorts := metaCfg
	changedPorts.SelfPorts = []uint16{9999}
	next, err = policy.Rebuild(changedPorts)
	if err != nil {
		t.Fatal(err)
	}
	if next.PolicyVersion() != 8 {
		t.Fatalf("self-port change version = %d, want 8", next.PolicyVersion())
	}
	if decision, err := next.Decide(context.Background(), mustNormalize(t, "127.0.0.1:8317")); err != nil {
		t.Fatal(err)
	} else if decision.Route != contract.RouteDirect || decision.Reason != contract.ReasonLocalException {
		t.Fatalf("old self port after change: class=%s route=%s reason=%s", decision.Class, decision.Route, decision.Reason)
	}

	changedMode := metaCfg
	changedMode.Mode = contract.ModeProxyAll
	next, err = policy.Rebuild(changedMode)
	if err != nil {
		t.Fatal(err)
	}
	if next.PolicyVersion() != 8 {
		t.Fatalf("mode change version = %d, want 8", next.PolicyVersion())
	}
	if next.Mode() != contract.ModeProxyAll {
		t.Fatalf("mode = %s", next.Mode())
	}

	changedRule := ruleA
	changedRule.Host = "other.example.com"
	addCfg := Config{Mode: cfg.Mode, Rules: []contract.Rule{changedRule, ruleB}, SelfPorts: cfg.SelfPorts}
	next2, err := policy.Rebuild(addCfg)
	if err != nil {
		t.Fatal(err)
	}
	if next2.PolicyVersion() != 8 {
		t.Fatalf("rule change version = %d, want 8", next2.PolicyVersion())
	}

	// A policy created at zero starts advancing from 1.
	zero, err := NewPolicy(0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := zero.Rebuild(changedMode)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.PolicyVersion() != 1 {
		t.Fatalf("zero version advanced to %d, want 1", advanced.PolicyVersion())
	}

	// Exhaustion is an internal defect, not a silent wrap-around.
	last, err := NewPolicy(math.MaxUint64, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := last.Rebuild(changedMode); err == nil {
		t.Fatal("version overflow was accepted")
	} else {
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.Internal {
			t.Fatalf("overflow error = %v", err)
		}
	}
}

func TestPolicyIsImmutable(t *testing.T) {
	rule := ruleWithID(t, contract.RuleDomainSuffix, "example.com", "r-a")
	rules := []contract.Rule{rule}
	policy, err := NewPolicy(1, Config{Mode: contract.ModeTargeted, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}

	before, err := policy.Decide(context.Background(), mustNormalize(t, "api.example.com"))
	if err != nil {
		t.Fatal(err)
	}

	rules[0].Host = "evil.example"
	rules[0].Kind = contract.RuleIP
	gotRules := policy.Rules()
	gotRules[0].Host = "also.evil.example"

	after, err := policy.Decide(context.Background(), mustNormalize(t, "api.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("caller mutation changed decisions: %+v -> %+v", before, after)
	}
	if policy.Rules()[0].Host != "example.com" {
		t.Fatalf("Rules() exposed internal storage: %+v", policy.Rules()[0])
	}
}

func TestPolicyRejectsUnknownMode(t *testing.T) {
	_, err := NewPolicy(1, Config{Mode: contract.Mode("bogus")})
	if err == nil {
		t.Fatal("unknown mode was accepted")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Internal {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPolicyRejectsDuplicateRuleID(t *testing.T) {
	a := ruleWithID(t, contract.RuleDomain, "api.example.com", "r-same")
	b := ruleWithID(t, contract.RuleDomain, "other.example.com", "r-same")
	_, err := NewPolicy(1, Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{a, b}})
	if err == nil {
		t.Fatal("duplicate rule id was accepted")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Internal {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideRejectsCIDRAndMalformedDestinations(t *testing.T) {
	policy := matrixPolicy(t, contract.ModeTargeted, 1)
	cidr := mustNormalize(t, "203.0.113.0/24")
	if _, err := policy.Decide(context.Background(), cidr); err == nil {
		t.Fatal("CIDR was accepted as a dial target")
	} else {
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.InvalidDestination {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if _, err := policy.Decide(context.Background(), contract.Destination{Kind: contract.DestDomain}); err == nil {
		t.Fatal("empty domain destination was accepted")
	}
	if _, err := policy.Decide(context.Background(), contract.Destination{Kind: contract.DestKind("bogus")}); err == nil {
		t.Fatal("unknown destination kind was accepted")
	}
	// A directly constructed mapped unspecified address must be rejected just
	// like NormalizeDestination rejects ::ffff:0.0.0.0.
	if _, err := policy.Decide(context.Background(), contract.Destination{
		Kind: contract.DestIP,
		Addr: netip.MustParseAddr("::ffff:0.0.0.0"),
	}); err == nil {
		t.Fatal("mapped unspecified address was accepted")
	}
	if _, err := policy.Decide(context.Background(), contract.Destination{
		Kind: contract.DestIP,
		Addr: mustAddr(t, "203.0.113.8"),
		Host: "example.com",
	}); err == nil {
		t.Fatal("IP destination with a host field was accepted")
	}
	if _, err := policy.Decide(context.Background(), contract.Destination{
		Kind:   contract.DestCIDR,
		Prefix: mustPrefix(t, "203.0.113.0/24"),
		Host:   "example.com",
	}); err == nil {
		t.Fatal("CIDR destination with a host field was accepted")
	}
	if _, err := policy.Decide(context.Background(), contract.Destination{
		Kind: contract.DestCIDR,
	}); err == nil {
		t.Fatal("CIDR destination without a prefix was accepted")
	}
}

func TestDecideNilContext(t *testing.T) {
	policy := matrixPolicy(t, contract.ModeTargeted, 1)
	decision, err := policy.Decide(nil, mustNormalize(t, "api.example.com"))
	if err != nil {
		t.Fatalf("nil context: %v", err)
	}
	if decision.Route != contract.RoutePool {
		t.Fatalf("route = %s, want pool", decision.Route)
	}
}

func TestRebuildPropagatesInvalidConfig(t *testing.T) {
	policy := matrixPolicy(t, contract.ModeTargeted, 3)
	bad := ruleWithID(t, contract.RuleDomain, "api.example.com", "r-bad")
	bad.Host = "localhost"
	if _, err := policy.Rebuild(Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{bad}}); err == nil {
		t.Fatal("Rebuild accepted an invalid rule")
	}
}

func TestRebuildCIDRRuleChangeBumpsVersion(t *testing.T) {
	rule := ruleWithID(t, contract.RuleCIDR, "203.0.113.0/24", "r-cidr")
	policy, err := NewPolicy(1, Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	changed := rule
	changed.Prefix = mustPrefix(t, "198.51.100.0/24")
	next, err := policy.Rebuild(Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{changed}})
	if err != nil {
		t.Fatal(err)
	}
	if next.PolicyVersion() != 2 {
		t.Fatalf("CIDR rule change version = %d, want 2", next.PolicyVersion())
	}
}

func TestDecideCanceledContext(t *testing.T) {
	policy := matrixPolicy(t, contract.ModeTargeted, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := policy.Decide(ctx, mustNormalize(t, "example.com"))
	if err == nil {
		t.Fatal("canceled context was accepted")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Canceled {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConcurrentDecisionsAreStable(t *testing.T) {
	policy := matrixPolicy(t, contract.ModeTargeted, 5)
	destinations := []string{
		"api.example.com:443",
		"203.0.113.8:443",
		"198.51.100.7:443",
		"other.example.net:443",
		"127.0.0.1:9999",
		"10.0.0.1:443",
		"127.0.0.1:8317",
	}
	type result struct {
		decision contract.Decision
		err      error
	}
	type testCase struct {
		raw  string
		dst  contract.Destination
		want result
	}
	cases := make([]testCase, 0, len(destinations))
	for _, raw := range destinations {
		dst := mustNormalize(t, raw)
		decision, err := policy.Decide(context.Background(), dst)
		cases = append(cases, testCase{raw, dst, result{decision, err}})
	}

	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, tc := range cases {
				decision, err := policy.Decide(context.Background(), tc.dst)
				if err != nil || tc.want.err != nil || decision != tc.want.decision {
					select {
					case failures <- tc.raw:
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for raw := range failures {
		t.Fatalf("concurrent decision for %q differed or failed", raw)
	}
}
