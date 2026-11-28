package control

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/diagnostics"
	"routelatch/internal/routing"
)

// newProposalService wires the in-memory proposal registry to the diagnostics
// manager so acceptProposal can run offline, without a listener or a probe.
func newProposalService(t *testing.T) *Service {
	t.Helper()
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.diag = diagnostics.New(diagnostics.Options{Store: svc.props})
	t.Cleanup(func() { svc.diag.Close(context.Background()) })
	return svc
}

func seedRouteProposal(t *testing.T, svc *Service, dst contract.Destination) contract.Proposal {
	t.Helper()
	host := dst.Host
	if dst.Kind == contract.DestIP {
		host = dst.Addr.Unmap().String()
	}
	p, created, err := svc.props.Upsert(dst, contract.Evidence{
		Kind:      contract.EvidenceDirectRefused,
		Outcome:   contract.OutcomeRefused,
		ProbeKind: contract.ProbeDirectTCP,
		Address:   net.JoinHostPort(host, "443"),
	}, "")
	if err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	if !created {
		t.Fatal("seed proposal was not created")
	}
	return p
}

func acceptViaHandle(t *testing.T, svc *Service, id contract.ProposalID, kind string) (ProposalAcceptData, error) {
	t.Helper()
	out, err := svc.Handle(context.Background(), Request{Op: OpProposalsAccept, ID: string(id), RuleKind: kind})
	if err != nil {
		return ProposalAcceptData{}, err
	}
	data, ok := out.(ProposalAcceptData)
	if !ok {
		t.Fatalf("proposals.accept returned %T, want ProposalAcceptData", out)
	}
	return data, nil
}

func proposalStatus(t *testing.T, svc *Service, id contract.ProposalID) contract.ProposalStatus {
	t.Helper()
	p, ok := svc.props.Get(id)
	if !ok {
		t.Fatalf("proposal %s disappeared from the registry", id)
	}
	return p.Status
}

func requireUsageError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("request was accepted, want a protocol error")
	}
	var opErr *OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error = %v (%T), want *OpError", err, err)
	}
	if opErr.Exit != contract.ExitUsage {
		t.Fatalf("exit = %d, want usage %d: %v", opErr.Exit, contract.ExitUsage, err)
	}
}

// TestAcceptProposalDomainSuffix covers the tray's "Accept as domain suffix"
// request end to end through the protocol handler: one suffix rule for the
// proposal hostname, the proposal consumed, and a replay refused without a
// duplicate rule.
func TestAcceptProposalDomainSuffix(t *testing.T) {
	svc := newProposalService(t)
	dst := contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443}
	p := seedRouteProposal(t, svc, dst)

	data, err := acceptViaHandle(t, svc, p.ID, string(contract.RuleDomainSuffix))
	if err != nil {
		t.Fatalf("accept suffix: %v", err)
	}
	if data.RuleID == "" {
		t.Fatal("accept returned no rule id")
	}
	rules := svc.Snapshot().State.Rules
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want exactly one", rules)
	}
	rule := rules[0]
	if rule.ID != data.RuleID || rule.Kind != contract.RuleDomainSuffix || rule.Host != "example.com" {
		t.Fatalf("rule = %+v, want id %s kind domain_suffix host example.com", rule, data.RuleID)
	}
	if rule.Origin != contract.RuleOriginUser || rule.AddedAt.IsZero() {
		t.Fatalf("rule provenance = %+v, want a user rule with an added_at", rule)
	}
	if got := proposalStatus(t, svc, p.ID); got != contract.ProposalAccepted {
		t.Fatalf("proposal status = %s, want accepted", got)
	}

	// The suffix must match the bare host and label-boundary subdomains, and
	// must never widen to a lookalike or an unrelated host.
	for host, want := range map[string]bool{
		"example.com":      true,
		"api.example.com":  true,
		"a.b.example.com":  true,
		"badexample.com":   false,
		"notexample.com":   false,
		"example.com.evil": false,
		"other.com":        false,
	} {
		got, ok := routing.Match(rules, contract.Destination{Kind: contract.DestDomain, Host: host})
		if ok != want {
			t.Fatalf("match %s = %t, want %t", host, ok, want)
		}
		if want && got.ID != data.RuleID {
			t.Fatalf("match %s chose rule %s, want %s", host, got.ID, data.RuleID)
		}
	}

	if _, err := acceptViaHandle(t, svc, p.ID, string(contract.RuleDomainSuffix)); err == nil {
		t.Fatal("replayed accept succeeded, want conflict")
	} else {
		var opErr *OpError
		if !errors.As(err, &opErr) || opErr.Exit != contract.ExitConflict {
			t.Fatalf("replay error = %v, want conflict", err)
		}
	}
	if got := len(svc.Snapshot().State.Rules); got != 1 {
		t.Fatalf("replay changed the rule count to %d, want 1", got)
	}
}

