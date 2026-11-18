package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// TestHelperSidecar is a local stand-in for the real executable. It listens on
// every port in the generated config and stays alive until killed; it proves
// the runtime's readiness gate and handoff without starting a public exit.
func TestHelperSidecar(t *testing.T) {
	var configPath string
	for i, arg := range os.Args {
		if arg == "-c" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
		}
	}
	if configPath == "" {
		t.Skip("helper process")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sidecar helper: read config:", err)
		os.Exit(2)
	}
	var cfg struct {
		Inbounds []struct {
			Listen     string `json:"listen"`
			ListenPort uint16 `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sidecar helper: decode config:", err)
		os.Exit(2)
	}
	listeners := make([]net.Listener, 0, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		if in.Listen != "127.0.0.1" {
			fmt.Fprintln(os.Stderr, "sidecar helper: non-loopback bind")
			os.Exit(2)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", in.Listen, in.ListenPort))
		if err != nil {
			fmt.Fprintln(os.Stderr, "sidecar helper: listen:", err)
			os.Exit(2)
		}
		listeners = append(listeners, ln)
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}(ln)
	}
	select {}
}

func fakeExecutable(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	path := filepath.Join(dir, "sing-box-fake")
	body := "#!/bin/sh\nexec " + exe + " -test.run=^TestHelperSidecar$ \"$@\"\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

func TestNewRequiresConfigDir(t *testing.T) {
	if _, err := New(Options{Executable: "sing-box"}); err == nil {
		t.Fatal("an empty config directory was accepted")
	}
	if _, err := New(Options{Executable: "sing-box", ConfigDir: "   "}); err == nil {
		t.Fatal("a whitespace config directory was accepted")
	}
}

func TestSyncFailsClosedWithoutExecutable(t *testing.T) {
	rt, err := New(Options{ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Sync(context.Background(), []contract.SidecarExit{testExit("rl-x1")}); err == nil {
		t.Fatal("sync without an executable reported success")
	}
	if rt.Healthy() {
		t.Fatal("a failed runtime is healthy")
	}
	if _, ok := rt.Handoff("rl-x1"); ok {
		t.Fatal("a failed runtime published a handoff")
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestSyncEmptyStopsAndIsFailedClosed(t *testing.T) {
	rt, err := New(Options{Executable: "/does/not/exist", ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Sync(context.Background(), nil); err != nil {
		t.Fatalf("empty sync: %v", err)
	}
	if rt.Healthy() {
		t.Fatal("empty runtime is healthy")
	}
	if _, ok := rt.Handoff("rl-x1"); ok {
		t.Fatal("empty runtime published a handoff")
	}
}

func TestRuntimeLifecycleWithLocalFake(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(Options{Executable: fakeExecutable(t, dir), ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	exits := []contract.SidecarExit{testExit("rl-x1"), testExit("rl-x2")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rt.Sync(ctx, exits); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !rt.Healthy() {
		t.Fatal("runtime is not healthy after sync")
	}
	for _, tag := range []string{"rl-x1", "rl-x2"} {
		h, ok := rt.Handoff(tag)
		if !ok || !strings.HasPrefix(h.Address, "127.0.0.1:") || h.Username == "" || h.Password == "" {
			t.Fatalf("handoff %s=%+v ok=%v", tag, h, ok)
		}
		conn, err := net.Dial("tcp", h.Address)
		if err != nil {
			t.Fatalf("handoff %s did not listen: %v", tag, err)
		}
		_ = conn.Close()
	}
	if _, ok := rt.Handoff("unknown"); ok {
		t.Fatal("an unknown tag produced a handoff")
	}
	// A regeneration must replace the published credentials, not add to them.
	first, _ := rt.Handoff("rl-x1")
	if err := rt.Sync(ctx, exits); err != nil {
		t.Fatalf("resync: %v", err)
	}
	second, _ := rt.Handoff("rl-x1")
	if first == second {
		t.Fatal("regeneration reused a handoff")
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if rt.Healthy() {
		t.Fatal("stopped runtime is healthy")
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestSyncSurvivesContextCancellation(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(Options{Executable: fakeExecutable(t, dir), ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Sync(ctx, []contract.SidecarExit{testExit("rl-x1")}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	cancel() // the engine cancels its short sync context right after Sync
	h, ok := rt.Handoff("rl-x1")
	if !ok {
		t.Fatal("handoff withdrawn after sync")
	}
	conn, err := net.Dial("tcp", h.Address)
	if err != nil {
		t.Fatalf("the sidecar died with the sync context: %v", err)
	}
	_ = conn.Close()
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestConfigFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(Options{Executable: fakeExecutable(t, dir), ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rt.Sync(ctx, []contract.SidecarExit{testExit("rl-x1")}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()
	info, err := os.Stat(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config is readable by others: %v", info.Mode())
	}
}
