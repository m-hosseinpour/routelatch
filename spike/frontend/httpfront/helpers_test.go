package httpfront

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elazarl/goproxy"
)

// connTracker records accepted client connections so a test can close them the
// way a mode switch would close live hijacked tunnels. goproxy keeps the
// hijacked conn private, so owning the listener is the only way to reach it.
type connTracker struct {
	ln net.Listener

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnTracker(ln net.Listener) *connTracker {
	return &connTracker{ln: ln, conns: make(map[net.Conn]struct{})}
}

func (l *connTracker) Accept() (net.Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	return c, nil
}

func (l *connTracker) Close() error   { return l.ln.Close() }
func (l *connTracker) Addr() net.Addr { return l.ln.Addr() }

// CloseAll closes the client connections that exist now, like a mode switch.
// The listener stays open for connections admitted after the switch.
func (l *connTracker) CloseAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for c := range l.conns {
		_ = c.Close()
		delete(l.conns, c)
	}
}

// startProxy serves handler on a loopback listener and returns its address plus
// a tracker that can tear down live client connections.
func startProxy(t *testing.T, handler http.Handler) (string, *connTracker) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	tracker := newConnTracker(ln)
	srv := &http.Server{Handler: handler}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(tracker)
	}()
	t.Cleanup(func() {
		tracker.CloseAll()
		_ = srv.Close()
		wg.Wait()
	})
	return ln.Addr().String(), tracker
}

// startTCPServer runs handle on every accepted loopback connection.
func startTCPServer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
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
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return ln.Addr().String()
}

// newProxy builds the hardened configuration under test and closes any pooled
// upstream connections when the test ends.
func newProxy(t *testing.T, cfg Config) *goproxy.ProxyHttpServer {
	t.Helper()
	p := New(cfg)
	t.Cleanup(func() { p.Tr.CloseIdleConnections() })
	return p
}

func newStockProxy(t *testing.T) *goproxy.ProxyHttpServer {
	t.Helper()
	p := goproxy.NewProxyHttpServer()
	t.Cleanup(func() { p.Tr.CloseIdleConnections() })
	return p
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// readHead reads one HTTP head (through the terminating empty line).
func readHead(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	head, err := readHeadErr(r)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	return head
}

// readHeadErr is the goroutine-safe variant for upstream handlers.
func readHeadErr(r *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		buf.Write(line)
		if err != nil {
			return buf.Bytes(), err
		}
		if bytes.HasSuffix(buf.Bytes(), []byte("\r\n\r\n")) {
			return buf.Bytes(), nil
		}
	}
}

// readHeadConn reads a head byte by byte so that no tunnel or body byte is
// prefetched and hidden from a later direct read of c.
func readHeadConn(t *testing.T, c net.Conn) []byte {
	t.Helper()
	var buf bytes.Buffer
	one := make([]byte, 1)
	for {
		if _, err := c.Read(one); err != nil {
			t.Fatalf("read head after %q: %v", buf.String(), err)
		}
		buf.WriteByte(one[0])
		if bytes.HasSuffix(buf.Bytes(), []byte("\r\n\r\n")) {
			return buf.Bytes()
		}
	}
}

func sendRaw(t *testing.T, c net.Conn, s string) {
	t.Helper()
	if _, err := io.WriteString(c, s); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// addrRecorder is a dialer that records the exact address the frontend gave it.
type addrRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *addrRecorder) add(addr string) {
	r.mu.Lock()
	r.seen = append(r.seen, addr)
	r.mu.Unlock()
}

func (r *addrRecorder) addrs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func (r *addrRecorder) last() string {
	a := r.addrs()
	if len(a) == 0 {
		return ""
	}
	return a[len(a)-1]
}

// mapDial records every destination and dials backend instead. The record keeps
// the name and port the frontend supplied, which is how a test tells it apart
// from the address actually dialed.
func mapDial(rec *addrRecorder, backend string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		rec.add(addr)
		var d net.Dialer
		return d.DialContext(ctx, network, backend)
	}
}

// requireTeardown rejects nil and timeout errors: only a real close counts as
// observed teardown.
func requireTeardown(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: read succeeded; expected teardown", what)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("%s: read timed out; teardown was not observed", what)
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func countFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// socketFDCount counts only socket descriptors. Total fds include Go's
// process-wide splice(2) pipe pool, which grows and is reclaimed by GC and is
// therefore not a leak signal.
func socketFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		if link, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.HasPrefix(link, "socket:[") {
			n++
		}
	}
	return n
}

// rawResponse is the exact byte sequence the client received.
func readAllResponse(t *testing.T, c net.Conn) []byte {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read full response: %v (got %q)", err, b)
	}
	return b
}

func statusLine(b []byte) string {
	line, _, _ := strings.Cut(string(b), "\r\n")
	return line
}
