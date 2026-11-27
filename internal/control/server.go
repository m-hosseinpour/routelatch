package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// Handler executes one management request. Service implements it; tests use a
// small in-process handler.
type Handler interface {
	Handle(ctx context.Context, req Request) (any, error)
}

// ServerOptions configures the Unix management socket.
type ServerOptions struct {
	Path    string
	Handler Handler
	// PeerUID is injectable for tests; production uses SO_PEERCRED.
	PeerUID func(*net.UnixConn) (uint32, error)
	// AllowStale removes an existing socket only when nobody answers it. The
	// service sets this only after it owns the state lock.
	AllowStale bool
}

// Server is the loopback Unix-socket management surface. It has no HTTP layer,
// no TCP listener and no unauthenticated bypass: every peer is checked against
// the service's own uid before a request is read.
type Server struct {
	opts ServerOptions
	ln   *net.UnixListener
	done chan struct{}

	mu     sync.Mutex
	closed bool
	active map[*net.UnixConn]context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer validates options without binding.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Path == "" {
		return nil, &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "management socket path is empty"}
	}
	if opts.Handler == nil {
		return nil, &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "management handler is required"}
	}
	if opts.PeerUID == nil {
		opts.PeerUID = peerUID
	}
	return &Server{opts: opts, done: make(chan struct{}), active: make(map[*net.UnixConn]context.CancelFunc)}, nil
}

// Start binds, chmods and verifies the socket.
func (s *Server) Start(ctx context.Context) error {
	if len(s.opts.Path) > 100 {
		return &contract.Failure{
			Kind: contract.Unsupported, Path: contract.PathLocal, Op: "control.server",
			Message: "management socket path is too long for a Unix socket; set a shorter XDG_STATE_HOME or --data-dir",
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.Path), 0o700); err != nil {
		return &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "cannot create the runtime directory", Err: err}
	}
	if err := os.Chmod(filepath.Dir(s.opts.Path), 0o700); err != nil {
		return &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "cannot set runtime directory permissions", Err: err}
	}
	if _, err := os.Lstat(s.opts.Path); err == nil {
		// A live socket is a second instance; a refused one is stale and may be
		// removed only by the lock owner.
		if conn, derr := net.DialTimeout("unix", s.opts.Path, 250*time.Millisecond); derr == nil {
			_ = conn.Close()
			return &contract.Failure{Kind: contract.StateLocked, Path: contract.PathLocal, Op: "control.server", Message: "another RouteLatch instance is serving this data dir"}
		}
		if !s.opts.AllowStale {
			return &contract.Failure{Kind: contract.StateLocked, Path: contract.PathLocal, Op: "control.server", Message: "management socket already exists and its owner is unknown"}
		}
		if err := os.Remove(s.opts.Path); err != nil {
			return &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "cannot remove the stale management socket", Err: err}
		}
	}
	addr := &net.UnixAddr{Name: s.opts.Path, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return &contract.Failure{Kind: contract.StateLocked, Path: contract.PathLocal, Op: "control.server", Message: "cannot bind the management socket", Err: err}
	}
	if err := os.Chmod(s.opts.Path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(s.opts.Path)
		return &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "cannot set management socket permissions", Err: err}
	}
	info, err := os.Stat(s.opts.Path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		_ = ln.Close()
		_ = os.Remove(s.opts.Path)
		return &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.server", Message: "management socket permissions are not 0600"}
	}
	s.ln = ln
	go s.serve()
	select {
	case <-ctx.Done():
		_ = s.Close()
		return ctx.Err()
	default:
	}
	return nil
}

// Path reports the bound socket path.
func (s *Server) Path() string { return s.opts.Path }

