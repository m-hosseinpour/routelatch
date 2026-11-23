package instance

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func entry(root string) Legacy {
	return Legacy{Lock: filepath.Join(root, "state.lock"), Socket: filepath.Join(root, "run", "control.sock")}
}

func holdLock(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	return f
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	return int(exit.Exit)
}

func TestLegacyPathsDataDirIsScopedToThatRoot(t *testing.T) {
	root := t.TempDir()
	got := LegacyPaths(root, "/elsewhere/state", "/run/user/1234", 1234)
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want one", got)
	}
	if got[0].Lock != filepath.Join(root, "state.lock") ||
		got[0].Socket != filepath.Join(root, "run", "control.sock") {
		t.Fatalf("entry = %+v", got[0])
	}
}

func TestLegacyPathsDefaultIncludesKnownRuntimes(t *testing.T) {
	old := runUserRoot
	runUserRoot = t.TempDir()
	t.Cleanup(func() { runUserRoot = old })
	uid := 4242
	if err := os.MkdirAll(filepath.Join(runUserRoot, "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	got := LegacyPaths("", stateDir, runtimeDir, uid)
	want := []string{
		filepath.Join(stateDir, "state.lock"),
		filepath.Join(runtimeDir, "routelatch", "state.lock"),
		filepath.Join(runUserRoot, "4242", "routelatch", "state.lock"),
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %v", got, want)
	}
	for i := range want {
		if got[i].Lock != want[i] {
			t.Fatalf("entry %d = %s, want %s", i, got[i].Lock, want[i])
		}
	}

	// Without XDG_RUNTIME_DIR the no-runtime layout and the /run/user fallback
	// remain; the same runtime root is never listed twice.
	got = LegacyPaths("", stateDir, "", uid)
	if len(got) != 2 || got[0].Lock != want[0] || got[1].Lock != want[2] {
		t.Fatalf("entries without runtime = %+v", got)
	}
	got = LegacyPaths("", stateDir, filepath.Join(runUserRoot, "4242"), uid)
	if len(got) != 2 {
		t.Fatalf("duplicate runtime entries = %+v", got)
	}
}

func TestAcquireHoldsAgainstSecondAcquirer(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 4 {
		t.Fatalf("second acquire exit = %d, want 4", code)
	}
	info, err := os.Stat(entry(root).Lock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	_ = again.Close()
}

func TestAcquireRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, entry(root).Lock); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "private" {
		t.Fatalf("symlink target changed: %q", raw)
	}
	held, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("symlink target stayed locked: %v", err)
	}
}

func TestAcquireRejectsNonRegularLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(entry(root).Lock, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
}

func TestForeignOwnerLockRejected(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("creating a foreign-uid lock needs root")
	}
	root := t.TempDir()
	lock := entry(root).Lock
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(lock, 12345, 12345); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
}

func TestUnopenableLockUsesLegacySocket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("uid 0 can open mode-000 files, so the unverifiable path cannot be produced")
	}
	root := t.TempDir()
	lock := entry(root).Lock
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lock, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })

	// No socket: the legacy state is unverifiable and startup refuses with 6.
	_, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 6 {
		t.Fatalf("no-socket exit = %d, want 6", code)
	}

	// A responsive legacy socket proves an old engine is alive: exit 4.
	if err := os.MkdirAll(filepath.Dir(entry(root).Socket), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", entry(root).Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 4 {
		t.Fatalf("socket exit = %d, want 4", code)
	}
}

func TestAcquireDedupesSameInodeAndCreatesPrivateParent(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	first := Legacy{Lock: filepath.Join(root, "state.lock")}
	second := Legacy{Lock: filepath.Join(alias, "state.lock")}
	runtime := Legacy{Lock: filepath.Join(runtimeRoot, "routelatch", "state.lock")}
	locks, err := AcquireLegacy(context.Background(), []Legacy{first, second, runtime})
	if err != nil {
		t.Fatalf("acquire aliases: %v", err)
	}
	defer locks.Close()
	info, err := os.Stat(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime parent mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestReleaseThenDeleteAndRecreateLock(t *testing.T) {
	root := t.TempDir()
	locks, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if err != nil {
		t.Fatal(err)
	}
	if err := locks.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entry(root).Lock); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	_ = again.Close()
}

func TestHeldLockBlocksAcquire(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	held := holdLock(t, entry(root).Lock)
	defer held.Close()
	_, err := AcquireLegacy(context.Background(), []Legacy{entry(root)})
	if code := exitCode(t, err); code != 4 {
		t.Fatalf("exit = %d, want 4 (a stopped legacy engine also holds the lock)", code)
	}
}

func TestDefaultLegacyLockWithAndWithoutRuntime(t *testing.T) {
	old := runUserRoot
	runUserRoot = t.TempDir() // no /run/user/<uid> fallback for this test
	t.Cleanup(func() { runUserRoot = old })
	stateDir := filepath.Join(t.TempDir(), "state")
	runtimeDir := filepath.Join(t.TempDir(), "runtime")

	// With XDG_RUNTIME_DIR set, an old R12 default engine holds the runtime
	// lock even in mode stopped.
	withRuntime := LegacyPaths("", stateDir, runtimeDir, 4242)
	if len(withRuntime) != 2 {
		t.Fatalf("entries = %+v", withRuntime)
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	held := holdLock(t, withRuntime[1].Lock)
	_, err := AcquireLegacy(context.Background(), withRuntime)
	held.Close()
	if code := exitCode(t, err); code != 4 {
		t.Fatalf("runtime legacy exit = %d, want 4", code)
	}

	// Without XDG_RUNTIME_DIR, the no-runtime layout is the legacy lock.
	withoutRuntime := LegacyPaths("", stateDir, "", 4242)
	if len(withoutRuntime) != 1 {
		t.Fatalf("entries = %+v", withoutRuntime)
	}
	held = holdLock(t, withoutRuntime[0].Lock)
	_, err = AcquireLegacy(context.Background(), withoutRuntime)
	held.Close()
	if code := exitCode(t, err); code != 4 {
		t.Fatalf("no-runtime legacy exit = %d, want 4", code)
	}
}

func TestDefaultAndDataDirLegacySetsAreIndependent(t *testing.T) {
	old := runUserRoot
	runUserRoot = t.TempDir()
	t.Cleanup(func() { runUserRoot = old })
	uid := 4242
	if err := os.MkdirAll(filepath.Join(runUserRoot, "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "default-state")
	dataDir := filepath.Join(t.TempDir(), "custom")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	def := LegacyPaths("", stateDir, filepath.Join(t.TempDir(), "runtime"), uid)
	custom := LegacyPaths(dataDir, dataDir, "", uid)
	locks, err := AcquireLegacy(context.Background(), append(append([]Legacy{}, def...), custom...))
	if err != nil {
		t.Fatalf("default and data-dir sets must not block each other: %v", err)
	}
	defer locks.Close()
	for _, item := range custom {
		for _, legacy := range def {
			if item.Lock == legacy.Lock {
				t.Fatalf("default and data-dir share %s", item.Lock)
			}
		}
	}
}

func TestAcquireLegacyHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := AcquireLegacy(ctx, []Legacy{entry(t.TempDir())})
	if err == nil {
		t.Fatal("canceled context acquired locks")
	}
}
