package routing

import (
	"strings"

	"routelatch/internal/contract"
)

// Match returns the most specific rule that matches dst. It is pure and
// deterministic: input order, Origin, Note and AddedAt never affect the result.
//
// Precedence is by specificity, with the RuleID as the final deterministic
// tie-break:
//   - an exact IP rule beats any CIDR; otherwise the longest prefix wins;
//   - a longer domain suffix wins; an exact domain rule beats a suffix rule for
//     the same hostname;
//   - among equally specific rules, the lexicographically smaller RuleID wins.
func Match(rules []contract.Rule, dst contract.Destination) (contract.Rule, bool) {
	var best contract.Rule
	found := false
	for _, r := range rules {
		if !ruleMatches(r, dst) {
			continue
		}
		if !found || moreSpecific(r, best, dst) {
			best = r
			found = true
		}
	}
	return best, found
}

func ruleMatches(r contract.Rule, dst contract.Destination) bool {
	switch dst.Kind {
	case contract.DestDomain:
		switch r.Kind {
		case contract.RuleDomain:
			return dst.Host == r.Host
		case contract.RuleDomainSuffix:
			return dst.Host == r.Host || strings.HasSuffix(dst.Host, "."+r.Host)
		}
	case contract.DestIP:
		addr := dst.Addr.Unmap()
		switch r.Kind {
		case contract.RuleIP:
			return r.Addr.Unmap() == addr
		case contract.RuleCIDR:
			return r.Prefix.Contains(addr)
		}
	}
	return false
}

// moreSpecific reports whether a should replace b. It assumes both match dst.
func moreSpecific(a, b contract.Rule, dst contract.Destination) bool {
	switch dst.Kind {
	case contract.DestDomain:
		if len(a.Host) != len(b.Host) {
			return len(a.Host) > len(b.Host)
		}
		ar, br := domainRuleRank(a), domainRuleRank(b)
		if ar != br {
			return ar < br
		}
	case contract.DestIP:
		ar, br := ipRuleRank(a), ipRuleRank(b)
		if ar != br {
			return ar < br
		}
		ab, bb := ipRuleBits(a), ipRuleBits(b)
		if ab != bb {
			return ab > bb
		}
	}
	return a.ID < b.ID
}

func domainRuleRank(r contract.Rule) int {
	if r.Kind == contract.RuleDomain {
		return 0
	}
	return 1
}

func ipRuleRank(r contract.Rule) int {
	if r.Kind == contract.RuleIP {
		return 0
	}
	return 1
}

func ipRuleBits(r contract.Rule) int {
	if r.Kind == contract.RuleCIDR {
		return r.Prefix.Bits()
	}
	return 0
}
