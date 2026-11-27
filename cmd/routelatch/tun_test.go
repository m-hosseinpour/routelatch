package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/egress"
	"routelatch/internal/store"
	tunsetup "routelatch/internal/tun/setup"
)

func TestTunServiceEntrypointIsHiddenAndFailsClosed(t *testing.T) {
	dir := t.TempDir()

	// It takes no positional arguments: the installed unit passes only
	// --config, and no interface/route/domain can be requested.
	extra := runCLI(t, dir, "__tun-service", "extra")
	if extra.code != 2 {
		t.Fatalf("__tun-service extra exit = %d, want 2 (%+v)", extra.code, extra.env)
	}

	// A missing/untrusted config is refused before any listener or TUN is
	// created. The path is deliberately absent so this is safe on any host.
	missing := filepath.Join(t.TempDir(), "absent.json")
	refused := runCLI(t, dir, "__tun-service", "--config", missing)
	if refused.code != 1 {
		t.Fatalf("__tun-service untrusted config exit = %d, want 1", refused.code)
	}
}

type cliTunRuntime struct {
	mu         sync.Mutex
	status     contract.TunStatus
	enableErr  error
	disableErr error
	enables    int
	disables   int
}

func (f *cliTunRuntime) Status(context.Context) (contract.TunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *cliTunRuntime) Enable(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enables++
	if f.enableErr != nil {
		return f.enableErr
	}
	f.status.State = contract.TunOn
	f.status.Helper = contract.TunHelperInstalled
	f.status.IPv6 = true
	return nil
}

func (f *cliTunRuntime) Disable(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disables++
	if f.disableErr != nil {
		return f.disableErr
	}
	f.status.State = contract.TunOff
	return nil
}

func startInProcessWithTun(t *testing.T, rt contract.TunRuntime) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rltuncli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := control.NewService(context.Background(), control.Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		Bypass:            egress.NewController(),
		PhysicalInterface: func() (string, error) { return "eth0", nil },
		TunBindingCheck:   func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: svc, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return dir
}

func tunData(t *testing.T, result cliResult) map[string]any {
	t.Helper()
	data, ok := result.env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %+v", result.env)
	}
	return data
}

func TestTunStatusOnOffAndAdditiveJSON(t *testing.T) {
	rt := &cliTunRuntime{status: contract.TunStatus{
		State: contract.TunOff, DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked,
		Helper: contract.TunHelperInstalled,
	}}
	dir := startInProcessWithTun(t, rt)

	status := runCLI(t, dir, "status")
	if status.code != 0 {
		t.Fatalf("status exit = %d (%+v)", status.code, status.env)
	}
	statusData := tunData(t, status)
	tunObj, ok := statusData["tun"].(map[string]any)
	if !ok {
		t.Fatalf("status.tun missing: %v", statusData)
	}
	if tunObj["state"] != "off" || tunObj["dns"] != "direct" || tunObj["udp"] != "blocked" {
		t.Fatalf("status.tun = %v", tunObj)
	}
	if _, leaked := tunObj["socks_address"]; leaked {
		t.Fatalf("status.tun leaked the private socks target: %v", tunObj)
	}
	if strings.Contains(status.stdout, "socks_address") || strings.Contains(status.stdout, `"domain"`) {
		t.Fatalf("status JSON is not safe: %s", status.stdout)
	}
	for _, forbidden := range []string{"socks_address", "destination", "domain"} {
		if _, leaked := tunObj[forbidden]; leaked {
			t.Fatalf("status.tun leaked %q: %v", forbidden, tunObj)
		}
	}

	off := runCLI(t, dir, "tun", "off")
	if off.code != 0 || !off.env.OK || off.env.Command != "tun off" {
		t.Fatalf("tun off = %+v (exit %d)", off.env, off.code)
	}
	if tunData(t, off)["state"] != "off" {
		t.Fatalf("tun off data = %v", off.env.Data)
	}

	on := runCLI(t, dir, "tun", "on")
	if on.code != 0 || !on.env.OK || on.env.Command != "tun on" {
		t.Fatalf("tun on = %+v (exit %d)", on.env, on.code)
	}
	onData := tunData(t, on)
	if onData["state"] != "on" || onData["desired"] != true || onData["ipv6"] != true {
		t.Fatalf("tun on data = %v", onData)
	}

	again := runCLI(t, dir, "tun", "on")
	if again.code != 0 || tunData(t, again)["state"] != "on" {
		t.Fatalf("idempotent tun on = %+v (exit %d)", again.env, again.code)
	}
}

