package diagnostics

import (
	"context"
	"strings"

	"routelatch/internal/contract"
)

// FailureEvent is what the control layer reports to diagnostics after a failed
// direct attempt.
//
// The transport failure is the only evidence source. The extra fields exist so
// that response-shaped signals, a clean EOF and a caller-observed cancel are
// refused explicitly instead of being folded into a transport kind. They are
// not an error taxonomy: HTTP statuses, CAPTCHA pages and rate-limit responses
// never enter contract.ErrorKind in the first place (docs/contracts.md §3.5).
type FailureEvent struct {
	Failure *contract.Failure
	// HTTPStatus is a response status the forwarder passed through. Any value
	// disqualifies: a response is not a direct transport failure.
	HTTPStatus int
	// Challenge marks a CAPTCHA/interstitial page.
	Challenge bool
	// RateLimited marks a 429/503-style rate-limit response.
	RateLimited bool
	// CleanEOF marks a normal stream end, including a destination FIN.
	CleanEOF bool
}

// Refusal reasons are stable, machine-readable and safe to print.
const (
	ReasonNoFailure         = "no_failure"
	ReasonNotDirect         = "not_direct_path"
	ReasonCleanEOF          = "clean_eof"
	ReasonHTTPStatus        = "http_response"
	ReasonChallenge         = "captcha_or_interstitial"
	ReasonRateLimited       = "rate_limited"
	ReasonTLSEvidence       = "tls_failure"
	ReasonCanceled          = "canceled"
	ReasonUnconfirmed       = "unconfirmed_or_eof"
	ReasonPoolPath          = "pool_path_failure"
	ReasonStopped           = "mode_stopped"
	ReasonAllDirect         = "all_direct_no_automatic_probe"
	ReasonAllProxy          = "all_proxy_requires_no_direct_comparison"
	ReasonNotEvidence       = "kind_not_evidence"
	ReasonInvalidTarget     = "invalid_target"
	ReasonRouterUnavailable = "router_unavailable"
	ReasonAlreadyRouted     = "destination_not_direct"
	ReasonNotEligibleClass  = "destination_class_not_other"
	ReasonDisabled          = "diagnostics_disabled"
	ReasonCooldown          = "probe_cooldown"
	ReasonNoProber          = "prober_unavailable"
)

// EligibleKind maps a failure kind to the evidence it may produce. Only the
// three direct kinds in docs/contracts.md §3.5 are eligible; everything else
// is refused by name so the matrix stays auditable.
func EligibleKind(kind contract.ErrorKind, path contract.Path) (contract.EvidenceKind, string, bool) {
	if path != contract.PathDirect {
		return "", ReasonNotDirect, false
	}
	switch kind {
	case contract.ConnectRefused:
		return contract.EvidenceDirectRefused, "", true
	case contract.Timeout:
		return contract.EvidenceDirectTimeout, "", true
	case contract.DNSFailure:
		return contract.EvidenceDirectDNS, "", true
	case contract.TLSFailure:
		return "", ReasonTLSEvidence, false
	case contract.Canceled:
		return "", ReasonCanceled, false
	case contract.Stopped:
		return "", ReasonStopped, false
	case contract.DestinationUnconfirmed:
		return "", ReasonUnconfirmed, false
	case contract.PoolUnavailable, contract.ExitUnreachable,
		contract.HandshakeRejected, contract.DestinationRefused:
		return "", ReasonPoolPath, false
	default:
		// invalid_destination, unsupported, policy_denied, source_fetch,
		// import_invalid, state_*, peer_rejected and internal.
		return "", ReasonNotEvidence, false
	}
}

func validTarget(dst contract.Destination) bool {
	if dst.Port == 0 {
		return false
	}
	switch dst.Kind {
	case contract.DestDomain:
		return dst.Host != "" && !strings.ContainsAny(dst.Host, " /@?#\t\r\n")
	case contract.DestIP:
		return dst.Addr.IsValid() && !dst.Addr.IsUnspecified() && dst.Addr.Zone() == ""
	default:
		return false
	}
}

// evaluate applies the eligibility matrix without side effects. The first
// result is false when no candidate may be created, and the second names the
// refusal reason.
func (m *Manager) evaluate(ctx context.Context, ev FailureEvent) (bool, string) {
	if ev.CleanEOF {
		return false, ReasonCleanEOF
	}
	if ev.HTTPStatus != 0 {
		return false, ReasonHTTPStatus
	}
	if ev.Challenge {
		return false, ReasonChallenge
	}
	if ev.RateLimited {
		return false, ReasonRateLimited
	}
	if ev.Failure == nil {
		return false, ReasonNoFailure
	}
	if _, reason, ok := EligibleKind(ev.Failure.Kind, ev.Failure.Path); !ok {
		return false, reason
	}
	dst := ev.Failure.Destination
	if !validTarget(dst) {
		return false, ReasonInvalidTarget
	}

	switch m.mode() {
	case contract.ModeStopped:
		return false, ReasonStopped
	case contract.ModeDirectAll:
		// Automatic diagnosis does not run in all-direct mode (§6.3).
		return false, ReasonAllDirect
	case contract.ModeProxyAll:
		// In all-proxy a public destination is routed through the pool; a
		// direct probe to justify a rule would be a hidden direct-versus-pool
		// comparison (D09).
		return false, ReasonAllProxy
	}

	if m.opts.Router != nil {
		dec, err := m.opts.Router.Decide(ctx, dst)
		if err != nil {
			return false, ReasonRouterUnavailable
		}
		if dec.Route != contract.RouteDirect {
			return false, ReasonAlreadyRouted
		}
		if dec.Class != contract.ClassOther {
			return false, ReasonNotEligibleClass
		}
	}
	return true, ""
}
