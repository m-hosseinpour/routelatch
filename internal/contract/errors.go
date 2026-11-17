package contract

// ErrorKind is a closed set of failure classes. The set is part of the public
// contract: the HTTP status, the SOCKS5 reply code, the diagnostic eligibility,
// the status text and the JSON error body all derive from it
// (docs/contracts.md §3.1). A package may add context, never a new meaning.
type ErrorKind string

const (
	// InvalidDestination: the target could not be normalized or is not a
	// dialable destination (unspecified address, empty label, bad port).
	InvalidDestination ErrorKind = "invalid_destination"
	// Unsupported: the requested operation exists outside v1: UDP ASSOCIATE,
	// SOCKS5 BIND, an unknown address type, a removed engine transport.
	Unsupported ErrorKind = "unsupported"
	// PolicyDenied: local policy refused before the network. This includes
	// proxy-to-self and destinations that are RouteLatch's own management or
	// listener endpoints. No user rule overrides it.
	PolicyDenied ErrorKind = "policy_denied"
	// Stopped: the service is not listening, so no request can complete.
	Stopped ErrorKind = "stopped"
	// PoolUnavailable: there was no usable exit, or the selected tag did not
	// exist, before anything was sent to an exit. This is a clear pre-reply
	// error (D11); it is never followed by a direct attempt.
	PoolUnavailable ErrorKind = "pool_unavailable"
	// ExitUnreachable: the exit's own transport failed (TCP refused, TLS or
	// pin failure, tracked dispatcher error). Only reported when the engine
	// surfaced the failure; a bare EOF is not this kind.
	ExitUnreachable ErrorKind = "exit_unreachable"
	// HandshakeRejected: the exit explicitly rejected our request or
	// credentials (for example an HTTP CONNECT 407 from the exit, or a typed
	// SOCKS5 reply for the request itself). Not used for VLESS/Trojan, whose
	// rejection is indistinguishable from a dead exit.
	HandshakeRejected ErrorKind = "handshake_rejected"
	// DestinationRefused: a proof-capable exit reported that the destination
	// connection was refused. Only valid with ProofConfirmed, because the
	// exit's reply is the evidence.
	DestinationRefused ErrorKind = "destination_refused"
	// DestinationUnconfirmed: the pool path accepted the request and then the
	// stream ended or stalled without a transport-level error. It is the
	// honest label for a dead exit, a rejected UUID, a refused destination,
	// NXDOMAIN and a clean destination FIN on VLESS/Trojan: the caller cannot
	// tell them apart (docs/contracts.md §2.3).
	DestinationUnconfirmed ErrorKind = "destination_unconfirmed"
	// Timeout: a deadline expired. Path says whether the deadline covered a
	// direct dial, a pool dial, or the post-reply tunnel.
	Timeout ErrorKind = "timeout"
	// Canceled: RouteLatch canceled the operation, for example because the
	// client went away, the mode changed, or the service stopped.
	Canceled ErrorKind = "canceled"
	// ConnectRefused: a direct dial was refused by the destination.
	ConnectRefused ErrorKind = "connect_refused"
	// DNSFailure: the direct path's resolver could not resolve the
	// destination. The pool path never reports this from the local resolver.
	DNSFailure ErrorKind = "dns_failure"
	// TLSFailure: the direct path's TLS handshake or verification failed.
	// It is never eligible for a diagnostic proposal (D09).
	TLSFailure ErrorKind = "tls_failure"
	// SourceFetch: fetching or parsing a subscription/file source failed.
	SourceFetch ErrorKind = "source_fetch"
	// ImportInvalid: an imported record failed validation or sanitization.
	ImportInvalid ErrorKind = "import_invalid"
	// StateCorrupt: the persisted state could not be read or migrated. The
	// service fails closed and does not overwrite the file.
	StateCorrupt ErrorKind = "state_corrupt"
	// StateLocked: another process owns the single-writer lock.
	StateLocked ErrorKind = "state_locked"
	// PeerRejected: a management request's peer credential was not accepted.
	PeerRejected ErrorKind = "peer_rejected"
	// NotFound: the object the user named (a rule, exit, source or proposal)
	// does not exist. It is a command outcome, never a traffic result, and maps
	// to exit code 5 (docs/contracts.md §7.2).
	NotFound ErrorKind = "not_found"
	// ExternalToolFailed: an external desktop or service tool failed
	// (gsettings, kwriteconfig/kreadconfig, systemctl). Nothing may be assumed
	// about its result: RouteLatch reports the failure and never claims the
	// setting was applied or restored.
	ExternalToolFailed ErrorKind = "external_tool_failed"
	// Internal: a defect, not a routing outcome. It is never eligible for a
	// proposal.
	Internal ErrorKind = "internal"
)

// ErrorKinds returns the closed set declared above. Tests and documentation
// consume this one list; every new constant must be added here in the same
// change, otherwise the exit-code coverage test fails.
func ErrorKinds() []ErrorKind {
	return []ErrorKind{
		InvalidDestination,
		Unsupported,
		PolicyDenied,
		Stopped,
		PoolUnavailable,
		ExitUnreachable,
		HandshakeRejected,
		DestinationRefused,
		DestinationUnconfirmed,
		Timeout,
		Canceled,
		ConnectRefused,
		DNSFailure,
		TLSFailure,
		SourceFetch,
		ImportInvalid,
		StateCorrupt,
		StateLocked,
		PeerRejected,
		NotFound,
		ExternalToolFailed,
		Internal,
	}
}

// Path names the egress a failure happened on.
type Path string

const (
	PathDirect Path = "direct"
	PathPool   Path = "pool"
	PathLocal  Path = "local" // before any egress: policy, parse, state
)

// Failure is the typed error that crosses package boundaries. Message must
// already be redacted and safe for a terminal and a log: no credentials, no
// share-link query, no request body, no peer address of a private endpoint
// unless it is the user's own configured listener. Err is kept for
// errors.Is/errors.As only and is never formatted by Error().
type Failure struct {
	Kind        ErrorKind
	Path        Path
	Op          string // stable operation name, e.g. "pool.dial", "proxy.connect"
	Exit        ExitID
	Destination Destination
	Proof       ProofClass // proof class at the moment of failure, if any
	Retryable   bool
	Message     string // redacted, user-facing
	Err         error  // wrapped cause; may contain details that must not be printed raw
}

// Error implements error. It never formats Err: wrapped causes routinely carry
// URLs, peer addresses and other details that must not reach a log or a
// terminal (A09). Construct a Failure with a redacted Message; if Message is
// empty the kind and operation are the only safe text.
func (f *Failure) Error() string {
	switch {
	case f.Message != "":
		return f.Message
	case f.Op != "":
		return f.Op + ": " + string(f.Kind)
	default:
		return string(f.Kind)
	}
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (f *Failure) Unwrap() error { return f.Err }
