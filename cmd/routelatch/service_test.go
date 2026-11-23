package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/service"
	"routelatch/internal/store"
	"routelatch/internal/systemd"
)

const defaultServiceExe = "/opt/Route Latch/routelatch"

// fakeSystemd implements platform.CommandRunner for systemctl and loginctl.
// show statuses are consumed in order; the last one repeats so the readiness
// loop and rollback can observe a stable state.
type fakeSystemd struct {
	mu       sync.Mutex
	manager  string
	linger   string
	statuses []fakeUnitState
	calls    []string
	failures map[string]error
}

type fakeUnitState struct {
	LoadState      string
	ActiveState    string
	UnitFileState  string
	MainPID        int
	StartTime      uint64
	ExecMainStatus int
}

func inactiveUnit() fakeUnitState {
	return fakeUnitState{LoadState: "loaded", ActiveState: "inactive", UnitFileState: "disabled"}
}

func activeUnit(pid int, start uint64, enabled bool) fakeUnitState {
	state := fakeUnitState{LoadState: "loaded", ActiveState: "active", MainPID: pid, StartTime: start}
	if enabled {
		state.UnitFileState = "enabled"
	} else {
		state.UnitFileState = "disabled"
	}
	return state
}

func failedUnit(status int) fakeUnitState {
	return fakeUnitState{LoadState: "loaded", ActiveState: "failed", ExecMainStatus: status}
}

func (s fakeUnitState) render() string {
	return fmt.Sprintf("LoadState=%s\nActiveState=%s\nMainPID=%d\nExecMainStartTimestampMonotonic=%d\nUnitFileState=%s\nExecMainStatus=%d\n",
		s.LoadState, s.ActiveState, s.MainPID, s.StartTime, s.UnitFileState, s.ExecMainStatus)
}

func (f *fakeSystemd) nextStatus() fakeUnitState {
	if len(f.statuses) == 0 {
		return fakeUnitState{}
	}
	if len(f.statuses) == 1 {
		return f.statuses[0]
	}
	next := f.statuses[0]
	f.statuses = f.statuses[1:]
	return next
}

func (f *fakeSystemd) Output(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "loginctl" {
		if err := f.failures["loginctl"]; err != nil {
			return "", err
		}
		return "Linger=" + f.linger + "\n", nil
	}
	if name != "systemctl" {
		return "", errors.New("unexpected command " + name)
	}
	rest := args
	if len(rest) > 0 && rest[0] == "--user" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return "", errors.New("systemctl without a verb")
	}
	verb := rest[0]
	switch verb {
	case "show-environment":
		if err := f.failures[verb]; err != nil {
			return "", err
		}
		return f.manager, nil
	case "show":
		if err := f.failures[verb]; err != nil {
			return "", err
		}
		return f.nextStatus().render(), nil
	}
	f.calls = append(f.calls, strings.Join(rest, " "))
	if err := f.failures[verb]; err != nil {
		return "", err
	}
	return "", nil
}

func (f *fakeSystemd) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeSystemd) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSystemd) hasCall(want string) bool {
	for _, call := range f.callList() {
		if call == want {
			return true
		}
	}
	return false
}

func isolateService(t *testing.T, fake *fakeSystemd, ready service.Readiness) (home, managerBase string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "runtime"))
	t.Setenv("SUDO_USER", "")
	t.Setenv("PKEXEC_UID", "")
	managerBase = filepath.Join(home, "manager")
	fake.manager = "XDG_CONFIG_HOME=" + managerBase + "\nHOME=" + home + "\n"
	if fake.failures == nil {
		fake.failures = map[string]error{}
	}
	if fake.linger == "" {
		fake.linger = "yes"
	}
	if ready == nil {
		ready = func(context.Context) (service.ReadyStatus, error) {
			return service.ReadyStatus{Ready: true, ListenersOpen: true, Mode: "targeted"}, nil
		}
	}

	oldSystem, oldExe, oldGetenv := serviceSystem, serviceExecutable, serviceGetenv
	oldHome, oldFactory := serviceHomeDir, serviceReadinessFactory
	oldTimeout, oldPoll, oldProbe := serviceReadinessTimeout, servicePollInterval, serviceProbeLocks
	oldSync := serviceSyncDir
	serviceSyncDir = nil
	serviceSystem = fake
	serviceExecutable = func() (string, error) { return defaultServiceExe, nil }
	serviceGetenv = os.Getenv
	serviceHomeDir = func() (string, error) { return home, nil }
	serviceReadinessFactory = func(store.Paths) service.Readiness { return ready }
	serviceReadinessTimeout = 300 * time.Millisecond
	servicePollInterval = 5 * time.Millisecond
	serviceProbeLocks = func(context.Context, store.Paths) error { return nil }
	t.Cleanup(func() {
		serviceSystem, serviceExecutable, serviceGetenv = oldSystem, oldExe, oldGetenv
		serviceHomeDir, serviceReadinessFactory = oldHome, oldFactory
		serviceReadinessTimeout, servicePollInterval, serviceProbeLocks = oldTimeout, oldPoll, oldProbe
		serviceSyncDir = oldSync
	})
	return home, managerBase
}

