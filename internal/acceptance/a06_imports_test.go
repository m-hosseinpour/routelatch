package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA06ImportSurfaces covers the import paths beyond the file import:
// per-protocol `import link`, a v2rayN export, and doctor's report on
// quarantined records.
func TestA06ImportSurfaces(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	echo := newEchoPeer(t)
	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })

	// import link, one protocol at a time.
	protocols := []string{"vless", "trojan", "socks5"}
	for i, link := range fx.links {
		res := svc.mustOK("import", "link", link)
		counts, _ := res.mapData(t)["records"].(map[string]any)
		if num(counts["valid"]) != 1 {
			r.fail("A06", "import link %s: valid=%v, want 1", protocols[i], counts["valid"])
			continue
		}
		r.pass("A06", "import link accepted one %s link (valid 1, invalid 0, unsupported 0)", protocols[i])
	}
	pool := svc.mustOK("pool", "list")
	if got := len(pool.list(t, "records")); got != 3 {
		r.fail("A06", "three link imports produced %d records, want 3", got)
	} else {
		r.pass("A06", "three link imports merged into 3 records (dedupe keeps one per identity)")
	}

	// Trojan credentials now map to a working exit: an approved domain through
	// the Trojan core exit reaches the loopback destination and echoes.
	trojanRow := findRecord(t, pool, "trojan")
	svc.mustOK("pool", "select", str(trojanRow["id"]))
	svc.mustOK("rules", "add", "--kind", "domain", "trojan.rl.test")
	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("trojan.rl.test:%d", echo.port()))
	if err != nil || code != 200 {
		r.fail("A06", "Trojan tunnel CONNECT: code=%d err=%v (exit logs: %s)", code, err, fx.trojan.Logs())
	} else if got, e := tunnelEcho(t, conn, br, "trojan-echo"); e != nil || got != "trojan-echo" {
		r.fail("A06", "Trojan tunnel echo: got=%q err=%v", got, e)
	} else if !waitFor(5*time.Second, func() bool { return echo.contacts() == 1 }) {
		r.fail("A06", "Trojan tunnel reached the destination %d times, want 1", echo.contacts())
	} else {
		r.pass("A06", "imported Trojan credentials drive a live tunnel (echo through the pinned Trojan exit, 1 destination contact)")
	}
	if conn != nil {
		_ = conn.Close()
	}

	// v2rayN export: three profiles in the documented JSON shape.
	v2 := []map[string]any{
		{"remarks": "v2-vless", "protocol": "vless", "address": "127.0.0.1", "port": int(fx.vless.port), "id": fx.uuid, "network": "tcp"},
		{"remarks": "v2-trojan", "protocol": "trojan", "address": "127.0.0.1", "port": int(fx.trojan.port), "password": fx.password, "network": "tcp"},
		{"remarks": "v2-socks", "protocol": "socks", "address": "127.0.0.1", "port": int(fx.socks.port()), "network": "tcp"},
	}
	raw, err := json.MarshalIndent(v2, "", "  ")
	if err != nil {
		t.Fatalf("marshal v2rayN: %v", err)
	}
	v2Path := filepath.Join(t.TempDir(), "v2rayn.json")
	if err := os.WriteFile(v2Path, raw, 0o600); err != nil {
		t.Fatalf("write v2rayN: %v", err)
	}
	v2Res := svc.mustOK("import", "v2rayn", v2Path)
	v2Data := v2Res.mapData(t)
	v2Counts, _ := v2Data["records"].(map[string]any)
	if num(v2Counts["valid"]) != 3 || num(v2Counts["invalid"]) != 0 || num(v2Counts["unsupported"]) != 0 {
		r.fail("A06", "v2rayN import counts=%+v, want 3/0/0", v2Counts)
	} else {
		r.pass("A06", "v2rayN JSON export imported 3 profiles (valid 3, invalid 0, unsupported 0)")
	}
	v2Source := str(v2Data["source_id"])
	attributed := 0
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		if sources, ok := row["sources"].([]any); ok {
			for _, s := range sources {
				if str(s) == v2Source {
					attributed++
				}
			}
		}
	}
	if v2Source != "" && attributed == 3 {
		r.pass("A06", "the v2rayN source is attributed on the 3 records (ownership survives dedupe)")
	} else {
		r.fail("A06", "v2rayN source attribution: source=%q rows=%d", v2Source, attributed)
	}

	// doctor must report the quarantined record with the remediation hint.
	insecureLink := fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=tls&allowInsecure=1&type=tcp#insecure", randomUUID(t), fx.vless.port)
	insecurePath := filepath.Join(t.TempDir(), "insecure.txt")
	if err := os.WriteFile(insecurePath, []byte(insecureLink+"\n"+fx.links[1]+"\n"), 0o600); err != nil {
		t.Fatalf("write insecure fixture: %v", err)
	}
	svc.mustOK("pool", "add", "--file", insecurePath)
	doctor := svc.mustOK("doctor").mapData(t)
	checks, _ := doctor["checks"].([]any)
	var quarantined map[string]any
	for _, item := range checks {
		ch, ok := item.(map[string]any)
		if ok && str(ch["name"]) == "quarantined_records" {
			quarantined = ch
		}
	}
	fix := strings.ToLower(str(quarantined["fix"]))
	if quarantined != nil && str(quarantined["status"]) == "warn" &&
		(strings.Contains(fix, "certificate") || strings.Contains(fix, "pin")) {
		r.pass("A06", "doctor reports quarantined records as a warning with a certificate/pin remediation")
	} else {
		r.fail("A06", "doctor quarantined check=%+v", quarantined)
	}
}
