package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA05WireNoReplay checks the wire-level property behind A05: one client
// request is forwarded exactly once, a failed request leaves no bytes anywhere,
// and accepting a proposal moves the rule set by exactly one rule.
func TestA05WireNoReplay(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "wire")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	origin := newRecordingOrigin(t)
	svc := startService(t, env, dir, serviceConfig{
		Diagnostics: map[string]any{"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30},
		Probes:      map[string]any{"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })

	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("pool", "select", str(findRecord(t, svc.mustOK("pool", "list"), "socks5")["id"]))
	svc.mustOK("rules", "add", "--kind", "domain", "origin.rl.test")

	nonce := "wire-" + randomHex(t, 10)
	raw := fmt.Sprintf("GET http://origin.rl.test:%d/echo HTTP/1.1\r\nHost: origin.rl.test:%d\r\nX-RL-Nonce: %s\r\nConnection: close\r\n\r\n",
		origin.port(), origin.port(), nonce)
	resp, body, err := httpForwardMethod(t, svc.httpAddr, raw, "GET", 15*time.Second)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("forward through the pool: resp=%v err=%v", resp, err)
	}
	if string(body) != "echo:GET:" {
		r.fail("A05", "one forwarded request reached the origin with body %q, want echo:GET:", body)
	} else if origin.count() != 1 {
		r.fail("A05", "the origin saw %d requests for one client request", origin.count())
	} else {
		r.pass("A05", "one client request is forwarded to the origin exactly once (origin saw 1 request, same method/path)")
	}

	// An eligible direct failure with the *same* nonce in path, query and a
	// header must not reach the origin or appear in any evidence/output.
	before := origin.count()
	exitDials := fx.socks.dialCount()
	bad := fmt.Sprintf("GET http://[2001:db8::77]:%d/fail-%s?q=%s HTTP/1.1\r\nHost: [2001:db8::77]:%d\r\nCookie: rl=%s\r\nX-Trace: %s\r\nConnection: close\r\n\r\n",
		origin.port(), nonce, nonce, origin.port(), nonce, nonce)
	badResp, _, _ := httpForwardMethod(t, svc.httpAddr, bad, "GET", 15*time.Second)
	if badResp != nil && isDirectError(badResp.StatusCode) {
		r.pass("A05", "the failed direct request is a pre-reply error (HTTP %d), not a retry", badResp.StatusCode)
	} else {
		r.fail("A05", "failed request status=%v", badResp)
	}
	time.Sleep(300 * time.Millisecond)
	if origin.count() != before {
		r.fail("A05", "a request reached the origin during the failed request/diagnosis (%d new)", origin.count()-before)
	} else {
		r.pass("A05", "the origin listener saw no request at all during the failed request and its diagnosis (count still %d)", origin.count())
	}
	if fx.socks.dialCount() != exitDials {
		r.fail("A05", "the failed direct request or its diagnosis added an exit dial (%d→%d)", exitDials, fx.socks.dialCount())
	} else {
		r.pass("A05", "the failed direct request and its diagnosis added no exit dial (pool untouched)")
	}
	for _, req := range origin.requests() {
		if strings.Contains(req.Path, nonce) || strings.Contains(req.Body, nonce) {
			r.fail("A05", "the origin received the failed request's material")
		}
	}
	proposals := svc.mustOK("proposals", "list")
	stateRaw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if !strings.Contains(proposals.stdout, nonce) && !strings.Contains(string(stateRaw), nonce) &&
		!outputsContain(svc, nonce) {
		r.pass("A05", "evidence, state, CLI output and the service log contain none of the failed request's bytes")
	} else {
		r.fail("A05", "the failed request's material appeared in evidence/state/output")
	}
	if hasRuleAddr(svc.mustOK("rules", "list").list(t, "rules"), "2001:db8::77") {
		r.fail("A05", "a rule was created without approval")
	}
	r.note("A05", "an eligible direct failure is pre-reply by definition, so no origin can receive that request: wire-level 'no replay' is not observable here. Proven: exactly one forward on a successful request, no request material in evidence/state/output, and no pool dial from the failure or its diagnosis")

	// Accept moves the rule set by exactly one; the accepted rule names exactly
	// the proposed destination.
	diag := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::78]:%d", origin.port())).mapData(t)
	pid := str(diag["proposal_id"])
	beforeRules := len(svc.mustOK("rules", "list").list(t, "rules"))
	accept := svc.mustOK("proposals", "accept", pid)
	afterRules := svc.mustOK("rules", "list").list(t, "rules")
	if eligible, _ := diag["eligible"].(bool); eligible && pid != "" &&
		len(afterRules) == beforeRules+1 && hasRuleAddr(afterRules, "2001:db8::78") && str(accept.mapData(t)["rule_id"]) != "" {
		r.pass("A05", "accepting a proposal adds exactly one rule, for exactly the proposed destination (%d → %d)", beforeRules, len(afterRules))
	} else {
		r.fail("A05", "accept: eligible=%v before=%d after=%d rule=%q", diag["eligible"], beforeRules, len(afterRules), str(accept.mapData(t)["rule_id"]))
	}
}

// TestA05RuleCountStable checks that reject and recheck never change the rule
// set. It is separate from the cooldown-heavy flow so the count is asserted
// directly around each decision.
func TestA05RuleCountStable(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	svc := startService(t, env, dir, serviceConfig{
		Diagnostics: map[string]any{"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30},
		Probes:      map[string]any{"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", fx.filePath)

	diag := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::81]:%d", freePort(t))).mapData(t)
	id := str(diag["proposal_id"])
	before := len(svc.mustOK("rules", "list").list(t, "rules"))
	svc.mustOK("proposals", "reject", id)
	afterReject := len(svc.mustOK("rules", "list").list(t, "rules"))
	recheck := svc.cli("proposals", "recheck", id)
	afterRecheck := len(svc.mustOK("rules", "list").list(t, "rules"))
	status := proposalStatus(t, svc, id)
	if afterReject == before && afterRecheck == before && status == "rejected" && (recheck.code == 0 || recheck.code == 6) {
		r.pass("A05", "reject leaves the rule set unchanged (%d); recheck (%d) does not revive it (status %s, rules %d)", before, recheck.code, status, afterRecheck)
	} else {
		r.fail("A05", "rule count: before=%d after-reject=%d after-recheck=%d status=%q recheck-exit=%d",
			before, afterReject, afterRecheck, status, recheck.code)
	}
}
