package routing

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"routelatch/internal/contract"
)

const (
	maxDestinationBytes = 2048
	maxHostnameBytes    = 253
	maxLabelBytes       = 63
)

// NormalizeDestination parses and canonicalizes one dial target following
// docs/contracts.md §1.2. It accepts a hostname, an IP literal (including a
// bracketed IPv6 literal with an optional port), a CIDR range, or an http/https
// URL. URL userinfo, path, query, fragment and port are discarded.
func NormalizeDestination(raw string) (contract.Destination, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxDestinationBytes {
		return contract.Destination{}, invalidDestination("destination is empty or longer than 2048 bytes")
	}
	if strings.Contains(s, "://") {
		if isShareLink(s) {
			return contract.Destination{}, unsupportedDestination("proxy share links must be imported; use the import command")
		}
		return normalizeURL(s)
	}
	if strings.Contains(s, "%") {
		return contract.Destination{}, invalidDestination("percent-encoded or zone-qualified input is not accepted")
	}
	if strings.HasPrefix(s, "[") {
		return normalizeBracketed(s)
	}
	if strings.Contains(s, "/") {
		return normalizeCIDR(s)
	}
	if addr, err := normalizeAddr(s); err == nil {
		return ipDestination(addr, 0), nil
	}

	switch strings.Count(s, ":") {
	case 0:
		return normalizeDomain(s, 0)
	case 1:
		i := strings.IndexByte(s, ':')
		host, portText := s[:i], s[i+1:]
		if isASCIIDigits(portText) {
			port, err := parsePort(portText)
			if err != nil {
				return contract.Destination{}, invalidDestination("port is outside 1..65535")
			}
			if addr, err := normalizeAddr(host); err == nil {
				return ipDestination(addr, port), nil
			}
			return normalizeDomain(host, port)
		}
		if !strings.Contains(host, ".") && schemeLike(host) {
			return contract.Destination{}, unsupportedDestination("scheme-like input is not a destination; use import for proxy links")
		}
		return contract.Destination{}, invalidDestination("port is not numeric")
	default:
		first := s[:strings.IndexByte(s, ':')]
		if !strings.Contains(first, ".") && schemeLike(first) {
			return contract.Destination{}, unsupportedDestination("scheme-like input is not a destination; use import for proxy links")
		}
		return contract.Destination{}, invalidDestination("invalid IPv6 literal or port syntax; bracket IPv6 addresses")
	}
}

// normalizeURL extracts only the hostname from an http/https URL. The path,
// query, fragment and userinfo are deliberately never retained, logged or
// returned (docs/contracts.md §1.3).
func normalizeURL(s string) (contract.Destination, error) {
	u, err := url.Parse(s)
	if err != nil {
		return contract.Destination{}, invalidDestination("URL cannot be parsed")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return contract.Destination{}, unsupportedDestination("only http and https URLs are accepted as destinations")
	}
	host := u.Hostname()
	if host == "" {
		return contract.Destination{}, invalidDestination("URL has no hostname")
	}
	// The URL port is intentionally dropped: a rule is port-agnostic and the
	// target port comes from the client's CONNECT/SOCKS request (§1.3).
	return normalizeHost(host, 0)
}

func normalizeHost(host string, port uint16) (contract.Destination, error) {
	if addr, err := normalizeAddr(host); err == nil {
		return ipDestination(addr, port), nil
	}
	return normalizeDomain(host, port)
}

func normalizeBracketed(s string) (contract.Destination, error) {
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return contract.Destination{}, invalidDestination("bracketed address is missing ]")
	}
	inner := s[1:end]
	rest := s[end+1:]
	if inner == "" {
		return contract.Destination{}, invalidDestination("empty bracketed address")
	}
	parsed, err := netip.ParseAddr(inner)
	if err != nil || !parsed.Is6() {
		return contract.Destination{}, invalidDestination("brackets are only valid around an IPv6 literal")
	}
	addr, err := normalizeAddr(inner)
	if err != nil {
		return contract.Destination{}, invalidDestination("bracketed address is not a usable IP literal")
	}
	var port uint16
	if rest != "" {
		if rest[0] != ':' {
			return contract.Destination{}, invalidDestination("unexpected text after bracketed address")
		}
		port, err = parsePort(rest[1:])
		if err != nil {
			return contract.Destination{}, invalidDestination("port is outside 1..65535")
		}
	}
	return ipDestination(addr, port), nil
}

