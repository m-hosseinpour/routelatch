package acceptance

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA07PoolHealth proves the pool's health lifecycle on a loopback pool:
// cold-start health, cooling skip, last-good preservation across a failed
// refresh, engine identity across explicit probes/selection, and the all-cooling
// error.
func TestA07PoolHealth(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "pool")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	echo := newEchoPeer(t)
	greeter := newGreeterPeer(t, "g")

	// Two live SOCKS5 exits (one will become healthy, one stays unknown) and a
	// dead VLESS exit whose transport cannot connect. No manual selection is
	// made anywhere in this test.
	deadPort := deadVLESSPort(t)
	deadLink := fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=none&type=tcp#dead-exit", randomUUID(t), deadPort)
	secondSocks := newSOCKSExit(t)
	secondLink := fmt.Sprintf("socks5://127.0.0.1:%d#socks-second", secondSocks.port())
	poolFile := filepath.Join(t.TempDir(), "pool.txt")
	if err := os.WriteFile(poolFile, []byte(deadLink+"\n"+fx.links[2]+"\n"+secondLink+"\n"), 0o600); err != nil {
		t.Fatalf("write pool fixture: %v", err)
	}

	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000},
	})
	t.Cleanup(func() { _ = svc.stop() })
	pid := svc.proc.Process.Pid
	identity := procStartTime(t, pid)

	svc.mustOK("import", "file", poolFile)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")
	initial, _ := svc.statusNow()
	if initial.poolTotal != 3 || initial.poolUsable != 3 || initial.poolHealthy != 0 {
		r.fail("A07", "cold start pool=%d/%d/%d, want total 3, usable 3, healthy 0", initial.poolTotal, initial.poolUsable, initial.poolHealthy)
	} else {
		r.pass("A07", "cold start keeps every exit usable and none healthy yet (total 3, usable 3, healthy 0)")
	}

	// The service learns transport health on its first rotation tick (30s): the
	// dead exit cools, a successful endpoint probe never promotes to healthy
	// (§5.3).
	learned := waitFor(40*time.Second, func() bool {
		st, ok := svc.statusNow()
		return ok && st.poolCooling >= 1
	})
	if learned {
		st, _ := svc.statusNow()
		r.pass("A07", "the startup rotation cooled the dead exit and left the live exits usable (cooling=%d healthy=%d)", st.poolCooling, st.poolHealthy)
	} else {
		st, _ := svc.statusNow()
		r.fail("A07", "startup health was not learned within 40s: cooling=%d usable=%d", st.poolCooling, st.poolUsable)
	}

	var socksID, deadID string
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		switch {
		case str(row["protocol"]) == "socks5" && str(row["server"]) == "127.0.0.1" && num(row["port"]) == int(fx.socks.port()):
			socksID = str(row["id"])
		case str(row["protocol"]) == "vless":
			deadID = str(row["id"])
		}
	}
	if socksID == "" || deadID == "" {
		t.Fatalf("could not identify records: socks=%q dead=%q", socksID, deadID)
	}

	// Positive egress evidence promotes the probed exit to healthy (§5.3/§6.2);
	// the second live exit stays unknown.
	egress := svc.mustOK("pool", "test", "--exit", socksID, "--target", fmt.Sprintf("exact.rl.test:%d", greeter.port()))
	egressRows := egress.list(t, "results")
	if len(egressRows) == 1 && str(egressRows[0]["outcome"]) == "success" && num(egressRows[0]["bytes_down"]) >= 1 {
		r.pass("A07", "an egress probe produced positive byte evidence on the chosen exit (%d bytes down)", num(egressRows[0]["bytes_down"]))
	} else {
		r.fail("A07", "egress probe rows=%+v", egressRows)
	}
	st, _ := svc.statusNow()
	if st.poolHealthy == 1 && st.poolCooling == 1 && st.poolUsable == 2 {
		r.pass("A07", "health is now healthy=1 unknown=1 cooling=1 (usable 2)")
	} else {
		r.fail("A07", "health after the egress probe: healthy=%d cooling=%d usable=%d", st.poolHealthy, st.poolCooling, st.poolUsable)
	}

	// An explicit rerank reports the selection before/after, keeps the policy
	// version (contract §7.1), and chooses the healthy exit.
	policyBefore := st.policy
	rerank := svc.mustOK("pool", "rerank").mapData(t)
	skipped, skippedOK := rerank["skipped"].([]any)
	if str(rerank["selected_after"]) == socksID && skippedOK && num(rerank["policy_version"]) == policyBefore {
		r.pass("A07", "pool rerank selects the healthy exit (%s), reports %d skipped exit(s) and does not advance policy_version (%d)", socksID, len(skipped), policyBefore)
	} else {
		r.fail("A07", "rerank: selected_after=%q (want %q) skipped=%v policy=%v (want %d)",
			str(rerank["selected_after"]), socksID, rerank["skipped"], rerank["policy_version"], policyBefore)
	}

	// Without any manual selection, an approved destination uses the healthy
	// exit; the cooling exit is skipped and the unknown live exit is not chosen.
	dialsBefore := fx.socks.dialCount()
	secondBefore := secondSocks.dialCount()
	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", echo.port()))
	if err != nil || code != 200 {
		r.fail("A07", "approved destination selection: code=%d err=%v", code, err)
	} else if got, e := tunnelEcho(t, conn, br, "healthy"); e != nil || got != "healthy" {
		r.fail("A07", "tunnel through the selected exit: got=%q err=%v", got, e)
	} else if fx.socks.dialCount() != dialsBefore+1 || secondSocks.dialCount() != secondBefore || echo.contacts() < 1 {
		r.fail("A07", "selection: healthy dials %d→%d, unknown dials %d→%d, contacts=%d",
			dialsBefore, fx.socks.dialCount(), secondBefore, secondSocks.dialCount(), echo.contacts())
	} else {
		r.pass("A07", "selection used the healthy exit (dial +1) and skipped the cooling and unknown exits (unknown dials unchanged)")
	}
	if conn != nil {
		_ = conn.Close()
	}

	// Explicit probe/selection/refresh work without restarting the engine and
	// without dropping a live compatible *pool* connection (§2.6).
	tunnel, tunnelBr, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", echo.port()))
	if err != nil || code != 200 {
		t.Fatalf("live pool tunnel: code=%d err=%v", code, err)
	}
	if got, e := tunnelEcho(t, tunnel, tunnelBr, "before-pool-ops"); e != nil || got != "before-pool-ops" {
		t.Fatalf("live pool echo: got=%q err=%v", got, e)
	}
	probe := svc.mustOK("pool", "test", "--exit", deadID)
	probeRows := probe.list(t, "results")
	svc.mustOK("pool", "test", "--exit", socksID)
	svc.mustOK("pool", "select", socksID)
	svc.mustOK("pool", "refresh", "--all")
	svc.mustOK("pool", "rerank")
	svc.mustOK("pool", "select", "auto")
	if got, e := tunnelEcho(t, tunnel, tunnelBr, "after-pool-ops"); e != nil || got != "after-pool-ops" {
		r.fail("A07", "live pool tunnel after pool test/select/refresh/rerank: got=%q err=%v", got, e)
	} else if procStartTime(t, pid) != identity {
		r.fail("A07", "engine process identity changed across probe/select/refresh/rerank")
	} else {
		r.pass("A07", "pool test, select, refresh and rerank keep the engine process and a live compatible pool tunnel intact (start time unchanged, echo preserved)")
	}
	if len(probeRows) == 1 && str(probeRows[0]["exit"]) == deadID {
		r.pass("A07", "pool test --exit reported exactly the probed exit (%s, outcome %s)", deadID, str(probeRows[0]["outcome"]))
	} else {
		r.fail("A07", "pool test results=%+v", probeRows)
	}
	r.note("A07", "the explicit rerank path is exercised (shape, policy version, selection and live-tunnel survival); the 10-minute rotation, EWMA/hysteresis switching and latency thresholds are not")

	// Last-good survives a failed subscription refresh.
	sub := newFixedSubscription(t, []string{deadLink, fx.links[2]})
	subRes := svc.mustOK("import", "subscription", sub.url)
	subID := str(subRes.mapData(t)["source_id"])
	beforeRefresh := svc.mustOK("pool", "list")
	beforeFingerprints := recordFingerprints(beforeRefresh)
	sub.stop()
	refresh := svc.cli("pool", "refresh", "--source", subID)
	afterFingerprints := recordFingerprints(svc.mustOK("pool", "list"))
	if refresh.code == 1 && sameStrings(beforeFingerprints, afterFingerprints) {
		r.pass("A07", "a failed refresh keeps the last-good pool (exit 1, %d records unchanged)", len(afterFingerprints))
	} else {
		r.fail("A07", "failed refresh: exit=%d rows %v → %v", refresh.code, beforeFingerprints, afterFingerprints)
	}
	_ = tunnel.Close()
}

