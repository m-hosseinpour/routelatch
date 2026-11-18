package frontend

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// poolRoute makes every request on this stack an approved pool destination.
func poolRoute() func(contract.Destination) (contract.Decision, error) {
	return func(contract.Destination) (contract.Decision, error) {
		return contract.Decision{
			Mode: contract.ModeTargeted, Class: contract.ClassApproved,
			Route: contract.RoutePool, Reason: contract.ReasonApprovedRule, PolicyVersion: 1,
		}, nil
	}
}

func pipeResult(conn net.Conn, exit contract.ExitID, pinned bool) contract.PoolDialResult {
	return contract.PoolDialResult{
		Conn: conn, Exit: exit, Proof: contract.ProofAccepted, Pinned: pinned,
	}
}

func readExact(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	_ = c.SetReadDeadline(time.Time{})
	return string(buf)
}

// TestConnectFailsOverBeforeFirstByte is the core guarantee: the first exit
// accepts, receives the client's bytes and dies without answering; the same
// bytes are replayed to a second exit. The client sees exactly one 200 and only
// the second exit's response, and the failure is reported against the first
// exit without a direct attempt.
func TestConnectFailsOverBeforeFirstByte(t *testing.T) {
	firstServer, firstTunnel := net.Pipe()
	defer firstServer.Close()
	secondServer, secondTunnel := net.Pipe()
	defer secondServer.Close()

	var (
		mu      sync.Mutex
		reports []contract.ExitID
	)
	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.report = func(exit contract.ExitID, kind contract.ErrorKind) {
			if kind != contract.DestinationUnconfirmed {
				t.Errorf("reported kind = %q, want destination_unconfirmed", kind)
			}
			mu.Lock()
			reports = append(reports, exit)
			mu.Unlock()
		}
		s.pool.fn = func(_ context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
			s.pool.mu.Lock()
			n := len(s.pool.calls)
			s.pool.mu.Unlock()
			switch n {
			case 1:
				return pipeResult(firstTunnel, "x-1", false), nil
			case 2:
				return pipeResult(secondTunnel, "x-2", false), nil
			default:
				return contract.PoolDialResult{}, &contract.Failure{
					Kind: contract.PoolUnavailable, Path: contract.PathPool, Message: "no more exits",
				}
			}
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}

	mustWrite(t, c, "hello")
	if got := readExact(t, firstServer, 5); got != "hello" {
		t.Fatalf("first exit received %q, want hello", got)
	}
	_ = firstServer.Close()

	// The retry replays the same bytes to the second exit.
	if got := readExact(t, secondServer, 5); got != "hello" {
		t.Fatalf("second exit received %q, want the replayed hello", got)
	}
	if _, err := secondServer.Write([]byte("world")); err != nil {
		t.Fatalf("second exit write: %v", err)
	}
	if got := readExact(t, c, 5); got != "world" {
		t.Fatalf("client read %q, want world", got)
	}

	reqs := s.pool.requests()
	if len(reqs) != 2 {
		t.Fatalf("pool dials = %d, want 2", len(reqs))
	}
	if len(reqs[1].Exclude) != 1 || reqs[1].Exclude[0] != "x-1" {
		t.Fatalf("second attempt Exclude = %v, want [x-1]", reqs[1].Exclude)
	}
	mu.Lock()
	gotReports := append([]contract.ExitID(nil), reports...)
	mu.Unlock()
	if len(gotReports) != 1 || gotReports[0] != "x-1" {
		t.Fatalf("reported exits = %v, want [x-1]", gotReports)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials = %d, want 0", s.direct.count())
	}
}

