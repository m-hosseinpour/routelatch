package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestA13SystemdUserLifecycle is the gated companion to TestA13CrashRecovery:
// it drives the shipped unit's lifecycle under a real `systemd --user` manager
// instead of a bare foreground process. It is skipped unless
// ROUTELATCH_SYSTEMD_TEST=1 because it writes a temporary unit into the
// caller's real user-manager unit directory, reloads that manager and starts a
// service. That is an install-level action and needs the user's explicit
// authorization for the run (see this package's README.md).
//
// The unit under test is deploy/systemd/routelatch.service with ExecStart
// rewritten to the built binary plus --data-dir <temp>, so the shipped
// Restart/KillSignal/TimeoutStopSec/RestartPreventExitStatus settings are the
// ones exercised while the caller's normal data dir and ports stay untouched.
func TestA13SystemdUserLifecycle(t *testing.T) {
	if os.Getenv("ROUTELATCH_SYSTEMD_TEST") != "1" {
		t.Skip("set ROUTELATCH_SYSTEMD_TEST=1 to exercise the real systemd --user lifecycle (installs a temporary unit)")
	}
	if runtime.GOOS != "linux" {
		t.Skip("systemd user services are Linux-only")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Skipf("systemctl is not available: %v", err)
	}
	manager := managerEnvironment(t, systemctl)
	unitDir, err := managerUnitDir(manager)
	if err != nil {
		t.Skipf("user manager unit directory is unknown: %v", err)
	}
	r := report{t: t}

	dir := t.TempDir()
	// Go's test temp leaf is group-writable under a permissive umask; the
	// product fail-closes on that (docs/contracts.md §4.1), which is correct.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod data dir: %v", err)
	}
	httpPort, socksPort := freePort(t), freePort(t)
	writeConfig(t, dir, serviceConfig{
		HTTPListen:  fmt.Sprintf("127.0.0.1:%d", httpPort),
		SOCKSListen: fmt.Sprintf("127.0.0.1:%d", socksPort),
	})
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)

	unit := fmt.Sprintf("routelatch-acceptance-%d.service", os.Getpid())
	unitPath := filepath.Join(unitDir, unit)
	if _, err := os.Stat(unitPath); err == nil {
		t.Fatalf("refusing to overwrite existing unit %s", unitPath)
	}
	createdUnitDir := false
	if _, err := os.Stat(unitDir); os.IsNotExist(err) {
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatalf("create user unit directory %s: %v", unitDir, err)
		}
		createdUnitDir = true
	}
	if err := os.WriteFile(unitPath, renderUnit(t, dir), 0o644); err != nil {
		t.Fatalf("write temporary unit: %v", err)
	}
	t.Cleanup(func() {
		if out, err := sdRun(t, systemctl, "stop", unit); err != nil {
			t.Logf("cleanup: systemctl --user stop %s failed: %v (%s)", unit, err, strings.TrimSpace(out))
			_, _ = sdRun(t, systemctl, "kill", unit)
		}
		if out, err := sdRun(t, systemctl, "reset-failed", unit); err != nil {
			t.Logf("cleanup: reset-failed %s: %v (%s)", unit, err, strings.TrimSpace(out))
		}
		activeOut, _ := sdRun(t, systemctl, "is-active", unit)
		active := strings.TrimSpace(activeOut)
		if active == "active" {
			// Removing the definition now would leave a live service with no
			// unit file; report it instead of hiding the leak.
			t.Logf("temporary unit %s is still active after cleanup; left %s in place", unit, unitPath)
		} else {
			_ = os.Remove(unitPath)
		}
		_, _ = sdRun(t, systemctl, "daemon-reload")
		if createdUnitDir {
			_ = os.Remove(unitDir) // succeeds only if we left it empty
		}
		if _, err := os.Stat(unitPath); err == nil && active != "active" {
			t.Logf("temporary unit %s still exists after cleanup", unitPath)
		}
	})
	if _, err := sdRun(t, systemctl, "daemon-reload"); err != nil {
		t.Fatalf("daemon-reload: %v", err)
	}

	// 1. Start and prove the manager runs the built binary, not the caller's
	// normal install.
	pid := sdStart(t, systemctl, unit, dir)
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		t.Fatalf("read exe of main pid %d: %v", pid, err)
	}
	wantExe, err := filepath.EvalSymlinks(productBin)
	if err != nil {
		t.Fatalf("resolve built binary path %q: %v", productBin, err)
	}
	if exe != wantExe {
		r.fail("A13", "the unit runs %q, want the built binary %q", exe, productBin)
	} else {
		r.pass("A13", "the shipped unit starts under the real systemd --user manager (main pid %d, ExecStart binary %s)", pid, exe)
	}
	if ok := mustCLIStatus(t, dir); ok {
		r.pass("A13", "the systemd-run service answers `status` on its own data dir")
	}
	if listeners, ok := ssListeners(t, pid); ok {
		loopbackOnly := len(listeners) == 2
		for _, addr := range listeners {
			if !strings.HasPrefix(addr, "127.0.0.1:") {
				loopbackOnly = false
			}
		}
		if loopbackOnly {
			r.pass("A13", "the real process has exactly two listening TCP sockets, both loopback: %s", strings.Join(listeners, ", "))
		} else {
			r.fail("A13", "listeners of pid %d are %v; want exactly two loopback sockets", pid, listeners)
		}
	} else {
		r.note("A13", "ss is unavailable; the loopback-only listeners were observed in the R1 install run but are not asserted here")
	}

	// 2. Restart replaces the process and both listeners stay usable.
	if _, err := sdRun(t, systemctl, "restart", unit); err != nil {
		t.Fatalf("restart: %v", err)
	}
	pid2 := waitMainPID(t, systemctl, unit, pid, 15*time.Second)
	if pid2 == pid {
		r.fail("A13", "systemctl --user restart kept main pid %d", pid)
	} else if waitDial(httpAddr, 10*time.Second) && waitDial(socksAddr, 10*time.Second) {
		r.pass("A13", "systemctl --user restart replaced main pid %d -> %d and both listeners accept again", pid, pid2)
	} else {
		r.fail("A13", "listeners do not accept after restart (http=%v socks=%v)", dialOK(httpAddr), dialOK(socksAddr))
	}

	// 3. A foreign listener on the configured HTTP port makes startup fail with
	// the documented conflict (exit 6); RestartPreventExitStatus stops the loop.
	if _, err := sdRun(t, systemctl, "stop", unit); err != nil {
		t.Fatalf("stop before collision: %v", err)
	}
	foreign, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatalf("occupy %s: %v", httpAddr, err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := foreign.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("foreign\n"))
			_ = conn.Close()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	_, _ = sdRun(t, systemctl, "reset-failed", unit)
	_, _ = sdRun(t, systemctl, "start", unit)
	status := waitUnitStatus(t, systemctl, unit, func(u unitStatus) bool {
		return u.ActiveState == "failed" && u.ExecMainStatus == "6"
	}, 15*time.Second)
	if status.ExecMainStatus == "6" && status.NRestarts == "0" {
		r.pass("A13", "a foreign listener on %s makes the unit fail with exit 6 and systemd does not restart it (NRestarts=0)", httpAddr)
	} else {
		r.fail("A13", "port collision result: state=%s/%s exit=%s restarts=%s", status.ActiveState, status.SubState, status.ExecMainStatus, status.NRestarts)
	}
	// The operator sees this message in `systemctl status`/journald; the
	// documented truncation bound must not cut the explanation itself.
	if out, _ := sdRun(t, systemctl, "status", unit, "--no-pager", "-n", "30"); strings.Contains(out, "no traffic was redirected and no fallback port exists") {
		r.pass("A13", "the port-collision error reaches systemctl's status output complete (no fallback is explicit, not truncated)")
	} else {
		r.fail("A13", "the port-collision message is missing or truncated in systemctl status: %q", out)
	}
	conn, dialErr := net.DialTimeout("tcp", httpAddr, 2*time.Second)
	if dialErr == nil {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 16)
		n, readErr := conn.Read(buf)
		_ = conn.Close()
		select {
		case <-accepted:
			if readErr != nil || n == 0 {
				r.fail("A13", "the foreign listener accepted but sent no marker: %v", readErr)
			} else {
				r.pass("A13", "the foreign listener still owns %s and answered %q", httpAddr, strings.TrimSpace(string(buf[:n])))
			}
		case <-time.After(2 * time.Second):
			r.fail("A13", "the foreign listener did not accept after the failed start")
		}
	} else {
		r.fail("A13", "the foreign listener is gone after the failed start: %v", dialErr)
	}
	if dialOK(socksAddr) {
		r.fail("A13", "the SOCKS listener fell back to %s after the HTTP collision", socksAddr)
	} else if !dialOK(httpAddr) {
		r.fail("A13", "the foreign listener is gone after the failed start")
	} else {
		r.pass("A13", "the failed start left no SOCKS listener and no fallback port; only the foreign listener is on %s", httpAddr)
	}
	_ = foreign.Close()
	_, _ = sdRun(t, systemctl, "reset-failed", unit)

	// 4. SIGKILL: systemd's on-failure restart brings the same committed state
	// back on the same data dir.
	pid3 := sdStart(t, systemctl, unit, dir)
	if !waitDial(httpAddr, 20*time.Second) {
		t.Fatalf("HTTP listener did not bind before the crash step")
	}
	statePath := filepath.Join(dir, "state.json")
	if res := cliAt(t, dir, "rules", "add", "--kind", "domain", "sd-acceptance.rl.test"); res.code != 0 || !res.env.OK {
		t.Fatalf("rules add through the systemd-run service: exit=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state before SIGKILL: %v", err)
	}
	if err := syscall.Kill(pid3, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL main pid %d: %v", pid3, err)
	}
	pid4 := waitMainPID(t, systemctl, unit, pid3, 20*time.Second)
	if !waitDial(httpAddr, 20*time.Second) {
		t.Fatalf("HTTP listener did not come back after the crash restart")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after restart: %v", err)
	}
	status = mustUnitStatus(t, systemctl, unit)
	if bytes.Equal(before, after) && pid4 != pid3 && status.NRestarts != "0" {
		r.pass("A13", "SIGKILL of main pid %d is followed by systemd's on-failure restart (pid %d, NRestarts=%s) and state.json is byte-identical", pid3, pid4, status.NRestarts)
	} else {
		r.fail("A13", "SIGKILL recovery: state-identical=%v pid %s -> %d NRestarts=%s", bytes.Equal(before, after), status.MainPID, pid4, status.NRestarts)
	}
	if code, body := proxyRequest(t, httpAddr, "http://sd-acceptance.rl.test/"); code == 502 && strings.Contains(body, "pool unavailable") {
		r.pass("A13", "after crash recovery the approved destination still goes to the (empty) pool: 502 %q, no silent direct", strings.TrimSpace(body))
	} else {
		r.fail("A13", "post-recovery approved destination: code=%d body=%q; want 502 pool unavailable", code, body)
	}

	// 5. Stop is clean: exit 0 and both listeners are gone.
	if _, err := sdRun(t, systemctl, "stop", unit); err != nil {
		t.Fatalf("stop: %v", err)
	}
	final := waitUnitStatus(t, systemctl, unit, func(u unitStatus) bool {
		return u.ActiveState == "inactive"
	}, 20*time.Second)
	if final.ExecMainStatus == "0" && !dialOK(httpAddr) && !dialOK(socksAddr) {
		r.pass("A13", "systemctl --user stop exits 0 (SIGTERM shutdown path) and both listeners are closed")
	} else {
		r.fail("A13", "stop result: state=%s exit=%s http=%v socks=%v", final.ActiveState, final.ExecMainStatus, dialOK(httpAddr), dialOK(socksAddr))
	}
	r.note("A13", "login lifecycle (default.target pull-in, linger, restart after reboot/re-login) is not executed here; the real install run verified the enable symlink and the manager's WantedBy=default.target configuration only")
}

