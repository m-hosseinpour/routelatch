package acceptance

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Long-reuse parameters. They are the v1 definition of "long duration": three
// requests whose first and last starts are more than 30s apart, with a full
// idle gap of at least 10s between two of them. The 30s sleep is deliberately
// above the threshold so scheduler jitter cannot turn a pass into a flake.
const (
	longReuseRequests = 3
	longReuseShortGap = 2 * time.Second
	longReuseIdleGap  = 30 * time.Second
)

// countingRelay is a transparent loopback TCP relay placed in front of the
// pinned exit. TLS runs end to end, so the relay only moves bytes; its accept
// count is the number of TCP connections RouteLatch opened to that exit. The
// existing exit fixture is a child process and exposes no such counter.
//
// The relay keeps each connection identifiable, its byte counters and its
// liveness. That matters because the product's scheduled transport health probe
// (first tick 30s after start) also opens one short-lived connection to the
// exit without any destination traffic; only the client tunnel connection must
// stay alive across the window.
type countingRelay struct {
	ln     net.Listener
	target string

	accepts atomic.Int64

	mu    sync.Mutex
	conns map[int]*relayConn
	once  sync.Once
}

type relayConn struct {
	down, up net.Conn
	started  time.Time
	downN    atomic.Int64 // bytes from the product to the exit
	upN      atomic.Int64 // bytes from the exit to the product
	finished atomic.Int32
	closed   atomic.Bool
}

func newCountingRelay(t *testing.T, targetPort int) *countingRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("counting relay listen: %v", err)
	}
	relay := &countingRelay{
		ln:     ln,
		target: fmt.Sprintf("127.0.0.1:%d", targetPort),
		conns:  map[int]*relayConn{},
	}
	go relay.serve()
	t.Cleanup(relay.close)
	return relay
}

func (r *countingRelay) port() uint16 { return uint16(r.ln.Addr().(*net.TCPAddr).Port) }

func (r *countingRelay) accepted() int64 { return r.accepts.Load() }

func (r *countingRelay) close() {
	r.once.Do(func() {
		_ = r.ln.Close()
		r.mu.Lock()
		conns := make([]*relayConn, 0, len(r.conns))
		for _, rc := range r.conns {
			conns = append(conns, rc)
		}
		r.mu.Unlock()
		for _, rc := range conns {
			_ = rc.down.Close()
			_ = rc.up.Close()
		}
	})
}

func (r *countingRelay) serve() {
	for {
		down, err := r.ln.Accept()
		if err != nil {
			return
		}
		id := int(r.accepts.Add(1))
		up, err := net.DialTimeout("tcp", r.target, 5*time.Second)
		if err != nil {
			_ = down.Close()
			continue
		}
		rc := &relayConn{down: down, up: up, started: time.Now()}
		r.mu.Lock()
		r.conns[id] = rc
		r.mu.Unlock()
		go func() {
			relayCopy(rc.up, rc.down, &rc.downN)
			if tc, ok := rc.up.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
			r.finish(rc)
		}()
		go func() {
			relayCopy(rc.down, rc.up, &rc.upN)
			if tc, ok := rc.down.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
			r.finish(rc)
		}()
	}
}

func (r *countingRelay) finish(rc *relayConn) {
	if rc.finished.Add(1) == 2 {
		rc.closed.Store(true)
		_ = rc.down.Close()
		_ = rc.up.Close()
	}
}

// countWriter counts every byte handed to the destination; wrapping the
// reader as a plain io.Reader disables io.Copy's TCPConn fast paths so the
// counter sees each chunk.
type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func relayCopy(dst, src net.Conn, count *atomic.Int64) {
	_, _ = io.Copy(countWriter{w: dst, n: count}, struct{ io.Reader }{src})
}

func (r *countingRelay) live() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rc := range r.conns {
		if !rc.closed.Load() {
			n++
		}
	}
	return n
}

func (r *countingRelay) liveID(id int) bool {
	r.mu.Lock()
	rc := r.conns[id]
	r.mu.Unlock()
	return rc != nil && !rc.closed.Load()
}

func (r *countingRelay) downBytes(id int) int64 {
	r.mu.Lock()
	rc := r.conns[id]
	r.mu.Unlock()
	if rc == nil {
		return 0
	}
	return rc.downN.Load()
}

func (r *countingRelay) upBytes(id int) int64 {
	r.mu.Lock()
	rc := r.conns[id]
	r.mu.Unlock()
	if rc == nil {
		return 0
	}
	return rc.upN.Load()
}

