// Package control owns the management surface and the service orchestration
// that sits between the persisted state, the routing policy, the connection
// registry and the network packages (docs/contracts.md §2.6, §4.6, §7).
//
// The service is the only writer: every CLI and menu mutation reaches it over
// the protected Unix socket. It applies a committed policy snapshot, sweeps
// conflicting live and pending connections, and only then reports success.
package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"routelatch/internal/conns"
	"routelatch/internal/contract"
	"routelatch/internal/diagnostics"
	"routelatch/internal/engine"
	"routelatch/internal/frontend"
	"routelatch/internal/imports"
	"routelatch/internal/platform"
	"routelatch/internal/pool"
	"routelatch/internal/probes"
	"routelatch/internal/proposals"
	"routelatch/internal/routing"
	"routelatch/internal/store"
)

// Options wires the service. Store is the single writer; Direct is an optional
// test seam (production builds the loopback-aware direct dialer).
type Options struct {
	Store  *store.Store
	Paths  store.Paths
	Now    func() time.Time
	Direct contract.Dialer
	// LogOutput is the foreground log stream. Production passes stdout; tests
	// pass io.Discard. The file log is always at Paths.LogDir/routelatch.log.
	LogOutput io.Writer
}

// Service implements Handler and owns the in-process runtime.
type Service struct {
	store *store.Store
	paths store.Paths
	now   func() time.Time

	reg       *conns.Registry
	props     *proposals.Registry
	refresher *imports.Refresher

	engine   *engine.Engine
	fe       *frontend.Frontend
	prober   *probes.Engine
	diag     *diagnostics.Manager
	pool     contract.PoolDialer
	reranker *pool.Dialer
	direct   contract.Dialer

	snap   atomic.Pointer[contract.Snapshot]
	policy atomic.Pointer[routing.Policy]

	opMu    sync.Mutex // serializes command-level mutations and their sweeps
	propsMu sync.Mutex // serializes proposal decisions and the rule they confirm
	// pubMu orders every "store result -> s.snap" publication. Without one
	// publisher, a health or proposal write could publish an older snapshot
	// after a mode commit and make the in-memory mode regress (§4.4).
	pubMu sync.Mutex

	// fenceMu orders policy publication against a pool dial's admission and
	// completion checks. It is held only for those short checks, never across
	// the dial, so a mode/rule change can commit and sweep at once: the sweep
	// cancels a pending dial through the handle context instead of waiting for
	// its deadline (docs/contracts.md §2.6, §3.4). A dial that still finishes
	// against a superseded version is rejected and closed by fencedPool.
	fenceMu sync.RWMutex

	// usageMu guards the per-exit coalescing map for real-usage health
	// evidence; it is separate from pubMu so the data path never waits for a
	// store write to decide whether to record.
	usageMu sync.Mutex
	usageAt map[contract.ExitID]time.Time

	logger  *serviceLogger
	logPath string
	logErr  error

	httpListen    string
	socksListen   string
	httpAddr      string
	socksAddr     string
	listenersOpen bool
	stopped       bool
	// stoppedByThisProcess distinguishes "I closed the engine here" (only a
	// new process may leave stopped, §2.7) from a process that started in the
	// persisted stopped state and has not yet opened the engine at all.
	stoppedByThisProcess bool

	probeStop chan struct{}
	probeOnce sync.Once
}

