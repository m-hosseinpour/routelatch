package routing

import (
	"testing"

	"routelatch/internal/contract"
)

func TestMatchDomainPrecedence(t *testing.T) {
	example := contract.Rule{ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com", Origin: contract.RuleOriginUser}
	deep := contract.Rule{ID: "r-deep", Kind: contract.RuleDomainSuffix, Host: "deep.api.example.com", Origin: contract.RuleOriginUser}
	exactAPI := contract.Rule{ID: "r-api", Kind: contract.RuleDomain, Host: "api.example.com", Origin: contract.RuleOriginUser}
	exactSameAsSuffix := contract.Rule{ID: "r-apex-exact", Kind: contract.RuleDomain, Host: "example.com", Origin: contract.RuleOriginUser}
	rules := []contract.Rule{example, deep, exactAPI, exactSameAsSuffix}

	tests := []struct {
		host string
		want contract.RuleID
	}{
		{"example.com", "r-apex-exact"},
		{"api.example.com", "r-api"},
		{"deep.api.example.com", "r-deep"},
		{"x.deep.api.example.com", "r-deep"},
		{"x.api.example.com", "r-example"},
		{"evilexample.com", ""},
		{"notexample.com", ""},
		{"example.com.evil.net", ""},
		{"EXAMPLE.com", ""}, // normalized input never has uppercase; matcher is exact
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			dst := domainDst(tc.host, 443)
			got, ok := Match(rules, dst)
			if tc.want == "" {
				if ok {
					t.Fatalf("unexpected match %+v", got)
				}
				return
			}
			if !ok || got.ID != tc.want {
				t.Fatalf("match = %+v/%v, want %s", got, ok, tc.want)
			}
		})
	}
}

func TestMatchIPPrecedence(t *testing.T) {
	exact := contract.Rule{ID: "r-exact", Kind: contract.RuleIP, Addr: mustAddr(t, "203.0.113.8"), Origin: contract.RuleOriginUser}
	wide := contract.Rule{ID: "r-wide", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/24"), Origin: contract.RuleOriginUser}
	narrow := contract.Rule{ID: "r-narrow", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/25"), Origin: contract.RuleOriginUser}
	v6wide := contract.Rule{ID: "r-v6-wide", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "2001:db8:1::/64"), Origin: contract.RuleOriginUser}
	v6exact := contract.Rule{ID: "r-v6-exact", Kind: contract.RuleIP, Addr: mustAddr(t, "2001:db8:1::1"), Origin: contract.RuleOriginUser}
	domainRule := contract.Rule{ID: "r-domain", Kind: contract.RuleDomain, Host: "203.0.113.8", Origin: contract.RuleOriginUser}
	rules := []contract.Rule{wide, narrow, exact, v6wide, v6exact, domainRule}

	tests := []struct {
		name string
		dst  contract.Destination
		want contract.RuleID
	}{
		{"exact beats cidr", ipDst(mustAddr(t, "203.0.113.8"), 443), "r-exact"},
		{"longest prefix", ipDst(mustAddr(t, "203.0.113.9"), 443), "r-narrow"},
		{"wide only", ipDst(mustAddr(t, "203.0.113.200"), 443), "r-wide"},
		{"outside", ipDst(mustAddr(t, "203.0.114.1"), 443), ""},
		{"v6 exact beats prefix", ipDst(mustAddr(t, "2001:db8:1::1"), 443), "r-v6-exact"},
		{"v6 prefix", ipDst(mustAddr(t, "2001:db8:1::2"), 443), "r-v6-wide"},
		{"mapped destination matches v4", ipDst(mustAddr(t, "::ffff:203.0.113.8"), 443), "r-exact"},
		{"hostname never matches ip rule", domainDst("example.com", 443), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Match(rules, tc.dst)
			if tc.want == "" {
				if ok {
					t.Fatalf("unexpected match %+v", got)
				}
				return
			}
			if !ok || got.ID != tc.want {
				t.Fatalf("match = %+v/%v, want %s", got, ok, tc.want)
			}
		})
	}
}