// managerEnvironment returns the user manager's own environment. The test
// process may have a different HOME/XDG setup, so unit paths must come from
// the manager that will run the unit.
func managerEnvironment(t *testing.T, systemctl string) map[string]string {
	t.Helper()
	out, err := exec.Command(systemctl, "--user", "show-environment").Output()
	if err != nil {
		t.Skipf("systemd --user is not available: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			env[key] = value
		}
	}
	return env
}

func managerUnitDir(env map[string]string) (string, error) {
	configHome := env["XDG_CONFIG_HOME"]
	if configHome == "" {
		if env["HOME"] == "" {
			return "", fmt.Errorf("user manager has no HOME")
		}
		configHome = filepath.Join(env["HOME"], ".config")
	}
	return filepath.Join(configHome, "systemd", "user"), nil
}

// renderUnit takes the shipped unit and points ExecStart at the built binary
// plus an isolated data dir. Every other directive is the shipped one.
func renderUnit(t *testing.T, dataDir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", "routelatch.service"))
	if err != nil {
		t.Fatalf("read shipped unit: %v", err)
	}
	old := "ExecStart=%h/.local/bin/routelatch run"
	if !strings.Contains(string(raw), old) {
		t.Fatalf("shipped unit no longer contains %q", old)
	}
	if strings.ContainsAny(productBin+dataDir, "\n\r") {
		t.Skipf("temp paths contain a newline; cannot render a unit file")
	}
	rendered := strings.Replace(string(raw), old,
		"ExecStart="+systemdArg(productBin)+" --data-dir "+systemdArg(dataDir)+" run", 1)
	return []byte(rendered)
}