func useRealLockProbe(t *testing.T) {
	t.Helper()
	serviceProbeLocks = nil
}

func runDefaultCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"--json"}, args...)
	code := Execute(context.Background(), full, &out, &errOut)
	result := cliResult{code: code, stdout: out.String(), stderr: errOut.String()}
	if strings.HasPrefix(strings.TrimSpace(result.stdout), "{") {
		if err := json.Unmarshal(out.Bytes(), &result.env); err != nil {
			t.Fatalf("invalid JSON envelope %q: %v", result.stdout, err)
		}
	}
	return result
}

func serviceData(t *testing.T, result cliResult) map[string]any {
	t.Helper()
	data, ok := result.env.Data.(map[string]any)
	if !ok {
		t.Fatalf("service data is %T (%+v)", result.env.Data, result.env)
	}
	return data
}

func unitPath(managerBase string) string {
	return filepath.Join(managerBase, "systemd", "user", systemd.UnitName)
}

func writeManagedUnit(t *testing.T, managerBase, content string) string {
	t.Helper()
	path := unitPath(managerBase)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func oldManagedUnit() string {
	return systemd.OwnershipMarker + "\n[Service]\nExecStart=/usr/bin/old-routelatch run --headless\n"
}

func holdLock(t *testing.T, path string) func() {
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
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	t.Cleanup(release)
	return release
}

func TestServiceInstallWritesManagedUnitAtManagerBase(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	home, managerBase := isolateService(t, fake, nil)
	result := runDefaultCLI(t, "service", "install")
	if result.code != 0 || !result.env.OK || result.env.Command != "service install" {
		t.Fatalf("install = %+v (code %d, %s)", result.env, result.code, result.stderr)
	}
	data := serviceData(t, result)
	path := unitPath(managerBase)
	if data["unit"] != path || data["enabled"] != true || data["active"] != true || data["managed"] != true || data["linger"] != "yes" {
		t.Fatalf("install data = %+v", data)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		systemd.OwnershipMarker,
		`ExecStart="/opt/Route Latch/routelatch" run --headless`,
		`Environment="XDG_CONFIG_HOME=` + filepath.Join(home, "config") + `" "XDG_STATE_HOME=` + filepath.Join(home, "state") + `"`,
		"RestartPreventExitStatus=1 4 6 7 9 10",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit is missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, "config", "systemd", "user", systemd.UnitName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install wrote a unit under the shell config base, not the manager base")
	}
	if !fake.hasCall("daemon-reload") || !fake.hasCall("enable --now "+systemd.UnitName) {
		t.Fatalf("install calls = %v", fake.callList())
	}
}

func TestServiceInstallQuotesExecStartAndPinsDataDir(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	serviceExecutable = func() (string, error) { return "/opt/100%/$cash/Route Latch/routelatch", nil }
	dataDir := filepath.Join(t.TempDir(), "Route Latch data")
	result := runCLI(t, dataDir, "service", "install")
	if result.code != 0 {
		t.Fatalf("install = %+v (code %d, %s)", result.env, result.code, result.stderr)
	}
	raw, err := os.ReadFile(unitPath(managerBase))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	want := `ExecStart="/opt/100%%/$$cash/Route Latch/routelatch" --data-dir "` + dataDir + `" run --headless`
	if !strings.Contains(text, want) {
		t.Fatalf("ExecStart is not quoted/pinned:\n%s", text)
	}
	if strings.Contains(text, "Environment=") {
		t.Fatalf("--data-dir unit must omit Environment=:\n%s", text)
	}
}

func TestServiceInstallSecondRunNoOp(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	dataDir := t.TempDir()
	if res := runCLI(t, dataDir, "service", "install"); res.code != 0 {
		t.Fatalf("first install = %+v", res.env)
	}
	path := unitPath(managerBase)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	fake.resetCalls()
	fake.statuses = []fakeUnitState{activeUnit(42, 100, true)}
	result := runCLI(t, dataDir, "service", "install")
	if result.code != 0 {
		t.Fatalf("second install = %+v (code %d)", result.env, result.code)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("second install rewrote the managed unit")
	}
	if calls := fake.callList(); len(calls) != 0 {
		t.Fatalf("second install changed the unit or service: %v", calls)
	}
}

func TestServiceInstallConvergesEnabledActive(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	dataDir := t.TempDir()
	if res := runCLI(t, dataDir, "service", "install"); res.code != 0 {
		t.Fatalf("first install = %+v", res.env)
	}
	fake.resetCalls()
	fake.statuses = []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}
	result := runCLI(t, dataDir, "service", "install")
	if result.code != 0 {
		t.Fatalf("converge = %+v (code %d)", result.env, result.code)
	}
	if !fake.hasCall("enable "+systemd.UnitName) || !fake.hasCall("start "+systemd.UnitName) {
		t.Fatalf("converge calls = %v", fake.callList())
	}
	if fake.hasCall("daemon-reload") || fake.hasCall("restart "+systemd.UnitName) {
		t.Fatalf("converge reloaded or restarted: %v", fake.callList())
	}
	if _, err := os.Stat(unitPath(managerBase)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceInstallDifferingUnitNeedsForce(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	path := writeManagedUnit(t, managerBase, oldManagedUnit())
	result := runCLI(t, t.TempDir(), "service", "install")
	if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
		t.Fatalf("differing unit = %+v (code %d)", result.env, result.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != oldManagedUnit() {
		t.Fatal("differing unit was overwritten without --force")
	}
	if len(fake.callList()) != 0 {
		t.Fatalf("refused install changed the service: %v", fake.callList())
	}

	fake.statuses = []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}
	result = runCLI(t, t.TempDir(), "service", "install", "--force")
	if result.code != 0 {
		t.Fatalf("forced install = %+v (code %d, %s)", result.env, result.code, result.stderr)
	}
	if raw, _ := os.ReadFile(path); string(raw) == oldManagedUnit() {
		t.Fatal("forced install did not replace the unit")
	}
	if !fake.hasCall("daemon-reload") || !fake.hasCall("enable "+systemd.UnitName) || !fake.hasCall("start "+systemd.UnitName) {
		t.Fatalf("forced inactive calls = %v", fake.callList())
	}
}

func TestServiceForceActiveRestarts(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{activeUnit(7, 11, true)}}
	_, managerBase := isolateService(t, fake, nil)
	writeManagedUnit(t, managerBase, oldManagedUnit())
	result := runCLI(t, t.TempDir(), "service", "install", "--force")
	if result.code != 0 {
		t.Fatalf("forced active install = %+v (code %d)", result.env, result.code)
	}
	if !fake.hasCall("restart "+systemd.UnitName) || fake.hasCall("start "+systemd.UnitName) {
		t.Fatalf("forced active calls = %v", fake.callList())
	}
}

func TestServiceForceEnabledInactiveStartsWithoutRestart(t *testing.T) {
	inactive := inactiveUnit()
	inactive.UnitFileState = "enabled"
	fake := &fakeSystemd{statuses: []fakeUnitState{inactive, activeUnit(8, 9, true)}}
	_, managerBase := isolateService(t, fake, nil)
	writeManagedUnit(t, managerBase, oldManagedUnit())
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 0 {
		t.Fatalf("forced enabled-inactive install = %+v (code %d)", res.env, res.code)
	}
	if !fake.hasCall("start "+systemd.UnitName) || fake.hasCall("restart "+systemd.UnitName) {
		t.Fatalf("enabled-inactive calls = %v", fake.callList())
	}
}

func TestServiceInstallAdoptsInactiveMarkerlessWithForce(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	path := unitPath(managerBase)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Service]\nExecStart=/usr/bin/foreign run --headless\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, t.TempDir(), "service", "install"); res.code != 6 {
		t.Fatalf("markerless unit without --force = %+v (code %d)", res.env, res.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != foreign {
		t.Fatal("markerless inactive unit changed without --force")
	}
	fake.statuses = []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 0 {
		t.Fatalf("adoption = %+v (code %d, %s)", res.env, res.code, res.stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), systemd.OwnershipMarker) {
		t.Fatalf("adopted unit has no marker: %s, %v", raw, err)
	}
}

func TestServiceInstallRefusesActiveMarkerlessAndUnsafeUnits(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{activeUnit(9, 12, true)}}
	_, managerBase := isolateService(t, fake, nil)
	path := unitPath(managerBase)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Service]\nExecStart=/usr/bin/foreign run --headless\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"service", "install"}, {"service", "install", "--force"}} {
		if res := runCLI(t, t.TempDir(), args...); res.code != 6 {
			t.Fatalf("%v on active markerless = %+v (code %d)", args, res.env, res.code)
		}
	}
	if raw, _ := os.ReadFile(path); string(raw) != foreign {
		t.Fatal("active markerless unit changed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	fake.statuses = []fakeUnitState{inactiveUnit()}
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 6 {
		t.Fatalf("symlink unit = %+v (code %d)", res.env, res.code)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "target" {
		t.Fatal("symlink target changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 6 {
		t.Fatalf("non-regular unit = %+v (code %d)", res.env, res.code)
	}
}

func TestServiceInstallRefusesOtherEngineAndAllowsOwn(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit()}}
	_, managerBase := isolateService(t, fake, nil)
	dataDir := t.TempDir()
	useRealLockProbe(t)
	paths, err := store.ResolvePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	releaseApp := holdLock(t, paths.AppLock)
	result := runCLI(t, dataDir, "service", "install")
	if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
		t.Fatalf("app running = %+v (code %d)", result.env, result.code)
	}
	if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused install wrote a unit")
	}
	releaseApp()
	// A headless engine owns the canonical state lock, not the app lock.
	fake.statuses = []fakeUnitState{inactiveUnit()}
	releaseEngine := holdLock(t, paths.Lock)
	result = runCLI(t, dataDir, "service", "install")
	if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
		t.Fatalf("headless engine running = %+v (code %d)", result.env, result.code)
	}
	releaseEngine()
	// A forgotten old engine's legacy lock is not this unit's engine.
	fake.statuses = []fakeUnitState{inactiveUnit()}
	releaseLegacy := holdLock(t, paths.LegacyLocks[0].Lock)
	result = runCLI(t, dataDir, "service", "install")
	if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
		t.Fatalf("legacy engine running = %+v (code %d)", result.env, result.code)
	}
	releaseLegacy()
	// Own service: write the managed unit, then hold its canonical and legacy
	// locks and report the unit active. Those locks belong to this unit.
	fake.statuses = []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}
	if res := runCLI(t, dataDir, "service", "install"); res.code != 0 {
		t.Fatalf("first install = %+v (code %d, %s)", res.env, res.code, res.stderr)
	}
	holdLock(t, paths.Lock)
	holdLock(t, paths.LegacyLocks[0].Lock)
	fake.resetCalls()
	fake.statuses = []fakeUnitState{activeUnit(123, 456, true)}
	result = runCLI(t, dataDir, "service", "install")
	if result.code != 0 {
		t.Fatalf("own active service with legacy locks = %+v (code %d, %s)", result.env, result.code, result.stderr)
	}
	if calls := fake.callList(); len(calls) != 0 {
		t.Fatalf("idempotent own-service install changed state: %v", calls)
	}
}

