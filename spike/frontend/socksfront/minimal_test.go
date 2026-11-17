package socksfront

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func startMinimal(t *testing.T, dial DialFunc) *Server {
	t.Helper()
	srv, err := Listen("127.0.0.1:0", dial)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func waitSession(t *testing.T, srv *Server, state string) SessionInfo {
	t.Helper()
	var info SessionInfo
	if !waitUntil(testTimeout, func() bool {
		for _, s := range srv.Sessions() {
			if s.State == state {
				info = s
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no session reached state %q (current: %+v)", state, srv.Sessions())
	}
	return info
}

func TestMinimalHostnameUnresolved(t *testing.T) {
	es := startEcho(t)
	dialed := make(chan string, 1)
	var calls atomic.Int32
	srv := startMinimal(t, func(_ context.Context, network, host, port string) (net.Conn, error) {
		calls.Add(1)
		dialed <- net.JoinHostPort(host, port)
		if network != "tcp" {
			return nil, errors.New("unexpected network " + network)
		}
		return net.DialTimeout("tcp", es.addr, testTimeout)
	})

	c := dialTest(t, srv.Addr().String())
	greetNoAuth(t, c)
	writeAll(t, c, domainRequest(0x01, "socks-target.invalid", 443))
	reply := readReply(t, c)
	if reply.Rep != 0x00 {
		t.Fatalf("reply: got %#x raw=%s", reply.Rep, hex(reply.Raw))
	}
	writeAll(t, c, []byte("hello"))
	if got := readExact(t, c, 5); string(got) != "hello" {
		t.Fatalf("tunnel payload: got %q", got)
	}
	select {
	case got := <-dialed:
		if got != "socks-target.invalid:443" {
			t.Fatalf("dial saw %q, hostname was not preserved", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("dialer never called")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("dial calls: got %d want 1", n)
	}
}

func TestMinimalEarlyDataInOneWrite(t *testing.T) {
	es := startEcho(t)
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		return net.DialTimeout("tcp", es.addr, testTimeout)
	})
	payload := []byte("early-data-through-the-tunnel")

	c := dialTest(t, srv.Addr().String())
	oneWrite := append(append([]byte{0x05, 0x01, 0x00}, domainRequest(0x01, "socks-target.invalid", 80)...), payload...)
	writeAll(t, c, oneWrite)
	expectBytes(t, c, []byte{0x05, 0x00}, "method reply")
	if reply := readReply(t, c); reply.Rep != 0x00 {
		t.Fatalf("reply: got %#x", reply.Rep)
	}
	if got := readExact(t, c, len(payload)); string(got) != string(payload) {
		t.Fatalf("early payload: got %q want %q", got, payload)
	}
}

func TestMinimalDialFailure(t *testing.T) {
	var calls atomic.Int32
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("pool unavailable: no healthy exit")
	})

	c := dialTest(t, srv.Addr().String())
	writeAll(t, c, append(append([]byte{0x05, 0x01, 0x00}, domainRequest(0x01, "socks-target.invalid", 80)...), []byte("early")...))
	expectBytes(t, c, []byte{0x05, 0x00}, "method reply")
	reply := readReply(t, c)
	t.Logf("dial failure reply: %s", hex(reply.Raw))
	if reply.Rep == 0x00 {
		t.Fatal("dial failure produced a success reply")
	}
	if got := hex(reply.Raw); got != "05 01 00 01 00 00 00 00 00 00" {
		t.Fatalf("failure reply bytes: got %s", got)
	}
	expectClosed(t, c, time.Second, "after dial failure")
	if n := calls.Load(); n != 1 {
		t.Fatalf("dial calls: got %d want 1 (no fallback or retry)", n)
	}
	if !waitUntil(time.Second, func() bool { return srv.SessionCount() == 0 }) {
		t.Fatalf("session not removed: %+v", srv.Sessions())
	}
}

func TestMinimalCancelInFlightDial(t *testing.T) {
	started := make(chan struct{})
	dialDone := make(chan error, 1)
	srv := startMinimal(t, func(ctx context.Context, _, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		dialDone <- ctx.Err()
		return nil, ctx.Err()
	})

	c := dialTest(t, srv.Addr().String())
	greetNoAuth(t, c)
	writeAll(t, c, domainRequest(0x01, "blocked.invalid", 80))
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("dial never started")
	}
	info := waitSession(t, srv, "dialing")

	begin := time.Now()
	if !srv.Cancel(info.ID) {
		t.Fatalf("cancel of session %d failed", info.ID)
	}
	select {
	case err := <-dialDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dial context error: got %v want context.Canceled", err)
		}
		t.Logf("in-flight dial cancelled after %v", time.Since(begin).Round(time.Millisecond))
	case <-time.After(time.Second):
		t.Fatal("in-flight dial did not return within 1s of Cancel")
	}
	if !waitUntil(time.Second, func() bool { return srv.SessionCount() == 0 }) {
		t.Fatalf("session not removed after cancel: %+v", srv.Sessions())
	}
	expectClosed(t, c, time.Second, "client after cancel")
}

