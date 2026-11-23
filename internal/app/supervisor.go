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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

// SupervisorOptions wires the detached supervisor. Production leaves the
// command fields empty so the real binary is re-executed; tests replace the
// engine and tray with helper processes.
type SupervisorOptions struct {
	Paths  store.Paths
	LogDir string

	// ReadyFD is the inherited readiness-pipe descriptor (normally 3).
	ReadyFD int

	Engine Command
	Tray   Command
	// TrayCheck replaces the real StatusNotifier probe in tests. nil means the
	// app performs the real watcher/host probe.
	TrayCheck func() error
	// TrayRegistered replaces the bounded registration re-check in tests. nil
	// means the app queries the session bus for this tray child's item.
	TrayRegistered func() error

	ReadyTimeout   time.Duration
	RepairInterval time.Duration
	TermWait       time.Duration
	KillWait       time.Duration
	Now            func() time.Time
}

type childProc struct {
	cmd     *exec.Cmd
	handle  *ProcHandle
	wait    chan int
	control *os.File
	// msgs and readErr belong to exactly one tray generation. A restarted
	// tray gets fresh channels, so a message queued by the previous child can
	// never satisfy or abort the new generation's readiness.
	msgs    chan string
	readErr chan error
	// done stops that generation's control reader when the child is replaced,
	// so a full buffer can never strand a goroutine.
	done chan struct{}
	// readerDone closes after the reader has delivered every line the dead
	// child wrote, so a quit that races the exit status is not stranded.
	readerDone chan struct{}
}

type supervisor struct {
	opts SupervisorOptions
	log  *os.File

	appLock *AppLock
	owner   *Owner
	paths   MarkerPaths

	generation uint64
	startedAt  string
	phase      string
	listeners  Listeners
	logPath    string

	reporter *Reporter
	// ready once the supervisor has reported readiness; later failures are
	// logged but cannot reach the already-exited launcher.
	reported bool

	selfPidfd *os.File

	engine *childProc
	tray   *childProc

	quitRequested bool

	restarts map[string][]time.Time
}

