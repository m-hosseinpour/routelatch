// EXPERIMENT ONLY - not production code.
//
// This file is a minimal, hand-written SOCKS5 CONNECT-only frontend. It exists
// to settle one RouteLatch T1 question: how much protocol/connection code must
// RouteLatch own to keep its guarantees (unresolved hostname reaches our dialer,
// no silent direct fallback, 0x07 refusal for BIND/UDP, cancellable in-flight
// dials, live tunnel teardown from another goroutine)?
//
// Guarantees kept by this experiment:
//   - method 0x00 only, no authentication (loopback listener only);
//   - CONNECT only; BIND and UDP ASSOCIATE answer 0x07, unknown ATYP answers 0x08;
//   - success reply 0x00 only after DialFunc returned successfully;
//   - the hostname is passed verbatim to DialFunc, never resolved here;
//   - early client bytes (sent after the request) are preserved through
//     io.Copy on the same connection, without a read that could consume them;
//   - every accepted connection is tracked so another goroutine can cancel a
//     pending dial or close the client side of a live tunnel.
//
// Not provided: IPv6 listener policy, UDP, BIND, auth, rules, logging, byte
// accounting, handshake-readiness proof beyond "DialFunc returned", and
// detection of a client disconnect while DialFunc is blocked (only Server.Close
// or Cancel cancel it).
package socksfront

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	socksVersion = 0x05

	methodNoAuth       = 0x00
	methodNoAcceptable = 0xff

	cmdConnect   = 0x01
	cmdBind      = 0x02
	cmdAssociate = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess              = 0x00
	repGeneralFailure       = 0x01
	repCommandNotSupported  = 0x07
	repAddrTypeNotSupported = 0x08

	// handshakeTimeout bounds the greeting + request phase only. The tunnel and
	// dial phases are bounded by the caller's context/deadline, not by this.
	handshakeTimeout = 10 * time.Second
)

// DialFunc dials a destination. host is exactly what the SOCKS5 client asked
// for: an IP literal or a hostname that the frontend has NOT resolved. A
// DialFunc must return only when the destination is ready (or proven ready by
// the caller's handshake gate); the success reply is sent after it returns.
type DialFunc func(ctx context.Context, network, host, port string) (net.Conn, error)

// SessionInfo is a snapshot of one accepted connection.
type SessionInfo struct {
	ID     uint64
	State  string // handshake, dialing, streaming
	Remote string
}

// Server is the experiment frontend. It must be bound to loopback.
type Server struct {
	ln   net.Listener
	dial DialFunc

	mu       sync.Mutex
	nextID   uint64
	sessions map[uint64]*session
	closed   bool
}

type session struct {
	id     uint64
	client net.Conn
	cancel context.CancelFunc
	remote string

	mu       sync.Mutex
	state    string
	upstream net.Conn
}

// Listen creates a frontend. dial must be non-nil. addr should be loopback.
func Listen(addr string, dial DialFunc) (*Server, error) {
	if dial == nil {
		return nil, errors.New("socksfront: nil dialer")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{ln: ln, dial: dial, sessions: make(map[uint64]*session)}, nil
}

// Addr reports the listener address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve accepts connections until Close. It returns the accept error.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

// Close stops the listener and tears down every known session (pending dial
// included). Call after Serve has returned or accept errors are expected.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sessions := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.mu.Unlock()

	err := s.ln.Close()
	for _, ss := range sessions {
		ss.cancel()
		ss.closeAll()
	}
	return err
}

// Cancel cancels the context of one session and closes its client connection.
// It is the mode-switch hook for an in-flight dial.
func (s *Server) Cancel(id uint64) bool {
	ss := s.lookup(id)
	if ss == nil {
		return false
	}
	ss.cancel()
	_ = ss.client.Close()
	return true
}

// CloseClient closes the client side of one live tunnel from another
// goroutine. The tunnel's copy loops then propagate teardown to the upstream.
func (s *Server) CloseClient(id uint64) bool {
	ss := s.lookup(id)
	if ss == nil {
		return false
	}
	return ss.client.Close() == nil
}

// Sessions returns a snapshot ordered by ID.
func (s *Server) Sessions() []SessionInfo {
	s.mu.Lock()
	out := make([]SessionInfo, 0, len(s.sessions))
	for _, ss := range s.sessions {
		ss.mu.Lock()
		out = append(out, SessionInfo{ID: ss.id, State: ss.state, Remote: ss.remote})
		ss.mu.Unlock()
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SessionCount reports the number of tracked sessions.
func (s *Server) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) lookup(id uint64) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) handle(client net.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	ss := &session{client: client, cancel: cancel, remote: client.RemoteAddr().String(), state: "handshake"}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = client.Close()
		return
	}
	s.nextID++
	ss.id = s.nextID
	s.sessions[ss.id] = ss
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, ss.id)
		s.mu.Unlock()
		cancel()
		ss.closeAll()
	}()

	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := negotiate(client); err != nil {
		return
	}
	cmd, host, port, err := readRequest(client)
	if err != nil {
		return
	}
	if cmd != cmdConnect {
		_ = writeReply(client, repCommandNotSupported, nil)
		return
	}
	_ = client.SetDeadline(time.Time{})

	ss.setState("dialing")
	upstream, err := s.dial(ctx, "tcp", host, port)
	if err != nil {
		// No reply is attempted when our own cancel/close caused the failure.
		if ctx.Err() == nil {
			_ = writeReply(client, repGeneralFailure, nil)
		}
		return
	}
	ss.setUpstream(upstream)
	if err := writeReply(client, repSuccess, upstream.LocalAddr()); err != nil {
		return
	}
	ss.setState("streaming")
	pipeTunnel(upstream, client)
}