// NewService loads the snapshot, builds the initial policy and creates an
// empty in-memory proposal registry. It does not start the engine or listeners;
// StartNetwork does that. Proposals are deliberately not restored from the
// snapshot: a destination is never persisted, so a restart starts with no
// proposal history (D21).
func NewService(ctx context.Context, opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.new", Message: "service has no store"}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	snap, err := opts.Store.Load(ctx)
	if err != nil {
		return nil, withResidueNote(err, opts.Store.Residue())
	}
	if !validMode(snap.State.Mode) {
		return nil, &contract.Failure{Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "control.new", Message: "state has an unknown routing mode"}
	}
	policy, err := routing.NewPolicy(snap.State.PolicyVersion, routing.Config{
		Mode: snap.State.Mode, Rules: snap.State.Rules, SelfPorts: selfPorts(snap.Config),
	})
	if err != nil {
		return nil, err
	}
	props := proposals.New(opts.Now)
	reg := conns.New()
	reg.SetPolicyVersion(snap.State.PolicyVersion)
	refresher := imports.NewRefresher(opts.Store, nil)
	refresher.BuildCheck = buildCheck
	s := &Service{
		store: opts.Store, paths: opts.Paths, now: opts.Now,
		reg: reg, props: props,
		refresher:  refresher,
		direct:     opts.Direct,
		httpListen: snap.Config.HTTPListen, socksListen: snap.Config.SOCKSListen,
		stopped: snap.State.Mode == contract.ModeStopped,
	}
	s.snap.Store(&snap)
	s.policy.Store(policy)
	s.httpAddr = snap.Config.HTTPListen
	s.socksAddr = snap.Config.SOCKSListen
	paths := opts.Paths
	if paths.LogDir == "" {
		paths = opts.Store.Paths()
	}
	s.logPath = filepath.Join(paths.LogDir, "routelatch.log")
	out := opts.LogOutput
	if out == nil {
		out = os.Stdout
	}
	logger, logErr := newServiceLogger(s.logPath, snap.Config.LogLevel, out)
	s.logger = logger
	s.logErr = logErr
	if logErr != nil {
		s.logger.Logf(levelWarning, "log.open", "operational log is unavailable at %s: %s; the service continues", s.logPath, logErr.Error())
	}
	s.logger.Logf(levelInfo, "service.init", "mode=%s policy=%d", snap.State.Mode, snap.State.PolicyVersion)
	if residue := s.store.Residue(); residue.Rewritten > 0 || len(residue.Warnings) > 0 {
		// Only counts reach the log; doctor carries the destination-free detail.
		if residue.Rewritten > 0 {
			s.logger.Logf(levelInfo, "residue.scrub", "legacy proposal history removed from %d file(s)", residue.Rewritten)
		}
		if len(residue.Warnings) > 0 {
			s.logger.Logf(levelWarning, "residue.warning", "%d legacy file(s) were left untouched; run 'doctor' for details", len(residue.Warnings))
		}
	}
	return s, nil
}

// withResidueNote appends the destination-free residue warnings Open recorded
// to a load failure, so doctor can still name an unreadable legacy file whose
// corrupt live state prevents the normal checks from running.
func withResidueNote(err error, residue store.Residue) error {
	note := residue.Note()
	if note == "" {
		return err
	}
	var failure *contract.Failure
	if errors.As(err, &failure) {
		copy := *failure
		copy.Message = failure.Message + "; legacy proposal residue: " + note
		return &copy
	}
	return fmt.Errorf("%w (legacy proposal residue: %s)", err, note)
}

// stoppedFailure rejects operations that would touch the network while the
// service has no engine or listener (docs/contracts.md §6.3). It reads the flag
// under opMu so a concurrent stop is observed.
func (s *Service) stoppedFailure(op string) error {
	s.opMu.Lock()
	stopped := s.stopped
	s.opMu.Unlock()
	if !stopped {
		return nil
	}
	return &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: op, Message: "service is stopped; no network operation is available until a restart"}
}

// Store exposes the writer for the run command's offline lifecycle.
func (s *Service) Store() *store.Store { return s.store }

// Snapshot returns the current consistent view.
func (s *Service) Snapshot() contract.Snapshot {
	if p := s.snap.Load(); p != nil {
		return *p
	}
	return contract.Snapshot{}
}

// Registry exposes the connection registry (tests and status).
func (s *Service) Registry() *conns.Registry { return s.reg }

// StartNetwork starts the engine, both loopback listeners and the probe and
// diagnostics workers. It is skipped in stopped mode.
func (s *Service) StartNetwork(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startNetworkLocked(ctx)
}

func (s *Service) startNetworkLocked(ctx context.Context) error {
	return s.startNetwork(ctx, false)
}

// startNetworkLockedHeld is for callers that already hold pubMu (restore).
func (s *Service) startNetworkLockedHeld(ctx context.Context) error {
	return s.startNetwork(ctx, true)
}