func TestServiceForceDoesNotExemptAnotherActiveInstance(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	useRealLockProbe(t)
	// The target root is a prefix of the active unit's root; a substring
	// identity check would wrongly treat them as the same instance.
	base := t.TempDir()
	dataA := filepath.Join(base, "instance2")
	pathsB, err := store.ResolvePaths(filepath.Join(base, "instance"))
	if err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, dataA, "service", "install"); res.code != 0 {
		t.Fatalf("install A = %+v (code %d, %s)", res.env, res.code, res.stderr)
	}
	path := unitPath(managerBase)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	releaseB := holdLock(t, pathsB.Lock)
	fake.resetCalls()
	fake.statuses = []fakeUnitState{activeUnit(1, 1, true)}
	result := runCLI(t, pathsB.DataDir, "service", "install", "--force")
	if result.code != 6 || result.env.Error == nil || result.env.Error.Kind != contract.StateLocked {
		t.Fatalf("other instance engine = %+v (code %d)", result.env, result.code)
	}
	releaseB()
	if raw, _ := os.ReadFile(path); string(raw) != string(before) {
		t.Fatal("refused cross-instance install rewrote the managed unit")
	}
	if len(fake.callList()) != 0 {
		t.Fatalf("refused cross-instance install changed the service: %v", fake.callList())
	}
}

