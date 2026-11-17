package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA05D21Canary is the end-to-end D21 check: a unique unapproved
// destination is visited, diagnosed automatically, shown live, rejected,
// rechecked, carried across a mode switch and a restart, and must never reach a
// file, the service log or a persistence surface. The second half approves its
// own canary and requires the only file occurrence to be the approved rule set.
func TestA05D21Canary(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	root := filepath.Dir(env.home) // every temp HOME/XDG/data path lives here
	dir := filepath.Join(env.home, "d21")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{
		Diagnostics: map[string]any{"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30},
	})
	t.Cleanup(func() { _ = svc.stop() })

	canary := "rl-canary-" + randomHex(t, 10) + ".invalid"
	canaryAddr := fmt.Sprintf("%s:%d", canary, freePort(t))

	// 1. A failed direct request to an unapproved destination. The proxy must
	// answer an error (no crash) and the diagnostics hook must probe it
	// automatically; nothing is approved for this destination.
	conn, _, code, err := connectHTTP(t, svc.httpAddr, canaryAddr)
	if conn != nil {
		_ = conn.Close()
	}
	if err != nil || !isDirectError(code) {
		t.Fatalf("failed direct request: code=%d err=%v", code, err)
	}
	r.pass("A05", "an unapproved canary destination fails direct (HTTP %d) without a rule or pool exit", code)

	var proposal map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for proposal == nil && time.Now().Before(deadline) {
		for _, item := range svc.mustOK("proposals", "list").list(t, "proposals") {
			if dest, ok := item["destination"].(map[string]any); ok && str(dest["host"]) == canary {
				proposal = item
				break
			}
		}
		if proposal == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if proposal == nil {
		t.Fatalf("the automatic diagnostic probe never produced a proposal for %s (D21 requires it in memory after the failed request)", canary)
	}
	// The live view is the one permitted display of the destination.
	if str(proposal["status"]) != "pending" {
		r.fail("A05", "the automatic proposal is visible but status=%q, want pending", str(proposal["status"]))
	} else {
		r.pass("A05", "the automatic diagnostic probe produced a pending in-memory proposal that proposals list shows live")
	}
	proposalID := str(proposal["id"])

	// Sensitive replies that are not the live proposal view must not echo it.
	sensitive := []struct {
		name string
		res  cliResult
	}{
		{"status", svc.mustOK("status")},
		{"doctor", svc.mustOK("doctor")},
		{"pool list", svc.mustOK("pool", "list")},
		{"config get", svc.mustOK("config", "get")},
		{"env", svc.mustOK("env")},
		{"rules list", svc.mustOK("rules", "list")},
	}
	for _, s := range sensitive {
		if strings.Contains(s.res.stdout+s.res.stderr, canary) {
			r.fail("A05", "%s echoed the unapproved canary destination", s.name)
		}
	}

	// 2. Reject and recheck: the decision must not create a rule or persist the
	// destination; recheck may be refused by the prober cooldown (exit 6).
	svc.mustOK("proposals", "reject", proposalID)
	if status := proposalStatus(t, svc, proposalID); status != "rejected" {
		r.fail("A05", "rejected proposal status=%q", status)
	}
	recheck := svc.cli("proposals", "recheck", proposalID)
	if recheck.code != 0 && recheck.code != 6 {
		r.fail("A05", "proposals recheck: exit=%d kind=%q, want 0 (rechecked) or 6 (cooldown)", recheck.code, errorKind(recheck))
	} else if status := proposalStatus(t, svc, proposalID); status != "rejected" {
		r.fail("A05", "recheck changed the rejected proposal status to %q", status)
	} else if rulesNameDestination(svc.mustOK("rules", "list").list(t, "rules"), canary) {
		r.fail("A05", "reject/recheck created an approved rule for the canary")
	} else {
		r.pass("A05", "reject leaves the proposal rejected (recheck exit %d) and creates no rule", recheck.code)
	}

	// 3. A mode switch rewrites state, and a backup copies the state tree; the
	// canary must not enter either.
	svc.mustOK("mode", "all-direct")
	svc.mustOK("mode", "targeted")
	backup := svc.mustOK("backup").mapData(t)
	ref, _ := backup["ref"].(map[string]any)
	backupPath := str(ref["path"])
	if backupPath == "" {
		t.Fatalf("backup returned no path: %+v", backup)
	}
	r.pass("A05", "a mode switch and a backup ran with the canary only in memory (backup ref %s)", filepath.Base(backupPath))

	// 4. Restart on the same data dir: proposals are memory-only and must not
	// be resurrected from any file.
	if err := svc.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	restarted := startService(t, env, dir, svc.cfg)
	t.Cleanup(func() { _ = restarted.stop() })
	after := restarted.mustOK("proposals", "list").list(t, "proposals")
	if len(after) != 0 {
		r.fail("A05", "restart resurrected %d proposal(s) from disk; proposals are memory-only (D21)", len(after))
	} else {
		r.pass("A05", "after a restart proposals list is empty: the automatic proposal existed only in process memory (D21)")
	}
	if rulesNameDestination(restarted.mustOK("rules", "list").list(t, "rules"), canary) {
		r.fail("A05", "the restart produced an approved rule for the rejected canary")
	}

	// 5. No file under the temp HOME/XDG tree may contain the canary: state,
	// config, backups, the log and any rotation, locks and the socket dir.
	if hits := filesContaining(t, root, canary); len(hits) != 0 {
		r.fail("A05", "the unapproved canary destination reached %d file(s): %s", len(hits), strings.Join(hits, ", "))
	} else {
		r.pass("A05", "the unapproved canary appears in no file under the temp HOME/XDG tree (state, config, backup, log/rotations, locks)")
	}
	if strings.Contains(svc.Stdout()+svc.Stderr(), canary) || strings.Contains(restarted.Stdout()+restarted.Stderr(), canary) {
		r.fail("A05", "the canary appears in the service's captured stdout/stderr")
	} else {
		r.pass("A05", "the canary appears in no captured service stdout/stderr (only the live proposals view names it)")
	}
	// The log file must have been exercised, so the scan above is not vacuous.
	logPath := filepath.Join(dir, "log", "routelatch.log")
	if info, err := os.Stat(logPath); err != nil || info.Size() == 0 {
		r.fail("A05", "the operational log is missing or empty, so the canary scan had no log to inspect: %v", err)
	}

	// 6. Approval branch: a second canary is approved by the user; the only file
	// occurrence must then be the approved rule set, never a log, config,
	// backup or lock.
	dir2 := filepath.Join(env.home, "d21-approved")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatalf("second data dir: %v", err)
	}
	svc2 := startService(t, env, dir2, serviceConfig{
		Diagnostics: map[string]any{"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30},
	})
	t.Cleanup(func() { _ = svc2.stop() })
	approved := "rl-canary-" + randomHex(t, 10) + ".invalid"
	approvedAddr := fmt.Sprintf("%s:%d", approved, freePort(t))
	conn2, _, code2, err2 := connectHTTP(t, svc2.httpAddr, approvedAddr)
	if conn2 != nil {
		_ = conn2.Close()
	}
	if err2 != nil || !isDirectError(code2) {
		t.Fatalf("approved-branch direct request: code=%d err=%v", code2, err2)
	}
	var approvedProposal map[string]any
	deadline = time.Now().Add(20 * time.Second)
	for approvedProposal == nil && time.Now().Before(deadline) {
		for _, item := range svc2.mustOK("proposals", "list").list(t, "proposals") {
			if dest, ok := item["destination"].(map[string]any); ok && str(dest["host"]) == approved {
				approvedProposal = item
				break
			}
		}
		if approvedProposal == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if approvedProposal == nil {
		t.Fatalf("the second canary produced no proposal to approve")
	}
	accept := svc2.mustOK("proposals", "accept", str(approvedProposal["id"]))
	if str(accept.mapData(t)["rule_id"]) == "" || !rulesNameDestination(svc2.mustOK("rules", "list").list(t, "rules"), approved) {
		r.fail("A05", "approved canary is not present in the approved rules after accept")
	} else {
		r.pass("A05", "an approved canary appears in the user's approved rules (rule %s)", str(accept.mapData(t)["rule_id"]))
	}
	allowed := map[string]bool{
		filepath.Join(dir2, "state.json"):     true,
		filepath.Join(dir2, "state.json.bak"): true,
	}
	hits := filesContaining(t, root, approved)
	var outside []string
	for _, path := range hits {
		if !allowed[path] {
			outside = append(outside, path)
		}
	}
	if len(outside) != 0 {
		r.fail("A05", "the approved canary also reached %s; only the approved rule set may hold it", strings.Join(outside, ", "))
	} else if len(hits) == 0 {
		r.fail("A05", "the approved canary is in rules list but in no state file; the rule was not persisted")
	} else {
		r.pass("A05", "the approved canary appears only in the approved rule set (%s), not in the log, config, backups or locks", strings.Join(hits, ", "))
	}
	if strings.Contains(svc2.Stdout()+svc2.Stderr(), approved) {
		r.fail("A05", "the approved canary appears in the service's captured stdout/stderr")
	} else {
		r.pass("A05", "the approved canary appears in no captured service stdout/stderr")
	}
	for _, s := range []struct {
		name string
		res  cliResult
	}{
		{"status", svc2.mustOK("status")},
		{"doctor", svc2.mustOK("doctor")},
		{"pool list", svc2.mustOK("pool", "list")},
		{"config get", svc2.mustOK("config", "get")},
	} {
		if strings.Contains(s.res.stdout+s.res.stderr, approved) {
			r.fail("A05", "%s echoed the approved destination, which belongs only in rules list", s.name)
		}
	}
}

// rulesNameDestination reports whether the rule set names a domain or address.
func rulesNameDestination(rules []map[string]any, name string) bool {
	for _, rule := range rules {
		if str(rule["host"]) == name || str(rule["addr"]) == name {
			return true
		}
	}
	return false
}

// filesContaining returns every regular file under root whose bytes contain
// needle. It is the D21 scan: state, config, backups, the log and its
// rotations, locks and every other persisted byte.
func filesContaining(t *testing.T, root, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	return hits
}
