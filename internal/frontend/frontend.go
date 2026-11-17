// Package frontend implements RouteLatch's two local traffic listeners: an
// HTTP/1.1 proxy (CONNECT tunnels and absolute-form forwarding) and a SOCKS5
// proxy built on github.com/things-go/go-socks5 v0.1.4.
//
// Boundaries that this package upholds (docs/contracts.md §2, §3, §4):
//
//   - It binds only loopback addresses and offers no authentication; the
//     management surface is a separate component with its own protection.
//   - It never resolves a destination hostname. The client's authority reaches
//     routing.NormalizeDestination and then the Router, PoolDialer and Dialer
//     as an unresolved hostname; only the direct Dialer may use the system
//     resolver (D15).
//   - The HTTP 200 and SOCKS5 0x00 replies are written only after a pool dial
//     returned ProofConfirmed or ProofAccepted, or after a direct dial
//     succeeded (docs/contracts.md §2.2). A pool failure is a mapped pre-reply
//     error and is never retried or turned into a direct dial (D11).
//   - Before any reply, a failure is mapped to the HTTP status or SOCKS5 code
//     in §3.2 and the connection is closed. After a reply, the only possible
//     outcome is teardown: no error page is ever written into a tunnel.
//   - Every admitted connection is registered with the Decision that admitted
//     it, so a mode/rule change can close exactly the conflicting ones (D13).
//     Plain HTTP is registered per request because one keep-alive socket may
//     carry requests to different destinations; the client view is detached
//     when a request completes so the socket can carry the next one.
//   - A client reset while a dial is pending cancels that dial within the
//     pending-dial bound. A clean FIN is a half-close (TCP cannot distinguish
//     it from a full close), so it is not treated as "client went away"; the
//     tunnel forwards it after the reply, and a cleanly gone client is bounded
//     by the dial deadline. Bytes pipelined during the watch are replayed,
//     never lost, including past the capture cap.
//   - There is no TLS, certificate or interception code in this package. A
//     CONNECT target's certificate is the origin's certificate because the
//     bytes are forwarded, not terminated.
//
// Half-close requirement for the integration step: every connection returned
// by PoolDialer.DialPool or Dialer.DialDirect must implement CloseWrite (being
// or wrapping *net.TCPConn is the normal way). The frontend calls it to signal
// EOF to the destination after the client half-closes. net.Conn does not
// promise CloseWrite, so this package cannot enforce the property; wiring the
// real pool and direct adapters must check it for protocols that half-close.
// If it is missing, the tunnel still forwards the other direction, but the
// destination never sees EOF and the connection ends only when the client, the
// registry, or an error closes it (it cannot be ended by a half-close).
package frontend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/routing"
)

// Options are the frozen-contract implementations the frontend drives. All
// four are required: a missing consumer is a wiring defect, not a mode.
//
// Pool and Direct must return conns that implement CloseWrite for half-close
// propagation; the package comment states the requirement and its limitation.
type Options struct {
	Router   contract.Router
	Pool     contract.PoolDialer
	Direct   contract.Dialer
	Registry contract.Registry

	// Normalize overrides the destination normalizer. It is nil in production:
	// the frontend then calls routing.NormalizeDestination, the single
	// producer of normalized destinations (docs/contracts.md §1.2). Tests use
	// it to observe the untouched client authority.
	Normalize NormalizeFunc
	// NewID generates ConnIDs. The default is a per-process counter.
	NewID func(contract.ConnKind) contract.ConnID
	// Now supplies StartedAt. The default is time.Now.
	Now func() time.Time
}

// Frontend owns both listeners and every connection they accept.
type Frontend struct {
	opts   Options
	nextID atomic.Uint64

	httpAddr  net.Addr
	socksAddr net.Addr

	mu      sync.Mutex
	started bool
	stopped bool
	conns   map[net.Conn]struct{}
	handles map[contract.ConnHandle]struct{}

	httpSrv *http.Server
	httpLn  net.Listener
	socksLn net.Listener

	stopOnce sync.Once
	stopDone chan struct{}
	baseCtx  context.Context
	baseStop context.CancelFunc
	wg       sync.WaitGroup
}