func (s *Service) startNetwork(ctx context.Context, pubHeld bool) error {
	if s.stopped {
		return nil
	}
	snap := s.Snapshot()
	if err := (platform.Targets{HTTPListen: snap.Config.HTTPListen, SOCKSListen: snap.Config.SOCKSListen}).Validate(); err != nil {
		return err
	}
	records := dialableRecords(snap.State)
	eng, err := engine.Start(engine.Options{
		Exits: records, PolicyVersion: snap.State.PolicyVersion, LogLevel: snap.Config.LogLevel,
	})
	if err != nil {
		return err
	}
	s.engine = eng
	if excluded := eng.Excluded(); len(excluded) > 0 {
		for _, ex := range excluded {
			s.logger.Logf(levelWarning, "engine.excluded", "exit=%s kind=%s detail=%s", ex.ID, ex.Kind, ex.Detail)
		}
		// Make pool list/status agree with what the engine actually built: an
		// excluded record is unsupported, not a usable unknown exit.
		var markErr error
		if pubHeld {
			markErr = s.markEngineExcludedLocked(ctx, excluded)
		} else {
			s.pubMu.Lock()
			markErr = s.markEngineExcludedLocked(ctx, excluded)
			s.pubMu.Unlock()
		}
		if markErr != nil {
			s.logger.Logf(levelWarning, "engine.excluded.persist", "could not record the exclusion: %s", markErr.Error())
		} else {
			snap = s.Snapshot()
		}
	}
	innerPool := pool.NewDialer(
		func() (contract.Snapshot, bool) { v := s.Snapshot(); return v, true },
		func(ctx context.Context, exit contract.ExitID, dst contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			res, err := eng.Dial(ctx, engine.DialRequest{
				Exit: exit, Destination: dst, PolicyVersion: s.policy.Load().PolicyVersion(),
			})
			if err != nil {
				return nil, contract.ProofUnknown, "", err
			}
			return res.Conn, res.Proof, res.Tag, nil
		},
		s.now,
	)
	s.reranker = innerPool
	s.pool = &fencedPool{svc: s, inner: innerPool}

	direct := s.direct
	if direct == nil {
		direct = newDirectDialer(selfPorts(snap.Config))
	}
	s.direct = &diagnosticDialer{inner: direct, svc: s}

	// Only the frontend sees real user traffic; the prober keeps the unwrapped
	// dialer because ApplyProbe already records its positive evidence.
	fe, err := frontend.New(frontend.Options{
		Router: routerView{s: s}, Pool: &usagePool{svc: s, inner: s.pool}, Direct: s.direct, Registry: s.reg,
	})
	if err != nil {
		_ = eng.Stop(ctx)
		return err
	}
	if err := fe.Start(snap.Config.HTTPListen, snap.Config.SOCKSListen); err != nil {
		_ = eng.Stop(ctx)
		// The addresses stay in the operational log: EnvelopeFor truncates
		// dynamic text to contract.MaxNodeNameRunes, and a long loopback IPv6
		// literal would push the actionable tail out of the message.
		s.logger.Logf(levelError, "listeners.bind_failed", "http=%s socks=%s",
			snap.Config.HTTPListen, snap.Config.SOCKSListen)
		return &contract.Failure{
			Kind: contract.StateLocked, Path: contract.PathLocal, Op: "control.listen",
			Message: "a traffic listener could not bind; no traffic was redirected and no fallback port exists",
			Err:     err,
		}
	}
	s.fe = fe
	s.httpAddr = fe.HTTPAddr().String()
	s.socksAddr = fe.SOCKSAddr().String()
	s.listenersOpen = true
	s.logger.Logf(levelInfo, "listeners.start", "http=%s socks=%s mode=%s", s.httpAddr, s.socksAddr, snap.State.Mode)

	prober := probes.NewEngine(probes.Options{
		Pool: s.pool,
		Record: func(id contract.ExitID) (contract.Record, bool) {
			v := s.Snapshot()
			for _, rec := range v.State.Records {
				if contract.ExitID(rec.ID) == id {
					return rec, true
				}
			}
			return contract.Record{}, false
		},
		AllRecords:         func() []contract.Record { return s.Snapshot().State.Records },
		Mode:               func() contract.Mode { return s.Snapshot().State.Mode },
		DiagnosticsEnabled: func() bool { return s.Snapshot().Config.Diagnostics.Enabled },
		PolicyVersion:      func() contract.PolicyVersion { return s.policy.Load().PolicyVersion() },
		Clock:              s.now,
		Targets:            append([]string(nil), snap.Config.Probes.Targets...),
		Timeout:            time.Duration(snap.Config.Probes.TimeoutMS) * time.Millisecond,
		FirstByte:          contract.ProbeFirstByte,
		QueueLimit:         snap.Config.Diagnostics.QueueLimit,
		HealthConcurrent:   2, DiagnosticConcurrent: 1,
	})
	s.prober = prober
	s.diag = diagnostics.New(diagnostics.Options{
		Prober: prober, Router: routerView{s: s}, Store: s.props,
		Config:        func() contract.DiagnosticsConfig { return s.Snapshot().Config.Diagnostics },
		ProbeConfig:   func() contract.ProbeConfig { return s.Snapshot().Config.Probes },
		Mode:          func() contract.Mode { return s.Snapshot().State.Mode },
		PolicyVersion: func() contract.PolicyVersion { return s.policy.Load().PolicyVersion() },
		Clock:         s.now,
	})
	s.startProbeLoop()
	return nil
}

