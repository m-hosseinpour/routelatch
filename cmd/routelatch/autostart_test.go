package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routelatch/internal/app"
	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/systemd"
)

// TestMain keeps package tests away from the developer's session bus. Without
// this, an offline doctor call would dial the real bus and could block for the
// length of the test timeout; the gated real-KDE probe test opts back in.
func TestMain(m *testing.M) {
	appTrayProbe = func() error { return errors.New("tray probe is disabled in package tests") }
	appPIDFDProbe = func() error { return nil }
	os.Exit(m.Run())
}

// isolateAutostart points HOME/XDG at a temporary tree and replaces the
// session-bus, pidfd and systemd seams. Nothing here can touch the user's real
// autostart file or user manager.
func isolateAutostart(t *testing.T, tray, pidfd func() error, exe string, unit func(context.Context) (systemd.Unit, error)) (home, dataDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	dataDir = t.TempDir()
	if tray == nil {
		tray = func() error { return nil }
	}
	if pidfd == nil {
		pidfd = func() error { return nil }
	}
	if exe == "" {
		exe = "/opt/Route Latch/routelatch"
	}
	if unit == nil {
		unit = func(context.Context) (systemd.Unit, error) { return systemd.Unit{}, nil }
	}
	oldTray, oldPIDFD, oldExe, oldUnit := appTrayProbe, appPIDFDProbe, autostartExecutable, autostartServiceUnit
	appTrayProbe, appPIDFDProbe, autostartServiceUnit = tray, pidfd, unit
	autostartExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() {
		appTrayProbe, appPIDFDProbe, autostartExecutable, autostartServiceUnit = oldTray, oldPIDFD, oldExe, oldUnit
	})
	return home, dataDir
}

func autostartData(t *testing.T, result cliResult) map[string]any {
	t.Helper()
	data, ok := result.env.Data.(map[string]any)
	if !ok {
		t.Fatalf("autostart data is %T, not an object", result.env.Data)
	}
	return data
}

