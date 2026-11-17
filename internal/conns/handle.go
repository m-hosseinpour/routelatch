package conns

import (
	"context"
	"errors"
	"net"
	"sync"

	"routelatch/internal/contract"
)

type handle struct {
	reg  *Registry
	spec contract.ConnSpec

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	client   net.Conn
	upstream net.Conn
	exit     contract.ExitID
	proof    contract.ProofClass
	state    contract.ConnState
	closed   bool
	closeErr error
	// done is closed once both sockets are closed and the context canceled.
	// A second caller waits on it instead of returning while a close is still
	// in flight.
	done chan struct{}
}

var _ contract.ConnHandle = (*handle)(nil)

// ID implements contract.ConnHandle.
func (h *handle) ID() contract.ConnID { return h.spec.ID }

// Context implements contract.ConnHandle. It is canceled when the registry
// closes the connection and is the context every dial and copy loop must use.
func (h *handle) Context() context.Context { return h.ctx }

// SetClient implements contract.ConnHandle. The first accepted conn wins; a
// later call closes its argument and succeeds, so a duplicate call cannot leak
// a socket. On an already closed handle the conn is closed and a Canceled
// failure is returned, because nothing may be attached to a closed connection.
func (h *handle) SetClient(c net.Conn) error { return h.adopt(c, false) }

// SetUpstream implements contract.ConnHandle. Attaching the upstream conn is
// what moves a connection out of the dialing state: before that point the
// registry treats it as a pending dial that a sweep must cancel.
func (h *handle) SetUpstream(c net.Conn) error { return h.adopt(c, true) }

func (h *handle) adopt(c net.Conn, upstream bool) error {
	if c == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = c.Close()
		return &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "conns.set_conn",
			Destination: h.spec.Destination,
			Message:     "connection is closed",
		}
	}
	slot := &h.client
	if upstream {
		slot = &h.upstream
	}
	if *slot != nil {
		// Idempotent: the first conn is authoritative.
		h.mu.Unlock()
		_ = c.Close()
		return nil
	}
	*slot = c
	if upstream && h.state == contract.ConnDialing {
		h.state = contract.ConnOpen
	}
	h.mu.Unlock()
	return nil
}

// SetExit implements contract.ConnHandle. The first non-empty value wins so
// status can never disagree with the exit the dial actually used.
func (h *handle) SetExit(id contract.ExitID) {
	if id == "" {
		return
	}
	h.mu.Lock()
	if h.exit == "" {
		h.exit = id
	}
	h.mu.Unlock()
}

// SetProof implements contract.ConnHandle. It is set-once: the proof class the
// client was told is never overwritten by a later observation.
func (h *handle) SetProof(p contract.ProofClass) {
	if p == contract.ProofUnknown {
		return
	}
	h.mu.Lock()
	if h.proof == contract.ProofUnknown {
		h.proof = p
	}
	h.mu.Unlock()
}

// Close implements contract.ConnHandle. It cancels the connection context
// first, so a blocked dial observes the cancellation, then closes both
// sockets. It is idempotent and safe from any goroutine, and it never blocks
// on a copy loop.
func (h *handle) Close() error {
	_, _, err := h.closeFrom()
	return err
}

// closeFrom closes the connection and reports the state it was in when it was
// closed and whether it had already been closed (for example by its client).
// A sweep uses that to count only the connections it actually closed, even when
// a dial completes between the registry snapshot and the close pass.
func (h *handle) closeFrom() (contract.ConnState, bool, error) {
	h.mu.Lock()
	if h.closed {
		done := h.done
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		state, err := h.state, h.closeErr
		h.mu.Unlock()
		return state, true, err
	}
	was := h.state
	h.closed = true
	h.state = contract.ConnClosing
	client, upstream := h.client, h.upstream
	h.client, h.upstream = nil, nil
	h.mu.Unlock()

	h.cancel()

	var errs []error
	for _, c := range []net.Conn{client, upstream} {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}

	joined := errors.Join(errs...)
	h.mu.Lock()
	h.state = contract.ConnClosed
	h.closeErr = joined
	done := h.done
	h.mu.Unlock()
	h.reg.remove(h.spec.ID, h)
	close(done)

	return was, false, joined
}

func (h *handle) info() contract.ConnInfo {
	h.mu.Lock()
	state, exit, proof := h.state, h.exit, h.proof
	h.mu.Unlock()
	return contract.ConnInfo{
		ID:            h.spec.ID,
		Kind:          h.spec.Kind,
		Destination:   h.spec.Destination,
		Route:         h.spec.Decision.Route,
		Mode:          h.spec.Decision.Mode,
		Reason:        h.spec.Decision.Reason,
		Proof:         proof,
		State:         state,
		PolicyVersion: h.spec.Decision.PolicyVersion,
		Exit:          exit,
		StartedAt:     h.spec.StartedAt,
	}
}
