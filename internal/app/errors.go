package app

import (
	"errors"
	"fmt"

	"routelatch/internal/contract"
)

// Failure is a documented app-mode outcome. Exit is the process status from
// docs/contracts.md §7.2; Reason is the stable machine-readable detail for
// unsupported pidfd/tray and marker failures.
type Failure struct {
	Exit    contract.ExitCode
	Kind    contract.ErrorKind
	Op      string
	Message string
	Reason  string
	Fix     string
	Err     error
}

func (f *Failure) Error() string { return f.Message }
func (f *Failure) Unwrap() error { return f.Err }

func newFailure(exit contract.ExitCode, kind contract.ErrorKind, op, message, reason string) *Failure {
	return &Failure{Exit: exit, Kind: kind, Op: op, Message: message, Reason: reason}
}

func unsupportedPIDFD(cause error) *Failure {
	f := newFailure(contract.ExitUnsupported, contract.Unsupported, "app.pidfd",
		"pidfd is not available on this system; the app/tray mode needs Linux >= 5.3 and an allowed pidfd_open; use 'routelatch run --headless' or the service instead",
		"pidfd_unavailable")
	f.Fix = "--headless"
	f.Err = cause
	return f
}

func unsupportedTray(cause error) *Failure {
	f := newFailure(contract.ExitUnsupported, contract.Unsupported, "app.tray",
		"no StatusNotifier tray host is available; use 'routelatch run --headless' for a terminal, SSH or service session",
		"tray_unavailable")
	f.Fix = "--headless"
	f.Err = cause
	return f
}

func markerInvalid(cause error) *Failure {
	f := newFailure(contract.ExitConflict, contract.StateLocked, "app.marker",
		"the app ownership marker is invalid or was replaced; refusing to overwrite it", "app_marker_invalid")
	f.Err = cause
	return f
}

func markerWriteFailed(cause error) *Failure {
	f := newFailure(contract.ExitError, contract.Internal, "app.marker",
		"the app ownership marker could not be written; the app is stopping instead of leaving a stale ready record", "app_marker_write_failed")
	f.Err = cause
	return f
}

func conflictExit(exit contract.ExitCode, op, message, reason string, cause error) *Failure {
	f := newFailure(exit, contract.StateLocked, op, message, reason)
	f.Err = cause
	return f
}

func internalFailure(op, message string, cause error) *Failure {
	f := newFailure(contract.ExitError, contract.Internal, op, message, "")
	f.Err = cause
	return f
}

func asFailure(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	return internalFailure("app", fmt.Sprintf("app-mode failure: %v", err), err)
}
