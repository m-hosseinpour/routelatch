package control

import (
	"errors"
	"strconv"

	"routelatch/internal/contract"
)

// Management protocol version of every request (docs/contracts.md §4.6). It is
// deliberately separate from the state schema version.
const protocolVersion = 1

// Request is one management request line. It carries no secret in any field
// that is logged; the source URL lives in Input and is never printed.
type Request struct {
	SchemaVersion int    `json:"schema_version"`
	Op            string `json:"op"`
	Mode          string `json:"mode,omitempty"`
	Input         string `json:"input,omitempty"`
	Kind          string `json:"kind,omitempty"`
	RuleKind      string `json:"rule_kind,omitempty"`
	ID            string `json:"id,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	Key           string `json:"key,omitempty"`
	Value         string `json:"value,omitempty"`
	Note          string `json:"note,omitempty"`
	Path          string `json:"path,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	All           bool   `json:"all,omitempty"`
	AllowEmpty    bool   `json:"allow_empty,omitempty"`
	Direct        bool   `json:"direct,omitempty"`
	Environment   string `json:"environment,omitempty"`
}

// Operation names. They are stable inside the management protocol; the CLI and
// menu are thin wrappers over them.
const (
	OpStatus           = "status"
	OpMode             = "mode"
	OpRulesList        = "rules.list"
	OpRulesAdd         = "rules.add"
	OpRulesRemove      = "rules.remove"
	OpPoolList         = "pool.list"
	OpPoolAdd          = "pool.add"
	OpPoolRemove       = "pool.remove"
	OpPoolRefresh      = "pool.refresh"
	OpPoolTest         = "pool.test"
	OpPoolSelect       = "pool.select"
	OpPoolRerank       = "pool.rerank"
	OpImport           = "import"
	OpDiagnose         = "diagnose"
	OpProposalsList    = "proposals.list"
	OpProposalsAccept  = "proposals.accept"
	OpProposalsReject  = "proposals.reject"
	OpProposalsRecheck = "proposals.recheck"
	OpProposalsClear   = "proposals.clear"
	OpConfigGet        = "config.get"
	OpConfigSet        = "config.set"
	OpConfigPath       = "config.path"
	OpDoctor           = "doctor"
	OpEnv              = "env"
	OpDesktopStatus    = "desktop.status"
	OpDesktopSet       = "desktop.set"
	OpDesktopRestore   = "desktop.restore"
	OpBackup           = "backup"
	OpRestore          = "restore"
)

// CommandName maps an operation to the command string shown in envelopes.
func CommandName(op string) string {
	switch op {
	case OpRulesList:
		return "rules list"
	case OpRulesAdd:
		return "rules add"
	case OpRulesRemove:
		return "rules remove"
	case OpPoolList:
		return "pool list"
	case OpPoolAdd:
		return "pool add"
	case OpPoolRemove:
		return "pool remove"
	case OpPoolRefresh:
		return "pool refresh"
	case OpPoolTest:
		return "pool test"
	case OpPoolSelect:
		return "pool select"
	case OpPoolRerank:
		return "pool rerank"
	case OpProposalsList:
		return "proposals list"
	case OpProposalsAccept:
		return "proposals accept"
	case OpProposalsReject:
		return "proposals reject"
	case OpProposalsRecheck:
		return "proposals recheck"
	case OpProposalsClear:
		return "proposals clear"
	case OpConfigGet, OpConfigSet, OpConfigPath:
		return "config " + op[len("config."):]
	case OpDesktopStatus, OpDesktopSet, OpDesktopRestore:
		return "desktop proxy " + op[len("desktop."):]
	default:
		return op
	}
}

// OpError is a command outcome that is not a routing failure. It carries an
// explicit process exit code for the contract's command-specific codes (for
// example `rules remove` with an unknown id is exit 5). The JSON error kind
// stays inside the frozen ErrorKind set; the exit override is an extra field in
// detail so the CLI does not invent a second error vocabulary.
type OpError struct {
	Kind    contract.ErrorKind
	Message string
	Exit    contract.ExitCode
	Detail  map[string]any
	Cause   error
}

func (e *OpError) Error() string { return e.Message }
func (e *OpError) Unwrap() error { return e.Cause }

// NotFound is the shared exit-5 outcome for absent ids. The contract now has a
// Not Found kind, so no command-specific exit override is needed.
func NotFound(what string) error {
	return &contract.Failure{
		Kind: contract.NotFound, Path: contract.PathLocal, Op: "control.lookup",
		Message: what + " not found",
	}
}

// EnvelopeFor builds the one documented response shape. It returns an error
// envelope for a typed Failure or an OpError; unknown errors are Internal.
func EnvelopeFor(command string, data any, err error) contract.Envelope {
	if err == nil {
		return contract.Envelope{SchemaVersion: protocolVersion, OK: true, Command: command, Data: data}
	}
	body := &contract.ErrorBody{Kind: contract.Internal, Path: contract.PathLocal, Message: "internal error"}
	var failure *contract.Failure
	var opErr *OpError
	switch {
	case errors.As(err, &failure):
		body.Kind = failure.Kind
		body.Path = failure.Path
		body.Message = SanitizeTerminal(failure.Error())
	case errors.As(err, &opErr):
		body.Kind = opErr.Kind
		body.Message = SanitizeTerminal(opErr.Message)
		if len(opErr.Detail) > 0 {
			body.Detail = copyDetail(opErr.Detail)
		}
		if opErr.Exit != 0 {
			if body.Detail == nil {
				body.Detail = map[string]any{}
			}
			body.Detail["exit_code"] = int(opErr.Exit)
		}
	default:
		body.Message = SanitizeTerminal(err.Error())
	}
	if body.Message == "" {
		body.Message = string(body.Kind)
	}
	return contract.Envelope{SchemaVersion: protocolVersion, OK: false, Command: command, Error: body}
}

func copyDetail(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ExitCodeForEnvelope maps a response to its process exit status. A successful
// envelope is 0; a command-specific exit override wins over the kind table.
func ExitCodeForEnvelope(env contract.Envelope) contract.ExitCode {
	if env.OK {
		return contract.ExitOK
	}
	if env.Error == nil {
		return contract.ExitError
	}
	if raw, ok := env.Error.Detail["exit_code"]; ok {
		switch n := raw.(type) {
		case float64:
			return contract.ExitCode(int(n))
		case int:
			return contract.ExitCode(n)
		case string:
			if v, err := strconv.Atoi(n); err == nil {
				return contract.ExitCode(v)
			}
		}
	}
	if code, ok := contract.ExitCodeFor[env.Error.Kind]; ok {
		return code
	}
	return contract.ExitError
}
