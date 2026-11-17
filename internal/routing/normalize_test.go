package routing

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"routelatch/internal/contract"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("test setup: parse addr %q: %v", s, err)
	}
	return a.Unmap()
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("test setup: parse prefix %q: %v", s, err)
	}
	p = p.Masked()
	a, bits := p.Addr(), p.Bits()
	if a.Is4In6() {
		a = a.Unmap()
		bits -= 96
		p = netip.PrefixFrom(a, bits).Masked()
	}
	return p
}

func domainDst(host string, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestDomain, Host: host, Port: port}
}

func ipDst(addr netip.Addr, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestIP, Addr: addr, Port: port}
}

func cidrDst(p netip.Prefix) contract.Destination {
	return contract.Destination{Kind: contract.DestCIDR, Addr: p.Addr(), Prefix: p}
}

func canonicalText(dst contract.Destination) string {
	switch dst.Kind {
	case contract.DestDomain:
		if dst.Port != 0 {
			return dst.Host + ":" + strconv.Itoa(int(dst.Port))
		}
		return dst.Host
	case contract.DestIP:
		if dst.Port != 0 {
			if dst.Addr.Is6() {
				return "[" + dst.Addr.String() + "]:" + strconv.Itoa(int(dst.Port))
			}
			return dst.Addr.String() + ":" + strconv.Itoa(int(dst.Port))
		}
		return dst.Addr.String()
	case contract.DestCIDR:
		return dst.Prefix.String()
	default:
		return ""
	}
}