// RunSupervisor owns app.lock and app.owner for one app instance. It starts the
// engine child (run --headless) and the tray child, publishes readiness only
// after status.ready and tray registration, repairs the marker, and stops both
// children on Quit, signals, child loss and its own fail-closed paths.
func RunSupervisor(ctx context.Context, opts SupervisorOptions) error {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 15 * time.Second
	}
	if opts.RepairInterval <= 0 {
		opts.RepairInterval = 10 * time.Second
	}
	if opts.TermWait <= 0 {
		// Engine shutdown closes listeners and in-flight connections with its
		// own 15s bound; give it that long before SIGKILL.
		opts.TermWait = 15 * time.Second
	}
	if opts.KillWait <= 0 {
		opts.KillWait = 3 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LogDir == "" {
		opts.LogDir = opts.Paths.LogDir
	}
	if opts.Engine.Path == "" {
		exe, err := os.Executable()
		if err != nil {
			return internalFailure("app.supervisor", "cannot resolve the RouteLatch executable", err)
		}
		opts.Engine.Path = exe
	}
	if opts.Tray.Path == "" {
		exe, err := os.Executable()
		if err != nil {
			return internalFailure("app.supervisor", "cannot resolve the RouteLatch executable", err)
		}
		opts.Tray.Path = exe
	}
	logFile, err := openAppLog(opts.LogDir)
	if err != nil {
		return internalFailure("app.supervisor", "cannot open the app log", err)
	}
	defer logFile.Close()

	dataDir := opts.Paths.DataDir
	if dataDir == "" {
		dataDir = opts.Paths.StateDir
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return internalFailure("app.supervisor", "cannot resolve the data directory", err)
	}
	logPath := filepath.Join(opts.Paths.LogDir, "routelatch.log")
	s := &supervisor{
		opts: opts, log: logFile,
		paths:     MarkerPathsFor(opts.Paths.InstanceDir, absData, logPath, opts.Paths.Socket),
		logPath:   logPath,
		phase:     MarkerStarting,
		listeners: configuredListeners(opts.Paths.Config),
		restarts:  map[string][]time.Time{},
	}
	s.generation = uint64(opts.Now().UTC().UnixNano())
	s.startedAt = opts.Now().UTC().Format(time.RFC3339Nano)
	if s.generation == 0 {
		s.generation = 1
	}
	if opts.ReadyFD > 0 {
		s.reporter = &Reporter{fd: opts.ReadyFD}
	}
	s.logf("supervisor.start pid=%d data_dir=%s", os.Getpid(), absData)

	// 1. app.lock decides singleton. A held lock goes through the second-run
	// rules, which never take over a live marker.
	lock, lockErr := AcquireAppLock(s.paths.AppLock)
	if lockErr != nil {
		if errors.Is(lockErr, ErrAppLocked) {
			return s.secondRun()
		}
		return conflictExit(contract.ExitConflict, "app.lock", "the app lock is invalid; refusing to start", "app_lock_invalid", lockErr)
	}
	s.appLock = lock
	// Release order on exit is owner then app.lock; defer is a safety net for
	// early startup failures that did not take ownership yet.
	defer func() {
		if s.owner != nil {
			s.owner.Close()
		}
		s.appLock.Release()
	}()

	// 2. Open or create the stable app.owner inode and write starting BEFORE
	// taking its flock, so no previous ready record stays valid.
	owner, err := OpenOwner(s.paths.AppOwner)
	if err != nil {
		return markerInvalid(err)
	}
	s.owner = owner
	if err := s.writeState(MarkerStarting); err != nil {
		return s.abortStartup(markerWriteFailed(err))
	}
	if err := owner.Lock(3, 50*time.Millisecond); err != nil {
		return s.abortStartup(markerWriteFailed(err))
	}

	// 3. Preflight the engine lock before the tray probe: a headless engine is
	// already_running/4 regardless of marker content and must win over a
	// missing tray's unsupported/7.
	free, err := ProbeEngineLock(s.opts.Paths.Lock)
	if err != nil {
		return s.abortStartup(conflictExit(contract.ExitConflict, "app.preflight", "the engine lock path is invalid; refusing to start", "engine_lock_invalid", err))
	}
	if !free {
		return s.abortStartup(conflictExit(contract.ExitAlreadyRunning, "app.preflight",
			"a headless RouteLatch engine is running for this data dir; stop it first or use 'routelatch run --headless'", "headless_running", nil))
	}
	if err := s.checkTray(); err != nil {
		return s.abortStartup(unsupportedTray(err))
	}

	// 4. Hand the tray and engine a pidfd on this supervisor so both observe
	// supervisor death even without PR_SET_PDEATHSIG.
	fd, err := pidfdOpen(os.Getpid(), 0)
	if err != nil {
		return s.abortStartup(unsupportedPIDFD(err))
	}
	s.selfPidfd = os.NewFile(uintptr(fd), "supervisor-pidfd")
	if s.reporter != nil {
		if start, _, _, statErr := readProcStat(os.Getpid()); statErr == nil {
			s.reporter.Emit(ReadyEvent{Event: "identity", Role: "supervisor", PID: os.Getpid(), StartTime: start})
		}
	}

	// 5. Engine first, then tray, then readiness. A failure stops whatever was
	// already started with the same bounded teardown.
	if err := s.spawnEngine(); err != nil {
		return s.abortStartup(err)
	}
	if err := s.waitEngineReady(ctx); err != nil {
		var repair *repairError
		if errors.As(err, &repair) {
			return s.failClosed(repair.err)
		}
		return s.abortStartup(s.mapChildExit("engine", err))
	}
	if err := s.spawnTray(); err != nil {
		return s.abortStartup(err)
	}
	if err := s.waitTrayReady(ctx); err != nil {
		var repair *repairError
		if errors.As(err, &repair) {
			return s.failClosed(repair.err)
		}
		return s.abortStartup(s.mapChildExit("tray", err))
	}
	if err := s.writeState(MarkerReady); err != nil {
		return s.abortStartup(markerWriteFailed(err))
	}
	s.phase = MarkerReady
	s.reportReady()
	s.logf("supervisor.ready pid=%d http=%s socks=%s", os.Getpid(), s.listeners.HTTP, s.listeners.SOCKS)

	return s.loop(ctx)
}