func TestServicePostRenameFsyncFailureRollsBack(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	path := writeManagedUnit(t, managerBase, oldManagedUnit())
	failed := false
	serviceSyncDir = func(string) error {
		if failed {
			return nil
		}
		failed = true
		return errors.New("directory fsync failed")
	}
	result := runCLI(t, t.TempDir(), "service", "install", "--force")
	if result.code != 1 {
		t.Fatalf("post-rename fsync failure = %+v (code %d)", result.env, result.code)
	}
	if !failed {
		t.Fatal("the fsync seam was not exercised")
	}
	if raw, _ := os.ReadFile(path); string(raw) != oldManagedUnit() {
		t.Fatalf("snapshot was not restored after the committed write:\n%s", raw)
	}
}

func readySequence(states ...service.ReadyStatus) service.Readiness {
	var mu sync.Mutex
	index := 0
	return func(context.Context) (service.ReadyStatus, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(states) == 0 {
			return service.ReadyStatus{}, nil
		}
		state := states[index]
		if index < len(states)-1 {
			index++
		}
		return state, nil
	}
}

func TestServiceReadinessStoppedAndBeforeBind(t *testing.T) {
	stopped := readySequence(service.ReadyStatus{Ready: true, ListenersOpen: false, Mode: "stopped"})
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	isolateService(t, fake, stopped)
	if res := runCLI(t, t.TempDir(), "service", "install"); res.code != 0 {
		t.Fatalf("stopped mode install = %+v (code %d)", res.env, res.code)
	}

	beforeBind := readySequence(
		service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"},
		service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"},
		service.ReadyStatus{Ready: true, ListenersOpen: true, Mode: "targeted"},
	)
	fake = &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(2, 2, true)}}
	isolateService(t, fake, beforeBind)
	if res := runCLI(t, t.TempDir(), "service", "install"); res.code != 0 {
		t.Fatalf("readiness-before-bind install = %+v (code %d)", res.env, res.code)
	}

	// ready=true is not enough while a non-stopped mode has closed listeners.
	probes := 0
	bindGate := func(context.Context) (service.ReadyStatus, error) {
		probes++
		if probes < 2 {
			return service.ReadyStatus{Ready: true, ListenersOpen: false, Mode: "targeted"}, nil
		}
		return service.ReadyStatus{Ready: true, ListenersOpen: true, Mode: "targeted"}, nil
	}
	fake = &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(3, 3, true)}}
	isolateService(t, fake, bindGate)
	if res := runCLI(t, t.TempDir(), "service", "install"); res.code != 0 {
		t.Fatalf("listeners-open gate install = %+v (code %d)", res.env, res.code)
	}
	if probes < 2 {
		t.Fatalf("install accepted ready=true with listeners_open=false after %d probe(s)", probes)
	}
}

