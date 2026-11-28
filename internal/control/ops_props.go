package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/diagnostics"
	"routelatch/internal/platform"
	"routelatch/internal/probes"
	"routelatch/internal/proposals"
	"routelatch/internal/routing"
)

// ---------- diagnose ----------

// AttemptView is one bounded probe outcome in the documented duration_ms
// shape (the diagnostics package keeps nanoseconds internally).
type AttemptView struct {
	Kind       contract.ProbeKind    `json:"kind"`
	Outcome    contract.ProbeOutcome `json:"outcome"`
	Failure    contract.ErrorKind    `json:"failure,omitempty"`
	DurationMS int64                 `json:"duration_ms"`
}

func attemptViews(in []diagnostics.Attempt) []AttemptView {
	out := make([]AttemptView, 0, len(in))
	for _, a := range in {
		out = append(out, AttemptView{
			Kind: a.Kind, Outcome: a.Outcome, Failure: a.Failure,
			DurationMS: a.Duration.Milliseconds(),
		})
	}
	return out
}

// DiagnoseData is the `diagnose` payload.
type DiagnoseData struct {
	Destination contract.Destination `json:"destination"`
	Kind        string               `json:"kind"`
	Attempts    []AttemptView        `json:"attempts"`
	Eligible    bool                 `json:"eligible"`
	ProposalID  contract.ProposalID  `json:"proposal_id,omitempty"`
	Scope       string               `json:"scope"`
}

func (s *Service) diagnose(ctx context.Context, req Request) (DiagnoseData, error) {
	if strings.EqualFold(req.Kind, "pool") {
		return DiagnoseData{}, &contract.Failure{
			Kind: contract.Unsupported, Path: contract.PathLocal, Op: "diagnostics.diagnose",
			Message: "pool-path diagnosis is outside v1; only direct, bounded probes are supported",
		}
	}
	if s.diag == nil {
		return DiagnoseData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "diagnostics.diagnose", Message: "diagnostics are not running"}
	}
	dst, err := routing.NormalizeDestination(strings.TrimSpace(req.Input))
	if err != nil {
		return DiagnoseData{}, err
	}
	if dst.Port == 0 {
		return DiagnoseData{}, usage("diagnose needs host:port")
	}
	res, err := s.diag.Diagnose(ctx, dst)
	if err != nil {
		return DiagnoseData{}, mapDiagnosticsError(err)
	}
	s.logger.Logf(levelInfo, "diagnose", "kind=direct eligible=%t attempts=%d", res.Eligible, len(res.Attempts))
	return DiagnoseData{
		Destination: res.Destination, Kind: "direct", Attempts: attemptViews(res.Attempts),
		Eligible: res.Eligible, ProposalID: res.ProposalID, Scope: res.Scope,
	}, nil
}

func mapDiagnosticsError(err error) error {
	switch {
	case errors.Is(err, diagnostics.ErrProposalNotFound):
		return NotFound("proposal")
	case errors.Is(err, diagnostics.ErrProposalDecided):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "proposal was already decided"}
	case errors.Is(err, diagnostics.ErrProposalExpired):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "proposal expired"}
	case errors.Is(err, diagnostics.ErrCooldown):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "probe cooldown has not elapsed"}
	case errors.Is(err, probes.ErrProbeCooldown):
		// The prober enforces its own fixed destination cooldown, which can
		// still be running after the diagnostics cooldown has elapsed. It is
		// the same "come back later" outcome, not an internal defect: the
		// documented proposals exit set is 0/3/5/6 (docs/contracts.md §7.1).
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "probe cooldown has not elapsed"}
	case errors.Is(err, probes.ErrProbeInFlight):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "a probe for this destination is already running"}
	case errors.Is(err, diagnostics.ErrInFlight):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "a probe for this destination is already running"}
	case errors.Is(err, diagnostics.ErrClosed):
		return &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "diagnostics", Message: "diagnostics manager is closed"}
	default:
		return err
	}
}

// ---------- proposals ----------