// systemdArg quotes one ExecStart argument. systemd expands '%' specifiers
// unless it is doubled and uses double-quote escaping for spaces.
func systemdArg(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

type unitStatus struct {
	ActiveState    string
	SubState       string
	ExecMainStatus string
	NRestarts      string
	MainPID        string
}

func mustUnitStatus(t *testing.T, systemctl, unit string) unitStatus {
	t.Helper()
	out, err := sdRun(t, systemctl, "show", unit, "-p", "ActiveState", "-p", "SubState", "-p", "ExecMainStatus", "-p", "NRestarts", "-p", "MainPID")
	if err != nil {
		t.Fatalf("systemctl show %s: %v", unit, err)
	}
	var u unitStatus
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			u.ActiveState = value
		case "SubState":
			u.SubState = value
		case "ExecMainStatus":
			u.ExecMainStatus = value
		case "NRestarts":
			u.NRestarts = value
		case "MainPID":
			u.MainPID = value
		}
	}
	return u
}

func waitUnitStatus(t *testing.T, systemctl, unit string, cond func(unitStatus) bool, d time.Duration) unitStatus {
	t.Helper()
	deadline := time.Now().Add(d)
	var last unitStatus
	for {
		last = mustUnitStatus(t, systemctl, unit)
		if cond(last) {
			return last
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sdRun(t *testing.T, systemctl string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--user"}, args...)
	cmd := exec.Command(systemctl, full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sdStart starts the unit and returns its main pid. Type=simple reports the
// start job as done at exec time, so readiness is observed through `status`.
func sdStart(t *testing.T, systemctl, unit, dataDir string) int {
	t.Helper()
	if _, err := sdRun(t, systemctl, "start", unit); err != nil {
		t.Fatalf("systemctl --user start %s: %v", unit, err)
	}
	st := waitUnitStatus(t, systemctl, unit, func(u unitStatus) bool {
		return u.ActiveState == "active" && u.MainPID != "" && u.MainPID != "0"
	}, 20*time.Second)
	if st.ActiveState != "active" {
		t.Fatalf("unit %s did not become active: %+v", unit, st)
	}
	pid := 0
	_, _ = fmt.Sscanf(st.MainPID, "%d", &pid)
	if pid == 0 {
		t.Fatalf("unit %s has no main pid: %+v", unit, st)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !mustCLIStatus(t, dataDir) {
		if time.Now().After(deadline) {
			t.Fatalf("service main pid %d never answered status", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return pid
}

// cliAt runs one product command against a data dir (the same path the
// manager-run service uses).
func cliAt(t *testing.T, dataDir string, args ...string) cliResult {
	t.Helper()
	full := append([]string{"--json", "--data-dir", dataDir}, args...)
	var out, errb strings.Builder
	cmd := exec.Command(productBin, full...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	res := cliResult{stdout: out.String(), stderr: errb.String()}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("cli %v: %v", args, runErr)
		}
		res.code = exitErr.ExitCode()
	}
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if err := json.Unmarshal([]byte(line), &res.env); err != nil {
			continue
		}
	}
	return res
}

func waitMainPID(t *testing.T, systemctl, unit string, old int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		st := mustUnitStatus(t, systemctl, unit)
		pid := 0
		_, _ = fmt.Sscanf(st.MainPID, "%d", &pid)
		if st.ActiveState == "active" && pid != 0 && pid != old {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("unit %s did not get a new active main pid within %s (last: %+v)", unit, d, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mustCLIStatus(t *testing.T, dataDir string) bool {
	t.Helper()
	cmd := exec.Command(productBin, "--json", "--data-dir", dataDir, "status")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var env struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal(out, &env) == nil && env.OK
}

// ssListeners returns the listening TCP addresses owned by pid (empty ok when
// ss is unavailable).
func ssListeners(t *testing.T, pid int) ([]string, bool) {
	t.Helper()
	if _, err := exec.LookPath("ss"); err != nil {
		return nil, false
	}
	out, err := exec.Command("ss", "-ltnp").Output()
	if err != nil {
		return nil, false
	}
	var addrs []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, fmt.Sprintf("pid=%d,", pid)) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			addrs = append(addrs, fields[3])
		}
	}
	return addrs, true
}

func dialOK(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitDial waits for a listener to accept. systemd marks a Type=simple unit
// active at exec time, before the process has bound its listeners.
func waitDial(addr string, d time.Duration) bool {
	return waitFor(d, func() bool { return dialOK(addr) })
}

// proxyRequest sends one absolute-form GET through the HTTP listener and
// returns the status code and body.
func proxyRequest(t *testing.T, proxyAddr, target string) (int, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial HTTP listener: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: sd-acceptance.rl.test\r\nConnection: close\r\n\r\n", target)
	raw, _ := io.ReadAll(conn)
	head, body, _ := strings.Cut(string(raw), "\r\n\r\n")
	code := 0
	fmt.Sscanf(head, "HTTP/1.1 %d", &code)
	return code, body
}
