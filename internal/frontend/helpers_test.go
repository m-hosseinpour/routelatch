package frontend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// --- Router ---------------------------------------------------------------

type testRouter struct {
	mu      sync.Mutex
	version contract.PolicyVersion
	fn      func(contract.Destination) (contract.Decision, error)
}

func (r *testRouter) Decide(_ context.Context, dst contract.Destination) (contract.Decision, error) {
	r.mu.Lock()
	fn, version := r.fn, r.version
	r.mu.Unlock()
	if fn != nil {
		return fn(dst)
	}
	return contract.Decision{
		Mode:          contract.ModeTargeted,
		Class:         contract.ClassOther,
		Route:         contract.RouteDirect,
		Reason:        contract.ReasonUnapproved,
		PolicyVersion: version,
	}, nil
}

func (r *testRouter) PolicyVersion() contract.PolicyVersion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

// --- Direct dialer --------------------------------------------------------

type testDirect struct {
	mu    sync.Mutex
	calls []contract.Destination
	fn    func(context.Context, contract.Destination) (net.Conn, error)
}

func (d *testDirect) DialDirect(ctx context.Context, dst contract.Destination) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, dst)
	fn := d.fn
	d.mu.Unlock()
	if fn != nil {
		return fn(ctx, dst)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", dialAddress(dst))
}

func (d *testDirect) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *testDirect) destinations() []contract.Destination {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]contract.Destination(nil), d.calls...)
}

// --- Pool dialer ----------------------------------------------------------

type testPool struct {
	mu    sync.Mutex
	calls []contract.PoolDialRequest
	fn    func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error)
}

func (p *testPool) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	fn := p.fn
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return contract.PoolDialResult{}, &contract.Failure{
		Kind: contract.PoolUnavailable, Path: contract.PathPool,
		Op: "pool.dial", Message: "pool unavailable",
	}
}

func (p *testPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *testPool) requests() []contract.PoolDialRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]contract.PoolDialRequest(nil), p.calls...)
}

// --- Registry -------------------------------------------------------------

type testRegistry struct {
	mu          sync.Mutex
	specs       []contract.ConnSpec
	handles     []*testHandle
	registerErr error
}

func (r *testRegistry) SetPolicyVersion(contract.PolicyVersion) {}

func (r *testRegistry) Register(spec contract.ConnSpec) (contract.ConnHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registerErr != nil {
		return nil, r.registerErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &testHandle{id: spec.ID, ctx: ctx, cancel: cancel}
	r.specs = append(r.specs, spec)
	r.handles = append(r.handles, h)
	return h, nil
}

func (r *testRegistry) CloseConflicting(ctx context.Context, cause contract.CloseCause, next contract.PolicyVersion, keep func(contract.ConnInfo) bool) (contract.CloseReport, error) {
	r.mu.Lock()
	handles := append([]*testHandle(nil), r.handles...)
	r.mu.Unlock()
	report := contract.CloseReport{Cause: cause, ToVersion: next}
	for _, h := range handles {
		if keep != nil {
			info := h.info()
			if keep(info) {
				report.Kept++
				continue
			}
		}
		if h.close() {
			report.Closed++
		}
	}
	return report, nil
}

func (r *testRegistry) CloseAll(_ context.Context, cause contract.CloseCause) (contract.CloseReport, error) {
	return r.CloseConflicting(context.Background(), cause, 0, nil)
}

func (r *testRegistry) Snapshot() []contract.ConnInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]contract.ConnInfo, 0, len(r.handles))
	for _, h := range r.handles {
		out = append(out, h.info())
	}
	return out
}

func (r *testRegistry) allSpecs() []contract.ConnSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contract.ConnSpec(nil), r.specs...)
}

func (r *testRegistry) firstHandle() *testHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.handles) == 0 {
		return nil
	}
	return r.handles[0]
}

func (r *testRegistry) handleCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.handles)
}

type testHandle struct {
	id     contract.ConnID
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	client   net.Conn
	upstream net.Conn
	exit     contract.ExitID
	proof    contract.ProofClass
	closed   bool
}

func (h *testHandle) ID() contract.ConnID            { return h.id }
func (h *testHandle) Context() context.Context       { return h.ctx }
func (h *testHandle) SetExit(e contract.ExitID)      { h.mu.Lock(); h.exit = e; h.mu.Unlock() }
func (h *testHandle) SetProof(p contract.ProofClass) { h.mu.Lock(); h.proof = p; h.mu.Unlock() }

func (h *testHandle) SetClient(c net.Conn) error {
	h.mu.Lock()
	h.client = c
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil
	}
	return nil
}

func (h *testHandle) SetUpstream(c net.Conn) error {
	h.mu.Lock()
	h.upstream = c
	closed := h.closed
	h.mu.Unlock()
	if closed {
		_ = c.Close()
	}
	return nil
}

func (h *testHandle) Close() error { h.close(); return nil }

