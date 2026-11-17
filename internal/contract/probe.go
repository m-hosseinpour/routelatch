package contract

import (
	"context"
	"time"
)

// ProbeKind is one measurement. The set is deliberately small: a probe either
// measures reachability of an exit or of one destination, and it never sends
// the user's request (D09).
type ProbeKind string

const (
	// ProbeTransport measures the exit endpoint only: TCP connect, then TLS
	// (with verification or configured pin) for TLS-shaped transports. It
	// sends no destination traffic and is the only kind allowed while the
	// service is in all-direct mode.
	ProbeTransport ProbeKind = "transport"
	// ProbeEgress dials one configured ProbeConfig target through the exit and
	// waits for the first downlink byte. It is the only honest proof that an
	// exit passed traffic, because a nil pool dial proves nothing (T1).
	ProbeEgress ProbeKind = "egress"
	// ProbeDirectTCP connects directly to the destination. It never sends
	// application bytes and never replays the failed request.
	ProbeDirectTCP ProbeKind = "direct_tcp"
	// ProbeDirectTLS performs a direct TLS handshake with SNI set to the
	// hostname and full certificate verification (D09). It is never used for
	// an IP literal without a name.
	ProbeDirectTLS ProbeKind = "direct_tls"
)

// ProbeTrigger records what caused a probe. Scheduled and failure triggers
// are suppressed in all-direct and while the service is stopped
// (docs/contracts.md §6.3).
type ProbeTrigger string

const (
	TriggerStartup ProbeTrigger = "startup"
	TriggerRotate  ProbeTrigger = "rotation"
	TriggerManual  ProbeTrigger = "manual"
	TriggerFailure ProbeTrigger = "failure" // direct failure -> diagnostics
	TriggerRerank  ProbeTrigger = "rerank"
)

// ProbeOutcome is the measured result. Unconfirmed is not a synonym for
// failure: on a pool path without a destination ack it means only "no
// evidence" (docs/contracts.md §2.3).
type ProbeOutcome string

const (
	OutcomeSuccess     ProbeOutcome = "success" // connected / first byte received
	OutcomeRefused     ProbeOutcome = "refused"
	OutcomeTimeout     ProbeOutcome = "timeout"
	OutcomeDNSFailure  ProbeOutcome = "dns_failure"
	OutcomeTLSFailure  ProbeOutcome = "tls_failure"
	OutcomeRejected    ProbeOutcome = "rejected"    // exit rejected the request/credentials
	OutcomeUnconfirmed ProbeOutcome = "unconfirmed" // no ack, no attributable failure
	OutcomeCanceled    ProbeOutcome = "canceled"
)

// ProbeRequest is one queued or immediate measurement.
type ProbeRequest struct {
	Kind          ProbeKind
	Exit          ExitID
	Destination   Destination
	Trigger       ProbeTrigger
	PolicyVersion PolicyVersion
}

// ProbeAttribution says what a probe result is evidence about. It is the only
// gate pool health may use: a failure may cool an exit only when this field is
// ProbeAttributionExit (docs/contracts.md §6.2).
type ProbeAttribution string

const (
	// ProbeAttributionNone means there is no exit-level evidence: a direct
	// destination probe, an unconfirmed stream, a first-byte stall, or any
	// result that was not evaluated. It never changes exit health.
	ProbeAttributionNone ProbeAttribution = ""
	// ProbeAttributionExit means the observed success or failure belongs to
	// the exit's own transport.
	ProbeAttributionExit ProbeAttribution = "exit"
	// ProbeAttributionLocalOutage means the machine's own network failed, so
	// the exit is not blamed and must not be cooled.
	ProbeAttributionLocalOutage ProbeAttribution = "local_outage"
)

// ProbeResult is what the prober reports. Eligible is computed from the
// evidence rules in docs/contracts.md §3.5 and is the only gate a proposal
// may use: a probe with Eligible false can never create a Proposal. Attribution
// is the only gate pool health may use.
type ProbeResult struct {
	Kind        ProbeKind        `json:"kind"`
	Trigger     ProbeTrigger     `json:"trigger"`
	Exit        ExitID           `json:"exit,omitempty"`
	Destination Destination      `json:"destination"`
	StartedAt   time.Time        `json:"started_at"`
	Duration    time.Duration    `json:"duration_ns"`
	Outcome     ProbeOutcome     `json:"outcome"`
	Failure     ErrorKind        `json:"failure,omitempty"` // zero when Outcome is success
	Attribution ProbeAttribution `json:"attribution,omitempty"`
	Eligible    bool             `json:"eligible"`
	BytesDown   int64            `json:"bytes_down"` // >0 proves the egress path carried data
}

