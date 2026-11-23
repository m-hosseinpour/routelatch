package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"routelatch/internal/store"
	"routelatch/internal/systemd"
)

func TestQuoteArgument(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/tmp/routelatch", "/tmp/routelatch"},
		{"/tmp/with space/routelatch", `"/tmp/with space/routelatch"`},
		{"/tmp/100%/routelatch", "/tmp/100%%/routelatch"},
		{"/tmp/$cash/routelatch", "/tmp/$$cash/routelatch"},
		{`/tmp/a"b/routelatch`, `"/tmp/a\"b/routelatch"`},
		{`/tmp/a\b/routelatch`, `"/tmp/a\\b/routelatch"`},
	}
	for _, c := range cases {
		got, err := quoteArgument(c.in)
		if err != nil || got != c.want {
			t.Errorf("quoteArgument(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := quoteArgument("/tmp/\nbad"); err == nil {
		t.Fatal("newline in a path was accepted")
	}
}

func TestUnitContentPinsBasesWithoutDataDir(t *testing.T) {
	m := &Manager{
		Executable: "/opt/Route Latch/100%/routelatch",
		Paths: store.Paths{
			ConfigDir: "/home/u/.config/routelatch",
			StateDir:  "/home/u/.local/state/routelatch",
		},
	}
	raw, err := m.unitContent()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		systemd.OwnershipMarker + "\n",
		"Type=simple\n",
		"ExecStart=\"/opt/Route Latch/100%%/routelatch\" run --headless\n",
		`Environment="XDG_CONFIG_HOME=/home/u/.config" "XDG_STATE_HOME=/home/u/.local/state"` + "\n",
		"Restart=on-failure\n",
		"RestartSec=2s\n",
		"RestartPreventExitStatus=1 4 6 7 9 10\n",
		"KillSignal=SIGTERM\n",
		"KillMode=mixed\n",
		"TimeoutStopSec=20s\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "User=") || strings.Contains(text, "Group=") {
		t.Fatalf("user unit must not set User=/Group=:\n%s", text)
	}
}

func TestUnitContentEnvironmentLeavesDollarLiteral(t *testing.T) {
	m := &Manager{
		Executable: "/opt/routelatch",
		Paths: store.Paths{
			ConfigDir: "/home/u/100%/$cash config/routelatch",
			StateDir:  "/home/u/$state/100%/routelatch",
		},
	}
	raw, err := m.unitContent()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	want := `Environment="XDG_CONFIG_HOME=/home/u/100%%/$cash config" "XDG_STATE_HOME=/home/u/$state/100%%"` + "\n"
	if !strings.Contains(text, want) {
		t.Fatalf("Environment is not systemd-escaped:\n%s", text)
	}
}

func TestUnitContentDataDirOmitsEnvironment(t *testing.T) {
	m := &Manager{
		Executable: "/opt/routelatch",
		DataDirSet: true,
		Paths:      store.Paths{DataDir: "/srv/Route Latch/data"},
	}
	raw, err := m.unitContent()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `ExecStart=/opt/routelatch --data-dir "/srv/Route Latch/data" run --headless`+"\n") {
		t.Fatalf("ExecStart = %s", text)
	}
	if strings.Contains(text, "Environment=") {
		t.Fatalf("--data-dir unit must omit Environment=:\n%s", text)
	}
}

func TestProbeOtherEnginesDetectsHeldAppLock(t *testing.T) {
	dir := t.TempDir()
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.AppLock), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(paths.AppLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := ProbeOtherEngines(paths); err == nil {
		t.Fatal("a held app.lock was not detected")
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := ProbeOtherEngines(paths); err != nil {
		t.Fatalf("free locks reported a conflict: %v", err)
	}
}

func TestSocketReadinessReportsNotRunning(t *testing.T) {
	ready := SocketReadiness(filepath.Join(t.TempDir(), "absent.sock"))
	if _, err := ready(context.Background()); err == nil {
		t.Fatal("absent socket reported readiness")
	}
}
