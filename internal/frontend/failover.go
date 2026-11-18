package frontend

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"routelatch/internal/contract"
)

// execute carries an HTTP CONNECT tunnel for a pool exit and makes a pre-byte
// exit failure invisible to the client.
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
		forward:  make(chan struct{}),
		downDone: make(chan struct{}),
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
// non-nil err from the same Read call. buf is the pooled buffer that owns data;
// the coordinator returns it after the write so bulk tunnels do not allocate a
// fresh 32 KiB slice per chunk.
type tunnelEvent struct {
	data []byte
	buf  []byte
	err  error
}

var tunnelBufPool = sync.Pool{
	New: func() any { return make([]byte, 32<<10) },
}

func freeTunnelBuf(buf []byte) {
	if buf != nil {
		tunnelBufPool.Put(buf)
	}
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
	committed atomic.Bool
	sawDown   atomic.Bool
	clientEOF bool
	upEOF     bool

	clientCh chan tunnelEvent
	upCh     *upReader

	// forward is closed once when the destination's first byte commits the
	// tunnel; a reader waiting with that byte then writes it directly to the
	// client. downDone is closed when the reader observes an upstream EOF.
	forward     chan struct{}
	forwardOnce sync.Once
	downDone    chan struct{}
	downOnce    sync.Once

	wg        sync.WaitGroup
	done      chan struct{}
	once      sync.Once
	stopAbort func() bool
}

func (t *poolTunnel) readClient() {
	defer t.wg.Done()
	buf := tunnelBufPool.Get().([]byte)
	for {
		n, err := t.clientR.Read(buf)
		if n > 0 {
			if !t.send(t.clientCh, tunnelEvent{data: buf[:n], buf: buf}) {
				freeTunnelBuf(buf)
				return
			}
			// The coordinator owns the previous buffer until it finishes the
			// write; an empty pool here simply allocates another one.
			buf = tunnelBufPool.Get().([]byte)
		}
		if err != nil {
			freeTunnelBuf(buf)
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
		buf := tunnelBufPool.Get().([]byte)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				t.sawDown.Store(true)
				ev := tunnelEvent{data: buf[:n], buf: buf}
				if t.committed.Load() {
					// The tunnel is committed: this reader is the only writer
					// to the client, so a slow client cannot stall the upload
					// copy that the coordinator runs in parallel.
					if !t.writeDown(ev) {
						return
					}
				} else {
					// Hand the first byte to the coordinator, which decides
					// whether this attempt is the one that serves the client.
					if !t.sendReader(r, ev) {
						freeTunnelBuf(buf)
						return
					}
					select {
					case <-t.forward:
					case <-r.done:
						freeTunnelBuf(buf)
						return
					case <-t.done:
						freeTunnelBuf(buf)
						return
					case <-t.ctx.Done():
						freeTunnelBuf(buf)
						return
					}
					if !t.writeDown(ev) {
						return
					}
				}
				if err != nil {
					t.endDown(err)
					return
				}
				buf = tunnelBufPool.Get().([]byte)
				continue
			}
			if err != nil {
				if t.committed.Load() {
					t.endDown(err)
					return
				}
				t.sendReader(r, tunnelEvent{err: err})
				return
			}
		}
	}()
	return r
}

// writeDown writes one destination chunk and returns its pooled buffer. A
// client write failure is a client-side teardown, not an exit failure.
func (t *poolTunnel) writeDown(ev tunnelEvent) bool {
	err := writeAll(t.client, ev.data)
	freeTunnelBuf(ev.buf)
	if err != nil {
		t.abort()
		return false
	}
	return true
}