// Prober owns probe concurrency, cooldowns and retention. It is the only
// component that creates background network activity on behalf of diagnostics.
//
// Invariants:
//   - At most MaxConcurrent probes run. Run acquires the same semaphore as
//     queued work, so a caller cannot bypass the concurrency cap. The queue is
//     bounded by QueueLimit and a full queue drops the oldest diagnostic
//     request, never a health sample.
//   - A destination has at most one probe per cooldown window, and one probe in
//     flight regardless of trigger.
//   - CancelQueued cancels queued diagnostic probes and is called when
//     Diagnostics.Enabled becomes false, when the mode changes and when the
//     service stops; it never cancels a probe that is already reporting a
//     health result to the pool (those are bounded by the probe deadline).
//   - No probe runs in all-direct mode or while stopped. There is no hidden
//     direct-versus-pool comparison (D09, §6.3).
//   - Probe results retain only destination, port, kind, timing and outcome;
//     never a URL, header, body, cookie or request byte.
type Prober interface {
	Enqueue(ctx context.Context, req ProbeRequest) error
	Run(ctx context.Context, req ProbeRequest) (ProbeResult, error)
	CancelQueued(ctx context.Context) (int, error)
}

// ProposalStatus is the review state of a diagnostic suggestion.
type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalAccepted ProposalStatus = "accepted"
	ProposalRejected ProposalStatus = "rejected"
	ProposalExpired  ProposalStatus = "expired"
)

// EvidenceKind is a failure that may justify a proposal. Only direct-path
// failures are listed; a pool-path failure, an HTTP status, a CAPTCHA, a rate
// limit, an invalid certificate, a cancel, a mode change and EOF are
// explicitly outside this set (D09, docs/contracts.md §3.5).
type EvidenceKind string

const (
	EvidenceDirectRefused EvidenceKind = "direct_refused"
	EvidenceDirectTimeout EvidenceKind = "direct_timeout"
	EvidenceDirectDNS     EvidenceKind = "direct_dns_failure"
)

// ProposalKind selects what a proposal suggests changing: approving one
// destination, or removing one approved rule. Both kinds share status,
// retention and State.Proposals; accepting either still requires an explicit
// user confirmation and the registry itself changes no routing
// (docs/contracts.md §4.3, §7.1).
type ProposalKind string

const (
	// ProposalRoute suggests adding one destination to the approved list.
	ProposalRoute ProposalKind = "route"
	// ProposalUnroute suggests removing one approved rule. RuleID names it;
	// Destination is what the suggestion is about and Evidence is empty.
	ProposalUnroute ProposalKind = "unroute"
)

// Proposal is a suggestion to add one destination to the approved list or to
// remove one approved rule. It is not a rule and never changes routing by
// itself. An empty Kind is read as ProposalRoute for state written before the
// kind field existed.
type Proposal struct {
	ID          ProposalID     `json:"id"`
	Kind        ProposalKind   `json:"kind"`
	RuleID      RuleID         `json:"rule_id,omitempty"` // ProposalUnroute only
	Destination Destination    `json:"destination"`
	Evidence    []Evidence     `json:"evidence"`
	Status      ProposalStatus `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	DecidedAt   time.Time      `json:"decided_at,omitzero"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Note        string         `json:"note,omitempty"`
}

// ProposalID is stable: a route proposal derives it from the destination, so
// repeated failures update one proposal instead of piling up duplicates; an
// unroute proposal derives it from the rule ID.
type ProposalID string

// Evidence is one stored failure with its provenance. It keeps the address
// and port that were tried, the probe kind, and the time; nothing else.
type Evidence struct {
	Kind      EvidenceKind `json:"kind"`
	At        time.Time    `json:"at"`
	Address   string       `json:"address"` // destination:port as tried
	ProbeKind ProbeKind    `json:"probe_kind"`
	Outcome   ProbeOutcome `json:"outcome"`
}
