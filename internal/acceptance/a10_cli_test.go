package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA10CLISurface runs the commands the earlier flow did not touch and
// asserts their documented shape and exit codes.
func TestA10CLISurface(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	echo := newEchoPeer(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("import", "file", fx.filePath)
	svc.mustOK("rules", "add", "--kind", "domain", "exact.rl.test")

	// env: loopback proxy variables plus a non-empty NO_PROXY and notice.
	envRes := svc.mustOK("env")
	envData := envRes.mapData(t)
	httpProxy, httpsProxy, allProxy := str(envData["HTTP_PROXY"]), str(envData["HTTPS_PROXY"]), str(envData["ALL_PROXY"])
	noProxy := str(envData["NO_PROXY"])
	if strings.HasPrefix(httpProxy, "http://127.0.0.1:") && strings.HasPrefix(httpsProxy, "http://127.0.0.1:") &&
		strings.HasPrefix(allProxy, "http://127.0.0.1:") && noProxy != "" && str(envData["notice"]) != "" {
		r.pass("A10", "env returns loopback HTTP(S)/ALL proxy values, a NO_PROXY list and a notice")
	} else {
		r.fail("A10", "env values: %+v", envData)
	}

	// exec: proxy environment reaches the child; the child's exit code is
	// passed through; a direct fallback is never offered.
	child := svc.cli("exec", "--", "sh", "-c", `printf "%s" "$HTTPS_PROXY"`)
	// §7.1: with --json the child's stdout and stderr go to stderr and stdout
	// carries exactly one Envelope.
	if child.code == 0 && child.env.OK && num(child.mapData(t)["exit_code"]) == 0 &&
		strings.Contains(child.stderr, httpsProxy) {
		r.pass("A10", "exec injects the proxy environment into the child and, with --json, keeps child output on stderr and one parseable Envelope on stdout")
	} else {
		r.fail("A10", "exec --json: exit=%d ok=%v stdout=%q stderr=%q (want the child value on stderr and a parseable envelope)", child.code, child.env.OK, child.stdout, child.stderr)
	}
	exit7 := svc.cli("exec", "--", "sh", "-c", "exit 7")
	if exit7.code == 7 {
		r.pass("A10", "exec passes the child exit code through (7)")
	} else {
		r.fail("A10", "exec child exit: %d, want 7", exit7.code)
	}

	// doctor: a structured check list, including files, listeners and pool.
	doctor := svc.mustOK("doctor").mapData(t)
	checks, _ := doctor["checks"].([]any)
	names := map[string]bool{}
	shapeOK := len(checks) > 0
	for _, item := range checks {
		ch, ok := item.(map[string]any)
		if !ok {
			shapeOK = false
			continue
		}
		names[str(ch["name"])] = true
		switch str(ch["status"]) {
		case "ok", "warn", "error":
		default:
			shapeOK = false
		}
		if str(ch["detail"]) == "" {
			shapeOK = false
		}
	}
	if shapeOK && names["state_file"] && names["listeners"] && names["pool"] {
		r.pass("A10", "doctor returns %d structured checks including state_file, listeners and pool", len(checks))
	} else {
		r.fail("A10", "doctor shape: %d checks, names=%v", len(checks), names)
	}

	// backup and restore: the documented ref shape, and a restore that keeps
	// the rules and serves traffic.
	backup := svc.mustOK("backup").mapData(t)
	ref, _ := backup["ref"].(map[string]any)
	refPath := str(ref["path"])
	checksum := str(ref["checksum"])
	if refPath != "" && strings.HasPrefix(refPath, filepath.Join(dir, "backup")+string(os.PathSeparator)) &&
		strings.HasPrefix(checksum, "sha256:") && num(ref["schema_version"]) == 1 && str(ref["created_at"]) != "" {
		r.pass("A10", "backup returns a ref inside the private backup dir with schema 1 and a sha256 checksum")
	} else {
		r.fail("A10", "backup ref: %+v", ref)
	}
	restore := svc.mustOK("restore", refPath).mapData(t)
	afterRestore := len(svc.mustOK("rules", "list").list(t, "rules"))
	if str(restore["mode"]) == "targeted" && num(restore["policy_version"]) >= 1 && afterRestore == 1 {
		r.pass("A10", "restore of that ref reports mode/policy and keeps the rule set")
	} else {
		r.fail("A10", "restore: %+v rules=%d", restore, afterRestore)
	}
	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", echo.port()))
	if err != nil || code != 200 {
		r.fail("A10", "direct destination after restore: code=%d err=%v", code, err)
	} else {
		if got, e := tunnelEcho(t, conn, br, "restored"); e == nil && got == "restored" {
			r.pass("A10", "traffic still works after restore")
		} else {
			r.fail("A10", "traffic after restore: got=%q err=%v", got, e)
		}
		_ = conn.Close()
	}

	// desktop proxy status: documented shape; unsupported environment is an
	// explicit false, not an error.
	desktop := svc.mustOK("desktop", "proxy", "status").mapData(t)
	if _, ok := desktop["supported"].(bool); ok {
		if _, ok := desktop["applied"].(bool); ok {
			r.pass("A10", "desktop proxy status returns supported/applied booleans (supported=%v)", desktop["supported"])
		} else {
			r.fail("A10", "desktop status missing applied: %+v", desktop)
		}
	} else {
		r.fail("A10", "desktop status missing supported: %+v", desktop)
	}

	// config get/path/set.
	cfg := svc.mustOK("config", "get").mapData(t)
	cfgPath := svc.mustOK("config", "path").mapData(t)
	values, _ := cfg["values"].(map[string]any)
	if values != nil && str(values["http_listen"]) == svc.httpAddr && str(values["log_level"]) != "" &&
		str(cfgPath["config"]) == filepath.Join(dir, "config.json") &&
		str(cfgPath["state"]) == filepath.Join(dir, "state.json") &&
		str(cfgPath["socket"]) == filepath.Join(dir, "run", "control.sock") {
		r.pass("A10", "config get/path return the effective values and the private data paths")
	} else {
		r.fail("A10", "config get=%+v path=%+v", cfg, cfgPath)
	}
	set := svc.mustOK("config", "set", "log_level", "debug")
	setValues, _ := set.mapData(t)["values"].(map[string]any)
	if str(setValues["log_level"]) == "debug" && strings.Contains(strings.ToLower(str(set.mapData(t)["notice"])), "restart") {
		r.pass("A10", "config set applies log_level and reports that the running prober keeps its startup value until a restart")
	} else {
		r.fail("A10", "config set: values=%+v notice=%q", setValues, str(set.mapData(t)["notice"]))
	}
	// §4.2: any non-loopback listener is `unsupported` (exit 7), not a usage
	// error. The wildcard and a valid LAN address are both outside v1.
	wildcardSet := svc.cli("config", "set", "http_listen", "0.0.0.0:1000")
	lanSet := svc.cli("config", "set", "http_listen", "192.168.1.10:1000")
	if wildcardSet.code == 7 && errorKind(wildcardSet) == "unsupported" &&
		lanSet.code == 7 && errorKind(lanSet) == "unsupported" {
		r.pass("A10", "wildcard and valid-but-non-loopback listeners are refused as unsupported with exit 7 (§4.2)")
	} else {
		r.fail("A10", "non-loopback listeners: 0.0.0.0 exit=%d kind=%q, 192.168.1.10 exit=%d kind=%q; §4.2 requires unsupported/7",
			wildcardSet.code, errorKind(wildcardSet), lanSet.code, errorKind(lanSet))
	}
	r.note("A10", "§7.1's config get|set|path row lists exits 0/2/3/6 and omits 7, while §4.2 requires unsupported/7 for a non-loopback listener; the command table should be amended to include 7")

	// menu in a non-interactive shell must return without hanging or mutating.
	menu := svc.cliEnv(nil, 10*time.Second, "menu")
	if menu.code == 0 {
		r.pass("A10", "the menu in a non-interactive shell (stdin at EOF) exits 0 without hanging")
	} else {
		r.fail("A10", "menu non-interactive: exit=%d (a hang is killed by the 10s bound)", menu.code)
	}
	r.note("A10", "menu item mapping is not driven item-by-item; only the non-interactive shell exit is asserted here (the item map is covered by the package's unit tests)")
}