func TestServiceReadinessTimeoutRollsBackFreshInstall(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(5, 6, true)}}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	result := runCLI(t, t.TempDir(), "service", "install")
	if result.code != 8 || result.env.Error == nil || result.env.Error.Kind != contract.Timeout {
		t.Fatalf("timeout = %+v (code %d)", result.env, result.code)
	}
	if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("timeout rollback left the new unit")
	}
	if !fake.hasCall("stop "+systemd.UnitName) || !fake.hasCall("disable "+systemd.UnitName) {
		t.Fatalf("fresh rollback calls = %v", fake.callList())
	}
}

func TestServiceChildExitMapping(t *testing.T) {
	cases := []struct{ child, want int }{{1, 1}, {4, 6}, {6, 6}, {7, 7}, {9, 9}, {10, 10}}
	for _, c := range cases {
		fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), failedUnit(c.child)}}
		_, managerBase := isolateService(t, fake, nil)
		result := runCLI(t, t.TempDir(), "service", "install")
		if result.code != c.want {
			t.Fatalf("child exit %d -> %d, want %d (%+v)", c.child, result.code, c.want, result.env)
		}
		if result.env.Error == nil {
			t.Fatalf("child exit %d returned no error", c.child)
		}
		if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("child exit %d rollback left the unit", c.child)
		}
	}
}

