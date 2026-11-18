package frontend

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"routelatch/internal/contract"
)

// noLookup preserves a hostname end to end. go-socks5 resolves RawDestAddr
// before calling the dialer unless a resolver returns no IP, and AddrSpec.String
// then falls back to the untouched FQDN (spike/frontend FINDINGS S1/S2). This
// resolver intentionally performs no I/O.
type noLookup struct{}

func (noLookup) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

func (f *Frontend) acceptSOCKS(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			// A resource-exhaustion error must not silently kill the listener;
			// the HTTP side gets the same behaviour from net/http.Server.
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return
		}
		if !f.trackConn(c) {
			_ = c.Close()
			continue
		}
		if !f.beginHandler() {
			f.untrackConn(c)
			_ = c.Close()
			continue
		}
		go func() {
			defer f.endHandler()
			defer f.untrackConn(c)
			f.serveSOCKS(c)
		}()
	}
}

// serveSOCKS gives every connection its own library server so the dial
// callback can carry that connection's state (the library returns no handle).
func (f *Frontend) serveSOCKS(raw net.Conn) {
	st := &socksConn{f: f}
	st.client = &socksClientConn{Conn: raw, st: st}
	defer st.close()

	srv := socks5.NewServer(
		socks5.WithResolver(noLookup{}),
		socks5.WithDialAndRequest(st.dial),
		socks5.WithBindHandle(refuseSOCKSAuxiliary),
		socks5.WithAssociateHandle(refuseSOCKSAuxiliary),
	)
	_ = srv.ServeConn(st.client)
}

// refuseSOCKSAuxiliary answers BIND and UDP ASSOCIATE with 0x07 without
// touching a socket, resolving a name or dialing (docs/contracts.md §3.2,
// A12). The library's default UDP handler would open a real relay (FINDINGS S4).
func refuseSOCKSAuxiliary(_ context.Context, w io.Writer, _ *socks5.Request) error {
	return socks5.SendReply(w, statute.RepCommandNotSupported, nil)
}

type socksConn struct {
	f      *Frontend
	client net.Conn

	mu       sync.Mutex
	handle   contract.ConnHandle
	upstream net.Conn
	watching atomic.Bool
}

// dial is WithDialAndRequest. It refuses or dials exactly the decision's route,
// returns a usable conn only after the proof gate passed, and never falls back
// from a failed pool dial to a direct one.
func (s *socksConn) dial(ctx context.Context, _ string, _ string, req *socks5.Request) (net.Conn, error) {
	raw := req.RawDestAddr
	if raw == nil {
		return nil, socksDialErrorText(newFailure(contract.InvalidDestination,
			contract.PathLocal, "socks.destination", "invalid destination"))
	}

	// With the no-lookup resolver, RawDestAddr.String() is the untouched
	// "host:port" (FQDN fallback) or the literal IP form (FINDINGS S2); the
	// single normalizer turns it into a Destination without resolving it.
	dst, ferr := s.f.destination(raw.String())
	if ferr != nil {
		return nil, socksDialErrorText(ferr)
	}

	dec, ferr := s.f.decide(s.f.baseCtx, dst)
	if ferr != nil {
		return nil, s.fail(ferr)
	}
	h, ferr := s.f.register(contract.ConnSOCKS5Connect, dst, dec, s.client)
	if ferr != nil {
		return nil, s.fail(ferr)
	}
	s.mu.Lock()
	s.handle = h
	s.mu.Unlock()

	// The library is blocked in this callback while the dial runs, so only
	// this watch can notice a vanished client and cancel the pending dial.
	// Bytes it consumes are replayed through request.Reader before the copy.
	var watch *clientWatch
	if br, ok := req.Reader.(*bufio.Reader); ok {
		s.watching.Store(true)
		watch = startClientWatch(s.client, br, func() { _ = h.Close() })
	}
	upstream, exit, proof, _, ferr := s.f.dial(h.Context(), dec, dst, nil)
	captured, clientGone := []byte(nil), false
	if watch != nil {
		captured, clientGone = watch.stop()
		s.watching.Store(false)
	}
	if ferr != nil {
		return nil, s.fail(ferr)
	}
	if clientGone {
		_ = upstream.Close()
		return nil, s.fail(newFailure(contract.Canceled, contract.PathLocal,
			"socks.client", "client went away"))
	}
	if err := h.SetUpstream(upstream); err != nil {
		_ = upstream.Close()
		return nil, s.fail(failureOf(err))
	}
	if exit != "" {
		h.SetExit(exit)
	}
	if proof != contract.ProofUnknown {
		h.SetProof(proof)
	}
	if len(captured) > 0 {
		req.Reader = bufio.NewReader(io.MultiReader(bytes.NewReader(captured), req.Reader))
	}
	s.mu.Lock()
	s.upstream = upstream
	s.mu.Unlock()
	return ensureLocalAddr(upstream), nil
}

// fail maps a failure to the library's reply vocabulary. Canceled suppresses
// the reply entirely by closing the client first (docs/contracts.md §3.2: no
// reply after cancellation); the library's own write then fails harmlessly.
func (s *socksConn) fail(f *contract.Failure) error {
	if f.Kind == contract.Canceled || f.Kind == contract.Stopped {
		_ = s.client.Close()
	}
	return socksDialErrorText(f)
}

func (s *socksConn) abort() {
	s.mu.Lock()
	upstream := s.upstream
	s.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
	_ = s.client.Close()
}

func (s *socksConn) close() {
	s.mu.Lock()
	h, upstream := s.handle, s.upstream
	s.mu.Unlock()
	if h != nil {
		s.f.untrackHandle(h)
		_ = h.Close()
	}
	if upstream != nil {
		_ = upstream.Close()
	}
	_ = s.client.Close()
}

// socksClientConn watches for an abortive client read error (RST, not FIN) so
// the upstream connection cannot outlive a vanished client. A clean EOF is
// left to the library, which half-closes the upstream write side.
type socksClientConn struct {
	net.Conn
	st *socksConn
}

func (c *socksClientConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	// Only the watch's own pre-reply poll may time out without aborting; a
	// deadline outside that window would be a defect, not a dead client.
	if err != nil && !errors.Is(err, io.EOF) && c.st != nil {
		if isTimeoutError(err) && c.st.watching.Load() {
			return n, err
		}
		c.st.abort()
	}
	return n, err
}

// CloseWrite/CloseRead keep TCP half-close visible through the wrapper. The
// library asserts its copy destinations for these methods, and embedding the
// net.Conn interface promotes neither.
func (c *socksClientConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *socksClientConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// ensureLocalAddr keeps the library's success reply independent of an upstream
// connection whose LocalAddr is missing or not a TCP address. SendReply turns
// such an address into 0x08 even after a successful dial, which would falsely
// refuse a working tunnel.
func ensureLocalAddr(c net.Conn) net.Conn {
	if addr := c.LocalAddr(); addr != nil {
		if _, ok := addr.(*net.TCPAddr); ok {
			return c
		}
	}
	return &localAddrConn{Conn: c}
}

type localAddrConn struct {
	net.Conn
}

func (l *localAddrConn) LocalAddr() net.Addr { return &net.TCPAddr{} }

func (l *localAddrConn) CloseWrite() error {
	if cw, ok := l.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (l *localAddrConn) CloseRead() error {
	if cr, ok := l.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}
