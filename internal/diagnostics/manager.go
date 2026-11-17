// Package diagnostics turns an eligible direct-path failure into bounded,
// independent probe evidence and a proposal. It never replays the failed
// request, never sends application bytes, never creates a rule and never
// changes routing (D09, docs/contracts.md §3.5, §6).
//
// Health probes and pool ranking are owned by the Prober/pool packages and are
// deliberately not reached from here.
package diagnostics

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// ProposalStore is the registry contract used by the control layer. The
// concrete registry lives in internal/proposals; diagnostics depends on this
// narrow interface so it cannot write state or create rules.
type ProposalStore interface {
	Upsert(dst contract.Destination, ev contract.Evidence, note string) (contract.Proposal, bool, error)
	Pending() []contract.Proposal
	Get(id contract.ProposalID) (contract.Proposal, bool)
	Accept(id contract.ProposalID) (contract.Destination, error)
	Reject(id contract.ProposalID, note string) error
	Recheck(id contract.ProposalID, ev contract.Evidence, note string) (contract.Proposal, bool, error)
}

// Options wires the manager to the service. Prober is the only component that
// performs network activity; Store is optional for callers that only need the
// diagnostic result.
type Options struct {
	Prober        contract.Prober
	Router        contract.Router
	Store         ProposalStore
	Config        func() contract.DiagnosticsConfig
	ProbeConfig   func() contract.ProbeConfig
	Mode          func() contract.Mode
	PolicyVersion func() contract.PolicyVersion
	Clock         func() time.Time
}

// Errors exposed to the control layer. They are command outcomes, not routing
// failures, so they do not invent a contract ErrorKind.
var (
	ErrClosed   = errors.New("diagnostics manager is closed")
	ErrCooldown = errors.New("destination probe cooldown has not elapsed")
	ErrInFlight = errors.New("a probe for this destination is already in flight")
	ErrNoProber = errors.New("diagnostics manager has no prober")

	ErrProposalNotFound = errors.New("proposal not found")
	ErrProposalDecided  = errors.New("proposal was already decided")
	ErrProposalExpired  = errors.New("proposal expired")
)

// Stats is the `diagnostics` block of status.
type Stats struct {
	Queued   int  `json:"queued"`
	InFlight int  `json:"inflight"`
	Disabled bool `json:"disabled"`
}

// SubmitResult reports whether a failure became a queued candidate.
type SubmitResult struct {
	Accepted      bool
	Duplicate     bool
	DroppedOldest bool
	Reason        string
}

// Attempt is one bounded probe outcome for `diagnose`/`proposals recheck`.
type Attempt struct {
	Kind     contract.ProbeKind    `json:"kind"`
	Outcome  contract.ProbeOutcome `json:"outcome"`
	Failure  contract.ErrorKind    `json:"failure,omitempty"`
	Duration time.Duration         `json:"duration_ns"`
}

// DiagnoseResult is the manual command's bounded answer.
type DiagnoseResult struct {
	Destination contract.Destination `json:"destination"`
	Attempts    []Attempt            `json:"attempts"`
	Eligible    bool                 `json:"eligible"`
	ProposalID  contract.ProposalID  `json:"proposal_id,omitempty"`
	Scope       string               `json:"scope"`
}

// RecheckResult is the proposal recheck answer. A rejected proposal keeps its
// status even when fresh evidence is added.
type RecheckResult struct {
	Proposal      contract.Proposal
	Attempts      []Attempt
	Eligible      bool
	EvidenceAdded bool
}

const diagnoseScope = "one destination, direct probes only (TCP, plus verified TLS on port 443); no application bytes and no request replay"

type job struct {
	dst    contract.Destination
	key    string
	kind   contract.ErrorKind
	auto   bool // automatic failure-triggered candidate, not a manual command
	ctx    context.Context
	cancel context.CancelFunc
}

// Manager owns the bounded candidate queue, diagnostic probe budget and
// cancel-on-disable behaviour. Health management is independent and never
// reaches this queue.
type Manager struct {
	opts Options

	mu        sync.Mutex
	cond      *sync.Cond
	wg        sync.WaitGroup
	queue     []*job
	inflight  map[string]*job
	lastProbe map[string]time.Time
	closed    bool
	disabled  bool
}