// New validates the wiring and returns a stopped frontend.
func New(opts Options) (*Frontend, error) {
	switch {
	case opts.Router == nil:
		return nil, errors.New("frontend: Router is required")
	case opts.Pool == nil:
		return nil, errors.New("frontend: PoolDialer is required")
	case opts.Direct == nil:
		return nil, errors.New("frontend: Dialer is required")
	case opts.Registry == nil:
		return nil, errors.New("frontend: Registry is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Frontend{
		opts:     opts,
		conns:    make(map[net.Conn]struct{}),
		handles:  make(map[contract.ConnHandle]struct{}),
		stopDone: make(chan struct{}),
		baseCtx:  ctx,
		baseStop: cancel,
	}
	if opts.NewID == nil {
		f.opts.NewID = func(kind contract.ConnKind) contract.ConnID {
			return contract.ConnID(fmt.Sprintf("c-%d-%s", f.nextID.Add(1), kind))
		}
	}
	return f, nil
}

// Start binds both listeners. Both addresses must name a loopback IP
// (127/8 or ::1); a wildcard or hostname is rejected rather than silently
// exposed (docs/contracts.md §4.2, A09). The caller owns port-collision
// handling.
func (f *Frontend) Start(httpAddr, socksAddr string) error {
	// Hold the mutex across both binds and the startup transition so a second
	// Start or a concurrent Stop cannot overwrite a live listener.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started || f.stopped {
		return errors.New("frontend: already started")
	}

	hl, err := listenLoopback(httpAddr)
	if err != nil {
		return err
	}
	sl, err := listenLoopback(socksAddr)
	if err != nil {
		hl.Close()
		return err
	}

	h := newHTTPProxy(f)
	f.httpLn, f.socksLn = hl, sl
	f.httpSrv = h.server()
	f.httpAddr = hl.Addr()
	f.socksAddr = sl.Addr()
	f.started = true
	f.wg.Add(2)

	go func() {
		defer f.wg.Done()
		_ = f.httpSrv.Serve(&recordingListener{Listener: hl})
	}()
	go func() {
		defer f.wg.Done()
		f.acceptSOCKS(sl)
	}()
	return nil
}

// HTTPAddr returns the bound HTTP address after Start.
func (f *Frontend) HTTPAddr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.httpAddr
}

// SOCKSAddr returns the bound SOCKS5 address after Start.
func (f *Frontend) SOCKSAddr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.socksAddr
}