func (r *countingRelay) acceptedAt(id int) time.Time {
	r.mu.Lock()
	rc := r.conns[id]
	r.mu.Unlock()
	if rc == nil {
		return time.Time{}
	}
	return rc.started
}

// TestTransportLongReuse is the gated R6 long-duration cell. One client CONNECT
// tunnel to an approved destination goes through the already-proven WS/TLS
// exit; three HTTP requests travel over that same tunnel across more than 30s of
// wall clock, including a 30s idle gap. It asserts byte-identical echoes, one
// destination request per attempt (no replay), the single client exit
// connection staying single and alive across the whole window (no silent
// client re-dial), no direct delivery, and a service that still answers after
// the idle period.
//
// It is skipped by default because it deliberately holds a tunnel idle for
// half a minute; the offline acceptance suite stays fast.
func TestTransportLongReuse(t *testing.T) {
	if os.Getenv("RL_ACCEPT_LONG_REUSE_TEST") != "1" {
		t.Skip("set RL_ACCEPT_LONG_REUSE_TEST=1 to run the ~40s single-tunnel long-reuse cell")
	}
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "long-reuse")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}

	dest := newRecordingOrigin(t)

	uuid := randomUUID(t)
	exitPort := freePort(t)
	certName := "long-reuse-exit.rl.test"
	destHost := "long-reuse-dest.rl.test"
	certPath, keyPath, pin := selfSignedPin(t, certName)
	exit := startExit(t, exitSpec{
		Protocol: "vless", Port: int(exitPort), UUID: uuid,
		Hosts:       map[string]string{destHost: "127.0.0.1"},
		TLSCertFile: certPath, TLSKeyFile: keyPath,
		Transport: "ws", Host: certName, Path: "/r6-long-reuse",
		ALPN: []string{"http/1.1"},
	})
	relay := newCountingRelay(t, int(exitPort))

	serviceStart := time.Now()
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("rules", "add", "--kind", "domain", destHost)

	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "tls")
	q.Set("type", "ws")
	q.Set("sni", certName)
	q.Set("alpn", "http/1.1")
	q.Set("pinSHA256", pin)
	q.Set("host", certName)
	q.Set("path", "/r6-long-reuse")
	link := url.URL{
		Scheme:   "vless",
		User:     url.User(uuid),
		Host:     "127.0.0.1:" + strconv.Itoa(int(relay.port())),
		RawQuery: q.Encode(),
		Fragment: "r6-long-reuse",
	}

	imported := svc.mustOK("import", "link", link.String())
	counts, _ := imported.mapData(t)["records"].(map[string]any)
	if num(counts["valid"]) != 1 || num(counts["invalid"]) != 0 || num(counts["unsupported"]) != 0 {
		r.fail("R6", "long-reuse pinned link import counts=%+v, want 1/0/0", counts)
		t.FailNow()
	}
	rows := svc.mustOK("pool", "list").list(t, "records")
	if len(rows) != 1 {
		r.fail("R6", "long-reuse import produced %d records, want 1", len(rows))
		t.FailNow()
	}
	if got := str(rows[0]["transport"]); got != "ws" {
		r.fail("R6", "long-reuse record transport=%q, want ws", got)
		t.FailNow()
	}
	svc.mustOK("pool", "select", str(rows[0]["id"]))

	acceptsBefore := dest.acceptCount()
	// The fixture name is resolved only by the exit's static DNS. If the host
	// resolved it too, a direct dial could reach the same origin and the
	// destination counters alone could not tell the two paths apart.
	if ips, err := net.LookupHost(destHost); err == nil {
		r.fail("R6", "the fixture destination %q resolves on the host (%v); a direct path would be indistinguishable from the exit", destHost, ips)
		t.FailNow()
	}
	// No background dial may have happened before the client tunnel: otherwise
	// the first accept would not be the tunnel this test is measuring.
	if got := relay.accepted(); got != 0 {
		r.fail("R6", "the product opened %d exit connections before the client CONNECT (setup outlived the first rotation tick?)", got)
		t.FailNow()
	}

	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("%s:%d", destHost, dest.port()))
	if conn != nil {
		defer conn.Close()
	}
	if err != nil || code != 200 {
		r.fail("R6", "long-reuse CONNECT failed: code=%d err=%v (exit connections=%d, destination accepts=%d, exit logs: %s)",
			code, err, relay.accepted(), dest.acceptCount(), exit.Logs())
		t.FailNow()
	}
	// The frontend may answer CONNECT before the lazy pool dial (the documented
	// handshake gate); the tunnel connection is expected with the first request.
	if got := relay.accepted(); got > 1 {
		r.fail("R6", "the long-reuse CONNECT opened %d exit connections, want at most 1", got)
		t.FailNow()
	}

	var starts, ends []time.Time
	do := func(n int) int64 {
		payload := fmt.Sprintf("long-reuse-%d", n)
		want := "echo:POST:" + payload
		before := dest.count()
		start := time.Now()
		_ = conn.SetDeadline(start.Add(20 * time.Second))
		raw := fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n%s",
			destHost, len(payload), payload)
		if _, err := io.WriteString(conn, raw); err != nil {
			r.fail("R6", "long-reuse request %d could not be written over the established tunnel: %v (exit connections=%d, destination requests=%d)",
				n, err, relay.accepted(), dest.count())
			t.FailNow()
		}
		req, _ := http.NewRequest(http.MethodPost, "http://"+destHost+"/echo", nil)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			r.fail("R6", "long-reuse request %d got no response (502/timeout or a closed tunnel): %v (exit connections=%d, destination accepts=%d, requests=%d)",
				n, err, relay.accepted(), dest.acceptCount(), dest.count())
			t.FailNow()
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		end := time.Now()
		_ = conn.SetDeadline(time.Time{})
		if readErr != nil {
			r.fail("R6", "long-reuse request %d response body read failed: %v", n, readErr)
			t.FailNow()
		}
		if resp.StatusCode != http.StatusOK {
			r.fail("R6", "long-reuse request %d returned HTTP %d, want 200 (no 502/timeout during or after the idle window)", n, resp.StatusCode)
			t.FailNow()
		}
		if string(body) != want {
			r.fail("R6", "long-reuse request %d body=%q, want the origin echo %q", n, string(body), want)
			t.FailNow()
		}
		if got := resp.Header.Get("X-RL-Method"); got != http.MethodPost {
			r.fail("R6", "long-reuse request %d X-RL-Method=%q, want POST", n, got)
			t.FailNow()
		}
		if got := resp.Header.Get("X-RL-Body-Length"); got != strconv.Itoa(len(payload)) {
			r.fail("R6", "long-reuse request %d X-RL-Body-Length=%q, want %d", n, got, len(payload))
			t.FailNow()
		}
		if got := dest.count(); got != before+1 {
			r.fail("R6", "long-reuse request %d produced %d destination requests, want exactly 1 (duplicate or replay)", n, got-before)
			t.FailNow()
		}
		starts = append(starts, start)
		ends = append(ends, end)
		return relay.downBytes(1)
	}

	// The first accept is the client tunnel: nothing else had connected yet.
	tunnelStart := do(1)
	if got := relay.accepted(); got != 1 {
		r.fail("R6", "the first long-reuse request used %d exit connections, want exactly 1", got)
		t.FailNow()
	}
	if !relay.liveID(1) {
		r.fail("R6", "the client tunnel's exit connection closed right after the first request")
		t.FailNow()
	}
	if tunnelStart <= 0 {
		r.fail("R6", "the first request did not travel over the first exit connection (relay saw %d downstream bytes)", tunnelStart)
		t.FailNow()
	}
	time.Sleep(longReuseShortGap)
	tunnelBytes := do(2)
	if tunnelBytes <= tunnelStart {
		r.fail("R6", "the second request did not reuse the client tunnel connection (downstream bytes stayed %d)", tunnelBytes)
		t.FailNow()
	}

	// The idle window: no bytes on the client tunnel, no request and no new
	// destination connection. A scheduled transport health probe may open one
	// short-lived connection to the exit; it must never reach the destination.
	relayIdle := relay.accepted()
	acceptsIdle := dest.acceptCount()
	destIdle := dest.count()
	tunnelIdle := relay.downBytes(1)
	time.Sleep(longReuseIdleGap)
	if got := relay.accepted(); got > relayIdle+1 {
		r.fail("R6", "the product opened %d exit connections during the idle gap, want at most the one scheduled health probe (before=%d after=%d)", got-relayIdle, relayIdle, got)
		t.FailNow()
	}
	if !relay.liveID(1) {
		r.fail("R6", "the client tunnel's exit connection closed during the idle gap")
		t.FailNow()
	}
	if got := dest.acceptCount(); got != acceptsIdle {
		r.fail("R6", "a new destination connection appeared during the idle gap (%d→%d accepts)", acceptsIdle, got)
		t.FailNow()
	}
	if got := dest.count(); got != destIdle {
		r.fail("R6", "the destination received %d requests during the idle gap", got-destIdle)
		t.FailNow()
	}
	if got := relay.downBytes(1); got != tunnelIdle {
		r.fail("R6", "the client tunnel carried %d bytes during the idle gap, want 0", got-tunnelIdle)
		t.FailNow()
	}
	// A probe connection is short-lived; wait for it to close so the final
	// liveness check names the tunnel, not an in-flight handshake.
	if !waitFor(5*time.Second, func() bool { return relay.live() == 1 }) {
		r.fail("R6", "the exit kept %d connections open after the idle gap, want only the client tunnel", relay.live())
		t.FailNow()
	}

	tunnelBytes = do(3)
	if tunnelBytes <= tunnelIdle {
		r.fail("R6", "the third request after the idle gap did not reuse the client tunnel connection (downstream bytes stayed %d)", tunnelBytes)
		t.FailNow()
	}
	if relay.upBytes(1) <= 0 {
		r.fail("R6", "the client tunnel connection carried no response bytes")
		t.FailNow()
	}

	if len(starts) != longReuseRequests || len(ends) != longReuseRequests {
		r.fail("R6", "long-reuse completed %d requests, want %d", len(starts), longReuseRequests)
		t.FailNow()
	}
	// A probe may still be in flight when the last request finishes; wait for
	// its short-lived connection to close before attributing liveness.
	if !waitFor(5*time.Second, func() bool { return relay.live() == 1 }) {
		r.fail("R6", "the exit kept %d connections open after the last request, want only the client tunnel", relay.live())
		t.FailNow()
	}
	totalAccepts := relay.accepted()
	extra := totalAccepts - 1
	if extra > 1 {
		r.fail("R6", "the product opened %d exit connections besides the client tunnel, want at most the one scheduled health probe", extra)
		t.FailNow()
	}
	// The extra accept is attributed, not assumed: the product's first eligible
	// rotation tick is 30s after start (later rotations are 10 minutes apart),
	// and the relay saw this connection appear in that window, reach no
	// destination and close. Connection #1 is identified independently, so a
	// silent client re-dial would still fail the liveness/byte checks below.
	probeNote := "no extra exit connection"
	if extra == 1 {
		probeAt := relay.acceptedAt(2)
		age := probeAt.Sub(serviceStart)
		if probeAt.IsZero() || age < 25*time.Second || age > 60*time.Second {
			r.fail("R6", "the extra exit connection appeared at %v after service start, not at the first 30s rotation tick", age)
			t.FailNow()
		}
		probeNote = fmt.Sprintf("1 rotation-tick connection at +%.1fs", age.Seconds())
	}
	if !relay.liveID(1) {
		r.fail("R6", "the client tunnel's exit connection did not stay alive across the whole window")
		t.FailNow()
	}
	if got := relay.live(); got != 1 {
		r.fail("R6", "the exit had %d live connections at the end, want only the client tunnel", got)
		t.FailNow()
	}
	if got := dest.acceptCount(); got != acceptsBefore+1 {
		r.fail("R6", "the destination saw %d connections across the whole window, want exactly 1 (re-dial or direct delivery)", got-acceptsBefore)
		t.FailNow()
	}
	spread := ends[len(ends)-1].Sub(starts[0])
	idle := starts[len(starts)-1].Sub(ends[1])
	if spread < 30*time.Second {
		r.fail("R6", "the three requests spanned only %.1fs, want at least 30s", spread.Seconds())
		t.FailNow()
	}
	if idle < 10*time.Second {
		r.fail("R6", "the idle gap was only %.1fs, want at least 10s", idle.Seconds())
		t.FailNow()
	}
	if _, up := svc.statusNow(); !up {
		r.fail("R6", "the service stopped answering management after the idle window")
		t.FailNow()
	}

	r.pass("R6", "long-reuse WS/TLS: one client CONNECT tunnel carried 3 requests over %.1fs (idle gap %.1fs), each body byte-identical to the origin echo with exactly 1 destination request per attempt; exit connection #1 stayed live and carried every request (1 client connection, %d total accepts; %s with 0 destination contact), 1 destination connection, no direct delivery (the fixture name does not resolve on the host), and the service still answered after the idle gap",
		spread.Seconds(), idle.Seconds(), totalAccepts, probeNote)
}