func (s *supervisor) loop(ctx context.Context) error {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	repair := time.NewTicker(s.opts.RepairInterval)
	defer repair.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.shutdown(0, nil)
		case sig := <-sigCh:
			s.logf("supervisor.signal signal=%s", sig)
			return s.shutdown(0, nil)
		case code := <-s.engine.wait:
			if s.quitRequested {
				continue
			}
			if code == 0 {
				// An engine that exits cleanly is a stop request, not a
				// restart; shutdown owns the stopping transition.
				return s.shutdown(0, nil)
			}
			if err := s.engineDied(code); err != nil {
				return err
			}
			if s.quitRequested {
				return nil
			}
		case code := <-s.trayWait():
			if s.quitRequested {
				continue
			}
			if err := s.trayDied(code, ""); err != nil {
				return err
			}
			if s.quitRequested {
				return nil
			}
		case msg := <-s.trayMessages():
			if msg == "quit" {
				s.quitRequested = true
				s.logf("supervisor.quit requested=tray")
				return s.shutdown(0, nil)
			}
			if err := s.handleTrayMessage(msg); err != nil {
				return s.shutdown(0, err)
			}
			if s.quitRequested {
				return nil
			}
		case <-repair.C:
			if err := s.repair(); err != nil {
				return s.failClosed(err)
			}
		}
	}
}

func (s *supervisor) secondRun() error {
	owner, rec, err := ExistingState(s.paths)
	if err != nil {
		if errors.Is(err, ErrAppNotRunning) {
			err = conflictExit(contract.ExitConflict, "app.secondrun",
				"the app lock changed while checking it; retry the start", "app_lock_race", err)
		}
		s.reportError(err)
		return err
	}
	defer owner.Close()
	// Negative readiness veto: the socket can reject an apparently valid
	// ready marker, but it can never create a zero by itself.
	if _, ready := s.statusReady(context.Background()); !ready {
		err := conflictExit(contract.ExitConflict, "app.secondrun",
			"the app marker says ready but the management socket is not ready; retry shortly", "app_socket_veto", nil)
		s.reportError(err)
		return err
	}
	s.logf("supervisor.already_running pid=%d", rec.PID)
	if s.reporter != nil {
		s.reporter.Emit(ReadyEvent{
			Event: "already", PID: rec.PID, Listeners: rec.Listeners, LogPath: rec.LogPath, State: rec.State,
		})
		s.reporter.Close()
	}
	return nil
}

func (s *supervisor) abortStartup(err error) error {
	// A normal startup abort releases both locks. The fail-closed window is
	// entered only after a transition write/repair failure, which has already
	// closed app.owner before children are stopped.
	stopErr := s.stopChildren()
	if err == nil {
		s.closeStartupHandles()
		if stopErr != nil {
			return teardownIncomplete(stopErr)
		}
		return nil
	}
	if stopErr != nil {
		err = teardownIncomplete(stopErr)
	}
	s.reportError(err)
	s.closeStartupHandles()
	return err
}

func (s *supervisor) closeStartupHandles() {
	// Stop the tray's parent-death source only after both children are gone.
	if s.selfPidfd != nil {
		_ = s.selfPidfd.Close()
		s.selfPidfd = nil
	}
	if s.owner != nil {
		s.owner.Close()
		s.owner = nil
	}
	s.appLock.Release()
}

func (s *supervisor) shutdown(code int, cause error) error {
	s.quitRequested = true
	if s.phase != MarkerStopping {
		if err := s.writeState(MarkerStopping); err != nil {
			return s.failClosed(markerWriteFailed(err))
		}
		s.phase = MarkerStopping
	}
	stopErr := s.stopChildren()
	if s.selfPidfd != nil {
		_ = s.selfPidfd.Close()
	}
	s.owner.Close()
	s.owner = nil
	s.appLock.Release()
	if stopErr != nil {
		return teardownIncomplete(stopErr)
	}
	if cause != nil {
		return cause
	}
	if code != 0 {
		return &Failure{Exit: contract.ExitCode(code), Kind: contract.Internal, Op: "app.supervisor", Message: fmt.Sprintf("the app supervisor is exiting with status %d", code)}
	}
	return nil
}

// failClosed is the one documented window where app.owner is released first
// and app.lock is kept until the children/tray are stopped.
func (s *supervisor) failClosed(cause error) error {
	s.quitRequested = true
	// Close app.owner without unlinking it before stopping anything. A
	// concurrent run therefore sees app.lock still held and can only get 6.
	if s.owner != nil {
		s.owner.Close()
		s.owner = nil
	}
	stopErr := s.stopChildren()
	if s.selfPidfd != nil {
		_ = s.selfPidfd.Close()
	}
	s.appLock.Release()
	f := asFailure(cause)
	if stopErr != nil {
		f = teardownIncomplete(stopErr)
	}
	if f.Reason == "" {
		f.Reason = "app_fail_closed"
	}
	s.reportError(f)
	s.logf("supervisor.fail_closed reason=%s", f.Reason)
	return f
}

