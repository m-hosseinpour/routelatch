package pool

import (
	"time"

	"routelatch/internal/contract"
)

// CooldownDuration implements docs/contracts.md §6.4: base 60s, doubled per
// consecutive transport failure, capped at 15 minutes. ProbeCooldownFailures
// is the count at which the cap applies, so the reachable steps are
// 60s, 120s, 240s, 900s.
func CooldownDuration(consecutive int) time.Duration {
	if consecutive <= 1 {
		return contract.ProbeCooldownBase
	}
	if consecutive >= contract.ProbeCooldownFailures {
		return contract.ProbeCooldownMax
	}
	d := contract.ProbeCooldownBase
	for i := 1; i < consecutive; i++ {
		d *= 2
		if d >= contract.ProbeCooldownMax {
			return contract.ProbeCooldownMax
		}
	}
	return d
}

// IsTransportFailure reports the only failure classes that may cool an exit:
// the exit's own transport failed, the exit rejected our handshake, or the dial
// phase timed out. destination_unconfirmed, destination_refused, canceled,
// TLS verification of a destination, HTTP status and EOF are not here.
func IsTransportFailure(kind contract.ErrorKind) bool {
	switch kind {
	case contract.ExitUnreachable, contract.HandshakeRejected, contract.Timeout:
		return true
	default:
		return false
	}
}

// ApplyProbe updates health from one measurement. A failure may cool an exit
// only when the prober set ProbeAttributionExit; a local-network outage or a
// result with no exit-level evidence never changes exit health. The function
// writes only state; it never touches the engine.
func ApplyProbe(st *contract.State, res contract.ProbeResult, now time.Time) {
	if res.Exit == "" {
		// Direct diagnostic probes carry no exit; they must not create a
		// phantom health row or cool anything.
		return
	}
	if res.Kind == contract.ProbeDirectTCP || res.Kind == contract.ProbeDirectTLS {
		// Direct probes are diagnostic evidence only (§6.2). Even a caller that
		// mistakenly sets Exit must not cool pool health from them.
		return
	}
	switch res.Outcome {
	case contract.OutcomeSuccess:
		if res.BytesDown > 0 && res.Kind == contract.ProbeEgress {
			recordSuccess(st, res.Exit, latencyMS(res.Duration), now, true)
			return
		}
		if res.Kind == contract.ProbeTransport {
			// A transport success proves the endpoint, not egress. It clears a
			// cooldown but never promotes to healthy (§5.3).
			recordTransportSuccess(st, res.Exit, now)
		}
	case contract.OutcomeUnconfirmed, contract.OutcomeCanceled:
		// No evidence: neither cooling nor success.
		return
	default:
		if res.Failure == "" || res.Attribution != contract.ProbeAttributionExit || !IsTransportFailure(res.Failure) {
			return
		}
		recordFailure(st, res.Exit, res.Failure, now)
	}
}

// RecordUsage records positive evidence from a real user connection: bytes
// came back through this exit. latency is the observed dial/first-byte time.
func RecordUsage(st *contract.State, exit contract.ExitID, latency time.Duration, now time.Time) {
	recordSuccess(st, exit, latencyMS(latency), now, true)
}

// RecordTransportSuccess clears a cooldown after a successful endpoint probe.
func RecordTransportSuccess(st *contract.State, exit contract.ExitID, now time.Time) {
	recordTransportSuccess(st, exit, now)
}

// RecordTransportFailure cools an exit for an attributable transport failure.
func RecordTransportFailure(st *contract.State, exit contract.ExitID, kind contract.ErrorKind, now time.Time) {
	if !IsTransportFailure(kind) {
		return
	}
	recordFailure(st, exit, kind, now)
}

func latencyMS(d time.Duration) int64 {
	ms := d.Milliseconds()
	if d > 0 && ms == 0 {
		return 1
	}
	return ms
}

