package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestA09SanitizedNodeTitle drives an untrusted node title carrying ESC, CSI,
// OSC, BEL and CR bytes through import and every display surface. The stored
// text is untrusted; the terminal boundary is what is asserted.
func TestA09SanitizedNodeTitle(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "sanitize")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	// ESC [ 31m , ESC ] 0;owned BEL , CR LF: percent-encoded in the fragment.
	attack := "%1b%5b31mPWN%1b%5d0%3Bowned%07%0d%0aSECOND"
	link := fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=none&type=tcp#%s", fx.uuid, fx.vless.port, attack)
	if res := svc.cli("import", "link", link); res.code != 0 {
		t.Fatalf("import attack title: exit=%d %s", res.code, res.stderr)
	}

	jsonOut := svc.mustOK("pool", "list").stdout
	human := svc.cliHuman("pool", "list")
	statusJSON := svc.mustOK("status").stdout

	// Only the pool tables carry a record's display name; status does not, and
	// the service's streams/log carry IDs rather than titles, so those are
	// checked as controls, not as proof.
	bad := []string{"\x1b", "\x9b", "\x07", "\r"}
	surfaces := map[string]string{"pool list --json": jsonOut, "pool list": human.stdout, "service stdout": svc.Stdout(), "service stderr": svc.Stderr()}
	if raw, err := os.ReadFile(filepath.Join(dir, "log", "routelatch.log")); err == nil {
		surfaces["log file"] = string(raw)
	}
	clean := true
	for name, out := range surfaces {
		for _, seq := range bad {
			if strings.Contains(out, seq) {
				r.fail("A09", "%s contains a raw %q terminal control byte", name, seq)
				clean = false
			}
		}
	}
	if clean && strings.Contains(jsonOut, "PWN") && strings.Contains(human.stdout, "PWN") {
		r.pass("A09", "a node title with ESC/CSI/OSC/BEL/CR appears sanitized in both pool tables; the service streams and (when present) the log file carry no raw control byte")
	}
	if !strings.Contains(statusJSON, "PWN") {
		r.note("A09", "status does not carry node titles (checked as a control; it cannot leak the title)")
	}
	if strings.Contains(human.stdout, "PWN") && strings.Contains(human.stdout, "SECOND") {
		r.pass("A09", "the sanitized title keeps its printable text (PWN … SECOND) in the human table")
	} else {
		r.fail("A09", "the sanitized title is not visible as text: %q", human.stdout)
	}
	if !strings.Contains(human.stdout, "\x1b[31m") {
		r.pass("A09", "the SGR wipe attempt (ESC[31m) never reaches the terminal")
	} else {
		r.fail("A09", "the human table rendered the SGR sequence")
	}

	// Unauthorized-peer rejection needs a second uid; an unprivileged test
	// process cannot create one without changing the machine.
	if os.Getuid() == 0 {
		r.note("A09", "running as root; the same-uid boundary is documented as inside the trust boundary and is not asserted")
	} else {
		r.note("A09", "unauthorized-peer SO_PEERCRED rejection is not testable here: creating a second uid needs privileges the suite does not have (running as uid %d)", os.Getuid())
	}
}