// ProposalView is the CLI shape of one proposal.
type ProposalView struct {
	ID          contract.ProposalID     `json:"id"`
	Kind        contract.ProposalKind   `json:"kind,omitempty"`
	RuleID      contract.RuleID         `json:"rule_id,omitempty"`
	Destination contract.Destination    `json:"destination"`
	Status      contract.ProposalStatus `json:"status"`
	ExpiresAt   time.Time               `json:"expires_at"`
	Note        string                  `json:"note,omitempty"`
}

// ProposalsData is the `proposals list` payload.
type ProposalsData struct {
	Proposals []ProposalView `json:"proposals"`
}

func proposalView(p contract.Proposal) ProposalView {
	return ProposalView{
		ID: p.ID, Kind: p.Kind, RuleID: p.RuleID, Destination: p.Destination,
		Status: p.Status, ExpiresAt: p.ExpiresAt, Note: SanitizeTerminal(p.Note),
	}
}

func (s *Service) listProposals() ProposalsData {
	list := s.props.List()
	out := make([]ProposalView, 0, len(list))
	for _, p := range list {
		out = append(out, proposalView(p))
	}
	for _, u := range s.props.ListUnroutes() {
		out = append(out, ProposalView{
			ID: contract.ProposalID(u.ID), Kind: contract.ProposalUnroute, RuleID: u.RuleID,
			Destination: u.Destination, Status: u.Status, ExpiresAt: u.ExpiresAt,
			Note: SanitizeTerminal(u.Note),
		})
	}
	return ProposalsData{Proposals: out}
}

// ProposalAcceptData reports the rule created from an accepted proposal.
type ProposalAcceptData struct {
	RuleID contract.RuleID `json:"rule_id"`
}

// acceptRuleKind maps the optional `rule_kind` of proposals.accept to the rule
// kind to create. An empty kind keeps the proposal's natural rule (exact
// domain or exact IP); a hostname proposal can widen to domain_suffix. A kind
// that does not fit the proposal is refused before the proposal is consumed,
// so a request never silently creates a different kind than it asked for.
func acceptRuleKind(raw string, dst contract.Destination) (contract.RuleKind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		switch dst.Kind {
		case contract.DestDomain:
			return contract.RuleDomain, nil
		case contract.DestIP:
			return contract.RuleIP, nil
		default:
			return "", usage("proposals accept needs a domain or IP proposal")
		}
	case "domain", "exact":
		if dst.Kind != contract.DestDomain {
			return "", usage("an address proposal cannot become a domain rule; omit rule_kind")
		}
		return contract.RuleDomain, nil
	case "domain_suffix", "suffix":
		if dst.Kind != contract.DestDomain {
			return "", usage("rule kind domain_suffix needs a hostname proposal; this proposal is an address")
		}
		return contract.RuleDomainSuffix, nil
	default:
		return "", usage("rule kind must be domain, domain_suffix or empty")
	}
}

func (s *Service) acceptProposal(ctx context.Context, rawID, rawKind string) (ProposalAcceptData, error) {
	s.propsMu.Lock()
	defer s.propsMu.Unlock()
	id := contract.ProposalID(strings.TrimSpace(rawID))
	if id == "" {
		return ProposalAcceptData{}, usage("proposals accept needs a proposal id")
	}
	if s.diag == nil {
		return ProposalAcceptData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "proposals.accept", Message: "diagnostics are not running"}
	}
	p, found := s.props.Get(id)
	if found && p.Kind == contract.ProposalUnroute {
		if strings.TrimSpace(rawKind) != "" {
			return ProposalAcceptData{}, usage("an unroute proposal removes a rule; rule_kind does not apply")
		}
		ruleID, err := s.props.AcceptUnroute(proposals.UnrouteID(id))
		if err != nil {
			return ProposalAcceptData{}, mapProposalError(err)
		}
		// Accepting an unroute suggestion is the user's confirmation to remove
		// the rule; removal still runs through the normal sweep.
		if _, err := s.removeRule(ctx, string(ruleID)); err != nil {
			return ProposalAcceptData{}, err
		}
		return ProposalAcceptData{RuleID: ruleID}, nil
	}
	// Validate the requested kind against the pending proposal before Accept
	// consumes it: a refused request must leave the proposal pending.
	if !found {
		return ProposalAcceptData{}, mapProposalError(proposals.ErrNotFound)
	}
	kind, err := acceptRuleKind(rawKind, p.Destination)
	if err != nil {
		return ProposalAcceptData{}, err
	}
	dest, err := s.diag.Accept(id)
	if err != nil {
		return ProposalAcceptData{}, mapProposalError(err)
	}
	operand := dest.Host
	if dest.Kind == contract.DestIP {
		operand = dest.Addr.Unmap().String()
	}
	if _, err := s.addRule(ctx, Request{Input: operand, RuleKind: string(kind), Note: "accepted from diagnostic proposal " + string(id)}); err != nil {
		return ProposalAcceptData{}, err
	}
	snap := s.Snapshot()
	for _, r := range snap.State.Rules {
		if r.Kind == kind && (r.Host == dest.Host || (dest.Kind == contract.DestIP && r.Addr == dest.Addr.Unmap())) {
			return ProposalAcceptData{RuleID: r.ID}, nil
		}
	}
	return ProposalAcceptData{}, nil
}

