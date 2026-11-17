package socksfront

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	gosocks5 "github.com/things-go/go-socks5"
)

// TestLibBlockedDialNotCancelledByClientClose is the 4a probe: the dial gets
// context.Background(), so a client disconnect neither cancels it nor closes
// the accepted connection until the dial returns.
func TestLibBlockedDialNotCancelledByClientClose(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	dialCtxErr := make(chan error, 1)
	dialReturned := make(chan struct{})
	addr, accepted := startTrackedLibServer(t, gosocks5.WithDial(
		func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			<-release
			dialCtxErr <- ctx.Err()
			close(dialReturned)
			return nil, errors.New("released by test")
		}))
	baseG, baseFD := logResources(t, "baseline")

	c := dialTest(t, addr)
	greetNoAuth(t, c)
	writeAll(t, c, ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80))
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("dial never started")
	}
	var tc *trackedConn
	select {
	case tc = <-accepted:
	case <-time.After(testTimeout):
		t.Fatal("server did not accept the connection")
	}

	_ = c.Close() // client disconnects while the dial is blocked
	time.Sleep(300 * time.Millisecond)
	select {
	case <-dialReturned:
		t.Fatal("dial returned within 300ms of client close")
	default:
	}
	if tc.closed.Load() {
		t.Fatal("accepted connection was closed although the dial is still blocked")
	}
	t.Logf("300ms after client close: dial still blocked, accepted conn open, goroutines=%d", runtimeNumGoroutine())

	close(release)
	select {
	case <-dialReturned:
	case <-time.After(time.Second):
		t.Fatal("dial did not return 1s after release")
	}
	if err := <-dialCtxErr; err != nil {
		t.Fatalf("dial context was cancelled by client close: %v", err)
	}
	if !waitUntil(testTimeout, func() bool { return tc.closed.Load() }) {
		t.Fatal("library did not close the accepted connection after the dial returned")
	}
	waitSettled(t, "blocked dial released", baseG, baseFD, 2)
}

// TestLibCloseLiveTunnelFromAnotherGoroutine is the 4b probe.
func TestLibCloseLiveTunnelFromAnotherGoroutine(t *testing.T) {
	es := startEcho(t)
	addr, accepted := startTrackedLibServer(t, gosocks5.WithDial(
		func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("tcp", es.addr, testTimeout)
		}))
	baseG, baseFD := logResources(t, "baseline")

	c := dialTest(t, addr)
	greetNoAuth(t, c)
	writeAll(t, c, ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80))
	if reply := readReply(t, c); reply.Rep != 0x00 {
		t.Fatalf("reply: got %#x", reply.Rep)
	}
	var tc *trackedConn
	select {
	case tc = <-accepted:
	case <-time.After(testTimeout):
		t.Fatal("server did not accept the connection")
	}
	writeAll(t, c, []byte("ping"))
	if got := readExact(t, c, 4); string(got) != "ping" {
		t.Fatalf("echo: got %q", got)
	}

	go func() { _ = tc.Close() }() // another goroutine closes the client side
	expectClosed(t, c, time.Second, "client after server-side close")
	select {
	case <-es.peerGone:
	case <-time.After(time.Second):
		t.Fatal("upstream peer did not observe teardown")
	}
	waitSettled(t, "live tunnel close", baseG, baseFD, 2)
}

// TestLibFiveConnectCloseRounds is the 4c resource bound.
func TestLibFiveConnectCloseRounds(t *testing.T) {
	es := startEcho(t)
	addr, _ := startLibServer(t, gosocks5.WithDial(
		func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("tcp", es.addr, testTimeout)
		}))

	round := func() {
		c, err := net.DialTimeout("tcp", addr, testTimeout)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		greetNoAuth(t, c)
		writeAll(t, c, ipv4Request(0x01, net.IPv4(192, 0, 2, 10), 80))
		if reply := readReply(t, c); reply.Rep != 0x00 {
			t.Fatalf("reply: got %#x", reply.Rep)
		}
		writeAll(t, c, []byte("r"))
		if got := readExact(t, c, 1); string(got) != "r" {
			t.Fatalf("echo: got %q", got)
		}
		_ = c.Close()
	}

	round() // warm-up keeps lazily created runtime fds out of the baseline
	time.Sleep(100 * time.Millisecond)
	baseG, baseFD := logResources(t, "baseline")
	for i := 0; i < 5; i++ {
		round()
	}
	ok := waitUntil(testTimeout, func() bool {
		return runtimeNumGoroutine() <= baseG+2 && socketFDCount(t) <= baseFD+2
	})
	g, sockets := logResources(t, "after 5 rounds")
	if !ok {
		t.Fatalf("resources did not settle: goroutines=%d (base %d), socket_fds=%d (base %d)", g, baseG, sockets, baseFD)
	}
}
