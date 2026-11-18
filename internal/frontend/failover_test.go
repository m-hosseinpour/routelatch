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
	h := s.registry.firstHandle()
	if h == nil {
		t.Fatal("no registry handle")
	}
	if h.info().Exit != "x-1" || h.proofClass() != "" {
		t.Fatalf("pre-commit handle = %+v, want exit x-1 and no proof", h.info())
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
	h = s.registry.firstHandle()
	if h == nil {
		t.Fatal("no registry handle after commit")
	}
	if h.info().Exit != "x-2" || h.proofClass() != contract.ProofAccepted {
		t.Fatalf("committed handle = %+v, want exit x-2 with accepted proof", h.info())
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
	h := s.registry.firstHandle()
	if h == nil {
		t.Fatal("no registry handle")
	}
	if h.info().Exit != "x-1" || h.proofClass() != contract.ProofAccepted {
		t.Fatalf("cap commit handle = %+v, want exit x-1 with accepted proof", h.info())
	}
}

func containsStatus(head string, code int) bool {
	return len(head) >= 9 && head[:9] == "HTTP/1.1 " &&
		head[9:12] == string([]byte{byte('0' + code/100), byte('0' + (code/10)%10), byte('0' + code%10)})
}

// TestCommittedTunnelKeepsUploadIndependentOfClientReads proves the committed
// tunnel has two independent copy directions: a client that stops reading the
// downstream burst must not stall its own upload. The exit drains the upload in
// one goroutine while its downstream write blocks.
func TestCommittedTunnelKeepsUploadIndependentOfClientReads(t *testing.T) {
	proxyConn, exitConn := tcpPair(t)

	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return pipeResult(proxyConn, "x-1", false), nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4096)
	}
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}

	// The exit sends a large downstream burst and blocks on the client that is
	// deliberately not reading. Its reader still drains the upload.
	payload := make([]byte, 1<<20)
	go func() { _, _ = exitConn.Write(make([]byte, 8<<20)) }()
	uploadDone := make(chan int64, 1)
	go func() {
		buf := make([]byte, 32<<10)
		var total int64
		for total < int64(len(payload)) {
			n, err := exitConn.Read(buf)
			total += int64(n)
			if err != nil {
				break
			}
		}
		uploadDone <- total
	}()

	go func() { _, _ = c.Write(payload) }()

	select {
	case n := <-uploadDone:
		if n < int64(len(payload)) {
			t.Fatalf("exit received %d upload bytes, want at least %d", n, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload stalled behind a client that does not read the downstream burst")
	}
}

// halfDeadConn models the review's race deterministically: the destination
// produced one byte, but the client-to-exit write fails before the coordinator
// has forwarded it. The first byte must commit the attempt, so it is not
// retried or reported as an exit failure.
type halfDeadConn struct {
	once     sync.Once
	read     chan struct{}
	closed   chan struct{}
	closeOne sync.Once
}

func newHalfDeadConn() *halfDeadConn {
	return &halfDeadConn{read: make(chan struct{}), closed: make(chan struct{})}
}

func (c *halfDeadConn) Read(p []byte) (int, error) {
	first := false
	c.once.Do(func() {
		first = true
		close(c.read)
	})
	if first {
		p[0] = 'S'
		return 1, nil
	}
	<-c.closed
	return 0, io.EOF
}

func (c *halfDeadConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (c *halfDeadConn) Close() error {
	c.closeOne.Do(func() { close(c.closed) })
	return nil
}
func (c *halfDeadConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *halfDeadConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *halfDeadConn) SetDeadline(time.Time) error      { return nil }
func (c *halfDeadConn) SetReadDeadline(time.Time) error  { return nil }
func (c *halfDeadConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// tcpPair returns two ends of a real loopback TCP connection, whose directions
// are independent unlike net.Pipe's serialized writers.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	_ = ln.Close()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func TestPreByteDownlinkIsNotDiscardedByAClientWriteFailure(t *testing.T) {
	conn := newHalfDeadConn()
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
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return pipeResult(conn, "x-1", false), nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}

	// Wait until the exit has produced its first byte. sawDown is set before
	// the reader hands it to the coordinator, so the client write below races
	// exactly the scene the fix must handle.
	select {
	case <-conn.read:
	case <-time.After(3 * time.Second):
		t.Fatal("exit never produced its first byte")
	}
	_, _ = c.Write([]byte("hello"))
	time.Sleep(100 * time.Millisecond)

	if s.pool.count() != 1 {
		t.Fatalf("pool dials = %d after a destination byte, want 1 (no retry)", s.pool.count())
	}
	mu.Lock()
	got := append([]contract.ExitID(nil), reports...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("reported a pre-byte failure after the destination answered: %v", got)
	}
	h := s.registry.firstHandle()
	if h == nil || h.info().Exit != "x-1" {
		t.Fatalf("handle = %+v, want the serving exit x-1", h)
	}
}

// TestPreByteReplayCanDuplicateAtDestination records the documented limit: the
// client sees one response, but an exit that already forwarded the request
// before dying may have caused the destination to see it once more. This is
// inherent to replaying bytes the first exit accepted; it is not a client-visible
// duplicate.
func TestPreByteReplayCanDuplicateAtDestination(t *testing.T) {
	firstServer, firstTunnel := net.Pipe()
	defer firstServer.Close()
	secondServer, secondTunnel := net.Pipe()
	defer secondServer.Close()

	var (
		mu       sync.Mutex
		requests int
	)
	count := func() {
		mu.Lock()
		requests++
		mu.Unlock()
	}
	s := newStack(t, func(s *testStack) {
		s.router.fn = poolRoute()
		s.pool.fn = func(_ context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			s.pool.mu.Lock()
			n := len(s.pool.calls)
			s.pool.mu.Unlock()
			if n == 1 {
				return pipeResult(firstTunnel, "x-1", false), nil
			}
			return pipeResult(secondTunnel, "x-2", false), nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}
	mustWrite(t, c, "hello")
	if got := readExact(t, firstServer, 5); got != "hello" {
		t.Fatalf("first exit received %q", got)
	}
	count() // the first exit forwarded the request before dying
	_ = firstServer.Close()

	if got := readExact(t, secondServer, 5); got != "hello" {
		t.Fatalf("second exit received %q", got)
	}
	count() // the second exit forwarded the replayed request
	if _, err := secondServer.Write([]byte("world")); err != nil {
		t.Fatalf("second exit response: %v", err)
	}
	if got := readExact(t, c, 5); got != "world" {
		t.Fatalf("client read %q, want one response", got)
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 2 {
		t.Fatalf("destination requests = %d, want 2 (documented replay limit)", got)
	}
}

// capEOFConn produces an upstream EOF while its write side still accepts, and
// blocks the write that follows the replay-cap commit. It covers the
// post-commit EOF branch; the committed-guard itself has a direct test in
// TestNextAttemptRefusesAfterCommit.
type capEOFConn struct {
	writeCh   chan int
	readGate  chan struct{}
	writeGate chan struct{}
	closed    chan struct{}

	mu      sync.Mutex
	writes  int
	closeOn sync.Once
}

func newCapEOFConn() *capEOFConn {
	return &capEOFConn{
		writeCh:   make(chan int),
		readGate:  make(chan struct{}),
		writeGate: make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

func (c *capEOFConn) Read([]byte) (int, error) {
	select {
	case <-c.readGate:
		return 0, io.EOF
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *capEOFConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	n := c.writes
	c.mu.Unlock()
	select {
	case c.writeCh <- n:
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
	if n >= 3 {
		select {
		case <-c.writeGate:
			return len(p), nil
		case <-c.closed:
			return 0, io.ErrClosedPipe
		}
	}
	return len(p), nil
}

func (c *capEOFConn) Close() error {
	c.closeOn.Do(func() { close(c.closed) })
	return nil
}
func (c *capEOFConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *capEOFConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *capEOFConn) SetDeadline(time.Time) error      { return nil }
func (c *capEOFConn) SetReadDeadline(time.Time) error  { return nil }
func (c *capEOFConn) SetWriteDeadline(time.Time) error { return nil }

func TestPostCommitDownstreamEOFDoesNotRetry(t *testing.T) {
	conn := newCapEOFConn()
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
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return pipeResult(conn, "x-1", false), nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if head := readHeadDirect(t, c); !containsStatus(head, 200) {
		t.Fatalf("CONNECT reply = %q", head)
	}

	// Three separated 32 KiB writes: the first two fill the replay buffer
	// without crossing the cap, the third crosses it and commits. The EOF is
	// released only after the commit, so this exercises the post-commit branch
	chunk := make([]byte, 32<<10)
	for i := 0; i < 3; i++ {
		if _, err := c.Write(chunk); err != nil {
			t.Fatalf("client write %d: %v", i, err)
		}
		select {
		case n := <-conn.writeCh:
			if n != i+1 {
				t.Fatalf("exit write %d, want %d", n, i+1)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("exit did not receive write %d", i+1)
		}
	}
	// The third write is blocked inside the exit; release the EOF and then the
	// write. The coordinator must treat the EOF as post-commit.
	close(conn.readGate)
	close(conn.writeGate)
	time.Sleep(200 * time.Millisecond)

	if s.pool.count() != 1 {
		t.Fatalf("pool dials = %d after the cap commit, want 1 (no retry)", s.pool.count())
	}
	mu.Lock()
	got := append([]contract.ExitID(nil), reports...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("reported a pre-byte failure after commit: %v", got)
	}
}

// TestNextAttemptRefusesAfterCommit guards the invariant that commitment closes
// the retry window even if a stale error event is still in flight. The parent
// implementation reported a false pre-byte failure and dialed a second exit
// with an emptied replay buffer.
func TestNextAttemptRefusesAfterCommit(t *testing.T) {
	first, firstPeer := net.Pipe()
	defer firstPeer.Close()
	second, secondPeer := net.Pipe()
	defer secondPeer.Close()

	s := newStack(t, func(s *testStack) {
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			s.pool.mu.Lock()
			n := len(s.pool.calls)
			s.pool.mu.Unlock()
			if n == 1 {
				return pipeResult(first, "x-1", false), nil
			}
			return pipeResult(second, "x-2", false), nil
		}
	})
	pt := &poolTunnel{
		ctx:      context.Background(),
		p:        &httpProxy{f: s.f},
		h:        &testHandle{id: "c-1", ctx: context.Background()},
		dec:      contract.Decision{Route: contract.RoutePool, PolicyVersion: 1},
		upstream: first,
		exit:     "x-1",
		attempts: 1,
		max:      contract.ConnectFailoverExits,
	}
	pt.committed.Store(true)
	if pt.nextAttempt() {
		t.Fatal("nextAttempt retried after the tunnel committed")
	}
	if s.pool.count() != 0 {
		t.Fatalf("pool dials = %d, want 0 after commit", s.pool.count())
	}
}
