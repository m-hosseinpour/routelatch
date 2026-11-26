package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// Core is one proxy-core lifecycle. A core that ran successfully is never
// restarted inside the same process (R-4): Enable creates a new one, Disable
// stops it, and production exits so the next enable is a new process. A core
// whose start or verification failed is stopped and discarded, never counted
// as run, so a later enable may build a fresh one.
type Core interface {
	Start(context.Context) error
	Stop(context.Context) error
}

// CoreFactory builds a stopped core for one loopback SOCKS target. The real
// implementation parses the fixed document and starts the pinned Xray core.
type CoreFactory interface {
	New(socksAddress string) (Core, error)
}

// Network owns the link and route verification/cleanup around the core. The
// pinned core creates the interface and routes from the fixed config; the
// service verifies them and can remove leftovers with fixed arguments.
type Network interface {
	LinkPresent(name string) bool
	// SetDown brings the link down before the core is stopped, so no new packet
	// enters a dying stack. It is a no-op when the link is absent.
	SetDown(ctx context.Context, name string) error
	// CheckUp proves the interface exists, is up, has both gateways and all
	// half routes. A false positive here would leave traffic uncovered.
	CheckUp(name string, gateways, routes []string) error
	// Remove deletes our link if it is still present and verifies that neither
	// the link nor any route on it remains.
	Remove(ctx context.Context, name string) error
}

// Options wires one service instance. Tests inject a fake core and network;
// production uses the pinned core and the system network.
type Options struct {
	Config      ServiceConfig
	CoreFactory CoreFactory
	Network     Network
	LeasePath   string
	Now         func() time.Time
	BootID      func() string
	Logger      *log.Logger
	// ExitAfterDisable is the installed-service behavior: an explicit disable
	// stops the process so the next enable gets a fresh core (contracts §10.4).
	// Tests may keep it false to exercise in-process idempotency.
	ExitAfterDisable bool
}

// Service is the TUN helper's state machine. It owns no policy and never sees a
// destination: the only addresses in it are the fixed gateways and the engine's
// own loopback listener.
type Service struct {
	cfg       ServiceConfig
	factory   CoreFactory
	net       Network
	leasePath string
	now       func() time.Time
	bootID    func() string
	log       *log.Logger
	exitOnOff bool

	mu          sync.Mutex
	state       contract.TunState
	desired     bool
	core        Core
	coreCreated bool // a core may exist in this process; a failed Stop latches retry (R-4)
	ownsLink    bool // the link is ours and must be removed on stop
	since       time.Time
	lastErr     contract.TunReason
	detail      string

	exitCh   chan struct{}
	exitOnce sync.Once
	exitErr  error
}

// NewService validates its dependencies. It does not touch the network and does
// not adopt a lease; Serve does that before accepting requests.
func NewService(opts Options) (*Service, error) {
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}
	if opts.CoreFactory == nil || opts.Network == nil {
		return nil, fmt.Errorf("the TUN service needs a core factory and a network")
	}
	if opts.LeasePath == "" {
		opts.LeasePath = LeasePath
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.BootID == nil {
		opts.BootID = readBootID
	}
	if opts.Logger == nil {
		opts.Logger = log.New(io.Discard, "", 0)
	}
	return &Service{
		cfg:       opts.Config,
		factory:   opts.CoreFactory,
		net:       opts.Network,
		leasePath: opts.LeasePath,
		now:       opts.Now,
		bootID:    opts.BootID,
		log:       opts.Logger,
		exitOnOff: opts.ExitAfterDisable,
		state:     contract.TunOff,
		exitCh:    make(chan struct{}),
	}, nil
}

// Status returns the additive status.tun object. It contains no destination,
// domain or flow data (D21).
func (s *Service) Status(context.Context) (contract.TunStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(), nil
}