func TestMatchOriginAndOrderIndependent(t *testing.T) {
	base := contract.Rule{ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com", Origin: contract.RuleOriginUser}
	alternative := base
	alternative.Origin = contract.RuleOrigin("diagnostic")
	other := contract.Rule{ID: "r-api", Kind: contract.RuleDomain, Host: "api.example.com", Origin: contract.RuleOriginUser}

	dst := domainDst("api.example.com", 443)
	want, ok := Match([]contract.Rule{base, other}, dst)
	if !ok {
		t.Fatal("expected a match")
	}
	for _, rules := range [][]contract.Rule{
		{alternative, other},
		{other, base},
		{other, alternative},
		{alternative, other, base},
	} {
		got, ok := Match(rules, dst)
		if !ok || got.ID != want.ID || got.Kind != want.Kind {
			t.Fatalf("origin/order changed matching: got %+v/%v, want %+v", got, ok, want)
		}
	}
}

func TestMatchIsIdenticalAfterNewestFirstReversal(t *testing.T) {
	// The stored list moved from oldest-first to newest-first in schema 2.
	// Reversing it is a storage migration only: every decision stays the same.
	rules := []contract.Rule{
		{ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com"},
		{ID: "r-deep", Kind: contract.RuleDomainSuffix, Host: "deep.example.com"},
		{ID: "r-api", Kind: contract.RuleDomain, Host: "api.example.com"},
		{ID: "r-wide", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/24")},
		{ID: "r-narrow", Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/25")},
		{ID: "r-exact", Kind: contract.RuleIP, Addr: mustAddr(t, "203.0.113.8")},
		{ID: "r-tie-b", Kind: contract.RuleDomain, Host: "tie.example"},
		{ID: "r-tie-a", Kind: contract.RuleDomain, Host: "tie.example"},
	}
	reversed := make([]contract.Rule, len(rules))
	for i, rule := range rules {
		reversed[len(rules)-1-i] = rule
	}
	destinations := []contract.Destination{
		domainDst("example.com", 443),
		domainDst("api.example.com", 443),
		domainDst("deep.api.example.com", 443),
		domainDst("tie.example", 443),
		domainDst("unmatched.example", 443),
		ipDst(mustAddr(t, "203.0.113.8"), 443),
		ipDst(mustAddr(t, "203.0.113.200"), 443),
		ipDst(mustAddr(t, "198.51.100.1"), 443),
	}
	for _, dst := range destinations {
		want, wantOK := Match(rules, dst)
		got, gotOK := Match(reversed, dst)
		if got != want || gotOK != wantOK {
			t.Fatalf("reversal changed match for %+v: %+v/%v -> %+v/%v", dst, want, wantOK, got, gotOK)
		}
	}
}

func TestMatchTieUsesRuleID(t *testing.T) {
	b := contract.Rule{ID: "r-b", Kind: contract.RuleDomainSuffix, Host: "example.com"}
	a := contract.Rule{ID: "r-a", Kind: contract.RuleDomainSuffix, Host: "example.com"}
	for _, rules := range [][]contract.Rule{{b, a}, {a, b}} {
		got, ok := Match(rules, domainDst("api.example.com", 443))
		if !ok || got.ID != "r-a" {
			t.Fatalf("tie result = %+v/%v, want r-a", got, ok)
		}
	}
}

func TestMatchRequiresLabelBoundary(t *testing.T) {
	rule := contract.Rule{ID: "r", Kind: contract.RuleDomainSuffix, Host: "example.com"}
	if _, ok := Match([]contract.Rule{rule}, domainDst("badexample.com", 443)); ok {
		t.Fatal("badexample.com matched example.com")
	}
	if _, ok := Match([]contract.Rule{rule}, domainDst("example.com.evil.net", 443)); ok {
		t.Fatal("example.com.evil.net matched example.com")
	}
	if _, ok := Match([]contract.Rule{rule}, domainDst("a.example.com", 443)); !ok {
		t.Fatal("a.example.com did not match example.com")
	}
}
