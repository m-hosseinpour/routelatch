package socksfront

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	gosocks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// TestLibResolvesDomainBeforeDial is the 1a probe: with the default resolver,
// a domain request is resolved in handle.go before the custom dialer is called.
func TestLibResolvesDomainBeforeDial(t *testing.T) {
	t.Run("unresolvable name never reaches dialer", func(t *testing.T) {
		var calls atomic.Int32
		addr, _ := startLibServer(t, gosocks5.WithDial(
			func(_ context.Context, _, target string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("dial must not be reached")
			}))

		c := dialTest(t, addr)
		greetNoAuth(t, c)
		start := time.Now()
		writeAll(t, c, domainRequest(0x01, "socks-target.invalid", 443))
		reply := readReply(t, c)
		t.Logf("resolution failure after %v, reply=%s", time.Since(start).Round(time.Millisecond), hex(reply.Raw))

		if reply.Rep != 0x04 {
			t.Fatalf("reply code: got %#x want 0x04 (host unreachable)", reply.Rep)
		}
		if got := hex(reply.Raw); got != "05 04 00 01 00 00 00 00 00 00" {
			t.Fatalf("reply bytes: got %s", got)
		}
		if n := calls.Load(); n != 0 {
			t.Fatalf("custom dialer called %d times before local resolution finished", n)
		}
		expectClosed(t, c, time.Second, "after resolution failure")
	})

	t.Run("resolvable name reaches dialer as an IP literal", func(t *testing.T) {
		dialed := make(chan string, 1)
		addr, _ := startLibServer(t, gosocks5.WithDial(
			func(_ context.Context, _, target string) (net.Conn, error) {
				dialed <- target
				return nil, errors.New("record only")
			}))

		c := dialTest(t, addr)
		greetNoAuth(t, c)
		writeAll(t, c, domainRequest(0x01, "localhost", 443))
		reply := readReply(t, c)
		if reply.Rep != 0x04 {
			t.Fatalf("reply code: got %#x want 0x04", reply.Rep)
		}
		select {
		case target := <-dialed:
			host, _, err := net.SplitHostPort(target)
			if err != nil {
				t.Fatalf("dial target %q: %v", target, err)
			}
			if net.ParseIP(host) == nil {
				t.Fatalf("dial target %q: host %q is not an IP literal, so the library did not resolve first", target, host)
			}
			t.Logf("localhost resolved locally; custom dialer saw %q", target)
		case <-time.After(testTimeout):
			t.Fatal("dialer was never called")
		}
	})
}

// TestLibHostnamePreservedWithResolverHack is the 1b probe.
func TestLibHostnamePreservedWithResolverHack(t *testing.T) {
	es := startEcho(t)

	var dialAddr, dialFQDN string
	var dialIP net.IP
	addr, _ := startLibServer(t,
		gosocks5.WithResolver(noopResolver{}),
		gosocks5.WithDialAndRequest(func(_ context.Context, _, target string, req *gosocks5.Request) (net.Conn, error) {
			dialAddr = target
			dialFQDN = req.RawDestAddr.FQDN
			dialIP = req.DestAddr.IP
			return net.DialTimeout("tcp", es.addr, testTimeout)
		}),
	)

	req := domainRequest(0x01, "socks-target.invalid", 443)
	payload := []byte("early-bytes")
	// Greeting, request and payload in one write: early data has to survive the
	// library's buffered request reader.
	oneWrite := append(append([]byte{0x05, 0x01, 0x00}, req...), payload...)

	c := dialTest(t, addr)
	writeAll(t, c, oneWrite)
	expectBytes(t, c, []byte{0x05, 0x00}, "method reply")
	reply := readReply(t, c)
	if reply.Rep != 0x00 {
		t.Fatalf("reply code: got %#x want success; raw=%s", reply.Rep, hex(reply.Raw))
	}
	got := readExact(t, c, len(payload))
	if string(got) != string(payload) {
		t.Fatalf("early payload: got %q want %q", got, payload)
	}

	if dialFQDN != "socks-target.invalid" {
		t.Fatalf("RawDestAddr.FQDN: got %q", dialFQDN)
	}
	if dialIP != nil {
		t.Fatalf("DestAddr.IP: got %v, want nil (unresolved)", dialIP)
	}
	if dialAddr != "socks-target.invalid:443" {
		t.Fatalf("dial target: got %q want %q", dialAddr, "socks-target.invalid:443")
	}
	t.Logf("dial target=%q RawDestAddr.FQDN=%q DestAddr.IP=nil", dialAddr, dialFQDN)

	spec := statute.AddrSpec{FQDN: "socks-target.invalid", Port: 443}
	if got := spec.String(); got != "socks-target.invalid:443" {
		t.Fatalf("AddrSpec.String with nil IP: got %q", got)
	}
}

