package routing

import (
	"errors"

	"routelatch/internal/contract"
)

var (
	errZone        = errors.New("address has a zone")
	errUnspecified = errors.New("address is unspecified")
	errPort        = errors.New("port is outside 1..65535")
)

// invalidDestination and unsupportedDestination build typed local failures.
// Messages are static: raw input is never echoed, so a URL with credentials or
// a share link cannot leak through an error path (docs/contracts.md §3.1).
func invalidDestination(msg string) *contract.Failure {
	return &contract.Failure{
		Kind:    contract.InvalidDestination,
		Path:    contract.PathLocal,
		Op:      "routing.normalize",
		Message: msg,
	}
}

func unsupportedDestination(msg string) *contract.Failure {
	return &contract.Failure{
		Kind:    contract.Unsupported,
		Path:    contract.PathLocal,
		Op:      "routing.normalize",
		Message: msg,
	}
}

func policyFailure(msg string) *contract.Failure {
	return &contract.Failure{
		Kind:    contract.Internal,
		Path:    contract.PathLocal,
		Op:      "routing.policy",
		Message: msg,
	}
}
