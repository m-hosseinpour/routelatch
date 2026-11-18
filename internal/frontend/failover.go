package frontend

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"routelatch/internal/contract"
)

// tunnelPool carries an HTTP CONNECT tunnel for an automatic pool exit and
// makes a pre-byte exit failure invisible to the client.
//
// The 200 is sent as soon as the pool dial returns a proven connection: a
// conforming client sends nothing until it sees 2xx (RFC 9110 §9.3.6), so
// withholding it would deadlock every TLS handshake. Commitment is instead
// tied to the destination's first byte. Until that byte arrives the client's
// bytes are both forwarded to the current exit and kept in a bounded replay
// buffer. If the exit ends the tunnel before answering, the bytes are replayed
// to a different automatic exit; the client sees one 200 and one tunnel, and
// only the successful exit's response is ever written to the client
// (docs/contracts.md §2.2, §3.4).
//
// A manual pin is never failed over: its pre-byte failure closes the tunnel
// and is reported as the pin's failure. Once the replay cap is reached, or a
// destination byte arrives, the current exit is committed and the ordinary
// half-close teardown applies; no byte is ever sent to two exits at the same
// time because this coordinator is the only writer to an upstream connection.
func (t *poolTunnel) execute() {
	t.start()
	t.run()
	if t.stopAbort != nil {
		t.stopAbort()
	}
	t.abort()
	t.wg.Wait()
}

func newPoolTunnel(p *httpProxy, h contract.ConnHandle, client net.Conn, clientR io.Reader, first net.Conn, firstExit contract.ExitID, firstProof contract.ProofClass, dec contract.Decision, dst contract.Destination, maxAttempts int) *poolTunnel {
	return &poolTunnel{
		ctx:      h.Context(),
		p:        p,
		h:        h,
		client:   client,
		clientR:  clientR,
		dec:      dec,
		dst:      dst,
		upstream: first,
		exit:     firstExit,
		proof:    firstProof,
		attempts: 1,
		max:      maxAttempts,
		tried:    []contract.ExitID{firstExit},
		clientCh: make(chan tunnelEvent),
		done:     make(chan struct{}),
	}
}

func (t *poolTunnel) start() {
	t.upCh = t.readUpstream(t.currentUpstream())
	t.wg.Add(1)
	go t.readClient()

	// A registry close cancels the handle's context. Closing the client and the
	// current upstream unblocks whichever copy or dial the coordinator is in;
	// retry attempts are not owned by the registry handle, so this is the only
	// place that tears them down.
	t.stopAbort = context.AfterFunc(t.ctx, t.abort)
}

// tunnelEvent is one read result from either side. data is delivered before a
// non-nil err from the same Read call.
type tunnelEvent struct {
	data []byte
	err  error
}

type upReader struct {
	ch   chan tunnelEvent
	done chan struct{}
}

type poolTunnel struct {
	ctx     context.Context
	p       *httpProxy
	h       contract.ConnHandle
	client  net.Conn
	clientR io.Reader
	dec     contract.Decision
	dst     contract.Destination

	// mu guards only the conn the abort callback may close. The coordinator is
	// the sole writer and the sole swapper of upstream.
	mu       sync.Mutex
	upstream net.Conn
	exit     contract.ExitID
	proof    contract.ProofClass

	attempts  int
	max       int
	tried     []contract.ExitID
	replay    []byte
	committed bool
	clientEOF bool
	upEOF     bool

	clientCh chan tunnelEvent
	upCh     *upReader

	wg        sync.WaitGroup
	done      chan struct{}
	once      sync.Once
	stopAbort func() bool
}

func (t *poolTunnel) readClient() {
	defer t.wg.Done()
	buf := make([]byte, 32<<10)
	for {
		n, err := t.clientR.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if !t.send(t.clientCh, tunnelEvent{data: data}) {
				return
			}
		}
		if err != nil {
			t.send(t.clientCh, tunnelEvent{err: err})
			return
		}
	}
}

func (t *poolTunnel) readUpstream(conn net.Conn) *upReader {
	r := &upReader{ch: make(chan tunnelEvent), done: make(chan struct{})}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				if !t.sendReader(r, tunnelEvent{data: data}) {
					return
				}
			}
			if err != nil {
				t.sendReader(r, tunnelEvent{err: err})
				return
			}
		}
	}()
	return r
}

func (t *poolTunnel) send(ch chan tunnelEvent, ev tunnelEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-t.done:
		return false
	case <-t.ctx.Done():
		return false
	}
}

func (t *poolTunnel) sendReader(r *upReader, ev tunnelEvent) bool {
	select {
	case r.ch <- ev:
		return true
	case <-r.done:
		return false
	case <-t.done:
		return false
	case <-t.ctx.Done():
		return false
	}
}

func (t *poolTunnel) abort() {
	t.once.Do(func() { close(t.done) })
	t.mu.Lock()
	upstream := t.upstream
	t.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
	_ = t.client.Close()
}

func (t *poolTunnel) currentUpstream() net.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upstream
}