// New starts the diagnostic worker. Call Close when the service stops.
func New(opts Options) *Manager {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	m := &Manager{
		opts:      opts,
		inflight:  make(map[string]*job),
		lastProbe: make(map[string]time.Time),
	}
	m.cond = sync.NewCond(&m.mu)
	// §6.4 fixes the diagnostic budget at one concurrent probe: the manager
	// runs one worker and a configured MaxConcurrent cannot raise that cap.
	m.wg.Add(1)
	go m.worker()
	return m
}

func (m *Manager) now() time.Time { return m.opts.Clock().UTC() }

func (m *Manager) mode() contract.Mode {
	if m.opts.Mode == nil {
		return contract.ModeTargeted
	}
	return m.opts.Mode()
}

func (m *Manager) config() contract.DiagnosticsConfig {
	cfg := contract.DiagnosticsConfig{Enabled: true, QueueLimit: 32, MaxConcurrent: 1, RetentionDays: 30}
	if m.opts.Config != nil {
		cfg = m.opts.Config()
	}
	if cfg.QueueLimit <= 0 {
		cfg.QueueLimit = 32
	}
	return cfg
}

func (m *Manager) enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabledLocked()
}

func (m *Manager) enabledLocked() bool {
	if m.disabled {
		return false
	}
	return m.config().Enabled
}

// Evaluate implements the eligibility matrix without side effects.
func (m *Manager) Evaluate(ctx context.Context, ev FailureEvent) (bool, string) {
	return m.evaluate(ctx, ev)
}

// Submit queues one eligible failure as a diagnostic candidate. It never
// blocks on the probe and never returns the user's failure text.
func (m *Manager) Submit(ctx context.Context, ev FailureEvent) (SubmitResult, error) {
	ok, reason := m.evaluate(ctx, ev)
	if !ok {
		return SubmitResult{Reason: reason}, nil
	}
	if err := ctx.Err(); err != nil {
		return SubmitResult{}, err
	}
	if m.opts.Prober == nil {
		return SubmitResult{Reason: ReasonNoProber}, nil
	}
	if !m.enabled() {
		return SubmitResult{Reason: ReasonDisabled}, nil
	}
	f := ev.Failure
	dst := f.Destination
	key := targetKey(dst)
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmitResult{}, ErrClosed
	}
	if !m.enabledLocked() {
		return SubmitResult{Reason: ReasonDisabled}, nil
	}
	for _, q := range m.queue {
		if q.key == key {
			return SubmitResult{Accepted: true, Duplicate: true}, nil
		}
	}
	if _, busy := m.inflight[key]; busy {
		return SubmitResult{Accepted: true, Duplicate: true}, nil
	}
	if last, seen := m.lastProbe[key]; seen && now.Sub(last) < m.cooldown() {
		return SubmitResult{Reason: ReasonCooldown}, nil
	}

	dropped := false
	if limit := m.config().QueueLimit; len(m.queue) >= limit {
		old := m.queue[0]
		old.cancel()
		m.queue = m.queue[1:]
		dropped = true
	}
	probeCtx, cancel := context.WithCancel(context.Background())
	m.queue = append(m.queue, &job{
		dst: dst, key: key, kind: f.Kind, auto: true, ctx: probeCtx, cancel: cancel,
	})
	m.cond.Signal()
	return SubmitResult{Accepted: true, DroppedOldest: dropped}, nil
}

// SubmitFailure is the common path for a typed direct failure.
func (m *Manager) SubmitFailure(ctx context.Context, f *contract.Failure) (SubmitResult, error) {
	return m.Submit(ctx, FailureEvent{Failure: f})
}

// Stats reports the bounded queue and in-flight count for status.
func (m *Manager) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	auto := 0
	for _, j := range m.inflight {
		if j.auto {
			auto++
		}
	}
	return Stats{Queued: len(m.queue), InFlight: auto, Disabled: !m.enabledLocked()}
}

// Disable cancels queued and in-flight diagnostic probes, stops new automatic
// candidates and calls the Prober's CancelQueued. Pool health is untouched.
func (m *Manager) Disable(ctx context.Context) (queued int, inflight int) {
	m.mu.Lock()
	m.disabled = true
	queued = len(m.queue)
	for _, q := range m.queue {
		q.cancel()
	}
	m.queue = nil
	for key, j := range m.inflight {
		if !j.auto {
			continue
		}
		inflight++
		j.cancel()
		delete(m.inflight, key)
	}
	m.mu.Unlock()
	if m.opts.Prober != nil {
		_, _ = m.opts.Prober.CancelQueued(ctx)
	}
	return queued, inflight
}

