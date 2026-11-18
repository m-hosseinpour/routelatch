package contract

import (
	"context"
	"net"
	"time"
)

// ConnID is stable for the lifetime of one accepted connection.
type ConnID string

// ConnKind is the client-facing protocol of a connection.
type ConnKind string

const (
	ConnHTTPConnect   ConnKind = "http_connect"
	ConnHTTPForward   ConnKind = "http_forward"
	ConnSOCKS5Connect ConnKind = "socks5_connect"
)

// ConnState is the lifecycle state reported by the registry.
type ConnState string

const (
	ConnDialing ConnState = "dialing"
	ConnOpen    ConnState = "open"
	ConnClosing ConnState = "closing"
	ConnClosed  ConnState = "closed"
)

// ConnSpec is what a frontend registers before it dials. The Decision carries
// both the policy version and the mode that admitted the connection; there is
// no second copy to disagree with it.
type ConnSpec struct {
	ID          ConnID
	Kind        ConnKind
	Destination Destination
	Decision    Decision
	StartedAt   time.Time
}

// ConnInfo is a read-only snapshot for status and for tests that assert
// "no pooled connection survives a mode change". Proof and Reason exist so
// status and logs can distinguish a confirmed dial from an accepted one and
// name the matrix rule that fired (docs/contracts.md §2.5).
type ConnInfo struct {
	ID            ConnID         `json:"id"`
	Kind          ConnKind       `json:"kind"`
	Destination   Destination    `json:"destination"`
	Route         Route          `json:"route"`
	Mode          Mode           `json:"mode"`
	Reason        DecisionReason `json:"reason"`
	Proof         ProofClass     `json:"proof,omitempty"`
	State         ConnState      `json:"state"`
	PolicyVersion PolicyVersion  `json:"policy_version"`
	Exit          ExitID         `json:"exit,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
}

// ConnHandle is the frontend's ownership token for one connection. The
// registry, not the frontend, decides when a conflicting connection closes.
//
// Invariants:
//   - Context is canceled when the registry closes the connection; every dial
//     and copy loop must be derived from it.
//   - SetUpstream and SetClient are idempotent and must be called as soon as
//     the conn exists, because the frontend libraries return no handle and
//     Close is the only way to unblock their copies.
//   - SetProof is called once, immediately after PoolDial returns and before
//     the first byte is copied, so status and logs can report confirmed vs
//     accepted. On a failover-capable HTTP CONNECT the frontend calls it when
//     the serving exit is committed (the destination's first byte), because a
//     pre-byte failure may replace the exit. It is set-once: a later different
//     value must be ignored and never overwrite the class the client was told.
//     Direct connections leave the proof empty.
//   - Close is safe to call more than once and from any goroutine; after it
//     returns both conns are closed and the context is canceled.
type ConnHandle interface {
	ID() ConnID
	Context() context.Context
	SetClient(net.Conn) error
	SetUpstream(net.Conn) error
	SetExit(ExitID)
	SetProof(ProofClass)
	Close() error
}

// CloseCause names why a close sweep ran.
type CloseCause string

const (
	CloseModeSwitch CloseCause = "mode_switch"
	CloseRuleChange CloseCause = "rule_change"
	CloseStop       CloseCause = "stop"
	CloseShutdown   CloseCause = "shutdown"
	CloseClientGone CloseCause = "client_gone"
	CloseExitGone   CloseCause = "exit_gone" // record removed or rejected by validation
)

// CloseReport is the evidence a mode or rule change needs before it may
// announce success (D13). It is also what the acceptance checks assert on.
type CloseReport struct {
	Cause         CloseCause    `json:"cause"`
	FromVersion   PolicyVersion `json:"from_version"`
	ToVersion     PolicyVersion `json:"to_version"`
	Closed        int           `json:"closed"`
	PendingCancel int           `json:"pending_canceled"`
	Kept          int           `json:"kept"` // connections the new policy still allows
	Duration      time.Duration `json:"duration_ns"`
	Errors        []error       `json:"errors,omitempty"`
}

// Registry is the single owner of live and in-progress user connections.
//
// Invariants:
//   - SetPolicyVersion seeds the fence with the persisted PolicyVersion before
//     the service accepts traffic. It never moves backwards and closes nothing;
//     a later Register with an older Decision.PolicyVersion is rejected.
//   - Register atomically fences the policy version: after a new version is
//     committed, no spec whose Decision.PolicyVersion is older may enter. A
//     rejected spec returns a *Failure with Kind Canceled.
//   - CloseConflicting closes exactly the connections whose route the new
//     policy no longer allows, using keep as the authoritative answer: keep
//     reports whether the new router would still produce the same
//     Decision.Route for that destination. A nil keep closes everything, and
//     a connection that is still Dialing is closed unless keep returns true
//     (D13 allows keeping compatible connections, never an unproven route).
//   - CloseConflicting does not return until every closed connection's client
//     and upstream socket is closed and its dial context is canceled. The
//     local part must finish within LocalCloseDeadline; the remote exit's own
//     observation is outside our control (docs/contracts.md §3.4).
//   - keep is called while holding the registry fence, so it must be pure and
//     must not call back into the registry.
//   - Snapshot never blocks a dial and never exposes a conn's secret-bearing
//     upstream address in CLI output.
type Registry interface {
	SetPolicyVersion(v PolicyVersion)
	Register(spec ConnSpec) (ConnHandle, error)
	CloseConflicting(ctx context.Context, cause CloseCause, next PolicyVersion, keep func(ConnInfo) bool) (CloseReport, error)
	CloseAll(ctx context.Context, cause CloseCause) (CloseReport, error)
	Snapshot() []ConnInfo
}
