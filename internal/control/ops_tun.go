package control

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"

	"routelatch/internal/contract"
	"routelatch/internal/egress"
)

// This file owns the TUN management seam and the engine lifecycle around it.
// A nil runtime keeps the honest R16-B fallback (off/unavailable, enable
// unsupported); production injects the internal/tun client, which enables and
// disables the separate service (docs/contracts.md §10.1–§10.6).

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
			status := normalizeTunStatus(got)
			if status.State == contract.TunOn && !s.tunTargetMatches(status) {
				// A live service pointing at a different loopback listener is a
				// conflict, not a silent retarget: live flows must not be
				// switched without a disable/enable (§10.3, §10.6).
				status.LastError = contract.TunReasonConflict
				status.Detail = "TUN targets a different SOCKS listener; disable and enable coverage"
			}
			if status.State == contract.TunOn && (!snap.Config.TunEnabled || snap.State.Mode == contract.ModeStopped) {
				// Crash-left coverage while the user disabled it (or while the
				// service is stopped) is never a healthy "on": say so.
				status.LastError = contract.TunReasonConflict
				status.Detail = "TUN coverage is on although coverage is not requested; stop or disable it"
			} else if !snap.Config.TunEnabled && s.tunCoverageUncertain && status.State != contract.TunOff {
				status.LastError = contract.TunReasonConflict
				status.Detail = "TUN coverage could not be verified off; the engine keeps its own dials outside it"
			}
			return s.applyTunBypassTruth(status, snap)
		}
		if !snap.Config.TunEnabled {
			if s.tunCoverageUncertain {
				// The helper is unreachable and the startup could not prove that
				// crash-left coverage is gone. Report that instead of a quiet off.
				st.State = contract.TunUnavailable
				st.LastError = contract.TunReasonConflict
				st.Detail = "the TUN helper did not answer; coverage could not be verified off"
				return st
			}
			// Nothing was requested and the runtime is unreachable. Reporting a
			// hard error for every ordinary process start would turn a missing
			// helper into a noisy failure; coverage is off and the warning is
			// actionable only after a request.
			return st
		}
		st.State = contract.TunUnavailable
		st.LastError = contract.TunReasonServiceUnavailable
		st.Detail = "TUN service did not report its state"
		return s.applyTunBypassTruth(st, snap)
	}
	if snap.Config.TunEnabled {
		// No runtime is wired in this build. The preference is still real, so
		// report that TUN was requested and cannot be provided.
		st.State = contract.TunUnavailable
		st.LastError = contract.TunReasonHelperUnavailable
		st.Detail = "TUN is not available in this build"
	}
	return s.applyTunBypassTruth(st, snap)
}

// applyTunBypassTruth surfaces a failed engine-egress bypass when coverage was
// requested but is not on. It is a stable platform reason and fixed detail; no
// interface or error text reaches status.
func (s *Service) applyTunBypassTruth(st contract.TunStatus, snap contract.Snapshot) contract.TunStatus {
	if snap.Config.TunEnabled && snap.State.Mode != contract.ModeStopped &&
		st.State != contract.TunOn && st.LastError == "" && s.tunBypassReason != "" {
		st.LastError = s.tunBypassReason
		switch s.tunBypassReason {
		case contract.TunReasonConflict:
			st.Detail = "TUN could not be retargeted to this listener; disable and re-enable coverage"
		case contract.TunReasonSidecarLoopRisk:
			st.Detail = "the sidecar cannot bind to the physical interface; coverage stays off"
		case contract.TunReasonHelperUnavailable:
			st.Detail = "the TUN helper is not installed or not usable"
		default:
			st.Detail = "the engine cannot keep its own dials outside TUN safely"
		}
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
	st.SocksAddress = sanitizeTunTarget(st.SocksAddress)
	return st
}

// sanitizeTunTarget keeps only a loopback IP-literal target. The field is a
// listener, not a destination; anything else is dropped rather than shown.
func sanitizeTunTarget(address string) string {
	if address == "" {
		return ""
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return ""
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return ""
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10))
}

// sameLoopbackTarget compares two normalized loopback host:port values.
func sameLoopbackTarget(a, b string) bool {
	an := sanitizeTunTarget(a)
	bn := sanitizeTunTarget(b)
	if an == "" || bn == "" {
		return an == bn
	}
	ah, ap, _ := net.SplitHostPort(an)
	bh, bp, _ := net.SplitHostPort(bn)
	aip, _ := netip.ParseAddr(ah)
	bip, _ := netip.ParseAddr(bh)
	return ap == bp && aip.Unmap() == bip.Unmap()
}

