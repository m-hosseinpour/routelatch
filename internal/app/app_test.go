package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

func pathsFor(t *testing.T, root string) store.Paths {
	t.Helper()
	paths, err := store.ResolvePathsFor(root, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestMarkerRoundTripAndCorruption(t *testing.T) {
	rec := Record{
		State: MarkerReady, Generation: 7, PID: 42, StartedAt: "2026-11-23T00:00:00Z",
		DataDir: "/tmp/data", Listeners: Listeners{HTTP: "127.0.0.1:1", SOCKS: "127.0.0.1:2"},
		LogPath: "/tmp/data/log/routelatch.log",
	}
	raw, err := EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if raw[len(raw)-1] != '\n' || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("record must have exactly one trailing newline: %q", raw)
	}
	got, err := ParseRecord(raw)
	if err != nil || got.State != MarkerReady || got.Generation != 7 {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	for name, bad := range map[string][]byte{
		"missing newline": raw[:len(raw)-1],
		"extra byte":      append(append([]byte{}, raw...), 'x'),
		"truncated":       raw[:len(raw)/2],
		"empty":           nil,
		"embedded LF":     []byte(`{"state":"ready"` + "\n" + `}` + "\n"),
	} {
		if _, err := ParseRecord(bad); err == nil {
			t.Errorf("%s: accepted invalid record", name)
		}
	}
}

func TestOwnerWritesBeforeLockAndRepairsDeletedMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run", "app.owner")
	owner, err := OpenOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	rec := Record{State: MarkerStarting, Generation: 1, PID: os.Getpid(), StartedAt: "now", DataDir: root, LogPath: filepath.Join(root, "log")}
	if err := owner.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	if err := owner.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := owner.Verify(); !errors.Is(err, ErrDeleted) {
		t.Fatalf("verify after delete = %v, want ErrDeleted", err)
	}
	rec.State = MarkerStopping
	if err := owner.RecreateAfterDelete(rec, 3, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got, err := owner.Read()
	if err != nil || got.State != MarkerStopping {
		t.Fatalf("recreated record = %+v, %v", got, err)
	}
	// The new inode must be locked by us.
	other, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("recreated app.owner is not locked")
	}
}

func TestOwnerRejectsSymlinkAndReplacedInode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "app.owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOwner(filepath.Join(dir, "app.owner")); err == nil {
		t.Fatal("followed a symlink app.owner")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "private" {
		t.Fatal("symlink target was modified")
	}

	os.Remove(filepath.Join(dir, "app.owner"))
	owner, err := OpenOwner(filepath.Join(dir, "app.owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	os.Remove(filepath.Join(dir, "app.owner"))
	if err := os.WriteFile(filepath.Join(dir, "app.owner"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner.Verify(); err == nil || errors.Is(err, ErrDeleted) {
		t.Fatalf("replaced inode verify = %v, want invalid", err)
	}
}

func TestAppLockDeleteRecreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "app.lock")
	first, err := AcquireAppLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireAppLock(path); !errors.Is(err, ErrAppLocked) {
		t.Fatalf("second acquire = %v, want ErrAppLocked", err)
	}
	first.Release()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireAppLock(path)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	again.Release()
}

func TestExistingStateRules(t *testing.T) {
	root := t.TempDir()
	paths := MarkerPathsFor(filepath.Join(root, "run"), root, filepath.Join(root, "log"), filepath.Join(root, "run", "control.sock"))
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
	rec := Record{State: MarkerReady, Generation: 1, PID: os.Getpid(), StartedAt: "now", DataDir: root, LogPath: filepath.Join(root, "log")}
	if err := owner.Write(rec); err != nil {
		t.Fatal(err)
	}
	// The owner is not flocked: its state is stale and must always be 6.
	if _, _, err := ExistingState(paths); err == nil {
		t.Fatal("stale owner lock produced success")
	} else if f := asFailure(err); f.Exit != contract.ExitConflict {
		t.Fatalf("stale owner exit = %d, want 6", f.Exit)
	}
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	// A live lock with a complete ready marker for this data dir is the only
	// case that may proceed; the caller still vetoes it with the socket.
	if _, got, err := ExistingState(paths); err != nil || got.PID != rec.PID {
		t.Fatalf("live ready marker = %+v, %v", got, err)
	}
	srv := startStatusServer(t, paths.Socket, true)
	defer srv.Close()
	if _, got, err := ExistingState(paths); err != nil || got.PID != rec.PID {
		t.Fatalf("ready + socket = %+v, %v", got, err)
	}
	// A different data directory is always refused.
	other := paths
	other.DataDir = filepath.Join(root, "elsewhere")
	if _, _, err := ExistingState(other); err == nil {
		t.Fatal("data-dir mismatch produced success")
	}
	// starting and stopping are never ready.
	for _, state := range []string{MarkerStarting, MarkerStopping} {
		rec.State = state
		if err := owner.Write(rec); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ExistingState(paths); err == nil {
			t.Fatalf("%s marker produced success", state)
		}
	}
}

type statusHandler struct{ ready bool }

func (h statusHandler) Handle(context.Context, control.Request) (any, error) {
	return map[string]any{
		"ready": h.ready,
		"listeners": map[string]any{
			"http": "127.0.0.1:1", "socks": "127.0.0.1:2",
		},
	}, nil
}

func startStatusServer(t *testing.T, path string, ready bool) *control.Server {
	t.Helper()
	srv, err := control.NewServer(control.ServerOptions{Path: path, Handler: statusHandler{ready: ready}, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestRejectHeadlessConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "app.lock")
	if err := RejectHeadlessConflict(path); err != nil {
		t.Fatalf("absent app.lock: %v", err)
	}
	lock, err := AcquireAppLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = RejectHeadlessConflict(path)
	if f := asFailure(err); f.Exit != contract.ExitAlreadyRunning {
		t.Fatalf("exit = %d, want 4", f.Exit)
	}
}

func TestProbePIDFDFailureCreatesNoProcess(t *testing.T) {
	old := pidfdOpen
	defer func() { pidfdOpen = old }()
	pidfdOpen = func(int, int) (int, error) { return -1, unix.ENOSYS }
	marker := filepath.Join(t.TempDir(), "spawned")
	_, err := Launch(context.Background(), LaunchOptions{
		Command: Command{Path: "/bin/sh", Args: []string{"-c", "touch " + marker}},
		LogDir:  t.TempDir(), ReadyTimeout: time.Second,
	})
	f := asFailure(err)
	if f.Exit != contract.ExitUnsupported || f.Reason != "pidfd_unavailable" {
		t.Fatalf("failure = %+v, want unsupported/pidfd_unavailable", f)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a process was spawned despite the pidfd preflight failure")
	}
}

func TestSignalESRCHContinuesAndEPERMStops(t *testing.T) {
	old := pidfdSendSignal
	defer func() { pidfdSendSignal = old }()
	h := &ProcHandle{Identity: Identity{PID: 12345}, fd: 99}
	pidfdSendSignal = func(int, unix.Signal, *unix.Siginfo, int) error { return unix.ESRCH }
	if err := h.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("ESRCH = %v, want nil", err)
	}
	pidfdSendSignal = func(int, unix.Signal, *unix.Siginfo, int) error { return unix.EPERM }
	if err := h.Signal(syscall.SIGTERM); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("EPERM = %v, want ErrNotPermitted", err)
	}
}

func TestConfiguredListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(map[string]any{"http_listen": "127.0.0.1:9001", "socks_listen": "127.0.0.1:9002"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := configuredListeners(path)
	if got.HTTP != "127.0.0.1:9001" || got.SOCKS != "127.0.0.1:9002" {
		t.Fatalf("listeners = %+v", got)
	}
}

func TestStatusSocketRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	srv := startStatusServer(t, path, true)
	defer srv.Close()
	client := &control.Client{Path: path, Timeout: time.Second}
	env, err := client.Call(context.Background(), control.Request{Op: control.OpStatus})
	if err != nil || !env.OK {
		t.Fatalf("status = %+v, %v", env, err)
	}
	if _, ok := env.Data.(map[string]any)["ready"]; !ok {
		t.Fatalf("status data = %+v", env.Data)
	}
}

func TestLaunchSupervisorPIDFDOpenFailureUsesRawKill(t *testing.T) {
	old := pidfdOpen
	defer func() { pidfdOpen = old }()
	self := os.Getpid()
	pidfdOpen = func(pid, flags int) (int, error) {
		if pid == self {
			return old(pid, flags)
		}
		return -1, unix.ENOSYS
	}
	dir := t.TempDir()
	token := "rl-pidfd-raw-kill-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := Launch(context.Background(), LaunchOptions{
		Command: Command{Path: "/bin/sh", Args: []string{"-c", "sleep 30 # " + token}},
		LogDir:  filepath.Join(dir, "log"), ReadyTimeout: 2 * time.Second,
		TermWait: time.Second, KillWait: time.Second,
	})
	f := asFailure(err)
	if f.Exit != contract.ExitError {
		t.Fatalf("failure = %+v, want exit 1 (not unsupported 7)", f)
	}
	for _, pidRaw := range procPIDs(t) {
		raw, readErr := os.ReadFile(filepath.Join(procRoot, pidRaw, "cmdline"))
		if readErr == nil && strings.Contains(string(raw), token) {
			_ = syscall.Kill(atoi(pidRaw), syscall.SIGKILL)
			t.Fatalf("direct child %s survived the documented raw SIGKILL path", pidRaw)
		}
	}
}

func procPIDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func TestFailClosedReleasesOwnerBeforeAppLock(t *testing.T) {
	root := t.TempDir()
	instance := filepath.Join(root, "run")
	lock, err := AcquireAppLock(filepath.Join(instance, "app.lock"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := OpenOwner(filepath.Join(instance, "app.owner"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	// Replace the descriptor with a read-only one so the transition write
	// fails exactly like a full/immutable marker.
	owner.f.Close()
	owner.f, err = os.Open(filepath.Join(instance, "app.owner"))
	if err != nil {
		t.Fatal(err)
	}
	s := &supervisor{
		opts: SupervisorOptions{Now: time.Now}, owner: owner, appLock: lock,
		paths: MarkerPathsFor(instance, root, filepath.Join(root, "log"), filepath.Join(instance, "control.sock")),
	}
	f := asFailure(s.failClosed(markerWriteFailed(errors.New("injected write failure"))))
	if f.Exit != contract.ExitError || f.Reason != "app_marker_write_failed" {
		t.Fatalf("fail-closed failure = %+v", f)
	}
	if owner.f != nil {
		t.Fatal("app.owner descriptor stayed open during fail-closed")
	}
	assertUnlocked(t, filepath.Join(instance, "app.lock"))
}

func TestRepairRewritesCorruptRecordAndRecreatesDeletedPath(t *testing.T) {
	root := t.TempDir()
	instance := filepath.Join(root, "run")
	owner, err := OpenOwner(filepath.Join(instance, "app.owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{
		opts: SupervisorOptions{Now: time.Now}, owner: owner, phase: MarkerStarting,
		generation: 3, startedAt: "now",
		paths:   MarkerPathsFor(instance, root, filepath.Join(root, "log"), filepath.Join(instance, "control.sock")),
		logPath: filepath.Join(root, "log", "routelatch.log"),
	}
	// A partial record on the same inode is repaired in place.
	if err := os.WriteFile(filepath.Join(instance, "app.owner"), []byte(`{"state":"ready"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	rec, err := owner.Read()
	if err != nil || rec.State != MarkerStarting {
		t.Fatalf("repaired record = %+v, %v", rec, err)
	}
	// A deleted path is recreated, written and re-locked with the current
	// phase; a second repair after that is stable.
	if err := os.Remove(filepath.Join(instance, "app.owner")); err != nil {
		t.Fatal(err)
	}
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	rec, err = owner.Read()
	if err != nil || rec.State != MarkerStarting {
		t.Fatalf("recreated record = %+v, %v", rec, err)
	}
	if err := s.repair(); err != nil {
		t.Fatalf("stable repair: %v", err)
	}
	// Replacing the inode is fail-closed, never adopted.
	path := filepath.Join(instance, "app.owner")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := asFailure(s.repair())
	if f.Reason != "app_marker_invalid" {
		t.Fatalf("replaced inode repair = %+v", f)
	}
}

func TestTwoDataDirsHaveIndependentAppLocks(t *testing.T) {
	first, err := AcquireAppLock(filepath.Join(t.TempDir(), "run", "app.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireAppLock(filepath.Join(t.TempDir(), "run", "app.lock"))
	if err != nil {
		t.Fatalf("second data dir blocked: %v", err)
	}
	second.Release()
}

func TestExistingStateMissingMarkerIsRepairPendingAndNotCreated(t *testing.T) {
	root := t.TempDir()
	paths := MarkerPathsFor(filepath.Join(root, "run"), root, filepath.Join(root, "log"), filepath.Join(root, "run", "control.sock"))
	if err := os.MkdirAll(paths.InstanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireAppLock(paths.AppLock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, _, err = ExistingState(paths)
	if f := asFailure(err); f.Exit != contract.ExitConflict || f.Reason != "app_marker_repair_pending" {
		t.Fatalf("missing marker = %+v, want exit 6 repair pending", f)
	}
	if _, statErr := os.Stat(paths.AppOwner); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("second-run check created app.owner")
	}
}

func TestProbePIDFDPreflightKinds(t *testing.T) {
	old := pidfdOpen
	defer func() { pidfdOpen = old }()
	for _, want := range []error{unix.ENOSYS, unix.EPERM} {
		want := want
		pidfdOpen = func(int, int) (int, error) { return -1, want }
		err := ProbePIDFD()
		_, launchErr := Launch(context.Background(), LaunchOptions{
			Command: Command{Path: "/bin/false"}, LogDir: t.TempDir(), ReadyTimeout: time.Second,
		})
		f := asFailure(launchErr)
		if err == nil || f.Exit != contract.ExitUnsupported || f.Reason != "pidfd_unavailable" {
			t.Fatalf("%v: probe=%v launch=%+v", want, err, f)
		}
	}
}

func TestRepairInStoppingPhaseNeverWritesReady(t *testing.T) {
	root := t.TempDir()
	instance := filepath.Join(root, "run")
	owner, err := OpenOwner(filepath.Join(instance, "app.owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{
		opts: SupervisorOptions{Now: time.Now}, owner: owner, phase: MarkerStopping,
		generation: 3, startedAt: "now",
		paths:   MarkerPathsFor(instance, root, filepath.Join(root, "log"), filepath.Join(instance, "control.sock")),
		logPath: filepath.Join(root, "log", "routelatch.log"),
	}
	rec := s.record(MarkerStopping)
	// A valid ready record on the same inode must be rewritten to stopping.
	rec.State = MarkerReady
	if err := owner.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	if got, err := owner.Read(); err != nil || got.State != MarkerStopping {
		t.Fatalf("in-place stopping repair = %+v, %v", got, err)
	}
	// A deleted path is recreated with stopping, never ready, even when the
	// engine still answers status.ready.
	if err := os.Remove(filepath.Join(instance, "app.owner")); err != nil {
		t.Fatal(err)
	}
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	if got, err := owner.Read(); err != nil || got.State != MarkerStopping {
		t.Fatalf("recreated stopping marker = %+v, %v", got, err)
	}
}

// Repair may publish ready only while both readiness legs hold: status.ready
// and a registered current tray. A readiness dip writes starting but must not
// move the logical phase, so a later repair can restore ready without a restart.
func TestRepairPublishesReadyOnlyWhileReadinessHolds(t *testing.T) {
	root := t.TempDir()
	instance := filepath.Join(root, "run")
	paths := MarkerPathsFor(instance, root, filepath.Join(root, "log"), filepath.Join(instance, "control.sock"))
	if err := os.MkdirAll(instance, 0o700); err != nil {
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
	if err := owner.Lock(1, 0); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{
		opts: SupervisorOptions{
			Now:            time.Now,
			Paths:          store.Paths{Socket: paths.Socket},
			TrayRegistered: func() error { return nil },
		},
		owner: owner, phase: MarkerReady, generation: 3, startedAt: "now",
		paths: paths, logPath: filepath.Join(root, "log", "routelatch.log"),
	}
	if err := owner.Write(s.record(MarkerStarting)); err != nil {
		t.Fatal(err)
	}
	startStatus := func() *control.Server {
		t.Helper()
		srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: helperStatus{}, AllowStale: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	// With readiness present, repair fills the marker back in as ready.
	srv := startStatus()
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	if got, err := owner.Read(); err != nil || got.State != MarkerReady {
		t.Fatalf("repair with readiness = %+v, %v", got, err)
	}
	// Readiness disappears: a deleted marker must come back as starting, not a
	// stale ready, and a concurrent run must get 6 instead of 0.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.AppOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	rec, err := owner.Read()
	if err != nil || rec.State != MarkerStarting {
		t.Fatalf("recreated unready marker = %+v, %v; want starting", rec, err)
	}
	if s.phase != MarkerReady {
		t.Fatalf("repair changed the logical phase to %q", s.phase)
	}
	if _, _, err := ExistingState(paths); asFailure(err).Exit != contract.ExitConflict {
		t.Fatalf("concurrent second run = %v, want exit 6", err)
	}
	// Readiness returning lets the next repair restore ready without a restart.
	srv = startStatus()
	defer srv.Close()
	if err := s.repair(); err != nil {
		t.Fatal(err)
	}
	if got, err := owner.Read(); err != nil || got.State != MarkerReady {
		t.Fatalf("restored ready marker = %+v, %v", got, err)
	}
}
