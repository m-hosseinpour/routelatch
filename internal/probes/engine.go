// Package probes owns measurement concurrency, scheduling limits, retention
// and evidence attribution. It never replays a user request and never sends
// user data: a probe either touches an exit endpoint or completes a dial and
// waits for one downlink byte (docs/contracts.md §6).
package probes

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// Options wires the prober to the service. Only Pool, Records and Targets are
// required for egress/transport probes; the rest have contract defaults.
type Options struct {
	Pool               contract.PoolDialer
	Record             func(contract.ExitID) (contract.Record, bool)
	AllRecords         func() []contract.Record
	Mode               func() contract.Mode
	DiagnosticsEnabled func() bool
	PolicyVersion      func() contract.PolicyVersion
	Clock              func() time.Time
	Sink               func(contract.ProbeResult)
	OnMeasurement      func(Measurement)
	// Dial and TLSHandshake are injectable for loopback tests. The production
	// defaults use net.Dialer and crypto/tls with full verification.
	Dial         func(ctx context.Context, network, address string) (net.Conn, error)
	TLSHandshake func(ctx context.Context, conn net.Conn, cfg *tls.Config) (net.Conn, error)
	// LocalControl is an optional caller-supplied check of the machine's own
	// network. It is never run to compare direct against pool and is nil by
	// default; only a configured check can mark a failure as a local outage.
	LocalControl func(ctx context.Context) error

	Timeout              time.Duration
	FirstByte            time.Duration
	QueueLimit           int
	HealthConcurrent     int
	DiagnosticConcurrent int
	RotationInterval     time.Duration
	RotationBatch        int
	Targets              []string
}

// Phase names where a failure happened so first-byte stalls are not confused
// with dial-phase transport failures.
type Phase string

const (
	PhaseNone      Phase = ""
	PhaseDial      Phase = "dial"
	PhaseHandshake Phase = "handshake"
	PhaseFirstByte Phase = "first_byte"
)

// Measurement is a probe result plus the attribution the pool needs. Result
// carries the contract attribution (ProbeResult.Attribution); Attributable and
// LocalOutage stay as the prober's phase-level view for scheduling tests.
type Measurement struct {
	Result       contract.ProbeResult
	Attributable bool
	LocalOutage  bool
	Phase        Phase
}

var _ contract.Prober = (*Engine)(nil)

// Engine implements contract.Prober.
type Engine struct {
	opts Options

	healthSem chan struct{}
	diagSem   chan struct{}

	mu         sync.Mutex
	queue      []*queued
	closed     bool
	cond       *sync.Cond
	lastRotate time.Time
	rotateAt   int
	lastProbe  map[string]time.Time
	inflight   map[string]bool

	retention *Retention
}

type queued struct {
	req    contract.ProbeRequest
	cancel bool
}

// ErrQueueFull means the bounded automatic queue has no health slot left.
var ErrQueueFull = errors.New("probe queue is full")

// ErrProbeInFlight and ErrProbeCooldown make duplicate destination probes
// observable without turning them into a routing failure.
var (
	ErrProbeInFlight = errors.New("a probe for this destination or exit is already in flight")
	ErrProbeCooldown = errors.New("destination probe cooldown has not elapsed")
)

// NewEngine starts the dispatcher. Close releases it.
func NewEngine(opts Options) *Engine {
	if opts.Timeout <= 0 {
		opts.Timeout = contract.ProbeTimeout
	}
	if opts.FirstByte <= 0 {
		opts.FirstByte = contract.ProbeFirstByte
	}
	if opts.QueueLimit <= 0 {
		opts.QueueLimit = 32
	}
	if opts.HealthConcurrent <= 0 {
		opts.HealthConcurrent = 2
	}
	if opts.DiagnosticConcurrent <= 0 {
		opts.DiagnosticConcurrent = 1
	}
	if opts.RotationInterval <= 0 {
		opts.RotationInterval = contract.HealthProbeInterval
	}
	if opts.RotationBatch <= 0 {
		opts.RotationBatch = 8
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Dial == nil {
		d := &net.Dialer{}
		opts.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, address)
		}
	}
	e := &Engine{
		opts:      opts,
		healthSem: make(chan struct{}, opts.HealthConcurrent),
		diagSem:   make(chan struct{}, opts.DiagnosticConcurrent),
		retention: NewRetention(opts.Clock),
		lastProbe: make(map[string]time.Time),
		inflight:  make(map[string]bool),
	}
	e.cond = sync.NewCond(&e.mu)
	go e.dispatch()
	return e
}

// Close stops the dispatcher and cancels queued work.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	for _, q := range e.queue {
		q.cancel = true
	}
	e.queue = nil
	e.cond.Broadcast()
	e.mu.Unlock()
	e.cond.Broadcast()
}