// TestLibReplyOnlyAfterDial is the 1c probe: success, error-before-reply, and
// failure after success.
func TestLibReplyOnlyAfterDial(t *testing.T) {
	t.Run("no success reply while dial is blocked", func(t *testing.T) {
		es := startEcho(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		addr, _ := startLibServer(t, gosocks5.WithDial(
			func(_ context.Context, _, _ string) (net.Conn, error) {
				close(entered)
				<-release
				return net.DialTimeout("tcp", es.addr, testTimeout)
			}))

		c := dialTest(t, addr)
		greetNoAuth(t, c)
		writeAll(t, c, ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80))
		select {
		case <-entered:
		case <-time.After(testTimeout):
			t.Fatal("dialer never started")
		}

		if _, err := tryReadExact(c, 1, 250*time.Millisecond); err == nil {
			t.Fatal("received a reply byte while the dial was still blocked")
		} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			t.Fatalf("expected read timeout while dial blocked, got %v", err)
		}
		t.Log("no SOCKS5 reply within the 250ms window while the dial was blocked")

		close(release)
		reply := readReply(t, c)
		if reply.Rep != 0x00 {
			t.Fatalf("reply code after dial: got %#x", reply.Rep)
		}
		writeAll(t, c, []byte("after"))
		if got := readExact(t, c, 5); string(got) != "after" {
			t.Fatalf("post-reply payload: got %q", got)
		}
	})

	t.Run("dial errors map to reply codes and no upstream bytes", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
			want byte
		}{
			{"refused substring", errors.New("connect: connection refused"), 0x05},
			{"network unreachable", errors.New("dial: network is unreachable"), 0x03},
			{"pool unavailable", errors.New("pool unavailable: no healthy exit"), 0x04},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var calls atomic.Int32
				addr, _ := startLibServer(t, gosocks5.WithDial(
					func(_ context.Context, _, _ string) (net.Conn, error) {
						calls.Add(1)
						return nil, tc.err
					}))

				c := dialTest(t, addr)
				writeAll(t, c, append(append([]byte{0x05, 0x01, 0x00}, ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80)...), []byte("pending")...))
				expectBytes(t, c, []byte{0x05, 0x00}, "method reply")
				reply := readReply(t, c)
				t.Logf("dial error %q -> reply %s", tc.err, hex(reply.Raw))
				if reply.Rep != tc.want {
					t.Fatalf("reply code: got %#x want %#x", reply.Rep, tc.want)
				}
				if n := calls.Load(); n != 1 {
					t.Fatalf("dial calls: got %d want 1 (no retry)", n)
				}
				expectClosed(t, c, time.Second, "after dial error")
			})
		}
	})

	t.Run("failure after success has no second reply", func(t *testing.T) {
		peer, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen peer: %v", err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		go func() {
			for {
				conn, err := peer.Accept()
				if err != nil {
					return
				}
				_ = conn.Close() // accept then immediately close
			}
		}()
		addr, _ := startLibServer(t, gosocks5.WithDial(
			func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("tcp", peer.Addr().String(), testTimeout)
			}))

		c := dialTest(t, addr)
		greetNoAuth(t, c)
		writeAll(t, c, append(ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80), []byte("x")...))
		reply := readReply(t, c)
		if reply.Rep != 0x00 {
			t.Fatalf("reply code: got %#x want success", reply.Rep)
		}
		expectClosed(t, c, time.Second, "after upstream EOF post-success")
		t.Log("success reply then stream close; no error reply is possible after 0x00")
	})
}