// Close stops the network stack in the documented order: listeners and
// registry first (so copy loops see Canceled), then probe work, then the engine.
func (s *Service) Close(ctx context.Context) error {
	s.logger.Logf(levelInfo, "service.close", "shutting down")
	defer s.logger.Close()
	s.stopProbeLoop()
	if s.diag != nil {
		s.diag.Close(ctx)
	}
	if s.prober != nil {
		s.prober.Close()
	}
	if s.fe != nil {
		_ = s.fe.Stop(ctx)
	}
	_, _ = s.reg.CloseAll(ctx, contract.CloseShutdown)
	if s.engine != nil {
		return s.engine.Stop(ctx)
	}
	return nil
}

// StartProbeLoop starts automatic rotation; safe to call once.
func (s *Service) startProbeLoop() {
	if s.probeStop != nil || s.prober == nil {
		return
	}
	s.probeStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.probeStop:
				return
			case <-ticker.C:
				reqs := s.prober.Rotate(s.now())
				for _, req := range reqs {
					ctx, cancel := context.WithTimeout(context.Background(), contract.ProbeTimeout)
					m, err := s.prober.RunMeasurement(ctx, req)
					cancel()
					if err == nil {
						s.applyMeasurement(m)
					}
				}
			}
		}
	}()
}

func (s *Service) stopProbeLoop() {
	s.probeOnce.Do(func() {
		if s.probeStop != nil {
			close(s.probeStop)
		}
	})
}

func (s *Service) applyMeasurement(m probes.Measurement) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := s.store.Mutate(ctx, func(st *contract.State) error {
		pool.ApplyProbe(st, m.Result, s.now().UTC())
		pool.SyncStatuses(st, s.now().UTC())
		return nil
	})
	if err != nil {
		return
	}
	s.snap.Store(&snap)
}

// Handle implements Handler. Every operation goes through the store; nothing
// outside the service writes a file.
func (s *Service) Handle(ctx context.Context, req Request) (any, error) {
	switch req.Op {
	case OpStatus:
		return s.status(ctx)
	case OpMode:
		return s.setMode(ctx, req.Mode)
	case OpRulesList:
		return s.listRules(ctx)
	case OpRulesAdd:
		return s.addRule(ctx, req)
	case OpRulesRemove:
		return s.removeRule(ctx, req.ID)
	case OpPoolList:
		return s.listPool(ctx, req.Limit, req.Offset)
	case OpPoolAdd:
		return s.poolAdd(ctx, req)
	case OpPoolRemove:
		return s.poolRemove(ctx, req.ID)
	case OpPoolRefresh:
		return s.poolRefresh(ctx, req)
	case OpPoolTest:
		return s.poolTest(ctx, req)
	case OpPoolSelect:
		return s.poolSelect(ctx, req.ID)
	case OpPoolRerank:
		return s.rerank(ctx)
	case OpImport:
		return s.importSource(ctx, req)
	case OpDiagnose:
		return s.diagnose(ctx, req)
	case OpProposalsList:
		return s.listProposals(), nil
	case OpProposalsAccept:
		return s.acceptProposal(ctx, req.ID)
	case OpProposalsReject:
		return s.rejectProposal(ctx, req.ID, req.Note)
	case OpProposalsRecheck:
		return s.recheckProposal(ctx, req.ID)
	case OpProposalsClear:
		return s.clearProposals(ctx)
	case OpConfigGet:
		return s.configGet()
	case OpConfigSet:
		return s.configSet(ctx, req.Key, req.Value)
	case OpConfigPath:
		return s.configPath(), nil
	case OpDoctor:
		return s.doctor(ctx)
	case OpEnv:
		return s.env()
	case OpDesktopStatus:
		return s.desktopStatus(ctx)
	case OpDesktopSet:
		return s.desktopApply(ctx)
	case OpDesktopRestore:
		return s.desktopRestore(ctx)
	case OpBackup:
		return s.backup(ctx)
	case OpRestore:
		return s.restore(ctx, req.Ref)
	default:
		return nil, &contract.Failure{Kind: contract.Unsupported, Path: contract.PathLocal, Op: "control.handle", Message: "unknown management operation"}
	}
}