// ProposalStatusData is the reject/clear payload.
type ProposalStatusData struct {
	Status  contract.ProposalStatus `json:"status,omitempty"`
	Cleared int                     `json:"cleared,omitempty"`
}

func (s *Service) rejectProposal(ctx context.Context, rawID, note string) (ProposalStatusData, error) {
	s.propsMu.Lock()
	defer s.propsMu.Unlock()
	id := contract.ProposalID(strings.TrimSpace(rawID))
	if id == "" {
		return ProposalStatusData{}, usage("proposals reject needs a proposal id")
	}
	if s.diag == nil {
		return ProposalStatusData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "proposals.reject", Message: "diagnostics are not running"}
	}
	if p, ok := s.props.Get(id); ok && p.Kind == contract.ProposalUnroute {
		if err := s.props.RejectUnroute(proposals.UnrouteID(id), SanitizeTerminal(note)); err != nil {
			return ProposalStatusData{}, mapProposalError(err)
		}
	} else if err := s.diag.Reject(id, SanitizeTerminal(note)); err != nil {
		return ProposalStatusData{}, mapProposalError(err)
	}
	return ProposalStatusData{Status: contract.ProposalRejected}, nil
}

// ProposalRecheckData is the `proposals recheck` payload.
type ProposalRecheckData struct {
	Proposal      ProposalView  `json:"proposal"`
	Attempts      []AttemptView `json:"attempts"`
	Eligible      bool          `json:"eligible"`
	EvidenceAdded bool          `json:"evidence_added"`
}

func (s *Service) recheckProposal(ctx context.Context, rawID string) (ProposalRecheckData, error) {
	s.propsMu.Lock()
	defer s.propsMu.Unlock()
	id := contract.ProposalID(strings.TrimSpace(rawID))
	if id == "" {
		return ProposalRecheckData{}, usage("proposals recheck needs a proposal id")
	}
	if s.diag == nil {
		return ProposalRecheckData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "proposals.recheck", Message: "diagnostics are not running"}
	}
	if p, ok := s.props.Get(id); ok && p.Kind == contract.ProposalUnroute {
		return ProposalRecheckData{}, &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "an unroute proposal has no probe evidence to recheck"}
	}
	res, err := s.diag.Recheck(ctx, id)
	if err != nil {
		return ProposalRecheckData{}, mapDiagnosticsError(err)
	}
	return ProposalRecheckData{
		Proposal: proposalView(res.Proposal), Attempts: attemptViews(res.Attempts),
		Eligible: res.Eligible, EvidenceAdded: res.EvidenceAdded,
	}, nil
}

func (s *Service) clearProposals(ctx context.Context) (ProposalStatusData, error) {
	s.propsMu.Lock()
	defer s.propsMu.Unlock()
	n := s.props.Clear()
	return ProposalStatusData{Cleared: n}, nil
}

