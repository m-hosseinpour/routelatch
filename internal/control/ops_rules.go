package control

import (
	"context"
	"net/url"
	"strings"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/routing"
)

// CloseData is the sweep evidence a mode/rule change reports.
type CloseData struct {
	Count   int `json:"count"`
	Pending int `json:"pending"`
}

// ModeData is the `mode` payload.
type ModeData struct {
	Mode          contract.Mode          `json:"mode"`
	PolicyVersion contract.PolicyVersion `json:"policy_version"`
	Closed        CloseData              `json:"closed"`
	Kept          int                    `json:"kept"`
	DurationMS    int64                  `json:"duration_ms"`
}

func modeResult(mode contract.Mode, version contract.PolicyVersion, report contract.CloseReport) ModeData {
	return ModeData{
		Mode: mode, PolicyVersion: version,
		Closed: CloseData{Count: report.Closed, Pending: report.PendingCancel},
		Kept:   report.Kept, DurationMS: report.Duration.Milliseconds(),
	}
}

func (s *Service) setMode(ctx context.Context, raw string) (ModeData, error) {
	mode := contract.Mode(raw)
	if !validMode(mode) {
		return ModeData{}, usage("mode must be targeted, all-proxy, all-direct or stopped")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snap := s.Snapshot()
	cur := snap.State.Mode
	if s.stopped {
		if mode == contract.ModeStopped {
			return modeResult(mode, snap.State.PolicyVersion, contract.CloseReport{}), nil
		}
		if s.stoppedByThisProcess {
			return ModeData{}, &OpError{
				Kind: contract.StateLocked, Exit: contract.ExitConflict,
				Message: "service is stopped; leaving stopped mode needs a process restart",
			}
		}
		// A process that started in the persisted stopped state has not opened
		// the engine yet. This command is the restart, so it may start it once.
		after, _, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
			st.Mode = mode
			st.PolicyVersion++
			return nil
		})
		if err != nil {
			return ModeData{}, err
		}
		nextVersion := after.State.PolicyVersion
		s.stopped = false
		if err := s.startNetworkLocked(ctx); err != nil {
			// A partially started engine may already have been stopped; §2.7
			// forbids trying again in this process. Persist stopped so a restart
			// is the only recovery, and never claim the requested mode.
			s.stopped = true
			s.stoppedByThisProcess = true
			_, _, _ = s.mutateAndCommit(ctx, func(st *contract.State) error {
				st.Mode = contract.ModeStopped
				st.PolicyVersion = after.State.PolicyVersion + 1
				return nil
			})
			return ModeData{}, err
		}
		s.logger.Logf(levelInfo, "mode.change", "mode=%s policy=%d fresh-start", mode, nextVersion)
		return modeResult(mode, nextVersion, contract.CloseReport{}), nil
	}
	if mode == cur {
		return modeResult(mode, snap.State.PolicyVersion, contract.CloseReport{}), nil
	}

	if mode == contract.ModeStopped {
		// §10.1 ordering: close TUN before listeners/process. A failure aborts
		// the stop so the host is not left redirected into a dead listener.
		if err := s.stopTunForModeStopped(ctx); err != nil {
			return ModeData{}, err
		}
		// Apply the new snapshot, then close every conflicting live and pending
		// connection. Success is reported only after the sweep completes.
		after, _, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
			st.Mode = contract.ModeStopped
			st.PolicyVersion++
			return nil
		})
		if err != nil {
			return ModeData{}, err
		}
		nextVersion := after.State.PolicyVersion
		report, err := s.sweep(ctx, contract.CloseModeSwitch, nextVersion, nil)
		if err != nil {
			return modeResult(mode, nextVersion, report), err
		}
		if s.diag != nil {
			s.diag.Close(ctx)
		}
		if s.prober != nil {
			s.prober.Close()
		}
		if s.fe != nil {
			if err := s.fe.Stop(ctx); err != nil {
				return modeResult(mode, nextVersion, report), err
			}
		}
		if s.engine != nil {
			if err := s.engine.Stop(ctx); err != nil {
				return modeResult(mode, nextVersion, report), err
			}
		}
		s.listenersOpen = false
		s.stopped = true
		s.stoppedByThisProcess = true
		s.logger.Logf(levelInfo, "mode.change", "mode=stopped policy=%d closed=%d pending=%d kept=%d", nextVersion, report.Closed, report.PendingCancel, report.Kept)
		return modeResult(mode, nextVersion, report), nil
	}

	after, nextPolicy, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
		st.Mode = mode
		st.PolicyVersion++
		return nil
	})
	if err != nil {
		return ModeData{}, err
	}
	nextVersion := after.State.PolicyVersion
	report, err := s.sweep(ctx, contract.CloseModeSwitch, nextVersion, routeKeep(nextPolicy))
	if err != nil {
		return modeResult(mode, nextVersion, report), err
	}
	// A mode change cancels queued diagnostic work; health probes keep their
	// own budget (docs/contracts.md §6.3).
	if s.prober != nil {
		_, _ = s.prober.CancelQueued(ctx)
	}
	s.logger.Logf(levelInfo, "mode.change", "mode=%s policy=%d closed=%d pending=%d kept=%d", mode, nextVersion, report.Closed, report.PendingCancel, report.Kept)
	return modeResult(mode, nextVersion, report), nil
}

