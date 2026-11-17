package contract

// ExitCode is the process exit status of every CLI command. The numeric
// values are part of the CLI contract (docs/contracts.md §7.2); scripts may
// rely on them.
type ExitCode int

const (
	ExitOK              ExitCode = 0
	ExitError           ExitCode = 1
	ExitUsage           ExitCode = 2
	ExitNotRunning      ExitCode = 3 // needs the service; management socket absent
	ExitAlreadyRunning  ExitCode = 4
	ExitNotFound        ExitCode = 5
	ExitConflict        ExitCode = 6 // change refused: stale policy, lock held, unsafe restore
	ExitUnsupported     ExitCode = 7 // platform/feature outside v1
	ExitTimeout         ExitCode = 8
	ExitDenied          ExitCode = 9 // peer rejected or permission denied
	ExitStateCorrupt    ExitCode = 10
	ExitPoolUnavailable ExitCode = 11
	ExitCanceled        ExitCode = 12
)

// Envelope is the single JSON shape of a machine-readable CLI response. It is
// emitted with --json on stdout; human output goes to stdout as well, while
// diagnostics and warnings go to stderr.
type Envelope struct {
	SchemaVersion int        `json:"schema_version"`
	OK            bool       `json:"ok"`
	Command       string     `json:"command"`
	Data          any        `json:"data,omitempty"`
	Error         *ErrorBody `json:"error,omitempty"`
}

// ErrorBody is the redacted error form. Detail must never carry credentials,
// subscription URLs, request bodies or traffic content.
type ErrorBody struct {
	Kind    ErrorKind      `json:"kind"`
	Path    Path           `json:"path,omitempty"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// ExitCodeFor maps a failure kind to the documented process exit status. It is
// the only mapping; CLI code must not invent another one.
//
// It is intentionally a data table rather than branching logic so that tests
// can assert every kind is covered. Treat it as read-only; no package may
// mutate it, and a missing kind is a test failure, not "exit 1".
var ExitCodeFor = map[ErrorKind]ExitCode{
	InvalidDestination:     ExitUsage,
	Unsupported:            ExitUnsupported,
	PolicyDenied:           ExitDenied,
	Stopped:                ExitNotRunning,
	PoolUnavailable:        ExitPoolUnavailable,
	ExitUnreachable:        ExitPoolUnavailable,
	HandshakeRejected:      ExitPoolUnavailable,
	DestinationRefused:     ExitPoolUnavailable,
	DestinationUnconfirmed: ExitPoolUnavailable,
	Timeout:                ExitTimeout,
	Canceled:               ExitCanceled,
	ConnectRefused:         ExitError,
	DNSFailure:             ExitError,
	TLSFailure:             ExitError,
	SourceFetch:            ExitError,
	ImportInvalid:          ExitConflict,
	StateCorrupt:           ExitStateCorrupt,
	StateLocked:            ExitConflict,
	PeerRejected:           ExitDenied,
	NotFound:               ExitNotFound,
	ExternalToolFailed:     ExitError,
	Internal:               ExitError,
}
