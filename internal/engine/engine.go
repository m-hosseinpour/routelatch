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
	// Sidecar is the separate sing-box executable used only for hysteria2
	// exits. When it is nil, unhealthy, or has no handoff for a tag, those
	// exits are excluded and a dial naming one fails closed. It never affects
	// the pinned Xray path.
	Sidecar contract.SidecarRuntime
}

// Exit is the engine's per-record dialing handle. Tag is empty only for SOCKS5
// exits dialed by our own adapter (docs/contracts.md §2.1). Hysteria2 exits
// dialed through the sing-box sidecar carry a stable tag: the sidecar has its
// own outbound tag and Dial must name that one explicit exit.
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
	mu sync.RWMutex
	// syncMu serializes sidecar AddExit/RemoveExit regenerations with each
	// other so a stale snapshot cannot win. It is NOT taken by Stop or Dial:
	// e.mu is released across the potentially slow Sync so neither is blocked.
	// A registration racing a Stop cleans up the runtime it started itself.
	syncMu    sync.Mutex
	inst      *core.Instance
	exits     map[contract.ExitID]*exitSpec
	version   contract.PolicyVersion
	stopped   bool
	rawConfig []byte
	// sidecar manages hysteria2 exits. It is nil when no runtime was supplied.
	sidecar contract.SidecarRuntime
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
	// sidecar marks an exit dialed through the separate sing-box process, not
	// the pinned core. Such an exit has no core outbound/handler.
	sidecar bool
}

// ReasonRecordRejected is the stable StatusReason for a record the engine could
// not turn into an exit. The record is kept for reporting and never dialed.
const ReasonRecordRejected = "engine_record_rejected"