func (s *supervisor) reportError(err error) {
	if s.reporter == nil || s.reported {
		return
	}
	f := asFailure(err)
	s.reporter.Emit(ReadyEvent{
		Event: "error", Exit: int(f.Exit), Kind: string(f.Kind), Message: f.Message, Reason: f.Reason, Fix: f.Fix,
	})
	s.reporter.Close()
}

func (s *supervisor) stopChildren() error {
	trayErr := s.stopTrayOnly()
	engineErr := s.stopEngineOnly()
	if trayErr != nil {
		return trayErr
	}
	return engineErr
}

func (s *supervisor) stopEngineOnly() error {
	if s.engine == nil {
		return nil
	}
	err := s.engine.handle.Terminate(s.opts.TermWait, s.opts.KillWait)
	s.engine = nil
	return err
}

func (s *supervisor) engineDied(code int) error {
	s.logf("supervisor.engine_exit code=%d", code)
	if code == 0 {
		return s.shutdown(0, nil)
	}
	if terminalExit(code) {
		if err := s.writeState(MarkerStarting); err != nil {
			return s.failClosed(markerWriteFailed(err))
		}
		s.phase = MarkerStarting
		stopErr := s.stopChildren()
		s.owner.Close()
		s.owner = nil
		s.appLock.Release()
		if stopErr != nil {
			return teardownIncomplete(stopErr)
		}
		return &Failure{Exit: contract.ExitCode(code), Kind: exitKind(code), Op: "app.engine", Message: fmt.Sprintf("the engine child exited with documented status %d and was not restarted", code)}
	}
	if err := s.restartAllowed("engine"); err != nil {
		return s.shutdown(1, &Failure{Exit: contract.ExitError, Kind: contract.Internal, Op: "app.engine", Message: "the engine child keeps failing; the app is stopping instead of entering a restart loop", Reason: "restart_limit"})
	}
	if err := s.writeState(MarkerStarting); err != nil {
		return s.failClosed(markerWriteFailed(err))
	}
	s.phase = MarkerStarting
	if err := s.stopEngineOnly(); err != nil {
		return s.failClosed(teardownIncomplete(err))
	}
	if err := s.spawnEngine(); err != nil {
		return s.failClosed(err)
	}
	if err := s.waitEngineReady(context.Background()); err != nil {
		var repair *repairError
		if errors.As(err, &repair) {
			return s.failClosed(repair.err)
		}
		return s.shutdown(0, s.mapChildExit("engine", err))
	}
	// Tray must still be registered (it was not restarted here).
	if err := s.checkTray(); err != nil {
		return s.shutdown(7, unsupportedTray(err))
	}
	if s.tray == nil || s.trayRegistered() != nil {
		return s.trayDied(3, "")
	}
	if err := s.writeState(MarkerReady); err != nil {
		return s.failClosed(markerWriteFailed(err))
	}
	s.phase = MarkerReady
	s.logf("supervisor.engine_restarted pid=%d", s.engine.handle.PID)
	return nil
}

func (s *supervisor) trayDied(code int, reason string) error {
	s.logf("supervisor.tray_exit code=%d reason=%s", code, reason)
	if s.quitRequested {
		return s.shutdown(0, nil)
	}
	if code == 7 || reason == "tray_unavailable" {
		return s.shutdown(7, unsupportedTray(errors.New(firstReason(reason, "the tray child reported that no tray host is available"))))
	}
	// A tray menu/registration/internal failure is terminal: restarting would
	// repeat the same failure, so exit 1 (or the documented child status)
	// without a restart.
	if reason != "" || terminalExit(code) {
		exit := contract.ExitError
		kind := contract.Internal
		if reason == "" && code != 1 {
			exit = contract.ExitCode(code)
			kind = exitKind(code)
		}
		message := "the tray child failed"
		if reason != "" {
			message += ": " + reason
		}
		return s.shutdown(int(exit), &Failure{Exit: exit, Kind: kind, Op: "app.tray", Message: message + "; it was not restarted"})
	}
	if err := s.checkTray(); err != nil {
		return s.shutdown(7, unsupportedTray(err))
	}
	if err := s.restartAllowed("tray"); err != nil {
		return s.shutdown(1, &Failure{Exit: contract.ExitError, Kind: contract.Internal, Op: "app.tray", Message: "the tray child keeps failing; the app is stopping instead of entering a restart loop", Reason: "restart_limit"})
	}
	if err := s.writeState(MarkerStarting); err != nil {
		return s.failClosed(markerWriteFailed(err))
	}
	s.phase = MarkerStarting
	if err := s.stopTrayOnly(); err != nil {
		return s.failClosed(teardownIncomplete(err))
	}
	if s.quitRequested {
		return s.shutdown(0, nil)
	}
	if err := s.spawnTray(); err != nil {
		return s.failClosed(err)
	}
	if err := s.waitTrayReady(context.Background()); err != nil {
		var repair *repairError
		if errors.As(err, &repair) {
			return s.failClosed(repair.err)
		}
		return s.shutdown(0, s.mapChildExit("tray", err))
	}
	if _, ready := s.statusReady(context.Background()); !ready {
		return s.shutdown(1, &Failure{Exit: contract.ExitError, Kind: contract.Internal, Op: "app.tray", Message: "the engine is no longer ready after the tray restarted", Reason: "engine_not_ready"})
	}
	if err := s.writeState(MarkerReady); err != nil {
		return s.failClosed(markerWriteFailed(err))
	}
	s.phase = MarkerReady
	s.logf("supervisor.tray_restarted pid=%d", s.tray.handle.PID)
	return nil
}