func TestTunUnsupportedAndStateLockedExits(t *testing.T) {
	rt := &cliTunRuntime{
		status: contract.TunStatus{State: contract.TunOff, DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked, Helper: contract.TunHelperInstalled},
		enableErr: &contract.TunError{
			Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
		},
	}
	dir := startInProcessWithTun(t, rt)
	got := runCLI(t, dir, "tun", "on")
	if got.code != 7 || got.env.Error == nil || got.env.Error.Kind != contract.Unsupported {
		t.Fatalf("unsupported = %+v (exit %d)", got.env, got.code)
	}
	if reason, _ := got.env.Error.Detail["reason"].(string); reason != string(contract.TunReasonHelperUnavailable) {
		t.Fatalf("unsupported reason = %q", reason)
	}

	rt.mu.Lock()
	rt.enableErr = &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict}
	rt.mu.Unlock()
	got = runCLI(t, dir, "tun", "on")
	if got.code != 6 || got.env.Error == nil || got.env.Error.Kind != contract.StateLocked {
		t.Fatalf("state_locked = %+v (exit %d)", got.env, got.code)
	}
	if reason, _ := got.env.Error.Detail["reason"].(string); reason != string(contract.TunReasonConflict) {
		t.Fatalf("state_locked reason = %q", reason)
	}
}

func TestTunOnInStoppedModeExits3(t *testing.T) {
	rt := &cliTunRuntime{status: contract.TunStatus{
		State: contract.TunOff, DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked,
		Helper: contract.TunHelperInstalled,
	}}
	dir := startInProcessWithTun(t, rt)
	stopped := runCLI(t, dir, "mode", "stopped")
	if stopped.code != 0 {
		t.Fatalf("mode stopped exit = %d (%+v)", stopped.code, stopped.env)
	}
	got := runCLI(t, dir, "tun", "on")
	if got.code != 3 || got.env.Error == nil || got.env.Error.Kind != contract.Stopped {
		t.Fatalf("stopped tun on = %+v (exit %d)", got.env, got.code)
	}
	// Disable is always permitted, including stopped.
	off := runCLI(t, dir, "tun", "off")
	if off.code != 0 {
		t.Fatalf("tun off in stopped mode exit = %d (%+v)", off.code, off.env)
	}
}

func TestTunStatusWithoutServiceExits3(t *testing.T) {
	dir := t.TempDir()
	got := runCLI(t, dir, "tun", "status")
	if got.code != 3 || got.env.Command != "tun status" || got.env.Error == nil {
		t.Fatalf("tun status without service = %+v (exit %d)", got.env, got.code)
	}
}

type cliTunRunner struct {
	mu     sync.Mutex
	calls  []string
	active string
}

func (r *cliTunRunner) Output(_ context.Context, name string, args ...string) (string, error) {
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

func testTunSetupManager(t *testing.T) (*tunsetup.Manager, *cliTunRunner, string) {
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
	runner := &cliTunRunner{}
	m := &tunsetup.Manager{
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
		Paths: tunsetup.Paths{
			ServiceUnit: filepath.Join(dir, "etc/systemd/system", tunsetup.ServiceUnitName),
			SocketUnit:  filepath.Join(dir, "etc/systemd/system", tunsetup.SocketUnitName),
			Config:      filepath.Join(dir, "etc/routelatch/tun.json"),
			Binary:      filepath.Join(dir, "usr/libexec/routelatch/tun-service"),
			RuntimeDir:  filepath.Join(dir, "run/routelatch-tun"),
		},
		SocksAddress: tunsetup.DefaultSocksAddress,
	}
	return m, runner, dir
}

func withTunSetupManager(t *testing.T, m *tunsetup.Manager) {
	t.Helper()
	old := newTunSetupManager
	t.Cleanup(func() { newTunSetupManager = old })
	newTunSetupManager = func(force bool, socks string) *tunsetup.Manager {
		m.Force = force
		if socks != "" {
			m.SocksAddress = socks
		}
		return m
	}
}

func TestTunSetupAndUninstallCLIIdempotent(t *testing.T) {
	m, _, _ := testTunSetupManager(t)
	withTunSetupManager(t, m)

	first := runCLI(t, t.TempDir(), "tun", "setup")
	if first.code != 0 || !first.env.OK || first.env.Command != "tun setup" {
		t.Fatalf("tun setup = %+v (exit %d, stderr %s)", first.env, first.code, first.stderr)
	}
	data := tunData(t, first)
	if data["changed"] != true || data["allowed_uid"] != float64(1000) || data["managed"] != true {
		t.Fatalf("setup data = %v", data)
	}
	second := runCLI(t, t.TempDir(), "tun", "setup")
	if second.code != 0 {
		t.Fatalf("second tun setup exit = %d (%+v)", second.code, second.env)
	}
	if tunData(t, second)["changed"] != false {
		t.Fatalf("second setup data = %v", second.env.Data)
	}

	uninstall := runCLI(t, t.TempDir(), "tun", "uninstall")
	if uninstall.code != 0 || !uninstall.env.OK || uninstall.env.Command != "tun uninstall" {
		t.Fatalf("tun uninstall = %+v (exit %d)", uninstall.env, uninstall.code)
	}
	removed, _ := tunData(t, uninstall)["removed"].([]any)
	if len(removed) == 0 {
		t.Fatalf("uninstall removed nothing: %v", uninstall.env.Data)
	}
	for _, path := range []string{m.Paths.ServiceUnit, m.Paths.SocketUnit, m.Paths.Config, m.Paths.Binary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists (err %v)", path, err)
		}
	}
	again := runCLI(t, t.TempDir(), "tun", "uninstall")
	if again.code != 0 {
		t.Fatalf("idempotent uninstall exit = %d (%+v)", again.code, again.env)
	}
}