func TestNormalizeDestinationBoundaryTable(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		want      contract.Destination
		wantClass contract.DestClass
		wantErr   contract.ErrorKind
	}{
		// §1.6 domain normalization.
		{name: "uppercase", raw: "API.Example.COM", want: domainDst("api.example.com", 0), wantClass: contract.ClassOther},
		{name: "trailing dot", raw: "api.example.com.", want: domainDst("api.example.com", 0), wantClass: contract.ClassOther},
		{name: "idn", raw: "bücher.de", want: domainDst("xn--bcher-kva.de", 0), wantClass: contract.ClassOther},
		{name: "punycode stable", raw: "XN--BCHER-KVA.DE", want: domainDst("xn--bcher-kva.de", 0), wantClass: contract.ClassOther},
		{name: "private suffix child", raw: "foo.github.io", want: domainDst("foo.github.io", 0), wantClass: contract.ClassOther},
		{name: "single label lan", raw: "example", want: domainDst("example", 0), wantClass: contract.ClassLAN},
		{name: "single label intranet", raw: "intranet", want: domainDst("intranet", 0), wantClass: contract.ClassLAN},
		{name: "localhost", raw: "localhost", want: domainDst("localhost", 0), wantClass: contract.ClassLocal},
		{name: "localhost subdomain", raw: "api.localhost", want: domainDst("api.localhost", 0), wantClass: contract.ClassLocal},
		{name: "domain with port", raw: "api.example.com:443", want: domainDst("api.example.com", 443), wantClass: contract.ClassOther},
		{name: "bracketed ipv6", raw: "[::1]", want: ipDst(mustAddr(t, "::1"), 0), wantClass: contract.ClassLocal},
		{name: "label max 63", raw: strings.Repeat("a", 63) + ".com", want: domainDst(strings.Repeat("a", 63)+".com", 0), wantClass: contract.ClassOther},
		{name: "port one", raw: "example.com:1", want: domainDst("example.com", 1), wantClass: contract.ClassOther},
		{name: "port max", raw: "203.0.113.8:65535", want: ipDst(mustAddr(t, "203.0.113.8"), 65535), wantClass: contract.ClassOther},
		{name: "bracketed ipv6 port max", raw: "[::1]:65535", want: ipDst(mustAddr(t, "::1"), 65535), wantClass: contract.ClassLocal},
		{name: "public ipv6 port", raw: "[2001:db8::1]:443", want: ipDst(mustAddr(t, "2001:db8::1"), 443), wantClass: contract.ClassOther},
		{name: "unbracketed ipv6 has no port", raw: "2001:db8::1:443", want: ipDst(mustAddr(t, "2001:db8::1:443"), 0), wantClass: contract.ClassOther},
		{name: "loopback", raw: "127.0.0.1", want: ipDst(mustAddr(t, "127.0.0.1"), 0), wantClass: contract.ClassLocal},
		{name: "loopback with port", raw: "127.0.0.1:80", want: ipDst(mustAddr(t, "127.0.0.1"), 80), wantClass: contract.ClassLocal},
		{name: "private ip", raw: "192.168.1.10:443", want: ipDst(mustAddr(t, "192.168.1.10"), 443), wantClass: contract.ClassLAN},
		{name: "public ip", raw: "203.0.113.8:443", want: ipDst(mustAddr(t, "203.0.113.8"), 443), wantClass: contract.ClassOther},
		{name: "link local v4", raw: "169.254.1.1", want: ipDst(mustAddr(t, "169.254.1.1"), 0), wantClass: contract.ClassLocal},
		{name: "link local v6", raw: "fe80::1", want: ipDst(mustAddr(t, "fe80::1"), 0), wantClass: contract.ClassLocal},
		{name: "ula", raw: "fd00::1", want: ipDst(mustAddr(t, "fd00::1"), 0), wantClass: contract.ClassLAN},
		{name: "cgnat", raw: "100.64.0.1", want: ipDst(mustAddr(t, "100.64.0.1"), 0), wantClass: contract.ClassLAN},
		{name: "multicast v4", raw: "224.0.0.1", want: ipDst(mustAddr(t, "224.0.0.1"), 0), wantClass: contract.ClassLocal},
		{name: "multicast v6", raw: "ff02::1", want: ipDst(mustAddr(t, "ff02::1"), 0), wantClass: contract.ClassLocal},
		{name: "limited broadcast", raw: "255.255.255.255", want: ipDst(mustAddr(t, "255.255.255.255"), 0), wantClass: contract.ClassLocal},
		{name: "mapped private", raw: "::ffff:192.168.1.1", want: ipDst(mustAddr(t, "192.168.1.1"), 0), wantClass: contract.ClassLAN},
		{name: "mapped public", raw: "::ffff:8.8.8.8", want: ipDst(mustAddr(t, "8.8.8.8"), 0), wantClass: contract.ClassOther},
		{name: "mapped private bracketed port", raw: "[::ffff:192.168.1.1]:443", want: ipDst(mustAddr(t, "192.168.1.1"), 443), wantClass: contract.ClassLAN},
		{name: "cidr", raw: "203.0.113.0/24", want: cidrDst(mustPrefix(t, "203.0.113.0/24")), wantClass: contract.ClassOther},
		{name: "cidr host bits masked", raw: "203.0.113.8/24", want: cidrDst(mustPrefix(t, "203.0.113.0/24")), wantClass: contract.ClassOther},
		{name: "cidr slash 32", raw: "203.0.113.8/32", want: cidrDst(mustPrefix(t, "203.0.113.8/32")), wantClass: contract.ClassOther},
		{name: "cidr v6", raw: "2001:db8::/32", want: cidrDst(mustPrefix(t, "2001:db8::/32")), wantClass: contract.ClassOther},
		{name: "cidr v6 slash 64", raw: "2001:db8:1::/64", want: cidrDst(mustPrefix(t, "2001:db8:1::/64")), wantClass: contract.ClassOther},
		{name: "cidr v6 slash 128", raw: "2001:db8:1::1/128", want: cidrDst(mustPrefix(t, "2001:db8:1::1/128")), wantClass: contract.ClassOther},
		{name: "mapped cidr", raw: "::ffff:192.168.1.0/120", want: cidrDst(mustPrefix(t, "192.168.1.0/24")), wantClass: contract.ClassOther},
		{name: "url with credentials", raw: "https://user:pass@Example.com:8443/p?q=1#f", want: domainDst("example.com", 0), wantClass: contract.ClassOther},
		{name: "url bracketed ipv6", raw: "http://[2001:db8::1]/", want: ipDst(mustAddr(t, "2001:db8::1"), 0), wantClass: contract.ClassOther},
		{name: "uppercase scheme and trailing dot", raw: "HTTPS://EXAMPLE.COM./path", want: domainDst("example.com", 0), wantClass: contract.ClassOther},
		{name: "url trailing dot and port", raw: "http://example.com.:8443/", want: domainDst("example.com", 0), wantClass: contract.ClassOther},
		{name: "url loopback", raw: "http://127.0.0.1/", want: ipDst(mustAddr(t, "127.0.0.1"), 0), wantClass: contract.ClassLocal},
		{name: "url localhost", raw: "http://localhost/", want: domainDst("localhost", 0), wantClass: contract.ClassLocal},

		// §1.6 rejections.
		{name: "empty", raw: "", wantErr: contract.InvalidDestination},
		{name: "spaces", raw: "   ", wantErr: contract.InvalidDestination},
		{name: "too long", raw: strings.Repeat("a", maxDestinationBytes+1), wantErr: contract.InvalidDestination},
		{name: "double trailing dot", raw: "api.example.com..", wantErr: contract.InvalidDestination},
		{name: "leading dot", raw: ".example.com", wantErr: contract.InvalidDestination},
		{name: "empty inner label", raw: "api..example.com", wantErr: contract.InvalidDestination},
		{name: "wildcard", raw: "*.example.com", wantErr: contract.InvalidDestination},
		{name: "wildcard only", raw: "*", wantErr: contract.InvalidDestination},
		{name: "tld", raw: "com", wantErr: contract.InvalidDestination},
		{name: "public suffix", raw: "co.uk", wantErr: contract.InvalidDestination},
		{name: "private suffix", raw: "github.io", wantErr: contract.InvalidDestination},
		{name: "icann single label", raw: "dev", wantErr: contract.InvalidDestination},
		{name: "unspecified v4", raw: "0.0.0.0", wantErr: contract.InvalidDestination},
		{name: "unspecified v6", raw: "::", wantErr: contract.InvalidDestination},
		{name: "unspecified v4 with port", raw: "0.0.0.0:80", wantErr: contract.InvalidDestination},
		{name: "unspecified v6 with port", raw: "[::]:80", wantErr: contract.InvalidDestination},
		{name: "mapped unspecified", raw: "::ffff:0.0.0.0", wantErr: contract.InvalidDestination},
		{name: "legacy 127.1", raw: "127.1", wantErr: contract.InvalidDestination},
		{name: "legacy integer", raw: "2130706433", wantErr: contract.InvalidDestination},
		{name: "legacy hex", raw: "0x7f.1", wantErr: contract.InvalidDestination},
		{name: "legacy octal", raw: "0177.0.0.1", wantErr: contract.InvalidDestination},
		{name: "bad dotted quad", raw: "999.999.999.999", wantErr: contract.InvalidDestination},
		{name: "five part numeric", raw: "1.2.3.4.5", wantErr: contract.InvalidDestination},
		{name: "zone", raw: "fe80::1%eth0", wantErr: contract.InvalidDestination},
		{name: "zone in prefix", raw: "fe80::1%eth0/64", wantErr: contract.InvalidDestination},
		{name: "non numeric port", raw: "example.com:https", wantErr: contract.InvalidDestination},
		{name: "port zero", raw: "example.com:0", wantErr: contract.InvalidDestination},
		{name: "port too large", raw: "example.com:65536", wantErr: contract.InvalidDestination},
		{name: "empty port", raw: "example.com:", wantErr: contract.InvalidDestination},
		{name: "bracket port zero", raw: "[::1]:0", wantErr: contract.InvalidDestination},
		{name: "bracket port too large", raw: "[::1]:65536", wantErr: contract.InvalidDestination},
		{name: "text after bracket", raw: "[::1]junk", wantErr: contract.InvalidDestination},
		{name: "missing closing bracket", raw: "[::1", wantErr: contract.InvalidDestination},
		{name: "bracketed non numeric port", raw: "[::1]:https", wantErr: contract.InvalidDestination},
		{name: "bracketed junk port", raw: "[::1]:443x", wantErr: contract.InvalidDestination},
		{name: "url zone", raw: "http://[fe80::1%25eth0]/", wantErr: contract.InvalidDestination},
		{name: "two ports", raw: "[::1]:80:90", wantErr: contract.InvalidDestination},
		{name: "bracket ipv4", raw: "[127.0.0.1]", wantErr: contract.InvalidDestination},
		{name: "bracket name", raw: "[example.com]", wantErr: contract.InvalidDestination},
		{name: "empty bracket", raw: "[]", wantErr: contract.InvalidDestination},
		{name: "junk after cidr", raw: "203.0.113.0/24:80", wantErr: contract.InvalidDestination},
		{name: "prefix too long", raw: "203.0.113.8/33", wantErr: contract.InvalidDestination},
		{name: "mapped prefix too wide", raw: "::ffff:0:0/95", wantErr: contract.InvalidDestination},
		{name: "path without scheme", raw: "example.com/path", wantErr: contract.InvalidDestination},
		{name: "query without scheme", raw: "example.com?x", wantErr: contract.InvalidDestination},
		{name: "fragment without scheme", raw: "example.com#f", wantErr: contract.InvalidDestination},
		{name: "userinfo without scheme", raw: "example.com@evil", wantErr: contract.InvalidDestination},
		{name: "inner space", raw: "exa mple.com", wantErr: contract.InvalidDestination},
		{name: "inner tab", raw: "exa\tmple.com", wantErr: contract.InvalidDestination},
		{name: "leading hyphen", raw: "-bad.example.com", wantErr: contract.InvalidDestination},
		{name: "trailing hyphen", raw: "bad-.example.com", wantErr: contract.InvalidDestination},
		{name: "underscore", raw: "under_score.example.com", wantErr: contract.InvalidDestination},
		{name: "numeric last label", raw: "example.123", wantErr: contract.InvalidDestination},
		{name: "label too long", raw: strings.Repeat("a", 64) + ".example.com", wantErr: contract.InvalidDestination},
		{name: "name too long", raw: strings.Join([]string{
			strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60),
			strings.Repeat("d", 60), strings.Repeat("e", 60), "com",
		}, "."), wantErr: contract.InvalidDestination},
		{name: "url empty host", raw: "https://", wantErr: contract.InvalidDestination},
		{name: "url empty host with user", raw: "http://user@/path", wantErr: contract.InvalidDestination},
		{name: "url bad port", raw: "http://example.com:abc/", wantErr: contract.InvalidDestination},
		{name: "url bad escape", raw: "http://exa%20mple.com/", wantErr: contract.InvalidDestination},

		// Unsupported inputs (§1.3).
		{name: "ftp", raw: "ftp://example.com", wantErr: contract.Unsupported},
		{name: "file", raw: "file:///etc/passwd", wantErr: contract.Unsupported},
		{name: "vless url", raw: "vless://uuid@host:443", wantErr: contract.Unsupported},
		{name: "vmess url", raw: "vmess://payload", wantErr: contract.Unsupported},
		{name: "trojan url", raw: "trojan://pass@host:443", wantErr: contract.Unsupported},
		{name: "ss url", raw: "ss://method:pass@host:443", wantErr: contract.Unsupported},
		{name: "socks url", raw: "socks://host:1080", wantErr: contract.Unsupported},
		{name: "socks5 url", raw: "socks5://host:1080", wantErr: contract.Unsupported},
		{name: "scheme-like vless", raw: "vless:uuid@host:443", wantErr: contract.Unsupported},
		{name: "scheme-like uppercase", raw: "VLESS:uuid@host:443", wantErr: contract.Unsupported},
		{name: "scheme-like simple", raw: "foo:bar", wantErr: contract.Unsupported},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDestination(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeDestination(%q) = %+v, want error %s", tc.raw, got, tc.wantErr)
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
				return
			}
			if err != nil {
				t.Fatalf("NormalizeDestination(%q): unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeDestination(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			if tc.wantClass != "" {
				if class := classify(got, nil); class != tc.wantClass {
					t.Fatalf("class(%q) = %s, want %s", tc.raw, class, tc.wantClass)
				}
			}
		})
	}
}