func TestServiceRollbackRestoresPreviousProcess(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{
		activeUnit(1, 100, true),
		activeUnit(2, 200, true),
	}}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	path := writeManagedUnit(t, managerBase, oldManagedUnit())
	result := runCLI(t, t.TempDir(), "service", "install", "--force")
	if result.code != 8 {
		t.Fatalf("rollback scenario = %+v (code %d)", result.env, result.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != oldManagedUnit() {
		t.Fatalf("previous unit was not restored:\n%s", raw)
	}
	calls := fake.callList()
	if !containsCall(calls, "stop "+systemd.UnitName) || !containsCall(calls, "start "+systemd.UnitName) {
		t.Fatalf("previous-active rollback calls = %v", calls)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestServiceRollbackAfterFailedRestartStartsPrevious(t *testing.T) {
	fake := &fakeSystemd{
		statuses: []fakeUnitState{activeUnit(1, 100, true), inactiveUnit()},
		failures: map[string]error{"restart": errors.New("restart failed")},
	}
	_, managerBase := isolateService(t, fake, nil)
	path := writeManagedUnit(t, managerBase, oldManagedUnit())
	result := runCLI(t, t.TempDir(), "service", "install", "--force")
	if result.code != 1 || result.env.Error == nil || result.env.Error.Kind != contract.ExternalToolFailed {
		t.Fatalf("failed restart = %+v (code %d)", result.env, result.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != oldManagedUnit() {
		t.Fatalf("failed restart did not restore the previous unit:\n%s", raw)
	}
	if !fake.hasCall("stop "+systemd.UnitName) || !fake.hasCall("start "+systemd.UnitName) {
		t.Fatalf("failed restart rollback calls = %v", fake.callList())
	}
}

func TestServiceRollbackSameProcessDoesNotStop(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{activeUnit(1, 100, true)}}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	path := writeManagedUnit(t, managerBase, oldManagedUnit())
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 8 {
		t.Fatalf("scenario = %+v (code %d)", res.env, res.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != oldManagedUnit() {
		t.Fatal("same-process rollback did not restore bytes")
	}
	if fake.hasCall("stop "+systemd.UnitName) || fake.hasCall("start "+systemd.UnitName) {
		t.Fatalf("same-process rollback stopped/started: %v", fake.callList())
	}
}

func TestServiceRollbackRestoresDisabledPrevious(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{
		activeUnit(1, 100, false),
		activeUnit(2, 200, false),
	}}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	writeManagedUnit(t, managerBase, oldManagedUnit())
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 8 {
		t.Fatalf("scenario = %+v (code %d)", res.env, res.code)
	}
	calls := fake.callList()
	if !containsCall(calls, "disable "+systemd.UnitName) || !containsCall(calls, "start "+systemd.UnitName) {
		t.Fatalf("disabled-active rollback calls = %v", calls)
	}
}

func TestServiceRollbackAdoptionDoesNotStart(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	path := unitPath(managerBase)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Service]\nExecStart=/usr/bin/foreign run --headless\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, t.TempDir(), "service", "install", "--force"); res.code != 8 {
		t.Fatalf("adoption rollback = %+v (code %d)", res.env, res.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != foreign {
		t.Fatalf("adoption rollback did not restore bytes:\n%s", raw)
	}
	calls := fake.callList()
	lastStop := -1
	for i, call := range calls {
		if call == "stop "+systemd.UnitName {
			lastStop = i
		}
	}
	for i, call := range calls {
		if call == "start "+systemd.UnitName && i > lastStop {
			t.Fatalf("adoption rollback started the previous unit: %v", calls)
		}
	}
}

func TestServiceRollbackFailureLeavesUnit(t *testing.T) {
	fake := &fakeSystemd{
		statuses: []fakeUnitState{inactiveUnit(), activeUnit(3, 4, true)},
		failures: map[string]error{"stop": errors.New("stop failed")},
	}
	_, managerBase := isolateService(t, fake, readySequence(service.ReadyStatus{Ready: false, ListenersOpen: false, Mode: "targeted"}))
	result := runCLI(t, t.TempDir(), "service", "install")
	if result.code != 1 || result.env.Error == nil {
		t.Fatalf("rollback failure = %+v (code %d)", result.env, result.code)
	}
	if result.env.Error.Detail["reason"] != "rollback_failed" ||
		!strings.Contains(result.env.Error.Message, "rollback") ||
		!strings.Contains(result.env.Error.Message, "install") {
		t.Fatalf("rollback failure detail = %+v", result.env.Error)
	}
	if _, err := os.Stat(unitPath(managerBase)); err != nil {
		t.Fatal("rollback failure removed the unit despite the failure")
	}
}

func TestServiceAutostartConflictAndStatus(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit()}}
	home, managerBase := isolateService(t, fake, nil)
	entry := filepath.Join(home, "config", "autostart", autostart.Filename)
	if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("[Desktop Entry]\n"+autostart.MarkerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, t.TempDir(), "service", "install"); res.code != 6 {
		t.Fatalf("install with managed autostart = %+v (code %d)", res.env, res.code)
	}
	if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install wrote a unit despite the autostart conflict")
	}

	writeManagedUnit(t, managerBase, systemd.OwnershipMarker+"\n")
	status := runDefaultCLI(t, "service", "status")
	if status.code != 0 || serviceData(t, status)["conflict"] != service.ConflictAutostart {
		t.Fatalf("status with autostart = %+v (code %d)", status.env, status.code)
	}
}

func TestServiceUninstallOwnershipAndIdempotence(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}}
	_, managerBase := isolateService(t, fake, nil)
	dataDir := t.TempDir()
	if res := runCLI(t, dataDir, "service", "install"); res.code != 0 {
		t.Fatalf("install = %+v", res.env)
	}
	fake.resetCalls()
	result := runCLI(t, dataDir, "service", "uninstall")
	if result.code != 0 || !result.env.OK {
		t.Fatalf("uninstall = %+v (code %d)", result.env, result.code)
	}
	if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("uninstall left the managed unit")
	}
	if !fake.hasCall("stop "+systemd.UnitName) || !fake.hasCall("disable "+systemd.UnitName) || !fake.hasCall("daemon-reload") {
		t.Fatalf("uninstall calls = %v", fake.callList())
	}
	fake.statuses = []fakeUnitState{inactiveUnit()}
	if res := runCLI(t, dataDir, "service", "uninstall"); res.code != 0 {
		t.Fatalf("second uninstall = %+v (code %d)", res.env, res.code)
	}

	// Foreign unit: untouched, exit 6, no stop/disable/remove.
	path := writeManagedUnit(t, managerBase, "[Service]\nExecStart=/usr/bin/foreign run\n")
	before, _ := os.ReadFile(path)
	fake.resetCalls()
	foreign := runCLI(t, dataDir, "service", "uninstall")
	if foreign.code != 6 || foreign.env.Error == nil || foreign.env.Error.Kind != contract.StateLocked {
		t.Fatalf("foreign uninstall = %+v (code %d)", foreign.env, foreign.code)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(before) {
		t.Fatal("foreign unit changed")
	}
	if len(fake.callList()) != 0 {
		t.Fatalf("foreign uninstall touched the manager: %v", fake.callList())
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	fake.resetCalls()
	if res := runCLI(t, dataDir, "service", "uninstall"); res.code != 6 {
		t.Fatalf("symlink uninstall = %+v (code %d)", res.env, res.code)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "target" {
		t.Fatal("symlink target changed during uninstall")
	}
	if len(fake.callList()) != 0 {
		t.Fatalf("symlink uninstall touched the manager: %v", fake.callList())
	}
}

func TestServiceStatusReadOnlyAndUnavailableManager(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{activeUnit(4, 5, true)}}
	_, managerBase := isolateService(t, fake, nil)
	writeManagedUnit(t, managerBase, systemd.OwnershipMarker+"\n")
	status := runDefaultCLI(t, "service", "status")
	if status.code != 0 {
		t.Fatalf("status = %+v (code %d)", status.env, status.code)
	}
	data := serviceData(t, status)
	if data["enabled"] != true || data["active"] != true || data["managed"] != true || data["unit"] != unitPath(managerBase) {
		t.Fatalf("status data = %+v", data)
	}
	if len(fake.callList()) != 0 {
		t.Fatalf("status performed mutating commands: %v", fake.callList())
	}

	fake = &fakeSystemd{failures: map[string]error{
		"show-environment": errors.New("Failed to connect to bus"),
		"show":             errors.New("Failed to connect to bus"),
	}}
	_, managerBase = isolateService(t, fake, nil)
	for _, verb := range []string{"install", "status", "uninstall"} {
		res := runDefaultCLI(t, "service", verb)
		if res.code != 7 || res.env.Error == nil || res.env.Error.Kind != contract.Unsupported {
			t.Fatalf("%s without a manager = %+v (code %d)", verb, res.env, res.code)
		}
	}
	if _, err := os.Lstat(unitPath(managerBase)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install wrote a unit without a confirmed manager base")
	}
}

func TestServiceLingerHintAndWrongUser(t *testing.T) {
	fake := &fakeSystemd{statuses: []fakeUnitState{inactiveUnit(), activeUnit(1, 1, true)}, linger: "no"}
	isolateService(t, fake, nil)
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"service", "install"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("install exit = %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "service installed:") || !strings.Contains(out.String(), "linger: no") {
		t.Fatalf("human output =\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "sudo loginctl enable-linger ") {
		t.Fatalf("missing linger hint: %s", errOut.String())
	}

	for key, value := range map[string]string{"SUDO_USER": "someone-else", "PKEXEC_UID": "1234567"} {
		isolateService(t, &fakeSystemd{statuses: []fakeUnitState{inactiveUnit()}}, nil)
		t.Setenv(key, value)
		res := runDefaultCLI(t, "service", "install")
		if res.code != 9 || res.env.Error == nil || res.env.Error.Kind != contract.PolicyDenied {
			t.Fatalf("%s=%s = %+v (code %d)", key, value, res.env, res.code)
		}
	}
}

func TestServiceUnknownVerb(t *testing.T) {
	isolateService(t, &fakeSystemd{}, nil)
	res := runDefaultCLI(t, "service", "maybe")
	if res.code != 2 || res.env.Error == nil || res.env.Error.Kind != contract.InvalidDestination {
		t.Fatalf("unknown verb = %+v (code %d)", res.env, res.code)
	}
}

// TestRealUserManagerServiceStatus is opt-in and read-only: it resolves the
// real user manager and reports the current unit without writing, starting,
// stopping or reloading anything. The gated install lifecycle is not run here
// because this host already has a live routelatch.service that must not be
// touched.
func TestRealUserManagerServiceStatus(t *testing.T) {
	if os.Getenv("ROUTELATCH_SYSTEMD_SERVICE_STATUS") != "1" {
		t.Skip("set ROUTELATCH_SYSTEMD_SERVICE_STATUS=1 for the read-only real user-manager status check")
	}
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "service", "status"}, &out, &errOut)
	if code == 7 {
		t.Skipf("systemd --user is not available: %s", errOut.String())
	}
	if code != 0 {
		t.Fatalf("real service status exit = %d: %s %s", code, out.String(), errOut.String())
	}
}