func (t *poolTunnel) setUpstream(c net.Conn, exit contract.ExitID) {
	t.mu.Lock()
	t.upstream = c
	t.exit = exit
	t.mu.Unlock()
}

func (t *poolTunnel) currentExit() contract.ExitID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exit
}

func (t *poolTunnel) run() {
	for {
		select {
		case <-t.ctx.Done():
			return
		case ev := <-t.clientCh:
			if len(ev.data) > 0 && !t.onClientData(ev.data) {
				return
			}
			if ev.err != nil && !t.onClientEnd(ev.err) {
				return
			}
		case ev := <-t.upCh.ch:
			if len(ev.data) > 0 && !t.onUpstreamData(ev.data) {
				return
			}
			if ev.err != nil && !t.onUpstreamEnd(ev.err) {
				return
			}
		}
		if t.clientEOF && t.upEOF {
			return
		}
	}
}

func (t *poolTunnel) onClientData(data []byte) bool {
	if !t.committed {
		if len(t.replay)+len(data) > contract.ConnectReplayBufferBytes {
			// Retrying would require dropping bytes the client already sent.
			// Commit to this exit and behave like the ordinary tunnel.
			t.committed = true
			t.replay = nil
		} else {
			t.replay = append(t.replay, data...)
		}
	}
	if err := writeAll(t.currentUpstream(), data); err != nil {
		if !t.committed {
			return t.nextAttempt()
		}
		return false
	}
	return true
}

func (t *poolTunnel) onClientEnd(err error) bool {
	if !errors.Is(err, io.EOF) {
		// A reset or any other client read error means the client is gone; its
		// failure is not attributable to an exit.
		return false
	}
	t.clientEOF = true
	closeWrite(t.currentUpstream())
	return true
}

func (t *poolTunnel) onUpstreamData(data []byte) bool {
	if !t.committed {
		t.commit()
	}
	if err := writeAll(t.client, data); err != nil {
		return false
	}
	return true
}

// commit fixes the serving exit and its proof class in the registry. Before the
// first destination byte the connection is still retryable, so the handle must
// not report an exit that may be replaced (docs/contracts.md §2.2).
func (t *poolTunnel) commit() {
	t.committed = true
	t.replay = nil
	t.h.SetExit(t.currentExit())
	t.h.SetProof(t.proof)
}

func (t *poolTunnel) onUpstreamEnd(err error) bool {
	if !t.committed {
		return t.nextAttempt()
	}
	if errors.Is(err, io.EOF) {
		t.upEOF = true
		closeWrite(t.client)
		return true
	}
	return false
}

// nextAttempt reports the current exit's pre-byte failure and tries the next
// automatic exit with the same client bytes. It returns false when the budget
// is exhausted; the client then sees only the teardown of a tunnel that was
// never committed, and no direct attempt is made.
func (t *poolTunnel) nextAttempt() bool {
	for {
		if t.ctx.Err() != nil {
			return false
		}
		exit := t.currentExit()
		t.reportFailure(exit)
		t.abandonUpstream()
		if t.attempts >= t.max {
			return false
		}
		upstream, nextExit, nextProof, _, ferr := t.p.f.dial(t.ctx, t.dec, t.dst, t.tried)
		if ferr != nil {
			return false
		}
		t.attempts++
		t.tried = append(t.tried, nextExit)
		t.setUpstream(upstream, nextExit)
		t.proof = nextProof
		t.upCh = t.readUpstream(upstream)
		if len(t.replay) > 0 {
			if err := writeAll(upstream, t.replay); err != nil {
				_ = upstream.Close()
				continue
			}
		}
		if t.clientEOF {
			// The replay completed the client's write side; carry the FIN.
			closeWrite(upstream)
		}
		return true
	}
}

// abandonUpstream closes the current exit connection and stops its reader.
// The connection is closed instead of merely dropped so the reader goroutine
// cannot outlive the attempt.
func (t *poolTunnel) abandonUpstream() {
	t.mu.Lock()
	upstream := t.upstream
	t.upstream = nil
	t.exit = ""
	t.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
	if t.upCh != nil {
		close(t.upCh.done)
		t.upCh = nil
	}
}

func (t *poolTunnel) reportFailure(exit contract.ExitID) {
	if exit == "" || t.p.f.opts.ReportExitFailure == nil {
		return
	}
	// Pre-byte tunnel death keeps its honest kind: the destination side is
	// unknown (docs/contracts.md §2.3, §3.1). The service records it as an
	// exit-level failed sample, never as a destination confirmation.
	t.p.f.opts.ReportExitFailure(exit, contract.DestinationUnconfirmed)
}

// lifetimeConn is what the registry handle owns for a failover-capable tunnel.
// Close tears down whichever exit the coordinator currently holds, including a
// retry attempt that the handle never saw, so a mode/rule sweep closes the
// whole tunnel synchronously rather than waiting for a context callback.
type lifetimeConn struct {
	net.Conn
	closeOnce sync.Once
	closeFn   func()
}

func (l *lifetimeConn) Close() error {
	l.closeOnce.Do(l.closeFn)
	return nil
}
