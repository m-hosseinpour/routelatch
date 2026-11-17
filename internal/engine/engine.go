package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"

	// The distribution registration is what makes the pinned core able to build
	// the handlers named in our generated config (spike/xray/FINDINGS.md §0).
	// It registers the core's own code; it does not read any imported config.
	_ "github.com/xtls/xray-core/main/distro/all"

	"routelatch/internal/contract"
)

// Options configures one engine start. Exits are the records the pool considers
// usable; the engine validates their shape but never their health or status,
// because selection belongs to the pool.
type Options struct {
	Exits         []contract.Record
	PolicyVersion contract.PolicyVersion
	// LogLevel is one of "debug", "info", "warning", "error", "none"; empty
	// means "warning". Access logging is always disabled.
	LogLevel string
}

// Exit is the engine's per-record dialing handle. Tag is empty for protocols
// dialed by our own adapter (SOCKS5), which is the contract's rule for an
// adapter-selected exit (docs/contracts.md §2.1).
type Exit struct {
	ID        contract.ExitID    `json:"id"`
	Tag       string             `json:"tag,omitempty"`
	Protocol  contract.Protocol  `json:"protocol"`
	Transport contract.Transport `json:"transport"`
}

// DialRequest is one dial through one named exit. Exit must be set: the engine
// never selects an exit, and an empty value is PoolUnavailable rather than a
// cue to pick a default (that default would be a silent substitution).
type DialRequest struct {
	Exit          contract.ExitID
	Destination   contract.Destination
	PolicyVersion contract.PolicyVersion
}

// DialResult mirrors contract.PoolDialResult so the pool can adapt it without
// losing information. Proof is never ProofUnknown on success.
type DialResult struct {
	Conn       net.Conn
	Exit       contract.ExitID
	Tag        string
	Proof      contract.ProofClass
	AcceptedAt time.Time
}

// WarmTransport reports a core transport that may outlive its user connection
// and cannot be closed through the pinned core's API: gRPC and XHTTP keep
// process-global dialer state, including across core.Stop (spike/xray/FINDINGS.md
// §5, R-4). It is reported conservatively: the first accepted dial through such
// an exit marks it, because an accepted dial is all the core ever confirms.
// These connections carry no destination decision and are outside the registry;
// a mode change must show them as "engine transports, outside the registry" and
// can only clear them with a process restart, which v1 never does in place.
type WarmTransport struct {
	Exit      contract.ExitID    `json:"exit"`
	Tag       string             `json:"tag"`
	Transport contract.Transport `json:"transport"`
	Since     time.Time          `json:"since"`
}

// Engine owns the single core instance of this process and the exit table built
// from trusted records.
type Engine struct {
	mu        sync.RWMutex
	inst      *core.Instance
	exits     map[contract.ExitID]*exitSpec
	version   contract.PolicyVersion
	stopped   bool
	rawConfig []byte
	// excluded records could not be built and are reported, not fatal.
	excluded []ExcludedExit
	// handlers is the set of tags that already have a core outbound handler.
	// It outlives RemoveExit because the pinned core's RemoveHandler does not
	// close the handler and re-adding a tag is rejected; the stable tag must be
	// reusable when a removed record is restored.
	handlers map[string]struct{}
	// retired remembers the identity of a removed exit so its reserved tag is
	// never reused for a record with different settings.
	retired map[contract.ExitID]string

	connMu   sync.Mutex
	conns    map[io.Closer]struct{}
	stopping atomic.Bool

	warmMu sync.Mutex
	warm   map[contract.ExitID]WarmTransport
}

type exitSpec struct {
	record   contract.Record
	exit     Exit
	outbound *coreOutbound
}

// ReasonRecordRejected is the stable StatusReason for a record the engine could
// not turn into an exit. The record is kept for reporting and never dialed.
const ReasonRecordRejected = "engine_record_rejected"