// ReasonSidecarUnavailable is the stable StatusReason for a hysteria2 record
// the engine refused because the separate sing-box executable was absent or
// could not be started. It is reported, never silently routed direct.
const ReasonSidecarUnavailable = "sidecar_unavailable"

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
func Start(opts Options) (_ *Engine, err error) {
	e := &Engine{
		exits:    make(map[contract.ExitID]*exitSpec),
		conns:    make(map[io.Closer]struct{}),
		warm:     make(map[contract.ExitID]WarmTransport),
		handlers: make(map[string]struct{}),
		retired:  make(map[contract.ExitID]string),
		version:  opts.PolicyVersion,
		sidecar:  opts.Sidecar,
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

	// Hysteria2 exits live only in the separate executable. There is no
	// add/remove-outbound RPC, so they are configured by a full regeneration.
	// A missing or failing sidecar excludes every such record; the pinned core
	// still serves the remaining exits and no traffic falls back to direct.
	accepted, sidecarStarted := e.prepareSidecar(accepted)
	if sidecarStarted {
		// prepareSidecar started a process before the core was configured. Every
		// failure from here until a live engine is returned must stop it again:
		// no authenticated loopback listener or its 0600 credential config may
		// survive a failed Start, and retries must not accumulate processes.
		// Traffic stays fail-closed because no engine is published on failure.
		defer func() {
			if err != nil {
				ctx, cancel := context.WithTimeout(context.Background(), sidecarSyncTimeout)
				defer cancel()
				_ = e.sidecar.Stop(ctx)
			}
		}()
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

// sidecarSyncTimeout bounds a process start so a stuck executable cannot hold
// service startup indefinitely. The runtime applies the same bound itself.
const sidecarSyncTimeout = 15 * time.Second

// prepareSidecar starts (or refuses) the second executable for the hysteria2
// exits in accepted. It returns the list the pinned core must still serve and
// whether this call actually started the runtime, so Start can guarantee a
// Stop on every later failure path.
func (e *Engine) prepareSidecar(accepted []*exitSpec) ([]*exitSpec, bool) {
	kept := accepted[:0]
	hy := make([]*exitSpec, 0, len(accepted))
	for _, spec := range accepted {
		if spec.sidecar {
			hy = append(hy, spec)
		} else {
			kept = append(kept, spec)
		}
	}
	if len(hy) == 0 {
		return kept, false
	}
	unavailable := e.sidecar == nil
	if !unavailable {
		exits := make([]contract.SidecarExit, 0, len(hy))
		for _, spec := range hy {
			exits = append(exits, sidecarExitFor(spec.record))
		}
		ctx, cancel := context.WithTimeout(context.Background(), sidecarSyncTimeout)
		err := e.sidecar.Sync(ctx, exits)
		cancel()
		unavailable = err != nil
		// A failed regeneration may have withdrawn every handoff already; the
		// engine treats all sidecar exits as unusable, not just one.
	}
	if unavailable {
		for _, spec := range hy {
			e.excluded = append(e.excluded, ExcludedExit{
				ID:     spec.exit.ID,
				Kind:   contract.Unsupported,
				Reason: ReasonSidecarUnavailable,
				Detail: "the sing-box sidecar is unavailable; the exit was not dialed",
			})
			delete(e.exits, spec.exit.ID)
		}
		return kept, false
	}
	return append(kept, hy...), true
}

// sidecarExitFor projects one typed record into the minimal, trusted view the
// sidecar needs. Verification uses an explicit name when one was imported and
// otherwise the exit server; it is never disabled.
//
// The final fallback to rec.Server (which may be an IP literal) is deliberate.
// In the pinned sing-box v1.14.4 schema (option/tls.go) the hysteria2 outbound's
// tls.server_name is the SNI/certificate-verification name and tls.insecure is
// a separate flag RouteLatch always sets false; an empty server_name makes
// sing-box default to the server address. Setting it explicitly to rec.Server
// therefore matches that default and never weakens verification: a server
// whose certificate lacks a matching IP SAN fails closed, and a domain
// certificate is reached by importing sni=, not by disabling verification.
func sidecarExitFor(rec contract.Record) contract.SidecarExit {
	serverName := rec.VerifyPeerCertByName
	if serverName == "" {
		serverName = rec.SNI
	}
	if serverName == "" {
		serverName = rec.Server
	}
	return contract.SidecarExit{
		Tag:          tagFor(rec.ID),
		Server:       rec.Server,
		Port:         rec.Port,
		Password:     rec.Credentials.Password,
		ServerName:   serverName,
		UpMbps:       rec.Hysteria2UpMbps,
		DownMbps:     rec.Hysteria2DownMbps,
		Obfs:         rec.Hysteria2Obfs,
		ObfsPassword: rec.Hysteria2ObfsPassword,
	}
}

// sidecarExitsLocked builds the sidecar exit list from a consistent view of
// the exit table. It must be called with e.mu held (read or write).
func (e *Engine) sidecarExitsLocked() []contract.SidecarExit {
	exits := make([]contract.SidecarExit, 0, len(e.exits))
	for _, spec := range e.exits {
		if spec.sidecar {
			exits = append(exits, sidecarExitFor(spec.record))
		}
	}
	return exits
}

// syncSidecar regenerates the runtime from a prebuilt snapshot. It must NOT
// hold e.mu: Sync can take up to sidecarSyncTimeout and would otherwise block
// every dial and Stop. It returns the runtime it used so a caller that loses a
// race with Stop can stop exactly that process. Callers hold e.syncMu, which
// serializes regenerations with each other; the runtime pointer is read under
// e.mu so a concurrent Stop cannot cause a data race.
func (e *Engine) syncSidecar(ctx context.Context, exits []contract.SidecarExit) (contract.SidecarRuntime, error) {
	e.mu.RLock()
	sidecar := e.sidecar
	e.mu.RUnlock()
	if sidecar == nil {
		if len(exits) > 0 {
			return nil, &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.sidecar",
				Message: "the sing-box sidecar is unavailable; the exit was not added",
			}
		}
		return nil, nil
	}
	return sidecar, sidecar.Sync(ctx, exits)
}

// stopIfStopped cleans up a runtime used by a Sync that completed after a
// concurrent Stop withdrew the engine's runtime. The engine is already
// fail-closed (Stop set stopped and Dial sees no runtime); stopping the runtime
// here ensures no process outlives the in-flight registration.
func (e *Engine) stopIfStopped(sidecar contract.SidecarRuntime) bool {
	e.mu.RLock()
	stopped := e.stopped
	e.mu.RUnlock()
	if !stopped || sidecar == nil {
		return stopped
	}
	ctx, cancel := context.WithTimeout(context.Background(), sidecarSyncTimeout)
	defer cancel()
	_ = sidecar.Stop(ctx)
	return true
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
	sidecar := e.sidecar
	e.sidecar = nil
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
			if sidecar != nil {
				_ = sidecar.Stop(ctx)
			}
			return &contract.Failure{
				Kind:    contract.Internal,
				Path:    contract.PathLocal,
				Op:      "engine.stop",
				Message: "engine core did not close cleanly",
				Err:     err,
			}
		}
	}
	// The second executable is stopped through the same narrow interface; no
	// handoff survives a stopped engine.
	if sidecar != nil {
		if err := sidecar.Stop(ctx); err != nil {
			return err
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
	// syncMu serializes this regeneration with other regenerations. e.mu is
	// released by prepareAddExit before the slow Sync, so dials, status reads
	// and Stop are never blocked by a restart; a racing Stop is cleaned up via
	// stopIfStopped below.
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	spec, snapshot, needsSync, err := e.prepareAddExit(rec)
	if err != nil {
		return Exit{}, err
	}
	if !needsSync {
		return spec.exit, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sidecarSyncTimeout)
	sidecar, err := e.syncSidecar(ctx, snapshot)
	cancel()
	if err != nil {
		// No half-registered exit: the runtime never published a handoff, so
		// remove the spec committed by prepareAddExit. syncMu keeps this
		// rollback atomic with respect to other regenerations.
		e.mu.Lock()
		delete(e.exits, spec.exit.ID)
		e.mu.Unlock()
		var failure *contract.Failure
		if errors.As(err, &failure) {
			failure.Exit = spec.exit.ID
			return Exit{}, failure
		}
		return Exit{}, err
	}
	if e.stopIfStopped(sidecar) {
		// A Stop ran while Sync was in flight. Its runtime is stopped above and
		// the exit must not be handed out by the now-stopped engine.
		e.mu.Lock()
		delete(e.exits, spec.exit.ID)
		e.mu.Unlock()
		return Exit{}, &contract.Failure{
			Kind:    contract.Stopped,
			Path:    contract.PathLocal,
			Exit:    spec.exit.ID,
			Op:      "engine.add_exit",
			Message: "engine is stopped",
		}
	}
	return spec.exit, nil
}

// prepareAddExit performs the quick, non-blocking part of AddExit under e.mu:
// checks, core handler registration, and committing the spec to the exit table.
// needsSync is true only when a new sidecar exit was committed; an already
// registered exit or a core-backed one returns false and is not regenerated.
// The snapshot of sidecar exits is built under the same lock so the caller can
// regenerate the runtime without holding e.mu.
func (e *Engine) prepareAddExit(rec contract.Record) (*exitSpec, []contract.SidecarExit, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || e.inst == nil {
		return nil, nil, false, &contract.Failure{
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
		return nil, nil, false, &contract.Failure{
			Kind:    contract.Internal,
			Path:    contract.PathLocal,
			Exit:    exitID,
			Op:      "engine.add_exit",
			Message: "exit id was removed with different settings",
		}
	}
	if cur, ok := e.exits[exitID]; ok {
		if cur.record.DedupeKey != rec.DedupeKey {
			return nil, nil, false, &contract.Failure{
				Kind:    contract.Internal,
				Path:    contract.PathLocal,
				Op:      "engine.add_exit",
				Message: "exit id is already registered with different settings",
			}
		}
		return cur, nil, false, nil
	}
	spec, ob, err := prepareExit(rec)
	if err != nil {
		return nil, nil, false, err
	}
	if ob != nil {
		if _, registered := e.handlers[ob.Tag]; !registered {
			cfg, err := singleOutboundConfig(*ob)
			if err != nil {
				var f *contract.Failure
				if errors.As(err, &f) && f.Kind == contract.Internal {
					return nil, nil, false, err
				}
				failure := describeRecordError(err).(*contract.Failure)
				failure.Exit = spec.exit.ID
				return nil, nil, false, failure
			}
			if len(cfg.Outbound) != 2 {
				return nil, nil, false, &contract.Failure{
					Kind:    contract.Internal,
					Path:    contract.PathLocal,
					Op:      "engine.add_exit",
					Message: "generated outbound configuration is incomplete",
				}
			}
			if err := core.AddOutboundHandler(e.inst, cfg.Outbound[1]); err != nil {
				return nil, nil, false, &contract.Failure{
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
	var snapshot []contract.SidecarExit
	if spec.sidecar {
		snapshot = e.sidecarExitsLocked()
	}
	return spec, snapshot, spec.sidecar, nil
}

// RemoveExit makes an exit undialable. The core handler is intentionally left
// in place: RemoveHandler does not close a handler's transport (T1), and the
// stable tag may still be serving warm connections that the registry closes by
// exit id instead. A dial naming the removed exit returns PoolUnavailable. The
// tag stays reserved, so restoring the same record reuses its handler instead
// of failing on a duplicate tag.
func (e *Engine) RemoveExit(id contract.ExitID) bool {
	// syncMu serializes this regeneration with other regenerations; e.mu is
	// released by prepareRemoveExit before the slow Sync, so dials and Stop
	// are never blocked. A Stop racing the Sync is cleaned up below.
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	snapshot, sidecar, ok := e.prepareRemoveExit(id)
	if !ok {
		return false
	}
	if sidecar {
		// There is no remove-outbound RPC: a full regeneration restarts the
		// process and drops connections to the removed exit. The call has no
		// error return, so a failed regeneration leaves every handoff withdrawn
		// (fail closed) rather than reviving the removed exit.
		ctx, cancel := context.WithTimeout(context.Background(), sidecarSyncTimeout)
		runtime, _ := e.syncSidecar(ctx, snapshot)
		cancel()
		// If a Stop landed while Sync was in flight, stop the process this
		// regeneration started so it does not outlive the stopped engine.
		e.stopIfStopped(runtime)
	}
	return true
}

// prepareRemoveExit deletes the exit from the table under e.mu and returns a
// snapshot of the remaining sidecar exits, so the caller can regenerate the
// runtime without holding e.mu.
func (e *Engine) prepareRemoveExit(id contract.ExitID) ([]contract.SidecarExit, bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	spec, ok := e.exits[id]
	if !ok {
		return nil, false, false
	}
	delete(e.exits, id)
	e.retired[id] = spec.record.DedupeKey
	if !spec.sidecar {
		return nil, false, true
	}
	return e.sidecarExitsLocked(), true, true
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
	sidecar := e.sidecar
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

	if spec.sidecar {
		// Fail closed: a missing runtime or a missing tag is never a cue to
		// try another exit or to dial the destination directly.
		if sidecar == nil {
			return DialResult{}, poolUnavailable("the sing-box sidecar is unavailable", req.Exit, req.Destination)
		}
		handoff, ok := sidecar.Handoff(spec.exit.Tag)
		if !ok {
			return DialResult{}, poolUnavailable("the sing-box sidecar has no handoff for this exit", req.Exit, req.Destination)
		}
		return e.dialSidecar(ctx, spec, req.Destination, handoff)
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
		sidecar: rec.Protocol.RequiresSidecar(),
	}
	if rec.Protocol == contract.ProtocolSOCKS5 {
		return spec, nil, nil
	}
	if spec.sidecar {
		// The sidecar has its own outbound tag; the pinned core has no handler
		// for this exit. The tag still names one explicit exit in Dial.
		spec.exit.Tag = tagFor(rec.ID)
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