// Stop closes both listeners, every registered handle (which closes the client
// and upstream sockets) and every accepted connection, then waits for its
// goroutines. It is safe to call more than once; ctx bounds only the wait.
func (f *Frontend) Stop(ctx context.Context) error {
	f.stopOnce.Do(func() {
		f.mu.Lock()
		f.stopped = true
		srv, hl, sl := f.httpSrv, f.httpLn, f.socksLn
		f.mu.Unlock()

		f.baseStop()
		if srv != nil {
			_ = srv.Close()
		}
		if hl != nil {
			_ = hl.Close()
		}
		if sl != nil {
			_ = sl.Close()
		}
		f.closeTracked()
		go func() {
			f.wg.Wait()
			close(f.stopDone)
		}()
	})
	select {
	case <-f.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closeTracked snapshots the handle and connection sets, then closes them
// outside the lock. Close is idempotent, so a concurrent handler cleanup is
// harmless.
func (f *Frontend) closeTracked() {
	f.mu.Lock()
	handles := make([]contract.ConnHandle, 0, len(f.handles))
	for h := range f.handles {
		handles = append(handles, h)
	}
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()

	for _, h := range handles {
		_ = h.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// beginHandler registers a handler goroutine unless Stop has begun. The check
// and Add happen under the same mutex as Stop's flag so Wait can never race an
// Add from zero.
func (f *Frontend) beginHandler() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	f.wg.Add(1)
	return true
}

func (f *Frontend) endHandler() { f.wg.Done() }

func (f *Frontend) trackConn(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	f.conns[c] = struct{}{}
	return true
}

func (f *Frontend) untrackConn(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

func (f *Frontend) trackHandle(h contract.ConnHandle) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	f.handles[h] = struct{}{}
	return true
}

func (f *Frontend) untrackHandle(h contract.ConnHandle) {
	f.mu.Lock()
	delete(f.handles, h)
	f.mu.Unlock()
}

// listenLoopback binds a TCP listener only when the requested host is a
// loopback IP literal. The bound address is checked again so a resolver or a
// platform quirk cannot widen the bind.
func listenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("frontend: invalid listener address: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("frontend: listener %q is not a loopback IP", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("frontend: bound address is not loopback")
	}
	return ln, nil
}

// destination converts a client authority through routing.NormalizeDestination
// (or the injected test seam). The returned failure is always pre-network and
// never echoes the raw input, so a malformed share link cannot leak.
func (f *Frontend) destination(authority string) (contract.Destination, *contract.Failure) {
	normalize := f.opts.Normalize
	if normalize == nil {
		normalize = routing.NormalizeDestination
	}
	dst, err := normalize(authority)
	if err != nil {
		var failure *contract.Failure
		if errors.As(err, &failure) &&
			(failure.Kind == contract.InvalidDestination || failure.Kind == contract.Unsupported) {
			return contract.Destination{}, failure
		}
		return contract.Destination{}, newFailure(
			contract.InvalidDestination, contract.PathLocal,
			"frontend.destination", "invalid destination")
	}
	// A destination without a port is a rule operand, not a dial target; a
	// CONNECT/SOCKS/USE request always names a port (docs/contracts.md §1.2 N6).
	if dst.Port == 0 {
		return contract.Destination{}, newFailure(
			contract.InvalidDestination, contract.PathLocal,
			"frontend.destination", "destination has no port")
	}
	return dst, nil
}

// decide asks the router and converts any error to a typed failure.
func (f *Frontend) decide(ctx context.Context, dst contract.Destination) (contract.Decision, *contract.Failure) {
	dec, err := f.opts.Router.Decide(ctx, dst)
	if err != nil {
		return contract.Decision{}, failureOf(err)
	}
	return dec, nil
}

// dial executes exactly the route the Decision names. It checks the proof gate
// before returning a usable connection: a pool success without a proof class
// is a defect and must not reach a 2xx/0x00 reply (docs/contracts.md §2.1).
// There is no fallback path from pool to direct.
func (f *Frontend) dial(ctx context.Context, dec contract.Decision, dst contract.Destination) (net.Conn, contract.ExitID, contract.ProofClass, *contract.Failure) {
	switch dec.Route {
	case contract.RoutePool:
		pctx, cancel := context.WithTimeout(ctx, contract.PoolDialDeadline)
		defer cancel()
		res, err := f.opts.Pool.DialPool(pctx, contract.PoolDialRequest{
			Destination:   dst,
			RuleID:        dec.RuleID,
			PolicyVersion: dec.PolicyVersion,
		})
		if err != nil {
			return nil, "", "", dialFailure(err, pctx, contract.PathPool)
		}
		if res.Conn == nil || (res.Proof != contract.ProofConfirmed && res.Proof != contract.ProofAccepted) {
			if res.Conn != nil {
				_ = res.Conn.Close()
			}
			return nil, "", "", newFailure(contract.Internal, contract.PathPool,
				"frontend.pool", "pool dial returned no proof")
		}
		return res.Conn, res.Exit, res.Proof, nil
	case contract.RouteDirect:
		dctx, cancel := context.WithTimeout(ctx, contract.DirectDialDeadline)
		defer cancel()
		conn, err := f.opts.Direct.DialDirect(dctx, dst)
		if err != nil {
			return nil, "", "", dialFailure(err, dctx, contract.PathDirect)
		}
		if conn == nil {
			return nil, "", "", newFailure(contract.Internal, contract.PathDirect,
				"frontend.direct", "direct dial returned no connection")
		}
		return conn, "", contract.ProofUnknown, nil
	case contract.RouteRefuse:
		kind := contract.PolicyDenied
		if dec.Reason == contract.ReasonModeStopped {
			kind = contract.Stopped
		}
		return nil, "", "", newFailure(kind, contract.PathLocal,
			"frontend.refuse", httpReasonText(kind))
	default:
		return nil, "", "", newFailure(contract.Internal, contract.PathLocal,
			"frontend.route", "unknown route")
	}
}

// register admits one connection under the Decision's policy version. A stale
// version is rejected by the registry with Canceled (contract.Registry).
func (f *Frontend) register(kind contract.ConnKind, dst contract.Destination, dec contract.Decision, client net.Conn) (contract.ConnHandle, *contract.Failure) {
	h, err := f.opts.Registry.Register(contract.ConnSpec{
		ID:          f.opts.NewID(kind),
		Kind:        kind,
		Destination: dst,
		Decision:    dec,
		StartedAt:   f.opts.Now(),
	})
	if err != nil {
		return nil, failureOf(err)
	}
	if h == nil {
		return nil, newFailure(contract.Internal, contract.PathLocal,
			"frontend.register", "registry returned no handle")
	}
	if !f.trackHandle(h) {
		_ = h.Close()
		return nil, newFailure(contract.Canceled, contract.PathLocal,
			"frontend.register", "frontend is stopping")
	}
	if client != nil {
		if err := h.SetClient(client); err != nil {
			f.untrackHandle(h)
			_ = h.Close()
			return nil, failureOf(err)
		}
	}
	return h, nil
}