// routerView resolves the current immutable policy for the frontend and
// probes. A nil policy is a programming error, never a permissive fallback.
type routerView struct{ s *Service }

func (r routerView) Decide(ctx context.Context, dst contract.Destination) (contract.Decision, error) {
	p := r.s.policy.Load()
	if p == nil {
		return contract.Decision{}, &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "control.router", Message: "routing policy is not initialized"}
	}
	return p.Decide(ctx, dst)
}

func (r routerView) PolicyVersion() contract.PolicyVersion {
	p := r.s.policy.Load()
	if p == nil {
		return 0
	}
	return p.PolicyVersion()
}

// mutateAndCommit holds pubMu across the read-modify-write, policy rebuild and
// snapshot publication, so a health or proposal write cannot publish an older
// version after this commit. Callers hold opMu; sweeps happen afterwards.
func (s *Service) mutateAndCommit(ctx context.Context, fn func(*contract.State) error) (contract.Snapshot, *routing.Policy, error) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap, err := s.store.Mutate(ctx, fn)
	if err != nil {
		return contract.Snapshot{}, nil, err
	}
	policy, err := s.rebuildPolicy(snap)
	if err != nil {
		return contract.Snapshot{}, nil, err
	}
	s.commitSnapshotLocked(snap, policy)
	return snap, policy, nil
}

// commitSnapshotLocked is commitSnapshot for callers that already hold pubMu.
func (s *Service) commitSnapshotLocked(snap contract.Snapshot, policy *routing.Policy) {
	s.fenceMu.Lock()
	defer s.fenceMu.Unlock()
	s.snap.Store(&snap)
	s.policy.Store(policy)
	s.httpListen = snap.Config.HTTPListen
	s.socksListen = snap.Config.SOCKSListen
	if s.engine != nil {
		s.engine.SetPolicyVersion(policy.PolicyVersion())
	}
}

func (s *Service) rebuildPolicy(snap contract.Snapshot) (*routing.Policy, error) {
	return routing.NewPolicy(snap.State.PolicyVersion, routing.Config{
		Mode: snap.State.Mode, Rules: snap.State.Rules, SelfPorts: selfPorts(snap.Config),
	})
}

func (s *Service) sweep(ctx context.Context, cause contract.CloseCause, next contract.PolicyVersion, keep func(contract.ConnInfo) bool) (contract.CloseReport, error) {
	report, err := s.reg.CloseConflicting(ctx, cause, next, keep)
	if err != nil {
		return report, err
	}
	if len(report.Errors) > 0 {
		return report, &contract.Failure{
			Kind: contract.Internal, Path: contract.PathLocal, Op: "control.sweep",
			Message: "a conflicting connection did not close cleanly; the change is not reported as successful",
			Err:     errors.Join(report.Errors...),
		}
	}
	return report, nil
}

// routeKeep keeps a connection only when the new policy still chooses the same
// route for its destination. It is pure and is called under the registry fence.
func routeKeep(p *routing.Policy) func(contract.ConnInfo) bool {
	return func(ci contract.ConnInfo) bool {
		if p == nil {
			return false
		}
		dec, err := p.Decide(context.Background(), ci.Destination)
		return err == nil && dec.Route == ci.Route
	}
}

func validMode(mode contract.Mode) bool {
	switch mode {
	case contract.ModeTargeted, contract.ModeProxyAll, contract.ModeDirectAll, contract.ModeStopped:
		return true
	}
	return false
}