// ExcludedExit is one record Start (or AddExit) refused to build. Kind is the
// contract classification; Reason is a stable, redacted token suitable for
// status, while Detail is the actionable message for logs.
type ExcludedExit struct {
	ID     contract.ExitID    `json:"id"`
	Kind   contract.ErrorKind `json:"kind"`
	Reason string             `json:"reason"`
	Detail string             `json:"detail,omitempty"`
}

// CheckRecord reports whether one record can become a dialable exit without
// starting a core. It is the same validation and config build Start performs,
// exposed so the import path can reject unbuildable records before committing.
func CheckRecord(rec contract.Record) error {
	_, ob, err := prepareExit(rec)
	if err != nil {
		return err
	}
	if ob != nil {
		if _, err := singleOutboundConfig(*ob); err != nil {
			return describeRecordError(err)
		}
	}
	return nil
}

// Start validates the records, generates the configuration and starts the
// pinned core once. It never reads a configuration from an imported source and
// never starts a second instance while one is running.
//
// One unbuildable record never aborts the service: it is excluded and reported
// in Excluded(), while the remaining records serve. Only a genuinely global
// configuration or core failure aborts Start.
func Start(opts Options) (*Engine, error) {
	e := &Engine{
		exits:    make(map[contract.ExitID]*exitSpec),
		conns:    make(map[io.Closer]struct{}),
		warm:     make(map[contract.ExitID]WarmTransport),
		handlers: make(map[string]struct{}),
		retired:  make(map[contract.ExitID]string),
		version:  opts.PolicyVersion,
	}

	accepted := make([]*exitSpec, 0, len(opts.Exits))
	for _, rec := range opts.Exits {
		spec, ob, err := prepareExit(rec)
		if err != nil {
			excluded, ok := exclusionFor(rec, err)
			if !ok {
				return nil, err
			}
			e.excluded = append(e.excluded, excluded)
			continue
		}
		if _, taken := e.exits[spec.exit.ID]; taken {
			e.excluded = append(e.excluded, ExcludedExit{
				ID: spec.exit.ID, Kind: contract.ImportInvalid, Reason: ReasonRecordRejected,
				Detail: "duplicate exit id in the exit list",
			})
			continue
		}
		if ob != nil {
			if _, err := singleOutboundConfig(*ob); err != nil {
				excluded, ok := exclusionFor(rec, describeRecordError(err))
				if !ok {
					return nil, err
				}
				e.excluded = append(e.excluded, excluded)
				continue
			}
		}
		e.exits[spec.exit.ID] = spec
		accepted = append(accepted, spec)
	}

	var (
		raw []byte
		cfg *core.Config
	)
	for {
		outbounds := make([]coreOutbound, 0, len(accepted))
		for _, spec := range accepted {
			if spec.outbound != nil {
				outbounds = append(outbounds, *spec.outbound)
			}
		}
		var err error
		raw, err = generatedConfig(logLevelOrDefault(opts.LogLevel), outbounds)
		if err != nil {
			return nil, err
		}
		cfg, err = serial.LoadJSONConfig(bytes.NewReader(raw))
		if err == nil {
			break
		}
		// The pinned core rejects the whole document. Re-parse each remaining
		// record alone so one bad record is excluded by id instead of taking
		// the service down; only a failure with no attributable record is a
		// genuinely global config error.
		idx := -1
		for i, spec := range accepted {
			if spec.outbound == nil {
				continue
			}
			if _, err := singleOutboundConfig(*spec.outbound); err != nil {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, describeConfigError(err)
		}
		spec := accepted[idx]
		excluded, ok := exclusionFor(spec.record, describeRecordError(err))
		if !ok {
			return nil, describeConfigError(err)
		}
		e.excluded = append(e.excluded, excluded)
		delete(e.exits, spec.exit.ID)
		accepted = append(accepted[:idx], accepted[idx+1:]...)
	}

	for _, spec := range accepted {
		if spec.outbound != nil {
			e.handlers[spec.outbound.Tag] = struct{}{}
		}
	}

	inst, err := core.New(cfg)
	if err != nil {
		return nil, describeConfigError(err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		return nil, describeConfigError(err)
	}

	e.inst = inst
	e.rawConfig = raw
	return e, nil
}

// exclusionFor classifies a per-record build failure. Only record-specific
// kinds are isolated; anything else is a global failure the caller must see.
func exclusionFor(rec contract.Record, err error) (ExcludedExit, bool) {
	var failure *contract.Failure
	if errors.As(err, &failure) {
		switch failure.Kind {
		case contract.Unsupported, contract.ImportInvalid:
			return ExcludedExit{
				ID:     contract.ExitID(rec.ID),
				Kind:   failure.Kind,
				Reason: ReasonRecordRejected,
				Detail: failure.Message,
			}, true
		}
	}
	return ExcludedExit{}, false
}

// Excluded reports the records Start refused to build, in input order. The
// list is stable for the engine's lifetime.
func (e *Engine) Excluded() []ExcludedExit {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ExcludedExit, len(e.excluded))
	copy(out, e.excluded)
	return out
}

// Stop closes every pooled connection the engine returned, then the core. It is
// idempotent. Warm gRPC/XHTTP transports are not closed (the pinned core offers
// no hook) and are reported by WarmTransports; only a process restart clears
// them.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	inst := e.inst
	e.inst = nil
	e.mu.Unlock()

	// From here a concurrent dial that already passed the stopped check must
	// not be able to add a connection after the snapshot below.
	e.stopping.Store(true)
	e.connMu.Lock()
	list := make([]io.Closer, 0, len(e.conns))
	for c := range e.conns {
		list = append(list, c)
	}
	e.connMu.Unlock()

	var wg sync.WaitGroup
	var closeMu sync.Mutex
	var closeErr error
	for _, c := range list {
		wg.Add(1)
		go func(c io.Closer) {
			defer wg.Done()
			if err := c.Close(); err != nil {
				closeMu.Lock()
				closeErr = errors.Join(closeErr, err)
				closeMu.Unlock()
			}
		}(c)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// The closes are already in flight and will finish; report the caller's
		// deadline instead of pretending the stop completed in time.
		if inst != nil {
			_ = inst.Close()
		}
		return &contract.Failure{
			Kind:    contract.Canceled,
			Path:    contract.PathLocal,
			Op:      "engine.stop",
			Message: "the engine stop did not finish before the caller's deadline",
			Err:     ctx.Err(),
		}
	}

	if inst != nil {
		if err := inst.Close(); err != nil {
			return &contract.Failure{
				Kind:    contract.Internal,
				Path:    contract.PathLocal,
				Op:      "engine.stop",
				Message: "engine core did not close cleanly",
				Err:     err,
			}
		}
	}
	if closeErr != nil {
		return &contract.Failure{
			Kind:    contract.Internal,
			Path:    contract.PathLocal,
			Op:      "engine.stop",
			Message: "a connection did not close cleanly",
			Err:     closeErr,
		}
	}
	return nil
}