// TestAcceptProposalDefaultExactRule pins the unchanged default: no rule_kind
// still creates an exact hostname rule that does not cover subdomains.
func TestAcceptProposalDefaultExactRule(t *testing.T) {
	svc := newProposalService(t)
	dst := contract.Destination{Kind: contract.DestDomain, Host: "exact.example", Port: 443}
	p := seedRouteProposal(t, svc, dst)

	data, err := acceptViaHandle(t, svc, p.ID, "")
	if err != nil {
		t.Fatalf("accept default: %v", err)
	}
	rules := svc.Snapshot().State.Rules
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want exactly one", rules)
	}
	rule := rules[0]
	if rule.ID != data.RuleID || rule.Kind != contract.RuleDomain || rule.Host != "exact.example" {
		t.Fatalf("rule = %+v, want id %s kind domain host exact.example", rule, data.RuleID)
	}
	if _, ok := routing.Match(rules, contract.Destination{Kind: contract.DestDomain, Host: "exact.example"}); !ok {
		t.Fatal("exact rule does not match its own host")
	}
	if _, ok := routing.Match(rules, contract.Destination{Kind: contract.DestDomain, Host: "api.exact.example"}); ok {
		t.Fatal("exact rule widened to a subdomain")
	}
}

// TestAcceptProposalRefusesKindMismatch proves a refused request creates no
// rule and leaves the proposal pending, so the user can still accept it
// correctly afterwards.
func TestAcceptProposalRefusesKindMismatch(t *testing.T) {
	svc := newProposalService(t)
	ipDst := contract.Destination{Kind: contract.DestIP, Addr: netip.MustParseAddr("203.0.113.8"), Port: 443}
	ipProposal := seedRouteProposal(t, svc, ipDst)
	domainDst := contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443}
	domainProposal := seedRouteProposal(t, svc, domainDst)

	for _, tc := range []struct {
		name string
		id   contract.ProposalID
		kind string
	}{
		{"suffix on ip", ipProposal.ID, string(contract.RuleDomainSuffix)},
		{"domain on ip", ipProposal.ID, string(contract.RuleDomain)},
		{"unknown kind", domainProposal.ID, "bogus"},
		{"cidr kind", domainProposal.ID, string(contract.RuleCIDR)},
		{"ip kind", domainProposal.ID, string(contract.RuleIP)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := acceptViaHandle(t, svc, tc.id, tc.kind)
			requireUsageError(t, err)
			if data.RuleID != "" {
				t.Fatalf("refused request returned rule id %q", data.RuleID)
			}
			if got := len(svc.Snapshot().State.Rules); got != 0 {
				t.Fatalf("refused request created %d rule(s)", got)
			}
			if got := proposalStatus(t, svc, tc.id); got != contract.ProposalPending {
				t.Fatalf("proposal status = %s, want pending", got)
			}
		})
	}

	// The untouched proposals still accept normally.
	if _, err := acceptViaHandle(t, svc, domainProposal.ID, ""); err != nil {
		t.Fatalf("accept domain after refusals: %v", err)
	}
	if _, err := acceptViaHandle(t, svc, ipProposal.ID, ""); err != nil {
		t.Fatalf("accept ip after refusals: %v", err)
	}
	if got := len(svc.Snapshot().State.Rules); got != 2 {
		t.Fatalf("rules after recovery = %d, want 2", got)
	}
}