// RuleView is the CLI shape of a rule. Note is sanitized before it leaves the
// service.
type RuleView struct {
	ID      contract.RuleID     `json:"id"`
	Kind    contract.RuleKind   `json:"kind"`
	Host    string              `json:"host,omitempty"`
	Addr    string              `json:"addr,omitempty"`
	Prefix  string              `json:"prefix,omitempty"`
	Note    string              `json:"note,omitempty"`
	Origin  contract.RuleOrigin `json:"origin"`
	AddedAt time.Time           `json:"added_at"`
}

func ruleView(r contract.Rule) RuleView {
	v := RuleView{ID: r.ID, Kind: r.Kind, Host: r.Host, Note: SanitizeTerminal(r.Note), Origin: r.Origin, AddedAt: r.AddedAt}
	if r.Addr.IsValid() {
		v.Addr = r.Addr.String()
	}
	if r.Prefix.IsValid() {
		v.Prefix = r.Prefix.String()
	}
	return v
}

// RulesListData is the `rules list` payload. Rules keep the stored newest-first
// order so CLI output matches state.json and the tray.
type RulesListData struct {
	Rules []RuleView `json:"rules"`
}

func (s *Service) listRules(ctx context.Context) (RulesListData, error) {
	if err := ctx.Err(); err != nil {
		return RulesListData{}, err
	}
	snap := s.Snapshot()
	out := make([]RuleView, 0, len(snap.State.Rules))
	for _, r := range snap.State.Rules {
		out = append(out, ruleView(r))
	}
	return RulesListData{Rules: out}, nil
}

// RuleAddData is the `rules add` payload.
type RuleAddData struct {
	Rule       RuleView `json:"rule"`
	Normalized string   `json:"normalized"`
	Notice     string   `json:"notice,omitempty"`
}

func parseRuleKind(raw string) (contract.RuleKind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "", true
	case "domain", "exact":
		return contract.RuleDomain, true
	case "domain_suffix", "suffix":
		return contract.RuleDomainSuffix, true
	case "ip":
		return contract.RuleIP, true
	case "cidr":
		return contract.RuleCIDR, true
	default:
		return "", false
	}
}

func normalizeRule(kind contract.RuleKind, input string) (contract.Rule, error) {
	if kind == "" {
		return routing.NormalizeRule(input)
	}
	return routing.NormalizeRuleAs(kind, input)
}

func sameRule(a, b contract.Rule) bool {
	return a.Kind == b.Kind && a.Host == b.Host && a.Addr == b.Addr && a.Prefix == b.Prefix
}

