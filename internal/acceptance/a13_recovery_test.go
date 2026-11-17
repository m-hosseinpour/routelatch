package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestA13CrashRecovery kills the service with SIGKILL mid-flow, restarts it on
// the same data dir and checks that the committed state is intact and usable.
// systemd/launcher lifecycle is out of scope for this unit.
func TestA13CrashRecovery(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "recovery")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	echo := newEchoPeer(t)
	origin := newRecordingOrigin(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")
	svc.mustOK("rules", "add", "--kind", "domain", "stall.rl.test")
	svc.mustOK("pool", "select", str(findRecord(t, svc.mustOK("pool", "list"), "socks5")["id"]))

	live, liveBr, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", echo.port()))
	if err != nil || code != 200 {
		t.Fatalf("pre-crash tunnel: code=%d err=%v", code, err)
	}
	if got, e := tunnelEcho(t, live, liveBr, "pre-crash"); e != nil || got != "pre-crash" {
		t.Fatalf("pre-crash echo: got=%q err=%v", got, e)
	}
	stateBefore, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	// A request is in flight (the origin's streaming handler has started) when
	// the process is killed.
	streamDone := make(chan error, 1)
	go func() {
		req := fmt.Sprintf("GET http://127.0.0.1:%d/stream HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", origin.port(), origin.port())
		resp, body, err := httpForwardMethod(t, svc.httpAddr, req, "GET", 10*time.Second)
		switch {
		case err != nil:
			streamDone <- err
		case resp == nil || resp.StatusCode != 200:
			streamDone <- fmt.Errorf("stream status=%v", resp)
		case string(body) == "startend":
			streamDone <- nil // the whole response completed before the kill: not mid-flow
		default:
			streamDone <- fmt.Errorf("short body %q", body)
		}
	}()
	if !waitFor(5*time.Second, func() bool { return origin.count() > 0 }) {
		t.Fatalf("in-flight stream never reached the origin")
	}

	if err := svc.proc.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	killErr := svc.waitExit(10 * time.Second)
	if killErr == nil {
		r.fail("A13", "the service exited 0 after SIGKILL")
	} else {
		r.pass("A13", "the service is killed with SIGKILL (unclean exit, no shutdown path)")
	}
	_ = live.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := live.Read(make([]byte, 1)); err == nil {
		r.fail("A13", "the pre-crash tunnel still delivered data after SIGKILL")
	} else {
		r.pass("A13", "the pre-crash idle tunnel is gone after SIGKILL")
	}
	_ = live.Close()
	select {
	case err := <-streamDone:
		if err != nil {
			r.pass("A13", "the in-flight streaming request was cut by SIGKILL (no completed response)")
		} else {
			r.fail("A13", "the in-flight streaming request completed before SIGKILL; the kill was not mid-flow")
		}
	case <-time.After(10 * time.Second):
		r.fail("A13", "the in-flight streaming request never ended after SIGKILL")
	}

	// Restart on the same data dir and the same listener configuration.
	svc2 := startService(t, env, dir, svc.cfg)
	t.Cleanup(func() { _ = svc2.stop() })
	stateAfter, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read state after restart: %v", err)
	}
	st, _ := svc2.statusNow()
	rows := svc2.mustOK("pool", "list").list(t, "records")
	rules := svc2.mustOK("rules", "list").list(t, "rules")
	if string(stateBefore) == string(stateAfter) && st.poolTotal == 3 && st.poolUsable == 3 && len(rows) == 3 && len(rules) == 2 {
		r.pass("A13", "after an unclean kill and restart the committed state is byte-identical (3 exits, 2 rules, %d usable)", st.poolUsable)
	} else {
		r.fail("A13", "recovery: state-identical=%v pool=%d/%d rows=%d rules=%d", string(stateBefore) == string(stateAfter), st.poolTotal, st.poolUsable, len(rows), len(rules))
	}
	svc2.mustOK("pool", "select", str(findRecord(t, svc2.mustOK("pool", "list"), "socks5")["id"]))
	recovered, recoveredBr, code, err := connectHTTP(t, svc2.httpAddr, fmt.Sprintf("exact.rl.test:%d", echo.port()))
	if err != nil || code != 200 {
		r.fail("A13", "approved destination after restart: code=%d err=%v", code, err)
	} else if got, e := tunnelEcho(t, recovered, recoveredBr, "recovered"); e != nil || got != "recovered" {
		r.fail("A13", "post-restart tunnel echo: got=%q err=%v", got, e)
	} else {
		r.pass("A13", "the recovered rules and pool serve a live approved-destination tunnel after the crash")
	}
	if recovered != nil {
		_ = recovered.Close()
	}
	direct, directBr, code, err := connectHTTP(t, svc2.httpAddr, fmt.Sprintf("127.0.0.1:%d", echo.port()))
	if err == nil && code == 200 {
		if got, e := tunnelEcho(t, direct, directBr, "direct-after"); e == nil && got == "direct-after" {
			r.pass("A13", "the direct destination works after recovery")
		}
		_ = direct.Close()
	}

	// The stopped mode persists across a crash and a restart exactly once: the
	// restarted process stays stopped until a new process explicitly changes it.
	svc2.mustOK("mode", "stopped")
	if listenerRefuses(t, svc2.httpAddr) && listenerRefuses(t, svc2.socksAddr) {
		r.pass("A13", "stopped closes both listeners before the second crash")
	} else {
		r.fail("A13", "stopped listeners still accept before the second crash")
	}
	if err := svc2.proc.Process.Kill(); err != nil {
		t.Fatalf("second SIGKILL: %v", err)
	}
	_ = svc2.waitExit(10 * time.Second)

	svc3 := startService(t, env, dir, svc2.cfg)
	t.Cleanup(func() { _ = svc3.stop() })
	st3, _ := svc3.statusNow()
	if st3.mode == "stopped" && listenerRefuses(t, svc3.httpAddr) && listenerRefuses(t, svc3.socksAddr) {
		r.pass("A13", "the stopped mode persists across the crash and restart (mode stopped, no listeners)")
	} else {
		r.fail("A13", "stopped persistence: mode=%q http-refuses=%v socks-refuses=%v", st3.mode,
			listenerRefuses(t, svc3.httpAddr), listenerRefuses(t, svc3.socksAddr))
	}
	leave := svc3.mustOK("mode", "targeted")
	recovered2, recovered2Br, code, err := connectHTTP(t, svc3.httpAddr, fmt.Sprintf("127.0.0.1:%d", echo.port()))
	if str(leave.mapData(t)["mode"]) == "targeted" && err == nil && code == 200 {
		if got, e := tunnelEcho(t, recovered2, recovered2Br, "after-stopped"); e == nil && got == "after-stopped" {
			r.pass("A13", "a new process may leave the persisted stopped state once and serve traffic again")
		}
	} else {
		r.fail("A13", "leave stopped after restart: mode=%v code=%d err=%v", str(leave.mapData(t)["mode"]), code, err)
	}
	if recovered2 != nil {
		_ = recovered2.Close()
	}
	r.note("A13", "systemd unit lifecycle, crash-loop supervision and a foreign listener's behaviour after a port collision are out of scope for this unit")
}
