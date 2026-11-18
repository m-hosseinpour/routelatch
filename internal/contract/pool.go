package contract

import (
	"context"
	"net"
	"time"
)

// ExitID identifies one pool record as a dialable exit. It is stable across
// policy generations and independent of the engine tag used for it.
type ExitID string

// Protocol is the pool protocol of a record.
type Protocol string

const (
	ProtocolVLESS  Protocol = "vless"
	ProtocolTrojan Protocol = "trojan"
	ProtocolSOCKS5 Protocol = "socks5"
	ProtocolHTTP   Protocol = "http"
)

// Supported reports whether v1 can ever dial the protocol. `http` is
// representable but gated on R-2, and any other value is a scheme outside the
// v1 set; both are imported as unsupported and never dialed (docs/contracts.md
// §5.4).
func (p Protocol) Supported() bool {
	switch p {
	case ProtocolVLESS, ProtocolTrojan, ProtocolSOCKS5:
		return true
	}
	return false
}

// ProofClass is the strength of the evidence a pool dial carries. It is the
// single place where the T1 handshake gate lives (docs/contracts.md §2.1–2.2).
type ProofClass string

const (
	// ProofUnknown is the zero value and is never returned with a usable conn.
	ProofUnknown ProofClass = ""
	// ProofConfirmed: the exit's own protocol reply reported success after its
	// connect. This is an exit assertion, not independent verification: an
	// early-reply exit (including Xray's own SOCKS inbound, measured in T1)
	// sends the same reply before dispatching, and a client cannot tell the
	// two apart (R-11). Status must say "the exit reported success", never
	// "the destination is reachable".
	ProofConfirmed ProofClass = "confirmed"
	// ProofAccepted: the pool path accepted the request and returned a usable
	// stream, but the protocol has no destination acknowledgement at all
	// (VLESS/Trojan through the pinned core). Measured: the core returns a nil
	// error in ~23µs while the destination is contacted ~0.5s later, and a
	// dead exit produces the same nil error. A 2xx/0x00 sent with this class
	// must be reported as "accepted, destination not confirmed" everywhere
	// (D11, docs/contracts.md §2.2).
	ProofAccepted ProofClass = "accepted"
)

// PoolDialRequest is one pool attempt. The dialer owns exit selection only
// when Exit is empty; the core never selects an exit for us (a core balancer
// falls through to the default handler, measured in T1).
type PoolDialRequest struct {
	Destination   Destination
	RuleID        RuleID
	PolicyVersion PolicyVersion
	Exit          ExitID   // empty: ranker selects; set: manual selection
	Exclude       []ExitID // per-connection failover attempts; ignored for a manual pin
}

// PoolDialResult is a successful pool dial.
type PoolDialResult struct {
	Conn       net.Conn
	Exit       ExitID
	Tag        string // explicit engine tag; empty when our SOCKS5/HTTP adapter dialed the exit
	Proof      ProofClass
	AcceptedAt time.Time // when the engine or adapter accepted the request
	// Pinned reports that a manual selection chose this exit. The frontend
	// never fails over from a pin: the user's explicit choice must not become
	// a different exit silently (docs/contracts.md §2.2, §2.5).
	Pinned bool
}

// PoolDialer dials approved destinations through one selected exit.
//
// Invariants:
//   - It never dials directly and never calls the core's default handler; the
//     core path names one explicit tag, and a missing or dead tag is a typed
//     *Failure, not an EOF (`spike/xray/FINDINGS.md` §6). The owned SOCKS5/HTTP adapter has no tag,
//     but still dials exactly the requested exit.
//   - It returns either a conn with ProofConfirmed or ProofAccepted, or a
//     *Failure whose Kind is one of PoolUnavailable, ExitUnreachable,
//     HandshakeRejected, DestinationRefused, DestinationUnconfirmed, Timeout,
//     Canceled, PolicyDenied.
//   - It must not report ProofConfirmed unless the exit's reply was parsed and
//     reported success. A bare nil return from the core is ProofAccepted at
//     best.
//   - It does not write to Conn before returning it, so the frontend owns the
//     first byte and can still send an error reply. A stream that fails after a
//     successful return and before any response byte, without an attributable
//     transport error, surfaces DestinationUnconfirmed on the returned conn,
//     not a second dial error; no separate kind exists (docs/contracts.md §3).
//   - Tag names the exit, not the decision: tags are stable per record, so a
//     mode or rule change does not retag a healthy exit. The dialer instead
//     fences req.PolicyVersion against the current policy and rejects a
//     removed or unsupported exit before dialing; it never substitutes a
//     different exit for the requested one.
//   - Request.Exclude lists exits already tried for this same client
//     connection. Automatic selection skips them; a manual pin ignores them
//     because a pin is never silently replaced (docs/contracts.md §2.2).
//     Result.Pinned tells the caller that this exit is a manual choice, so the
//     caller must not fail over either.
type PoolDialer interface {
	DialPool(ctx context.Context, req PoolDialRequest) (PoolDialResult, error)
}

// Dialer is the direct path. Its DNS behaviour is intentionally different:
// the destination hostname may be resolved locally, because the direct path
// is the machine's own egress (D15). It must not be used for a pool decision
// and must not consult pool health.
type Dialer interface {
	DialDirect(ctx context.Context, dst Destination) (net.Conn, error)
}

// ExitHealth is the persisted health picture for one exit. It is a cache of
// measurements, not a policy decision; selection rules live in the pool
// package (docs/contracts.md §6).
type ExitHealth struct {
	Exit                ExitID       `json:"exit"`
	Status              HealthStatus `json:"status"`
	LastSuccessAt       time.Time    `json:"last_success_at,omitzero"`
	LastFailureAt       time.Time    `json:"last_failure_at,omitzero"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	EWMALatencyMS       int64        `json:"ewma_latency_ms"`
	Samples             int          `json:"samples"`
	CooldownUntil       time.Time    `json:"cooldown_until,omitzero"`
	// LastFailureKind is the transport-level kind that caused cooling. It is
	// never a destination-level cause (docs/contracts.md §6.2).
	LastFailureKind ErrorKind `json:"last_failure_kind,omitempty"`
}

// HealthStatus mirrors the persisted record status after health is applied.
type HealthStatus string

const (
	HealthUnknown HealthStatus = "unknown"
	HealthHealthy HealthStatus = "healthy"
	HealthCooling HealthStatus = "cooling"
)
