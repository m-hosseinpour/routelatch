package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// Command is the executable and fixed argument prefix for a child process.
// Tests use it to replace the real supervisor/engine/tray with a helper.
type Command struct {
	Path string
	Args []string
	Env  []string
}

func (c Command) exec(more ...string) *exec.Cmd {
	args := make([]string, 0, len(c.Args)+len(more))
	args = append(args, c.Args...)
	args = append(args, more...)
	return exec.Command(c.Path, args...)
}

// ReadyEvent is one line of the supervisor-to-launcher readiness pipe.
type ReadyEvent struct {
	Event     string    `json:"event"` // identity | ready | already | error
	Role      string    `json:"role,omitempty"`
	PID       int       `json:"pid,omitempty"`
	StartTime uint64    `json:"starttime,omitempty"`
	Exit      int       `json:"exit,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Message   string    `json:"message,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Fix       string    `json:"fix,omitempty"`
	Listeners Listeners `json:"listeners,omitempty"`
	LogPath   string    `json:"log_path,omitempty"`
	State     string    `json:"state,omitempty"`
}

// LaunchOptions configures the detached app launcher.
type LaunchOptions struct {
	Command      Command
	LogDir       string
	Env          []string
	ReadyTimeout time.Duration
	TermWait     time.Duration
	KillWait     time.Duration
}

// Result is the successful startup payload.
type Result struct {
	PID            int
	Listeners      Listeners
	LogPath        string
	AlreadyRunning bool
	State          string
}

const defaultReadyTimeout = 10 * time.Second

// Launch detaches the supervisor, follows its readiness pipe and returns only
// after engine readiness and tray registration. Every signal it sends during
// teardown goes through a pidfd, except the one documented raw SIGKILL for its
// own unreaped direct child when that child's pidfd could not be opened.
func Launch(ctx context.Context, opts LaunchOptions) (*Result, error) {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = defaultReadyTimeout
	}
	if opts.TermWait <= 0 {
		opts.TermWait = 5 * time.Second
	}
	if opts.KillWait <= 0 {
		opts.KillWait = 3 * time.Second
	}
	// The pidfd capability is checked before any process exists.
	if err := ProbePIDFD(); err != nil {
		return nil, unsupportedPIDFD(err)
	}

	logFile, err := openAppLog(opts.LogDir)
	if err != nil {
		return nil, internalFailure("app.launch", "cannot open the app log", err)
	}
	defer logFile.Close()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, internalFailure("app.launch", "cannot create the readiness pipe", err)
	}
	defer readyR.Close()

	cmd := opts.Command.exec()
	cmd.Stdin = nil // /dev/null for a detached process
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.Env = append(os.Environ(), opts.Command.Env...)
	cmd.Env = append(cmd.Env, opts.Env...)
	cmd.Env = append(cmd.Env, "ROUTELATCH_READY_FD=3", "ROUTELATCH_LOG="+filepath.Join(opts.LogDir, "routelatch.log"))

	if err := cmd.Start(); err != nil {
		_ = readyW.Close()
		return nil, internalFailure("app.launch", "cannot start the app supervisor", err)
	}
	_ = readyW.Close()

	supervisor, err := OpenProc(cmd.Process.Pid)
	if err != nil {
		// The child is a direct, unreaped child, so its PID cannot be reused
		// before waitpid. This raw SIGKILL is the single documented exception.
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, internalFailure("app.launch", "cannot pin the app supervisor with a pidfd; it was stopped before startup", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	events := make(chan ReadyEvent, 16)
	readErr := make(chan error, 1)
	go readEvents(readyR, events, readErr)

	var children []*ProcHandle
	defer func() {
		for _, h := range children {
			h.Close()
		}
		supervisor.Close()
	}()

	deadline := time.NewTimer(opts.ReadyTimeout)
	defer deadline.Stop()

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// The pipe closed without a terminal event. Wait briefly for
				// the supervisor so its documented exit code is preserved.
				code, waitErr := waitExit(waitCh, 3*time.Second)
				if code != 0 {
					return nil, startupExitFailure(code, waitErr)
				}
				return nil, internalFailure("app.launch", "the app supervisor exited before reporting readiness", waitErr)
			}
			switch ev.Event {
			case "identity":
				if ev.Role == "supervisor" {
					if ev.PID != supervisor.PID || ev.StartTime != supervisor.StartTime {
						return nil, abortTeardown(supervisor, children, opts, "the supervisor identity did not match the pinned pidfd")
					}
					continue
				}
				handle, openErr := OpenProc(ev.PID)
				if openErr != nil {
					if errors.Is(openErr, ErrGone) {
						continue
					}
					return nil, abortTeardown(supervisor, children, opts, "a reported child could not be pinned with a pidfd")
				}
				if handle.StartTime != ev.StartTime {
					handle.Close()
					return nil, abortTeardown(supervisor, children, opts, "a reported child identity did not match its pidfd")
				}
				children = append(children, handle)
			case "ready":
				if ev.PID != supervisor.PID {
					return nil, abortTeardown(supervisor, children, opts, "the ready report did not come from the pinned supervisor")
				}
				return &Result{PID: ev.PID, Listeners: ev.Listeners, LogPath: ev.LogPath}, nil
			case "already":
				return &Result{PID: ev.PID, Listeners: ev.Listeners, LogPath: ev.LogPath, AlreadyRunning: true, State: ev.State}, nil
			case "error":
				return nil, &Failure{
					Exit: contract.ExitCode(ev.Exit), Kind: contract.ErrorKind(ev.Kind),
					Op: "app.supervisor", Message: ev.Message, Reason: ev.Reason, Fix: ev.Fix,
				}
			}
		case err := <-readErr:
			if !errors.Is(err, io.EOF) && err != nil {
				return nil, abortTeardown(supervisor, children, opts, "the readiness pipe failed")
			}
			code, waitErr := waitExit(waitCh, 3*time.Second)
			if code != 0 {
				return nil, startupExitFailure(code, waitErr)
			}
			return nil, internalFailure("app.launch", "the app supervisor exited before reporting readiness", waitErr)
		case <-deadline.C:
			_ = readyR.Close()
			if err := Teardown(supervisor, children, opts.TermWait, opts.KillWait); err != nil {
				return nil, teardownIncomplete(err)
			}
			_, _ = waitExit(waitCh, 2*time.Second)
			return nil, &Failure{
				Exit: contract.ExitTimeout, Kind: contract.Timeout, Op: "app.launch",
				Message: fmt.Sprintf("the app did not become ready within %s; the supervisor and its reported children were stopped", opts.ReadyTimeout), Reason: "readiness_timeout",
			}
		case <-ctx.Done():
			_ = readyR.Close()
			if err := Teardown(supervisor, children, opts.TermWait, opts.KillWait); err != nil {
				return nil, teardownIncomplete(err)
			}
			_, _ = waitExit(waitCh, 2*time.Second)
			return nil, &Failure{
				Exit: contract.ExitCanceled, Kind: contract.Canceled, Op: "app.launch",
				Message: "app startup was canceled; the supervisor and its reported children were stopped",
			}
		}
	}
}