// TestA07RealUsageHealth drives one real tunnel through the production
// frontend and asserts that bytes returned through the exit promote it to
// healthy (contracts §5.3, §6.2). It fails if StartNetwork stops attaching the
// usage-aware pool wrapper, or if probes start counting real traffic twice.
func TestA07RealUsageHealth(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "usage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	echo := newEchoPeer(t)
	second := newSOCKSExit(t)
	poolFile := filepath.Join(t.TempDir(), "pool.txt")
	lines := fx.links[2] + "\n" + fmt.Sprintf("socks5://127.0.0.1:%d#socks-second\n", second.port())
	if err := os.WriteFile(poolFile, []byte(lines), 0o600); err != nil {
		t.Fatalf("write pool fixture: %v", err)
	}

	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000},
	})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", poolFile)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")

	cold, _ := svc.statusNow()
	if cold.poolHealthy != 0 {
		r.fail("A07", "cold start already reported healthy=%d", cold.poolHealthy)
	}

	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", echo.port()))
	if err != nil || code != 200 {
		r.fail("A07", "real-usage tunnel: code=%d err=%v", code, err)
		return
	}
	got, echoErr := tunnelEcho(t, conn, br, "usage-evidence")
	_ = conn.Close()
	if echoErr != nil || got != "usage-evidence" {
		r.fail("A07", "real-usage echo: got=%q err=%v", got, echoErr)
		return
	}

	if !waitFor(15*time.Second, func() bool {
		st, ok := svc.statusNow()
		return ok && st.poolHealthy == 1
	}) {
		st, _ := svc.statusNow()
		r.fail("A07", "a successful real request did not promote any exit: healthy=%d", st.poolHealthy)
		return
	}
	healthy, unknown := 0, 0
	samples, ewma := 0, int64(0)
	usedID := ""
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		switch str(row["status"]) {
		case "healthy":
			healthy++
			usedID = str(row["id"])
			if h, ok := row["health"].(map[string]any); ok {
				samples, ewma = num(h["samples"]), int64(num(h["ewma_latency_ms"]))
			}
		case "unknown":
			unknown++
		}
	}
	if healthy != 1 || unknown != 1 || samples < 1 || ewma <= 0 {
		r.fail("A07", "real-usage health: healthy=%d unknown=%d samples=%d ewma=%d", healthy, unknown, samples, ewma)
		return
	}
	r.pass("A07", "bytes returned through a real tunnel promoted the serving exit to healthy (samples=%d, ewma=%dms) and left the untouched exit unknown", samples, ewma)

	// The probe path must not be counted twice: one explicit egress probe adds
	// exactly one sample, even though a wrapped probe conn would also read a
	// byte and could record usage on top of ApplyProbe. The pause clears the
	// one-per-second usage coalescing window so a double count cannot hide.
	time.Sleep(1100 * time.Millisecond)
	greeter := newGreeterPeer(t, "g")
	probe := svc.mustOK("pool", "test", "--exit", usedID, "--target", fmt.Sprintf("exact.rl.test:%d", greeter.port()))
	rows := probe.list(t, "results")
	if len(rows) != 1 || str(rows[0]["outcome"]) != "success" || num(rows[0]["bytes_down"]) < 1 {
		r.fail("A07", "usage-probe interleaving: rows=%+v", rows)
		return
	}
	time.Sleep(200 * time.Millisecond)
	after := 0
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		if str(row["id"]) == usedID {
			if h, ok := row["health"].(map[string]any); ok {
				after = num(h["samples"])
			}
		}
	}
	if after != samples+1 {
		r.fail("A07", "an egress probe changed samples by %d, want exactly 1", after-samples)
		return
	}
	r.pass("A07", "one explicit egress probe added exactly one sample (no probe/usage double count)")
}

