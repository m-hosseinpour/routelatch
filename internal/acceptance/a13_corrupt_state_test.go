package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestA13SystemdCorruptState exercises docs/contracts.md §4.5 under a real
// `systemd --user` manager: a state file that is truncated JSON, from a newer
// schema, or unreadable must fail closed with the documented exit status, stay
// byte-identical, bind no traffic listener, never fall back to direct, and
// leave the explicit operator recovery (restore from a valid backup, or chmod
// for an unreadable file) working. It is gated like
// TestA13SystemdUserLifecycle because it installs a temporary unit into the
// caller's real user-manager unit directory; the data dir, ports and backup
// stay isolated and the unit is removed afterwards.
func TestA13SystemdCorruptState(t *testing.T) {
	if os.Getenv("ROUTELATCH_SYSTEMD_TEST") != "1" {
		t.Skip("set ROUTELATCH_SYSTEMD_TEST=1 to exercise corrupt state under a real systemd --user unit (installs a temporary unit)")
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
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod data dir: %v", err)
	}
	httpPort, socksPort := freePort(t), freePort(t)
	for socksPort == httpPort {
		socksPort = freePort(t)
	}
	writeConfig(t, dir, serviceConfig{
		HTTPListen:  fmt.Sprintf("127.0.0.1:%d", httpPort),
		SOCKSListen: fmt.Sprintf("127.0.0.1:%d", socksPort),
	})
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	statePath := filepath.Join(dir, "state.json")
	backupPath := ""

	unit := fmt.Sprintf("routelatch-acceptance-corrupt-%d.service", os.Getpid())
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
		state := strings.TrimSpace(activeOut)
		keep := state == "active" || state == "activating" || state == "reloading" || state == "deactivating"
		if keep {
			t.Logf("temporary unit %s is still %s after cleanup; left %s in place", unit, state, unitPath)
		} else {
			_ = os.Remove(unitPath)
		}
		_, _ = sdRun(t, systemctl, "daemon-reload")
		if createdUnitDir {
			_ = os.Remove(unitDir)
		}
		if _, err := os.Stat(unitPath); err == nil && !keep {
			t.Logf("temporary unit %s still exists after cleanup", unitPath)
		}
	})
	if _, err := sdRun(t, systemctl, "daemon-reload"); err != nil {
		t.Fatalf("daemon-reload: %v", err)
	}

	// Baseline: this unit and its isolated ports work before the state is
	// damaged, so a later failure cannot be attributed to the unit setup.
	baselinePID := sdStart(t, systemctl, unit, dir)
	if waitDial(httpAddr, 15*time.Second) && waitDial(socksAddr, 15*time.Second) {
		r.pass("A13", "corrupt-state cell baseline: the temporary unit serves both isolated loopback listeners (main pid %d)", baselinePID)
	} else {
		r.fail("A13", "corrupt-state cell baseline: listeners http=%v socks=%v", dialOK(httpAddr), dialOK(socksAddr))
	}
	stopUnit := func(t *testing.T) {
		t.Helper()
		if _, err := sdRun(t, systemctl, "stop", unit); err != nil {
			t.Fatalf("stop: %v", err)
		}
		final := waitUnitStatus(t, systemctl, unit, func(u unitStatus) bool { return u.ActiveState == "inactive" }, 20*time.Second)
		if final.ExecMainStatus == "0" && !dialOK(httpAddr) && !dialOK(socksAddr) {
			r.pass("A13", "the unit stops cleanly (ExecMainStatus=0) and closes both listeners before the next corruption case")
		} else {
			r.fail("A13", "pre-case stop: state=%s/%s exit=%s http=%v socks=%v", final.ActiveState, final.SubState, final.ExecMainStatus, dialOK(httpAddr), dialOK(socksAddr))
		}
	}
	// Seed a real rule through the running service, then stop it cleanly and
	// take the backup offline: restore in §4.5 is an offline operation against
	// the same data dir.
	if res := cliAt(t, dir, "rules", "add", "--kind", "domain", "sd-corrupt.rl.test"); res.code != 0 || !res.env.OK {
		t.Fatalf("seed state: exit=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
	}
	stopUnit(t)

	backup := cliAt(t, dir, "backup")
	ref, _ := backup.mapData(t)["ref"].(map[string]any)
	backupPath = str(ref["path"])
	if backup.code != 0 || !backup.env.OK || backupPath == "" {
		t.Fatalf("create recovery backup: exit=%d path=%q stdout=%q stderr=%s", backup.code, backupPath, backup.stdout, backup.stderr)
	}
	r.pass("A13", "an offline backup of the healthy state exists at %s and is the documented recovery source", filepath.Base(backupPath))

	// expectFailClosed starts the unit and asserts the documented fail-closed
	// shape: state_corrupt maps to exit 10 (the mode-000 case is the
	// unreadable-state path, not the too-permissive 9 path), and wantLog is a
	// fragment of the operator-visible reason.
	expectFailClosed := func(t *testing.T, label, wantLog string) {
		t.Helper()
		_, _ = sdRun(t, systemctl, "reset-failed", unit)
		startOut, startErr := sdRun(t, systemctl, "start", unit)
		status := waitUnitStatus(t, systemctl, unit, func(u unitStatus) bool {
			return u.ActiveState == "failed"
		}, 20*time.Second)
		switch {
		case status.ActiveState != "failed":
			r.fail("A13", "%s: unit is %s/%s after start, want failed (start: %v %s)", label, status.ActiveState, status.SubState, startErr, strings.TrimSpace(startOut))
		case status.ExecMainStatus != "10":
			r.fail("A13", "%s: ExecMainStatus=%s, want 10", label, status.ExecMainStatus)
		case status.NRestarts != "0":
			r.fail("A13", "%s: NRestarts=%s; systemd restarted a state that cannot be fixed by restarting", label, status.NRestarts)
		default:
			r.pass("A13", "%s: the unit fails closed with ExecMainStatus=%s and NRestarts=0", label, status.ExecMainStatus)
		}
		if status.MainPID != "0" {
			r.fail("A13", "%s: a main pid %s survived the failed start", label, status.MainPID)
		}
		if dialOK(httpAddr) || dialOK(socksAddr) {
			r.fail("A13", "%s: a listener accepts on http=%s socks=%s after the failed start", label, httpAddr, socksAddr)
		} else {
			r.pass("A13", "%s: neither traffic listener remains bound after the failed start, so no request can be routed (no silent direct fallback)", label)
		}
		if status := cliAt(t, dir, "status"); status.code != 3 || status.env.OK {
			r.fail("A13", "%s: offline `status` exits %d ok=%v; want exit 3 with no service claiming success", label, status.code, status.env.OK)
		} else {
			r.pass("A13", "%s: no management service claims success (offline `status` exit 3)", label)
		}
		var statusOut string
		foundReason := waitFor(3*time.Second, func() bool {
			statusOut, _ = sdRun(t, systemctl, "status", unit, "--no-pager", "-n", "30")
			return strings.Contains(statusOut, wantLog)
		})
		if foundReason {
			r.pass("A13", "%s: systemctl status shows the documented reason (%q)", label, wantLog)
		} else {
			r.fail("A13", "%s: systemctl status does not show %q: %q", label, wantLog, statusOut)
		}
	}
	// doctorReportsCorrupt proves the offline diagnostic reports state_corrupt
	// with exit 10 instead of hiding it behind "not running".
	doctorReportsCorrupt := func(t *testing.T, label string) {
		t.Helper()
		doctor := cliAt(t, dir, "doctor")
		if doctor.code != 10 || doctor.env.Error == nil || doctor.env.Error.Kind != "state_corrupt" {
			r.fail("A13", "%s: offline doctor exit=%d kind=%v, want exit 10 state_corrupt", label, doctor.code, errorKind(doctor))
			return
		}
		r.pass("A13", "%s: offline doctor reports state_corrupt with exit 10 (no silent repair)", label)
	}
	// restoresAndServes runs the documented recovery and proves the unit is
	// healthy again on the same data dir and ports.
	restoresAndServes := func(t *testing.T, label string) {
		t.Helper()
		restored := cliAt(t, dir, "restore", backupPath)
		if restored.code != 0 || !restored.env.OK {
			r.fail("A13", "%s: explicit restore: exit=%d stdout=%q stderr=%s", label, restored.code, restored.stdout, restored.stderr)
			return
		}
		r.pass("A13", "%s: the documented explicit restore from a valid backup exits 0", label)
		_, _ = sdRun(t, systemctl, "reset-failed", unit)
		pid := sdStart(t, systemctl, unit, dir)
		if !waitDial(httpAddr, 20*time.Second) || !waitDial(socksAddr, 20*time.Second) {
			r.fail("A13", "%s: listeners after restore http=%v socks=%v", label, dialOK(httpAddr), dialOK(socksAddr))
			return
		}
		rules := cliAt(t, dir, "rules", "list")
		found := false
		for _, rule := range rules.list(t, "rules") {
			if str(rule["host"]) == "sd-corrupt.rl.test" {
				found = true
			}
		}
		if pid != 0 && found {
			r.pass("A13", "%s: after restore the unit starts (main pid %d), serves both listeners and keeps the backed-up rule", label, pid)
		} else {
			r.fail("A13", "%s: post-restore pid=%d rule-present=%v", label, pid, found)
		}
		stopUnit(t)
	}

	// 1. Truncated JSON: fail closed, file untouched, no listener, restore.
	t.Run("truncated json", func(t *testing.T) {
		before := []byte(`{"schema_version":1,"mode":`)
		if err := os.WriteFile(statePath, before, 0o600); err != nil {
			t.Fatalf("write truncated state: %v", err)
		}
		expectFailClosed(t, "truncated JSON state", "not valid JSON")
		doctorReportsCorrupt(t, "truncated JSON state")
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("re-read truncated state: %v", err)
		}
		info, statErr := os.Stat(statePath)
		unchanged := bytes.Equal(before, after) && statErr == nil && info.Mode().Perm() == 0o600
		if unchanged {
			r.pass("A13", "truncated JSON state: the service and doctor left all %d bytes byte-identical at mode 0600 (no rewrite, no restore from state.json.bak)", len(after))
		} else {
			r.fail("A13", "truncated JSON state: bytes-identical=%v stat-err=%v", bytes.Equal(before, after), statErr)
		}
		restoresAndServes(t, "truncated JSON state")
	})

	// 2. Valid JSON from a newer schema: no downgrade, no structural guess.
	t.Run("newer schema", func(t *testing.T) {
		before := []byte(`{"schema_version":99,"mode":"targeted","policy_version":1}`)
		if err := os.WriteFile(statePath, before, 0o600); err != nil {
			t.Fatalf("write newer state: %v", err)
		}
		expectFailClosed(t, "newer-schema state", "newer RouteLatch")
		doctorReportsCorrupt(t, "newer-schema state")
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("re-read newer state: %v", err)
		}
		info, statErr := os.Stat(statePath)
		if bytes.Equal(before, after) && statErr == nil && info.Mode().Perm() == 0o600 {
			r.pass("A13", "newer-schema state: the service left the file byte-identical at mode 0600 (refused to downgrade or guess the structure)")
		} else {
			r.fail("A13", "newer-schema state was rewritten or its mode changed: bytes=%q stat-err=%v", after, statErr)
		}
		restoresAndServes(t, "newer-schema state")
	})

	// 3. Unreadable file (mode 000): fail closed without changing mode; the
	// documented recovery is the explicit chmod, after which the same file
	// serves again with no restore and no data loss.
	t.Run("unreadable state", func(t *testing.T) {
		before, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read valid state before chmod: %v", err)
		}
		if err := os.Chmod(statePath, 0o000); err != nil {
			t.Fatalf("chmod 000: %v", err)
		}
		expectFailClosed(t, "unreadable state (mode 000)", "cannot be read")
		doctorReportsCorrupt(t, "unreadable state (mode 000)")
		info, statErr := os.Stat(statePath)
		if statErr == nil && info.Mode().Perm() == 0o000 {
			r.pass("A13", "unreadable state: the service did not loosen or change the file mode (still 000)")
		} else {
			r.fail("A13", "unreadable state: mode changed or stat failed: %v", statErr)
		}
		// Documented operator recovery for a permission failure (§4.1): an
		// explicit chmod, never an automatic one.
		if err := os.Chmod(statePath, 0o600); err != nil {
			t.Fatalf("operator chmod 600: %v", err)
		}
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("re-read state after chmod: %v", err)
		}
		if bytes.Equal(before, after) {
			r.pass("A13", "unreadable state: after the explicit chmod the file is byte-identical to before the corruption (no wipe)")
		} else {
			r.fail("A13", "unreadable state bytes changed while the service refused to read it")
		}
		if doctor := cliAt(t, dir, "doctor"); doctor.code != 0 || !doctor.env.OK {
			r.fail("A13", "offline doctor after chmod: exit=%d ok=%v", doctor.code, doctor.env.OK)
		} else {
			r.pass("A13", "offline doctor after the explicit chmod exits 0")
		}
		_, _ = sdRun(t, systemctl, "reset-failed", unit)
		pid := sdStart(t, systemctl, unit, dir)
		if waitDial(httpAddr, 20*time.Second) && waitDial(socksAddr, 20*time.Second) {
			r.pass("A13", "unreadable state: after the explicit chmod the unit starts (main pid %d) and serves both listeners with the original state", pid)
		} else {
			r.fail("A13", "unreadable state after chmod: listeners http=%v socks=%v", dialOK(httpAddr), dialOK(socksAddr))
		}
		stopUnit(t)
	})
}
