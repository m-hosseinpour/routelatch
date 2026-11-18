package contract

import "time"

// Numeric contract. Every value here is derivable from the T1 measurements or
// is an explicit product bound; docs/contracts.md §3.4, §5.6 and §6.4 cite the
// same numbers, and a change here is a contract change.
//
// T1 reference points (spike/xray/FINDINGS.md, spike/frontend/FINDINGS.md):
//   - core.Dial returns nil in 22.7–33.8µs; a live destination is contacted
//     ~502ms later (VLESS) or ~103ms later (Trojan); a dead exit also returns
//     nil, so a dial return is not readiness.
//   - Our close/cancel unblocks the caller in 2.4–3.0ms (pending dial 2.34ms,
//     working conn 2.98ms).
//   - The destination observes EOF ~1.001s after close under the exit's
//     default policy and 2.5–3.0ms when the exit has a fast policy; the exit's
//     policy is outside our control for public exits.
//   - Transport failures surface through the tracked-error channel (exit
//     refused at 2.006s, caller deadline at 401ms); destination failures on
//     VLESS/Trojan collapse to EOF and are not attributable.
const (
	// ConnectFailoverExits is the total number of exits one HTTP CONNECT may
	// try before the destination's first byte decides the tunnel. It is two
	// retries after the first exit (docs/contracts.md §2.2, §3.4).
	ConnectFailoverExits = 3
	// ConnectReplayBufferBytes bounds the client bytes kept for replay while a
	// CONNECT waits for the destination's first byte. At the cap, replay stops
	// and the current exit is committed: retrying would require dropping bytes
	// the client already sent (docs/contracts.md §2.2).
	ConnectReplayBufferBytes = 64 << 10

	// PoolDialDeadline bounds one blocking pool dial. It is a real handshake
	// bound only for the owned SOCKS5 adapter, which must finish greeting,
	// authentication and CONNECT before returning. The pinned core has no
	// observable dial phase: core.Dial returns before the exit is contacted, so
	// a stuck core session is classified later, in the tunnel stage, as Timeout
	// or DestinationUnconfirmed (docs/contracts.md §2.1, §3).
	PoolDialDeadline = 10 * time.Second
	// DirectDialDeadline bounds one direct dial.
	DirectDialDeadline = 10 * time.Second
	// LocalCloseDeadline is the acceptance bound for our own side of a mode
	// switch: client conn closed, upstream conn closed, dial context canceled.
	// Measured 2.4–3.0ms; the bound leaves an order of magnitude of headroom.
	LocalCloseDeadline = 50 * time.Millisecond
	// PendingDialCancelDeadline is the acceptance bound for canceling a dial
	// that never completed. Measured 2.34–2.80ms.
	PendingDialCancelDeadline = 50 * time.Millisecond
	// ExitObserveDefaultPolicy is the observed upper bound at which a
	// destination sees the exit-side EOF after our close, when the exit keeps
	// its default policy. It is a documented expectation, not a guarantee we
	// can enforce on a public exit.
	ExitObserveDefaultPolicy = 1500 * time.Millisecond
	// ExitObserveFastPolicy is the acceptance bound used in tests when the
	// exit policy is fast; the measurement was 2.46–3.07ms, and this bound is
	// deliberately loose because it is informational only.
	ExitObserveFastPolicy = 20 * time.Millisecond

	// ProbeTimeout bounds one probe overall, including a pool dial and the
	// first-byte wait.
	ProbeTimeout = 20 * time.Second
	// ProbeFirstByte bounds the wait for the first downlink byte after a probe
	// dial succeeded. It applies only to egress probes; it is never a read
	// deadline on a user tunnel, so idle SSH/WebSocket sessions are not cut.
	ProbeFirstByte = 8 * time.Second
	// HealthProbeInterval is the automatic transport-probe rotation in
	// targeted and all-proxy modes. It sends no destination traffic.
	HealthProbeInterval = 10 * time.Minute
	// ProbeCooldownBase is the first cooldown after a transport-level failure.
	ProbeCooldownBase = 60 * time.Second
	// ProbeCooldownMax caps exponential cooldown.
	ProbeCooldownMax = 15 * time.Minute
	// ProbeCooldownFailures is the consecutive-failure count that reaches the
	// cap; a success resets the counter.
	ProbeCooldownFailures = 4
	// HealthSamplesPerExit and HealthRetention bound the prober's in-memory
	// sample ring, not the persisted state: ExitHealth is the only health
	// summary that survives a restart (docs/contracts.md §6.4).
	HealthSamplesPerExit = 200
	HealthRetention      = 7 * 24 * time.Hour
	// ProposalRetention bounds how long an unanswered proposal stays visible
	// in the in-memory registry; proposals are never persisted (D21).
	ProposalRetention = 30 * 24 * time.Hour
	// SwitchMinDwell prevents flapping between near-equal exits (D16).
	SwitchMinDwell = 5 * time.Minute
	// SwitchMinImprovement is the relative latency gain a candidate must show
	// over the current exit before a switch is allowed.
	SwitchMinImprovement = 0.20
	// SwitchMinSamples is the number of successful samples a candidate needs.
	SwitchMinSamples = 2
	// EWMAAlpha weights the newest latency sample.
	EWMAAlpha = 0.30
	// MaxPoolTestExits bounds one `pool test` invocation; a larger pool needs
	// repeated or filtered runs. It keeps the command inside the socket
	// deadline.
	MaxPoolTestExits = 20

	// Subscription limits (§5.6).
	MaxSourceBytes       = 4 << 20 // 4 MiB
	SourceConnectTimeout = 10 * time.Second
	SourceTotalTimeout   = 30 * time.Second
	SourceMaxRedirects   = 3
	MaxRecordsPerSource  = 2000

	// Management surface limits (§4.6).
	MaxManagementRequest  = 64 << 10 // one JSON request line
	MaxManagementResponse = 4 << 20  // one JSON response line; a 2000-record list fits
	MaxNodeNameRunes      = 120
)