func healthIndex(st *contract.State, exit contract.ExitID) int {
	for i := range st.Health {
		if st.Health[i].Exit == exit {
			return i
		}
	}
	return -1
}

func recordSuccess(st *contract.State, exit contract.ExitID, latency int64, now time.Time, egress bool) {
	idx := healthIndex(st, exit)
	if idx < 0 {
		st.Health = append(st.Health, contract.ExitHealth{Exit: exit, Status: contract.HealthUnknown})
		idx = len(st.Health) - 1
	}
	h := &st.Health[idx]
	if latency > 0 {
		if h.Samples == 0 || h.EWMALatencyMS == 0 {
			h.EWMALatencyMS = latency
		} else {
			h.EWMALatencyMS = int64(float64(latency)*contract.EWMAAlpha + float64(h.EWMALatencyMS)*(1-contract.EWMAAlpha))
		}
	}
	if h.Samples < contract.HealthSamplesPerExit {
		h.Samples++
	}
	h.LastSuccessAt = now
	h.ConsecutiveFailures = 0
	h.CooldownUntil = time.Time{}
	h.LastFailureKind = ""
	if egress {
		h.Status = contract.HealthHealthy
	} else if h.Status == contract.HealthCooling {
		h.Status = contract.HealthUnknown
	}
	syncRecordStatusAt(st, *h, now)
}

func recordTransportSuccess(st *contract.State, exit contract.ExitID, now time.Time) {
	idx := healthIndex(st, exit)
	if idx < 0 {
		return
	}
	h := &st.Health[idx]
	h.LastSuccessAt = now
	h.ConsecutiveFailures = 0
	h.CooldownUntil = time.Time{}
	h.LastFailureKind = ""
	if h.Status == contract.HealthCooling {
		h.Status = contract.HealthUnknown
	}
	syncRecordStatusAt(st, *h, now)
}

func recordFailure(st *contract.State, exit contract.ExitID, kind contract.ErrorKind, now time.Time) {
	idx := healthIndex(st, exit)
	if idx < 0 {
		st.Health = append(st.Health, contract.ExitHealth{Exit: exit, Status: contract.HealthUnknown})
		idx = len(st.Health) - 1
	}
	h := &st.Health[idx]
	h.ConsecutiveFailures++
	h.LastFailureAt = now
	h.LastFailureKind = kind
	h.Status = contract.HealthCooling
	h.CooldownUntil = now.Add(CooldownDuration(h.ConsecutiveFailures))
	syncRecordStatusAt(st, *h, now)
}

// SyncStatuses normalizes expired cooldowns in a mutation. It keeps the record
// status consistent with the persisted health bucket without claiming health.
func SyncStatuses(st *contract.State, now time.Time) {
	for i := range st.Health {
		h := &st.Health[i]
		if h.Status != contract.HealthCooling {
			continue
		}
		if !h.CooldownUntil.IsZero() && !h.CooldownUntil.After(now) {
			h.Status = contract.HealthUnknown
			h.CooldownUntil = time.Time{}
			h.LastFailureKind = ""
		}
		syncRecordStatusAt(st, *h, now)
	}
}

func syncRecordStatusAt(st *contract.State, h contract.ExitHealth, now time.Time) {
	for i := range st.Records {
		if contract.ExitID(st.Records[i].ID) != h.Exit {
			continue
		}
		rec := &st.Records[i]
		if rec.Status == contract.RecordInvalid || rec.Status == contract.RecordUnsupported {
			return
		}
		switch h.Status {
		case contract.HealthCooling:
			if h.CooldownUntil.After(now) {
				rec.Status = contract.RecordCooling
				rec.StatusReason = cooldownReason(h)
			} else {
				rec.Status = contract.RecordUnknown
				rec.StatusReason = ""
			}
		case contract.HealthHealthy:
			rec.Status = contract.RecordHealthy
			rec.StatusReason = ""
		default:
			rec.Status = contract.RecordUnknown
			rec.StatusReason = ""
		}
		return
	}
}
