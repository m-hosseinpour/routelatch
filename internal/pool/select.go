package pool

import (
	"math"
	"sort"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// RerankSkipReason is a stable, machine-readable explanation for an exit the
// automatic selection did not choose.
type RerankSkipReason string

const (
	RerankSkipPinned      RerankSkipReason = "pinned"
	RerankSkipCooling     RerankSkipReason = "cooling"
	RerankSkipUnsupported RerankSkipReason = "unsupported"
	RerankSkipInvalid     RerankSkipReason = "invalid"
	RerankSkipUnknown     RerankSkipReason = "unknown"
	RerankSkipDwell       RerankSkipReason = "dwell"
	RerankSkipSamples     RerankSkipReason = "samples"
	RerankSkipImprovement RerankSkipReason = "improvement"
	RerankSkipNoTarget    RerankSkipReason = "no_target"
)

// RerankSkip names one exit that was not selected and why.
type RerankSkip struct {
	Exit   contract.ExitID  `json:"exit"`
	Reason RerankSkipReason `json:"reason"`
}

// RerankResult is the outcome of a re-selection. Version is read from the
// snapshot and is never advanced by re-selection.
type RerankResult struct {
	Before  contract.ExitID        `json:"before,omitempty"`
	After   contract.ExitID        `json:"after,omitempty"`
	Skipped []RerankSkip           `json:"skipped,omitempty"`
	Version contract.PolicyVersion `json:"version"`
}

// Selector adds the D16 hysteresis to pool ranking. Its only mutable state is
// the auto-selected exit and the last switch time; it never writes the store,
// restarts the engine or changes a policy version.
type Selector struct {
	mu         sync.Mutex
	current    contract.ExitID
	switchedAt time.Time
}

// NewSelector returns an empty selector.
func NewSelector() *Selector { return &Selector{} }

// Current reports the last auto-selected exit (empty before the first choice).
func (s *Selector) Current() contract.ExitID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// SelectFor returns the exit to dial. A non-empty requested value, including a
// persisted manual selection, is pinned: it is never replaced by a different
// exit, even when it is cooling. An automatic choice ranks healthy before
// unknown, then EWMA ascending, then user order, with the switch gates in
// docs/contracts.md §6.4.
func (s *Selector) SelectFor(state contract.State, requested contract.ExitID, now time.Time) (contract.Record, *contract.Failure) {
	return s.SelectForExcluding(state, requested, nil, now)
}

// SelectForExcluding is SelectFor with the exits already attempted by one
// client connection removed from automatic selection. A manual pin ignores the
// exclusion list: the pin is the user's decision, and the caller must surface
// its failure rather than silently using another exit (docs/contracts.md §2.2).
func (s *Selector) SelectForExcluding(state contract.State, requested contract.ExitID, excludes []contract.ExitID, now time.Time) (contract.Record, *contract.Failure) {
	p := Build(state, now)
	if pinned := firstNonEmptyExit(requested, state.SelectedExit); pinned != "" {
		rec, ok := p.Record(pinned)
		if !ok {
			return contract.Record{}, selectFailure("the selected exit no longer exists")
		}
		if rec.Status == contract.RecordInvalid || rec.Status == contract.RecordUnsupported || !Dialable(rec) {
			return contract.Record{}, selectFailure("the selected exit is not usable")
		}
		return rec, nil
	}
	rec, _, fail := s.auto(p, excludes, now)
	return rec, fail
}

// Rerank recomputes the automatic selection without dialing. It honours a
// pinned state.SelectedExit, reports the previous and new exit with skip
// reasons, and never changes rules, mode, connections or policy version.
func (s *Selector) Rerank(state contract.State, now time.Time) (RerankResult, error) {
	version := state.PolicyVersion
	if pinned := state.SelectedExit; pinned != "" {
		return RerankResult{
			Before: pinned, After: pinned, Version: version,
			Skipped: []RerankSkip{{Exit: pinned, Reason: RerankSkipPinned}},
		}, nil
	}
	before := s.Current()
	after, skips, fail := s.auto(Build(state, now), nil, now)
	if fail != nil {
		// No usable exit is not an execution error: the CLI needs the skip
		// reasons (including no_target) to explain it.
		return RerankResult{Before: before, Skipped: skips, Version: version}, nil
	}
	return RerankResult{
		Before: before, After: contract.ExitID(after.ID), Skipped: skips, Version: version,
	}, nil
}

// auto applies the ranking and hysteresis and updates the selector's in-memory
// current exit. It returns skip reasons for every exit that was not chosen.
// excludes are exits this client connection already tried; they are not
// candidates, and a current exit that becomes excluded is replaced immediately
// so a pre-byte retry cannot loop on the same exit.
func (s *Selector) auto(p *Pool, excludes []contract.ExitID, now time.Time) (contract.Record, []RerankSkip, *contract.Failure) {
	ranked := rank(p, excludes)
	if len(ranked) == 0 {
		return contract.Record{}, unusableSkips(p), selectFailure("no usable exit: the pool is empty, cooling or unsupported")
	}
	best := ranked[0]

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, skipOthers(p, contract.ExitID(best.ID), contract.ExitID(best.ID), ""), nil
	}
	current, ok := p.Record(s.current)
	if !ok || !p.Usable(s.current) || isExcluded(s.current, excludes) {
		// A cooling/removed/excluded current exit is not a dwell case: recovery
		// must be immediate. A cooling current is excluded from rank(), so best
		// is a usable exit.
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, skipOthers(p, contract.ExitID(best.ID), contract.ExitID(best.ID), ""), nil
	}
	if contract.ExitID(best.ID) == s.current {
		return current, skipOthers(p, s.current, s.current, ""), nil
	}
	if s.shouldSwitch(p, current, best, now) {
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, skipOthers(p, contract.ExitID(best.ID), contract.ExitID(best.ID), ""), nil
	}
	return current, skipOthers(p, s.current, contract.ExitID(best.ID), s.gateReason(p, current, best, now)), nil
}

