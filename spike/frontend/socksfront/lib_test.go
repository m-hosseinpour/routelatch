package socksfront

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	gosocks5 "github.com/things-go/go-socks5"
)

// trackedConn records Close so a test can tell whether the library has returned
// from ServeConn while a dial is still blocked.
type trackedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// startLibServer runs go-socks5 with its own accept loop.
func startLibServer(t *testing.T, opts ...gosocks5.Option) (string, *gosocks5.Server) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := gosocks5.NewServer(opts...)
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return ln.Addr().String(), srv
}

// startTrackedLibServer accepts connections itself and serves each through
// Server.ServeConn on a trackedConn, so the test can close the accepted
// client-side connection from another goroutine.
func startTrackedLibServer(t *testing.T, opts ...gosocks5.Option) (string, <-chan *trackedConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := gosocks5.NewServer(opts...)
	accepted := make(chan *trackedConn, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := &trackedConn{Conn: c}
			accepted <- tc
			go func() { _ = srv.ServeConn(tc) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return ln.Addr().String(), accepted
}

// noopResolver is the workaround probe: report no IP and no error so the
// library keeps FQDN in AddrSpec.String().
type noopResolver struct{}

func (noopResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}
