package frontend

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"routelatch/internal/contract"
)

// failureOf returns the typed failure that crossed a package boundary. A raw
// error is treated as an internal defect: the contract requires every traffic
// error to be attributable to one kind, and guessing a kind from text would be
// an unreviewed policy decision.
func failureOf(err error) *contract.Failure {
	var f *contract.Failure
	if errors.As(err, &f) {
		return f
	}
	return newFailure(contract.Internal, contract.PathLocal, "frontend", "internal proxy error")
}

func newFailure(kind contract.ErrorKind, path contract.Path, op, message string) *contract.Failure {
	return &contract.Failure{Kind: kind, Path: path, Op: op, Message: message}
}

// dialFailure classifies what a consumer's DialPool/DialDirect returned. A
// deadline owned by this frontend is Timeout; a canceled parent (client gone,
// mode switch, stop) is Canceled. Everything else must already be typed. The
// path is the route that was being dialed, so §3.1 attribution is correct.
func dialFailure(err error, parent context.Context, path contract.Path) *contract.Failure {
	var f *contract.Failure
	if errors.As(err, &f) {
		return f
	}
	if parentErr := parent.Err(); parentErr != nil {
		if errors.Is(parentErr, context.DeadlineExceeded) {
			return newFailure(contract.Timeout, path, "frontend.dial", "dial deadline exceeded")
		}
		return newFailure(contract.Canceled, path, "frontend.dial", "dial canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newFailure(contract.Timeout, path, "frontend.dial", "dial deadline exceeded")
	}
	if errors.Is(err, context.Canceled) {
		return newFailure(contract.Canceled, path, "frontend.dial", "dial canceled")
	}
	return newFailure(contract.Internal, path, "frontend.dial", "internal dial error")
}

// httpStatusFor is the only HTTP mapping (docs/contracts.md §3.2).
func httpStatusFor(kind contract.ErrorKind) int {
	switch kind {
	case contract.InvalidDestination:
		return http.StatusBadRequest // 400
	case contract.Unsupported:
		return http.StatusNotImplemented // 501; 405 is reserved for a rejected method
	case contract.PolicyDenied:
		return http.StatusForbidden // 403
	case contract.Stopped:
		return http.StatusServiceUnavailable // 503
	case contract.PoolUnavailable, contract.ExitUnreachable,
		contract.HandshakeRejected, contract.DestinationRefused:
		return http.StatusBadGateway // 502
	case contract.DestinationUnconfirmed:
		// The contract has no pre-reply status for this kind; if it reaches
		// the frontend before a reply, the honest proxy answer is a gateway
		// failure, never a claimed tunnel.
		return http.StatusBadGateway // 502
	case contract.Timeout:
		return http.StatusGatewayTimeout // 504
	case contract.Canceled:
		return http.StatusBadGateway // 502 (best effort; the peer usually went away)
	case contract.ConnectRefused, contract.DNSFailure:
		return http.StatusBadGateway // 502
	case contract.TLSFailure, contract.SourceFetch, contract.ImportInvalid,
		contract.StateCorrupt, contract.StateLocked, contract.PeerRejected,
		contract.Internal:
		return http.StatusInternalServerError // 500
	default:
		return http.StatusInternalServerError
	}
}

// httpReasonText is the short, redacted description written into a proxy error
// response. Failure.Message is deliberately not printed: a peer address or an
// engine detail is not needed by the client and A09 keeps the response free of
// operational detail.
func httpReasonText(kind contract.ErrorKind) string {
	switch kind {
	case contract.InvalidDestination:
		return "invalid destination"
	case contract.Unsupported:
		return "operation not supported"
	case contract.PolicyDenied:
		return "denied by local policy"
	case contract.Stopped:
		return "service is stopped"
	case contract.PoolUnavailable:
		return "pool unavailable"
	case contract.ExitUnreachable:
		return "exit unreachable"
	case contract.HandshakeRejected:
		return "exit rejected the request"
	case contract.DestinationRefused:
		return "destination rejected by exit"
	case contract.DestinationUnconfirmed:
		return "destination not confirmed"
	case contract.Timeout:
		return "deadline exceeded"
	case contract.Canceled:
		return "canceled"
	case contract.ConnectRefused:
		return "connection refused"
	case contract.DNSFailure:
		return "dns failure"
	case contract.TLSFailure:
		return "tls failure"
	default:
		return "proxy error"
	}
}

// socksDialErrorText chooses the text the pinned library turns into a reply
// code. It matches by substring ("refused" -> 0x05, "network is unreachable"
// -> 0x03, otherwise 0x04; R-1), so every kind except ConnectRefused gets a
// phrase that cannot accidentally select 0x05 or 0x03. ConnectRefused is the
// layer's own classification of a real refusal, so it always gets the phrase
// that selects the documented 0x05; a consumer's redacted message must not be
// able to turn a refusal into 0x04/0x03. R-1's "text is not manipulated" rule
// is about not fabricating a refusal from another kind, which this does not do.
func socksDialErrorText(f *contract.Failure) error {
	if f.Kind == contract.ConnectRefused {
		return fmt.Errorf("connection refused")
	}
	switch f.Kind {
	case contract.InvalidDestination:
		return fmt.Errorf("invalid destination")
	case contract.Unsupported:
		return fmt.Errorf("operation not supported")
	case contract.PolicyDenied:
		return fmt.Errorf("denied by local policy")
	case contract.Stopped:
		return fmt.Errorf("service is stopped")
	case contract.PoolUnavailable:
		return fmt.Errorf("pool unavailable")
	case contract.ExitUnreachable:
		return fmt.Errorf("exit unreachable")
	case contract.HandshakeRejected:
		return fmt.Errorf("exit handshake reported failure")
	case contract.DestinationRefused:
		return fmt.Errorf("exit reported a destination failure")
	case contract.DestinationUnconfirmed:
		return fmt.Errorf("destination not confirmed")
	case contract.Timeout:
		return fmt.Errorf("deadline exceeded")
	case contract.Canceled:
		return fmt.Errorf("canceled")
	case contract.DNSFailure:
		return fmt.Errorf("name resolution failed")
	default:
		return fmt.Errorf("proxy failure")
	}
}