func TestNormalizeDestinationClassBoundaries(t *testing.T) {
	tests := []struct {
		raw  string
		want contract.DestClass
	}{
		{"127.255.255.254", contract.ClassLocal},
		{"::1", contract.ClassLocal},
		{"10.0.0.1", contract.ClassLAN},
		{"172.16.0.1", contract.ClassLAN},
		{"172.31.255.255", contract.ClassLAN},
		{"172.32.0.1", contract.ClassOther},
		{"192.168.255.255", contract.ClassLAN},
		{"100.127.255.255", contract.ClassLAN},
		{"100.128.0.1", contract.ClassOther},
		{"fc00::1", contract.ClassLAN},
		{"fdff::1", contract.ClassLAN},
		{"fec0::1", contract.ClassLAN},
		{"2001:db8::1", contract.ClassOther},
		{"8.8.8.8", contract.ClassOther},
		{"example.com", contract.ClassOther},
		{"example", contract.ClassLAN},
		{"notlocalhost", contract.ClassLAN},
		{"not.localhost", contract.ClassLocal},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			dst, err := NormalizeDestination(tc.raw)
			if err != nil {
				t.Fatalf("normalize %q: %v", tc.raw, err)
			}
			if got := classify(dst, nil); got != tc.want {
				t.Fatalf("class(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSelfClassRequiresOwnListenerPort(t *testing.T) {
	ports := map[uint16]struct{}{8317: {}, 8318: {}}
	tests := []struct {
		raw  string
		want contract.DestClass
	}{
		{"127.0.0.1:8317", contract.ClassSelf},
		{"[::1]:8318", contract.ClassSelf},
		{"localhost:8317", contract.ClassSelf},
		{"api.localhost:8318", contract.ClassSelf},
		{"127.0.0.1:9999", contract.ClassLocal},
		{"localhost:9999", contract.ClassLocal},
		{"203.0.113.8:8317", contract.ClassOther},
		{"example.com:8317", contract.ClassOther},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			dst, err := NormalizeDestination(tc.raw)
			if err != nil {
				t.Fatalf("normalize %q: %v", tc.raw, err)
			}
			if got := classify(dst, ports); got != tc.want {
				t.Fatalf("class(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsInvalidUTF8WithoutPanic(t *testing.T) {
	raw := string([]byte{0xff, 0xfe, 0xfd})
	if utf8.ValidString(raw) {
		t.Fatal("test setup: expected invalid UTF-8")
	}
	if _, err := NormalizeDestination(raw); err == nil {
		t.Fatal("invalid UTF-8 hostname was accepted")
	}
}

func TestNormalizeDestinationIsIdempotent(t *testing.T) {
	inputs := []string{
		"API.Example.COM",
		"api.example.com.",
		"bücher.de",
		"example",
		"localhost",
		"api.localhost.",
		"203.0.113.8",
		"203.0.113.8:443",
		"[2001:db8::1]:443",
		"2001:db8::1:443",
		"::ffff:192.168.1.1",
		"203.0.113.8/24",
		"::ffff:192.168.1.0/120",
		"2001:db8:1::/64",
		"https://user:pass@Example.com:8443/p?q=1#f",
	}
	for _, raw := range inputs {
		first := mustNormalize(t, raw)
		text := canonicalText(first)
		second, err := NormalizeDestination(text)
		if err != nil {
			t.Fatalf("re-normalize %q (from %q): %v", text, raw, err)
		}
		if second != first {
			t.Fatalf("idempotency failed for %q: %+v -> %+v", raw, first, second)
		}
	}
}

func TestShareLinkErrorsPointToImport(t *testing.T) {
	inputs := []string{
		"vless://uuid@host:443",
		"vmess://payload",
		"trojan://pass@host:443",
		"ss://method:pass@host:443",
		"socks://host:1080",
		"socks5://host:1080",
		"vless:uuid@host:443",
		"vless://uuid@host:bad/",
		"VLESS://uuid@host:bad/",
		"ss://%%zz",
		"trojan://",
	}
	for _, raw := range inputs {
		_, err := NormalizeRule(raw)
		if err == nil {
			t.Fatalf("NormalizeRule(%q) unexpectedly succeeded", raw)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) {
			t.Fatalf("NormalizeRule(%q): error is not *contract.Failure: %v", raw, err)
		}
		if failure.Kind != contract.Unsupported {
			t.Fatalf("NormalizeRule(%q): kind = %s, want unsupported", raw, failure.Kind)
		}
		if !strings.Contains(failure.Message, "import") {
			t.Fatalf("NormalizeRule(%q): message %q does not point to import", raw, failure.Message)
		}
	}
}
