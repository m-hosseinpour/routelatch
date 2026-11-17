package frontend

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func TestSOCKSConnectKeepsHostnameUnresolved(t *testing.T) {
	echo := startEchoServer(t)
	var seen atomic.Value
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(_ context.Context, dst contract.Destination) (net.Conn, error) {
			seen.Store(dst)
			return net.Dial("tcp", echo)
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectDomain("socks-target.invalid", 443)))
	code, _ := readSOCKSReply(t, c)
	if code != 0x00 {
		t.Fatalf("reply = 0x%02x, want 0x00", code)
	}
	mustWrite(t, c, "ping")
	echoed := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, echoed); err != nil || string(echoed) != "ping" {
		t.Fatalf("echo = %q err=%v", echoed, err)
	}

	dst, ok := seen.Load().(contract.Destination)
	if !ok {
		t.Fatal("dialer never saw the destination")
	}
	if dst.Kind != contract.DestDomain || dst.Host != "socks-target.invalid" || dst.Port != 443 {
		t.Fatalf("destination = %+v; hostname was resolved or rewritten", dst)
	}
	specs := s.registry.allSpecs()
	if len(specs) != 1 || specs[0].Kind != contract.ConnSOCKS5Connect {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].Decision.PolicyVersion != 1 {
		t.Fatalf("registered version = %d", specs[0].Decision.PolicyVersion)
	}
	if specs[0].Destination.Host != "socks-target.invalid" {
		t.Fatalf("registered destination = %+v", specs[0].Destination)
	}
}

func TestSOCKSReplaysBytesPipelinedWithRequest(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
			return net.Dial("tcp", echo)
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	// The request and application bytes share one write, as a client may do.
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80))+"EARLY!")
	code, _ := readSOCKSReply(t, c)
	if code != 0x00 {
		t.Fatalf("reply = 0x%02x", code)
	}
	got := make([]byte, 6)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "EARLY!" {
		t.Fatalf("early bytes = %q err=%v", got, err)
	}
}

func TestSOCKSFailureReplyCodes(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*testStack)
		req         []byte
		want        byte
		directCalls int
	}{
		{
			name: "pool unavailable",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
				}
				s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
					return contract.PoolDialResult{}, &contract.Failure{Kind: contract.PoolUnavailable, Path: contract.PathPool, Message: "pool unavailable"}
				}
			},
			req:  socksConnectIP(net.IPv4(127, 0, 0, 1), 80),
			want: 0x04,
		},
		{
			name: "pool timeout",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
				}
				s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
					return contract.PoolDialResult{}, &contract.Failure{Kind: contract.Timeout, Path: contract.PathPool, Message: "deadline exceeded"}
				}
			},
			req:  socksConnectIP(net.IPv4(127, 0, 0, 1), 80),
			want: 0x04,
		},
		{
			name: "pool destination refused stays in the pool family",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
				}
				s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
					return contract.PoolDialResult{}, &contract.Failure{Kind: contract.DestinationRefused, Path: contract.PathPool, Message: "the exit reported a destination refused"}
				}
			},
			req:  socksConnectIP(net.IPv4(127, 0, 0, 1), 80),
			want: 0x04,
		},
		{
			name: "policy denied",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{Route: contract.RouteRefuse, Reason: contract.ReasonSelfLoop, PolicyVersion: 1}, nil
				}
			},
			req:  socksConnectIP(net.IPv4(127, 0, 0, 1), 80),
			want: 0x04,
		},
		{
			name: "direct refused",
			mutate: func(s *testStack) {
				s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
					return nil, &contract.Failure{Kind: contract.ConnectRefused, Path: contract.PathDirect, Message: "dial tcp: connect: connection refused"}
				}
			},
			req:         socksConnectIP(net.IPv4(127, 0, 0, 1), 80),
			want:        0x05,
			directCalls: 1,
		},
		{
			name: "invalid port",
			req:  socksConnectIP(net.IPv4(127, 0, 0, 1), 0),
			want: 0x04,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, tc.mutate)
			c := dialProxy(t, s.f.SOCKSAddr().String())
			socksHandshake(t, c)
			mustWrite(t, c, string(tc.req))
			code, _ := readSOCKSReply(t, c)
			if code != tc.want {
				t.Fatalf("reply = 0x%02x, want 0x%02x", code, tc.want)
			}
			if s.direct.count() != tc.directCalls {
				t.Fatalf("direct dials = %d, want %d (no fallback)", s.direct.count(), tc.directCalls)
			}
		})
	}
}