func mapProposalError(err error) error {
	switch {
	case errors.Is(err, proposals.ErrNotFound):
		return NotFound("proposal")
	case errors.Is(err, proposals.ErrNotPending):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "proposal was already decided"}
	case errors.Is(err, proposals.ErrExpired):
		return &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "proposal expired"}
	default:
		return err
	}
}

// ---------- desktop ----------

// DesktopData is the `desktop proxy status` payload.
type DesktopData struct {
	Environment  string   `json:"environment,omitempty"`
	Supported    bool     `json:"supported"`
	Applied      bool     `json:"applied"`
	Cleared      bool     `json:"cleared,omitempty"`
	ClearedKeys  []string `json:"cleared_keys,omitempty"`
	PreviousSet  bool     `json:"previous_set"`
	Conflict     []string `json:"conflict,omitempty"`
	Restored     []string `json:"restored,omitempty"`
	UserModified []string `json:"user_modified,omitempty"`
	Failed       []string `json:"failed,omitempty"`
	Notice       string   `json:"notice,omitempty"`
}

func (s *Service) desktopAdapterFor(health platform.HealthChecker) *platform.DesktopAdapter {
	if s.desktopAdapter != nil {
		return s.desktopAdapter(health)
	}
	return &platform.DesktopAdapter{Runner: platform.ExecRunner{}, Health: health, Now: s.now}
}

func (s *Service) desktopStatus(ctx context.Context) (DesktopData, error) {
	s.opMu.Lock()
	snap := s.Snapshot()
	s.opMu.Unlock()
	kind, err := platform.DetectDesktop(os.LookupEnv)
	out := DesktopData{
		Applied: snap.State.Desktop.Applied, Cleared: snap.State.Desktop.Cleared,
		PreviousSet: len(snap.State.Desktop.Keys) > 0,
	}
	if snap.State.Desktop.Environment != "" {
		out.Environment = SanitizeTerminal(snap.State.Desktop.Environment)
	}
	if err == nil {
		out.Environment = string(kind)
		out.Supported = true
	}
	if snap.State.Desktop.Applied {
		// Reading the desktop settings can block on an external tool, so it
		// happens after opMu is released: status must not stall mutations.
		adapter := s.desktopAdapterFor(nil)
		changed, verifyErr := adapter.Conflicts(ctx, snap.State.Desktop)
		switch {
		case verifyErr != nil:
			out.Notice = "could not verify the desktop proxy ownership; check the desktop settings tool and the recorded state"
		case len(changed) > 0:
			out.Conflict = changed
			out.Notice = "the desktop setting changed since RouteLatch applied it; run 'routelatch desktop proxy set' to re-apply or 'routelatch desktop proxy restore' to revert"
		}
	}
	return out, nil
}

type serviceHealth struct{ s *Service }

func (h serviceHealth) Check(ctx context.Context) (platform.Health, error) {
	if err := ctx.Err(); err != nil {
		return platform.Health{}, err
	}
	if !h.s.listenersOpen {
		return platform.Health{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "platform.health", Message: "no traffic listener is running"}
	}
	return platform.Health{HTTPListen: h.s.httpAddr, SOCKSListen: h.s.socksAddr}, nil
}

func (s *Service) desktopApply(ctx context.Context) (DesktopData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !s.listenersOpen {
		return DesktopData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "desktop.apply", Message: "cannot point the desktop at a listener that is not running"}
	}
	kind, err := platform.DetectDesktop(os.LookupEnv)
	if err != nil {
		return DesktopData{}, &contract.Failure{Kind: contract.Unsupported, Path: contract.PathLocal, Op: "desktop.apply", Message: "this desktop environment is not supported; only GNOME and KDE are changed"}
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap := s.Snapshot()
	targets := platform.Targets{HTTPListen: s.httpAddr, SOCKSListen: s.socksAddr}
	adapter := s.desktopAdapterFor(serviceHealth{s: s})
	state, err := adapter.Apply(ctx, platform.ApplyOptions{
		Kind: kind, Targets: targets, Previous: snap.State.Desktop, InstanceID: randomID("d-"),
		Save: func(ctx context.Context, marker contract.DesktopProxyState) error {
			_, err := s.store.Mutate(ctx, func(st *contract.State) error {
				st.Desktop = marker
				return nil
			})
			return err
		},
	})
	if err != nil {
		return DesktopData{}, err
	}
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		st.Desktop = state
		return nil
	})
	if err != nil {
		return DesktopData{}, err
	}
	s.snap.Store(&after)
	return DesktopData{Environment: string(kind), Supported: true, Applied: state.Applied, PreviousSet: len(state.Keys) > 0}, nil
}

