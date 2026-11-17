package acceptance

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestFailureAndLimits covers the documented failure modes: a corrupt state
// file, a second instance, a traffic-port collision and a clean shutdown.
func TestFailureAndLimits(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)

	// Corrupt state fails closed without overwriting the file.
	t.Run("corrupt state", func(t *testing.T) {
		dir := filepath.Join(env.home, "corrupt")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
		writeConfig(t, dir, serviceConfig{HTTPListen: fmt.Sprintf("127.0.0.1:%d", freePort(t)), SOCKSListen: fmt.Sprintf("127.0.0.1:%d", freePort(t))})
		statePath := filepath.Join(dir, "state.json")
		corrupt := []byte("{\"schema_version\":1,\"mode\":")
		if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
			t.Fatalf("write corrupt state: %v", err)
		}
		code, out := startExpectingFailure(t, env, dir)
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("re-read state: %v", err)
		}
		if code == 10 && string(after) == string(corrupt) {
			r.pass("A13", "corrupt state exits 10 and the file is byte-identical afterwards (%d bytes)", len(after))
		} else {
			r.fail("A13", "corrupt state: exit=%d unchanged=%v output=%s", code, string(after) == string(corrupt), out)
		}
		info, err := os.Stat(statePath)
		if err == nil && info.Mode().Perm() == 0o600 {
			r.pass("A13", "the untouched corrupt state file keeps mode 0600")
		} else {
			r.fail("A13", "corrupt state permissions changed: %v", err)
		}

		// A newer schema is rejected, not guessed.
		newer := filepath.Join(env.home, "newer")
		if err := os.MkdirAll(newer, 0o700); err != nil {
			t.Fatalf("newer dir: %v", err)
		}
		writeConfig(t, newer, serviceConfig{HTTPListen: fmt.Sprintf("127.0.0.1:%d", freePort(t)), SOCKSListen: fmt.Sprintf("127.0.0.1:%d", freePort(t))})
		newerPath := filepath.Join(newer, "state.json")
		doc := `{"schema_version":99,"mode":"targeted","policy_version":1}`
		if err := os.WriteFile(newerPath, []byte(doc), 0o600); err != nil {
			t.Fatalf("write newer state: %v", err)
		}
		code, out = startExpectingFailure(t, env, newer)
		after, _ = os.ReadFile(newerPath)
		if code == 10 && string(after) == doc {
			r.pass("A13", "a newer state schema exits 10 without downgrade or overwrite")
		} else {
			r.fail("A13", "newer schema: exit=%d unchanged=%v output=%s", code, string(after) == doc, out)
		}
	})

	// A corrupt config is also a fail-closed startup error.
	t.Run("corrupt config", func(t *testing.T) {
		dir := filepath.Join(env.home, "badconfig")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
		cfgPath := filepath.Join(dir, "config.json")
		bad := []byte("{\"schema_version\":1,\"http_listen\":")
		if err := os.WriteFile(cfgPath, bad, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		code, out := startExpectingFailure(t, env, dir)
		after, _ := os.ReadFile(cfgPath)
		if code == 10 && string(after) == string(bad) {
			r.pass("A13", "corrupt config exits 10 and is left untouched")
		} else {
			r.fail("A13", "corrupt config: exit=%d unchanged=%v output=%s", code, string(after) == string(bad), out)
		}
	})

	// Second instance gets the documented singleton error and does not touch
	// the first instance's data.
	t.Run("second instance", func(t *testing.T) {
		dir := filepath.Join(env.home, "singleton")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
		svc := startService(t, env, dir, serviceConfig{})
		defer svc.stop()
		code, out := startExpectingFailure(t, env, dir)
		still := svc.cli("status")
		if code == 4 && !strings.Contains(out, `"ok":true`) && still.code == 0 {
			r.pass("A13", "a second instance exits 4 with a non-success envelope and leaves the first one serving")
		} else {
			r.fail("A13", "second instance: exit=%d output=%s first-status-exit=%d", code, out, still.code)
		}
	})

	// Traffic port collision is a clear error and never selects another port.
	t.Run("port collision", func(t *testing.T) {
		dir := filepath.Join(env.home, "collision")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("hold port: %v", err)
		}
		defer held.Close()
		heldPort := held.Addr().(*net.TCPAddr).Port
		freeSocks := freePort(t)
		writeConfig(t, dir, serviceConfig{
			HTTPListen:  fmt.Sprintf("127.0.0.1:%d", heldPort),
			SOCKSListen: fmt.Sprintf("127.0.0.1:%d", freeSocks),
		})
		code, out := startExpectingFailure(t, env, dir)
		fallback := !listenerRefuses(t, fmt.Sprintf("127.0.0.1:%d", freeSocks))
		if code == 6 && !fallback {
			r.pass("A13", "an occupied HTTP port exits 6 with no silent fallback port and no SOCKS listener left behind")
		} else {
			r.fail("A13", "port collision: exit=%d fallback-listener=%v output=%s", code, fallback, out)
		}
		if strings.Contains(out, fmt.Sprintf("%d", heldPort)) && strings.Contains(strings.ToLower(out), "listener") {
			r.pass("A13", "the collision error names the listener and the failing port")
		} else {
			r.fail("A13", "collision error does not name the port: %s", strings.TrimSpace(out))
		}
	})
}

// TestResourceBaseline checks that the product's descriptors and the test's
// goroutines return to their idle baseline after a full traffic flow.
func TestResourceBaseline(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "resources")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	goroutinesBefore := runtime.NumGoroutine()
	fx := newFixtureSet(t)
	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 1, "cooldown_max": 5},
	})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")
	svc.mustOK("pool", "select", str(findRecord(t, svc.mustOK("pool", "list"), "socks5")["id"]))

	time.Sleep(400 * time.Millisecond)
	baseline, err := svc.fdCount()
	if err != nil {
		t.Fatalf("baseline fds: %v", err)
	}
	echo := newEchoPeer(t)
	directAddr := fmt.Sprintf("127.0.0.1:%d", echo.port())
	poolAddr := fmt.Sprintf("exact.rl.test:%d", echo.port())

	for i := 0; i < 5; i++ {
		if c, br, code, err := connectHTTP(t, svc.httpAddr, directAddr); err == nil && code == 200 {
			_, _ = tunnelEcho(t, c, br, "x")
			_ = c.Close()
		}
		c, _, code, err := connectHTTP(t, svc.httpAddr, poolAddr)
		if err == nil {
			_ = c.Close()
			_ = code
		}
	}

	var final int
	settled := waitFor(8*time.Second, func() bool {
		n, err := svc.fdCount()
		if err != nil {
			return false
		}
		final = n
		return n <= baseline+2
	})
	if settled {
		r.pass("A09", "product descriptors returned to baseline after the flow (%d baseline, %d final)", baseline, final)
	} else {
		r.fail("A09", "product descriptors did not settle: baseline=%d final=%d", baseline, final)
	}

	if err := svc.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Fixtures are stopped explicitly here so the goroutine baseline measures
	// leftovers, not the test's own servers.
	fx.close()
	echo.close()
	goroutineSettled := waitFor(8*time.Second, func() bool { return runtime.NumGoroutine() <= goroutinesBefore+2 })
	if goroutineSettled {
		r.pass("A09", "test-process goroutines returned to baseline after fixtures and service stopped (%d baseline, %d final)", goroutinesBefore, runtime.NumGoroutine())
	} else {
		r.fail("A09", "test-process goroutines did not settle: baseline=%d final=%d", goroutinesBefore, runtime.NumGoroutine())
	}
}