// Enqueue implements contract.Prober. A full queue drops the oldest automatic
// diagnostic request; a health sample is never dropped for a diagnostic.
func (e *Engine) Enqueue(ctx context.Context, req contract.ProbeRequest) error {
	if err := e.checkPolicy(req); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return canceledFailure("probes.enqueue")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return &contract.Failure{Kind: contract.Canceled, Path: contract.PathLocal, Op: "probes.enqueue", Message: "prober is closed"}
	}
	if len(e.queue) >= e.opts.QueueLimit {
		index := -1
		for i, q := range e.queue {
			if isDiagnostic(q.req) {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrQueueFull
		}
		e.queue = append(e.queue[:index], e.queue[index+1:]...)
	}
	e.queue = append(e.queue, &queued{req: req})
	e.cond.Signal()
	return nil
}

// Run implements contract.Prober. It acquires the same semaphore as queued work
// so a caller cannot bypass the concurrency cap.
func (e *Engine) Run(ctx context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
	m, err := e.RunMeasurement(ctx, req)
	return m.Result, err
}

// RunMeasurement is Run with attribution. Callers that update pool health must
// use this so a local outage or an unconfirmed result cannot cool an exit.
func (e *Engine) RunMeasurement(ctx context.Context, req contract.ProbeRequest) (Measurement, error) {
	return e.runMeasurement(ctx, req, true)
}

func (e *Engine) runMeasurement(ctx context.Context, req contract.ProbeRequest, acquire bool) (Measurement, error) {
	if err := e.checkPolicy(req); err != nil {
		return Measurement{}, err
	}
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return Measurement{}, canceledFailure("probes.run")
	}
	key, err := e.admit(req)
	if err != nil {
		return Measurement{}, err
	}
	defer e.releaseProbe(key)
	var sem chan struct{}
	if acquire {
		sem = e.semaphore(req)
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return Measurement{}, canceledFailure("probes.run")
		}
	}

	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	start := e.opts.Clock()
	m := e.measure(ctx, req, start)
	switch {
	case m.Result.Kind == contract.ProbeDirectTCP || m.Result.Kind == contract.ProbeDirectTLS:
		// A direct probe is destination evidence, never exit evidence.
		m.Result.Attribution = contract.ProbeAttributionNone
	case m.LocalOutage:
		m.Result.Attribution = contract.ProbeAttributionLocalOutage
	case m.Attributable:
		m.Result.Attribution = contract.ProbeAttributionExit
	default:
		m.Result.Attribution = contract.ProbeAttributionNone
	}
	if m.Result.Kind == contract.ProbeTransport && m.Result.Outcome == contract.OutcomeSuccess {
		// Transport probes measure the endpoint only and never prove egress.
		m.Result.Eligible = false
	}
	if m.Result.Exit != "" && (m.Result.Kind == contract.ProbeTransport || m.Result.Kind == contract.ProbeEgress) {
		e.retention.Add(Sample{
			Exit: m.Result.Exit, Kind: m.Result.Kind, Destination: m.Result.Destination,
			At: start.UTC(), DurationMS: m.Result.Duration.Milliseconds(),
			Outcome: m.Result.Outcome, Failure: m.Result.Failure,
			Eligible: m.Result.Eligible, BytesDown: m.Result.BytesDown,
			LocalOutage: m.LocalOutage,
		})
	}
	if e.opts.OnMeasurement != nil {
		e.opts.OnMeasurement(m)
	}
	return m, nil
}

func (e *Engine) admit(req contract.ProbeRequest) (string, error) {
	key := probeKey(req)
	now := e.opts.Clock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inflight[key] {
		return "", ErrProbeInFlight
	}
	if isDestinationProbe(req) {
		if last, ok := e.lastProbe[key]; ok && now.Sub(last) < contract.ProbeCooldownBase {
			return "", ErrProbeCooldown
		}
	}
	e.inflight[key] = true
	return key, nil
}

func (e *Engine) releaseProbe(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inflight, key)
	e.lastProbe[key] = e.opts.Clock()
}

func isDestinationProbe(req contract.ProbeRequest) bool {
	return req.Kind == contract.ProbeDirectTCP || req.Kind == contract.ProbeDirectTLS
}

func probeKey(req contract.ProbeRequest) string {
	if isDestinationProbe(req) {
		return string(req.Kind) + "|" + destinationKey(req.Destination)
	}
	if req.Kind == contract.ProbeEgress {
		return string(req.Kind) + "|exit:" + string(req.Exit) + "|" + destinationKey(req.Destination)
	}
	return string(req.Kind) + "|exit:" + string(req.Exit)
}

