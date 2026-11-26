package control

import (
	"context"
	"errors"

	"routelatch/internal/contract"
)

// This file defines the R16-B TUN management seam. The real helper, route and
// engine integration arrive in R16-C/D; until then a nil runtime reports off /
// unavailable and refuses enable with unsupported instead of pretending TUN is
// active (docs/contracts.md §10.3).

// tunStatus returns the additive status.tun object. Desired comes from config;
// every other field comes from the runtime (or a conservative default when no
// runtime is wired). It contains no destination, domain or flow data (D21).
func (s *Service) tunStatus(ctx context.Context, snap contract.Snapshot) contract.TunStatus {
	st := contract.TunStatus{
		Desired: snap.Config.TunEnabled,
		State:   contract.TunOff,
		DNS:     contract.TunDNSDirect,
		UDP:     contract.TunUDPBlocked,
		Helper:  contract.TunHelperMissing,
	}
	if s.tun != nil {
		got, err := s.tun.Status(ctx)
		if err == nil {
			got.Desired = snap.Config.TunEnabled
			return normalizeTunStatus(got)
		}
		st.State = contract.TunUnavailable
		st.LastError = contract.TunReasonServiceUnavailable
		st.Detail = "TUN service did not report its state"
		return st
	}
	if snap.Config.TunEnabled {
		// No runtime is wired in this build. The preference is still real, so
		// report that TUN was requested and cannot be provided.
		st.State = contract.TunUnavailable
		st.LastError = contract.TunReasonHelperUnavailable
		st.Detail = "TUN is not available in this build"
	}
	return st
}

// normalizeTunStatus keeps the wire enums inside the contract even if a future
// runtime returns a zero value or an unknown state.
func normalizeTunStatus(st contract.TunStatus) contract.TunStatus {
	switch st.State {
	case contract.TunOff, contract.TunStarting, contract.TunOn, contract.TunStopping,
		contract.TunStateError, contract.TunUnavailable:
	default:
		st.State = contract.TunUnavailable
		if st.LastError == "" {
			st.LastError = contract.TunReasonServiceUnavailable
		}
	}
	// First-phase DNS-direct/UDP-blocked are fixed values, not runtime
	// choices (§10.2, §10.5/D30). Pin them so a defect cannot report a
	// resolver, address or arbitrary text through these fields.
	if st.DNS != contract.TunDNSDirect {
		st.DNS = contract.TunDNSDirect
	}
	if st.UDP != contract.TunUDPBlocked {
		st.UDP = contract.TunUDPBlocked
	}
	switch st.Helper {
	case contract.TunHelperInstalled, contract.TunHelperMissing, contract.TunHelperIncompatible:
	default:
		st.Helper = contract.TunHelperMissing
	}
	if st.LastError != "" && !validTunReason(st.LastError) {
		// Keep arbitrary runtime text out of status. An unknown reason is
		// dropped rather than shown; the fixed Detail carries the safe hint.
		st.LastError = ""
	}
	st.Interface = SanitizeTerminal(st.Interface)
	st.Detail = SanitizeTerminal(st.Detail)
	return st
}

func validTunReason(reason contract.TunReason) bool {
	for _, known := range contract.TunReasons() {
		if reason == known {
			return true
		}
	}
	return false
}

func (s *Service) tunStatusOp(ctx context.Context) (contract.TunStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.tunStatus(ctx, s.Snapshot()), nil
}

func (s *Service) tunEnable(ctx context.Context) (contract.TunStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snap := s.Snapshot()
	if s.stopped || snap.State.Mode == contract.ModeStopped {
		return s.tunStatus(ctx, snap), &OpError{
			Kind:    contract.Stopped,
			Message: "service is stopped; choose a mode before enabling TUN",
		}
	}
	// Persist the explicit wish before attempting the runtime. Only `tun off`
	// clears it, so a missing helper stays visible after a restart instead of
	// silently forgetting that the user asked for coverage (§10.1–10.2).
	if !snap.Config.TunEnabled {
		var err error
		snap, err = s.setTunDesiredLocked(ctx, true)
		if err != nil {
			return contract.TunStatus{}, tunPersistError("tun.enable", err)
		}
	}
	if s.tun == nil {
		return s.tunStatus(ctx, snap), tunUnsupported(contract.TunReasonHelperUnavailable)
	}
	if err := s.tun.Enable(ctx); err != nil {
		return s.tunStatus(ctx, snap), tunOpError("tun.enable", err, true)
	}
	return s.tunStatus(ctx, snap), nil
}

func (s *Service) tunDisable(ctx context.Context) (contract.TunStatus, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snap := s.Snapshot()
	// Disable is allowed in every mode, including stopped.
	if s.tun != nil {
		if err := s.tun.Disable(ctx); err != nil {
			// §10.3 allows only internal for tun.disable: it is always
			// permitted, so an unexpected runtime outcome must not become
			// state_locked/unsupported/stopped.
			return s.tunStatus(ctx, snap), tunOpError("tun.disable", err, false)
		}
	}
	if snap.Config.TunEnabled {
		var err error
		snap, err = s.setTunDesiredLocked(ctx, false)
		if err != nil {
			return contract.TunStatus{}, tunPersistError("tun.disable", err)
		}
	}
	return s.tunStatus(ctx, snap), nil
}