// Enable clears the manager's local disable latch. The configuration source
// still has to say Enabled=true for automatic diagnosis to resume.
func (m *Manager) Enable() {
	m.mu.Lock()
	m.disabled = false
	m.mu.Unlock()
}

// Close stops the worker and cancels all outstanding diagnostic work.
func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, q := range m.queue {
		q.cancel()
	}
	m.queue = nil
	for _, j := range m.inflight {
		j.cancel()
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	m.wg.Wait()
	if m.opts.Prober != nil {
		_, _ = m.opts.Prober.CancelQueued(ctx)
	}
}

// ListPending reports proposals that still await a decision.
func (m *Manager) ListPending() []contract.Proposal {
	if m.opts.Store == nil {
		return nil
	}
	return m.opts.Store.Pending()
}

// Accept returns exactly the destination of a pending proposal. Applying a
// rule is the caller's explicit, confirmed action; this package writes no rule
// and no state.
func (m *Manager) Accept(id contract.ProposalID) (contract.Destination, error) {
	if m.opts.Store == nil {
		return contract.Destination{}, ErrNoStore
	}
	return m.opts.Store.Accept(id)
}

// Reject records a user rejection.
func (m *Manager) Reject(id contract.ProposalID, note string) error {
	if m.opts.Store == nil {
		return ErrNoStore
	}
	return m.opts.Store.Reject(id, note)
}

// ErrNoStore means the caller asked for proposal operations without wiring a
// registry.
var ErrNoStore = errors.New("no proposal store configured")

// Diagnose runs one explicit, bounded direct diagnosis. It is independent of
// Diagnostics.Enabled and of pool health, but it never runs while stopped and
// never sends application bytes.
func (m *Manager) Diagnose(ctx context.Context, dst contract.Destination) (DiagnoseResult, error) {
	if m.mode() == contract.ModeStopped {
		return DiagnoseResult{}, &contract.Failure{
			Kind: contract.Stopped, Path: contract.PathLocal, Op: "diagnostics.diagnose",
			Message: "service is stopped",
		}
	}
	if !validTarget(dst) {
		return DiagnoseResult{}, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "diagnostics.diagnose",
			Message: "diagnose needs a normalized host:port or ip:port",
		}
	}
	results, ev, err := m.runManual(ctx, dst)
	res := DiagnoseResult{
		Destination: dst,
		Attempts:    attempts(results),
		Scope:       diagnoseScope,
	}
	if err != nil {
		return res, err
	}
	if ev != nil {
		res.Eligible = true
		if id, err := m.record(dst, *ev); err == nil {
			res.ProposalID = id
		} else {
			return res, err
		}
	}
	return res, nil
}

// Recheck runs the same bounded direct probe for an existing proposal and
// appends fresh evidence. It never revives a rejected or expired proposal.
func (m *Manager) Recheck(ctx context.Context, id contract.ProposalID) (RecheckResult, error) {
	if m.opts.Store == nil {
		return RecheckResult{}, ErrNoStore
	}
	p, ok := m.opts.Store.Get(id)
	if !ok {
		return RecheckResult{}, ErrProposalNotFound
	}
	switch p.Status {
	case contract.ProposalAccepted:
		return RecheckResult{}, ErrProposalDecided
	case contract.ProposalExpired:
		return RecheckResult{}, ErrProposalExpired
	}
	// Pending and rejected proposals may both be rechecked; the registry keeps
	// a rejected one rejected.
	results, ev, err := m.runManual(ctx, p.Destination)
	out := RecheckResult{Proposal: p, Attempts: attempts(results)}
	if err != nil {
		return out, err
	}
	if ev != nil {
		out.Eligible = true
		updated, added, err := m.opts.Store.Recheck(id, *ev, noteFor(p.Destination, *ev))
		if err != nil {
			return out, err
		}
		out.Proposal = updated
		out.EvidenceAdded = added
	}
	return out, nil
}

