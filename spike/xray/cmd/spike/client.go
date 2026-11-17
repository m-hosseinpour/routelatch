package main

import (
	"context"
	"net"
	"sync"
	"time"

	commonnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
)

// errTracker implements session.TrackedRequestErrorFeedback. Xray reports
// outbound failures through the request context; there is no matching success
// callback. This type records whatever Xray actually delivers.
type trackedErr struct {
	at  time.Time
	err error
}

type errTracker struct {
	mu   sync.Mutex
	list []trackedErr
	ch   chan struct{}
}

func newErrTracker() *errTracker { return &errTracker{ch: make(chan struct{}, 1)} }

func (t *errTracker) SubmitError(err error) {
	t.mu.Lock()
	t.list = append(t.list, trackedErr{at: time.Now(), err: err})
	t.mu.Unlock()
	select {
	case t.ch <- struct{}{}:
	default:
	}
}

func (t *errTracker) all() []trackedErr {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]trackedErr, len(t.list))
	copy(out, t.list)
	return out
}

// waitError waits up to d for the first tracked error.
func (t *errTracker) waitError(d time.Duration) []trackedErr {
	if got := t.all(); len(got) > 0 {
		return got
	}
	select {
	case <-t.ch:
	case <-time.After(d):
	}
	return t.all()
}

type client struct {
	inst *core.Instance
	tr   transportKind
	port int
}

func startClient(tr transportKind, o clientOutbounds) (*client, error) {
	b, err := buildClientConfig(tr, o)
	if err != nil {
		return nil, err
	}
	inst, err := startCore(b)
	if err != nil {
		return nil, err
	}
	return &client{inst: inst, tr: tr, port: o.poolPort}, nil
}

// startClientRaw starts a core from a purpose-built config (fallback boundary
// check only).
func startClientRaw(configBytes []byte) (*client, error) {
	inst, err := startCore(configBytes)
	if err != nil {
		return nil, err
	}
	return &client{inst: inst, tr: "route-test"}, nil
}

func (c *client) stop() error {
	if c == nil || c.inst == nil {
		return nil
	}
	return c.inst.Close()
}

// attempt is one core.Dial call. The decisive measurement is the difference
// between tDial (core.Dial returned) and events observed at the peer.
type attempt struct {
	tag     string
	dest    string
	tStart  time.Time
	tDial   time.Time
	dialErr error
	conn    net.Conn
	track   *errTracker
	ctx     context.Context
	cancel  context.CancelFunc
}

func (c *client) dial(tag, host string, port int) *attempt {
	return c.dialWith(tag, host, port, 0)
}

// dialWith applies an optional caller deadline to the session context. A
// deadline is a caller-side cancellation, not an engine handshake timeout.
func (c *client) dialWith(tag, host string, port int, deadline time.Duration) *attempt {
	var ctx0 context.Context
	var cancel context.CancelFunc
	if deadline > 0 {
		ctx0, cancel = context.WithTimeout(context.Background(), deadline)
	} else {
		ctx0, cancel = context.WithCancel(context.Background())
	}
	tr := newErrTracker()
	// Force the outbound tag exactly as a pool selector would, then attach the
	// error tracker that Xray's outbound handler feeds on failure.
	ctx := session.SetForcedOutboundTagToContext(ctx0, tag)
	ctx = session.TrackedConnectionError(ctx, tr)
	dest := commonnet.TCPDestination(commonnet.ParseAddress(host), commonnet.Port(port))
	a := &attempt{tag: tag, dest: net.JoinHostPort(host, itoa(port)), track: tr, ctx: ctx0, cancel: cancel}
	a.tStart = time.Now()
	a.conn, a.dialErr = core.Dial(ctx, c.inst, dest)
	a.tDial = time.Now()
	return a
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

type readOutcome struct {
	data    []byte
	err     error
	at      time.Time
	timeout bool
}

// readFor reads with a caller-side timeout. core.Dial returns a cnc.Connection
// whose SetReadDeadline/SetWriteDeadline are no-ops, so a real timeout has to
// be built by abandoning a blocked Read and closing the connection.
func readFor(conn net.Conn, d time.Duration, max int) readOutcome {
	if max <= 0 {
		max = 64
	}
	type res struct {
		data []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, max)
		n, err := conn.Read(buf)
		if n > 0 {
			ch <- res{data: buf[:n], err: err}
			return
		}
		ch <- res{err: err}
	}()
	select {
	case r := <-ch:
		return readOutcome{data: r.data, err: r.err, at: time.Now()}
	case <-time.After(d):
		return readOutcome{err: errReadTimeout, at: time.Now(), timeout: true}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "caller-side read timeout (no data)" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var errReadTimeout = timeoutErr{}

func writeFor(conn net.Conn, data []byte, d time.Duration) (int, error, time.Time) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := conn.Write(data)
		ch <- res{n: n, err: err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err, time.Now()
	case <-time.After(d):
		return 0, errReadTimeout, time.Now()
	}
}

func closeAttempt(a *attempt) {
	if a == nil {
		return
	}
	if a.conn != nil {
		_ = a.conn.Close()
	}
	if a.cancel != nil {
		a.cancel()
	}
}
