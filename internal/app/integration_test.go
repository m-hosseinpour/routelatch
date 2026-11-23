package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

// TestAppHelper is the re-exec entry point for the app integration harness. It
// is inert during a normal test run and never used by production code.
func TestAppHelper(t *testing.T) {
	mode := os.Getenv("RL_TEST_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "supervisor":
		os.Exit(helperSupervisor())
	case "fakeengine":
		os.Exit(helperEngine())
	case "faketray":
		os.Exit(helperTray())
	}
	os.Exit(2)
}

func helperPaths() store.Paths {
	data := os.Getenv("RL_TEST_DATA")
	paths, err := store.ResolvePathsFor(data, "", "", "", "")
	if err != nil {
		panic(err)
	}
	return paths
}

func helperCommand(mode string) Command {
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestAppHelper", "--"},
		Env: []string{
			"RL_TEST_MODE=" + mode,
			"RL_TEST_DATA=" + os.Getenv("RL_TEST_DATA"),
			"RL_TEST_FAKE=" + os.Getenv("RL_TEST_FAKE"),
			"RL_TEST_LOSSES=" + os.Getenv("RL_TEST_LOSSES"),
		},
	}
}

// fakeCommand builds one re-exec helper for an in-process supervisor test.
func fakeCommand(data, mode, fake string) Command {
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestAppHelper", "--", data},
		Env:  []string{"RL_TEST_MODE=" + mode, "RL_TEST_DATA=" + data, "RL_TEST_FAKE=" + fake},
	}
}

func helperSupervisor() int {
	paths := helperPaths()
	ready, _ := strconv.Atoi(os.Getenv("ROUTELATCH_READY_FD"))
	fake := os.Getenv("RL_TEST_FAKE")
	opts := SupervisorOptions{
		Paths: paths, LogDir: paths.LogDir, ReadyFD: ready,
		Engine: helperCommand("fakeengine"), Tray: helperCommand("faketray"),
		TrayCheck:      func() error { return nil },
		TrayRegistered: func() error { return nil },
		ReadyTimeout:   5 * time.Second,
		RepairInterval: 250 * time.Millisecond,
		TermWait:       2 * time.Second,
		KillWait:       2 * time.Second,
	}
	if fake == "trayfail" {
		opts.TrayCheck = func() error { return errors.New("no StatusNotifier host") }
	}
	err := RunSupervisor(context.Background(), opts)
	if err == nil {
		return 0
	}
	var f *Failure
	if errors.As(err, &f) {
		return int(f.Exit)
	}
	return 1
}

func helperEngine() int {
	paths := helperPaths()
	fake := os.Getenv("RL_TEST_FAKE")
	if fake == "exit4" {
		return 4
	}
	if fake == "exit10" {
		return 10
	}
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 1
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return 4
	}
	parent, cancel, _, err := ParentWatchFromEnv()
	if err != nil {
		return 1
	}
	if parent == nil {
		parent = context.Background()
	}
	if cancel != nil {
		defer cancel()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)

	if fake == "noready" {
		select {
		case <-sig:
		case <-parent.Done():
		}
		return 0
	}
	if fake == "slowready" {
		select {
		case <-time.After(800 * time.Millisecond):
		case <-sig:
			return 0
		case <-parent.Done():
			return 0
		}
	}
	firstGeneration := false
	if fake == "enginedie" {
		f, err := os.OpenFile(filepath.Join(paths.DataDir, "engine-died-once"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			firstGeneration = true
			_ = f.Close()
		} else {
			// The replacement engine stays in starting long enough for a
			// second run to observe the transition instead of a stale ready.
			select {
			case <-time.After(time.Second):
			case <-sig:
				return 0
			case <-parent.Done():
				return 0
			}
		}
	}
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: helperStatus{}, AllowStale: true})
	if err != nil {
		return 1
	}
	if err := srv.Start(context.Background()); err != nil {
		return 1
	}
	defer srv.Close()
	_ = os.WriteFile(filepath.Join(paths.DataDir, "engine.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	if firstGeneration {
		select {
		case <-time.After(300 * time.Millisecond):
			return 3
		case <-sig:
			return 0
		case <-parent.Done():
			return 0
		}
	}
	select {
	case <-sig:
		if fake == "slowstop" {
			time.Sleep(800 * time.Millisecond)
		}
	case <-parent.Done():
	}
	return 0
}