func selfPorts(cfg contract.Config) []uint16 {
	var ports []uint16
	for _, addr := range []string{cfg.HTTPListen, cfg.SOCKSListen} {
		_, portRaw, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portRaw)
		if err == nil && port > 0 && port <= 65535 {
			ports = append(ports, uint16(port))
		}
	}
	return ports
}

func dialableRecords(st contract.State) []contract.Record {
	out := make([]contract.Record, 0, len(st.Records))
	for _, rec := range st.Records {
		if rec.Status == contract.RecordInvalid || rec.Status == contract.RecordUnsupported {
			continue
		}
		if !pool.Dialable(rec) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func newRuleID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "r-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "r-" + hex.EncodeToString(b[:])
}

func ruleOperand(r contract.Rule) string {
	switch r.Kind {
	case contract.RuleDomain, contract.RuleDomainSuffix:
		return r.Host
	case contract.RuleIP:
		return r.Addr.String()
	case contract.RuleCIDR:
		return r.Prefix.String()
	}
	return ""
}

func isLoopbackListen(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

var _ Handler = (*Service)(nil)

// fencedPool enforces the policy fence around a pool dial without holding a
// lease for the dial's whole lifetime.
//
// The read lease covers two short moments: the admission check (so a dial can
// never observe a half-published snapshot/policy pair) and the completion
// revalidation. It is deliberately not held across the dial: a mode/rule change
// must be able to commit and sweep immediately, so the sweep can cancel a
// pending dial inside the documented bound instead of waiting for its deadline
// (docs/contracts.md §3.4, finding F3).
//
// Safety does not depend on the lease. A dial that finishes after the policy
// moved is revalidated when it completes and is closed and reported Canceled
// unless the new policy still keeps it (same pool route, usable exit); and
// every pool dial runs on the registry handle's context, so the sweep's close
// cancels it. Together they preserve §2.7's rule that a stale dial can never
// complete against a superseded policy, while §2.5's audit-only version bump
// does not cancel work the sweep itself would have kept.
type fencedPool struct {
	svc   *Service
	inner contract.PoolDialer
}

func (p *fencedPool) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	if err := p.admitted(req.PolicyVersion); err != nil {
		return contract.PoolDialResult{}, err
	}
	res, err := p.inner.DialPool(ctx, req)
	if err != nil {
		return res, err
	}
	if !p.current(req.PolicyVersion) && !p.completedUnderCurrentPolicy(ctx, req.Destination, res.Exit) {
		// The dial crossed a policy commit that superseded it. The registry
		// sweep has already closed the handle this dial belongs to; close the
		// connection it produced instead of letting it be attached and used.
		if res.Conn != nil {
			_ = res.Conn.Close()
		}
		return contract.PoolDialResult{}, &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "control.pool",
			Destination: req.Destination,
			Retryable:   true,
			Message:     "policy changed while the dial was in flight; the stale connection was closed",
		}
	}
	return res, nil
}

// admitted reports whether reqVersion is the currently published policy
// version. It runs under the read lease, so it cannot see a snapshot/policy
// pair that is only half stored.
func (p *fencedPool) admitted(reqVersion contract.PolicyVersion) error {
	p.svc.fenceMu.RLock()
	policy := p.svc.policy.Load()
	p.svc.fenceMu.RUnlock()
	if policy == nil || reqVersion != policy.PolicyVersion() {
		return &contract.Failure{
			Kind:      contract.Canceled,
			Path:      contract.PathLocal,
			Op:        "control.pool",
			Retryable: true,
			Message:   "connection was admitted under a stale policy version",
		}
	}
	return nil
}

// current reports whether version is still the published policy version.
func (p *fencedPool) current(version contract.PolicyVersion) bool {
	p.svc.fenceMu.RLock()
	policy := p.svc.policy.Load()
	p.svc.fenceMu.RUnlock()
	return policy != nil && policy.PolicyVersion() == version
}