func (s *Service) addRule(ctx context.Context, req Request) (RuleAddData, error) {
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return RuleAddData{}, usage("rules add needs a hostname, IP or CIDR")
	}
	kind, ok := parseRuleKind(req.RuleKind)
	if !ok {
		return RuleAddData{}, usage("rule kind must be domain, domain_suffix, ip or cidr")
	}
	parsed, err := normalizeRule(kind, input)
	if err != nil {
		return RuleAddData{}, err
	}
	parsed.ID = contract.RuleID(newRuleID())
	parsed.Origin = contract.RuleOriginUser
	parsed.AddedAt = s.now().UTC()
	parsed.Note = SanitizeTerminal(req.Note)
	if err := routing.ValidateRule(parsed); err != nil {
		return RuleAddData{}, err
	}

	notice := ruleInputNotice(input)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	before := s.Snapshot()
	for _, existing := range before.State.Rules {
		if sameRule(existing, parsed) {
			return RuleAddData{Rule: ruleView(existing), Normalized: ruleOperand(existing), Notice: "destination is already approved"}, nil
		}
	}
	added := false
	after, nextPolicy, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
		for _, existing := range st.Rules {
			if sameRule(existing, parsed) {
				return nil
			}
		}
		// Stored order is newest first: a new rule becomes index 0. The CLI,
		// state.json and the tray all read this one order.
		st.Rules = append([]contract.Rule{parsed}, st.Rules...)
		st.PolicyVersion++
		added = true
		return nil
	})
	if err != nil {
		return RuleAddData{}, err
	}
	if !added {
		for _, existing := range after.State.Rules {
			if sameRule(existing, parsed) {
				return RuleAddData{Rule: ruleView(existing), Normalized: ruleOperand(existing), Notice: "destination is already approved"}, nil
			}
		}
	}
	_, err = s.sweep(ctx, contract.CloseRuleChange, nextPolicy.PolicyVersion(), routeKeep(nextPolicy))
	if err != nil {
		return RuleAddData{Rule: ruleView(parsed), Normalized: ruleOperand(parsed), Notice: notice}, err
	}
	s.logger.Logf(levelInfo, "rules.add", "rule=%s kind=%s", parsed.ID, parsed.Kind)
	return RuleAddData{Rule: ruleView(parsed), Normalized: ruleOperand(parsed), Notice: notice}, nil
}

// ruleInputNotice tells the user when a URL's port was discarded. A rule is
// port-agnostic, and silently widening its scope would be misleading (§1.1,
// §1.3).
func ruleInputNotice(input string) string {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || u.Port() == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return "the URL port was removed: an approved rule covers every port"
	default:
		return ""
	}
}

// RuleRemoveData is the `rules remove` payload.
type RuleRemoveData struct {
	Removed contract.RuleID `json:"removed"`
	Closed  int             `json:"closed"`
}

func (s *Service) removeRule(ctx context.Context, rawID string) (RuleRemoveData, error) {
	id := contract.RuleID(strings.TrimSpace(rawID))
	if id == "" {
		return RuleRemoveData{}, usage("rules remove needs a rule id")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	before := s.Snapshot()
	found := false
	for _, r := range before.State.Rules {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		return RuleRemoveData{}, NotFound("rule " + string(id))
	}
	_, nextPolicy, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
		// Filtering in place keeps the relative order of the remaining rules,
		// so a removal never reshuffles the newest-first list.
		out := st.Rules[:0]
		for _, r := range st.Rules {
			if r.ID == id {
				continue
			}
			out = append(out, r)
		}
		st.Rules = out
		st.PolicyVersion++
		return nil
	})
	if err != nil {
		return RuleRemoveData{}, err
	}
	report, err := s.sweep(ctx, contract.CloseRuleChange, nextPolicy.PolicyVersion(), routeKeep(nextPolicy))
	if err != nil {
		return RuleRemoveData{Removed: id, Closed: report.Closed}, err
	}
	s.logger.Logf(levelInfo, "rules.remove", "rule=%s closed=%d", id, report.Closed)
	return RuleRemoveData{Removed: id, Closed: report.Closed}, nil
}
