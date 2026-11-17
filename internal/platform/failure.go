package platform

import (
	"context"
	"errors"

	"routelatch/internal/contract"
)

func localFailure(kind contract.ErrorKind, op, message string) *contract.Failure {
	return &contract.Failure{
		Kind:    kind,
		Path:    contract.PathLocal,
		Op:      op,
		Message: message,
	}
}

func commandFailure(op string, err error) *contract.Failure {
	f := localFailure(contract.Internal, op, "a desktop settings command failed; nothing was assumed about the result")
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.Kind = contract.Timeout
		f.Message = "a desktop settings command timed out"
	case errors.Is(err, context.Canceled):
		f.Kind = contract.Canceled
		f.Message = "a desktop settings command was canceled"
	}
	f.Err = err
	return f
}