// endDown handles the end of the committed downstream stream: a clean EOF is a
// half-close the tunnel forwards, anything else tears the tunnel down.
func (t *poolTunnel) endDown(err error) {
	if errors.Is(err, io.EOF) {
		closeWrite(t.client)
		t.downOnce.Do(func() { close(t.downDone) })
		return
	}
	t.abort()
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
	// downDone is closed once; a closed channel in a select case would fire on
	// every iteration. Nil it after the first observation.
	downDone := t.downDone
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.done:
			return
		case ev := <-t.clientCh:
			if len(ev.data) > 0 && !t.onClientData(ev.data) {
				freeTunnelBuf(ev.buf)
				return
			}
			if ev.err != nil && !t.onClientEnd(ev.err) {
				freeTunnelBuf(ev.buf)
				return
			}
			freeTunnelBuf(ev.buf)
		case ev := <-t.upCh.ch:
			if len(ev.data) > 0 {
				// The first destination byte commits this attempt. The
				// reader owns ev.buf until it has written the byte after the
				// forward signal, so it must not be freed here.
				t.commit()
				continue
			}
			if ev.err != nil {
				if t.committed.Load() {
					// The EOF was queued while the attempt was uncommitted, but
					// a client chunk then committed it at the replay cap. It is
					// a post-commit end, never a pre-byte retry.
					if errors.Is(ev.err, io.EOF) {
						t.upEOF = true
						closeWrite(t.client)
					} else {
						return
					}
				} else if !t.nextAttempt() {
					return
				}
			}
		case <-downDone:
			t.upEOF = true
			downDone = nil
		}
		if t.clientEOF && t.upEOF {
			return
		}
	}
}

func (t *poolTunnel) onClientData(data []byte) bool {
	if !t.committed.Load() {
		if len(t.replay)+len(data) > contract.ConnectReplayBufferBytes {
			// Retrying would require dropping bytes the client already sent.
			// Commit to this exit and behave like the ordinary tunnel.
			t.commit()
		} else {
			t.replay = append(t.replay, data...)
		}
	}
	if err := writeAll(t.currentUpstream(), data); err != nil {
		if t.committed.Load() || t.sawDown.Load() {
			// A destination byte already exists (possibly still in the
			// reader's handoff). Never retry or cool an exit that carried it.
			t.commit()
			return false
		}
		return t.nextAttempt()
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

// commit fixes the serving exit and its proof class in the registry and releases
// a reader waiting with the destination's first byte. Before that byte the
// connection is still retryable, so the handle must not report a proof for an
// exit that may be replaced (docs/contracts.md §2.2).
func (t *poolTunnel) commit() {
	t.forwardOnce.Do(func() {
		t.committed.Store(true)
		t.replay = nil
		t.h.SetExit(t.currentExit())
		t.h.SetProof(t.proof)
		close(t.forward)
	})
}

// nextAttempt reports the current exit's pre-byte failure and tries the next
// automatic exit with the same client bytes. It returns false when the budget
// is exhausted; the client then sees only the teardown of a tunnel that was
// never committed, and no direct attempt is made.
func (t *poolTunnel) nextAttempt() bool {
	if t.committed.Load() {
		// Commitment closes the window even if an old error event is still in
		// flight (for example an EOF queued just before the replay cap).
		return false
	}
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
		upstream, nextExit, nextProof, nextPinned, ferr := t.p.f.dial(t.ctx, t.dec, t.dst, t.tried)
		if ferr != nil {
			return false
		}
		t.attempts++
		if nextPinned {
			// A manual selection appeared while this connection was in flight.
			// Use it for this attempt but never chain another retry from it.
			t.max = t.attempts
		}
		t.tried = append(t.tried, nextExit)
		t.setUpstream(upstream, nextExit)
		t.h.SetExit(nextExit)
		t.proof = nextProof
		t.upCh = t.readUpstream(upstream)
		if len(t.replay) > 0 {
			if err := writeAll(upstream, t.replay); err != nil {
				// This attempt accepted the dial but failed during replay; the
				// next loop reports it and tries another exit. Stop its reader
				// now so it does not wait for the whole connection.
				_ = upstream.Close()
				if t.upCh != nil {
					close(t.upCh.done)
					t.upCh = nil
				}
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
