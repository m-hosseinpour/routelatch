package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/tun"
)

type fakeRunner struct {
	mu     sync.Mutex
	calls  []string
	active string
}

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[0] == "is-active" {
		if r.active == "" {
			return "inactive", nil
		}
		return r.active, nil
	}
	return "", nil
}

func (r *fakeRunner) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func (r *fakeRunner) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *fakeRunner) count(prefix string) int {
	n := 0
	for _, call := range r.recorded() {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func testManager(t *testing.T) (*Manager, *fakeRunner, string) {
	t.Helper()
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(srcDir, "routelatch")
	if err := os.WriteFile(source, []byte("test binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := Paths{
		ServiceUnit: filepath.Join(dir, "etc/systemd/system", ServiceUnitName),
		SocketUnit:  filepath.Join(dir, "etc/systemd/system", SocketUnitName),
		Config:      filepath.Join(dir, "etc/routelatch/tun.json"),
		Binary:      filepath.Join(dir, "usr/libexec/routelatch/tun-service"),
		RuntimeDir:  filepath.Join(dir, "run/routelatch-tun"),
	}
	runner := &fakeRunner{}
	m := &Manager{
		Runner:   runner,
		LookPath: func(string) (string, error) { return "/bin/systemctl", nil },
		Getenv: func(key string) string {
			if key == "SUDO_UID" {
				return "1000"
			}
			return ""
		},
		Euid:          func() int { return 0 },
		Getuid:        func() int { return 1000 },
		Executable:    func() (string, error) { return source, nil },
		Owner:         func(string) (int, error) { return 0, nil },
		Chown:         func(string, int, int) error { return nil },
		KernelVersion: func() (int, int, error) { return 6, 8, nil },
		TunDevice:     func() error { return nil },
		Paths:         paths,
		SocksAddress:  "127.0.0.1:8318",
	}
	return m, runner, dir
}

func TestInstallWritesOwnedHelperAndIsIdempotent(t *testing.T) {
	m, runner, _ := testManager(t)
	status, err := m.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !status.Changed || !status.Managed || status.AllowedUID != 1000 {
		t.Fatalf("status = %+v", status)
	}
	service, err := os.ReadFile(m.Paths.ServiceUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		OwnershipMarker, "User=1000\n", "AmbientCapabilities=CAP_NET_ADMIN\n",
		"CapabilityBoundingSet=CAP_NET_ADMIN\n", "NoNewPrivileges=yes\n",
		"RuntimeDirectory=routelatch-tun\n", "RuntimeDirectoryPreserve=yes\n",
		"PrivateDevices=no\n", "ExecStart=" + m.Paths.Binary + " __tun-service --config " + m.Paths.Config + "\n",
	} {
		if !strings.Contains(string(service), want) {
			t.Errorf("service unit missing %q:\n%s", want, service)
		}
	}
	socket, err := os.ReadFile(m.Paths.SocketUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{OwnershipMarker, "SocketMode=0600\n", "SocketUser=1000\n", "ListenStream=" + filepath.Join(m.Paths.RuntimeDir, "control.sock") + "\n"} {
		if !strings.Contains(string(socket), want) {
			t.Errorf("socket unit missing %q:\n%s", want, socket)
		}
	}
	cfg, err := tun.LoadServiceConfig(m.Paths.Config)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.AllowedUID != 1000 || cfg.SocksAddress != "127.0.0.1:8318" {
		t.Fatalf("config = %+v", cfg)
	}
	for path, mode := range map[string]os.FileMode{
		m.Paths.ServiceUnit: 0o644, m.Paths.SocketUnit: 0o644, m.Paths.Config: 0o644, m.Paths.Binary: 0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
		}
	}
	info, err := os.Stat(m.Paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir mode = %o, want 700", info.Mode().Perm())
	}
	if runner.count("systemctl daemon-reload") != 1 || runner.count("systemctl enable --now "+SocketUnitName) != 1 {
		t.Fatalf("first install calls = %v", runner.recorded())
	}

	runner.reset()
	status, err = m.Install(context.Background())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if status.Changed {
		t.Fatalf("second install changed = true")
	}
	if runner.count("systemctl daemon-reload") != 0 {
		t.Fatalf("second install reloaded: %v", runner.recorded())
	}
	if runner.count("systemctl enable --now "+SocketUnitName) != 1 {
		t.Fatalf("second install did not converge the socket: %v", runner.recorded())
	}
}

func TestInstallRefusesForeignUnitUnlessForcedAndInactive(t *testing.T) {
	m, runner, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(m.Paths.ServiceUnit), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "foreign unit\n"
	if err := os.WriteFile(m.Paths.ServiceUnit, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := m.Install(context.Background())
	var opErr *control.OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.StateLocked || opErr.Exit != contract.ExitConflict {
		t.Fatalf("foreign install error = %v", err)
	}
	got, _ := os.ReadFile(m.Paths.ServiceUnit)
	if string(got) != foreign {
		t.Fatalf("foreign unit was overwritten: %q", got)
	}

	runner.active = "active"
	m.Force = true
	_, err = m.Install(context.Background())
	if !errors.As(err, &opErr) || opErr.Kind != contract.StateLocked {
		t.Fatalf("active foreign force error = %v", err)
	}

	runner.active = "inactive"
	status, err := m.Install(context.Background())
	if err != nil {
		t.Fatalf("forced install: %v", err)
	}
	if !status.Changed {
		t.Fatal("forced install reported no change")
	}
	got, _ = os.ReadFile(m.Paths.ServiceUnit)
	if !strings.Contains(string(got), OwnershipMarker) {
		t.Fatalf("forced install did not write the marker: %q", got)
	}
}

func TestSetupRefusesRootAndRequiresInvokingUser(t *testing.T) {
	for name, tc := range map[string]struct {
		euid int
		uid  int
		sudo string
	}{
		"not root":        {euid: 1000, uid: 1000},
		"direct root":     {euid: 0, uid: 0},
		"root target uid": {euid: 0, uid: 1000, sudo: "0"},
		"bad sudo uid":    {euid: 0, uid: 1000, sudo: "not-a-uid"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _ := testManager(t)
			m.Euid = func() int { return tc.euid }
			m.Getuid = func() int { return tc.uid }
			m.Getenv = func(string) string { return tc.sudo }
			_, err := m.Install(context.Background())
			var opErr *control.OpError
			if !errors.As(err, &opErr) || opErr.Kind != contract.PolicyDenied || opErr.Exit != contract.ExitDenied {
				t.Fatalf("install error = %v", err)
			}
		})
	}
}

func TestSetupRejectsNonLoopbackSocksAndPreconditions(t *testing.T) {
	m, _, _ := testManager(t)
	m.SocksAddress = "192.0.2.1:8318"
	_, err := m.Install(context.Background())
	var opErr *control.OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.InvalidDestination || opErr.Exit != contract.ExitUsage {
		t.Fatalf("non-loopback socks error = %v", err)
	}

	m, _, _ = testManager(t)
	m.KernelVersion = func() (int, int, error) { return 5, 6, nil }
	_, err = m.Install(context.Background())
	if !errors.As(err, &opErr) || opErr.Kind != contract.Unsupported || opErr.Exit != contract.ExitUnsupported {
		t.Fatalf("old kernel error = %v", err)
	}

	m, _, _ = testManager(t)
	m.TunDevice = func() error { return os.ErrNotExist }
	_, err = m.Install(context.Background())
	if !errors.As(err, &opErr) || opErr.Kind != contract.Unsupported || !strings.Contains(opErr.Message, "/dev/net/tun") {
		t.Fatalf("missing device error = %v", err)
	}
}

func TestUninstallStopsFirstAndLeavesUnrelatedFiles(t *testing.T) {
	m, runner, dir := testManager(t)
	if _, err := m.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	lease := filepath.Join(m.Paths.RuntimeDir, "lease.json")
	if err := os.WriteFile(lease, []byte(`{"boot_id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelatedRuntime := filepath.Join(m.Paths.RuntimeDir, "keep.txt")
	if err := os.WriteFile(unrelatedRuntime, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelatedConfig := filepath.Join(filepath.Dir(m.Paths.Config), "keep.conf")
	if err := os.WriteFile(unrelatedConfig, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelatedUnit := filepath.Join(filepath.Dir(m.Paths.ServiceUnit), "other.service")
	if err := os.WriteFile(unrelatedUnit, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.reset()
	status, err := m.Uninstall(context.Background())
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !status.Managed || !status.Changed {
		t.Fatalf("status = %+v", status)
	}
	for _, path := range []string{m.Paths.ServiceUnit, m.Paths.SocketUnit, m.Paths.Config, m.Paths.Binary, lease} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (err %v)", path, err)
		}
	}
	for _, path := range []string{unrelatedRuntime, unrelatedConfig, unrelatedUnit} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("unrelated %s was removed: %v", path, err)
		}
	}
	calls := runner.recorded()
	stop, disable, reload := -1, -1, -1
	for i, call := range calls {
		switch {
		case strings.HasPrefix(call, "systemctl stop "+ServiceUnitName):
			stop = i
		case strings.HasPrefix(call, "systemctl disable --now "+SocketUnitName):
			disable = i
		case strings.HasPrefix(call, "systemctl daemon-reload"):
			reload = i
		}
	}
	if stop < 0 || disable < 0 || reload < 0 || disable > stop || stop > reload {
		t.Fatalf("uninstall call order = %v", calls)
	}
	_ = dir

	runner.reset()
	status, err = m.Uninstall(context.Background())
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if status.Managed || status.Changed {
		t.Fatalf("idempotent uninstall status = %+v", status)
	}
	if len(runner.recorded()) != 0 {
		t.Fatalf("idempotent uninstall ran commands: %v", runner.recorded())
	}
}

func TestUninstallRefusesForeignUnit(t *testing.T) {
	m, _, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(m.Paths.ServiceUnit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Paths.ServiceUnit, []byte("foreign\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := m.Uninstall(context.Background())
	var opErr *control.OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.StateLocked || opErr.Exit != contract.ExitConflict {
		t.Fatalf("foreign uninstall error = %v", err)
	}
	if _, statErr := os.Stat(m.Paths.ServiceUnit); statErr != nil {
		t.Fatalf("foreign unit was removed: %v", statErr)
	}
}

func TestUninstallRequiresRoot(t *testing.T) {
	m, _, _ := testManager(t)
	m.Euid = func() int { return 1000 }
	_, err := m.Uninstall(context.Background())
	var opErr *control.OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.PolicyDenied || opErr.Exit != contract.ExitDenied {
		t.Fatalf("uninstall non-root error = %v", err)
	}
}

func TestParseKernelRelease(t *testing.T) {
	major, minor, err := parseKernelRelease("6.8.0-arch1-1")
	if err != nil || major != 6 || minor != 8 {
		t.Fatalf("parse = %d.%d, %v", major, minor, err)
	}
	if _, _, err := parseKernelRelease("bad"); err == nil {
		t.Fatal("bad release accepted")
	}
}
