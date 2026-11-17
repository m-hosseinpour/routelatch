package frontend

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	// clientWatchPoll bounds how quickly a reset client cancels a pending
	// dial. It only runs while a dial is pending; docs/contracts.md §3.4 asks
	// for <= 50ms (measured work is microseconds, this leaves margin).
	clientWatchPoll = 10 * time.Millisecond
	// clientWatchCaptureCap bounds bytes buffered while a dial is pending. At
	// the cap the watch stops reading; every byte stays either in the buffer
	// (replayed before the copy) or in the kernel/bufio reader, so exceeding
	// the cap never truncates a body or early tunnel data.
	clientWatchCaptureCap = 64 << 10
)

// clientWatch observes a client while its dial is pending.
//
// FIN/close policy: TCP cannot distinguish a full close from a write
// half-close, and a half-close is a legitimate protocol state (for example a
// bodyless forward request, or a one-way tunnel) that must still receive the
// reply. This watch therefore treats a clean FIN as a half-close: it stops
// watching without canceling, the captured bytes are replayed, and the tunnel
// forwards the FIN with CloseWrite after the reply. A reset or any other read
// error is a vanished client and does cancel the pending dial (the contract's
// canceled kind includes "client went away"). The cost of the FIN choice is
// explicit: a client that cleanly closes during a pending dial is not
// detected there; the dial is bounded by its own dialer (PoolDialDeadline for
// the SOCKS5 exit handshake, DirectDialDeadline for the direct path; the
// core-backed pool path has no observable dial phase, §2.1) and the first
// copy or a registry close ends the connection. This is the
// only consistent choice, because canceling on FIN would reject valid
// half-closing clients.
//
// The watch reads through the connection's bufio.Reader, never more than the
// remaining capture room, so bytes beyond the cap stay in the reader. Any
// consumed bytes are replayed in order before the tunnel/body copy starts. The
// read deadline is only a liveness poll: it is cleared before any copy and is
// never set on an established tunnel (docs/contracts.md §3.4).
type clientWatch struct {
	conn   net.Conn
	br     *bufio.Reader
	onGone func()

	done     chan struct{}
	finished chan struct{}
	once     sync.Once

	mu       sync.Mutex
	captured []byte
	gone     bool
}

func startClientWatch(conn net.Conn, br *bufio.Reader, onGone func()) *clientWatch {
	w := &clientWatch{
		conn:     conn,
		br:       br,
		onGone:   onGone,
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *clientWatch) run() {
	defer close(w.finished)
	defer func() { _ = w.conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 4096)
	for {
		select {
		case <-w.done:
			return
		default:
		}

		w.mu.Lock()
		room := clientWatchCaptureCap - len(w.captured)
		w.mu.Unlock()
		if room <= 0 {
			return
		}
		readBuf := buf
		if room < len(readBuf) {
			readBuf = readBuf[:room]
		}

		if err := w.conn.SetReadDeadline(time.Now().Add(clientWatchPoll)); err != nil {
			return
		}
		n, err := w.br.Read(readBuf)
		if n > 0 {
			w.mu.Lock()
			w.captured = append(w.captured, readBuf[:n]...)
			full := len(w.captured) >= clientWatchCaptureCap
			w.mu.Unlock()
			if full {
				// The client is alive and streaming; stop buffering and leave
				// every further byte in the reader for the copy loop.
				return
			}
		}

		switch {
		case err == nil:
			continue
		case isTimeoutError(err):
			continue
		case errors.Is(err, io.EOF):
			// A clean FIN is a half-close, not a vanished client. The capture
			// is replayed and the tunnel forwards the FIN after the reply.
			return
		default:
			w.reportGone()
			return
		}
	}
}

func (w *clientWatch) reportGone() {
	w.once.Do(func() {
		w.mu.Lock()
		w.gone = true
		w.mu.Unlock()
		if w.onGone != nil {
			w.onGone()
		}
	})
}

// stop ends the watch, waits for the reader to exit, clears the liveness
// deadline, and returns the bytes that must be replayed before the copy.
func (w *clientWatch) stop() (captured []byte, gone bool) {
	close(w.done)
	<-w.finished
	_ = w.conn.SetReadDeadline(time.Time{})
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.captured...), w.gone
}

func isTimeoutError(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