func (s *Service) statusLocked() contract.TunStatus {
	st := contract.TunStatus{
		Desired:   s.desired,
		State:     s.state,
		Interface: InterfaceName,
		DNS:       contract.TunDNSDirect,
		UDP:       contract.TunUDPBlocked,
		IPv6:      true,
		Helper:    contract.TunHelperInstalled,
		LastError: s.lastErr,
		Detail:    safeText(s.detail),
	}
	if !s.since.IsZero() {
		st.Since = s.since
	}
	return st
}

// Enable is idempotent while coverage is on. It creates a fresh core, which
// creates the TUN link, addresses and half routes; the service then verifies
// them before reporting on.
func (s *Service) Enable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enableLocked(ctx)
}

func (s *Service) enableLocked(ctx context.Context) error {
	if s.state == contract.TunOn {
		return nil
	}
	if s.coreCreated {
		// R-4 forbids restarting a core that already ran in this process. A
		// core whose start or verification failed was stopped and discarded
		// below and never counted as running, so a later enable may safely
		// create a fresh one. The installed unit also exits after disable.
		s.state = contract.TunStateError
		s.since = time.Time{}
		s.lastErr = ""
		s.detail = "the TUN service must be restarted before coverage can be enabled again"
		s.log.Printf("enable refused: the core was already started in this process")
		return &contract.TunError{Kind: contract.Internal}
	}
	if s.ownsLink {
		// A previous teardown did not finish. Creating a second core over
		// leftover link/route state is not a retry, so require a restart.
		s.state = contract.TunStateError
		s.since = time.Time{}
		s.lastErr = ""
		s.detail = "the previous TUN teardown did not finish"
		s.log.Printf("enable refused: the previous teardown did not finish")
		return &contract.TunError{Kind: contract.Internal}
	}
	if s.net.LinkPresent(InterfaceName) {
		s.state = contract.TunStateError
		s.since = time.Time{}
		s.lastErr = contract.TunReasonConflict
		s.detail = "another TUN instance owns the interface"
		s.log.Printf("enable refused: the TUN interface already exists")
		return &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict}
	}

	s.state = contract.TunStarting
	s.since = s.now()
	s.lastErr = ""
	s.detail = ""

	core, err := s.factory.New(s.cfg.SocksAddress)
	if err != nil {
		s.failLocked(err, "the TUN core could not be created")
		s.log.Printf("enable failed: core creation")
		return classifyCoreError(err)
	}
	s.core = core
	s.ownsLink = true

	if err := core.Start(ctx); err != nil {
		stopErr := core.Stop(context.Background())
		s.core = nil
		detail := "the TUN interface could not be started"
		if cleanupErr := s.net.Remove(context.Background(), InterfaceName); cleanupErr != nil {
			detail = "the TUN interface could not be removed"
		} else {
			s.ownsLink = false
		}
		if stopErr != nil {
			// The core may still hold resources; a retry could stack a second
			// one, which R-4 forbids. Require a process restart.
			s.coreCreated = true
		}
		s.failLocked(err, detail)
		s.log.Printf("enable failed: core start")
		return classifyCoreError(err)
	}

	if err := s.net.CheckUp(InterfaceName, Gateways, HalfRoutes); err != nil {
		stopErr := core.Stop(context.Background())
		s.core = nil
		detail := "TUN did not come up with the fixed gateways and routes"
		if cleanupErr := s.net.Remove(context.Background(), InterfaceName); cleanupErr != nil {
			detail = "the TUN interface could not be removed"
		} else {
			s.ownsLink = false
		}
		if stopErr != nil {
			// The started core may still be alive; a retry could stack a
			// second one, which R-4 forbids. Require a process restart.
			s.coreCreated = true
		}
		s.failLocked(err, detail)
		s.log.Printf("enable failed: link or route verification")
		return &contract.TunError{Kind: contract.Internal}
	}

	if err := s.writeLease("on"); err != nil {
		stopErr := core.Stop(context.Background())
		s.core = nil
		detail := "the restart lease could not be written"
		if cleanupErr := s.net.Remove(context.Background(), InterfaceName); cleanupErr != nil {
			detail = "the TUN interface could not be removed"
		} else {
			s.ownsLink = false
		}
		if stopErr != nil {
			s.coreCreated = true
		}
		s.failLocked(err, detail)
		s.log.Printf("enable failed: lease write")
		return &contract.TunError{Kind: contract.Internal}
	}

	// Only a fully successful enable marks the core as run. A failed attempt
	// was torn down, so a fresh enable may retry with a new core (§10.5).
	s.coreCreated = true
	s.state = contract.TunOn
	s.desired = true
	s.lastErr = ""
	s.detail = ""
	s.log.Printf("coverage is on")
	return nil
}