func TestSOCKSCanceledHasNoReply(t *testing.T) {
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
			return nil, &contract.Failure{Kind: contract.Canceled, Path: contract.PathLocal, Message: "canceled"}
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	n, err := c.Read(one[:])
	if err == nil {
		t.Fatalf("read a byte 0x%02x after cancellation; no reply is allowed", one[0])
	}
	if isTimeoutError(err) {
		t.Fatalf("read timed out (n=%d) instead of the server closing the socket", n)
	}
}

func TestSOCKSBindAndUDPAssociateAreRefused(t *testing.T) {
	for _, cmd := range []byte{0x02, 0x03} {
		t.Run("command "+string(rune('0'+cmd)), func(t *testing.T) {
			s := newStack(t, nil)
			c := dialProxy(t, s.f.SOCKSAddr().String())
			socksHandshake(t, c)
			mustWrite(t, c, string(socksRequest(cmd, net.IPv4(127, 0, 0, 1), 80)))
			code, _ := readSOCKSReply(t, c)
			if code != 0x07 {
				t.Fatalf("command 0x%02x reply = 0x%02x, want 0x07", cmd, code)
			}
			if s.direct.count() != 0 || s.pool.count() != 0 {
				t.Fatalf("auxiliary command dialed: direct=%d pool=%d", s.direct.count(), s.pool.count())
			}
		})
	}

	// A domain-form BIND must be refused without any lookup: the library's
	// resolver hook performs no I/O.
	s := newStack(t, nil)
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	req := []byte{0x05, 0x02, 0x00, 0x03, byte(len("byname.invalid"))}
	req = append(req, []byte("byname.invalid")...)
	req = append(req, 0, 80)
	mustWrite(t, c, string(req))
	code, _ := readSOCKSReply(t, c)
	if code != 0x07 {
		t.Fatalf("domain BIND reply = 0x%02x, want 0x07", code)
	}
}

func TestSOCKSUnknownAddressType(t *testing.T) {
	s := newStack(t, nil)
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, "\x05\x01\x00\x09")
	code, _ := readSOCKSReply(t, c)
	if code != 0x08 {
		t.Fatalf("reply = 0x%02x, want 0x08", code)
	}
	if s.direct.count() != 0 || s.pool.count() != 0 {
		t.Fatalf("unknown ATYP dialed: direct=%d pool=%d", s.direct.count(), s.pool.count())
	}
}

func TestSOCKSPoolSuccessWaitsForProof(t *testing.T) {
	echo := startEchoServer(t)
	release := make(chan struct{})
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonApprovedRule, RuleID: "r-1", PolicyVersion: 1}, nil
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			<-release
			return contract.PoolDialResult{Conn: conn, Exit: "x-1", Tag: "rl-x", Proof: contract.ProofAccepted}, nil
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))

	_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var one [1]byte
	if _, err := c.Read(one[:]); err == nil {
		t.Fatalf("reply arrived before the pool dial returned (byte 0x%02x)", one[0])
	}
	close(release)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	code, _ := readSOCKSReply(t, c)
	if code != 0x00 {
		t.Fatalf("reply = 0x%02x", code)
	}
	mustWrite(t, c, "ok")
	echoed := make([]byte, 2)
	if _, err := io.ReadFull(c, echoed); err != nil || string(echoed) != "ok" {
		t.Fatalf("tunnel echo = %q err=%v", echoed, err)
	}
	h := s.registry.firstHandle()
	if h == nil || h.proofClass() != contract.ProofAccepted || h.info().Exit != "x-1" {
		t.Fatalf("handle proof/exit = %+v", h.info())
	}
}