// PolicyVersion reports the version the engine fences dials against.
func (e *Engine) PolicyVersion() contract.PolicyVersion {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.version
}

// SetPolicyVersion commits a new policy generation. It never moves backwards:
// a stale value is ignored so a racing caller cannot reopen a fenced version.
func (e *Engine) SetPolicyVersion(v contract.PolicyVersion) {
	e.mu.Lock()
	if v > e.version {
		e.version = v
	}
	e.mu.Unlock()
}

// AddExit registers one record at runtime without restarting the core. A
// record whose id is already registered must not have different settings: ids
// are derived from the record identity (docs/contracts.md §5.1), so a changed
// record has a different id and this is an internal inconsistency.
func (e *Engine) AddExit(rec contract.Record) (Exit, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || e.inst == nil {
		return Exit{}, &contract.Failure{
			Kind:    contract.Stopped,
			Path:    contract.PathLocal,
			Op:      "engine.add_exit",
			Message: "engine is stopped",
		}
	}
	exitID := contract.ExitID(rec.ID)
	if key, ok := e.retired[exitID]; ok && key != rec.DedupeKey {
		// Peek only: a failed re-add must not consume the reservation and let a
		// later, different record inherit the stale handler.
		return Exit{}, &contract.Failure{
			Kind:    contract.Internal,
			Path:    contract.PathLocal,
			Exit:    exitID,
			Op:      "engine.add_exit",
			Message: "exit id was removed with different settings",
		}
	}
	if cur, ok := e.exits[exitID]; ok {
		if cur.record.DedupeKey != rec.DedupeKey {
			return Exit{}, &contract.Failure{
				Kind:    contract.Internal,
				Path:    contract.PathLocal,
				Op:      "engine.add_exit",
				Message: "exit id is already registered with different settings",
			}
		}
		return cur.exit, nil
	}
	spec, ob, err := prepareExit(rec)
	if err != nil {
		return Exit{}, err
	}
	if ob != nil {
		if _, registered := e.handlers[ob.Tag]; !registered {
			cfg, err := singleOutboundConfig(*ob)
			if err != nil {
				var f *contract.Failure
				if errors.As(err, &f) && f.Kind == contract.Internal {
					return Exit{}, err
				}
				failure := describeRecordError(err).(*contract.Failure)
				failure.Exit = spec.exit.ID
				return Exit{}, failure
			}
			if len(cfg.Outbound) != 2 {
				return Exit{}, &contract.Failure{
					Kind:    contract.Internal,
					Path:    contract.PathLocal,
					Op:      "engine.add_exit",
					Message: "generated outbound configuration is incomplete",
				}
			}
			if err := core.AddOutboundHandler(e.inst, cfg.Outbound[1]); err != nil {
				return Exit{}, &contract.Failure{
					Kind:    contract.Internal,
					Path:    contract.PathLocal,
					Exit:    spec.exit.ID,
					Op:      "engine.add_exit",
					Message: "pinned core rejected the exit handler",
					Err:     err,
				}
			}
			e.handlers[ob.Tag] = struct{}{}
		}
	}
	delete(e.retired, spec.exit.ID)
	e.exits[spec.exit.ID] = spec
	return spec.exit, nil
}