func (s *supervisor) handleTrayMessage(msg string) error {
	switch {
	case msg == "lost":
		s.logf("supervisor.tray_lost")
		return s.trayDied(3, "")
	case strings.HasPrefix(msg, "error "):
		reason := strings.TrimSpace(strings.TrimPrefix(msg, "error "))
		if reason == "tray_unavailable" {
			return s.trayDied(7, reason)
		}
		return s.trayDied(1, reason)
	}
	return nil
}

func (s *supervisor) restartAllowed(name string) error {
	now := s.opts.Now().UTC()
	window := s.restarts[name]
	kept := window[:0]
	for _, at := range window {
		if now.Sub(at) < 60*time.Second {
			kept = append(kept, at)
		}
	}
	if len(kept) >= 5 {
		s.restarts[name] = kept
		return fmt.Errorf("restart limit reached")
	}
	s.restarts[name] = append(kept, now)
	return nil
}

func (s *supervisor) spawnEngine() error {
	cmd := s.opts.Engine.exec(s.engineArgs()...)
	cmd.Stdin = nil
	cmd.Stdout = s.log
	cmd.Stderr = s.log
	cmd.ExtraFiles = []*os.File{s.selfPidfd}
	cmd.Env = append(os.Environ(), s.opts.Engine.Env...)
	cmd.Env = append(cmd.Env,
		"ROUTELATCH_PARENT_PIDFD=3",
		"ROUTELATCH_PARENT_PID="+strconv.Itoa(os.Getpid()),
		"ROUTELATCH_APP_ENGINE=1",
	)
	if err := cmd.Start(); err != nil {
		return internalFailure("app.engine", "cannot start the engine child", err)
	}
	handle, err := OpenProc(cmd.Process.Pid)
	if err != nil {
		stopUnpinnedDirectChild(cmd)
		return internalFailure("app.engine", "cannot pin the engine child with a pidfd", err)
	}
	wait := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		wait <- code
	}()
	s.engine = &childProc{cmd: cmd, handle: handle, wait: wait}
	s.reportIdentity("engine", handle)
	s.logf("supervisor.engine_started pid=%d", handle.PID)
	return nil
}

func (s *supervisor) spawnTray() error {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		return internalFailure("app.tray", "cannot create the tray control pipe", err)
	}
	cmd := s.opts.Tray.exec(s.trayArgs()...)
	cmd.Stdin = nil
	cmd.Stdout = s.log
	cmd.Stderr = s.log
	cmd.ExtraFiles = []*os.File{s.selfPidfd, controlW}
	cmd.Env = append(os.Environ(), s.opts.Tray.Env...)
	cmd.Env = append(cmd.Env,
		"ROUTELATCH_TRAY_PARENT_PIDFD=3",
		"ROUTELATCH_TRAY_CONTROL_FD=4",
		"ROUTELATCH_PARENT_PID="+strconv.Itoa(os.Getpid()),
		"ROUTELATCH_LOG_PATH="+s.logPath,
		"ROUTELATCH_DATA_DIR="+s.paths.DataDir,
	)
	if err := cmd.Start(); err != nil {
		_ = controlR.Close()
		_ = controlW.Close()
		return internalFailure("app.tray", "cannot start the tray child", err)
	}
	_ = controlW.Close()
	handle, err := OpenProc(cmd.Process.Pid)
	if err != nil {
		_ = controlR.Close()
		stopUnpinnedDirectChild(cmd)
		return internalFailure("app.tray", "cannot pin the tray child with a pidfd", err)
	}
	wait := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		wait <- code
	}()
	// A fresh pair per tray child is what keeps a queued lost/quit from a
	// previous generation out of this generation's readiness window.
	msgs := make(chan string, 8)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	s.tray = &childProc{cmd: cmd, handle: handle, wait: wait, control: controlR, msgs: msgs, readErr: readErr, done: done, readerDone: readerDone}
	s.reportIdentity("tray", handle)
	go readTrayControl(controlR, msgs, readErr, done, readerDone)
	s.logf("supervisor.tray_started pid=%d", handle.PID)
	return nil
}