func (h *testHandle) close() bool {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false
	}
	h.closed = true
	client, upstream := h.client, h.upstream
	h.cancel()
	h.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if upstream != nil {
		_ = upstream.Close()
	}
	return true
}

func (h *testHandle) info() contract.ConnInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return contract.ConnInfo{ID: h.id, Exit: h.exit, Proof: h.proof}
}

func (h *testHandle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func (h *testHandle) proofClass() contract.ProofClass {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proof
}

// --- Frontend construction ------------------------------------------------

type testStack struct {
	f         *Frontend
	router    *testRouter
	direct    *testDirect
	pool      *testPool
	registry  *testRegistry
	normalize NormalizeFunc
	report    func(contract.ExitID, contract.ErrorKind)
}

func newStack(t *testing.T, mutate func(*testStack)) *testStack {
	t.Helper()
	s := &testStack{
		router:   &testRouter{version: 1},
		direct:   &testDirect{},
		pool:     &testPool{},
		registry: &testRegistry{},
	}
	if mutate != nil {
		mutate(s)
	}
	f, err := New(Options{
		Router:            s.router,
		Pool:              s.pool,
		Direct:            s.direct,
		Registry:          s.registry,
		Normalize:         s.normalize,
		ReportExitFailure: s.report,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.f = f
	if err := f.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return s
}

// --- Listener/server helpers ---------------------------------------------

// startRawServer runs handle for every accepted connection and returns its
// address. Cleanup closes the listener and every accepted connection.
func startRawServer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				handle(c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// resetConn closes the client with SO_LINGER 0 so the peer observes a reset,
// which is the unambiguous "client went away" signal (a FIN is a half-close).
func resetConn(t *testing.T, c net.Conn) {
	t.Helper()
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = c.Close()
}

func dialProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustWrite(t *testing.T, c net.Conn, s string) {
	t.Helper()
	if _, err := io.WriteString(c, s); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readUntilHead reads through the blank line and returns those bytes.
func readUntilHead(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var out []byte
	for {
		line, err := br.ReadSlice('\n')
		out = append(out, line...)
		if err != nil {
			t.Fatalf("read head: %v", err)
		}
		if isBlankLine(line) {
			return string(out)
		}
	}
}

func readAllWithDeadline(t *testing.T, c net.Conn, d time.Duration) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	b, err := io.ReadAll(c)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("read timed out waiting for EOF")
		}
		t.Fatalf("read: %v", err)
	}
	return b
}

func dialAddress(dst contract.Destination) string {
	host := dst.Host
	if dst.Kind == contract.DestIP {
		host = dst.Addr.String()
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", dst.Port))
}

// startEchoServer echoes every byte back.
func startEchoServer(t *testing.T) string {
	t.Helper()
	return startRawServer(t, func(c net.Conn) {
		_, _ = io.Copy(c, c)
	})
}

// readSOCKSMethodReply consumes the server method selection.
func readSOCKSMethodReply(t *testing.T, c net.Conn) []byte {
	t.Helper()
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	return buf
}

// socksConnectDomain builds a SOCKS5 CONNECT request for a domain.
func socksConnectDomain(host string, port uint16) []byte {
	out := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	out = append(out, host...)
	out = append(out, byte(port>>8), byte(port))
	return out
}

// socksConnectIP builds a SOCKS5 CONNECT request for an IPv4 literal.
func socksConnectIP(ip net.IP, port uint16) []byte {
	out := []byte{0x05, 0x01, 0x00, 0x01}
	out = append(out, ip.To4()...)
	out = append(out, byte(port>>8), byte(port))
	return out
}

// socksRequest builds a command request for an IPv4 literal.
func socksRequest(cmd byte, ip net.IP, port uint16) []byte {
	out := []byte{0x05, cmd, 0x00, 0x01}
	out = append(out, ip.To4()...)
	out = append(out, byte(port>>8), byte(port))
	return out
}

func readSOCKSReply(t *testing.T, c net.Conn) (code byte, rest []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatalf("read reply head: %v", err)
	}
	var addrLen int
	switch head[3] {
	case 0x01:
		addrLen = 4
	case 0x04:
		addrLen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			t.Fatalf("read reply domain len: %v", err)
		}
		addrLen = int(l[0])
	}
	rest = make([]byte, addrLen+2)
	if _, err := io.ReadFull(c, rest); err != nil {
		t.Fatalf("read reply tail: %v", err)
	}
	return head[1], rest
}

func socksHandshake(t *testing.T, c net.Conn) {
	t.Helper()
	mustWrite(t, c, "\x05\x01\x00")
	reply := readSOCKSMethodReply(t, c)
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("method reply = %v, want 05 00", reply)
	}
}

func isClosed(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	var one [1]byte
	_, err := c.Read(one[:])
	return err != nil
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func mustReadResponse(t *testing.T, br *bufio.Reader, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	return resp, body
}