func (s *Service) desktopRestore(ctx context.Context) (DesktopData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap := s.Snapshot()
	if !snap.State.Desktop.Applied && len(snap.State.Desktop.Keys) == 0 {
		return DesktopData{Applied: false, PreviousSet: false, Notice: "no desktop proxy setting was applied by RouteLatch"}, nil
	}
	adapter := s.desktopAdapterFor(nil)
	result, err := adapter.Restore(ctx, snap.State.Desktop)
	if err != nil {
		return DesktopData{}, err
	}
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		st.Desktop = result.State
		return nil
	})
	if err != nil {
		return DesktopData{}, err
	}
	s.snap.Store(&after)
	return DesktopData{
		Environment: snap.State.Desktop.Environment, Supported: true,
		Applied: result.State.Applied, PreviousSet: len(result.State.Keys) > 0,
		Restored: result.Restored, UserModified: result.UserModified, Failed: result.Failed,
	}, nil
}

// desktopClear disables the system proxy while keeping (or creating) the
// ownership marker, so a later restore still returns the user's original
// values. It needs no live listener, so it also works while listening is
// stopped; without a recorded environment it uses the detected desktop kind.
func (s *Service) desktopClear(ctx context.Context) (DesktopData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap := s.Snapshot()
	state := snap.State.Desktop
	kind := platform.Kind(state.Environment)
	if kind != platform.KindGNOME && kind != platform.KindKDE {
		detected, err := platform.DetectDesktop(os.LookupEnv)
		if err != nil {
			return DesktopData{}, &contract.Failure{
				Kind: contract.Unsupported, Path: contract.PathLocal, Op: "desktop.clear",
				Message: "this desktop environment is not supported; only GNOME and KDE desktop proxy settings can be cleared",
			}
		}
		kind = detected
	}
	adapter := s.desktopAdapterFor(nil)
	var saved *contract.Snapshot
	result, err := adapter.Clear(ctx, platform.ClearOptions{
		Kind: kind, State: state,
		// Persist the restore point before the desktop write, mirroring Apply:
		// a crash or a failed state write must not leave a disabled desktop
		// without the marker Restore needs.
		Save: func(ctx context.Context, marker contract.DesktopProxyState) error {
			after, merr := s.store.Mutate(ctx, func(st *contract.State) error {
				st.Desktop = marker
				return nil
			})
			if merr != nil {
				return merr
			}
			saved = &after
			return nil
		},
	})
	if saved != nil {
		s.snap.Store(saved)
	}
	if err != nil {
		return DesktopData{}, err
	}
	environment := state.Environment
	if environment == "" {
		environment = string(kind)
	}
	return DesktopData{
		Environment: environment, Supported: true,
		Applied: result.State.Applied, Cleared: result.State.Cleared,
		PreviousSet: len(result.State.Keys) > 0,
		ClearedKeys: result.Cleared, Notice: result.Notice,
		UserModified: result.UserModified, Failed: result.Failed,
	}, nil
}

// ---------- backup / restore ----------

// BackupData is the `backup` payload.
type BackupData struct {
	Ref contract.BackupRef `json:"ref"`
}

func (s *Service) backup(ctx context.Context) (BackupData, error) {
	ref, err := s.store.Backup(ctx)
	if err != nil {
		return BackupData{}, err
	}
	return BackupData{Ref: ref}, nil
}

// RestoreData reports the committed state after a restore.
type RestoreData struct {
	Mode          contract.Mode          `json:"mode"`
	PolicyVersion contract.PolicyVersion `json:"policy_version"`
	Closed        int                    `json:"closed"`
}

