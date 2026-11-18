package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA11Launcher checks the two launcher guarantees that are observable
// without touching real desktop settings: an inherited NO_PROXY cannot bypass an
// approved destination, and an unhealthy service refuses to launch a child
// instead of silently going direct.
func TestA11Launcher(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "launcher")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")

	envData := svc.mustOK("env").mapData(t)
	productNoProxy := str(envData["NO_PROXY"])
	// The caller's environment tries to exempt an approved destination (and a
	// random host) from proxying. The human mode leaves the child's stdout on
	// stdout, so the inherited value can be read without JSON framing.
	inherited := []string{"NO_PROXY=exact.rl.test,example.org", "no_proxy=exact.rl.test,example.org"}
	child := svc.cliEnvHuman(inherited, 30*time.Second, "exec", "--", "sh", "-c", `printf "%s|%s" "$NO_PROXY" "$no_proxy"`)
	upper, lower := child.stdout, ""
	if i := strings.Index(child.stdout, "|"); i >= 0 {
		upper, lower = child.stdout[:i], child.stdout[i+1:]
	}
	lowerOK := lower == "" || lower == productNoProxy
	if child.code == 0 && productNoProxy != "" && upper == productNoProxy && lowerOK &&
		!strings.Contains(strings.ToLower(upper), "exact.rl.test") {
		r.pass("A11", "an inherited NO_PROXY naming an approved destination is replaced by the product's fixed list in the child (no bypass)")
	} else {
		r.fail("A11", "child NO_PROXY=%q/%q product=%q exit=%d", upper, lower, productNoProxy, child.code)
	}

	// Unhealthy: listeners closed while management is still alive.
	svc.mustOK("mode", "stopped")
	stoppedEnv := svc.cli("env")
	marker := filepath.Join(env.tmp, "child-ran")
	stoppedExec := svc.cliEnv(inherited, 30*time.Second, "exec", "--", "sh", "-c", "touch '"+marker+"'")
	if stoppedEnv.code == 3 && stoppedExec.code == 3 && !markerExists(marker) {
		r.pass("A11", "with listeners stopped, env and exec exit 3 and the child never runs (no silent direct)")
	} else {
		r.fail("A11", "stopped launcher: env=%d exec=%d child-ran=%v", stoppedEnv.code, stoppedExec.code, markerExists(marker))
	}

	// Unhealthy: the service process is gone entirely.
	if err := svc.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	deadMarker := filepath.Join(env.tmp, "child-ran-dead")
	deadExec := svc.cliEnv(inherited, 30*time.Second, "exec", "--", "sh", "-c", "touch '"+deadMarker+"'")
	if deadExec.code == 3 && !markerExists(deadMarker) {
		r.pass("A11", "with no service at all, exec exits 3 and never starts the child directly")
	} else {
		r.fail("A11", "dead service exec: exit=%d child-ran=%v", deadExec.code, markerExists(deadMarker))
	}
	r.note("A11", "real desktop proxy set/restore is covered only by the gated ROUTELATCH_DESKTOP_TEST=1 check; this test asserts the launcher environment and unhealthy-service refusal")
}
