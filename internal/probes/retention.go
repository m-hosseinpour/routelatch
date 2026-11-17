package probes

import (
	"sync"
	"time"

	"routelatch/internal/contract"
)

// Sample is one retained measurement. It keeps only destination, port, kind,
// timing and outcome; never a URL, header, body, cookie or request byte. The
// retention ring is process memory only: samples are displayed live and are
// never written to state, a backup or a log (D21).
type Sample struct {
	Exit        contract.ExitID       `json:"exit"`
	Kind        contract.ProbeKind    `json:"kind"`
	Destination contract.Destination  `json:"destination"`
	At          time.Time             `json:"at"`
	DurationMS  int64                 `json:"duration_ms"`
	Outcome     contract.ProbeOutcome `json:"outcome"`
	Failure     contract.ErrorKind    `json:"failure,omitempty"`
	Eligible    bool                  `json:"eligible"`
	BytesDown   int64                 `json:"bytes_down"`
	LocalOutage bool                  `json:"local_outage,omitempty"`
}

// Retention is a bounded per-exit ring: HealthSamplesPerExit samples and
// HealthRetention age. Pruning happens on every Add, so a long-running service
// does not need a separate sweeper.
type Retention struct {
	mu     sync.Mutex
	byExit map[contract.ExitID][]Sample
	now    func() time.Time
}

// NewRetention creates an empty store. now is optional.
func NewRetention(now func() time.Time) *Retention {
	if now == nil {
		now = time.Now
	}
	return &Retention{byExit: make(map[contract.ExitID][]Sample), now: now}
}

// Add appends one sample and applies both retention bounds.
func (r *Retention) Add(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.At.IsZero() {
		s.At = r.now().UTC()
	}
	list := append(r.byExit[s.Exit], s)
	cutoff := r.now().UTC().Add(-contract.HealthRetention)
	first := 0
	for first < len(list) && list[first].At.Before(cutoff) {
		first++
	}
	if first > 0 {
		list = list[first:]
	}
	if len(list) > contract.HealthSamplesPerExit {
		list = list[len(list)-contract.HealthSamplesPerExit:]
	}
	r.byExit[s.Exit] = list
}

// Samples returns a copy of one exit's retained samples, oldest first.
func (r *Retention) Samples(exit contract.ExitID) []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Sample(nil), r.byExit[exit]...)
}

// Prune drops samples older than the retention window for every exit.
func (r *Retention) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-contract.HealthRetention)
	for exit, list := range r.byExit {
		first := 0
		for first < len(list) && list[first].At.Before(cutoff) {
			first++
		}
		if first >= len(list) {
			delete(r.byExit, exit)
			continue
		}
		if first > 0 {
			r.byExit[exit] = append([]Sample(nil), list[first:]...)
		}
	}
}
