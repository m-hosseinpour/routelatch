package app

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

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
		Env:  []string{"RL_TEST_MODE=" + mode, "RL_TEST_DATA=" + os.Getenv("RL_TEST_DATA"), "RL_TEST_FAKE=" + os.Getenv("RL_TEST_FAKE")},
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
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 1
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return 4
	}
	parent, cancel, err := ParentWatchFromEnv()
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
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: helperStatus{}, AllowStale: true})
	if err != nil {
		return 1
	}
	if err := srv.Start(context.Background()); err != nil {
		return 1
	}
	defer srv.Close()
	select {
	case <-sig:
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
	send("ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	var after <-chan time.Time
	switch fake {
	case "trayquit":
		after = time.After(300 * time.Millisecond)
	case "traylost":
		after = time.After(300 * time.Millisecond)
	case "trayexit":
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
			default:
				return 0
			}
		}
	}
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
		Args: []string{"-test.run=TestAppHelper", "--"},
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

func TestTrayLossClearsReadyAndStops(t *testing.T) {
	data := integrationData(t)
	result, err := launchFake(t, data, "traylost", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitGroupEmpty(t, result.PID, 10*time.Second)
	paths := pathsFor(t, data)
	raw, err := os.ReadFile(paths.AppOwner)
	if err != nil {
		t.Fatalf("marker unreadable: %v", err)
	}
	rec, err := ParseRecord(raw)
	if err != nil {
		t.Fatalf("marker = %q: %v", raw, err)
	}
	if rec.State == MarkerReady {
		t.Fatal("tray loss left a stale ready marker")
	}
	assertUnlocked(t, paths.AppLock)
}
