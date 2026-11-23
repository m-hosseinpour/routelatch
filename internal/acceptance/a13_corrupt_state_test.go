package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestA13CorruptStateRecovery exercises docs/contracts.md §4.5 with the
// supported foreground mode (`run --headless`), not a packaged systemd unit:
// a state file that is truncated JSON, from a newer schema, or unreadable must
// fail closed with the documented exit status, stay byte-identical, bind no
// traffic listener, never fall back to direct, and leave the explicit operator
// recovery (restore from a valid backup, or chmod for an unreadable file)
// working. Data dir, ports and backup are isolated; no service manager is
// touched.
func TestA13CorruptStateRecovery(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "corrupt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	httpPort, socksPort := freePort(t), freePort(t)
	cfg := serviceConfig{
		HTTPListen:  fmt.Sprintf("127.0.0.1:%d", httpPort),
		SOCKSListen: fmt.Sprintf("127.0.0.1:%d", socksPort),
	}
	httpAddr := cfg.HTTPListen
	socksAddr := cfg.SOCKSListen
	statePath := filepath.Join(dir, "state.json")
	backupPath := ""

	// Baseline: this data dir and its isolated ports work before the state is
	// damaged, so a later failure cannot be attributed to setup.
	svc := startService(t, env, dir, cfg)
	t.Cleanup(func() { _ = svc.stop() })
	if !listenerRefuses(t, httpAddr) && !listenerRefuses(t, socksAddr) {
		r.pass("A13", "corrupt-state cell baseline: the headless process serves both isolated loopback listeners")
	} else {
		r.fail("A13", "corrupt-state cell baseline: listeners http=%v socks=%v", !listenerRefuses(t, httpAddr), !listenerRefuses(t, socksAddr))
	}
	// Seed a real rule through the running service, then stop it cleanly and
	// take the backup offline: restore in §4.5 is an offline operation against
	// the same data dir.
	if res := svc.cli("rules", "add", "--kind", "domain", "sd-corrupt.rl.test"); res.code != 0 || !res.env.OK {
		t.Fatalf("seed state: exit=%d stdout=%q stderr=%s", res.code, res.stdout, res.stderr)
	}
	if err := svc.stop(); err != nil {
		r.fail("A13", "the headless process did not stop cleanly before the backup: %v", err)
	} else {
		r.pass("A13", "the headless process stops cleanly (SIGINT) and closes both listeners before the backup")
	}
	if !listenerRefuses(t, httpAddr) || !listenerRefuses(t, socksAddr) {
		r.fail("A13", "a listener still accepts after the clean stop")
	}

	backup := svc.cli("backup")
	ref, _ := backup.mapData(t)["ref"].(map[string]any)
	backupPath = str(ref["path"])
	if backup.code != 0 || !backup.env.OK || backupPath == "" {
		t.Fatalf("create recovery backup: exit=%d path=%q stdout=%q stderr=%s", backup.code, backupPath, backup.stdout, backup.stderr)
	}
	r.pass("A13", "an offline backup of the healthy state exists at %s and is the documented recovery source", filepath.Base(backupPath))

	// expectFailClosed starts the foreground engine and asserts the documented
	// fail-closed shape: state_corrupt maps to exit 10 and wantLog is a
	// fragment of the operator-visible reason.
	expectFailClosed := func(t *testing.T, label, wantLog string) {
		t.Helper()
		code, out := startExpectingFailure(t, env, dir)
		if code != 10 {
			r.fail("A13", "%s: run exited %d, want 10; output=%s", label, code, out)
		} else {
			r.pass("A13", "%s: run --headless fails closed with exit 10", label)
		}
		if !strings.Contains(out, wantLog) {
			r.fail("A13", "%s: output does not contain %q: %s", label, wantLog, out)
		} else {
			r.pass("A13", "%s: the operator-visible reason contains %q", label, wantLog)
		}
		if !listenerRefuses(t, httpAddr) || !listenerRefuses(t, socksAddr) {
			r.fail("A13", "%s: a listener accepts on http=%s socks=%s after the failed start", label, httpAddr, socksAddr)
		} else {
			r.pass("A13", "%s: neither traffic listener remains bound after the failed start, so no request can be routed (no silent direct fallback)", label)
		}
		if status := svc.cli("status"); status.code != 3 || status.env.OK {
			r.fail("A13", "%s: offline `status` exits %d ok=%v; want exit 3 with no service claiming success", label, status.code, status.env.OK)
		} else {
			r.pass("A13", "%s: no management service claims success (offline `status` exit 3)", label)
		}
	}
	// doctorReportsCorrupt proves the offline diagnostic reports state_corrupt
	// with exit 10 instead of hiding it behind "not running".
	doctorReportsCorrupt := func(t *testing.T, label string) {
		t.Helper()
		doctor := svc.cli("doctor")
		if doctor.code != 10 || doctor.env.Error == nil || doctor.env.Error.Kind != "state_corrupt" {
			r.fail("A13", "%s: offline doctor exit=%d kind=%v, want exit 10 state_corrupt", label, doctor.code, errorKind(doctor))
			return
		}
		r.pass("A13", "%s: offline doctor reports state_corrupt with exit 10 (no silent repair)", label)
	}
	// restoresAndServes runs the documented recovery and proves the foreground
	// process is healthy again on the same data dir and ports.
	restoresAndServes := func(t *testing.T, label string) {
		t.Helper()
		restored := svc.cli("restore", backupPath)
		if restored.code != 0 || !restored.env.OK {
			r.fail("A13", "%s: explicit restore: exit=%d stdout=%q stderr=%s", label, restored.code, restored.stdout, restored.stderr)
			return
		}
		r.pass("A13", "%s: the documented explicit restore from a valid backup exits 0", label)
		recovered := startService(t, env, dir, cfg)
		t.Cleanup(func() { _ = recovered.stop() })
		if listenerRefuses(t, httpAddr) || listenerRefuses(t, socksAddr) {
			r.fail("A13", "%s: listeners after restore http=%v socks=%v", label, listenerRefuses(t, httpAddr), listenerRefuses(t, socksAddr))
			return
		}
		rules := recovered.cli("rules", "list")
		found := false
		for _, rule := range rules.list(t, "rules") {
			if str(rule["host"]) == "sd-corrupt.rl.test" {
				found = true
			}
		}
		if found {
			r.pass("A13", "%s: after restore the process serves both listeners and keeps the backed-up rule", label)
		} else {
			r.fail("A13", "%s: restored rule is missing", label)
		}
		if err := recovered.stop(); err != nil {
			r.fail("A13", "%s: post-restore stop: %v", label, err)
		}
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
			r.pass("A13", "truncated JSON state: the process and doctor left all %d bytes byte-identical at mode 0600 (no rewrite, no restore from state.json.bak)", len(after))
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
			r.pass("A13", "newer-schema state: the process left the file byte-identical at mode 0600 (refused to downgrade or guess the structure)")
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
			r.pass("A13", "unreadable state: the process did not loosen or change the file mode (still 000)")
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
			r.fail("A13", "unreadable state bytes changed while the process refused to read it")
		}
		if doctor := svc.cli("doctor"); doctor.code != 0 || !doctor.env.OK {
			r.fail("A13", "offline doctor after chmod: exit=%d ok=%v", doctor.code, doctor.env.OK)
		} else {
			r.pass("A13", "offline doctor after the explicit chmod exits 0")
		}
		recovered := startService(t, env, dir, cfg)
		t.Cleanup(func() { _ = recovered.stop() })
		if listenerRefuses(t, httpAddr) || listenerRefuses(t, socksAddr) {
			r.fail("A13", "unreadable state after chmod: listeners http=%v socks=%v", listenerRefuses(t, httpAddr), listenerRefuses(t, socksAddr))
		} else {
			r.pass("A13", "unreadable state: after the explicit chmod the process serves both listeners with the original state")
		}
		if err := recovered.stop(); err != nil {
			r.fail("A13", "unreadable state stop: %v", err)
		}
	})
}