// trayWait and trayMessages return nil when no tray is running; a nil channel
// blocks forever in select, which is exactly right while a tray is absent.
func (s *supervisor) trayWait() <-chan int {
	if s.tray == nil {
		return nil
	}
	return s.tray.wait
}

func (s *supervisor) trayMessages() <-chan string {
	if s.tray == nil {
		return nil
	}
	return s.tray.msgs
}

func (s *supervisor) stopTrayOnly() error {
	if s.tray == nil {
		return nil
	}
	tray := s.tray
	err := tray.handle.Terminate(s.opts.TermWait, s.opts.KillWait)
	// The child is dead and its write end is closed, so the reader will reach
	// EOF. Drain what it already queued before discarding the generation: a
	// tray Quit writes "quit" and exits 0, and the exit status may win the
	// select race. Such a quit must still stop the app, not restart the tray.
	if tray.msgs != nil && tray.readerDone != nil {
		deadline := time.NewTimer(500 * time.Millisecond)
		defer deadline.Stop()
	drain:
		for {
			select {
			case msg := <-tray.msgs:
				if msg == "quit" {
					s.quitRequested = true
				}
			case <-tray.readerDone:
				for {
					select {
					case msg := <-tray.msgs:
						if msg == "quit" {
							s.quitRequested = true
						}
					default:
						break drain
					}
				}
			case <-deadline.C:
				break drain
			}
		}
	}
	if tray.done != nil {
		close(tray.done)
	}
	if tray.control != nil {
		_ = tray.control.Close()
	}
	s.tray = nil
	return err
}