func TestMinimalCloseClientFromAnotherGoroutine(t *testing.T) {
	es := startEcho(t)
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		return net.DialTimeout("tcp", es.addr, testTimeout)
	})

	c := dialTest(t, srv.Addr().String())
	greetNoAuth(t, c)
	writeAll(t, c, domainRequest(0x01, "tunnel.invalid", 80))
	if reply := readReply(t, c); reply.Rep != 0x00 {
		t.Fatalf("reply: got %#x", reply.Rep)
	}
	writeAll(t, c, []byte("ping"))
	if got := readExact(t, c, 4); string(got) != "ping" {
		t.Fatalf("echo: got %q", got)
	}
	info := waitSession(t, srv, "streaming")

	done := make(chan struct{})
	go func() {
		srv.CloseClient(info.ID)
		close(done)
	}()
	<-done
	expectClosed(t, c, time.Second, "client after CloseClient")
	select {
	case <-es.peerGone:
	case <-time.After(time.Second):
		t.Fatal("upstream peer did not observe teardown")
	}
	if !waitUntil(time.Second, func() bool { return srv.SessionCount() == 0 }) {
		t.Fatalf("session not removed: %+v", srv.Sessions())
	}
}

func TestMinimalRefusesUDPAndBind(t *testing.T) {
	var dialCalls atomic.Int32
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errors.New("dial must not be called for BIND/UDP")
	})

	cases := []struct {
		name string
		cmd  byte
	}{
		{"BIND", 0x02},
		{"UDP ASSOCIATE", 0x03},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			udpBefore := udpSockets(t)
			c := dialTest(t, srv.Addr().String())
			greetNoAuth(t, c)
			writeAll(t, c, rawRequest(tc.cmd, atypIPv4))
			reply := readReply(t, c)
			if got := hex(reply.Raw); got != "05 07 00 01 00 00 00 00 00 00" {
				t.Fatalf("%s reply: got %s want 05 07 00 01 00 00 00 00 00 00", tc.name, got)
			}
			expectClosed(t, c, time.Second, "after "+tc.name+" refusal")
			time.Sleep(100 * time.Millisecond)
			if after := udpSockets(t); after != udpBefore {
				t.Fatalf("%s opened a UDP socket: before=%d after=%d", tc.name, udpBefore, after)
			}
		})
	}

	t.Run("unknown address type", func(t *testing.T) {
		c := dialTest(t, srv.Addr().String())
		greetNoAuth(t, c)
		writeAll(t, c, []byte{0x05, 0x01, 0x00, 0x09})
		reply := readReply(t, c)
		if got := hex(reply.Raw); got != "05 08 00 01 00 00 00 00 00 00" {
			t.Fatalf("unknown ATYP reply: got %s", got)
		}
		expectClosed(t, c, time.Second, "after unknown ATYP")
	})

	if n := dialCalls.Load(); n != 0 {
		t.Fatalf("dialer called %d times for refused commands", n)
	}
}

func TestMinimalNoAuthOnly(t *testing.T) {
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	c := dialTest(t, srv.Addr().String())
	writeAll(t, c, []byte{0x05, 0x01, 0x02}) // offer user/pass only
	expectBytes(t, c, []byte{0x05, 0xff}, "method reply")
}

func TestMinimalFiveConnectCloseRounds(t *testing.T) {
	es := startEcho(t)
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		return net.DialTimeout("tcp", es.addr, testTimeout)
	})

	round := func() {
		c, err := net.DialTimeout("tcp", srv.Addr().String(), testTimeout)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		greetNoAuth(t, c)
		writeAll(t, c, domainRequest(0x01, "round.invalid", 80))
		if reply := readReply(t, c); reply.Rep != 0x00 {
			t.Fatalf("reply: got %#x", reply.Rep)
		}
		writeAll(t, c, []byte("r"))
		if got := readExact(t, c, 1); string(got) != "r" {
			t.Fatalf("echo: got %q", got)
		}
		_ = c.Close()
	}

	round()
	time.Sleep(100 * time.Millisecond)
	baseG, baseFD := logResources(t, "baseline")
	for i := 0; i < 5; i++ {
		round()
	}
	ok := waitUntil(testTimeout, func() bool {
		return srv.SessionCount() == 0 && runtimeNumGoroutine() <= baseG+2 && socketFDCount(t) <= baseFD+2
	})
	g, fd := logResources(t, "after 5 rounds")
	if !ok {
		t.Fatalf("resources did not settle: sessions=%d goroutines=%d (base %d) socket_fds=%d (base %d)",
			srv.SessionCount(), g, baseG, fd, baseFD)
	}
}

func TestMinimalNoReplyBeforeDialReturns(t *testing.T) {
	es := startEcho(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := startMinimal(t, func(context.Context, string, string, string) (net.Conn, error) {
		close(entered)
		<-release
		return net.DialTimeout("tcp", es.addr, testTimeout)
	})

	c := dialTest(t, srv.Addr().String())
	greetNoAuth(t, c)
	writeAll(t, c, domainRequest(0x01, "socks-target.invalid", 80))
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("dial never started")
	}
	if _, err := tryReadExact(c, 1, 250*time.Millisecond); err == nil {
		t.Fatal("received a reply byte while the dial was still blocked")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected read timeout while dial blocked, got %v", err)
	}
	close(release)
	if reply := readReply(t, c); reply.Rep != 0x00 {
		t.Fatalf("reply after dial: got %#x", reply.Rep)
	}
}