// RemoveExit makes an exit undialable. The core handler is intentionally left
// in place: RemoveHandler does not close a handler's transport (T1), and the
// stable tag may still be serving warm connections that the registry closes by
// exit id instead. A dial naming the removed exit returns PoolUnavailable. The
// tag stays reserved, so restoring the same record reuses its handler instead
// of failing on a duplicate tag.
func (e *Engine) RemoveExit(id contract.ExitID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	spec, ok := e.exits[id]
	if !ok {
		return false
	}
	delete(e.exits, id)
	e.retired[id] = spec.record.DedupeKey
	return true
}

// HasExit reports whether the tag is currently dialable.
func (e *Engine) HasExit(id contract.ExitID) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.exits[id]
	return ok
}

// TagFor returns the core tag of an exit and whether it exists. SOCKS5 exits
// have no tag; they are selected by exit id alone.
func (e *Engine) TagFor(id contract.ExitID) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	spec, ok := e.exits[id]
	if !ok {
		return "", false
	}
	return spec.exit.Tag, true
}

// WarmTransports returns the exits that may still hold core transports. It is
// empty for raw/TLS/WS exits, whose transport is the user connection itself.
func (e *Engine) WarmTransports() []WarmTransport {
	e.warmMu.Lock()
	defer e.warmMu.Unlock()
	out := make([]WarmTransport, 0, len(e.warm))
	for _, w := range e.warm {
		out = append(out, w)
	}
	return out
}