// Close stops accepting, cancels in-flight handlers, waits briefly for them and
// removes the socket file. It is idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	cancels := make([]context.CancelFunc, 0, len(s.active))
	for _, cancel := range s.active {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	var err error
	if ln != nil {
		err = ln.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// A handler that ignores cancellation must not hold shutdown forever;
		// its mutation is still atomic in the store.
	}
	if ln != nil {
		_ = os.Remove(s.opts.Path)
	}
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	for {
		conn, err := s.ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			cancel()
			_ = conn.Close()
			return
		}
		s.active[conn] = cancel
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.active, conn)
				s.mu.Unlock()
				cancel()
			}()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(base context.Context, conn *net.UnixConn) {
	defer conn.Close()

	uid, err := s.opts.PeerUID(conn)
	if err != nil || uid != uint32(os.Getuid()) {
		// Only the uid is relevant; never log or echo request data here.
		env := EnvelopeFor("management", nil, &contract.Failure{
			Kind: contract.PeerRejected, Path: contract.PathLocal, Op: "control.peer",
			Message: "management peer was rejected; only the same uid may connect",
		})
		_ = writeEnvelope(conn, env)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readLineLimit(bufio.NewReader(conn), contract.MaxManagementRequest)
	if err != nil {
		env := EnvelopeFor("management", nil, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "control.request",
			Message: "management request could not be read or exceeds 64 KiB",
		})
		_ = writeEnvelope(conn, env)
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		env := EnvelopeFor("management", nil, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "control.request",
			Message: "management request is not valid JSON",
		})
		_ = writeEnvelope(conn, env)
		return
	}
	command := CommandName(req.Op)
	if req.SchemaVersion != protocolVersion {
		env := EnvelopeFor(command, nil, &contract.Failure{
			Kind: contract.Unsupported, Path: contract.PathLocal, Op: "control.request",
			Message: "management request schema_version must be 1",
		})
		_ = writeEnvelope(conn, env)
		return
	}
	timeout := requestTimeout(req.Op)
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	_ = conn.SetDeadline(time.Now().Add(timeout + 2*time.Second))

	data, err := s.opts.Handler.Handle(ctx, req)
	if err != nil {
		var failure *contract.Failure
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &failure) && failure.Kind == contract.Timeout:
			err = &contract.Failure{Kind: contract.Timeout, Path: contract.PathLocal, Op: "control.handle", Message: "management request timed out"}
		case errors.Is(err, context.Canceled):
			err = &contract.Failure{Kind: contract.Canceled, Path: contract.PathLocal, Op: "control.handle", Message: "management request was canceled"}
		}
		_ = writeEnvelope(conn, EnvelopeFor(command, nil, err))
		return
	}
	env := EnvelopeFor(command, data, nil)
	raw, merr := json.Marshal(env)
	if merr != nil {
		_ = writeEnvelope(conn, EnvelopeFor(command, nil, &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.response", Message: "response could not be encoded"}))
		return
	}
	if len(raw) > contract.MaxManagementResponse {
		_ = writeEnvelope(conn, EnvelopeFor(command, nil, &contract.Failure{
			Kind: contract.Internal, Path: contract.PathLocal, Op: "control.response",
			Message: "response exceeds the 4 MiB management limit; use --limit/--offset",
		}))
		return
	}
	_, _ = conn.Write(append(raw, '\n'))
}

func writeEnvelope(w net.Conn, env contract.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

func requestTimeout(op string) time.Duration {
	switch op {
	case OpPoolTest:
		return 10 * time.Minute
	case OpDiagnose, OpProposalsRecheck:
		// A manual diagnosis runs up to two bounded probes; the documented
		// 5-second request deadline cannot cover it, so the command's own
		// ProbeTimeout bounds are used instead.
		return 60 * time.Second
	case OpPoolRefresh:
		return 90 * time.Second
	case OpImport, OpPoolAdd:
		// A subscription fetch is bounded by SourceTotalTimeout (30s); a file
		// import is local. The 5s default would cut a valid fetch short.
		return 60 * time.Second
	case OpDesktopSet, OpDesktopRestore, OpBackup, OpRestore:
		return 30 * time.Second
	case OpTunEnable, OpTunDisable, OpMode:
		// The TUN service allows a 30s operation, the client outlives that
		// exchange, and disable also re-syncs the sidecar (start+stop). A mode
		// change can run the same disable before committing stopped. The 5s
		// default would report a successful coverage change as a timeout
		// (docs/contracts.md §10.3).
		return 60 * time.Second
	default:
		return 5 * time.Second
	}
}
