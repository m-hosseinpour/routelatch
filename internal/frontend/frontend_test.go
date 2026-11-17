package frontend

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func newUnstarted(t *testing.T) *Frontend {
	t.Helper()
	f, err := New(Options{
		Router:   &testRouter{version: 1},
		Pool:     &testPool{},
		Direct:   &testDirect{},
		Registry: &testRegistry{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.Stop(ctx)
	})
	return f
}

func TestStartRejectsNonLoopbackListeners(t *testing.T) {
	bad := []string{
		"0.0.0.0:0",
		":0",
		"localhost:0",
		"192.168.1.1:0",
		"example.com:0",
		"[::]:0",
	}
	for _, addr := range bad {
		t.Run("http "+addr, func(t *testing.T) {
			f := newUnstarted(t)
			if err := f.Start(addr, "127.0.0.1:0"); err == nil {
				t.Fatalf("Start(%q) accepted a non-loopback bind", addr)
			}
		})
		t.Run("socks "+addr, func(t *testing.T) {
			f := newUnstarted(t)
			if err := f.Start("127.0.0.1:0", addr); err == nil {
				t.Fatalf("Start(socks=%q) accepted a non-loopback bind", addr)
			}
		})
	}
}

func stopTestFrontend(t *testing.T, f *Frontend) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestStopClosesListenersAndLiveConnections(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
			return net.Dial("tcp", echo)
		}
	})
	httpAddr := s.f.HTTPAddr().String()
	socksAddr := s.f.SOCKSAddr().String()

	c := dialProxy(t, httpAddr)
	mustWrite(t, c, "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\n\r\n")
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}

	socksConn := dialProxy(t, socksAddr)
	socksHandshake(t, socksConn)

	stopCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopCh <- s.f.Stop(ctx)
	}()
	select {
	case err := <-stopCh:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Stop did not return")
	}

	if _, err := net.DialTimeout("tcp", httpAddr, 500*time.Millisecond); err == nil {
		t.Fatal("HTTP listener still accepts after Stop")
	}
	if _, err := net.DialTimeout("tcp", socksAddr, 500*time.Millisecond); err == nil {
		t.Fatal("SOCKS listener still accepts after Stop")
	}
	if !isClosed(c, time.Second) {
		t.Fatal("live tunnel survived Stop")
	}
	if !isClosed(socksConn, time.Second) {
		t.Fatal("live SOCKS connection survived Stop")
	}
	if err := s.f.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStopLeavesNoFrontendGoroutines(t *testing.T) {
	echo := startEchoServer(t)
	baseline := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		s := newStack(t, func(s *testStack) {
			s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
				return net.Dial("tcp", echo)
			}
		})

		// Idle HTTP keep-alive connection (request completed, socket open).
		idle := dialProxy(t, s.f.HTTPAddr().String())
		mustWrite(t, idle, "GET http://127.0.0.1:1/ HTTP/1.1\r\nHost: x\r\n\r\n")
		// The dial to port 1 fails; read the error reply so the handler is idle.
		_ = readAllWithDeadline(t, idle, 2*time.Second)
		_ = idle.Close()

		// Live tunnel.
		tunnel := dialProxy(t, s.f.HTTPAddr().String())
		mustWrite(t, tunnel, "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\n\r\n")
		_ = readHeadDirect(t, tunnel)

		// SOCKS connection blocked before a request.
		socksConn := dialProxy(t, s.f.SOCKSAddr().String())
		socksHandshake(t, socksConn)

		stopTestFrontend(t, s.f)
		_ = tunnel.Close()
		_ = socksConn.Close()
	}

	waitFor(t, 5*time.Second, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= baseline+2
	}, "frontend goroutines to exit")
}

func TestStartTwiceFails(t *testing.T) {
	s := newStack(t, nil)
	if err := s.f.Start("127.0.0.1:0", "127.0.0.1:0"); err == nil {
		t.Fatal("second Start succeeded")
	}
}

func TestStopCancelsInFlightDial(t *testing.T) {
	started := make(chan struct{})
	dialDone := make(chan error, 1)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			close(started)
			<-ctx.Done()
			dialDone <- ctx.Err()
			return contract.PoolDialResult{}, ctx.Err()
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("pool dial never started")
	}
	stopCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopCh <- s.f.Stop(ctx)
	}()
	select {
	case <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel the in-flight dial context")
	}
	if err := <-stopCh; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !isClosed(c, time.Second) {
		t.Fatal("client survived Stop during a pending dial")
	}
}