// TestA07AllCooling checks the documented clear error when every exit is
// cooling or unusable.
func TestA07AllCooling(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "allcooling")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	_ = newFixtureSet(t) // exits are not used in this test; keeps the fixture wiring identical
	echo := newEchoPeer(t)
	var lines []string
	for i := 0; i < 2; i++ {
		port := deadVLESSPort(t)
		lines = append(lines, fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=none&type=tcp#dead-%d", randomUUID(t), port, i))
	}
	path := filepath.Join(t.TempDir(), "dead.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write dead pool: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000},
	})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", path)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		svc.mustOK("pool", "test", "--exit", str(row["id"]))
	}
	st, _ := svc.statusNow()
	if st.poolUsable == 0 && st.poolCooling >= 2 {
		r.pass("A07", "every exit is cooling after failed transport probes (usable 0, cooling %d)", st.poolCooling)
	} else {
		r.fail("A07", "all-cooling state: usable=%d cooling=%d", st.poolUsable, st.poolCooling)
	}
	conn, _, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", echo.port()))
	if conn != nil {
		_ = conn.Close()
	}
	time.Sleep(300 * time.Millisecond)
	sawWarning := false
	for _, w := range st.warnings {
		if strings.Contains(strings.ToLower(w), "no usable exit") {
			sawWarning = true
		}
	}
	if code == 502 && echo.contacts() == 0 && sawWarning {
		r.pass("A07", "all-cooling pool fails an approved destination with 502 pool_unavailable, 0 destination contacts and a no-usable-exit warning")
	} else {
		r.fail("A07", "all-cooling destination: code=%d err=%v contacts=%d warnings=%v", code, err, echo.contacts(), st.warnings)
	}
}

// fixedSubscription serves a fixed link list over loopback HTTP; stop takes it
// down so a subsequent refresh fails.
type fixedSubscription struct {
	url string
	srv *http.Server
}

func newFixedSubscription(t *testing.T, links []string) *fixedSubscription {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscription listen: %v", err)
	}
	payload := strings.Join(links, "\n") + "\n"
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	})}
	go func() { _ = srv.Serve(ln) }()
	sub := &fixedSubscription{url: fmt.Sprintf("http://%s/nodes.txt", ln.Addr().String()), srv: srv}
	t.Cleanup(sub.stop)
	return sub
}

func (s *fixedSubscription) stop() { _ = s.srv.Close() }
