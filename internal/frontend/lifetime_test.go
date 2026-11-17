package frontend

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// TestPoolTunnelOutlivesTheFrontendDialScope pins the ownership rule for a pool
// tunnel: the context the frontend hands to DialPool is the connection
// lifetime owned by the registry handle, not a dial scope. The real engine
// builds the core session and the SOCKS5 adapter on that context, so ending it
// when the dial returns (or attaching a dial deadline to it) tears down a live
// tunnel — the F1 finding (docs/contracts.md §2.6, §3.4, D13).
func TestPoolTunnelOutlivesTheFrontendDialScope(t *testing.T) {
	echo := startEchoServer(t)
	sessionCtxs := make(chan context.Context, 1)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			// Mimic the engine: the established session lives on the context
			// it was dialed with and ends when that context ends.
			sessionCtxs <- ctx
			context.AfterFunc(ctx, func() { _ = conn.Close() })
			return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofConfirmed}, nil
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\n\r\n")
	if reply := readHeadDirect(t, c); !strings.Contains(reply, "200") {
		t.Fatalf("CONNECT reply = %q, want 200", reply)
	}

	sessionCtx := <-sessionCtxs
	if err := sessionCtx.Err(); err != nil {
		t.Fatalf("the pool session's lifetime ended when the dial returned: %v", err)
	}
	if deadline, ok := sessionCtx.Deadline(); ok {
		t.Fatalf("the pool dial received a dial-scoped deadline (%v); the established tunnel would die at it", deadline)
	}

	mustWrite(t, c, "ping")
	got := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("pool tunnel after the 200 = %q, %v", got, err)
	}
}