func TestSOCKSPoolProofMissingIsRefused(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofUnknown}, nil
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
	code, _ := readSOCKSReply(t, c)
	if code != 0x04 {
		t.Fatalf("reply = 0x%02x, want 0x04; a proof-less pool conn must not be a success", code)
	}
}

func TestSOCKSRegistryCloseTearsDownBothPeers(t *testing.T) {
	originClosed := make(chan struct{}, 1)
	echo := startRawServer(t, func(c net.Conn) {
		_, _ = io.Copy(c, c)
		originClosed <- struct{}{}
	})
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
			return net.Dial("tcp", echo)
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
	if code, _ := readSOCKSReply(t, c); code != 0x00 {
		t.Fatalf("reply = 0x%02x", code)
	}
	h := s.registry.firstHandle()
	if h == nil {
		t.Fatal("no handle")
	}
	go func() { _ = h.Close() }()
	if !isClosed(c, 2*time.Second) {
		t.Fatal("client did not observe teardown")
	}
	select {
	case <-originClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not observe teardown")
	}
}

func TestSOCKSAuthIsNoAuthOnly(t *testing.T) {
	s := newStack(t, nil)
	c := dialProxy(t, s.f.SOCKSAddr().String())
	mustWrite(t, c, "\x05\x01\x02")
	reply := readSOCKSMethodReply(t, c)
	if reply[0] != 0x05 || reply[1] != 0xff {
		t.Fatalf("method reply = %v, want 05 ff", reply)
	}
}

func TestSOCKSFailureTextDoesNotLeakRefusedPrefix(t *testing.T) {
	f := newFailure(contract.DestinationRefused, contract.PathPool, "pool.dial", "refused")
	if text := socksDialErrorText(f).Error(); strings.Contains(text, "refused") {
		t.Fatalf("pool failure text %q would be mapped to 0x05 by the library", text)
	}
}

func TestSOCKSHalfClosePropagates(t *testing.T) {
	received := make(chan string, 1)
	origin := startRawServer(t, func(c net.Conn) {
		body, _ := io.ReadAll(c)
		received <- string(body)
		_ = writeAll(c, []byte("BYE"))
	})
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
			return net.Dial("tcp", origin)
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
	if code, _ := readSOCKSReply(t, c); code != 0x00 {
		t.Fatalf("reply = 0x%02x", code)
	}
	mustWrite(t, c, "ping")
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn is %T", c)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	rest := readAllWithDeadline(t, c, 3*time.Second)
	if string(rest) != "BYE" {
		t.Fatalf("after half-close the client got %q", rest)
	}
	select {
	case got := <-received:
		if got != "ping" {
			t.Fatalf("origin received %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin never saw the half-close")
	}
}

func TestClientGoneCancelsPendingSOCKSDial(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(canceled) })
			return contract.PoolDialResult{}, ctx.Err()
		}
	})
	c := dialProxy(t, s.f.SOCKSAddr().String())
	socksHandshake(t, c)
	mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("pool dial never started")
	}
	resetConn(t, c)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("resetting the client did not cancel the pending SOCKS dial")
	}
}

func TestSOCKSConnectRefusedIsKindDriven(t *testing.T) {
	for _, msg := range []string{"the direct dial failed", "connect: network is unreachable", ""} {
		t.Run(msg, func(t *testing.T) {
			s := newStack(t, func(s *testStack) {
				s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
					return nil, &contract.Failure{Kind: contract.ConnectRefused, Path: contract.PathDirect, Message: msg}
				}
			})
			c := dialProxy(t, s.f.SOCKSAddr().String())
			socksHandshake(t, c)
			mustWrite(t, c, string(socksConnectIP(net.IPv4(127, 0, 0, 1), 80)))
			if code, _ := readSOCKSReply(t, c); code != 0x05 {
				t.Fatalf("reply = 0x%02x, want 0x05 for a redacted ConnectRefused", code)
			}
		})
	}
}
