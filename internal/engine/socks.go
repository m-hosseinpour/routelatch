package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// SOCKS5 adapter.
//
// A SOCKS5 exit is dialed by this package, not by the pinned core, because the
// core exposes no positive destination event and its SOCKS client hides the
// reply (docs/contracts.md §2.1). Our client is deliberately thin:
//
//   - it offers no-auth, plus username/password only when the record carries
//     credentials, and nothing else;
//   - it sends a hostname as ATYP=0x03, i.e. unresolved, so the exit's resolver
//     handles it (D15). This is asserted by a fake-peer test;
//   - it parses the reply itself: 0x00 is ProofConfirmed, 0x05 is
//     DestinationRefused (the exit reported a refused destination), and every
//     other reply code is a typed HandshakeRejected instead of a dial success.
//     The raw code is kept in Err for logs, never in the message;
//   - it honors the caller's context, and applies contract.PoolDialDeadline
//     when the caller supplied no deadline, because this handshake really does
//     block (unlike the core path).

const (
	socksVersion5   byte = 0x05
	socksAuthNone   byte = 0x00
	socksAuthPass   byte = 0x02
	socksCmdConnect byte = 0x01

	socksRepSucceeded    byte = 0x00
	socksRepRefused      byte = 0x05
	socksRepNoAcceptable byte = 0xff
)

func (e *Engine) dialSOCKS5(ctx context.Context, spec *exitSpec, dst contract.Destination) (DialResult, error) {
	rec := spec.record

	dialCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, contract.PoolDialDeadline)
		defer cancel()
	}
	deadline, _ := dialCtx.Deadline()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(rec.Server, strconv.Itoa(int(rec.Port))))
	if err != nil {
		return DialResult{}, adapterFailure(dialCtx, spec.exit.ID, dst, "the exit address could not be reached", err)
	}

	// The timer hook unblocks a handshake read or write that is waiting on a
	// silent peer. deadlineMu makes "the handshake is over" and the hook
	// mutually exclusive, so a timer that fires exactly at the boundary cannot
	// put an immediate deadline on an established tunnel (finding F1's edge).
	var deadlineMu sync.Mutex
	handshaking := true
	stop := context.AfterFunc(dialCtx, func() {
		deadlineMu.Lock()
		if handshaking {
			_ = conn.SetDeadline(time.Now())
		}
		deadlineMu.Unlock()
	})
	defer stop()
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
	}

	// The exit socket is tracked before the handshake, so Stop closes a dial
	// that is still waiting for a silent exit instead of leaving it to the
	// caller's deadline.
	ac := &adapterConn{Conn: conn, engine: e, ctx: ctx, exit: spec.exit.ID, dest: dst}
	if !e.trackAdapter(ac) {
		_ = ac.Close()
		return DialResult{}, &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Exit:        spec.exit.ID,
			Destination: dst,
			Message:     "the engine is stopping",
		}
	}

	if err := socksHandshake(conn, spec.exit.ID, rec.Credentials.UserID, rec.Credentials.Password, dst); err != nil {
		closedByUs := ac.wasClosed()
		_ = ac.Close()
		if closedByUs {
			return DialResult{}, &contract.Failure{
				Kind:        contract.Canceled,
				Path:        contract.PathLocal,
				Op:          "engine.dial",
				Exit:        spec.exit.ID,
				Destination: dst,
				Message:     "the engine is stopping",
			}
		}
		var failure *contract.Failure
		if errors.As(err, &failure) {
			return DialResult{}, failure
		}
		return DialResult{}, adapterFailure(dialCtx, spec.exit.ID, dst, "the exit SOCKS5 handshake failed", err)
	}

	// The handshake succeeded; from here the connection is a plain tunnel with
	// no deadline of ours. Under deadlineMu the hook either already ran (our
	// clear wins) or will see handshaking=false, so an established tunnel can
	// never inherit the dial bound.
	deadlineMu.Lock()
	handshaking = false
	_ = conn.SetDeadline(time.Time{})
	deadlineMu.Unlock()
	stop()
	accepted := time.Now()
	ac.arm()

	return DialResult{
		Conn:       ac,
		Exit:       spec.exit.ID,
		Tag:        "",
		Proof:      contract.ProofConfirmed,
		AcceptedAt: accepted,
	}, nil
}

