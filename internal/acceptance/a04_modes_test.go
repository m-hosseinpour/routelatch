package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestA04PoolSweeps closes the §2.6 rows that were blocked while pool tunnels
// were unusable: a mode change and a rule removal must close live pool
// connections and report before success.
func TestA04PoolSweeps(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "sweeps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	dest := newEchoPeer(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")
	svc.mustOK("pool", "select", str(findRecord(t, svc.mustOK("pool", "list"), "socks5")["id"]))

	// A live pool tunnel is closed by targeted→all-direct, and the destination
	// observes the teardown.
	eofsBefore := dest.eofs()
	poolConn, poolBr, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dest.port()))
	if err != nil || code != 200 {
		t.Fatalf("live pool tunnel: code=%d err=%v", code, err)
	}
	if got, e := tunnelEcho(t, poolConn, poolBr, "before-direct"); e != nil || got != "before-direct" {
		t.Fatalf("live pool echo: got=%q err=%v", got, e)
	}
	mode := svc.mustOK("mode", "all-direct").mapData(t)
	closed, _ := mode["closed"].(map[string]any)
	if num(closed["count"]) >= 1 {
		r.pass("A04", "targeted→all-direct closes the live pool connection before success (closed=%v)", closed["count"])
	} else {
		r.fail("A04", "targeted→all-direct closed=%v with a live pool tunnel", closed)
	}
	_ = poolConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := poolConn.Read(make([]byte, 1)); err != nil {
		r.pass("A04", "the live pool tunnel's client side observes the mode-switch teardown")
	} else {
		r.fail("A04", "the live pool tunnel still delivered data after all-direct")
	}
	if waitFor(3*time.Second, func() bool { return dest.eofs() > eofsBefore }) {
		r.pass("A04", "the destination observes the live pool tunnel's teardown at the mode switch")
	} else {
		r.fail("A04", "the destination never observed the pool tunnel teardown")
	}
	_ = poolConn.Close()

	// Back to targeted, a new pool tunnel works, and removing its rule closes it.
	svc.mustOK("mode", "targeted")
	eofsBefore = dest.eofs()
	poolConn, poolBr, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dest.port()))
	if err != nil || code != 200 {
		t.Fatalf("second pool tunnel: code=%d err=%v", code, err)
	}
	if got, e := tunnelEcho(t, poolConn, poolBr, "before-remove"); e != nil || got != "before-remove" {
		t.Fatalf("second pool echo: got=%q err=%v", got, e)
	}
	var ruleID string
	for _, rule := range svc.mustOK("rules", "list").list(t, "rules") {
		if str(rule["host"]) == "exact.rl.test" {
			ruleID = str(rule["id"])
		}
	}
	if ruleID == "" {
		t.Fatalf("rule id not found")
	}
	removed := svc.mustOK("rules", "remove", ruleID).mapData(t)
	if num(removed["closed"]) >= 1 {
		r.pass("A04", "removing an approved rule closes the live pool connection to that destination (closed=%v)", removed["closed"])
	} else {
		r.fail("A04", "rules remove closed=%v with a live pool tunnel", removed["closed"])
	}
	_ = poolConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := poolConn.Read(make([]byte, 1)); err != nil {
		r.pass("A04", "the live pool tunnel's client side observes the rule-removal teardown")
	} else {
		r.fail("A04", "the live pool tunnel still delivered data after the rule was removed")
	}
	if waitFor(3*time.Second, func() bool { return dest.eofs() > eofsBefore }) {
		r.pass("A04", "the destination observes the live pool tunnel's teardown at rule removal")
	} else {
		r.fail("A04", "the destination never observed the rule-removal teardown")
	}
	_ = poolConn.Close()

	// After removal the destination is outside the list again: direct, no pool.
	dialsBefore := fx.socks.dialCount()
	unauthorized, _, unapprovedCode, _ := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dest.port()))
	if unauthorized != nil {
		_ = unauthorized.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if isDirectError(unapprovedCode) && fx.socks.dialCount() == dialsBefore {
		r.pass("A04", "after the rule is removed the destination is direct again (HTTP %d, 0 exit dials)", unapprovedCode)
	} else {
		r.fail("A04", "after rule removal: code=%d exit dials %d→%d", unapprovedCode, dialsBefore, fx.socks.dialCount())
	}
}