// completedUnderCurrentPolicy reports whether a connection that finished
// dialing after the policy moved may still be used. The rule mirrors the
// sweep's keep predicate for connections that were already live — the new
// policy must still route the destination to the pool — and adds the exit
// check a pending dial cannot get from the registry, because its exit is
// unknown until the dial returns. That keeps an audit-only version bump
// (`pool select`, which §2.5 says closes no connection) from cancelling a
// pending dial, while a mode/rule change or a removed/unusable exit still
// rejects it. §2.5, §2.6, §2.7.
func (p *fencedPool) completedUnderCurrentPolicy(ctx context.Context, dst contract.Destination, exit contract.ExitID) bool {
	p.svc.fenceMu.RLock()
	policy := p.svc.policy.Load()
	snap := p.svc.Snapshot()
	p.svc.fenceMu.RUnlock()
	if policy == nil {
		return false
	}
	dec, err := policy.Decide(ctx, dst)
	if err != nil || dec.Route != contract.RoutePool {
		return false
	}
	return pool.Build(snap.State, p.svc.now()).Usable(exit)
}

// directDialer is the production direct path: system resolver allowed, but a
// resolved loopback address that names one of our own listener ports is refused
// before connect (docs/contracts.md §1.4). TCP conns expose CloseWrite.
type directDialer struct {
	selfPorts map[uint16]struct{}
	dialer    net.Dialer
}

func newDirectDialer(ports []uint16) *directDialer {
	set := make(map[uint16]struct{}, len(ports))
	for _, p := range ports {
		set[p] = struct{}{}
	}
	return &directDialer{selfPorts: set, dialer: net.Dialer{Timeout: contract.DirectDialDeadline}}
}

func (d *directDialer) DialDirect(ctx context.Context, dst contract.Destination) (net.Conn, error) {
	port := strconv.Itoa(int(dst.Port))
	address := net.JoinHostPort(hostOf(dst), port)
	dialer := d.dialer
	dialer.Control = func(network, address string, c syscall.RawConn) error {
		host, p, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err == nil && ip.IsLoopback() {
			if n, err := strconv.Atoi(p); err == nil {
				if _, self := d.selfPorts[uint16(n)]; self {
					return &contract.Failure{
						Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "direct.dial",
						Destination: dst, Message: "destination resolves to a RouteLatch listener; refusing the proxy loop",
					}
				}
			}
		}
		return nil
	}
	return dialer.DialContext(ctx, "tcp", address)
}

func hostOf(dst contract.Destination) string {
	if dst.Kind == contract.DestDomain {
		return dst.Host
	}
	return dst.Addr.Unmap().String()
}

// diagnosticDialer reports eligible direct-path failures to the diagnostics
// manager. It never changes the failure, never replays the request and never
// interferes with the direct path.
type diagnosticDialer struct {
	inner contract.Dialer
	svc   *Service
}

func (d *diagnosticDialer) DialDirect(ctx context.Context, dst contract.Destination) (net.Conn, error) {
	conn, err := d.inner.DialDirect(ctx, dst)
	if err == nil {
		return conn, nil
	}
	failure := directFailure(err, dst)
	if d.svc != nil && d.svc.diag != nil {
		if ok, _ := d.svc.diag.Evaluate(ctx, diagnostics.FailureEvent{Failure: failure}); ok {
			_, _ = d.svc.diag.SubmitFailure(ctx, failure)
		}
	}
	return conn, failure
}

// directFailure classifies a raw direct dial error without echoing the address
// or resolver text. Only the three documented direct kinds are produced.
func directFailure(err error, dst contract.Destination) *contract.Failure {
	var existing *contract.Failure
	if errors.As(err, &existing) {
		return existing
	}
	kind := contract.ConnectRefused
	message := "the direct connection failed"
	switch {
	case errors.Is(err, context.Canceled):
		kind = contract.Canceled
		message = "the direct dial was canceled"
	case errors.Is(err, context.DeadlineExceeded):
		kind = contract.Timeout
		message = "the direct dial timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		kind = contract.ConnectRefused
		message = "the destination refused the direct connection"
	default:
		var dnsErr *net.DNSError
		var netErr net.Error
		if errors.As(err, &dnsErr) {
			kind = contract.DNSFailure
			message = "the local resolver could not resolve the destination"
		} else if errors.As(err, &netErr) && netErr.Timeout() {
			kind = contract.Timeout
			message = "the direct dial timed out"
		}
	}
	return &contract.Failure{
		Kind: kind, Path: contract.PathDirect, Op: "direct.dial",
		Destination: dst, Message: message, Err: err,
	}
}