func readEvents(r io.Reader, out chan<- ReadyEvent, errOut chan<- error) {
	defer close(out)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		var ev ReadyEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			errOut <- fmt.Errorf("malformed readiness event: %w", err)
			return
		}
		out <- ev
	}
	if err := scanner.Err(); err != nil {
		errOut <- err
		return
	}
	errOut <- io.EOF
}

func waitExit(ch <-chan error, timeout time.Duration) (int, error) {
	select {
	case err := <-ch:
		if err == nil {
			return 0, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), err
		}
		return -1, err
	case <-time.After(timeout):
		return -1, fmt.Errorf("supervisor did not exit within %s", timeout)
	}
}

func startupExitFailure(code int, cause error) *Failure {
	f := &Failure{
		Exit: contract.ExitCode(code), Kind: contract.Internal, Op: "app.launch",
		Message: fmt.Sprintf("the app supervisor exited with status %d before readiness", code), Err: cause,
	}
	switch contract.ExitCode(code) {
	case contract.ExitAlreadyRunning:
		f.Kind = contract.StateLocked
		f.Message = "another RouteLatch engine or app owns this data dir"
	case contract.ExitConflict:
		f.Kind = contract.StateLocked
		f.Message = "the app could not start because its state is held or invalid; retry shortly"
	case contract.ExitUnsupported:
		f.Kind = contract.Unsupported
		f.Message = "the app supervisor reported an unsupported feature"
	case contract.ExitTimeout:
		f.Kind = contract.Timeout
	}
	return f
}

func abortTeardown(supervisor *ProcHandle, children []*ProcHandle, opts LaunchOptions, message string) *Failure {
	_ = Teardown(supervisor, children, opts.TermWait, opts.KillWait)
	return teardownIncomplete(errors.New(message))
}

func teardownIncomplete(cause error) *Failure {
	f := &Failure{
		Exit: contract.ExitError, Kind: contract.Internal, Op: "app.teardown",
		Message: "some app processes could not be confirmed dead; inspect them before retrying", Reason: "teardown_incomplete",
		Err: cause,
	}
	return f
}

func openAppLog(logDir string) (*os.File, error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(logDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(logDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