func (ss *session) setState(state string) {
	ss.mu.Lock()
	ss.state = state
	ss.mu.Unlock()
}

func (ss *session) setUpstream(c net.Conn) {
	ss.mu.Lock()
	ss.upstream = c
	ss.mu.Unlock()
}

func (ss *session) closeAll() {
	_ = ss.client.Close()
	ss.mu.Lock()
	upstream := ss.upstream
	ss.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
}

// errReplied means a protocol reply was already written; the caller just closes.
var errReplied = errors.New("socksfront: reply already sent")

// negotiate performs the RFC 1928 method negotiation and accepts only 0x00.
func negotiate(rw net.Conn) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(rw, hdr); err != nil {
		return err
	}
	if hdr[0] != socksVersion || hdr[1] == 0 {
		return fmt.Errorf("socksfront: bad greeting version=%d methods=%d", hdr[0], hdr[1])
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(rw, methods); err != nil {
		return err
	}
	for _, m := range methods {
		if m == methodNoAuth {
			_, err := rw.Write([]byte{socksVersion, methodNoAuth})
			return err
		}
	}
	_, _ = rw.Write([]byte{socksVersion, methodNoAcceptable})
	return errors.New("socksfront: client did not offer no-auth")
}

// readRequest parses one RFC 1928 request and returns the unresolved host and
// port. Unknown address types are answered with 0x08 here (errReplied).
func readRequest(rw net.Conn) (cmd byte, host, port string, err error) {
	hdr := make([]byte, 4)
	if _, err = io.ReadFull(rw, hdr); err != nil {
		return 0, "", "", err
	}
	if hdr[0] != socksVersion {
		return 0, "", "", fmt.Errorf("socksfront: bad request version %d", hdr[0])
	}
	cmd, rsv, atyp := hdr[1], hdr[2], hdr[3]
	if rsv != 0 {
		_ = writeReply(rw, repGeneralFailure, nil)
		return 0, "", "", errReplied
	}

	var portBuf [2]byte
	switch atyp {
	case atypIPv4:
		b := make([]byte, net.IPv4len)
		if _, err = io.ReadFull(rw, b); err != nil {
			return 0, "", "", err
		}
		host = net.IP(b).String()
	case atypIPv6:
		b := make([]byte, net.IPv6len)
		if _, err = io.ReadFull(rw, b); err != nil {
			return 0, "", "", err
		}
		host = net.IP(b).String()
	case atypDomain:
		var n [1]byte
		if _, err = io.ReadFull(rw, n[:]); err != nil {
			return 0, "", "", err
		}
		if n[0] == 0 {
			_ = writeReply(rw, repGeneralFailure, nil)
			return 0, "", "", errReplied
		}
		b := make([]byte, int(n[0]))
		if _, err = io.ReadFull(rw, b); err != nil {
			return 0, "", "", err
		}
		host = string(b)
	default:
		_ = writeReply(rw, repAddrTypeNotSupported, nil)
		return 0, "", "", errReplied
	}
	if _, err = io.ReadFull(rw, portBuf[:]); err != nil {
		return 0, "", "", err
	}
	return cmd, host, strconv.Itoa(int(binary.BigEndian.Uint16(portBuf[:]))), nil
}

// writeReply encodes one RFC 1928 reply. A successful reply may carry the
// upstream's local address; failures use the all-zero IPv4 bind address.
func writeReply(w io.Writer, rep byte, local net.Addr) error {
	reply := []byte{socksVersion, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	if rep == repSuccess {
		if ta, ok := local.(*net.TCPAddr); ok && ta != nil {
			if ip4 := ta.IP.To4(); ip4 != nil {
				copy(reply[4:8], ip4)
			} else if ip16 := ta.IP.To16(); ip16 != nil {
				reply = append([]byte{socksVersion, rep, 0x00, atypIPv6}, ip16...)
				reply = append(reply, byte(ta.Port>>8), byte(ta.Port))
				_, err := w.Write(reply)
				return err
			}
			reply[8], reply[9] = byte(ta.Port>>8), byte(ta.Port)
		}
	}
	_, err := w.Write(reply)
	return err
}

func pipeTunnel(upstream, client net.Conn) {
	done := make(chan struct{}, 2)
	go copyHalf(upstream, client, done)
	go copyHalf(client, upstream, done)
	<-done
	<-done
}

// copyHalf forwards one direction and half-closes the destination so the peer
// learns about EOF. Full teardown is driven by Server.Close/Cancel/CloseClient
// or by the peers themselves; that keeps one-way streaming working.
func copyHalf(dst, src net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	done <- struct{}{}
}