func TestTunSetupForeignUnitAndRootRefusals(t *testing.T) {
	m, _, _ := testTunSetupManager(t)
	withTunSetupManager(t, m)
	if err := os.MkdirAll(filepath.Dir(m.Paths.ServiceUnit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Paths.ServiceUnit, []byte("foreign\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runCLI(t, t.TempDir(), "tun", "setup")
	if got.code != 6 || got.env.Error == nil || got.env.Error.Kind != contract.StateLocked {
		t.Fatalf("foreign setup = %+v (exit %d)", got.env, got.code)
	}
	if reason, _ := got.env.Error.Detail["reason"].(string); reason != string(contract.TunReasonConflict) {
		t.Fatalf("foreign setup reason = %q", reason)
	}
	if b, _ := os.ReadFile(m.Paths.ServiceUnit); string(b) != "foreign\n" {
		t.Fatalf("foreign unit overwritten: %q", b)
	}

	m.Euid = func() int { return 1000 }
	got = runCLI(t, t.TempDir(), "tun", "setup")
	if got.code != 9 || got.env.Error == nil || got.env.Error.Kind != contract.PolicyDenied {
		t.Fatalf("non-root setup = %+v (exit %d)", got.env, got.code)
	}
}

func TestTunHelpUsageAndHiddenService(t *testing.T) {
	help := runCLI(t, t.TempDir(), "help")
	if help.code != 0 {
		t.Fatalf("help exit = %d", help.code)
	}
	if !strings.Contains(help.stderr, "tun status|on|off") || !strings.Contains(help.stderr, "tun setup|uninstall") {
		t.Fatalf("main usage missing tun commands:\n%s", help.stderr)
	}
	if strings.Contains(help.stderr, "__tun-service") {
		t.Fatalf("main usage exposed the hidden service:\n%s", help.stderr)
	}
	tunHelp := runCLI(t, t.TempDir(), "tun", "help")
	if tunHelp.code != 0 || !strings.Contains(tunHelp.stderr, "sudo routelatch tun setup") {
		t.Fatalf("tun help = %d\n%s", tunHelp.code, tunHelp.stderr)
	}
	setupHelp := runCLI(t, t.TempDir(), "tun", "setup", "--help")
	if setupHelp.code != 0 || !strings.Contains(setupHelp.stderr, "--socks") {
		t.Fatalf("tun setup help = %d\n%s", setupHelp.code, setupHelp.stderr)
	}
	statusHelp := runCLI(t, t.TempDir(), "tun", "status", "--help")
	if statusHelp.code != 0 || !strings.Contains(statusHelp.stderr, "tun <command>") {
		t.Fatalf("tun status help = %d\n%s", statusHelp.code, statusHelp.stderr)
	}
	noArgs := runCLI(t, t.TempDir(), "tun")
	if noArgs.code != 2 {
		t.Fatalf("tun without subcommand exit = %d", noArgs.code)
	}
}

func TestTunMenuMappingAndInstallHelpOnly(t *testing.T) {
	args, ok := menuCommand("tun", "status", "", "")
	if !ok || len(args) != 2 || args[0] != "tun" || args[1] != "status" {
		t.Fatalf("tun menu command = %v, %v", args, ok)
	}
	var found bool
	for _, choice := range menuChoices() {
		if choice.Key == "tun" {
			found = true
			if strings.Contains(choice.Label, "setup") || strings.Contains(choice.Label, "sudo") {
				t.Fatalf("menu label must not offer installation: %q", choice.Label)
			}
		}
	}
	if !found {
		t.Fatal("menu has no TUN coverage item")
	}
	// The menu's help choice is a plain `tun help`; that text only displays
	// the sudo command and never runs installation.
	helpArgs, ok := menuCommand("tun", "help", "", "")
	if !ok || len(helpArgs) != 2 || helpArgs[1] != "help" {
		t.Fatalf("tun help menu command = %v, %v", helpArgs, ok)
	}
}