// TestConnectDoesNotFailOverAPinnedExit: a manual pin is an explicit choice. Its
// pre-byte death closes the tunnel and is reported against the pinned exit, but
// no other exit is dialed and the client sees no second 200 or error page.
func TestConnectDoesNotFailOverAPinnedExit(t *testing.T) {
	server, tunnel := net.Pipe()
	defer server.Close()

	var (
		mu      sync.Mutex
		reports []contract.ExitID
	)
	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.report = func(exit contract.ExitID, kind contract.ErrorKind) {
			if kind != contract.DestinationUnconfirmed {
				t.Errorf("reported kind = %q, want destination_unconfirmed", kind)
			}
			mu.Lock()
			reports = append(reports, exit)
			mu.Unlock()
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return pipeResult(tunnel, "x-pin", true), nil
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}
	mustWrite(t, c, "hello")
	if got := readExact(t, server, 5); got != "hello" {
		t.Fatalf("pinned exit received %q", got)
	}
	_ = server.Close()

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("client read after pin failure: %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("client saw extra bytes after a pinned failure: %q", buf)
	}
	if s.pool.count() != 1 {
		t.Fatalf("pool dials = %d, want exactly 1 (no failover from a pin)", s.pool.count())
	}
	mu.Lock()
	gotReports := append([]contract.ExitID(nil), reports...)
	mu.Unlock()
	if len(gotReports) != 1 || gotReports[0] != "x-pin" {
		t.Fatalf("reported exits = %v, want [x-pin]", gotReports)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials = %d, want 0", s.direct.count())
	}
}

// TestConnectFailoverBudgetExhaustedClosesWithoutSecondReply: every exit dies
// before a byte. The client sees the single 200 it already received, then a
// clean teardown; there is no error page, no second 200, no fourth dial and no
// direct fallback. Every failed attempt is reported.
func TestConnectFailoverBudgetExhaustedClosesWithoutSecondReply(t *testing.T) {
	const exits = contract.ConnectFailoverExits
	servers := make([]net.Conn, exits)
	tunnels := make([]net.Conn, exits)
	for i := range servers {
		servers[i], tunnels[i] = net.Pipe()
		defer servers[i].Close()
	}
	var (
		mu      sync.Mutex
		reports []contract.ExitID
	)
	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.report = func(exit contract.ExitID, _ contract.ErrorKind) {
			mu.Lock()
			reports = append(reports, exit)
			mu.Unlock()
		}
		s.pool.fn = func(_ context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			s.pool.mu.Lock()
			n := len(s.pool.calls)
			s.pool.mu.Unlock()
			if n > exits {
				return contract.PoolDialResult{}, &contract.Failure{
					Kind: contract.PoolUnavailable, Path: contract.PathPool, Message: "no more exits",
				}
			}
			return pipeResult(tunnels[n-1], contract.ExitID("x-"+string(rune('0'+n))), false), nil
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}
	mustWrite(t, c, "hello")
	for i := 0; i < exits; i++ {
		if got := readExact(t, servers[i], 5); got != "hello" {
			t.Fatalf("exit %d received %q, want replayed hello", i+1, got)
		}
		_ = servers[i].Close()
	}

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("client read after exhausted budget: %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("client saw an error page or second reply: %q", buf)
	}
	if s.pool.count() != exits {
		t.Fatalf("pool dials = %d, want %d", s.pool.count(), exits)
	}
	mu.Lock()
	gotReports := append([]contract.ExitID(nil), reports...)
	mu.Unlock()
	if len(gotReports) != exits {
		t.Fatalf("reported failures = %v, want %d attempts", gotReports, exits)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials = %d, want 0", s.direct.count())
	}
}

// TestConnectCommitsAtReplayCap: once the client has sent more than the replay
// cap without an answer, the bytes cannot be safely replayed. The tunnel commits
// to the current exit; a later death is an ordinary post-reply teardown, never a
// retry.
func TestConnectCommitsAtReplayCap(t *testing.T) {
	server, tunnel := net.Pipe()
	defer server.Close()

	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return pipeResult(tunnel, "x-1", false), nil
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}

	payload := make([]byte, contract.ConnectReplayBufferBytes+1)
	writeDone := make(chan error, 1)
	go func() {
		_, err := c.Write(payload)
		writeDone <- err
	}()
	got := make([]byte, len(payload))
	_ = server.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("first exit did not receive the payload: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("client write: %v", err)
	}
	_ = server.Close()

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.ReadAll(c)
	if s.pool.count() != 1 {
		t.Fatalf("pool dials = %d, want 1: replay must stop at the cap", s.pool.count())
	}
}

func containsStatus(head string, code int) bool {
	return len(head) >= 9 && head[:9] == "HTTP/1.1 " &&
		head[9:12] == string([]byte{byte('0' + code/100), byte('0' + (code/10)%10), byte('0' + code%10)})
}