func destinationKey(dst contract.Destination) string {
	switch dst.Kind {
	case contract.DestDomain:
		return dst.Host + ":" + strconv.Itoa(int(dst.Port))
	case contract.DestIP:
		return dst.Addr.String() + ":" + strconv.Itoa(int(dst.Port))
	default:
		return string(dst.Kind)
	}
}

// CancelQueued implements contract.Prober. It removes queued diagnostic probes
// and never cancels one already reporting a health result.
func (e *Engine) CancelQueued(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, canceledFailure("probes.cancel")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.queue[:0]
	canceled := 0
	for _, q := range e.queue {
		if isDiagnostic(q.req) {
			q.cancel = true
			canceled++
			continue
		}
		kept = append(kept, q)
	}
	e.queue = kept
	return canceled, nil
}

// Rotate returns the next automatic transport batch when the interval elapsed.
// It returns nothing outside targeted/all-proxy modes, so all-direct and
// stopped never generate background activity.
func (e *Engine) Rotate(now time.Time) []contract.ProbeRequest {
	mode := e.mode()
	if mode != contract.ModeTargeted && mode != contract.ModeProxyAll {
		return nil
	}
	if e.opts.AllRecords == nil {
		return nil
	}
	e.mu.Lock()
	if !e.lastRotate.IsZero() && now.Sub(e.lastRotate) < e.opts.RotationInterval {
		e.mu.Unlock()
		return nil
	}
	e.lastRotate = now
	cursor := e.rotateAt
	e.mu.Unlock()

	records := e.opts.AllRecords()
	if len(records) == 0 {
		return nil
	}
	var batch []contract.ProbeRequest
	transports := 0
	for i := 0; i < len(records) && len(batch) < e.opts.RotationBatch; i++ {
		rec := records[(cursor+i)%len(records)]
		batch = append(batch, contract.ProbeRequest{
			Kind: contract.ProbeTransport, Exit: contract.ExitID(rec.ID), Trigger: contract.TriggerRotate,
		})
		transports++
	}
	// Egress rotation runs only when the user configured a target; RouteLatch
	// never invents a third-party destination (§6.1).
	for i, rawTarget := range e.opts.Targets {
		if len(batch) >= e.opts.RotationBatch {
			break
		}
		dst, ok := ParseTarget(rawTarget)
		if !ok {
			continue
		}
		version := contract.PolicyVersion(0)
		if e.opts.PolicyVersion != nil {
			version = e.opts.PolicyVersion()
		}
		// Name the exit explicitly so a probe never moves the user's automatic
		// selection through the shared DialPool selector.
		exit := contract.ExitID(records[(cursor+i)%len(records)].ID)
		batch = append(batch, contract.ProbeRequest{
			Kind: contract.ProbeEgress, Trigger: contract.TriggerRotate, Exit: exit,
			Destination: dst, PolicyVersion: version,
		})
	}
	e.mu.Lock()
	e.rotateAt = (cursor + transports) % len(records)
	e.mu.Unlock()
	return batch
}

// Retention exposes the bounded sample store for status/rerank consumers.
func (e *Engine) Retention() *Retention { return e.retention }

func canceledFailure(op string) *contract.Failure {
	return &contract.Failure{
		Kind: contract.Canceled, Path: contract.PathLocal, Op: op,
		Message: "probe canceled",
	}
}

func (e *Engine) dispatch() {
	for {
		e.mu.Lock()
		for len(e.queue) == 0 && !e.closed {
			e.cond.Wait()
		}
		if e.closed {
			e.mu.Unlock()
			return
		}
		q := e.queue[0]
		sem := e.semaphore(q.req)
		e.mu.Unlock()

		// Acquire the same semaphore as direct callers. The request stays in
		// the queue while it waits, so CancelQueued can still cancel it.
		sem <- struct{}{}
		e.mu.Lock()
		if e.closed || q.cancel || len(e.queue) == 0 || e.queue[0] != q {
			e.mu.Unlock()
			<-sem
			continue
		}
		e.queue = e.queue[1:]
		e.mu.Unlock()

		m, err := e.runMeasurement(context.Background(), q.req, false)
		<-sem
		if err != nil {
			continue
		}
		if e.opts.Sink != nil {
			e.opts.Sink(m.Result)
		}
	}
}

func (e *Engine) semaphore(req contract.ProbeRequest) chan struct{} {
	if isDiagnostic(req) {
		return e.diagSem
	}
	return e.healthSem
}

