package routing

import (
	"net/netip"
	"strings"

	"routelatch/internal/contract"
)

var (
	cgnatPrefix      = netip.MustParsePrefix("100.64.0.0/10")
	siteLocalPrefix  = netip.MustParsePrefix("fec0::/10")
	limitedBroadcast = netip.MustParseAddr("255.255.255.255")
)

// localPrefixes and lanPrefixes are the ranges that the class table makes
// always-direct. Rules may never cover them; CIDR rule validation uses the same
// list through prefixOverlapsPrivileged.
var (
	localPrefixes = []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("255.255.255.255/32"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
	}
	lanPrefixes = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		cgnatPrefix,
		netip.MustParsePrefix("fc00::/7"),
		siteLocalPrefix,
	}
)

// classify applies Self > Local > LAN and otherwise returns Other. The caller
// promotes Other to Approved only after a rule match, so no rule can ever make
// a Local or LAN destination routable (docs/contracts.md §1.4).
func classify(dst contract.Destination, selfPorts map[uint16]struct{}) contract.DestClass {
	switch dst.Kind {
	case contract.DestDomain:
		host := dst.Host
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			if isSelfPort(dst.Port, selfPorts) {
				return contract.ClassSelf
			}
			return contract.ClassLocal
		}
		if !strings.Contains(host, ".") {
			return contract.ClassLAN
		}
		return contract.ClassOther
	case contract.DestIP:
		addr := dst.Addr.Unmap()
		if addr.IsLoopback() {
			if isSelfPort(dst.Port, selfPorts) {
				return contract.ClassSelf
			}
			return contract.ClassLocal
		}
		if addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr == limitedBroadcast {
			return contract.ClassLocal
		}
		if addr.IsPrivate() || cgnatPrefix.Contains(addr) || siteLocalPrefix.Contains(addr) {
			return contract.ClassLAN
		}
		return contract.ClassOther
	default:
		// CIDR is a rule operand, never a dial target; it is classified with
		// the public destinations so no rule promotion is implied.
		return contract.ClassOther
	}
}

func isSelfPort(port uint16, selfPorts map[uint16]struct{}) bool {
	if port == 0 || len(selfPorts) == 0 {
		return false
	}
	_, ok := selfPorts[port]
	return ok
}

func prefixOverlapsPrivileged(p netip.Prefix) bool {
	for _, q := range localPrefixes {
		if p.Overlaps(q) {
			return true
		}
	}
	for _, q := range lanPrefixes {
		if p.Overlaps(q) {
			return true
		}
	}
	return false
}