// socksHandshake runs greeting, optional authentication and one CONNECT. It
// returns a *contract.Failure for every protocol-level answer and a plain error
// only for transport problems the caller should classify.
func socksHandshake(conn net.Conn, exit contract.ExitID, user, password string, dst contract.Destination) error {
	methods := []byte{socksAuthNone}
	if user != "" {
		methods = append(methods, socksAuthPass)
	}
	greeting := append([]byte{socksVersion5, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != socksVersion5 {
		return handshakeRejected("the exit is not a SOCKS5 proxy", exit, dst, nil)
	}
	switch reply[1] {
	case socksAuthNone:
	case socksAuthPass:
		if user == "" {
			return handshakeRejected("the exit requires SOCKS5 authentication", exit, dst, nil)
		}
		if err := socksAuthenticate(conn, exit, dst, user, password); err != nil {
			return err
		}
	case socksRepNoAcceptable:
		return handshakeRejected("the exit rejected every SOCKS5 authentication method", exit, dst, nil)
	default:
		return handshakeRejected("the exit selected an unsupported authentication method", exit, dst, fmt.Errorf("method %#x", reply[1]))
	}

	request, err := socksConnectRequest(dst)
	if err != nil {
		return err
	}
	if _, err := conn.Write(request); err != nil {
		return err
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != socksVersion5 {
		return handshakeRejected("the exit answered with an invalid SOCKS5 reply", exit, dst, nil)
	}
	if err := discardSocksAddress(conn, head[3]); err != nil {
		if errors.Is(err, errUnknownSocksATYP) {
			return handshakeRejected("the exit replied with an unknown address type", exit, dst, nil)
		}
		return err
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return err
	}

	switch head[1] {
	case socksRepSucceeded:
		return nil
	case socksRepRefused:
		// The exit reported the destination connection as refused. This is a
		// destination outcome backed by the exit's own reply, not a rejection
		// of our request, and the proof class stays Confirmed.
		return destinationRefused("the exit reported the destination as refused", exit, dst, fmt.Errorf("socks5 reply %#x", head[1]))
	default:
		return handshakeRejected("the exit rejected the SOCKS5 request", exit, dst, fmt.Errorf("socks5 reply %#x", head[1]))
	}
}

func socksAuthenticate(conn net.Conn, exit contract.ExitID, dst contract.Destination, user, password string) error {
	if len(user) > 255 || len(password) > 255 {
		return errors.New("socks5 credentials are too long")
	}
	buf := make([]byte, 0, 3+len(user)+len(password))
	buf = append(buf, 0x01, byte(len(user)))
	buf = append(buf, user...)
	buf = append(buf, byte(len(password)))
	buf = append(buf, password...)
	if _, err := conn.Write(buf); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != 0x01 {
		return handshakeRejected("the exit answered with an invalid SOCKS5 authentication reply", exit, dst, nil)
	}
	if reply[1] != 0x00 {
		return &contract.Failure{
			Kind:        contract.HandshakeRejected,
			Path:        contract.PathPool,
			Op:          "engine.socks5",
			Exit:        exit,
			Destination: dst,
			Message:     "the exit rejected the SOCKS5 credentials",
		}
	}
	return nil
}

// socksConnectRequest encodes the destination. A hostname is never resolved
// here: it goes out as a domain address (ATYP 0x03) and the exit resolves it.
func socksConnectRequest(dst contract.Destination) ([]byte, error) {
	buf := []byte{socksVersion5, socksCmdConnect, 0x00}
	switch dst.Kind {
	case contract.DestDomain:
		if len(dst.Host) == 0 || len(dst.Host) > 255 {
			return nil, &contract.Failure{
				Kind:        contract.InvalidDestination,
				Path:        contract.PathLocal,
				Op:          "engine.socks5",
				Destination: dst,
				Message:     "destination host is not usable",
			}
		}
		buf = append(buf, 0x03, byte(len(dst.Host)))
		buf = append(buf, dst.Host...)
	case contract.DestIP:
		if dst.Addr.Is4() {
			buf = append(buf, 0x01)
			addr := dst.Addr.As4()
			buf = append(buf, addr[:]...)
		} else {
			buf = append(buf, 0x04)
			addr := dst.Addr.As16()
			buf = append(buf, addr[:]...)
		}
	default:
		return nil, &contract.Failure{
			Kind:        contract.InvalidDestination,
			Path:        contract.PathLocal,
			Op:          "engine.socks5",
			Destination: dst,
			Message:     "destination kind is not dialable",
		}
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], dst.Port)
	return append(buf, port[:]...), nil
}

var errUnknownSocksATYP = errors.New("unknown SOCKS5 address type")

func discardSocksAddress(conn net.Conn, atyp byte) error {
	switch atyp {
	case 0x01: // IPv4
		_, err := io.CopyN(io.Discard, conn, 4)
		return err
	case 0x04: // IPv6
		_, err := io.CopyN(io.Discard, conn, 16)
		return err
	case 0x03: // domain
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		_, err := io.CopyN(io.Discard, conn, int64(length[0]))
		return err
	default:
		return fmt.Errorf("%w %#x", errUnknownSocksATYP, atyp)
	}
}

// adapterFailure names why the adapter could not reach or complete a handshake
// with the exit. It is never a destination conclusion.
func adapterFailure(dialCtx context.Context, exit contract.ExitID, dst contract.Destination, message string, cause error) error {
	if err := dialCtx.Err(); err != nil {
		f := ctxFailure(dialCtx, exit, dst)
		f.Op = "engine.socks5"
		f.Message = "the pool dial deadline expired"
		if !errors.Is(err, context.DeadlineExceeded) {
			f.Message = "the operation was canceled"
		}
		f.Err = errors.Join(cause, err)
		return f
	}
	// The socket deadline can fire a moment before the context timer, and a
	// connect that hits the OS timeout arrives with no context error at all.
	// Both are the deadline expiring, not an unreachable exit.
	var timeout interface{ Timeout() bool }
	if errors.As(cause, &timeout) && timeout.Timeout() {
		return &contract.Failure{
			Kind:        contract.Timeout,
			Path:        contract.PathPool,
			Op:          "engine.socks5",
			Exit:        exit,
			Destination: dst,
			Retryable:   true,
			Message:     "the pool dial deadline expired",
			Err:         cause,
		}
	}
	return exitUnreachable(message, exit, dst, cause)
}

// adapterConn is the tunnel to a SOCKS5 exit. The engine owns it so Stop closes
// it even if the caller forgets, and its Read/Write surface the same typed
// kinds as a core-path connection: a SOCKS stream has no destination
// acknowledgement after the initial reply, so any later end is unconfirmed
// unless it is a clean EOF after downlink data.
type adapterConn struct {
	net.Conn
	engine *Engine
	ctx    context.Context
	exit   contract.ExitID
	dest   contract.Destination

	mu        sync.Mutex
	closed    bool
	gotDown   bool
	stopWatch func() bool

	once sync.Once
}

// arm closes the tunnel when the session context ends, but only after the
// handshake; the dial context is canceled as soon as Dial returns.
func (c *adapterConn) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.stopWatch = context.AfterFunc(c.ctx, func() { _ = c.Close() })
}

func (c *adapterConn) wasClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *adapterConn) Read(p []byte) (int, error) {
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

func (c *adapterConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil {
		return n, nil
	}
	return n, c.classify(err, false)
}

func (c *adapterConn) Close() error {
	var err error
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		stopWatch := c.stopWatch
		c.mu.Unlock()
		if stopWatch != nil {
			stopWatch()
		}
		err = c.Conn.Close()
		c.engine.untrackAdapter(c)
	})
	return err
}

// CloseWrite forwards a client half-close to the exit so the destination sees
// EOF and can finish its reply. The frontend requires a pool conn to expose
// CloseWrite when the transport supports half-close (frontend.Options); the
// exit socket is a *net.TCPConn in production, so this is the normal path.
// It is best effort: net.Conn does not promise CloseWrite, and the core-backed
// poolConn has no such API in the pinned core, so the frontend's documented
// fallback (the connection ends when the client, the registry or an error
// closes it) applies there.
func (c *adapterConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *adapterConn) classify(err error, read bool) error {
	c.mu.Lock()
	closed, gotDown := c.closed, c.gotDown
	c.mu.Unlock()
	if closed {
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
	if c.ctx.Err() != nil {
		return ctxFailure(c.ctx, c.exit, c.dest)
	}
	if read && gotDown && errors.Is(err, io.EOF) {
		return io.EOF
	}
	return destinationUnconfirmed(c.exit, c.dest, err)
}
