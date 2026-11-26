package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// These tests drive the real supervisor loop with the existing re-exec fake
// engine/tray. The fake tray maps SIGUSR1 to the authenticated
// `restart-engine` lifecycle message and SIGUSR2 to `quit`, and records every
// supervisor->tray lifecycle event in <data>/tray-events.

func sendTraySignal(t *testing.T, data string, sig syscall.Signal) {
	t.Helper()
	pid := readPIDFile(t, filepath.Join(data, "tray.pid"))
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("signal %s to tray %d: %v", sig, pid, err)
	}
}

func trayEvents(t *testing.T, data string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(data, "tray-events"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func waitTrayEvent(t *testing.T, data, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range trayEvents(t, data) {
			if line == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tray never received %q; events: %v", want, trayEvents(t, data))
}

func countTrayEvent(t *testing.T, data, want string) int {
	t.Helper()
	n := 0
	for _, line := range trayEvents(t, data) {
		if line == want {
			n++
		}
	}
	return n
}

func waitEngineChanged(t *testing.T, data string, old int, timeout time.Duration) int {
	t.Helper()
	path := filepath.Join(data, "engine.pid")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid, err := readPIDFileErr(path); err == nil && pid != old {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid, err := readPIDFileErr(path)
	t.Fatalf("engine was not replaced (old %d, current %d, %v)", old, pid, err)
	return 0
}

func waitRestartReady(t *testing.T, h *supervisorHandle, data string, old int, timeout time.Duration) int {
	t.Helper()
	pid := waitEngineChanged(t, data, old, timeout)
	waitMarkerState(t, h.paths.AppOwner, MarkerReady, timeout)
	select {
	case <-h.exited:
		t.Fatalf("supervisor exited instead of publishing ready: %v", <-h.errCh)
	default:
	}
	return pid
}

func TestManualRestartEngineFromTraySuccess(t *testing.T) {
	data := integrationData(t)
	// The replacement stays in starting for 500ms, so the marker transition
	// and the restart window are observable deterministically.
	h := startSupervisor(t, data, "restartslow", "normal")
	firstRecord := h.waitReady(t, 5*time.Second)
	firstEngine := readPIDFile(t, filepath.Join(data, "engine.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	rec := waitMarkerState(t, h.paths.AppOwner, MarkerStarting, 5*time.Second)
	if rec.PID != firstRecord.PID {
		t.Fatalf("restart changed the supervisor pid: %d -> %d", firstRecord.PID, rec.PID)
	}
	// The marker is starting before teardown; the old engine pid file is still
	// current until the replacement actually opens its management socket.
	if pid := readPIDFile(t, filepath.Join(data, "engine.pid")); pid != firstEngine {
		t.Fatalf("engine changed before the old child was torn down: %d -> %d", firstEngine, pid)
	}
	// A concurrent run must never see the restart window as ready.
	if _, err := launchFake(t, data, "normal", 2*time.Second); asFailure(err).Exit != contract.ExitConflict {
		t.Fatalf("second run during manual restart = %v, want exit 6", err)
	}

	second := waitRestartReady(t, h, data, firstEngine, 8*time.Second)
	waitProcessGone(t, firstEngine, 5*time.Second)
	if second == firstEngine {
		t.Fatal("restart reused the old engine pid")
	}
	waitTrayEvent(t, data, "ready 1", 5*time.Second)
	events := trayEvents(t, data)
	if len(events) != 2 || events[0] != "restarting 1" || events[1] != "ready 1" {
		t.Fatalf("lifecycle events = %v, want [restarting 1 ready 1]", events)
	}
	rec, err := readRecordFile(h.paths.AppOwner)
	if err != nil || rec.State != MarkerReady || rec.PID != firstRecord.PID {
		t.Fatalf("marker after restart = %+v, %v", rec, err)
	}
}

// The old engine keeps answering status.ready while the bounded teardown
// waits. The restart must not publish ready from that stale generation.
func TestManualRestartRejectsStaleReadyFromPreviousEngine(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartstubborn", "normal")
	h.waitReady(t, 5*time.Second)
	old := readPIDFile(t, filepath.Join(data, "engine.pid"))
	if !processAlive(old) {
		t.Fatalf("old engine %d is not alive before restart", old)
	}

	sendTraySignal(t, data, syscall.SIGUSR1)
	waitTrayEvent(t, data, "restarting 1", 5*time.Second)
	if !processAlive(old) {
		t.Fatalf("old engine %d exited before the restart teardown window", old)
	}
	// During the stubborn window the old status server still says ready; no
	// ready event may confirm the restart yet.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if countTrayEvent(t, data, "ready 1") != 0 {
			t.Fatal("stale ready from the previous engine confirmed the restart")
		}
		time.Sleep(20 * time.Millisecond)
	}

	waitProcessGone(t, old, 5*time.Second)
	second := waitEngineChanged(t, data, old, 5*time.Second)
	waitMarkerState(t, h.paths.AppOwner, MarkerReady, 5*time.Second)
	if !processAlive(second) {
		t.Fatalf("new engine %d is not alive", second)
	}
	waitTrayEvent(t, data, "ready 1", 5*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 1 {
		t.Fatalf("ready event count = %d, want exactly one", n)
	}
}

// A replacement that cannot even be executed takes the documented fail-closed
// path: exit 1, no ready event, and both singleton locks released.
func TestManualRestartChildStartFailureIsFailClosed(t *testing.T) {
	data := integrationData(t)
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(data, "engine-link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	engine := fakeCommand(data, "fakeengine", "normal")
	engine.Path = link
	h := startSupervisor(t, data, "normal", "normal", func(o *SupervisorOptions) {
		o.Engine = engine
	})
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	sendTraySignal(t, data, syscall.SIGUSR1)
	f := asFailure(h.result(t, 8*time.Second))
	if f.Exit != contract.ExitError || f.Reason != "app_fail_closed" {
		t.Fatalf("engine start failure = %+v, want exit 1/app_fail_closed", f)
	}
	waitTrayEvent(t, data, "restarting 1", 2*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 0 {
		t.Fatalf("start failure emitted %d ready event(s)", n)
	}
	waitProcessGone(t, trayPID, 5*time.Second)
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
}

func TestManualRestartChildFailureStopsWithoutReady(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartfail", "normal")
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	f := asFailure(h.result(t, 8*time.Second))
	if f.Exit != contract.ExitError {
		t.Fatalf("failed replacement engine = %+v, want exit 1", f)
	}
	waitTrayEvent(t, data, "restarting 1", 2*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 0 {
		t.Fatalf("failed restart emitted %d ready event(s)", n)
	}
	waitProcessGone(t, trayPID, 5*time.Second)
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
	paths := pathsFor(t, data)
	if rec, err := readRecordFile(paths.AppOwner); err == nil && rec.State == MarkerReady {
		t.Fatalf("failed restart left a ready marker: %+v", rec)
	}
}

func TestManualRestartReadinessTimeoutIsUnconfirmed(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartnoready", "normal", func(o *SupervisorOptions) {
		o.ReadyTimeout = 400 * time.Millisecond
	})
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	f := asFailure(h.result(t, 8*time.Second))
	if f.Exit != contract.ExitTimeout || f.Reason != "readiness_timeout" {
		t.Fatalf("restart timeout = %+v, want exit 8/readiness_timeout", f)
	}
	waitTrayEvent(t, data, "restarting 1", 2*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 0 {
		t.Fatalf("timed-out restart emitted %d ready event(s)", n)
	}
	waitProcessGone(t, trayPID, 5*time.Second)
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
}

func TestManualRestartTrayUnavailableFailsClosed(t *testing.T) {
	data := integrationData(t)
	var trayGone atomic.Bool
	h := startSupervisor(t, data, "normal", "normal", func(o *SupervisorOptions) {
		o.TrayCheck = func() error {
			if trayGone.Load() {
				return errors.New("no StatusNotifier host")
			}
			return nil
		}
		o.TrayRegistered = func() error {
			if trayGone.Load() {
				return errors.New("tray item is not registered")
			}
			return nil
		}
	})
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))
	trayGone.Store(true)

	sendTraySignal(t, data, syscall.SIGUSR1)
	f := asFailure(h.result(t, 8*time.Second))
	if f.Exit != contract.ExitUnsupported || f.Reason != "tray_unavailable" {
		t.Fatalf("missing tray during restart = %+v, want 7/tray_unavailable", f)
	}
	waitTrayEvent(t, data, "restarting 1", 2*time.Second)
	waitProcessGone(t, trayPID, 5*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 0 {
		t.Fatalf("missing tray emitted %d ready event(s)", n)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
}

func TestManualRestartQuitDuringRestart(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartslow", "normal")
	h.waitReady(t, 5*time.Second)
	old := readPIDFile(t, filepath.Join(data, "engine.pid"))
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	waitMarkerState(t, h.paths.AppOwner, MarkerStarting, 5*time.Second)
	// Quit and exit while the restart is still in progress. The written
	// contract processes it after the restart ends; it must never be lost or
	// leave a child behind.
	sendTraySignal(t, data, syscall.SIGUSR2)

	if err := h.result(t, 10*time.Second); err != nil {
		t.Fatalf("quit during restart = %v, want clean exit", err)
	}
	if processAlive(old) {
		t.Fatalf("old engine %d survived quit during restart", old)
	}
	if processAlive(trayPID) {
		t.Fatalf("tray %d survived quit during restart", trayPID)
	}
	if pid, err := readPIDFileErr(filepath.Join(data, "engine.pid")); err == nil && processAlive(pid) {
		t.Fatalf("replacement engine %d survived quit", pid)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
	rec, err := readRecordFile(h.paths.AppOwner)
	if err != nil || rec.State == MarkerReady {
		t.Fatalf("quit during restart marker = %+v, %v; want a non-ready state", rec, err)
	}
}

// A replacement that reports ready with closed listeners in a running mode is
// not ready for a restart; only the stored stopped mode is.
func TestManualRestartRejectsReadyWithClosedListeners(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartbadpaused", "normal", func(o *SupervisorOptions) {
		o.ReadyTimeout = 500 * time.Millisecond
	})
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	f := asFailure(h.result(t, 8*time.Second))
	if f.Exit != contract.ExitTimeout || f.Reason != "readiness_timeout" {
		t.Fatalf("closed-listener replacement = %+v, want exit 8/readiness_timeout", f)
	}
	waitProcessGone(t, trayPID, 5*time.Second)
	if n := countTrayEvent(t, data, "ready 1"); n != 0 {
		t.Fatalf("closed-listener replacement emitted %d ready event(s)", n)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
}

func TestManualRestartKeepsStoredStoppedMode(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "stopped", "normal")
	h.waitReady(t, 5*time.Second)
	old := readPIDFile(t, filepath.Join(data, "engine.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	second := waitRestartReady(t, h, data, old, 8*time.Second)
	waitProcessGone(t, old, 5*time.Second)
	if second == old {
		t.Fatal("stopped-mode restart reused the old engine pid")
	}
	// Readiness succeeded although the replacement reports closed listeners:
	// a restart is not allowed to silently resume a stored stopped mode.
	waitTrayEvent(t, data, "ready 1", 5*time.Second)
	events := trayEvents(t, data)
	if len(events) != 2 || events[0] != "restarting 1" || events[1] != "ready 1" {
		t.Fatalf("stopped restart events = %v", events)
	}
}

// Quit while a restart is going to fail must still leave no orphan; the
// restart failure owns the documented exit.
func TestQuitDuringFailingRestartLeavesNoChildren(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "restartnoready", "normal", func(o *SupervisorOptions) {
		o.ReadyTimeout = 700 * time.Millisecond
	})
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))

	sendTraySignal(t, data, syscall.SIGUSR1)
	waitMarkerState(t, h.paths.AppOwner, MarkerStarting, 5*time.Second)
	sendTraySignal(t, data, syscall.SIGUSR2)

	f := asFailure(h.result(t, 10*time.Second))
	if f.Exit != contract.ExitTimeout || f.Reason != "readiness_timeout" {
		t.Fatalf("quit during failing restart = %+v, want the documented timeout", f)
	}
	if processAlive(trayPID) {
		t.Fatalf("tray %d survived the failed restart", trayPID)
	}
	if pid, err := readPIDFileErr(filepath.Join(data, "engine.pid")); err == nil && processAlive(pid) {
		t.Fatalf("engine %d survived the failed restart", pid)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	assertUnlocked(t, h.paths.AppOwner)
}

// A restart request queued while an automatic restart is in progress is
// ignored, not replayed after readiness. The automatic restart itself must
// emit exactly one restarting/ready pair.
func TestManualRestartDuringAutomaticRestartIsIgnored(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "autorestart", "normal")
	h.waitReady(t, 5*time.Second)
	old := readPIDFile(t, filepath.Join(data, "engine.pid"))

	waitMarkerState(t, h.paths.AppOwner, MarkerStarting, 5*time.Second)
	sendTraySignal(t, data, syscall.SIGUSR1)

	waitEngineChanged(t, data, old, 8*time.Second)
	waitMarkerState(t, h.paths.AppOwner, MarkerReady, 8*time.Second)
	waitTrayEvent(t, data, "ready 1", 5*time.Second)
	if got := readCounter(filepath.Join(data, "engine-launch-count")); got != 2 {
		t.Fatalf("engine launches = %d, want initial + automatic replacement only", got)
	}
	// If the queued request were replayed, a third generation would appear.
	time.Sleep(600 * time.Millisecond)
	if got := readCounter(filepath.Join(data, "engine-launch-count")); got != 2 {
		t.Fatalf("queued manual request restarted again: %d launches", got)
	}
	events := trayEvents(t, data)
	if len(events) != 2 || events[0] != "restarting 1" || events[1] != "ready 1" {
		t.Fatalf("automatic restart events = %v, want one pair", events)
	}
}

// Manual restarts must not spend the automatic engine restart budget. Six
// successful manual restarts exceed the five-per-minute automatic limit; a
// subsequent crash must still restart automatically.
func TestManualRestartDoesNotConsumeAutomaticBudget(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "normal")
	h.waitReady(t, 5*time.Second)
	old := readPIDFile(t, filepath.Join(data, "engine.pid"))
	for i := 0; i < 6; i++ {
		sendTraySignal(t, data, syscall.SIGUSR1)
		old = waitRestartReady(t, h, data, old, 8*time.Second)
	}
	waitTrayEvent(t, data, "ready 6", 5*time.Second)

	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	replacement := waitEngineChanged(t, data, old, 8*time.Second)
	waitMarkerState(t, h.paths.AppOwner, MarkerReady, 8*time.Second)
	waitTrayEvent(t, data, "ready 7", 5*time.Second)
	if !processAlive(replacement) {
		t.Fatalf("automatic restart after manual restarts did not spawn a live engine")
	}
	if got := countTrayEvent(t, data, "restarting 7"); got != 1 {
		t.Fatalf("automatic restart event count = %d, want one", got)
	}
	select {
	case <-h.exited:
		t.Fatalf("supervisor exited during automatic restart: %v", <-h.errCh)
	default:
	}
}

func TestRestartEngineIgnoredWhenNotReady(t *testing.T) {
	s := &supervisor{phase: MarkerStarting}
	if err := s.handleTrayMessage("restart-engine"); err != nil {
		t.Fatalf("ignored restart returned %v", err)
	}
	if s.restartSeq != 0 || s.engineGeneration != 0 {
		t.Fatalf("ignored restart advanced tokens: seq=%d generation=%d", s.restartSeq, s.engineGeneration)
	}
}

// A late completion from an older attempt can never publish ready for the
// current one, and a full or closed event pipe never blocks the supervisor.
func TestRestartLifecycleTokenAndClosedEventPipe(t *testing.T) {
	readR, writeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd := int(writeW.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{tray: &childProc{events: writeW, eventFD: fd}, restartSeq: 2}
	s.emitLifecycleReady(1)
	_ = writeW.Close()
	raw, err := io.ReadAll(readR)
	_ = readR.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("stale token published %q", raw)
	}

	readR, writeW, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd = int(writeW.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	s = &supervisor{tray: &childProc{events: writeW, eventFD: fd}, restartSeq: 2}
	s.emitLifecycleReady(2)
	_ = writeW.Close()
	raw, err = io.ReadAll(readR)
	_ = readR.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "ready 2\n" {
		t.Fatalf("current token published %q, want ready 2", raw)
	}

	readR, writeW, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = readR.Close()
	fd = int(writeW.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	s = &supervisor{tray: &childProc{events: writeW, eventFD: fd}}
	done := make(chan struct{})
	go func() {
		s.emitLifecycle("ready")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle write blocked with a closed tray reader")
	}
	_ = writeW.Close()
}

// A tray that is alive but not reading fills the pipe; the event write must
// return with the event dropped, not stall the supervisor loop.
func TestLifecycleEventDropsWhenPipeFull(t *testing.T) {
	readR, writeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readR.Close()
	defer writeW.Close()
	fd := int(writeW.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	for {
		_, err := syscall.Write(fd, buf)
		if err == nil {
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		t.Fatal(err)
	}
	s := &supervisor{tray: &childProc{events: writeW, eventFD: fd}}
	done := make(chan struct{})
	go func() {
		s.emitLifecycle("ready")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle write blocked on a full tray pipe")
	}
}