func (e *Engine) checkPolicy(req contract.ProbeRequest) error {
	mode := e.mode()
	if mode == contract.ModeStopped {
		return &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "probes.policy", Message: "service is stopped"}
	}
	if mode == contract.ModeDirectAll && req.Trigger != contract.TriggerManual {
		return &contract.Failure{Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "probes.policy", Message: "automatic probes do not run in all-direct mode"}
	}
	if req.Trigger == contract.TriggerFailure && e.opts.DiagnosticsEnabled != nil && !e.opts.DiagnosticsEnabled() {
		return &contract.Failure{Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "probes.policy", Message: "automatic diagnostics are disabled"}
	}
	if !triggerAllowed(req.Kind, req.Trigger) {
		return &contract.Failure{Kind: contract.Unsupported, Path: contract.PathLocal, Op: "probes.policy", Message: "probe kind is not allowed for this trigger"}
	}
	if req.Kind == contract.ProbeEgress {
		if req.Exit == "" {
			return &contract.Failure{Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "probes.policy", Message: "egress probe needs an exit"}
		}
		if req.Destination.Kind == "" || req.Destination.Port == 0 ||
			(req.Destination.Kind == contract.DestDomain && req.Destination.Host == "") ||
			(req.Destination.Kind == contract.DestIP && (!req.Destination.Addr.IsValid() || req.Destination.Addr.IsUnspecified())) {
			return &contract.Failure{Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "probes.policy", Message: "egress probe needs an explicit target"}
		}
		if req.Trigger != contract.TriggerManual && !e.isConfiguredTarget(req.Destination) {
			return &contract.Failure{Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "probes.policy", Message: "automatic egress probes use configured targets only"}
		}
	}
	if req.Kind == contract.ProbeTransport && req.Exit == "" {
		return &contract.Failure{Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "probes.policy", Message: "transport probe needs an exit"}
	}
	if req.Kind == contract.ProbeDirectTCP || req.Kind == contract.ProbeDirectTLS {
		if req.Destination.Kind == "" || req.Destination.Port == 0 ||
			(req.Destination.Kind == contract.DestDomain && req.Destination.Host == "") ||
			(req.Destination.Kind == contract.DestIP && (!req.Destination.Addr.IsValid() || req.Destination.Addr.IsUnspecified())) {
			return &contract.Failure{Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "probes.policy", Message: "direct probe needs an explicit destination"}
		}
		if req.Kind == contract.ProbeDirectTLS && req.Destination.Kind != contract.DestDomain {
			return &contract.Failure{Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "probes.policy", Message: "direct TLS probe needs a hostname, not an IP literal"}
		}
	}
	return nil
}

func triggerAllowed(kind contract.ProbeKind, trigger contract.ProbeTrigger) bool {
	switch kind {
	case contract.ProbeTransport:
		switch trigger {
		case contract.TriggerStartup, contract.TriggerRotate, contract.TriggerRerank,
			contract.TriggerManual, contract.TriggerFailure:
			return true
		}
	case contract.ProbeEgress:
		switch trigger {
		case contract.TriggerManual, contract.TriggerRerank, contract.TriggerRotate:
			return true
		}
	case contract.ProbeDirectTCP, contract.ProbeDirectTLS:
		switch trigger {
		case contract.TriggerFailure, contract.TriggerManual:
			return true
		}
	}
	return false
}

func isDiagnostic(req contract.ProbeRequest) bool {
	// Only destination diagnostics use the diagnostic slot. A transport or
	// egress probe triggered by a failure is still a health sample and must
	// never be evicted for a diagnostic request.
	return req.Kind == contract.ProbeDirectTCP || req.Kind == contract.ProbeDirectTLS
}

func (e *Engine) mode() contract.Mode {
	if e.opts.Mode == nil {
		return contract.ModeTargeted
	}
	return e.opts.Mode()
}

func (e *Engine) isConfiguredTarget(dst contract.Destination) bool {
	for _, t := range e.opts.Targets {
		parsed, ok := ParseTarget(t)
		if !ok {
			continue
		}
		if parsed.Kind == dst.Kind && parsed.Port == dst.Port && parsed.Host == dst.Host && parsed.Addr == dst.Addr {
			return true
		}
	}
	return false
}

// ParseTarget converts "host:port" to a Destination without resolving it.
func ParseTarget(raw string) (contract.Destination, bool) {
	host, portRaw, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return contract.Destination{}, false
	}
	port, err := strconv.ParseUint(portRaw, 10, 16)
	if err != nil || port == 0 {
		return contract.Destination{}, false
	}
	host = strings.Trim(host, "[]")
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.IsValid() || ip.IsUnspecified() {
			return contract.Destination{}, false
		}
		return contract.Destination{Kind: contract.DestIP, Addr: ip, Port: uint16(port)}, true
	}
	if host == "" || strings.ContainsAny(host, " /@?#\t\r\n") {
		return contract.Destination{}, false
	}
	return contract.Destination{Kind: contract.DestDomain, Host: strings.ToLower(host), Port: uint16(port)}, true
}