func (s *supervisor) waitEngineReady(ctx context.Context) error {
	deadline := time.NewTimer(s.opts.ReadyTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	repair := time.NewTicker(s.opts.RepairInterval)
	defer repair.Stop()
	for {
		select {
		case code := <-s.engine.wait:
			return &childExit{name: "engine", code: code}
		case <-deadline.C:
			return &childExit{name: "engine", code: -2, timeout: true}
		case <-ctx.Done():
			return ctx.Err()
		case <-repair.C:
			if err := s.repair(); err != nil {
				return &repairError{err: err}
			}
		case <-poll.C:
			if listeners, ready := s.statusReady(ctx); ready {
				s.listeners = listeners
				return nil
			}
		}
	}
}

func (s *supervisor) waitTrayReady(ctx context.Context) error {
	tray := s.tray
	if tray == nil {
		return internalFailure("app.tray", "no tray child is running", nil)
	}
	// Bind every channel to this generation before waiting. A message or EOF
	// left over from an earlier tray child lives on its own channels and can
	// never satisfy, abort or busy-loop this readiness window.
	wait := tray.wait
	msgs := tray.msgs
	readErr := tray.readErr
	deadline := time.NewTimer(s.opts.ReadyTimeout)
	defer deadline.Stop()
	repair := time.NewTicker(s.opts.RepairInterval)
	defer repair.Stop()
	for {
		select {
		case code := <-wait:
			return &childExit{name: "tray", code: code}
		case <-deadline.C:
			return &childExit{name: "tray", code: -2, timeout: true}
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			if err == nil || errors.Is(err, io.EOF) {
				// The wait channel reports the exit code; stop selecting this
				// closed generation's error channel.
				readErr = nil
				continue
			}
			return internalFailure("app.tray", "the tray control pipe failed", err)
		case <-repair.C:
			if err := s.repair(); err != nil {
				return &repairError{err: err}
			}
		case msg := <-msgs:
			switch {
			case msg == "ready":
				return nil
			case msg == "quit":
				s.quitRequested = true
				return &childExit{name: "tray", code: 0, quit: true}
			case msg == "lost":
				return &childExit{name: "tray", code: 3, lost: true}
			case strings.HasPrefix(msg, "error "):
				reason := strings.TrimSpace(strings.TrimPrefix(msg, "error "))
				code := 1
				if reason == "tray_unavailable" {
					code = 7
				}
				return &childExit{name: "tray", code: code, reason: reason}
			}
		}
	}
}

func (s *supervisor) statusReady(ctx context.Context) (Listeners, bool) {
	client := &control.Client{Path: s.opts.Paths.Socket, Timeout: time.Second}
	callCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	env, err := client.Call(callCtx, control.Request{Op: control.OpStatus})
	if err != nil || !env.OK {
		return Listeners{}, false
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["ready"] != true {
		return Listeners{}, false
	}
	out := s.listeners
	if raw, ok := data["listeners"].(map[string]any); ok {
		if v, ok := raw["http"].(string); ok {
			out.HTTP = v
		}
		if v, ok := raw["socks"].(string); ok {
			out.SOCKS = v
		}
	}
	return out, true
}

// ownerWrite is the single marker write used by the supervisor. Tests replace
// it to exercise the documented stopping-write failure path through
// RunSupervisor, not through a direct failClosed call.
var ownerWrite = func(o *Owner, r Record) error { return o.Write(r) }

func (s *supervisor) writeState(state string) error {
	s.phase = state
	return s.writeRecordState(state)
}

// writeRecordState publishes a state without changing the supervisor's logical
// phase. Repair uses it so a readiness dip writes starting without trapping the
// phase there: when readiness returns, the next repair may publish ready again.
func (s *supervisor) writeRecordState(state string) error {
	if s.owner == nil {
		return fmt.Errorf("app.owner is not open")
	}
	return ownerWrite(s.owner, s.record(state))
}

// repairState is the state repair may publish right now. A ready phase is not
// enough: §7.5 allows ready only while the engine reports status.ready and the
// current tray item is still registered. Everything else is starting; stopping
// always stays stopping.
func (s *supervisor) repairState() string {
	if s.phase != MarkerReady {
		return s.phase
	}
	if _, ready := s.statusReady(context.Background()); ready && s.trayRegistered() == nil {
		return MarkerReady
	}
	return MarkerStarting
}

func (s *supervisor) repair() error {
	if s.owner == nil {
		return markerInvalid(errors.New("app.owner is not open during repair"))
	}
	state := s.repairState()
	err := s.owner.Verify()
	if err == nil {
		rec, readErr := s.owner.Read()
		if readErr != nil || rec.State != state {
			if writeErr := s.writeRecordState(state); writeErr != nil {
				return markerWriteFailed(writeErr)
			}
		}
		return nil
	}
	if errors.Is(err, ErrDeleted) {
		if recErr := s.owner.RecreateAfterDelete(s.record(state), 3, 50*time.Millisecond); recErr != nil {
			return markerWriteFailed(recErr)
		}
		return nil
	}
	return markerInvalid(err)
}

func (s *supervisor) record(state string) Record {
	return Record{
		State: state, Generation: s.generation, PID: os.Getpid(), StartedAt: s.startedAt,
		DataDir: s.paths.DataDir, Listeners: s.listeners, LogPath: s.logPath,
	}
}

func (s *supervisor) reportIdentity(role string, handle *ProcHandle) {
	if s.reporter == nil {
		return
	}
	s.reporter.Emit(ReadyEvent{Event: "identity", Role: role, PID: handle.PID, StartTime: handle.StartTime})
}

func (s *supervisor) reportReady() {
	if s.reporter == nil || s.reported {
		return
	}
	s.reported = true
	s.reporter.Emit(ReadyEvent{Event: "ready", PID: os.Getpid(), Listeners: s.listeners, LogPath: s.logPath})
	s.reporter.Close()
}

func (s *supervisor) checkTray() error {
	if s.opts.TrayCheck != nil {
		return s.opts.TrayCheck()
	}
	ctx, cancel := context.WithTimeout(context.Background(), trayProbeTimeout)
	defer cancel()
	return ProbeTrayContext(ctx)
}

func (s *supervisor) trayRegistered() error {
	if s.opts.TrayRegistered != nil {
		return s.opts.TrayRegistered()
	}
	if s.tray == nil || s.tray.handle == nil {
		return errors.New("no tray child is running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), trayProbeTimeout)
	defer cancel()
	return TrayRegisteredContext(ctx, s.tray.handle.PID)
}

func (s *supervisor) engineArgs() []string {
	var args []string
	if s.opts.Paths.DataDir != "" {
		args = append(args, "--data-dir", s.opts.Paths.DataDir)
	}
	return append(args, "run", "--headless")
}

func (s *supervisor) trayArgs() []string {
	var args []string
	if s.opts.Paths.DataDir != "" {
		args = append(args, "--data-dir", s.opts.Paths.DataDir)
	}
	return append(args, "__app-tray")
}

func (s *supervisor) logf(format string, args ...any) {
	if s.log == nil {
		return
	}
	msg := control.SanitizeTerminal(fmt.Sprintf(format, args...))
	_, _ = fmt.Fprintf(s.log, "%s app: %s\n", s.opts.Now().UTC().Format("2006-01-02T15:04:05.000Z"), msg)
}

type repairError struct{ err error }

func (e *repairError) Error() string { return "marker repair failed: " + e.err.Error() }
func (e *repairError) Unwrap() error { return e.err }

type childExit struct {
	name    string
	code    int
	timeout bool
	lost    bool
	quit    bool
	reason  string
}

func (e *childExit) Error() string {
	if e.timeout {
		return e.name + " did not become ready in time"
	}
	return fmt.Sprintf("%s exited with status %d", e.name, e.code)
}

func (s *supervisor) mapChildExit(name string, err error) error {
	var ce *childExit
	if errors.As(err, &ce) {
		if ce.quit {
			return nil
		}
		if ce.timeout {
			return &Failure{Exit: contract.ExitTimeout, Kind: contract.Timeout, Op: "app." + name, Message: "the app did not become ready before its deadline", Reason: "readiness_timeout"}
		}
		if ce.lost || (name == "tray" && ce.code == 3) {
			return unsupportedTray(errors.New("tray host was lost"))
		}
		if name == "tray" {
			if ce.reason == "tray_unavailable" || ce.code == 7 {
				return unsupportedTray(errors.New(firstReason(ce.reason, "tray child reported an unsupported tray")))
			}
			if ce.reason != "" {
				return &Failure{Exit: contract.ExitError, Kind: contract.Internal, Op: "app.tray", Message: "the tray child failed: " + ce.reason + "; it was not restarted"}
			}
		}
		if name == "engine" && terminalExit(ce.code) {
			return &Failure{Exit: contract.ExitCode(ce.code), Kind: exitKind(ce.code), Op: "app.engine", Message: fmt.Sprintf("the engine child exited with documented status %d", ce.code)}
		}
		return &Failure{Exit: contract.ExitError, Kind: contract.Internal, Op: "app." + name, Message: ce.Error()}
	}
	return asFailure(err)
}

func firstReason(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}

func terminalExit(code int) bool {
	switch code {
	case 1, 4, 6, 7, 9, 10:
		return true
	}
	return false
}

func exitKind(code int) contract.ErrorKind {
	switch contract.ExitCode(code) {
	case contract.ExitAlreadyRunning, contract.ExitConflict:
		return contract.StateLocked
	case contract.ExitUnsupported:
		return contract.Unsupported
	case contract.ExitStateCorrupt:
		return contract.StateCorrupt
	case contract.ExitDenied:
		return contract.PeerRejected
	case contract.ExitTimeout:
		return contract.Timeout
	default:
		return contract.Internal
	}
}

func configuredListeners(configPath string) Listeners {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return Listeners{}
	}
	var cfg struct {
		HTTP  string `json:"http_listen"`
		SOCKS string `json:"socks_listen"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return Listeners{}
	}
	return Listeners{HTTP: cfg.HTTP, SOCKS: cfg.SOCKS}
}

func readTrayControl(r io.Reader, out chan<- string, errOut chan<- error, done <-chan struct{}, readerDone chan<- struct{}) {
	defer close(readerDone)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 16*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		select {
		case out <- line:
		case <-done:
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case errOut <- err:
	case <-done:
	}
}

// Reporter writes one JSON line per readiness event and closes the inherited
// descriptor exactly once.
type Reporter struct {
	mu     sync.Mutex
	fd     int
	closed bool
}

func (r *Reporter) Emit(ev ReadyEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.fd <= 0 {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	_, _ = syscall.Write(r.fd, raw)
}

func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	_ = syscall.Close(r.fd)
}