// tunTargetMatches reports whether the reported target is the current listener
// or unknown. An empty target is not proof of a mismatch, so it stays usable.
func (s *Service) tunTargetMatches(st contract.TunStatus) bool {
	if st.SocksAddress == "" {
		return true
	}
	return sameLoopbackTarget(st.SocksAddress, s.currentSocksAddress())
}

func (s *Service) currentSocksAddress() string {
	if s.listenersOpen && s.socksAddr != "" {
		return s.socksAddr
	}
	return s.socksListen
}

func validTunReason(reason contract.TunReason) bool {
	for _, known := range contract.TunReasons() {
		if reason == known {
			return true
		}
	}
	return false
}

// tunReasonFromError recovers the stable published reason from a startup
// failure so status/doctor keep the documented cause instead of degrading to a
// generic "service unavailable". An untyped failure is the service being
// unreachable, not proof that the helper is missing.
func tunReasonFromError(err error) contract.TunReason {
	var opErr *OpError
	if errors.As(err, &opErr) {
		if v, ok := opErr.Detail["reason"].(string); ok {
			if reason := contract.TunReason(v); validTunReason(reason) {
				return reason
			}
		}
	}
	var te *contract.TunError
	if errors.As(err, &te) && validTunReason(te.Reason) {
		return te.Reason
	}
	return contract.TunReasonServiceUnavailable
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
	// Engine egress must be configured before coverage can capture a dial. A
	// failed kernel/interface/controller check is unsupported, never a loop.
	if err := s.prepareTunBypass(ctx); err != nil {
		return s.tunStatus(ctx, snap), err
	}
	pre, statusErr := s.tun.Status(ctx)
	if statusErr == nil && pre.State == contract.TunOn && !s.tunTargetMatches(pre) {
		// A changed SOCKS listener requires an explicit disable/re-enable; the
		// service must not retarget live flows silently (§10.3).
		if err := s.tun.Disable(ctx); err != nil {
			s.clearTunBypassIfOff(ctx)
			return s.tunStatus(ctx, snap), tunOpError("tun.enable", err, true)
		}
	}
	if err := s.tun.Enable(ctx); err != nil {
		s.clearTunBypassIfOff(ctx)
		return s.tunStatus(ctx, snap), tunOpError("tun.enable", err, true)
	}
	// Verify the target after enable. A service configured for an older
	// listener must not stay on while this engine serves a different one.
	if st, err := s.tun.Status(ctx); err == nil && st.State == contract.TunOn && !s.tunTargetMatches(st) {
		_ = s.tun.Disable(ctx)
		s.clearTunBypassIfOff(ctx)
		return s.tunStatus(ctx, snap), &OpError{
			Kind:    contract.StateLocked,
			Message: tunReasonMessage(contract.TunReasonConflict),
			Detail:  map[string]any{"reason": string(contract.TunReasonConflict)},
		}
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
	s.clearTunBypass()
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
// Callers hold opMu. Restore also calls it before committing a stopped backup.
func (s *Service) stopTunForModeStopped(ctx context.Context) error {
	if s.tun == nil {
		s.clearTunBypass()
		return nil
	}
	// Skip Disable only when the runtime positively reports off, or when the
	// runtime is provably unavailable because no installed helper/socket can
	// hold an overlay. `unavailable`, `error` and a generic failed status probe
	// are not proof that no overlay exists, so they take the fail-closed
	// Disable path; Disable must be idempotent (§10.1, §10.2).
	if st, err := s.tun.Status(ctx); err == nil && st.State == contract.TunOff {
		s.clearTunBypass()
		return nil
	} else if err != nil && tunRuntimeMissing(err) {
		s.clearTunBypass()
		return nil
	}
	if err := s.tun.Disable(ctx); err != nil {
		return tunOpError("control.mode", err, true)
	}
	s.clearTunBypass()
	return nil
}

// prepareTunBypass validates the kernel/interface, registers the one dial
// controller and activates it before TUN starts. The controller is registered
// once per process and only binds while active, so disabling coverage leaves
// ordinary dials untouched (§10.6).
func (s *Service) prepareTunBypass(ctx context.Context) error {
	if s.bypass == nil {
		s.bypass = egress.NewController()
	}
	s.tunBypassReason = ""
	if err := s.bindingCheck(); err != nil {
		s.tunBypassReason = contract.TunReasonPlatformUnsupported
		return tunUnsupported(contract.TunReasonPlatformUnsupported)
	}
	iface, err := s.discoverInterface()
	if err != nil || iface == "" {
		s.tunBypassReason = contract.TunReasonPlatformUnsupported
		return tunUnsupported(contract.TunReasonPlatformUnsupported)
	}
	if err := egress.RegisterXray(s.bypass); err != nil {
		s.tunBypassReason = contract.TunReasonPlatformUnsupported
		return tunUnsupported(contract.TunReasonPlatformUnsupported)
	}
	if err := s.bypass.Activate(iface); err != nil {
		s.tunBypassReason = contract.TunReasonPlatformUnsupported
		return tunUnsupported(contract.TunReasonPlatformUnsupported)
	}
	if err := s.setSidecarDefaultInterface(iface); err != nil {
		s.bypass.Deactivate()
		// The stable published reason for a sidecar that cannot honor the
		// physical interface is sidecar_loop_risk (§10.6): enabling coverage
		// would loop hysteria2 exits through TUN.
		s.tunBypassReason = contract.TunReasonSidecarLoopRisk
		s.logger.Logf(levelWarning, "tun.sidecar_loop_risk", "TUN coverage stays off: the sidecar cannot bind to the physical interface")
		return &OpError{
			Kind:    contract.Unsupported,
			Message: "the sidecar cannot bind to the physical interface; hysteria2 exits would loop through TUN",
			Detail:  map[string]any{"reason": string(contract.TunReasonSidecarLoopRisk)},
		}
	}
	s.logger.Logf(levelInfo, "tun.bypass", "interface=%s", iface)
	return nil
}

// clearTunBypassIfOff deactivates the bypass only when the runtime positively
// reports off. If the state is unknown or still on, keeping the binding is
// harmless (dials still work) and avoids a loop, so uncertainty stays
// fail-closed. It reports whether coverage is now proven off.
func (s *Service) clearTunBypassIfOff(ctx context.Context) bool {
	if s.tun != nil {
		st, err := s.tun.Status(ctx)
		if err == nil {
			if st.State != contract.TunOff {
				return false
			}
		} else if !tunRuntimeMissing(err) {
			return false
		}
	}
	s.clearTunBypass()
	return true
}

// clearTunBypass stops binding new dials. It is safe when no coverage was ever
// active; the sidecar is re-synced only when it has sidecar exits.
func (s *Service) clearTunBypass() {
	if s.bypass != nil {
		s.bypass.Deactivate()
	}
	s.tunCoverageUncertain = false
	if err := s.setSidecarDefaultInterface(""); err != nil {
		s.logger.Logf(levelWarning, "tun.bypass_clear", "the sidecar interface could not be cleared: %s", err.Error())
	}
}

// setSidecarDefaultInterface applies route.default_interface to the sidecar
// runtime. The value can only come from generated options, never an import
// (§10.6). A runtime that cannot express it is refused while hysteria2 exits
// exist, so those exits never loop through TUN.
func (s *Service) setSidecarDefaultInterface(name string) error {
	if s.sidecar == nil || !s.hasSidecarExits() {
		return nil
	}
	if setter, ok := s.sidecar.(interface{ SetDefaultInterface(string) error }); ok {
		return setter.SetDefaultInterface(name)
	}
	if name == "" {
		return nil
	}
	return &contract.Failure{
		Kind: contract.Unsupported, Path: contract.PathLocal, Op: "control.tun",
		Message: "the sidecar cannot bind to the physical interface; hysteria2 exits would loop through TUN",
	}
}

func (s *Service) hasSidecarExits() bool {
	for _, rec := range s.Snapshot().State.Records {
		if rec.Protocol.RequiresSidecar() {
			return true
		}
	}
	return false
}

// startTunCoverageLocked brings coverage up after the bypass is active and
// before traffic listeners. A runtime failure is returned for logging; callers
// keep listener readiness independent of it (§10.2).
func (s *Service) startTunCoverageLocked(ctx context.Context) error {
	snap := s.Snapshot()
	if !snap.Config.TunEnabled || s.stopped || snap.State.Mode == contract.ModeStopped || s.tun == nil {
		return nil
	}
	pre, statusErr := s.tun.Status(ctx)
	if statusErr == nil && pre.State == contract.TunOn {
		if s.tunTargetMatches(pre) {
			s.logger.Logf(levelInfo, "tun.adopt", "existing coverage is healthy")
			return nil
		}
		// Crash-left coverage points at another listener. It must be removed
		// before a new target can be requested; never retarget silently.
		if err := s.tun.Disable(ctx); err != nil {
			return tunOpError("control.tun_adopt", err, true)
		}
	}
	if err := s.tun.Enable(ctx); err != nil {
		return tunOpError("control.tun_start", err, true)
	}
	if st, err := s.tun.Status(ctx); err == nil && st.State == contract.TunOn && !s.tunTargetMatches(st) {
		_ = s.tun.Disable(ctx)
		return &OpError{
			Kind:    contract.StateLocked,
			Message: tunReasonMessage(contract.TunReasonConflict),
			Detail:  map[string]any{"reason": string(contract.TunReasonConflict)},
		}
	}
	return nil
}

// reconcileUnwantedTun is the startup best-effort cleanup when coverage was not
// requested (or the bypass needed to start it could not be prepared). A missing
// helper is not a reason to fail the proxy: it can hold no overlay. Any other
// state is closed when possible; it reports true when coverage might still be
// active, so the caller keeps the egress bypass active or aborts only if that
// bypass cannot be established (§10.1, §10.2).
func (s *Service) reconcileUnwantedTun(ctx context.Context) bool {
	if s.tun == nil {
		return false
	}
	st, err := s.tun.Status(ctx)
	if err == nil && st.State == contract.TunOff {
		return false
	}
	if err != nil && tunRuntimeMissing(err) {
		return false
	}
	if derr := s.tun.Disable(ctx); derr != nil {
		s.tunBypassReason = contract.TunReasonConflict
		s.logger.Logf(levelError, "tun.crash_leftover", "TUN coverage could not be closed: %s", derr.Error())
		return true
	}
	// Disable reported success; confirm before releasing the bypass.
	after, afterErr := s.tun.Status(ctx)
	if afterErr == nil && after.State == contract.TunOff {
		s.clearTunBypass()
		return false
	}
	if afterErr != nil && tunRuntimeMissing(afterErr) {
		s.clearTunBypass()
		return false
	}
	s.tunBypassReason = contract.TunReasonConflict
	return true
}

// stopTunForStoppedStart handles a process that starts in stopped mode while a
// crash left coverage active. A missing helper is harmless, but a coverage
// state that cannot be closed aborts readiness: stopped mode has no listener or
// engine to serve captured traffic (§10.1).
func (s *Service) stopTunForStoppedStart(ctx context.Context) error {
	if s.tun == nil {
		return nil
	}
	st, err := s.tun.Status(ctx)
	if err == nil && st.State == contract.TunOff {
		return nil
	}
	if err != nil && tunRuntimeMissing(err) {
		return nil
	}
	if derr := s.tun.Disable(ctx); derr != nil {
		s.tunBypassReason = contract.TunReasonConflict
		s.logger.Logf(levelError, "tun.stopped_start", "crash-left TUN could not be closed: %s", derr.Error())
		return tunOpError("control.tun_stopped", derr, true)
	}
	s.clearTunBypass()
	return nil
}

// stopTunAfterListenerFailure removes coverage when the traffic listener could
// not bind after TUN came up. The listener error remains the reported failure;
// a secondary disable failure is logged as the recovery path.
func (s *Service) stopTunAfterListenerFailure(ctx context.Context) {
	if s.tun == nil {
		return
	}
	if err := s.tun.Disable(ctx); err != nil {
		s.logger.Logf(levelError, "tun.listener_failure", "TUN could not be disabled after a listener failure: %s; recover with 'systemctl stop routelatch-tun.service' or 'sudo routelatch tun uninstall'", err.Error())
	}
}

// stopTunForShutdown is the Quit/stop first step. A supervisor-managed engine
// restart keeps coverage (the new engine adopts it); every other shutdown and
// Quit disables it first and aborts on failure. The persisted tun_enabled
// preference is never cleared here.
func (s *Service) stopTunForShutdown(ctx context.Context) error {
	if s.preserveTunOnShutdown && s.tunShutdownMarker != "" {
		if _, err := os.Stat(s.tunShutdownMarker); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if s.tun == nil {
		s.clearTunBypass()
		return nil
	}
	st, err := s.tun.Status(ctx)
	if err == nil && st.State == contract.TunOff {
		s.clearTunBypass()
		return nil
	}
	if err != nil && tunRuntimeMissing(err) {
		// No helper/socket can be holding an overlay. Treating that as a stop
		// failure would make every Quit fail on machines without the optional
		// helper; the documented missing-helper behavior is warning + off, not
		// a failed proxy (§10.2).
		s.clearTunBypass()
		return nil
	}
	if err := s.tun.Disable(ctx); err != nil {
		return tunOpError("control.stop", err, true)
	}
	s.clearTunBypass()
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

// tunRuntimeMissing reports whether a status failure proves the optional helper
// is not installed (a missing socket). A refused/crashed service is not proof
// that no overlay survives, so it stays fail-closed.
func tunRuntimeMissing(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var te *contract.TunError
	if !errors.As(err, &te) {
		return false
	}
	return te.Reason == contract.TunReasonHelperUnavailable
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