// Dial dials one destination through one named exit. It never selects another
// exit, never falls back to a direct dial, and never returns a conn without a
// proof class.
//
// Read and Write on the returned conn surface a *contract.Failure once the
// stream has failed, so a caller can record the honest kind:
//   - the session context ended -> Timeout or Canceled (we caused the latter);
//   - the core reported a transport failure -> ExitUnreachable;
//   - the stream ended before any downlink byte without attributable evidence
//     -> DestinationUnconfirmed (docs/contracts.md §2.3).
//
// A clean end of stream after data is returned as io.EOF.
//
// ctx is the session context: it must live as long as the tunnel and carry no
// dial-only deadline. The core path has no observable dial phase to bound — a
// deadline on this context would become a tunnel deadline, which §3.4 forbids.
// The SOCKS5 adapter, whose handshake really does block, applies
// contract.PoolDialDeadline when ctx has no earlier deadline.
func (e *Engine) Dial(ctx context.Context, req DialRequest) (DialResult, error) {
	if err := validateDialDestination(req.Destination); err != nil {
		return DialResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DialResult{}, ctxFailure(ctx, req.Exit, req.Destination)
	}
	if req.Exit == "" {
		return DialResult{}, poolUnavailable("no exit was selected", req.Exit, req.Destination)
	}

	e.mu.RLock()
	stopped := e.stopped
	inst := e.inst
	version := e.version
	spec, ok := e.exits[req.Exit]
	e.mu.RUnlock()

	if stopped || inst == nil {
		return DialResult{}, &contract.Failure{
			Kind:        contract.Stopped,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Destination: req.Destination,
			Message:     "engine is stopped",
		}
	}
	if !ok {
		return DialResult{}, poolUnavailable("exit is not configured", req.Exit, req.Destination)
	}
	if req.PolicyVersion < version {
		return DialResult{}, &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "engine.dial",
			Exit:        req.Exit,
			Destination: req.Destination,
			Retryable:   true,
			Message:     "policy version is stale; route the destination again",
		}
	}

	if spec.exit.Protocol == contract.ProtocolSOCKS5 {
		return e.dialSOCKS5(ctx, spec, req.Destination)
	}
	return e.dialCore(ctx, inst, spec, req.Destination)
}

// prepareExit validates one record and builds its engine handle.
func prepareExit(rec contract.Record) (*exitSpec, *coreOutbound, error) {
	if err := validateRecord(rec); err != nil {
		return nil, nil, err
	}
	spec := &exitSpec{
		record: rec,
		exit: Exit{
			ID:        contract.ExitID(rec.ID),
			Protocol:  rec.Protocol,
			Transport: rec.Transport,
		},
	}
	if rec.Protocol == contract.ProtocolSOCKS5 {
		return spec, nil, nil
	}
	ob, err := buildOutbound(rec)
	if err != nil {
		return nil, nil, err
	}
	spec.exit.Tag = ob.Tag
	spec.outbound = ob
	return spec, ob, nil
}

// singleOutboundConfig builds and parses one exit's document. It is the exact
// path AddExit uses, so Start can attribute a combined failure to a record.
func singleOutboundConfig(ob coreOutbound) (*core.Config, error) {
	raw, err := generatedConfig("warning", []coreOutbound{ob})
	if err != nil {
		return nil, err
	}
	return serial.LoadJSONConfig(bytes.NewReader(raw))
}

// attributeConfigError was replaced by the per-record retry in Start.

// track registers a live connection. It reports false when the engine is
// stopping, in which case the caller must close the connection itself.
func (e *Engine) track(c io.Closer) bool {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	if e.stopping.Load() {
		return false
	}
	e.conns[c] = struct{}{}
	return true
}

func (e *Engine) untrack(c io.Closer) {
	e.connMu.Lock()
	delete(e.conns, c)
	e.connMu.Unlock()
}

func (e *Engine) trackAdapter(c io.Closer) bool { return e.track(c) }
func (e *Engine) untrackAdapter(c io.Closer)    { e.untrack(c) }

func (e *Engine) noteWarm(spec *exitSpec) {
	if spec.exit.Tag == "" {
		return
	}
	if spec.record.Transport != contract.TransportGRPC && spec.record.Transport != contract.TransportXHTTP {
		return
	}
	e.warmMu.Lock()
	if _, ok := e.warm[spec.exit.ID]; !ok {
		e.warm[spec.exit.ID] = WarmTransport{
			Exit:      spec.exit.ID,
			Tag:       spec.exit.Tag,
			Transport: spec.record.Transport,
			Since:     time.Now(),
		}
	}
	e.warmMu.Unlock()
}
