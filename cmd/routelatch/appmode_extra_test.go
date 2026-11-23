package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/app"
	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

func isolatedData(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp/opencode", "rl-cmd-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func holdFileLock(t *testing.T, path string) *os.File {
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
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestRunHeadlessRejectsAppLockAndForgedMarker covers the reverse direction of
// app-vs-headless: a held app.lock is exit 4 even if the user exports the
// internal app-engine marker without a supervisor-parent pidfd.
func TestRunHeadlessRejectsAppLockAndForgedMarker(t *testing.T) {
	for _, forged := range []bool{false, true} {
		forged := forged
		t.Run(map[bool]string{false: "plain", true: "forged-marker"}[forged], func(t *testing.T) {
			dir := isolatedData(t)
			paths, err := store.ResolvePathsFor(dir, "", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			lock, err := app.AcquireAppLock(paths.AppLock)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			if forged {
				t.Setenv("ROUTELATCH_APP_ENGINE", "1")
			}
			got := runCLI(t, dir, "run", "--headless")
			if got.code != int(contract.ExitAlreadyRunning) {
				t.Fatalf("exit = %d, want 4 (%+v)", got.code, got.env)
			}
		})
	}
}

func TestHiddenAppCommandsCannotBypassChecks(t *testing.T) {
	t.Run("supervisor-honours-engine-lock", func(t *testing.T) {
		dir := isolatedData(t)
		paths, err := store.ResolvePathsFor(dir, "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		holdFileLock(t, paths.Lock)
		got := runCLI(t, dir, "__app-supervisor")
		if got.code != int(contract.ExitAlreadyRunning) {
			t.Fatalf("hidden supervisor exit = %d, want 4 (%+v)", got.code, got.env)
		}
	})
	t.Run("tray-needs-supervisor-env", func(t *testing.T) {
		dir := isolatedData(t)
		got := runCLI(t, dir, "__app-tray")
		if got.code == 0 {
			t.Fatalf("hidden tray ran without its supervisor descriptors")
		}
		paths, err := store.ResolvePathsFor(dir, "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(paths.AppOwner); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("hidden tray created app.owner: %v", err)
		}
	})
}

func realEngineCommand(data string) app.Command {
	return app.Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestRunProcessHelper", "--"},
		Env:  []string{"RL_CMD_HELPER=engine", "RL_CMD_DATA=" + data},
	}
}

func realTrayCommand(data string) app.Command {
	return app.Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestRunProcessHelper", "--"},
		Env:  []string{"RL_CMD_HELPER=tray", "RL_CMD_DATA=" + data},
	}
}

func writeAppConfig(t *testing.T, data, httpAddr, socksAddr string) {
	t.Helper()
	cfg, err := json.Marshal(map[string]any{
		"schema_version": 1, "http_listen": httpAddr, "socks_listen": socksAddr, "log_level": "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRealSupervisor(t *testing.T, data string, paths store.Paths) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.RunSupervisor(ctx, app.SupervisorOptions{
			Paths: paths, LogDir: paths.LogDir,
			Engine: realEngineCommand(data), Tray: realTrayCommand(data),
			TrayCheck:    func() error { return nil },
			ReadyTimeout: 30 * time.Second, RepairInterval: time.Second,
			TermWait: 5 * time.Second, KillWait: 3 * time.Second,
		})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Errorf("real supervisor did not exit")
		}
	})
	return cancel, done
}

func waitRealAppMarker(t *testing.T, paths store.Paths, state string, timeout time.Duration) app.Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(paths.AppOwner)
		if err == nil {
			if rec, parseErr := app.ParseRecord(raw); parseErr == nil && rec.State == state {
				return rec
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("app marker never became %s", state)
	return app.Record{}
}

func TestAppModeRealEnginePortConflict(t *testing.T) {
	data := isolatedData(t)
	paths, err := store.ResolvePathsFor(data, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr, socksAddr := freeLoopbackPair(t)
	busy, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	writeAppConfig(t, data, httpAddr, socksAddr)
	_, done := runRealSupervisor(t, data, paths)
	select {
	case err := <-done:
		var f *app.Failure
		if !errors.As(err, &f) || f.Exit != contract.ExitConflict {
			t.Fatalf("port conflict = %+v, want exit 6", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("port conflict did not surface")
	}
}

func TestAppModeRealEngineCorruptState(t *testing.T) {
	data := isolatedData(t)
	paths, err := store.ResolvePathsFor(data, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr, socksAddr := freeLoopbackPair(t)
	writeAppConfig(t, data, httpAddr, socksAddr)
	if err := os.WriteFile(paths.State, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, done := runRealSupervisor(t, data, paths)
	select {
	case err := <-done:
		var f *app.Failure
		if !errors.As(err, &f) || f.Exit != contract.ExitStateCorrupt {
			t.Fatalf("corrupt state = %+v, want exit 10", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("corrupt state did not surface")
	}
}

func TestAppModeRealEngineStoppedMode(t *testing.T) {
	data := isolatedData(t)
	paths, err := store.ResolvePathsFor(data, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr, socksAddr := freeLoopbackPair(t)
	writeAppConfig(t, data, httpAddr, socksAddr)
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := st.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(s *contract.State) error {
		s.Mode = contract.ModeStopped
		s.PolicyVersion++
		return nil
	}); err != nil {
		lock.Release()
		t.Fatal(err)
	}
	lock.Release()

	cancel, done := runRealSupervisor(t, data, paths)
	waitRealAppMarker(t, paths, app.MarkerReady, 40*time.Second)
	client := &control.Client{Path: paths.Socket, Timeout: 2 * time.Second}
	env, err := client.Call(context.Background(), control.Request{Op: control.OpStatus})
	if err != nil || !env.OK {
		t.Fatalf("status = %+v, %v", env, err)
	}
	status, _ := env.Data.(map[string]any)
	if status["ready"] != true || status["listeners_open"] != false {
		t.Fatalf("stopped app status = %+v, want ready=true listeners_open=false", status)
	}
	for _, addr := range []string{httpAddr, socksAddr} {
		conn, dialErr := net.Dial("tcp", addr)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatalf("stopped mode bound %s", addr)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped app shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("stopped app did not stop")
	}
}