// Disable stops coverage and removes the link/routes. It is always allowed and
// idempotent when nothing is running. On success the lease records the explicit
// user wish so a service restart in this boot does not rebuild coverage.
func (s *Service) Disable(ctx context.Context) error {
	s.mu.Lock()
	err := s.disableLocked(ctx)
	exit := s.exitOnOff
	s.mu.Unlock()
	if exit {
		s.requestExit(err)
	}
	return err
}

func (s *Service) disableLocked(ctx context.Context) error {
	running := s.core != nil || s.state == contract.TunOn || s.ownsLink
	if !running {
		// A link we do not own is not removed: another instance may hold it.
		if s.net.LinkPresent(InterfaceName) {
			s.state = contract.TunStateError
			s.lastErr = contract.TunReasonConflict
			s.detail = "another TUN instance owns the interface"
			return &contract.TunError{Kind: contract.Internal, Reason: contract.TunReasonConflict}
		}
		s.state = contract.TunOff
		s.desired = false
		s.since = time.Time{}
		s.lastErr = ""
		s.detail = ""
		if err := s.writeLease("off"); err != nil {
			s.state = contract.TunStateError
			s.detail = "the restart lease could not be written"
			return &contract.TunError{Kind: contract.Internal}
		}
		return nil
	}

	s.state = contract.TunStopping
	var errs []error
	// Order: take the TUN down (no new packets into the dying stack), stop the
	// core, then remove any route/link the core did not remove (contracts §10.5).
	if err := s.net.SetDown(ctx, InterfaceName); err != nil {
		errs = append(errs, err)
	}
	if s.core != nil {
		if err := s.core.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
		s.core = nil
	}
	if err := s.net.Remove(ctx, InterfaceName); err != nil {
		errs = append(errs, err)
	} else {
		s.ownsLink = false
	}
	if err := s.writeLease("off"); err != nil {
		errs = append(errs, err)
	}

	s.desired = false
	s.since = time.Time{}
	if len(errs) > 0 {
		s.state = contract.TunStateError
		s.lastErr = ""
		s.detail = "TUN cleanup did not finish"
		s.log.Printf("disable finished with a cleanup error")
		return &contract.TunError{Kind: contract.Internal}
	}
	s.state = contract.TunOff
	s.lastErr = ""
	s.detail = ""
	s.log.Printf("coverage is off")
	return nil
}

// adopt rebuilds coverage from the lease after a service restart in the same
// boot. A boot-id mismatch or a non-on lease is discarded; there is no
// automatic enable in a fresh boot.
func (s *Service) adopt(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.readLease()
	if !ok {
		return
	}
	if rec.BootID == "" || rec.BootID != s.bootID() {
		// The lease belongs to an earlier boot; /run may have survived in a
		// container, so discard it instead of rebuilding stale coverage.
		_ = s.writeLease("off")
		s.log.Printf("stale restart lease discarded")
		return
	}
	if rec.Desired != "on" {
		return
	}
	s.desired = true
	s.log.Printf("restart lease requests coverage rebuild")
	if err := s.enableLocked(ctx); err != nil {
		// Stay up and report the error; the lease stays on so a later service
		// restart can retry. There is no silent coverage claim.
		s.log.Printf("restart lease rebuild failed")
	}
}