// stopTunForModeStopped closes the overlay before a mode-stopped change touches
// listeners or the engine. It does not clear the persisted preference; only
// `tun off` does (§10.1). A runtime failure aborts the stop (fail-closed).
// Callers hold opMu.
//
// Restore can also commit mode=stopped (ops_props.go). R16-B deliberately does
// not change that path; closing TUN around restore belongs to the R16-C/D
// lifecycle work and must not be assumed here.
func (s *Service) stopTunForModeStopped(ctx context.Context) error {
	if s.tun == nil {
		return nil
	}
	// Skip Disable only when the runtime positively reports off. `unavailable`,
	// `error` and a failed status probe are not proof that no overlay exists,
	// so they take the fail-closed Disable path; Disable must be idempotent.
	if st, err := s.tun.Status(ctx); err == nil && st.State == contract.TunOff {
		return nil
	}
	if err := s.tun.Disable(ctx); err != nil {
		return tunOpError("control.mode", err, true)
	}
	return nil
}

// setTunDesiredLocked persists the preference without touching state.json.
// Callers hold opMu; pubMu orders the store write with snapshot publication.
func (s *Service) setTunDesiredLocked(ctx context.Context, enabled bool) (contract.Snapshot, error) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap, err := s.store.MutateConfig(ctx, func(cfg *contract.Config) error {
		cfg.TunEnabled = enabled
		return nil
	})
	if err != nil {
		return contract.Snapshot{}, err
	}
	s.snap.Store(&snap)
	return snap, nil
}

// tunWarnings returns only warnings that are true for the reported state. In
// particular, DNS-direct/UDP-blocked warnings appear only while TUN is on, and
// a stopped service is intentionally off, not unavailable (§10.1–10.2).
func tunWarnings(tun *contract.TunStatus, mode contract.Mode, desktopApplied bool) []string {
	if tun == nil || mode == contract.ModeStopped {
		return nil
	}
	var out []string
	if tun.State == contract.TunOn {
		out = append(out, string(contract.TunWarningDNSDirect), string(contract.TunWarningUDPBlocked))
	}
	if tun.Desired && tun.Helper == contract.TunHelperMissing &&
		tun.LastError != contract.TunReasonServiceUnavailable {
		// A failed status call cannot prove the helper is missing; only a
		// reported missing helper is actionable.
		out = append(out, string(contract.TunWarningHelperMissing))
	}
	if tun.Desired && (tun.State == contract.TunOff || tun.State == contract.TunUnavailable || tun.State == contract.TunStateError) {
		out = append(out, string(contract.TunWarningServiceUnavailable))
	}
	if tun.State == contract.TunOn && desktopApplied {
		out = append(out, string(contract.TunWarningDesktopProxyOverlap))
	}
	if tun.State == contract.TunOn && mode == contract.ModeDirectAll {
		out = append(out, string(contract.TunWarningAllDirect))
	}
	if tun.LastError == contract.TunReasonConflict {
		out = append(out, string(contract.TunWarningConflict))
	}
	return out
}

func tunUnsupported(reason contract.TunReason) error {
	return &OpError{
		Kind:    contract.Unsupported,
		Message: tunReasonMessage(reason),
		Detail:  map[string]any{"reason": string(reason)},
	}
}

// tunOpError maps the runtime's typed failure onto the frozen ErrorKind set.
// Messages are fixed per reason: a runtime message is never passed through, so
// a defect cannot leak a destination into an error envelope. allowConflict is
// false for tun.disable, whose documented errors are internal only.
func tunOpError(op string, err error, allowConflict bool) error {
	var te *contract.TunError
	if errors.As(err, &te) {
		reason := te.Reason
		if !validTunReason(reason) {
			// The runtime's reason is untrusted text. Unknown or empty values
			// are dropped; tunReasonMessage then uses the fixed generic text.
			reason = ""
		}
		kind := te.Kind
		switch kind {
		case contract.Unsupported, contract.Stopped, contract.Internal:
		case contract.StateLocked:
			// Allowed only for ops that document a conflict.
		default:
			kind = contract.Internal
		}
		if !allowConflict {
			// tun.disable documents only internal (§10.3).
			kind = contract.Internal
		}
		// Unsupported and state_locked are documented only with a stable
		// reason. A runtime that gives neither is a defect, not a refusal this
		// layer can name, so it becomes the documented internal failure.
		if reason == "" && (kind == contract.Unsupported || kind == contract.StateLocked) {
			kind = contract.Internal
		}
		detail := map[string]any{}
		if reason != "" {
			detail["reason"] = string(reason)
		}
		return &OpError{Kind: kind, Message: tunReasonMessage(reason), Detail: detail, Cause: te}
	}
	return &contract.Failure{
		Kind: contract.Internal, Path: contract.PathLocal, Op: op,
		Message: "TUN operation failed without a usable reason",
	}
}

// tunPersistError maps a config-store failure onto the frozen internal kind.
// §10.3 documents only internal for tun.disable and no new kind for tun.enable,
// so a store error must not surface as corrupt/state_locked/etc. The store
// error stays the cause; the envelope carries fixed text.
func tunPersistError(op string, err error) error {
	return &contract.Failure{
		Kind: contract.Internal, Path: contract.PathLocal, Op: op,
		Message: "the TUN preference could not be saved",
		Err:     err,
	}
}

func tunReasonMessage(reason contract.TunReason) string {
	switch reason {
	case contract.TunReasonHelperUnavailable:
		return "TUN helper is not installed"
	case contract.TunReasonPlatformUnsupported:
		return "TUN is not supported on this platform or kernel"
	case contract.TunReasonConflict:
		return "another TUN instance or helper owns the interface"
	case contract.TunReasonServiceUnavailable:
		return "the TUN service is not available"
	default:
		return "TUN operation failed"
	}
}
