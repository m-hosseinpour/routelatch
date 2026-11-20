package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R11 obfs acceptance evidence. The default suite proves the obfs projection
// into the handoff config with unit tests (no binary, no network); this gated
// cell proves that both obfuscators actually carry traffic through the real
// product path: a hysteria2:// link is imported, projected into the generated
// sing-box sidecar config, and the sidecar's hysteria2 client completes the
// obfs handshake with a loopback sing-box hysteria2 inbound. The binary is the
// same digest-pinned official archive the R11 cell uses; nothing is installed.
const r11ObfsPassword = "r11-hy2-obfs-secret"

func TestR11Hysteria2ObfsRealTraffic(t *testing.T) {
	if os.Getenv("RL_ACCEPT_HYSTERIA2_OBFS_TEST") != "1" {
		t.Skip("set RL_ACCEPT_HYSTERIA2_OBFS_TEST=1 for the real hysteria2 obfs cell (downloads a digest-verified sing-box into /tmp/opencode)")
	}
	r := report{t: t}
	work, err := os.MkdirTemp("/tmp/opencode", "rl-r11-obfs-")
	if err != nil {
		r11NotRun(t, r, "cannot create a disposable dir under /tmp/opencode: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	binary := r11ArchiveBinary(t, r, work)
	certs := r11MakeCerts(t, work)

	for _, obfsType := range []string{"salamander", "gecko"} {
		t.Run("obfs-"+obfsType, func(t *testing.T) {
			r11ObfsPositive(t, binary, certs, obfsType)
		})
	}
	// The server always speaks salamander; each negative client disagrees on
	// either the password or the type, so the handshake must fail before any
	// destination is contacted.
	for _, neg := range []struct {
		name, obfs, password string
	}{
		{"password-mismatch", "salamander", r11ObfsPassword + "-wrong"},
		{"type-mismatch", "gecko", r11ObfsPassword},
	} {
		t.Run(neg.name, func(t *testing.T) {
			r11ObfsNegative(t, binary, certs, neg.name, neg.obfs, neg.password)
		})
	}
}

// r11ObfsService starts a RouteLatch process with the verified binary as the
// sidecar and returns the service plus its data dir.
func r11ObfsService(t *testing.T, binary string, certs r11Certs) (*service, string) {
	t.Helper()
	env := newTestEnv(t)
	env.vars = append(env.vars,
		"SSL_CERT_FILE="+certs.caPath,
		"ROUTELATCH_SINGBOX_EXEC="+binary,
	)
	dir := filepath.Join(env.home, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("r11 obfs data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 2000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })
	return svc, dir
}

// r11ObfsImport drives the real import path and waits for the regenerated
// sidecar config. A non-nil outbound proves the obfs values survived the link,
// the stored contract and the generated document.
func r11ObfsImport(t *testing.T, svc *service, dir, link string, port uint16, obfsType, obfsPassword string) bool {
	t.Helper()
	r := report{t: t}
	res := svc.mustOK("import", "link", link)
	if got := num(res.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11-obfs", "hysteria2 obfs link import valid=%d, want 1", got)
		return false
	}
	cfgPath := filepath.Join(dir, "sidecar", "sing-box.json")
	if !waitFor(5*time.Second, func() bool { return fileContains(t, cfgPath, `"obfs"`) }) {
		r.fail("R11-obfs", "the generated sidecar config never carried an obfs block")
		return false
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		r.fail("R11-obfs", "read the generated sidecar config: %v", err)
		return false
	}
	var cfg struct {
		Outbounds []struct {
			Type       string `json:"type"`
			ServerPort uint16 `json:"server_port"`
			Obfs       *struct {
				Type     string `json:"type"`
				Password string `json:"password"`
			} `json:"obfs"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		r.fail("R11-obfs", "decode the generated sidecar config: %v", err)
		return false
	}
	found := false
	for _, out := range cfg.Outbounds {
		if out.Type == "hysteria2" && out.ServerPort == port {
			found = out.Obfs != nil && out.Obfs.Type == obfsType && out.Obfs.Password == obfsPassword
		}
	}
	if !found {
		r.fail("R11-obfs", "generated sidecar hysteria2 outbound for port %d has no matching obfs {type:%q,password:%q}", port, obfsType, obfsPassword)
		return false
	}
	r.pass("R11-obfs", "the hysteria2:// link with obfs=%s becomes a top-level sidecar obfs block with the imported password", obfsType)
	return true
}

// r11ObfsEcho is the shared data-path assertion: CONNECT to the approved
// fixture host, a byte-identical echo, exactly one destination contact, and a
// live connection proof.
func r11ObfsEcho(t *testing.T, svc *service, echo *echoPeer, label string) {
	t.Helper()
	r := report{t: t}
	payload := label + "-" + randomHex(t, 6)
	conn, br, code, reason, err := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo.port()))
	if err != nil || code != 200 {
		r.fail("R11-obfs", "approved CONNECT via the real %s-obfuscated sidecar: code=%d reason=%q err=%v", label, code, reason, err)
		return
	}
	got, e := tunnelEcho(t, conn, br, payload)
	contacts := echo.contacts()
	// Status counts proofs on LIVE connections, so read it before closing.
	st := svc.mustOK("status").mapData(t)
	conns, _ := st["connections"].(map[string]any)
	proofs, _ := conns["proofs"].(map[string]any)
	_ = conn.Close()
	switch {
	case e != nil || got != payload:
		r.fail("R11-obfs", "%s tunnel echo: got=%q err=%v", label, got, e)
	case contacts != 1:
		r.fail("R11-obfs", "%s destination contacts=%d, want exactly 1", label, contacts)
	case num(proofs["confirmed"]) < 1:
		r.fail("R11-obfs", "%s has no confirmed connection proof: %+v", label, proofs)
	default:
		r.pass("R11-obfs", "%s: RouteLatch → real sing-box sidecar → hysteria2 server → echo peer returned %d bytes byte-identically (1 destination contact, %d confirmed proof)",
			label, len(payload), num(proofs["confirmed"]))
	}
}

func r11ObfsPositive(t *testing.T, binary string, certs r11Certs, obfsType string) {
	t.Helper()
	r := report{t: t}
	port := freePort(t)
	r11StartServerWithObfs(t, binary, port, certs, obfsType, r11ObfsPassword)
	echo := newEchoPeer(t)
	svc, dir := r11ObfsService(t, binary, certs)

	link := fmt.Sprintf("hysteria2://%s@127.0.0.1:%d?sni=%s&obfs=%s&obfs-password=%s#hy-obfs-%s",
		r11Password, port, r11SNI, obfsType, r11ObfsPassword, obfsType)
	if !r11ObfsImport(t, svc, dir, link, port, obfsType, r11ObfsPassword) {
		return
	}
	row := findPoolRow(t, svc, func(row map[string]any) bool {
		return str(row["protocol"]) == "hysteria2" && num(row["port"]) == int(port)
	})
	if row == nil || row["status"] != "unknown" {
		r.fail("R11-obfs", "%s pool row=%+v, want a usable unknown record", obfsType, row)
		return
	}
	svc.mustOK("rules", "add", "--kind", "domain", r11DestHost)
	svc.mustOK("pool", "select", str(row["id"]))
	if _, err := net.LookupHost(r11DestHost); err == nil {
		r.fail("R11-obfs", "the fixture destination resolves on the host; hostname preservation is not proven")
		return
	}
	r.pass("R11-obfs", "%s: the approved fixture host does not resolve on the host, so a tunnel proves the exit resolved it", obfsType)
	r11ObfsEcho(t, svc, echo, obfsType)
}

func r11ObfsNegative(t *testing.T, binary string, certs r11Certs, label, clientObfs, clientPassword string) {
	t.Helper()
	r := report{t: t}
	// The server is obfuscated with salamander and the real password; the
	// client carries the same shape but a wrong secret (or a different type),
	// so only the obfs authentication can be responsible for the failure.
	port := freePort(t)
	r11StartServerWithObfs(t, binary, port, certs, "salamander", r11ObfsPassword)
	echo := newEchoPeer(t)
	svc, dir := r11ObfsService(t, binary, certs)

	link := fmt.Sprintf("hysteria2://%s@127.0.0.1:%d?sni=%s&obfs=%s&obfs-password=%s#hy-obfs-mismatch",
		r11Password, port, r11SNI, clientObfs, clientPassword)
	if !r11ObfsImport(t, svc, dir, link, port, clientObfs, clientPassword) {
		return
	}
	row := findPoolRow(t, svc, func(row map[string]any) bool {
		return str(row["protocol"]) == "hysteria2" && num(row["port"]) == int(port)
	})
	if row == nil || row["status"] != "unknown" {
		r.fail("R11-obfs", "%s: pool row=%+v, want a usable unknown record (the sidecar accepts the config; only the handshake may fail)", label, row)
		return
	}
	svc.mustOK("rules", "add", "--kind", "domain", r11DestHost)
	svc.mustOK("pool", "select", str(row["id"]))

	before := echo.contacts()
	conn, br, code, reason, err := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo.port()))
	payload := "r11-obfs-mismatch-probe"
	out := ""
	var byteErr error
	if err == nil && code == 200 && conn != nil {
		_, _ = io.WriteString(conn, payload)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, len(payload))
		var n int
		n, byteErr = io.ReadFull(br, buf)
		out = string(buf[:n])
	}
	if conn != nil {
		_ = conn.Close()
	}
	time.Sleep(300 * time.Millisecond)
	contacts := echo.contacts()
	// A direct fallback of the unresolved fixture name would surface as the
	// frontend's own 502 "dns failure". A mismatched obfs handshake instead
	// expires the pool dial (504 "deadline exceeded") or lets the mixed handoff
	// reply 200 and then delivers no byte. Only those two shapes pass.
	poolTimeout := err == nil && code == 504 && strings.Contains(strings.ToLower(reason), "deadline exceeded")
	emptyHandoff := err == nil && code == 200 && out == "" && byteErr != nil
	switch {
	case contacts != before:
		r.fail("R11-obfs", "%s: the mismatched obfs handshake still contacted the destination (%d→%d)", label, before, contacts)
	case poolTimeout:
		r.pass("R11-obfs", "%s: client obfs=%s against server obfs=salamander expires the pool dial (HTTP 504 %q) with 0 destination contacts and no direct fallback",
			label, clientObfs, strings.TrimSpace(reason))
	case emptyHandoff:
		r.pass("R11-obfs", "%s: client obfs=%s against server obfs=salamander: the sidecar handoff replied 200 but delivered no payload (%v) with 0 destination contacts and no direct fallback",
			label, clientObfs, byteErr)
	default:
		r.fail("R11-obfs", "%s: the obfs mismatch failed outside the fail-closed shapes: HTTP %d (%q) err=%v payload=%q byteErr=%v contacts=%d→%d",
			label, code, strings.TrimSpace(reason), err, out, byteErr, before, contacts)
	}
	// The failed handshake must not take the service down; management stays
	// readable and the stored record stays visible.
	st := svc.mustOK("status").mapData(t)
	rows := svc.mustOK("pool", "list").list(t, "records")
	if str(st["mode"]) == "" || len(rows) == 0 {
		r.fail("R11-obfs", "%s: after the obfs handshake failure status=%+v rows=%d", label, st, len(rows))
		return
	}
	r.pass("R11-obfs", "%s: after the failed handshake the service stays up and readable (mode=%s, %d pool rows)", label, str(st["mode"]), len(rows))
}
