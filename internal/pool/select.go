package pool

import (
	"math"
	"sort"
	"sync"
	"time"

	"routelatch/internal/contract"
)

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

	ranked := rank(p)
	if len(ranked) == 0 {
		return contract.Record{}, selectFailure("no usable exit: the pool is empty, cooling or unsupported")
	}
	best := ranked[0]

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, nil
	}
	current, ok := p.Record(s.current)
	if !ok || !p.Usable(s.current) {
		// A cooling/removed current exit is not a dwell case: recovery must be
		// immediate. A cooling current is excluded from rank(), so best is a
		// usable exit.
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, nil
	}
	if contract.ExitID(best.ID) == s.current {
		return current, nil
	}
	if s.shouldSwitch(p, current, best, now) {
		s.current = contract.ExitID(best.ID)
		s.switchedAt = now
		return best, nil
	}
	return current, nil
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
// its quality is unknown.
func rank(p *Pool) []contract.Record {
	usable := make([]contract.Record, 0, len(p.records))
	for _, rec := range p.records {
		if (rec.Status == contract.RecordHealthy || rec.Status == contract.RecordUnknown) && Dialable(rec) {
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