func normalizeCIDR(s string) (contract.Destination, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return contract.Destination{}, invalidDestination("CIDR cannot be parsed")
	}
	if p.Addr().Zone() != "" {
		return contract.Destination{}, invalidDestination("zone-qualified addresses are not accepted")
	}
	addr, bits := p.Addr(), p.Bits()
	if addr.Is4In6() {
		// Reduce a mapped IPv4 prefix by the 96-bit prefix that mapped it.
		if bits < 96 {
			return contract.Destination{}, invalidDestination("IPv4-mapped prefix is too wide")
		}
		addr = addr.Unmap()
		bits -= 96
	}
	p = netip.PrefixFrom(addr, bits).Masked()
	if !p.IsValid() {
		return contract.Destination{}, invalidDestination("CIDR cannot be normalized")
	}
	return contract.Destination{Kind: contract.DestCIDR, Addr: p.Addr(), Prefix: p}, nil
}

// normalizeAddr is the single IP-literal path used by every branch. It rejects
// zones and unspecified addresses and unmaps IPv4-mapped IPv6 literals.
func normalizeAddr(raw string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, err
	}
	if addr.Zone() != "" {
		return netip.Addr{}, errZone
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() {
		return netip.Addr{}, errUnspecified
	}
	return addr, nil
}

// normalizeDomain applies N8–N13: character and label limits, IDNA, one
// optional trailing dot, public-suffix rejection and the numeric-name trap.
func normalizeDomain(host string, port uint16) (contract.Destination, error) {
	if strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return contract.Destination{}, invalidDestination("destination contains whitespace or control characters")
	}
	if strings.ContainsAny(host, "/@?#*[]") {
		return contract.Destination{}, invalidDestination("destination contains a forbidden character")
	}
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if host == "" || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return contract.Destination{}, invalidDestination("destination has an empty label")
	}

	ascii, err := idna.Lookup.ToASCII(strings.ToLower(host))
	if err != nil {
		return contract.Destination{}, invalidDestination("hostname is not valid IDNA")
	}
	ascii = strings.ToLower(ascii)
	if ascii == "" || len(ascii) > maxHostnameBytes {
		return contract.Destination{}, invalidDestination("hostname is empty or longer than 253 bytes")
	}
	labels := strings.Split(ascii, ".")
	for _, label := range labels {
		if label == "" || len(label) > maxLabelBytes {
			return contract.Destination{}, invalidDestination("hostname has an empty or oversized label")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return contract.Destination{}, invalidDestination("hostname label contains a forbidden character")
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return contract.Destination{}, invalidDestination("hostname label starts or ends with a hyphen")
		}
	}

	// A numeric final label that netip did not accept is an old-style numeric
	// address (127.1, 2130706433, 0x7f.1); never reinterpret it (§1.2 N10).
	if isASCIIDigits(labels[len(labels)-1]) {
		return contract.Destination{}, invalidDestination("numeric hostname is not a standard dotted-quad address")
	}

	// localhost is Local before the public-suffix checks (§1.2 N10).
	if ascii == "localhost" || strings.HasSuffix(ascii, ".localhost") {
		return domainDestination(ascii, port), nil
	}

	ps, icann := publicsuffix.PublicSuffix(ascii)
	if len(labels) >= 2 {
		if ps == ascii {
			return contract.Destination{}, invalidDestination("hostname is a public or private suffix")
		}
	} else if icann && ps == ascii {
		return contract.Destination{}, invalidDestination("hostname is a top-level domain")
	}

	return domainDestination(ascii, port), nil
}

func domainDestination(host string, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestDomain, Host: host, Port: port}
}

func ipDestination(addr netip.Addr, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestIP, Addr: addr, Port: port}
}

func parsePort(s string) (uint16, error) {
	if !isASCIIDigits(s) {
		return 0, errPort
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n < 1 || n > 65535 {
		return 0, errPort
	}
	return uint16(n), nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isShareLink recognizes a proxy share-link scheme even when the rest of the
// URL is malformed; those must reach the user as unsupported with import
// guidance, never as a generic parse error (§1.3).
func isShareLink(s string) bool {
	scheme, _, ok := strings.Cut(s, "://")
	if !ok {
		return false
	}
	switch strings.ToLower(scheme) {
	case "vless", "vmess", "trojan", "ss", "socks", "socks5":
		return true
	default:
		return false
	}
}

// schemeLike is the §1.2 N6/N7 heuristic: a dot-free label that starts with a
// letter and contains only scheme characters. It is case-insensitive so
// VLESS://-style input cannot become a hostname by capitalization.
func schemeLike(s string) bool {
	if s == "" {
		return false
	}
	s = strings.ToLower(s)
	c := s[0]
	if c < 'a' || c > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
