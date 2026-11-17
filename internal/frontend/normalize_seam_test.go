package frontend

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// TestNormalizerSeesUntouchedAuthority proves the frontend never parses,
// lowercases or resolves a destination itself: routing.NormalizeDestination
// receives the client's authority exactly as sent on all three request kinds.
func TestNormalizerSeesUntouchedAuthority(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	normalize := func(authority string) (contract.Destination, error) {
		mu.Lock()
		seen = append(seen, authority)
		mu.Unlock()
		return contract.Destination{
			Kind: contract.DestIP,
			Addr: netip.MustParseAddr("127.0.0.1"),
			Port: 1,
		}, nil
	}
	s := newStack(t, func(s *testStack) { s.normalize = normalize })

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT Example.INVALID:443 HTTP/1.1\r\nHost: Example.INVALID:443\r\n\r\n")
	_ = readAllWithDeadline(t, c, 2*time.Second)

	c2 := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c2, "GET http://Example.INVALID:8080/path HTTP/1.1\r\nHost: Example.INVALID:8080\r\nConnection: close\r\n\r\n")
	_ = readAllWithDeadline(t, c2, 2*time.Second)

	c3 := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c3)
	mustWrite(t, c3, string(socksConnectDomain("socks-target.invalid", 443)))
	if code, _ := readSOCKSReply(t, c3); code != 0x04 {
		t.Fatalf("reply = 0x%02x", code)
	}

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	want := map[string]bool{
		"Example.INVALID:443":      true,
		"Example.INVALID:8080":     true,
		"socks-target.invalid:443": true,
	}
	if len(got) != len(want) {
		t.Fatalf("normalizer calls = %v", got)
	}
	for _, a := range got {
		if !want[a] {
			t.Fatalf("normalizer saw %q; authorities = %v", a, got)
		}
	}
}
