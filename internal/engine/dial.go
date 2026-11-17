package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	commonnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"

	"routelatch/internal/contract"
)

// trackedErrorGrace is the settle window used when a session ended before any
// downlink byte and the core has not delivered a tracked error yet. T1 measured
// the tracked error 1ms before the read error on a refused exit (2.006s vs
// 2.007s); without the window a race could label an attributable transport
// failure as an unconfirmed destination, which would change the health signal.
// It is only ever paid on a connection that is already failing.
const trackedErrorGrace = 10 * time.Millisecond

// validateDialDestination rejects anything that must never reach a dial. Port 0
// means "not stated" in Destination and is only valid while parsing.
func validateDialDestination(dst contract.Destination) error {
	bad := func(message string) error {
		return &contract.Failure{
			Kind:        contract.InvalidDestination,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Destination: dst,
			Message:     message,
		}
	}
	switch dst.Kind {
	case contract.DestDomain:
		if dst.Host == "" {
			return bad("destination host is missing")
		}
	case contract.DestIP:
		if !dst.Addr.IsValid() || dst.Addr.IsUnspecified() {
			return bad("destination address is not usable")
		}
	default:
		return bad("destination kind is not dialable")
	}
	if dst.Port == 0 {
		return bad("destination port is missing")
	}
	return nil
}

// xrayDestination keeps a hostname as a domain so only the exit resolves it
// (docs/contracts.md §1.7). The direct path is the only place that resolves.
func xrayDestination(dst contract.Destination) (commonnet.Destination, error) {
	var address commonnet.Address
	switch dst.Kind {
	case contract.DestDomain:
		address = commonnet.ParseAddress(dst.Host)
	case contract.DestIP:
		address = commonnet.ParseAddress(dst.Addr.String())
	default:
		return commonnet.Destination{}, &contract.Failure{
			Kind:        contract.InvalidDestination,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Destination: dst,
			Message:     "destination kind is not dialable",
		}
	}
	return commonnet.TCPDestination(address, commonnet.Port(dst.Port)), nil
}

// dialCore dispatches through one explicit tag on the running core. The forced
// tag is what makes the anti-fallback property structural: the dispatcher either
// uses that handler or fails, and the default handler is a blackhole
// (spike/xray/FINDINGS.md §6).
//
// The pinned core confirms nothing positive: core.Dial returns before the exit
// is contacted, so the result is always ProofAccepted and first-read errors are
// where exit/destination evidence appears (docs/contracts.md §2.1–2.3).
func (e *Engine) dialCore(ctx context.Context, inst *core.Instance, spec *exitSpec, dst contract.Destination) (DialResult, error) {
	target, err := xrayDestination(dst)
	if err != nil {
		return DialResult{}, err
	}
	tracker := &errorTracker{}
	sessionCtx, cancel := context.WithCancel(ctx)
	sessionCtx = session.SetForcedOutboundTagToContext(sessionCtx, spec.exit.Tag)
	sessionCtx = session.TrackedConnectionError(sessionCtx, tracker)

	conn, dialErr := core.Dial(sessionCtx, inst, target)
	accepted := time.Now()
	if dialErr != nil {
		cancel()
		if ctx.Err() != nil {
			return DialResult{}, ctxFailure(ctx, spec.exit.ID, dst)
		}
		return DialResult{}, exitUnreachable("the engine could not dispatch to the exit", spec.exit.ID, dst, dialErr)
	}

	pc := &poolConn{
		Conn:    conn,
		ctx:     ctx,
		cancel:  cancel,
		tracker: tracker,
		engine:  e,
		exit:    spec.exit.ID,
		dest:    dst,
	}
	if !e.track(pc) {
		_ = pc.Close()
		return DialResult{}, &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Exit:        spec.exit.ID,
			Destination: dst,
			Message:     "the engine is stopping",
		}
	}
	e.noteWarm(spec)
	return DialResult{
		Conn:       pc,
		Exit:       spec.exit.ID,
		Tag:        spec.exit.Tag,
		Proof:      contract.ProofAccepted,
		AcceptedAt: accepted,
	}, nil
}

// errorTracker implements session.TrackedRequestErrorFeedback. The pinned core
// only reports failures through it; there is no success event.
type errorTracker struct {
	mu  sync.Mutex
	err error
	ch  chan struct{}
}

func (t *errorTracker) SubmitError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.err == nil {
		t.err = err
		if t.ch == nil {
			t.ch = make(chan struct{})
		}
		ch := t.ch
		close(ch)
	}
	t.mu.Unlock()
}

func (t *errorTracker) first() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// wait tries to observe a tracked error within d. It is only called on an
// already failing session.
func (t *errorTracker) wait(d time.Duration) error {
	if err := t.first(); err != nil {
		return err
	}
	t.mu.Lock()
	if t.ch == nil {
		t.ch = make(chan struct{})
	}
	ch := t.ch
	t.mu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return t.first()
	case <-timer.C:
		return t.first()
	}
}

// poolConn is the net.Conn a pool dial returns. It owns the session context and
// the tracked-error channel, so a caller can learn the honest failure kind
// without the engine inventing a positive confirmation.
type poolConn struct {
	net.Conn

	ctx     context.Context
	cancel  context.CancelFunc
	tracker *errorTracker
	engine  *Engine
	exit    contract.ExitID
	dest    contract.Destination

	closeOnce sync.Once
	closed    atomic.Bool
	mu        sync.Mutex
	gotDown   bool
}

func (c *poolConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.gotDown = true
		c.mu.Unlock()
	}
	if err == nil || n > 0 {
		return n, err
	}
	return 0, c.classify(err, true)
}

func (c *poolConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil {
		return n, nil
	}
	return n, c.classify(err, false)
}

func (c *poolConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancel()
		err = c.Conn.Close()
		c.engine.untrack(c)
	})
	return err
}

// classify turns a stream error into the contract's kind. The order matters:
// our own cancellation wins over anything the core reported, because it names
// the cause the client will see.
func (c *poolConn) classify(err error, read bool) error {
	if c.closed.Load() {
		return &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathPool,
			Op:          "engine.session",
			Exit:        c.exit,
			Destination: c.dest,
			Message:     "the connection was closed",
			Err:         err,
		}
	}
	if ctxErr := c.ctx.Err(); ctxErr != nil {
		return ctxFailure(c.ctx, c.exit, c.dest)
	}
	if tracked := c.tracker.first(); tracked != nil {
		return exitUnreachable("the exit transport failed", c.exit, c.dest, tracked)
	}
	if read {
		c.mu.Lock()
		gotDown := c.gotDown
		c.mu.Unlock()
		if !gotDown {
			if tracked := c.tracker.wait(trackedErrorGrace); tracked != nil {
				return exitUnreachable("the exit transport failed", c.exit, c.dest, tracked)
			}
			if errors.Is(err, io.EOF) || isClosedStream(err) {
				return destinationUnconfirmed(c.exit, c.dest, err)
			}
		}
	}
	if read && errors.Is(err, io.EOF) {
		return io.EOF
	}
	return destinationUnconfirmed(c.exit, c.dest, err)
}

// isClosedStream matches the way a torn-down xray session reports a local
// close. It is not a destination cause on its own.
func isClosedStream(err error) bool {
	return err != nil && (errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		err.Error() == "io: read/write on closed pipe")
}
