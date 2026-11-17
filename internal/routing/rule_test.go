package routing

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func ruleWithID(t *testing.T, kind contract.RuleKind, operand, id string) contract.Rule {
	t.Helper()
	r, err := NormalizeRuleAs(kind, operand)
	if err != nil {
		t.Fatalf("test setup: normalize rule %q/%q: %v", kind, operand, err)
	}
	r.ID = contract.RuleID(id)
	r.Origin = contract.RuleOriginUser
	return r
}

func TestNormalizeRuleInfersKind(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want contract.Rule
	}{
		{name: "exact domain", raw: "api.example.com", want: contract.Rule{Kind: contract.RuleDomain, Host: "api.example.com"}},
		{name: "uppercase and dot", raw: "API.Example.COM.", want: contract.Rule{Kind: contract.RuleDomain, Host: "api.example.com"}},
		{name: "idn", raw: "bücher.de", want: contract.Rule{Kind: contract.RuleDomain, Host: "xn--bcher-kva.de"}},
		{name: "private suffix child", raw: "foo.github.io", want: contract.Rule{Kind: contract.RuleDomain, Host: "foo.github.io"}},
		{name: "url credentials dropped", raw: "https://user:pass@Example.com:8443/p?q=1#f", want: contract.Rule{Kind: contract.RuleDomain, Host: "example.com"}},
		{name: "ip", raw: "203.0.113.8", want: contract.Rule{Kind: contract.RuleIP, Addr: mustAddr(t, "203.0.113.8")}},
		{name: "mapped ip", raw: "::ffff:8.8.8.8", want: contract.Rule{Kind: contract.RuleIP, Addr: mustAddr(t, "8.8.8.8")}},
		{name: "unbracketed ipv6", raw: "2001:db8::1:443", want: contract.Rule{Kind: contract.RuleIP, Addr: mustAddr(t, "2001:db8::1:443")}},
		{name: "cidr", raw: "203.0.113.0/24", want: contract.Rule{Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/24")}},
		{name: "cidr host bits", raw: "203.0.113.8/24", want: contract.Rule{Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "203.0.113.0/24")}},
		{name: "cidr v6", raw: "2001:db8::/32", want: contract.Rule{Kind: contract.RuleCIDR, Prefix: mustPrefix(t, "2001:db8::/32")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRule(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeRule(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeRule(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeRuleAsExplicitKinds(t *testing.T) {
	suffix, err := NormalizeRuleAs(contract.RuleDomainSuffix, "API.Example.COM.")
	if err != nil {
		t.Fatalf("suffix: %v", err)
	}
	if suffix.Kind != contract.RuleDomainSuffix || suffix.Host != "api.example.com" {
		t.Fatalf("suffix rule = %+v", suffix)
	}

	exact, err := NormalizeRuleAs(contract.RuleDomain, "api.example.com")
	if err != nil {
		t.Fatalf("exact: %v", err)
	}
	if exact.Kind != contract.RuleDomain || exact.Host != "api.example.com" {
		t.Fatalf("exact rule = %+v", exact)
	}

	ip, err := NormalizeRuleAs(contract.RuleIP, "203.0.113.8")
	if err != nil || ip.Kind != contract.RuleIP {
		t.Fatalf("ip rule = %+v, %v", ip, err)
	}
	cidr, err := NormalizeRuleAs(contract.RuleCIDR, "203.0.113.0/24")
	if err != nil || cidr.Kind != contract.RuleCIDR {
		t.Fatalf("cidr rule = %+v, %v", cidr, err)
	}
}

func TestNormalizeRuleAsRejectsPort(t *testing.T) {
	tests := []struct {
		kind contract.RuleKind
		raw  string
	}{
		{contract.RuleDomain, "api.example.com:443"},
		{contract.RuleDomainSuffix, "api.example.com:443"},
		{contract.RuleIP, "203.0.113.8:443"},
	}
	for _, tc := range tests {
		if _, err := NormalizeRuleAs(tc.kind, tc.raw); err == nil {
			t.Fatalf("kind %s accepted a rule with a port: %q", tc.kind, tc.raw)
		}
	}
}

func TestNormalizeRuleRejections(t *testing.T) {
	tests := []struct {
		name    string
		kind    contract.RuleKind
		raw     string
		wantErr contract.ErrorKind
	}{
		{name: "port on domain", kind: "", raw: "example.com:443", wantErr: contract.InvalidDestination},
		{name: "port on url is ignored", kind: contract.RuleDomain, raw: "https://example.com:8443/path", wantErr: ""},
		{name: "port on ip", kind: "", raw: "203.0.113.8:443", wantErr: contract.InvalidDestination},
		{name: "port on bracketed ipv6", kind: "", raw: "[2001:db8::1]:443", wantErr: contract.InvalidDestination},
		{name: "tld", kind: "", raw: "com", wantErr: contract.InvalidDestination},
		{name: "public suffix", kind: "", raw: "co.uk", wantErr: contract.InvalidDestination},
		{name: "private suffix", kind: "", raw: "github.io", wantErr: contract.InvalidDestination},
		{name: "wildcard", kind: "", raw: "*.example.com", wantErr: contract.InvalidDestination},
		{name: "single label", kind: "", raw: "example", wantErr: contract.InvalidDestination},
		{name: "localhost", kind: "", raw: "localhost", wantErr: contract.InvalidDestination},
		{name: "localhost subdomain", kind: "", raw: "api.localhost", wantErr: contract.InvalidDestination},
		{name: "loopback", kind: "", raw: "127.0.0.1", wantErr: contract.InvalidDestination},
		{name: "link local", kind: "", raw: "169.254.1.1", wantErr: contract.InvalidDestination},
		{name: "private ip", kind: "", raw: "192.168.1.10", wantErr: contract.InvalidDestination},
		{name: "cgnat", kind: "", raw: "100.64.0.1", wantErr: contract.InvalidDestination},
		{name: "ula", kind: "", raw: "fd00::1", wantErr: contract.InvalidDestination},
		{name: "multicast", kind: "", raw: "224.0.0.1", wantErr: contract.InvalidDestination},
		{name: "broadcast", kind: "", raw: "255.255.255.255", wantErr: contract.InvalidDestination},
		{name: "default v4", kind: "", raw: "0.0.0.0/0", wantErr: contract.InvalidDestination},
		{name: "default v6", kind: "", raw: "::/0", wantErr: contract.InvalidDestination},
		{name: "half v4", kind: "", raw: "0.0.0.0/1", wantErr: contract.InvalidDestination},
		{name: "upper half v4", kind: "", raw: "128.0.0.0/1", wantErr: contract.InvalidDestination},
		{name: "private cidr", kind: "", raw: "10.0.0.0/8", wantErr: contract.InvalidDestination},
		{name: "private cidr overlap", kind: "", raw: "172.0.0.0/8", wantErr: contract.InvalidDestination},
		{name: "cgnat cidr", kind: "", raw: "100.64.0.0/10", wantErr: contract.InvalidDestination},
		{name: "loopback cidr", kind: "", raw: "127.0.0.0/8", wantErr: contract.InvalidDestination},
		{name: "link local cidr", kind: "", raw: "fe80::/10", wantErr: contract.InvalidDestination},
		{name: "ula cidr", kind: "", raw: "fc00::/7", wantErr: contract.InvalidDestination},
		{name: "multicast cidr", kind: "", raw: "224.0.0.0/4", wantErr: contract.InvalidDestination},
		{name: "domain kind on ip", kind: contract.RuleDomain, raw: "203.0.113.8", wantErr: contract.InvalidDestination},
		{name: "suffix kind on ip", kind: contract.RuleDomainSuffix, raw: "203.0.113.8", wantErr: contract.InvalidDestination},
		{name: "ip kind on domain", kind: contract.RuleIP, raw: "example.com", wantErr: contract.InvalidDestination},
		{name: "cidr kind on ip", kind: contract.RuleCIDR, raw: "203.0.113.8", wantErr: contract.InvalidDestination},
		{name: "domain kind on cidr", kind: contract.RuleDomain, raw: "203.0.113.0/24", wantErr: contract.InvalidDestination},
		{name: "unknown kind", kind: contract.RuleKind("bogus"), raw: "203.0.113.8", wantErr: contract.InvalidDestination},
		{name: "unsupported url", kind: "", raw: "vless://uuid@host:443", wantErr: contract.Unsupported},
		{name: "unsupported ftp", kind: "", raw: "ftp://example.com", wantErr: contract.Unsupported},
		{name: "bad port", kind: "", raw: "example.com:https", wantErr: contract.InvalidDestination},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.kind == "" {
				_, err = NormalizeRule(tc.raw)
			} else {
				_, err = NormalizeRuleAs(tc.kind, tc.raw)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s error", tc.wantErr)
			}
			var failure *contract.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("error %v is not *contract.Failure", err)
			}
			if failure.Kind != tc.wantErr {
				t.Fatalf("kind = %s, want %s (message %q)", failure.Kind, tc.wantErr, failure.Message)
			}
			if failure.Path != contract.PathLocal {
				t.Fatalf("path = %s, want local", failure.Path)
			}
		})
	}
}

func TestNormalizeRuleAcceptsPublicCidr(t *testing.T) {
	for _, operand := range []string{
		"203.0.113.0/24",
		"198.51.100.0/24",
		"2001:db8::/32",
		"1.0.0.0/8",
		"8.8.8.8/32",
		"2001:db8::1/128",
	} {
		if _, err := NormalizeRule(operand); err != nil {
			t.Errorf("NormalizeRule(%q) rejected: %v", operand, err)
		}
	}
}

func TestValidateRule(t *testing.T) {
	valid := []contract.Rule{
		ruleWithID(t, contract.RuleDomain, "api.example.com", "r-1"),
		ruleWithID(t, contract.RuleDomainSuffix, "example.com", "r-2"),
		ruleWithID(t, contract.RuleIP, "203.0.113.8", "r-3"),
		ruleWithID(t, contract.RuleCIDR, "203.0.113.0/24", "r-4"),
	}
	for i := range valid {
		if err := ValidateRule(valid[i]); err != nil {
			t.Fatalf("valid rule %d rejected: %v", i, err)
		}
	}

	originOther := valid[0]
	originOther.Origin = contract.RuleOrigin("diagnostic")
	if err := ValidateRule(originOther); err == nil {
		t.Fatal("non-user origin was accepted")
	}

	noted := valid[0]
	noted.Note = "display only"
	if err := ValidateRule(noted); err != nil {
		t.Fatalf("note must not affect validation: %v", err)
	}

	mutations := []struct {
		name string
		rule func() contract.Rule
	}{
		{"missing id", func() contract.Rule {
			r := valid[0]
			r.ID = ""
			return r
		}},
		{"missing origin", func() contract.Rule {
			r := valid[0]
			r.Origin = ""
			return r
		}},
		{"unknown kind", func() contract.Rule {
			r := valid[0]
			r.Kind = contract.RuleKind("bogus")
			return r
		}},
		{"domain with addr", func() contract.Rule {
			r := valid[0]
			r.Addr = mustAddr(t, "203.0.113.8")
			return r
		}},
		{"domain with prefix", func() contract.Rule {
			r := valid[0]
			r.Prefix = mustPrefix(t, "203.0.113.0/24")
			return r
		}},
		{"non canonical uppercase host", func() contract.Rule {
			r := valid[0]
			r.Host = "API.EXAMPLE.COM"
			return r
		}},
		{"non canonical trailing dot", func() contract.Rule {
			r := valid[0]
			r.Host = "api.example.com."
			return r
		}},
		{"local domain", func() contract.Rule {
			r := valid[0]
			r.Host = "localhost"
			return r
		}},
		{"lan ip rule", func() contract.Rule {
			r := valid[2]
			r.Addr = mustAddr(t, "192.168.1.1")
			return r
		}},
		{"mapped ip rule", func() contract.Rule {
			r := valid[2]
			r.Addr = netip.MustParseAddr("::ffff:203.0.113.8")
			return r
		}},
		{"ip with host", func() contract.Rule {
			r := valid[2]
			r.Host = "example.com"
			return r
		}},
		{"cidr with addr", func() contract.Rule {
			r := valid[3]
			r.Addr = mustAddr(t, "203.0.113.0")
			return r
		}},
		{"unmasked cidr", func() contract.Rule {
			r := valid[3]
			r.Prefix = netip.PrefixFrom(netip.MustParseAddr("203.0.113.8"), 24)
			return r
		}},
		{"private cidr", func() contract.Rule {
			r := valid[3]
			r.Prefix = mustPrefix(t, "10.0.0.0/8")
			return r
		}},
		{"default route", func() contract.Rule {
			r := valid[3]
			r.Prefix = mustPrefix(t, "0.0.0.0/0")
			return r
		}},
		{"control characters in id", func() contract.Rule {
			r := valid[0]
			r.ID = "r-\x1b[2J"
			return r
		}},
		{"control characters in note", func() contract.Rule {
			r := valid[0]
			r.Note = "note\x1bbad\x00"
			return r
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateRule(tc.rule()); err == nil {
				t.Fatal("rule was accepted")
			}
		})
	}
}

func TestPolicyRejectsInvalidRule(t *testing.T) {
	bad := ruleWithID(t, contract.RuleDomain, "api.example.com", "r-1")
	bad.Host = "localhost"
	_, err := NewPolicy(1, Config{Mode: contract.ModeTargeted, Rules: []contract.Rule{bad}})
	if err == nil {
		t.Fatal("policy accepted a local rule")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.InvalidDestination {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRuleErrorsNeverEchoInput(t *testing.T) {
	secrets := []string{"secret", "token", "password", "uuid"}
	inputs := []string{
		"ftp://user:secret@example.com/path?token=abc",
		"vless://uuid:password@host:443",
		"https://user:secret@example.com:badper/",
	}
	for _, raw := range inputs {
		if _, err := NormalizeRule(raw); err == nil {
			t.Fatalf("NormalizeRule(%q) unexpectedly succeeded", raw)
		} else {
			msg := strings.ToLower(err.Error())
			if raw == "vless://uuid:password@host:443" {
				// The kind name is safe; the credential fragments are not.
				if strings.Contains(msg, "password@") || strings.Contains(msg, "uuid:") {
					t.Fatalf("error leaked input: %q", err)
				}
			}
			for _, secret := range secrets {
				if strings.Contains(msg, ":"+secret+"@") || strings.Contains(msg, "="+secret) {
					t.Fatalf("error leaked %q: %q", secret, err)
				}
			}
		}
	}
}
