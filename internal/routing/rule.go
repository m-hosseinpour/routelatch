package routing

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"

	"routelatch/internal/contract"
)

// NormalizeRule parses one rule operand and infers its kind from the text:
// IP and CIDR operands keep their kind, and a hostname becomes an exact
// RuleDomain. Suffix rules need an explicit kind, so use NormalizeRuleAs for
// RuleDomainSuffix (the frozen NormalizeRule signature has no kind parameter).
//
// The returned rule has Kind and its operand set; ID, Origin and AddedAt belong
// to the caller because a RuleID is not derived from the operand. Rules are
// port-agnostic, so any explicit port is rejected.
func NormalizeRule(raw string) (contract.Rule, error) {
	dst, err := NormalizeDestination(raw)
	if err != nil {
		return contract.Rule{}, err
	}
	return ruleFromDestination(dst)
}

// NormalizeRuleAs parses one rule operand with an explicit kind. All kinds use
// the same normalization path; a kind that does not match the operand is
// rejected instead of being silently converted.
func NormalizeRuleAs(kind contract.RuleKind, raw string) (contract.Rule, error) {
	dst, err := NormalizeDestination(raw)
	if err != nil {
		return contract.Rule{}, err
	}
	if dst.Port != 0 {
		return contract.Rule{}, invalidDestination("rules are port-agnostic; remove the port")
	}

	switch kind {
	case contract.RuleDomain, contract.RuleDomainSuffix:
		if dst.Kind != contract.DestDomain {
			return contract.Rule{}, invalidDestination("rule kind does not match the operand")
		}
		if err := validateRuleClass(dst); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: kind, Host: dst.Host}, nil
	case contract.RuleIP:
		if dst.Kind != contract.DestIP {
			return contract.Rule{}, invalidDestination("rule kind does not match the operand")
		}
		if err := validateRuleClass(dst); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: contract.RuleIP, Addr: dst.Addr}, nil
	case contract.RuleCIDR:
		if dst.Kind != contract.DestCIDR {
			return contract.Rule{}, invalidDestination("rule kind does not match the operand")
		}
		if err := validateRulePrefix(dst.Prefix); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: contract.RuleCIDR, Prefix: dst.Prefix}, nil
	default:
		return contract.Rule{}, invalidDestination("unknown rule kind")
	}
}

func ruleFromDestination(dst contract.Destination) (contract.Rule, error) {
	if dst.Port != 0 {
		return contract.Rule{}, invalidDestination("rules are port-agnostic; remove the port")
	}
	switch dst.Kind {
	case contract.DestDomain:
		if err := validateRuleClass(dst); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: contract.RuleDomain, Host: dst.Host}, nil
	case contract.DestIP:
		if err := validateRuleClass(dst); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: contract.RuleIP, Addr: dst.Addr}, nil
	case contract.DestCIDR:
		if err := validateRulePrefix(dst.Prefix); err != nil {
			return contract.Rule{}, err
		}
		return contract.Rule{Kind: contract.RuleCIDR, Prefix: dst.Prefix}, nil
	default:
		return contract.Rule{}, invalidDestination("unknown destination kind")
	}
}

// validateRuleClass rejects a rule whose destination class is always-direct.
// Such a rule can never match the pool path and would be misleading (§1.4).
func validateRuleClass(dst contract.Destination) error {
	if classify(dst, nil) != contract.ClassOther {
		return invalidDestination("rule destination is local or LAN; those classes are always direct")
	}
	return nil
}

// validateRulePrefix rejects default routes and any prefix that covers a range
// classified Local or LAN. Overlapping such a range would make part or all of
// the rule ineffective (docs/contracts.md §1.4, §1.5).
func validateRulePrefix(p netip.Prefix) error {
	if !p.IsValid() || p != p.Masked() {
		return invalidDestination("CIDR rule is not a valid masked prefix")
	}
	if p.Addr().Zone() != "" {
		return invalidDestination("zone-qualified addresses are not accepted")
	}
	if p.Addr().Is4In6() {
		return invalidDestination("CIDR rule must be unmapped")
	}
	if p.Bits() == 0 {
		return invalidDestination("default route is too broad to approve")
	}
	if prefixOverlapsPrivileged(p) {
		return invalidDestination("CIDR rule covers a local or LAN range")
	}
	return nil
}

// ValidateRule validates a complete persisted rule: kind and operand must be
// canonical, never a port, never Local/LAN, never a public suffix, and the rule
// must carry the id and the user origin required by the contract. Origin is
// never used by Match.
func ValidateRule(r contract.Rule) error {
	if r.ID == "" {
		return invalidDestination("rule has no id")
	}
	if strings.ContainsFunc(string(r.ID), unicode.IsControl) || strings.ContainsFunc(r.Note, unicode.IsControl) {
		return invalidDestination("rule id or note contains control characters")
	}
	if r.Origin != contract.RuleOriginUser {
		return invalidDestination("rule origin must be user; diagnostics produce proposals, not rules")
	}

	var raw string
	switch r.Kind {
	case contract.RuleDomain, contract.RuleDomainSuffix:
		if r.Host == "" || r.Addr.IsValid() || r.Prefix.IsValid() {
			return invalidDestination("domain rule has inconsistent fields")
		}
		raw = r.Host
	case contract.RuleIP:
		if r.Host != "" || r.Prefix.IsValid() || !r.Addr.IsValid() {
			return invalidDestination("IP rule has inconsistent fields")
		}
		raw = r.Addr.String()
	case contract.RuleCIDR:
		if r.Host != "" || r.Addr.IsValid() || !r.Prefix.IsValid() {
			return invalidDestination("CIDR rule has inconsistent fields")
		}
		raw = r.Prefix.String()
	default:
		return invalidDestination("unknown rule kind")
	}

	canonical, err := NormalizeRuleAs(r.Kind, raw)
	if err != nil {
		return err
	}
	if canonical.Host != r.Host || canonical.Addr != r.Addr || canonical.Prefix != r.Prefix {
		return invalidDestination("rule operand is not in canonical form")
	}
	return nil
}

// ruleKey is the policy identity of a rule: fields that can change matching or
// the reported RuleID. Origin, Note and AddedAt are deliberately excluded so a
// metadata edit does not create a new PolicyVersion.
func ruleKey(r contract.Rule) string {
	switch r.Kind {
	case contract.RuleDomain, contract.RuleDomainSuffix:
		return fmt.Sprintf("%s\x00%s\x00%s", r.ID, r.Kind, r.Host)
	case contract.RuleIP:
		return fmt.Sprintf("%s\x00%s\x00%s", r.ID, r.Kind, r.Addr)
	case contract.RuleCIDR:
		return fmt.Sprintf("%s\x00%s\x00%s", r.ID, r.Kind, r.Prefix)
	default:
		return fmt.Sprintf("%s\x00%s", r.ID, r.Kind)
	}
}
