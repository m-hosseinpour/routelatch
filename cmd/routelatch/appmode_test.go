package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/app"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

// TestRunProcessHelper is a re-exec entry point for the real-engine app-mode
// test. It is inert in a normal run.
func TestRunProcessHelper(t *testing.T) {
	switch os.Getenv("RL_CMD_HELPER") {
	case "":
		return
	case "engine":
		code := Execute(context.Background(), []string{"--data-dir", os.Getenv("RL_CMD_DATA"), "run", "--headless"}, os.Stdout, os.Stderr)
		os.Exit(code)
	case "tray":
		os.Exit(helperTray())
	}
	os.Exit(2)
}

func helperTray() int {
	fd := 0
	if _, err := fmtSscanf(os.Getenv("ROUTELATCH_TRAY_CONTROL_FD"), &fd); err != nil {
		return 1
	}
	f := os.NewFile(uintptr(fd), "control")
	if f == nil {
		return 1
	}
	defer f.Close()
	if _, err := f.WriteString("ready\n"); err != nil {
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	<-sig
	return 0
}

func fmtSscanf(s string, out *int) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}

func TestAppModeRealEngineIsolatedDataDir(t *testing.T) {
	data := t.TempDir()
	if err := os.Chmod(data, 0o700); err != nil {
		t.Fatal(err)
	}
	httpAddr, socksAddr := freeLoopbackPair(t)
	cfg, _ := json.Marshal(map[string]any{
		"schema_version": 1, "http_listen": httpAddr, "socks_listen": socksAddr, "log_level": "warning",
	})
	if err := os.WriteFile(filepath.Join(data, "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := store.ResolvePathsFor(data, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	helper := func(mode string) app.Command {
		return app.Command{
			Path: os.Args[0],
			Args: []string{"-test.run=TestRunProcessHelper", "--"},
			Env:  []string{"RL_CMD_HELPER=" + mode, "RL_CMD_DATA=" + data},
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- app.RunSupervisor(ctx, app.SupervisorOptions{
			Paths: paths, LogDir: paths.LogDir,
			Engine: helper("engine"), Tray: helper("tray"),
			TrayCheck:    func() error { return nil },
			ReadyTimeout: 30 * time.Second, RepairInterval: time.Second,
			TermWait: 5 * time.Second, KillWait: 3 * time.Second,
		})
	}()

	markerPath := filepath.Join(paths.InstanceDir, "app.owner")
	deadline := time.Now().Add(30 * time.Second)
	var rec app.Record
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(markerPath)
		if readErr == nil {
			if parsed, parseErr := app.ParseRecord(raw); parseErr == nil && parsed.State == app.MarkerReady {
				rec = parsed
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("supervisor exited before readiness: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rec.State != app.MarkerReady {
		t.Fatal("app marker never became ready")
	}

	client := &control.Client{Path: paths.Socket, Timeout: 2 * time.Second}
	env, err := client.Call(context.Background(), control.Request{Op: control.OpStatus})
	if err != nil || !env.OK {
		t.Fatalf("status = %+v, %v", env, err)
	}
	status, _ := env.Data.(map[string]any)
	if status["ready"] != true || status["listeners_open"] != true {
		t.Fatalf("status not ready: %+v", status)
	}
	if got := status["listeners"].(map[string]any); got["http"] != httpAddr || got["socks"] != socksAddr {
		t.Fatalf("listeners = %+v, want %s / %s", got, httpAddr, socksAddr)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop after context cancellation")
	}
	members, _ := app.GroupMembers(rec.PID)
	if len(members) != 0 {
		t.Fatalf("app left live children: %+v", members)
	}
	if _, err := os.Stat(paths.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("management socket survived shutdown: %v", err)
	}
}