// shutdown is the signal path: stop coverage and remove the link, but keep the
// lease as it was. A systemd restart in the same boot then rebuilds coverage.
func (s *Service) shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.core == nil && !s.ownsLink && s.state != contract.TunOn {
		s.state = contract.TunOff
		return nil
	}
	s.state = contract.TunStopping
	var errs []error
	if err := s.net.SetDown(ctx, InterfaceName); err != nil {
		errs = append(errs, err)
	}
	if s.core != nil {
		if err := s.core.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
		s.core = nil
	}
	if err := s.net.Remove(ctx, InterfaceName); err != nil {
		errs = append(errs, err)
	} else {
		s.ownsLink = false
	}
	s.since = time.Time{}
	if len(errs) > 0 {
		s.state = contract.TunStateError
		s.detail = "TUN cleanup did not finish"
		return &contract.TunError{Kind: contract.Internal}
	}
	s.state = contract.TunOff
	s.detail = ""
	return nil
}

func (s *Service) failLocked(cause error, detail string) {
	s.state = contract.TunStateError
	s.since = time.Time{}
	s.detail = detail
	s.lastErr = ""
	var te *contract.TunError
	if errors.As(cause, &te) && validTunReason(te.Reason) {
		s.lastErr = te.Reason
	}
}

func validTunReason(reason contract.TunReason) bool {
	for _, known := range contract.TunReasons() {
		if reason == known {
			return true
		}
	}
	return false
}

// classifyCoreError maps a raw core failure to a stable, destination-free TUN
// error. A permission failure is the documented platform refusal, not a silent
// degradation.
func classifyCoreError(err error) error {
	var te *contract.TunError
	if errors.As(err, &te) {
		return te
	}
	if errors.Is(err, os.ErrPermission) {
		return &contract.TunError{Kind: contract.Unsupported, Reason: contract.TunReasonPlatformUnsupported}
	}
	return &contract.TunError{Kind: contract.Internal, Err: err}
}

// serviceErrorCode maps an error onto the private socket protocol. It is not a
// contract ErrorKind: the engine maps it onto the frozen management kinds.
func serviceErrorCode(err error) (code string, reason contract.TunReason) {
	var te *contract.TunError
	if !errors.As(err, &te) {
		return "internal", ""
	}
	reason = te.Reason
	if !validTunReason(reason) {
		reason = ""
	}
	switch te.Kind {
	case contract.Unsupported:
		code = "unsupported"
	case contract.StateLocked:
		code = "state_locked"
	default:
		code = "internal"
	}
	if (code == "unsupported" || code == "state_locked") && reason == "" {
		code = "internal"
	}
	return code, reason
}

// Serve adopts a lease, then serves the control socket until a signal, a
// disable (when ExitAfterDisable is set) or a listener failure. The listener is
// supplied by the caller so tests use a private directory.
func (s *Service) Serve(ctx context.Context, ln net.Listener) error {
	s.adopt(ctx)

	done := make(chan struct{})
	acceptErr := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			go s.handle(conn)
		}
	}()

	select {
	case <-ctx.Done():
		_ = ln.Close()
		<-done
		if err := s.shutdown(context.Background()); err != nil {
			return err
		}
		return ctx.Err()
	case <-s.exitCh:
		_ = ln.Close()
		<-done
		return s.exitErr
	case <-done:
		// The listener closed on its own. Prefer an explicit exit request or
		// cancellation over treating that as a successful stop.
		select {
		case <-s.exitCh:
			return s.exitErr
		default:
		}
		select {
		case <-ctx.Done():
			if err := s.shutdown(context.Background()); err != nil {
				return err
			}
			return ctx.Err()
		default:
		}
		if err := s.shutdown(context.Background()); err != nil {
			return err
		}
		select {
		case err := <-acceptErr:
			// An unexpected accept failure is a service failure, not a clean
			// stop: exit non-zero so the unit can restart it.
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("the TUN control listener failed: %w", err)
			}
		default:
		}
		return nil
	}
}

func (s *Service) requestExit(err error) {
	s.exitOnce.Do(func() {
		s.exitErr = err
		close(s.exitCh)
	})
}