type helperStatus struct{}

func (helperStatus) Handle(context.Context, control.Request) (any, error) {
	return map[string]any{
		"ready": true,
		"listeners": map[string]any{
			"http": "127.0.0.1:19001", "socks": "127.0.0.1:19002",
		},
	}, nil
}

func helperTray() int {
	opts, err := TrayOptionsFromEnv()
	if err != nil {
		return 1
	}
	fake := os.Getenv("RL_TEST_FAKE")
	controlFile := os.NewFile(uintptr(opts.ControlFD), "control")
	if controlFile == nil {
		return 1
	}
	defer controlFile.Close()
	send := func(line string) {
		_, _ = controlFile.WriteString(line + "\n")
	}
	gone, err := watchParent(opts.ParentFD, opts.ExpectedPID)
	if err != nil {
		return 1
	}
	_ = os.WriteFile(filepath.Join(helperPaths().DataDir, "tray.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	if f, err := os.OpenFile(filepath.Join(helperPaths().DataDir, "tray-launches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
	}
	switch fake {
	case "trayerror":
		send("error tray_menu_failed")
		time.Sleep(50 * time.Millisecond)
		return 1
	case "trayunavailable":
		send("error tray_unavailable")
		time.Sleep(50 * time.Millisecond)
		return 7
	}
	if fake == "traylostmany" {
		losses := 8
		if n, convErr := strconv.Atoi(os.Getenv("RL_TEST_LOSSES")); convErr == nil && n > 0 {
			losses = n
		}
		dir := helperPaths().DataDir
		counter := filepath.Join(dir, "tray-lost-count")
		count := readCounter(counter)
		if count < losses {
			writeCounter(counter, count+1)
			if count > 0 {
				// Stay in starting long enough that a lost/quit left on a
				// previous generation's channel would be selected first.
				time.Sleep(200 * time.Millisecond)
			}
			send("ready")
			time.Sleep(150 * time.Millisecond)
			send("lost")
			return 3
		}
		time.Sleep(200 * time.Millisecond)
		send("ready")
		_ = os.WriteFile(filepath.Join(dir, "tray-final-ready"), []byte("1"), 0o600)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(sig)
		for {
			select {
			case <-gone:
				return 0
			case <-sig:
				return 0
			}
		}
	}
	if fake == "trayquitrace" {
		once := filepath.Join(helperPaths().DataDir, "tray-quit-once")
		if f, err := os.OpenFile(once, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
			// The explicit Quit races the exit status; the supervisor must
			// honor the message whichever select case wins.
			send("ready")
			send("quit")
			return 0
		}
	}
	if fake == "traylost" && fileExists(filepath.Join(helperPaths().DataDir, "tray-lost-once")) {
		// Keep the replacement in starting so a second run observes the
		// transition before ready returns.
		time.Sleep(300 * time.Millisecond)
	}
	send("ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	var after <-chan time.Time
	switch fake {
	case "trayquit":
		after = time.After(300 * time.Millisecond)
	case "traylost":
		// Lose the host once; the replacement tray must be able to register.
		once := filepath.Join(helperPaths().DataDir, "tray-lost-once")
		if f, err := os.OpenFile(once, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
			after = time.After(300 * time.Millisecond)
		}
	case "trayexit":
		after = time.After(300 * time.Millisecond)
	case "trayexit1":
		after = time.After(300 * time.Millisecond)
	}
	for {
		select {
		case <-gone:
			return 0
		case <-sig:
			return 0
		case <-after:
			switch fake {
			case "trayquit":
				send("quit")
				return 0
			case "traylost":
				send("lost")
				return 3
			case "trayexit1":
				return 1
			default:
				return 0
			}
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readCounter(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(raw))
	return n
}

func writeCounter(path string, n int) {
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600)
}

func integrationData(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp/opencode", "rl-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func launchFake(t *testing.T, data, fake string, timeout time.Duration) (*Result, error) {
	t.Helper()
	cmd := Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestAppHelper", "--", data},
		Env:  []string{"RL_TEST_MODE=supervisor", "RL_TEST_DATA=" + data, "RL_TEST_FAKE=" + fake},
	}
	return Launch(context.Background(), LaunchOptions{
		Command: cmd, LogDir: filepath.Join(data, "log"), ReadyTimeout: timeout,
		TermWait: 3 * time.Second, KillWait: 2 * time.Second,
	})
}

func waitGroupEmpty(t *testing.T, pgid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		members, err := GroupMembers(pgid)
		if err == nil && len(members) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	members, _ := GroupMembers(pgid)
	t.Fatalf("process group %d still has live members: %+v", pgid, members)
}

func TestLaunchReadyAlreadyRunningAndQuit(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "normal", 5*time.Second)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	if result.PID <= 1 || result.AlreadyRunning {
		t.Fatalf("first result = %+v", result)
	}
	paths := pathsFor(t, data)
	logPath := filepath.Join(paths.LogDir, "routelatch.log")
	if result.LogPath != logPath {
		t.Fatalf("reported log = %q, want %q", result.LogPath, logPath)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("reported log does not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.LogDir, "app.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a separate app.log was created: %v", err)
	}
	raw, err := os.ReadFile(paths.AppOwner)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ParseRecord(raw)
	if err != nil || rec.State != MarkerReady || rec.PID != result.PID {
		t.Fatalf("marker = %+v, %v", rec, err)
	}

	second, err := launchFake(t, data, "normal", 5*time.Second)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if !second.AlreadyRunning || second.PID != result.PID {
		t.Fatalf("second result = %+v, want already_running from %d", second, result.PID)
	}

	// Detached: the supervisor is a session leader with no controlling tty.
	if got := procField(t, result.PID, 3); got != strconv.Itoa(result.PID) {
		t.Fatalf("session id = %s, want %d", got, result.PID)
	}
	if got := procField(t, result.PID, 2); got != strconv.Itoa(result.PID) {
		t.Fatalf("process group = %s, want %d", got, result.PID)
	}
	if got := procField(t, result.PID, 4); got != "0" {
		t.Fatalf("controlling tty = %s, want none", got)
	}

	sup, err := OpenProc(result.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	if err := sup.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !sup.WaitExit(5 * time.Second) {
		t.Fatal("supervisor did not exit after SIGTERM")
	}
	waitGroupEmpty(t, result.PID, 3*time.Second)
	raw, err = os.ReadFile(paths.AppOwner)
	if err != nil {
		t.Fatal(err)
	}
	rec, err = ParseRecord(raw)
	if err != nil || rec.State != MarkerStopping {
		t.Fatalf("stopping marker = %+v, %v", rec, err)
	}
	assertUnlocked(t, paths.AppOwner)
	assertUnlocked(t, paths.AppLock)
}

func procField(t *testing.T, pid, index int) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	end := lastIndex(text, ')')
	fields := splitFields(text[end+2:])
	if index >= len(fields) {
		t.Fatalf("stat %d has %d fields", pid, len(fields))
	}
	return fields[index]
}

func lastIndex(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\n' || s[i] == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func assertUnlocked(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("%s is still locked: %v", path, err)
	}
}

func TestLaunchReadinessTimeoutTeardown(t *testing.T) {
	data := integrationData(t)
	_, err := launchFake(t, data, "noready", 400*time.Millisecond)
	f := asFailure(err)
	if f.Exit != 8 {
		t.Fatalf("timeout failure = %+v (exit %d), want 8", f, f.Exit)
	}
	paths := pathsFor(t, data)
	assertUnlocked(t, paths.AppOwner)
	assertUnlocked(t, paths.AppLock)
	assertUnlocked(t, paths.Lock)
}

func TestLaunchTrayUnavailable(t *testing.T) {
	data := integrationData(t)
	_, err := launchFake(t, data, "trayfail", 5*time.Second)
	f := asFailure(err)
	if f.Exit != 7 || f.Reason != "tray_unavailable" {
		t.Fatalf("failure = %+v, want 7/tray_unavailable", f)
	}
	paths := pathsFor(t, data)
	assertUnlocked(t, paths.AppLock)
	if _, err := os.Stat(paths.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tray failure started an engine: socket exists")
	}
}

// A tray menu failure is an internal terminal failure, not "unsupported": it
// must exit 1 and must never start a replacement tray.
func TestTrayMenuFailureIsTerminalExitOne(t *testing.T) {
	data := integrationData(t)
	_, err := launchFake(t, data, "trayerror", 5*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitError {
		t.Fatalf("tray menu failure = %+v, want exit 1", f)
	}
	raw, readErr := os.ReadFile(filepath.Join(data, "tray-launches"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if launches := strings.Count(string(raw), "\n"); launches != 1 {
		t.Fatalf("tray menu failure restarted the tray: %d launches", launches)
	}
}

func TestTrayUnavailableMessageMapsToSeven(t *testing.T) {
	data := integrationData(t)
	_, err := launchFake(t, data, "trayunavailable", 5*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitUnsupported || f.Reason != "tray_unavailable" {
		t.Fatalf("tray unavailable message = %+v, want 7/tray_unavailable", f)
	}
}

func TestLaunchHeadlessConflict(t *testing.T) {
	data := integrationData(t)
	paths := pathsFor(t, data)
	if err := os.MkdirAll(paths.InstanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, err = launchFake(t, data, "normal", 5*time.Second)
	f := asFailure(err)
	if f.Exit != 4 {
		t.Fatalf("headless conflict exit = %d, want 4 (%+v)", f.Exit, f)
	}
}

func TestLaunchEngineTerminalExit(t *testing.T) {
	data := integrationData(t)
	_, err := launchFake(t, data, "exit4", 5*time.Second)
	f := asFailure(err)
	if f.Exit != 4 {
		t.Fatalf("engine exit4 = %+v, want exit 4", f)
	}
}

func TestTrayQuitStopsChildren(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "trayquit", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitGroupEmpty(t, result.PID, 5*time.Second)
	paths := pathsFor(t, data)
	raw, err := os.ReadFile(paths.AppOwner)
	if err == nil {
		rec, perr := ParseRecord(raw)
		if perr == nil && rec.State == MarkerReady {
			t.Fatal("Quit left a stale ready marker")
		}
	}
	assertUnlocked(t, paths.AppLock)
}

func TestSupervisorSIGKILLLeavesNoChildren(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "normal", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	members, err := GroupMembers(result.PID)
	if err != nil || len(members) == 0 {
		t.Fatalf("expected engine/tray children, got %+v (%v)", members, err)
	}
	sup, err := OpenProc(result.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	if err := sup.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !sup.WaitExit(5 * time.Second) {
		t.Fatal("supervisor did not die")
	}
	waitGroupEmpty(t, result.PID, 5*time.Second)
}

func TestTrayLossRestartsWithNewTray(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "traylost", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLaunched(t, result.PID) })
	paths := pathsFor(t, data)
	firstTray := readPIDFile(t, filepath.Join(data, "tray.pid"))
	waitMarkerState(t, paths.AppOwner, MarkerStarting, 5*time.Second)
	// The restart window must never be reported as ready to a second run.
	if _, err := launchFake(t, data, "normal", 2*time.Second); asFailure(err).Exit != contract.ExitConflict {
		t.Fatalf("second run during tray restart = %v, want exit 6", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := readPIDFileErr(filepath.Join(data, "tray.pid"))
		if err == nil && pid != firstTray {
			if rec, readErr := readRecordFile(paths.AppOwner); readErr == nil && rec.State == MarkerReady {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("tray loss did not restart the tray to a ready state")
}

func TestTrayQuitRacingExitStopsApp(t *testing.T) {
	for i := 0; i < 6; i++ {
		data := integrationData(t)
		result, err := launchFake(t, data, "trayquitrace", 5*time.Second)
		if err != nil {
			t.Fatalf("iteration %d launch: %v", i, err)
		}
		sup, err := OpenProc(result.PID)
		if err != nil {
			t.Fatal(err)
		}
		if !sup.WaitExit(5 * time.Second) {
			sup.Close()
			t.Fatalf("iteration %d: tray quit was stranded after a restart", i)
		}
		sup.Close()
		waitGroupEmpty(t, result.PID, 5*time.Second)
	}
}

// --- R13-B remaining checks -------------------------------------------------

type launched struct {
	result *Result
	err    error
}

type supervisorHandle struct {
	cancel context.CancelFunc
	errCh  chan error
	exited chan struct{}
	paths  store.Paths
}

func startSupervisor(t *testing.T, data, engineFake, trayFake string, configure ...func(*SupervisorOptions)) *supervisorHandle {
	t.Helper()
	paths := pathsFor(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	opts := SupervisorOptions{
		Paths: paths, LogDir: paths.LogDir,
		Engine:         fakeCommand(data, "fakeengine", engineFake),
		Tray:           fakeCommand(data, "faketray", trayFake),
		TrayCheck:      func() error { return nil },
		TrayRegistered: func() error { return nil },
		ReadyTimeout:   10 * time.Second,
		RepairInterval: 100 * time.Millisecond,
		TermWait:       2 * time.Second,
		KillWait:       2 * time.Second,
	}
	for _, fn := range configure {
		fn(&opts)
	}
	h := &supervisorHandle{cancel: cancel, errCh: make(chan error, 1), exited: make(chan struct{}), paths: paths}
	go func() {
		h.errCh <- RunSupervisor(ctx, opts)
		close(h.exited)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.exited:
		case <-time.After(5 * time.Second):
			t.Errorf("in-process supervisor did not exit")
		}
	})
	return h
}

func (h *supervisorHandle) waitReady(t *testing.T, timeout time.Duration) Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rec, err := readRecordFile(h.paths.AppOwner); err == nil && rec.State == MarkerReady {
			return rec
		}
		select {
		case <-h.exited:
			t.Fatalf("supervisor exited before readiness: %v", <-h.errCh)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("supervisor did not become ready")
	return Record{}
}

func (h *supervisorHandle) result(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-h.exited:
		return <-h.errCh
	case <-time.After(timeout):
		t.Fatal("supervisor did not exit")
		return nil
	}
}

func readRecordFile(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	return ParseRecord(raw)
}

func waitMarkerState(t *testing.T, path, state string, timeout time.Duration) Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rec, err := readRecordFile(path); err == nil && rec.State == state {
			return rec
		}
		time.Sleep(20 * time.Millisecond)
	}
	rec, err := readRecordFile(path)
	t.Fatalf("marker did not reach %q: %+v (%v)", state, rec, err)
	return Record{}
}

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	_, state, _, err := readProcStat(pid)
	return err == nil && state != 'Z'
}

func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d is still alive", pid)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("bad pid file %s: %q", path, raw)
	}
	return pid
}

func noProcessWithArg(t *testing.T, needle string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		for _, name := range procPIDs(t) {
			raw, err := os.ReadFile(filepath.Join(procRoot, name, "cmdline"))
			if err == nil && strings.Contains(string(raw), needle) {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("a helper process for %s survived", needle)
}

func stopLaunched(t *testing.T, pid int) {
	t.Helper()
	h, err := OpenProc(pid)
	if err != nil {
		return
	}
	defer h.Close()
	_ = h.Signal(syscall.SIGTERM)
	if !h.WaitExit(5 * time.Second) {
		t.Fatalf("supervisor %d did not stop", pid)
	}
	waitGroupEmpty(t, pid, 5*time.Second)
}

func assertLocked(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		t.Fatalf("%s is not locked", path)
	}
}

// TestTrayRestartContainsStaleControlMessages is the regression for the shared
// tray channel: eight generations queue lost just before exiting while the
// next generation stays in starting. A stale message must never abort the new
// generation with 7 (or a needless quit).
func TestTrayRestartContainsStaleControlMessages(t *testing.T) {
	data := integrationData(t)
	result, err := Launch(context.Background(), LaunchOptions{
		Command: Command{
			Path: os.Args[0],
			Args: []string{"-test.run=TestAppHelper", "--", data},
			Env: []string{
				"RL_TEST_MODE=supervisor", "RL_TEST_DATA=" + data,
				"RL_TEST_FAKE=traylostmany", "RL_TEST_LOSSES=4",
			},
		},
		LogDir: filepath.Join(data, "log"), ReadyTimeout: 15 * time.Second,
		TermWait: 3 * time.Second, KillWait: 2 * time.Second,
	})
	if err != nil {
		f := asFailure(err)
		t.Fatalf("stale tray message aborted the new generation: exit=%d reason=%s (%v)", f.Exit, f.Reason, err)
	}
	t.Cleanup(func() { stopLaunched(t, result.PID) })
	paths := pathsFor(t, data)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(data, "tray-final-ready")); err == nil {
			if rec, readErr := readRecordFile(paths.AppOwner); readErr == nil && rec.State == MarkerReady {
				return
			}
		}
		select {
		case <-time.After(30 * time.Millisecond):
		default:
		}
		if !processAlive(result.PID) {
			t.Fatal("supervisor exited during repeated tray loss")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tray never reached its final ready generation")
}

func TestWaitTrayReadyUsesOnlyTheCurrentGeneration(t *testing.T) {
	stale := &childProc{
		wait: make(chan int, 1), msgs: make(chan string, 2), readErr: make(chan error, 1),
	}
	stale.msgs <- "lost"
	stale.msgs <- "quit"
	stale.readErr <- io.EOF
	current := &childProc{
		wait: make(chan int, 1), msgs: make(chan string, 1), readErr: make(chan error, 1),
	}
	s := &supervisor{tray: current, opts: SupervisorOptions{ReadyTimeout: time.Second, RepairInterval: time.Hour}}
	if got := s.trayMessages(); got != current.msgs {
		t.Fatal("tray messages are not bound to the current child")
	}
	s.tray = nil
	if s.trayMessages() != nil || s.trayWait() != nil {
		t.Fatal("absent tray must expose nil channels")
	}
	s.tray = current
	go func() {
		time.Sleep(20 * time.Millisecond)
		current.msgs <- "ready"
	}()
	if err := s.waitTrayReady(context.Background()); err != nil {
		t.Fatalf("readiness consumed a previous generation's message: %v", err)
	}
}

func TestEngineDeathRestartsAndHidesStaleReady(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "enginedie", "normal")
	first := h.waitReady(t, 5*time.Second)
	seenStarting := false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		second, err := launchFake(t, data, "normal", 2*time.Second)
		if err == nil && second.AlreadyRunning {
			if seenStarting {
				if second.PID != first.PID {
					t.Fatalf("restart changed the supervisor pid: %d -> %d", first.PID, second.PID)
				}
				return
			}
		} else if err != nil {
			f := asFailure(err)
			if f.Exit == 6 {
				seenStarting = true
			} else {
				select {
				case <-h.exited:
					logRaw, _ := os.ReadFile(filepath.Join(data, "log", "routelatch.log"))
					t.Fatalf("supervisor exited: %v\n%s", <-h.errCh, logRaw)
				default:
				}
				t.Fatalf("second run during engine restart = %+v", f)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("engine never restarted, or the starting window was never observed")
}

func TestSecondRunWhileStoppingIsConflict(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "slowstop", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLaunched(t, result.PID) })
	paths := pathsFor(t, data)
	sup, err := OpenProc(result.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	if err := sup.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitMarkerState(t, paths.AppOwner, MarkerStopping, 3*time.Second)
	_, err = launchFake(t, data, "normal", 2*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitConflict {
		t.Fatalf("second run during stopping = %+v, want 6", f)
	}
	if !sup.WaitExit(5 * time.Second) {
		t.Fatal("supervisor did not exit")
	}
	waitGroupEmpty(t, result.PID, 5*time.Second)
}

func TestSecondRunSocketVetoNeverZero(t *testing.T) {
	data := integrationData(t)
	paths := pathsFor(t, data)
	if err := os.MkdirAll(paths.InstanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireAppLock(paths.AppLock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	owner, err := OpenOwner(paths.AppOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	rec := Record{
		State: MarkerReady, Generation: 1, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		DataDir: paths.DataDir, LogPath: filepath.Join(paths.LogDir, "routelatch.log"),
	}
	if err := owner.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	_, err = launchFake(t, data, "normal", 2*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitConflict || f.Reason != "app_socket_veto" {
		t.Fatalf("socket veto = %+v, want 6/app_socket_veto", f)
	}
}

func TestStartupPhaseRepairStaysAtSixThenReady(t *testing.T) {
	data := integrationData(t)
	paths := pathsFor(t, data)
	firstCh := make(chan launched, 1)
	go func() {
		res, err := launchFake(t, data, "slowready", 10*time.Second)
		firstCh <- launched{result: res, err: err}
	}()
	waitMarkerState(t, paths.AppOwner, MarkerStarting, 5*time.Second)
	if err := os.Remove(paths.AppOwner); err != nil {
		t.Fatal(err)
	}
	_, err := launchFake(t, data, "normal", 2*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitConflict {
		t.Fatalf("second run during startup repair = %+v, want 6", f)
	}
	first := <-firstCh
	if first.err != nil {
		t.Fatalf("first launch: %v", first.err)
	}
	t.Cleanup(func() { stopLaunched(t, first.result.PID) })
	waitMarkerState(t, paths.AppOwner, MarkerReady, 5*time.Second)
	third, err := launchFake(t, data, "normal", 2*time.Second)
	if err != nil || !third.AlreadyRunning || third.PID != first.result.PID {
		t.Fatalf("post-repair launch = %+v, %v", third, err)
	}
}

func TestDeletedMarkerIsRecreatedLockedWhileReady(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "normal", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLaunched(t, result.PID) })
	paths := pathsFor(t, data)
	if err := os.Remove(paths.AppOwner); err != nil {
		t.Fatal(err)
	}
	waitMarkerState(t, paths.AppOwner, MarkerReady, 5*time.Second)
	assertLocked(t, paths.AppOwner)
	second, err := launchFake(t, data, "normal", 2*time.Second)
	if err != nil || !second.AlreadyRunning || second.PID != result.PID {
		t.Fatalf("second run after live repair = %+v, %v", second, err)
	}
}

func TestDeletedMarkerWhileUnreadyIsNotReady(t *testing.T) {
	data := integrationData(t)
	var registered atomic.Bool
	registered.Store(true)
	h := startSupervisor(t, data, "normal", "normal", func(o *SupervisorOptions) {
		o.RepairInterval = 50 * time.Millisecond
		o.TrayRegistered = func() error {
			if registered.Load() {
				return nil
			}
			return errors.New("tray item is not registered")
		}
	})
	h.waitReady(t, 5*time.Second)
	// Readiness is gone, but the supervisor has not restarted anything, so the
	// next repair must not republish the old ready phase.
	registered.Store(false)
	if err := os.Remove(h.paths.AppOwner); err != nil {
		t.Fatal(err)
	}
	rec := waitMarkerState(t, h.paths.AppOwner, MarkerStarting, 5*time.Second)
	if rec.State != MarkerStarting {
		t.Fatalf("recreated marker = %+v, want starting", rec)
	}
	second, err := launchFake(t, data, "normal", 2*time.Second)
	if err == nil && second.AlreadyRunning {
		t.Fatalf("concurrent run treated the unready marker as ready: %+v", second)
	}
	if f := asFailure(err); f.Exit != contract.ExitConflict {
		t.Fatalf("concurrent run during unready repair = %+v, want exit 6", f)
	}
	// Readiness returning restores ready on the next repair, not a restart.
	registered.Store(true)
	waitMarkerState(t, h.paths.AppOwner, MarkerReady, 5*time.Second)
}

func TestLiveRepairFailureFailClosed(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "normal")
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))
	path := h.paths.AppOwner
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := h.result(t, 5*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitConflict || f.Reason != "app_marker_invalid" {
		t.Fatalf("repair failure = %+v, want 6/app_marker_invalid", f)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	waitProcessGone(t, trayPID, 5*time.Second)
}

func TestStoppingWriteFailureThroughRunSupervisor(t *testing.T) {
	oldWrite := ownerWrite
	ownerWrite = func(o *Owner, r Record) error {
		if r.State == MarkerStopping {
			return errors.New("injected stopping write failure")
		}
		return oldWrite(o, r)
	}
	defer func() { ownerWrite = oldWrite }()

	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "normal")
	h.waitReady(t, 5*time.Second)
	trayPID := readPIDFile(t, filepath.Join(data, "tray.pid"))
	h.cancel()
	err := h.result(t, 5*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitError || f.Reason != "app_marker_write_failed" {
		t.Fatalf("stopping write failure = %+v, want 1/app_marker_write_failed", f)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	waitProcessGone(t, trayPID, 5*time.Second)
	// The owner path stays, but no supervisor flock may outlive the failure.
	assertUnlocked(t, h.paths.AppOwner)
}

func TestEngineLockConflictWinsOverTrayProbe(t *testing.T) {
	data := integrationData(t)
	paths := pathsFor(t, data)
	if err := os.MkdirAll(paths.InstanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, err = launchFake(t, data, "trayfail", 2*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitAlreadyRunning {
		t.Fatalf("headless lock + no tray = %+v, want 4", f)
	}
}

func TestLaunchCancellationStopsSupervisorAndChildren(t *testing.T) {
	data := integrationData(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan launched, 1)
	go func() {
		res, err := Launch(ctx, LaunchOptions{
			Command: Command{
				Path: os.Args[0],
				Args: []string{"-test.run=TestAppHelper", "--", data},
				Env:  []string{"RL_TEST_MODE=supervisor", "RL_TEST_DATA=" + data, "RL_TEST_FAKE=noready"},
			},
			LogDir: filepath.Join(data, "log"), ReadyTimeout: 10 * time.Second,
			TermWait: 2 * time.Second, KillWait: 2 * time.Second,
		})
		done <- launched{result: res, err: err}
	}()
	paths := pathsFor(t, data)
	waitMarkerState(t, paths.AppOwner, MarkerStarting, 5*time.Second)
	cancel()
	got := <-done
	f := asFailure(got.err)
	if f.Exit != contract.ExitCanceled {
		t.Fatalf("canceled launch = %+v, want exit 12", f)
	}
	assertUnlocked(t, paths.AppLock)
	assertUnlocked(t, paths.Lock)
	assertUnlocked(t, paths.AppOwner)
	noProcessWithArg(t, data)
}

func TestChildPIDFDFailureCleansUp(t *testing.T) {
	old := pidfdOpen
	defer func() { pidfdOpen = old }()
	self := os.Getpid()
	pidfdOpen = func(pid, flags int) (int, error) {
		if pid == self {
			return old(pid, flags)
		}
		return -1, unix.ENOSYS
	}
	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "normal")
	err := h.result(t, 5*time.Second)
	f := asFailure(err)
	if f.Exit != contract.ExitError || !strings.Contains(f.Message, "cannot pin the engine child") {
		t.Fatalf("child pidfd failure = %+v", f)
	}
	assertUnlocked(t, h.paths.AppLock)
	assertUnlocked(t, h.paths.Lock)
	noProcessWithArg(t, data)
}

func TestChildSIGKILLRestartsWithNewEngine(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "normal")
	h.waitReady(t, 5*time.Second)
	enginePath := filepath.Join(data, "engine.pid")
	first := readPIDFile(t, enginePath)
	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := readPIDFileErr(enginePath)
		if err == nil && pid != first {
			if rec, readErr := readRecordFile(h.paths.AppOwner); readErr == nil && rec.State == MarkerReady {
				return
			}
		}
		select {
		case <-h.exited:
			t.Fatalf("supervisor exited after child SIGKILL: %v", <-h.errCh)
		default:
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("engine was not replaced after SIGKILL")
}

func readPIDFileErr(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

func TestTrayExitOneIsTerminalWithoutRestart(t *testing.T) {
	data := integrationData(t)
	h := startSupervisor(t, data, "normal", "trayexit1")
	h.waitReady(t, 5*time.Second)
	f := asFailure(h.result(t, 5*time.Second))
	if f.Exit != contract.ExitError {
		t.Fatalf("tray exit 1 = %+v, want exit 1", f)
	}
	raw, err := os.ReadFile(filepath.Join(data, "tray-launches"))
	if err != nil {
		t.Fatal(err)
	}
	if launches := strings.Count(string(raw), "\n"); launches != 1 {
		t.Fatalf("tray exit 1 restarted the tray: %d launches", launches)
	}
}

func TestStaleReadyMarkerWithoutOwnerIsNotAlreadyRunning(t *testing.T) {
	data := integrationData(t)
	paths := pathsFor(t, data)
	if err := os.MkdirAll(paths.InstanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := Record{
		State: MarkerReady, Generation: 1, PID: 999999, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		DataDir: paths.DataDir, LogPath: filepath.Join(paths.LogDir, "routelatch.log"),
	}
	raw, err := EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AppOwner, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := launchFake(t, data, "normal", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLaunched(t, result.PID) })
	if result.AlreadyRunning || result.PID == rec.PID {
		t.Fatalf("stale marker was reported as already running: %+v", result)
	}
}