func (s *Service) restore(ctx context.Context, ref string) (RestoreData, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RestoreData{}, usage("restore needs a backup path inside the backup directory")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	wasStopped := s.stopped
	_, backupState, ierr := s.store.InspectBackup(ref)
	if ierr != nil {
		return RestoreData{}, ierr
	}
	if !validMode(backupState.Mode) {
		return RestoreData{}, &contract.Failure{Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "store.restore", Message: "backup has an unknown routing mode; nothing was changed"}
	}
	if _, perr := routing.NewPolicy(backupState.PolicyVersion, routing.Config{
		Mode: backupState.Mode, Rules: backupState.Rules, SelfPorts: selfPorts(s.Snapshot().Config),
	}); perr != nil {
		return RestoreData{}, &contract.Failure{Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "store.restore", Message: "backup contains an invalid routing policy; nothing was changed", Err: perr}
	}
	if wasStopped && backupState.Mode != contract.ModeStopped && s.stoppedByThisProcess {
		return RestoreData{}, &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "service is stopped; restart it before restoring an active state"}
	}
	// Restoring a stopped state closes TUN before listeners/engine, exactly
	// like a mode-stopped change. A disable failure aborts before the backup is
	// committed, so the host is never left redirected into a dead listener.
	if !wasStopped && backupState.Mode == contract.ModeStopped {
		if err := s.stopTunForModeStopped(ctx); err != nil {
			return RestoreData{}, err
		}
	}
	before := s.Snapshot()
	after, err := s.store.RestorePath(ctx, ref)
	if err != nil {
		return RestoreData{}, err
	}
	if after.State.PolicyVersion <= before.State.PolicyVersion {
		after, err = s.store.Mutate(ctx, func(st *contract.State) error {
			st.PolicyVersion = before.State.PolicyVersion + 1
			return nil
		})
		if err != nil {
			return RestoreData{}, err
		}
	}
	if err := s.reconcileExits(ctx, before, after); err != nil {
		return RestoreData{}, err
	}
	nextPolicy, err := s.rebuildPolicy(after)
	if err != nil {
		return RestoreData{}, err
	}
	s.commitSnapshotLocked(after, nextPolicy)
	if after.State.Mode == contract.ModeStopped {
		if wasStopped {
			return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion()}, nil
		}
		report, err := s.sweep(ctx, contract.CloseStop, nextPolicy.PolicyVersion(), nil)
		if err != nil {
			return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion(), Closed: report.Closed}, err
		}
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
		if s.engine != nil {
			_ = s.engine.Stop(ctx)
		}
		s.listenersOpen = false
		s.stopped = true
		s.stoppedByThisProcess = true
		return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion(), Closed: report.Closed}, nil
	}
	if wasStopped {
		// A fresh process that started in stopped mode has not opened the
		// engine, so this restore may start it once.
		s.stopped = false
		if err := s.startNetworkLockedHeld(ctx); err != nil {
			s.stopped = true
			s.stoppedByThisProcess = true
			if reverted, rerr := s.store.Mutate(ctx, func(st *contract.State) error {
				st.Mode = contract.ModeStopped
				st.PolicyVersion = after.State.PolicyVersion + 1
				return nil
			}); rerr == nil {
				if p, perr := s.rebuildPolicy(reverted); perr == nil {
					s.commitSnapshotLocked(reverted, p)
				}
			}
			return RestoreData{}, err
		}
		return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion()}, nil
	}
	report, err := s.sweep(ctx, contract.CloseShutdown, nextPolicy.PolicyVersion(), routeKeep(nextPolicy))
	if err != nil {
		return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion(), Closed: report.Closed}, err
	}
	return RestoreData{Mode: after.State.Mode, PolicyVersion: nextPolicy.PolicyVersion(), Closed: report.Closed}, nil
}

func randomID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + hex.EncodeToString([]byte(time.Now().String()))
	}
	return prefix + hex.EncodeToString(b[:])
}