func isExcluded(exit contract.ExitID, excludes []contract.ExitID) bool {
	for _, x := range excludes {
		if x == exit {
			return true
		}
	}
	return false
}

func (s *Selector) gateReason(p *Pool, current, best contract.Record, now time.Time) RerankSkipReason {
	bh, ok := p.Health(contract.ExitID(best.ID))
	if !ok || bh.Samples < contract.SwitchMinSamples {
		return RerankSkipSamples
	}
	if !s.switchedAt.IsZero() && now.Sub(s.switchedAt) < contract.SwitchMinDwell {
		return RerankSkipDwell
	}
	return RerankSkipImprovement
}

func skipOthers(p *Pool, selected, bestExit contract.ExitID, bestGate RerankSkipReason) []RerankSkip {
	var skips []RerankSkip
	for _, rec := range p.records {
		exit := contract.ExitID(rec.ID)
		if exit == selected {
			continue
		}
		reason := RerankSkipReason("")
		switch rec.Status {
		case contract.RecordInvalid:
			reason = RerankSkipInvalid
		case contract.RecordUnsupported:
			reason = RerankSkipUnsupported
		case contract.RecordCooling:
			reason = RerankSkipCooling
		case contract.RecordUnknown:
			reason = RerankSkipUnknown
		case contract.RecordHealthy:
			if bestGate != "" && exit == bestExit {
				reason = bestGate
			} else {
				reason = RerankSkipImprovement
			}
		default:
			reason = RerankSkipUnknown
		}
		skips = append(skips, RerankSkip{Exit: exit, Reason: reason})
	}
	return skips
}

func unusableSkips(p *Pool) []RerankSkip {
	var skips []RerankSkip
	for _, rec := range p.records {
		var reason RerankSkipReason
		switch rec.Status {
		case contract.RecordInvalid:
			reason = RerankSkipInvalid
		case contract.RecordUnsupported:
			reason = RerankSkipUnsupported
		case contract.RecordCooling:
			reason = RerankSkipCooling
		default:
			reason = RerankSkipUnsupported
		}
		skips = append(skips, RerankSkip{Exit: contract.ExitID(rec.ID), Reason: reason})
	}
	skips = append(skips, RerankSkip{Reason: RerankSkipNoTarget})
	return skips
}

func (s *Selector) shouldSwitch(p *Pool, current, best contract.Record, now time.Time) bool {
	currentStatus := current.Status
	bestStatus := best.Status
	if bestStatus == contract.RecordHealthy && currentStatus != contract.RecordHealthy {
		return true
	}
	if currentStatus == contract.RecordHealthy && bestStatus != contract.RecordHealthy {
		return false
	}
	if currentStatus != contract.RecordHealthy && bestStatus != contract.RecordHealthy {
		// Both unknown: keep the established exit; user order already decided
		// the first pick.
		return false
	}
	ch, okCurrent := p.Health(contract.ExitID(current.ID))
	bh, okBest := p.Health(contract.ExitID(best.ID))
	if !okCurrent || !okBest {
		return false
	}
	if bh.Samples < contract.SwitchMinSamples {
		return false
	}
	if ch.EWMALatencyMS <= 0 || bh.EWMALatencyMS <= 0 {
		return false
	}
	if !s.switchedAt.IsZero() && now.Sub(s.switchedAt) < contract.SwitchMinDwell {
		return false
	}
	improvement := (float64(ch.EWMALatencyMS) - float64(bh.EWMALatencyMS)) / float64(ch.EWMALatencyMS)
	return improvement >= contract.SwitchMinImprovement
}

// rank returns usable records ordered healthy-first, then EWMA ascending, then
// user order. An exit with no latency sample sorts after sampled ones because
// its quality is unknown. Excluded exits are removed before ranking.
func rank(p *Pool, excludes []contract.ExitID) []contract.Record {
	usable := make([]contract.Record, 0, len(p.records))
	for _, rec := range p.records {
		if (rec.Status == contract.RecordHealthy || rec.Status == contract.RecordUnknown) &&
			Dialable(rec) && !isExcluded(contract.ExitID(rec.ID), excludes) {
			usable = append(usable, rec)
		}
	}
	sortRecords := func(i, j int) bool {
		a, b := usable[i], usable[j]
		if a.Status != b.Status {
			return a.Status == contract.RecordHealthy
		}
		ah, aok := p.health[contract.ExitID(a.ID)]
		bh, bok := p.health[contract.ExitID(b.ID)]
		ae, be := int64(math.MaxInt64), int64(math.MaxInt64)
		if aok && ah.Samples > 0 && ah.EWMALatencyMS > 0 {
			ae = ah.EWMALatencyMS
		}
		if bok && bh.Samples > 0 && bh.EWMALatencyMS > 0 {
			be = bh.EWMALatencyMS
		}
		if ae != be {
			return ae < be
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.ID < b.ID
	}
	sort.SliceStable(usable, func(i, j int) bool { return sortRecords(i, j) })
	return usable
}

func firstNonEmptyExit(values ...contract.ExitID) contract.ExitID {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func selectFailure(message string) *contract.Failure {
	return &contract.Failure{
		Kind:    contract.PoolUnavailable,
		Path:    contract.PathPool,
		Op:      "pool.select",
		Message: message,
	}
}
