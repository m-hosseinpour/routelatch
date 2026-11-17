package engine

import (
	"context"
	"errors"

	"routelatch/internal/contract"
)

// This file is the small, explicit error vocabulary the engine speaks. Every
// failure names one contract kind and the path it happened on; a caller never
// has to parse a message (docs/contracts.md §3.1).

func poolUnavailable(message string, exit contract.ExitID, dst contract.Destination) *contract.Failure {
	return &contract.Failure{
		Kind:        contract.PoolUnavailable,
		Path:        contract.PathPool,
		Op:          "engine.dial",
		Exit:        exit,
		Destination: dst,
		Message:     message,
	}
}

func exitUnreachable(message string, exit contract.ExitID, dst contract.Destination, cause error) *contract.Failure {
	return &contract.Failure{
		Kind:        contract.ExitUnreachable,
		Path:        contract.PathPool,
		Op:          "engine.dial",
		Exit:        exit,
		Destination: dst,
		Message:     message,
		Err:         cause,
	}
}

func handshakeRejected(message string, exit contract.ExitID, dst contract.Destination, cause error) *contract.Failure {
	return &contract.Failure{
		Kind:        contract.HandshakeRejected,
		Path:        contract.PathPool,
		Op:          "engine.socks5",
		Exit:        exit,
		Destination: dst,
		Message:     message,
		Err:         cause,
	}
}

func destinationRefused(message string, exit contract.ExitID, dst contract.Destination, cause error) *contract.Failure {
	return &contract.Failure{
		Kind:        contract.DestinationRefused,
		Path:        contract.PathPool,
		Op:          "engine.socks5",
		Exit:        exit,
		Destination: dst,
		Proof:       contract.ProofConfirmed,
		Message:     message,
		Err:         cause,
	}
}

func destinationUnconfirmed(exit contract.ExitID, dst contract.Destination, cause error) *contract.Failure {
	return &contract.Failure{
		Kind:        contract.DestinationUnconfirmed,
		Path:        contract.PathPool,
		Op:          "engine.session",
		Exit:        exit,
		Destination: dst,
		Message:     "the exit accepted the request and the stream ended without attributable evidence",
		Err:         cause,
	}
}

// ctxFailure maps an ended context to the contract's two kinds. A deadline is
// Timeout, anything else is Canceled because RouteLatch or its caller stopped
// the work.
func ctxFailure(ctx context.Context, exit contract.ExitID, dst contract.Destination) *contract.Failure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &contract.Failure{
			Kind:        contract.Timeout,
			Path:        contract.PathPool,
			Op:          "engine.dial",
			Exit:        exit,
			Destination: dst,
			Retryable:   true,
			Message:     "the deadline expired",
			Err:         ctx.Err(),
		}
	}
	return &contract.Failure{
		Kind:        contract.Canceled,
		Path:        contract.PathPool,
		Op:          "engine.dial",
		Exit:        exit,
		Destination: dst,
		Message:     "the operation was canceled",
		Err:         ctx.Err(),
	}
}