func (m *Manager) runManual(ctx context.Context, dst contract.Destination) ([]contract.ProbeResult, *contract.Evidence, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrClosed
	}
	key := targetKey(dst)
	if _, busy := m.inflight[key]; busy {
		m.mu.Unlock()
		return nil, nil, ErrInFlight
	}
	now := m.now()
	if last, seen := m.lastProbe[key]; seen && now.Sub(last) < m.cooldown() {
		m.mu.Unlock()
		return nil, nil, ErrCooldown
	}
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	j := &job{dst: dst, key: key, auto: false, ctx: probeCtx, cancel: cancel}
	m.inflight[key] = j
	m.lastProbe[key] = now
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if m.inflight[key] == j {
			delete(m.inflight, key)
		}
		m.mu.Unlock()
	}()

	// A manual command is bounded to one destination and at most two probes.
	probeCtx, timeout := context.WithTimeout(probeCtx, 2*contract.ProbeTimeout)
	defer timeout()
	return m.probePlan(probeCtx, dst, contract.TriggerManual)
}

func (m *Manager) record(dst contract.Destination, ev contract.Evidence) (contract.ProposalID, error) {
	if m.opts.Store == nil {
		return "", nil
	}
	p, _, err := m.opts.Store.Upsert(dst, ev, noteFor(dst, ev))
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && !m.closed {
			m.cond.Wait()
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		j := m.queue[0]
		m.queue = m.queue[1:]
		if !m.enabledLocked() {
			j.cancel()
			m.mu.Unlock()
			continue
		}
		// Re-check eligibility at execution time: a mode or rule change while
		// the candidate waited must not turn into a probe. Router.Decide is
		// pure and never dials.
		if ok, _ := m.evaluate(context.Background(), FailureEvent{Failure: &contract.Failure{
			Kind: j.kind, Path: contract.PathDirect, Destination: j.dst,
		}}); !ok {
			j.cancel()
			m.mu.Unlock()
			continue
		}
		now := m.now()
		if last, seen := m.lastProbe[j.key]; seen && now.Sub(last) < m.cooldown() {
			j.cancel()
			m.mu.Unlock()
			continue
		}
		m.lastProbe[j.key] = now
		m.inflight[j.key] = j
		m.mu.Unlock()

		_, ev, err := m.probePlan(j.ctx, j.dst, contract.TriggerFailure)
		m.mu.Lock()
		if m.inflight[j.key] == j {
			delete(m.inflight, j.key)
		}
		canceled := errors.Is(err, context.Canceled) || j.ctx.Err() != nil
		if canceled || !m.enabledLocked() {
			delete(m.lastProbe, j.key)
		}
		// Record while holding the lock so Disable cannot return before an
		// already-finishing probe has either been canceled or kept out of the
		// store. The store is the in-memory proposal registry, not disk.
		if err == nil && ev != nil && !canceled && m.enabledLocked() {
			_, _ = m.record(j.dst, *ev)
		}
		m.mu.Unlock()
		j.cancel()
	}
}

func (m *Manager) cooldown() time.Duration {
	base := contract.ProbeConfig{}
	if m.opts.ProbeConfig != nil {
		base = m.opts.ProbeConfig()
	}
	if base.CooldownBase <= 0 {
		return contract.ProbeCooldownBase
	}
	return time.Duration(base.CooldownBase) * time.Second
}

func attempts(results []contract.ProbeResult) []Attempt {
	out := make([]Attempt, 0, len(results))
	for _, r := range results {
		out = append(out, Attempt{Kind: r.Kind, Outcome: r.Outcome, Failure: r.Failure, Duration: r.Duration})
	}
	return out
}

func targetKey(dst contract.Destination) string {
	switch dst.Kind {
	case contract.DestDomain:
		return "domain|" + dst.Host + "|" + strconv.Itoa(int(dst.Port))
	case contract.DestIP:
		return "ip|" + dst.Addr.Unmap().String() + "|" + strconv.Itoa(int(dst.Port))
	default:
		return "invalid|" + string(dst.Kind)
	}
}

func address(dst contract.Destination) string {
	switch dst.Kind {
	case contract.DestDomain:
		return net.JoinHostPort(dst.Host, strconv.Itoa(int(dst.Port)))
	case contract.DestIP:
		return net.JoinHostPort(dst.Addr.Unmap().String(), strconv.Itoa(int(dst.Port)))
	default:
		return ""
	}
}