func TestCLIAutostartEnableStatusDisable(t *testing.T) {
	home, dataDir := isolateAutostart(t, nil, nil, "/opt/Route Latch/routelatch", nil)
	path := filepath.Join(home, "config", "autostart", autostart.Filename)

	status := runCLI(t, dataDir, "autostart", "status")
	if status.code != 0 || status.env.Command != "autostart status" {
		t.Fatalf("status = %+v (code %d, %s)", status.env, status.code, status.stderr)
	}
	data := autostartData(t, status)
	if data["path"] != path || data["enabled"] != false || data["managed"] != false ||
		data["tray_available"] != true || data["pidfd_available"] != true {
		t.Fatalf("status data = %+v", data)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status created the autostart entry")
	}

	enable := runCLI(t, dataDir, "autostart", "enable")
	if enable.code != 0 || !enable.env.OK {
		t.Fatalf("enable = %+v (code %d, %s)", enable.env, enable.code, enable.stderr)
	}
	data = autostartData(t, enable)
	if data["enabled"] != true || data["managed"] != true {
		t.Fatalf("enable data = %+v", data)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `Exec="/opt/Route Latch/routelatch" run`) ||
		!strings.Contains(string(raw), autostart.MarkerKey) {
		t.Fatalf("entry =\n%s", raw)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	again := runCLI(t, dataDir, "autostart", "enable")
	if again.code != 0 {
		t.Fatalf("second enable = %+v", again.env)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("second enable rewrote the managed entry")
	}

	status = runCLI(t, dataDir, "autostart", "status")
	data = autostartData(t, status)
	if status.code != 0 || data["enabled"] != true || data["managed"] != true {
		t.Fatalf("status after enable = %+v", data)
	}

	disable := runCLI(t, dataDir, "autostart", "disable")
	if disable.code != 0 || !disable.env.OK {
		t.Fatalf("disable = %+v (code %d)", disable.env, disable.code)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disable left the managed entry")
	}
	if again := runCLI(t, dataDir, "autostart", "disable"); again.code != 0 {
		t.Fatalf("second disable = %+v", again.env)
	}
}

func TestCLIAutostartHumanOutput(t *testing.T) {
	home, dataDir := isolateAutostart(t, nil, nil, "", nil)
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--data-dir", dataDir, "autostart", "enable"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("enable exit = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "autostart entry enabled:") ||
		!strings.Contains(out.String(), "managed: yes") ||
		!strings.Contains(out.String(), "tray available: yes") {
		t.Fatalf("human enable output =\n%s", out.String())
	}
	out.Reset()
	code = Execute(context.Background(), []string{"--data-dir", dataDir, "autostart", "status"}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "autostart entry:") ||
		!strings.Contains(out.String(), "pidfd available: yes") {
		t.Fatalf("human status exit = %d, output =\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "config", "autostart", autostart.Filename)); err != nil {
		t.Fatal(err)
	}
}

func TestCLIAutostartForeignAndUnsafeExitSix(t *testing.T) {
	home, dataDir := isolateAutostart(t, nil, nil, "", nil)
	path := filepath.Join(home, "config", "autostart", autostart.Filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Desktop Entry]\nExec=/usr/bin/other\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	enable := runCLI(t, dataDir, "autostart", "enable")
	if enable.code != 6 || enable.env.Error == nil || enable.env.Error.Kind != contract.StateLocked {
		t.Fatalf("foreign enable = %+v (code %d)", enable.env, enable.code)
	}
	disable := runCLI(t, dataDir, "autostart", "disable")
	if disable.code != 6 {
		t.Fatalf("foreign disable exit = %d", disable.code)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != foreign {
		t.Fatal("foreign entry changed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	symlink := runCLI(t, dataDir, "autostart", "enable")
	if symlink.code != 6 {
		t.Fatalf("symlink enable exit = %d", symlink.code)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "target" {
		t.Fatal("symlink target changed")
	}
}

func TestCLIAutostartCapabilityReasons(t *testing.T) {
	trayDown := func() error { return errors.New("no StatusNotifierWatcher") }
	_, dataDir := isolateAutostart(t, trayDown, nil, "", nil)
	enable := runCLI(t, dataDir, "autostart", "enable")
	if enable.code != 7 || enable.env.Error == nil || enable.env.Error.Kind != contract.Unsupported {
		t.Fatalf("tray enable = %+v (code %d)", enable.env, enable.code)
	}
	if enable.env.Error.Detail["reason"] != "tray_unavailable" {
		t.Fatalf("tray reason = %+v", enable.env.Error.Detail)
	}
	if enable.env.Error.Detail["suggestion"] != "--headless" || enable.env.Error.Detail["exit_code"] != float64(7) {
		t.Fatalf("tray detail = %+v", enable.env.Error.Detail)
	}

	pidfdDown := func() error { return errors.New("operation not permitted") }
	_, dataDir = isolateAutostart(t, nil, pidfdDown, "", nil)
	enable = runCLI(t, dataDir, "autostart", "enable")
	if enable.code != 7 || enable.env.Error.Detail["reason"] != "pidfd_unavailable" {
		t.Fatalf("pidfd enable = %+v (code %d)", enable.env, enable.code)
	}
}

func TestCLIAutostartServiceCoexistence(t *testing.T) {
	unit := func(context.Context) (systemd.Unit, error) {
		return systemd.Unit{Exists: true, Managed: true, Safe: true}, nil
	}
	home, dataDir := isolateAutostart(t, nil, nil, "", unit)
	path := filepath.Join(home, "config", "autostart", autostart.Filename)

	status := runCLI(t, dataDir, "autostart", "status")
	if status.code != 0 || !status.env.OK {
		t.Fatalf("status = %+v (code %d)", status.env, status.code)
	}
	if autostartData(t, status)["conflict"] != autostart.ConflictService {
		t.Fatalf("status data = %+v", autostartData(t, status))
	}
	for _, verb := range []string{"enable", "disable"} {
		result := runCLI(t, dataDir, "autostart", verb)
		if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
			t.Fatalf("%s with managed service = %+v (code %d)", verb, result.env, result.code)
		}
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("enable wrote an entry despite the managed service")
	}

	// Human status carries the warning on stderr and still exits 0.
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--data-dir", dataDir, "autostart", "status"}, &out, &errOut)
	if code != 0 || !strings.Contains(errOut.String(), "warning:") {
		t.Fatalf("human conflict status exit = %d, stderr = %s", code, errOut.String())
	}
}

func TestCLIDoctorReportsAutostartCapabilities(t *testing.T) {
	_, dataDir := isolateAutostart(t, func() error { return nil }, func() error { return errors.New("operation not supported") }, "", nil)
	doctor := runCLI(t, dataDir, "doctor")
	if doctor.code != 0 || !doctor.env.OK {
		t.Fatalf("doctor = %+v (code %d, %s)", doctor.env, doctor.code, doctor.stderr)
	}
	data, _ := doctor.env.Data.(map[string]any)
	checks, _ := data["checks"].([]any)
	byName := map[string]map[string]any{}
	for _, item := range checks {
		if check, ok := item.(map[string]any); ok {
			byName[check["name"].(string)] = check
		}
	}
	tray, ok := byName["tray_available"]
	if !ok || tray["status"] != "ok" {
		t.Fatalf("tray check = %+v", tray)
	}
	pidfd, ok := byName["pidfd_available"]
	if !ok || pidfd["status"] != "warn" || !strings.Contains(pidfd["detail"].(string), "pidfd_open") {
		t.Fatalf("pidfd check = %+v", pidfd)
	}
	if pidfd["fix"] == "" {
		t.Fatalf("pidfd check has no fix: %+v", pidfd)
	}
}

func TestCLIAutostartUnknownVerb(t *testing.T) {
	_, dataDir := isolateAutostart(t, nil, nil, "", nil)
	result := runCLI(t, dataDir, "autostart", "maybe")
	if result.code != 2 || result.env.Error == nil || result.env.Error.Kind != contract.InvalidDestination {
		t.Fatalf("unknown verb = %+v (code %d)", result.env, result.code)
	}
}

type fakeSystemRunner struct {
	out string
	err error
}

func (f fakeSystemRunner) Output(context.Context, string, ...string) (string, error) {
	return f.out, f.err
}

// The conflict check reads the user manager's base, not the shell's. This test
// puts the shell base and the manager base in different trees and only a
// managed unit under the manager base.
func TestCLIAutostartDetectsManagedUnitFromManagerBase(t *testing.T) {
	home, dataDir := isolateAutostart(t, nil, nil, "", nil)
	autostartServiceUnit = detectManagedServiceUnit
	managerBase := filepath.Join(home, "manager-config")
	unitDir := filepath.Join(managerBase, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unit := systemd.OwnershipMarker + "\n[Service]\nExecStart=/usr/bin/routelatch run --headless\n"
	if err := os.WriteFile(filepath.Join(unitDir, systemd.UnitName), []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRunner := autostartSystem
	autostartSystem = fakeSystemRunner{out: "XDG_CONFIG_HOME=" + managerBase + "\nHOME=" + home + "\n"}
	t.Cleanup(func() { autostartSystem = oldRunner })

	status := runCLI(t, dataDir, "autostart", "status")
	if status.code != 0 || autostartData(t, status)["conflict"] != autostart.ConflictService {
		t.Fatalf("status = %+v (code %d)", status.env, status.code)
	}
	if _, err := os.Lstat(filepath.Join(home, "config", "systemd", "user", systemd.UnitName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("test wrote a unit under the shell base")
	}
}

// TestRealKDEAutostartProbe is the optional host probe from the R13-C bar. It
// is skipped unless ROUTELATCH_KDE_AUTOSTART_PROBE=1 and a real tray host
// exists, and it writes only into a temporary XDG tree.
func TestRealKDEAutostartProbe(t *testing.T) {
	if os.Getenv("ROUTELATCH_KDE_AUTOSTART_PROBE") != "1" {
		t.Skip("set ROUTELATCH_KDE_AUTOSTART_PROBE=1 to run the real KDE autostart probe")
	}
	// TestMain replaced the probes with fakes; this test is the explicit
	// opt-in that uses the real session bus and pidfd.
	oldTray, oldPIDFD := appTrayProbe, appPIDFDProbe
	appTrayProbe, appPIDFDProbe = app.ProbeTray, app.ProbePIDFD
	t.Cleanup(func() { appTrayProbe, appPIDFDProbe = oldTray, oldPIDFD })
	if err := app.ProbeTray(); err != nil {
		t.Skipf("no real StatusNotifier tray host: %v", err)
	}
	if err := app.ProbePIDFD(); err != nil {
		t.Skipf("pidfd is unavailable: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	oldUnit := autostartServiceUnit
	autostartServiceUnit = func(context.Context) (systemd.Unit, error) { return systemd.Unit{}, nil }
	t.Cleanup(func() { autostartServiceUnit = oldUnit })

	dataDir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := Execute(context.Background(), []string{"--json", "--data-dir", dataDir, "autostart", "enable"}, &out, &errOut); code != 0 {
		t.Fatalf("real enable exit = %d: %s %s", code, out.String(), errOut.String())
	}
	path := filepath.Join(home, "config", "autostart", autostart.Filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	want, err := autostart.DesktopEntry(exe)
	if err != nil || string(raw) != string(want) {
		t.Fatalf("real entry = %q, %v; want %q", raw, err, want)
	}
	out.Reset()
	errOut.Reset()
	if code := Execute(context.Background(), []string{"--json", "--data-dir", dataDir, "autostart", "disable"}, &out, &errOut); code != 0 {
		t.Fatalf("real disable exit = %d: %s %s", code, out.String(), errOut.String())
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("real disable left the entry")
	}
	var env contract.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("real disable envelope = %s, %v", out.String(), err)
	}
}
