package acceptance

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// R11 acceptance evidence. The ordinary suite proves the import projection and
// the fail-closed exclusion with a local fake sidecar (no binary, no network).
// The real hysteria2 traffic cell is gated: it downloads the OFFICIAL sing-box
// v1.14.4 archive into a disposable /tmp/opencode directory, verifies the
// SHA-256 pinned in scripts/package.sh and packaging/README.md, extracts the
// binary there, and uses it ONLY as a loopback hysteria2 server and as the
// RouteLatch sidecar. It never installs or touches system state.
const (
	r11Tag      = "v1.14.4"
	r11Commit   = "c005f35300ce76427dc285481ec5695f5b179cd2"
	r11Archive  = "sing-box-1.14.4-linux-amd64.tar.gz"
	r11URL      = "https://github.com/SagerNet/sing-box/releases/download/v1.14.4/" + r11Archive
	r11Digest   = "699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968"
	r11Password = "r11-hy2-test-secret"
	r11SNI      = "hy.rl.test"
	r11DestHost = "hy-dest.rl.test"
	r11XrayHost = "hy-xray.rl.test"
)

// ---------------------------------------------------------------------------
// helpers: gated setup, official archive, local certificate, loopback server
// ---------------------------------------------------------------------------

func r11NotRun(t *testing.T, r report, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	r.note("R11", "real-traffic NOT RUN: %s", msg)
	t.Skip(msg)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// r11ArchiveBinary performs the controlled download and the pinned-digest
// check. Any failure is a documented not-run, never a fabricated pass.
func r11ArchiveBinary(t *testing.T, r report, work string) string {
	t.Helper()
	if supplied := strings.TrimSpace(os.Getenv("RL_ACCEPT_SINGBOX_EXEC")); supplied != "" {
		info, err := os.Stat(supplied)
		if err != nil || info.IsDir() {
			r11NotRun(t, r, "RL_ACCEPT_SINGBOX_EXEC=%q is not a usable file: %v", supplied, err)
		}
		version, _ := exec.Command(supplied, "version").CombinedOutput()
		if !strings.Contains(string(version), "1.14.4") || !strings.Contains(string(version), r11Commit) {
			r11NotRun(t, r, "RL_ACCEPT_SINGBOX_EXEC did not report tag 1.14.4 / commit %s: %s", r11Commit, strings.TrimSpace(string(version)))
		}
		sum, _ := sha256File(supplied)
		r.pass("R11", "caller-supplied sing-box binary sha256=%s reports tag 1.14.4 / commit %s", sum, r11Commit)
		return supplied
	}
	archive := filepath.Join(work, r11Archive)
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Get(r11URL)
	if err != nil {
		r11NotRun(t, r, "official archive download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r11NotRun(t, r, "official archive download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(archive)
	if err != nil {
		r11NotRun(t, r, "cannot write the archive: %v", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		r11NotRun(t, r, "archive download was truncated: %v", err)
	}
	_ = f.Close()

	got, err := sha256File(archive)
	if err != nil {
		r11NotRun(t, r, "cannot hash the archive: %v", err)
	}
	if got != r11Digest {
		r11NotRun(t, r, "archive sha256=%s, pinned authoritative digest=%s (refusing to use the binary)", got, r11Digest)
	}

	binary := filepath.Join(work, "sing-box")
	if err := r11Extract(archive, binary); err != nil {
		r11NotRun(t, r, "cannot extract sing-box from the verified archive: %v", err)
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		r11NotRun(t, r, "cannot make the extracted binary executable: %v", err)
	}
	version, _ := exec.Command(binary, "version").CombinedOutput()
	if !strings.Contains(string(version), "1.14.4") || !strings.Contains(string(version), r11Commit) {
		r11NotRun(t, r, "extracted binary did not report tag 1.14.4 / commit %s: %s", r11Commit, strings.TrimSpace(string(version)))
	}
	binSum, _ := sha256File(binary)
	r.pass("R11", "official v1.14.4 archive sha256=%s matches the pinned digest; extracted sing-box sha256=%s (commit %s)",
		got, binSum, r11Commit)
	return binary
}

func r11Extract(archive, binary string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("sing-box binary not found in archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) != "sing-box" || h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
}

type r11Certs struct {
	caPath   string
	certPath string
	keyPath  string
}

// r11MakeCerts builds a CA and a leaf valid for both the SNI and loopback. The
// CA is handed to the TEST process through SSL_CERT_FILE only; no system trust
// store is modified. Verification stays on in RouteLatch and in sing-box.
func r11MakeCerts(t *testing.T, dir string) r11Certs {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("r11 CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "RL R11 acceptance CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("r11 CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("r11 parse CA: %v", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("r11 leaf key: %v", err)
	}
	leaf := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: r11SNI},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{r11SNI},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("r11 leaf cert: %v", err)
	}
	write := func(name string, block *pem.Block) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatalf("r11 write %s: %v", name, err)
		}
		return path
	}
	return r11Certs{
		caPath:   write("ca.pem", &pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certPath: write("server.pem", &pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPath:  write("server.key", &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	}
}

type r11Server struct {
	cmd  *exec.Cmd
	logs *safeBuffer
	done chan struct{}
	err  error
	once sync.Once
}

// r11StartServer runs the verified binary as a loopback-only hysteria2 INBOUND
// server. Its DNS rule is a STATIC mapping of one fixture hostname to 127.0.0.1,
// so a successful tunnel proves RouteLatch sent the unresolved hostname.
func r11StartServer(t *testing.T, binary string, port uint16, certs r11Certs) *r11Server {
	t.Helper()
	return r11StartServerWithObfs(t, binary, port, certs, "", "")
}

// r11StartServerWithObfs is r11StartServer plus the inbound obfs block. sing-box
// 1.14.4 takes obfs as a top-level sibling of users/tls on the hysteria2
// inbound; obfsType "" leaves the inbound unobfuscated.
func r11StartServerWithObfs(t *testing.T, binary string, port uint16, certs r11Certs, obfsType, obfsPassword string) *r11Server {
	t.Helper()
	inbound := map[string]any{
		"type": "hysteria2", "tag": "hy-in", "listen": "127.0.0.1", "listen_port": port,
		"users": []any{map[string]any{"password": r11Password}},
		"tls": map[string]any{
			"enabled": true, "certificate_path": certs.certPath, "key_path": certs.keyPath,
		},
	}
	if obfsType != "" {
		inbound["obfs"] = map[string]any{"type": obfsType, "password": obfsPassword}
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{"rules": []any{map[string]any{
			"domain": []string{r11DestHost}, "action": "predefined",
			"answer": []string{r11DestHost + ". 3600 IN A 127.0.0.1"},
		}}},
		"inbounds":  []any{inbound},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("r11 server config: %v", err)
	}
	path := filepath.Join(filepath.Dir(certs.caPath), fmt.Sprintf("server-%d.json", port))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("r11 write server config: %v", err)
	}
	if out, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("r11 server config rejected: %v: %s\n%s", err, string(raw), string(out))
	}
	logs := &safeBuffer{}
	cmd := exec.Command(binary, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("r11 start server: %v", err)
	}
	s := &r11Server{cmd: cmd, logs: logs, done: make(chan struct{})}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	t.Cleanup(s.stop)
	// hysteria2 is QUIC/UDP: a loopback TCP dial would never answer, so the
	// readiness check is a short grace period plus a real process-exit check.
	// The real handshake below is the authoritative proof that the server is up.
	select {
	case <-s.done:
		t.Fatalf("r11 hysteria2 server exited during startup (config %s): %v: logs=%s", path, s.err, logs.String())
	case <-time.After(700 * time.Millisecond):
	}
	// A process that exits exactly at the grace boundary can lose the select
	// race; recheck so a bind failure is never mistaken for a live server.
	select {
	case <-s.done:
		t.Fatalf("r11 hysteria2 server exited during startup (config %s): %v: logs=%s", path, s.err, logs.String())
	default:
	}
	return s
}

func (s *r11Server) stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.once.Do(func() {
		_ = s.cmd.Process.Kill()
		<-s.done
	})
}

// r11ServerGone is a short settling period after Kill/Wait. hysteria2 listens
// over UDP, so there is no loopback TCP refusal to poll.
func r11ServerGone() { time.Sleep(700 * time.Millisecond) }

// ---------------------------------------------------------------------------
// gated real-traffic cell
// ---------------------------------------------------------------------------

func TestR11Hysteria2RealTraffic(t *testing.T) {
	if os.Getenv("RL_ACCEPT_HYSTERIA2_TEST") != "1" {
		t.Skip("set RL_ACCEPT_HYSTERIA2_TEST=1 for the real hysteria2 traffic cell (downloads a digest-verified sing-box into /tmp/opencode)")
	}
	r := report{t: t}
	work, err := os.MkdirTemp("/tmp/opencode", "rl-r11-hy2-")
	if err != nil {
		r11NotRun(t, r, "cannot create a disposable dir under /tmp/opencode: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	binary := r11ArchiveBinary(t, r, work)
	certs := r11MakeCerts(t, work)

	hysPort := freePort(t)
	server := r11StartServer(t, binary, hysPort, certs)
	echo := newEchoPeer(t)
	direct := newEchoPeer(t)

	env := newTestEnv(t)
	env.vars = append(env.vars,
		"SSL_CERT_FILE="+certs.caPath,
		"ROUTELATCH_SINGBOX_EXEC="+binary,
	)
	dir := filepath.Join(env.home, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("r11 data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 2000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })

	// Real import path: a hysteria2:// URI and a sing-box hysteria2 outbound.
	link := fmt.Sprintf("hysteria2://%s@127.0.0.1:%d?sni=%s#hy-exit", r11Password, hysPort, r11SNI)
	res := svc.mustOK("import", "link", link)
	if got := num(res.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "hysteria2:// import valid=%d, want 1", got)
	} else {
		r.pass("R11", "hysteria2:// link imported through the real CLI with TLS verification intact")
	}
	jsonPort := freePort(t)
	jsonPath := filepath.Join(work, "hy-out.json")
	jsonOut := fmt.Sprintf(`{"type":"hysteria2","server":"127.0.0.1","server_port":%d,"password":"%s","tls":{"enabled":true,"server_name":"%s","insecure":false}}`,
		jsonPort, r11Password, r11SNI)
	if err := os.WriteFile(jsonPath, []byte(jsonOut), 0o600); err != nil {
		t.Fatalf("r11 write outbound json: %v", err)
	}
	jres := svc.mustOK("import", "file", jsonPath)
	if got := num(jres.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "sing-box hysteria2 outbound import valid=%d, want 1", got)
	} else {
		r.pass("R11", "sing-box hysteria2 outbound JSON imported through the real CLI")
	}
	row := findPoolRow(t, svc, func(row map[string]any) bool {
		return str(row["protocol"]) == "hysteria2" && num(row["port"]) == int(hysPort)
	})
	if row == nil || row["status"] != "unknown" {
		r.fail("R11", "hysteria2 pool row=%+v, want a usable unknown record", row)
	} else {
		r.pass("R11", "the real sidecar accepted the exit (hysteria2 row usable, not sidecar_unavailable)")
	}
	svc.mustOK("rules", "add", "--kind", "domain", r11DestHost)
	svc.mustOK("pool", "select", str(row["id"]))
	if _, err := net.LookupHost(r11DestHost); err == nil {
		r.fail("R11", "the fixture destination resolves on the host; hostname preservation is not proven")
	} else {
		r.pass("R11", "fixture destination does not resolve on the host; a tunnel proves the exit resolved the hostname")
	}

	// This is the handshake proof: real TLS to a real hysteria2 server, a real
	// echo through it, and exactly one destination contact.
	payload := "r11-real-traffic-" + randomHex(t, 6)
	conn, br, code, reason, err := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo.port()))
	if err != nil || code != 200 {
		r.fail("R11", "approved CONNECT via the real hysteria2 sidecar: code=%d reason=%q err=%v", code, reason, err)
	} else {
		got, e := tunnelEcho(t, conn, br, payload)
		if e != nil || got != payload {
			r.fail("R11", "real hysteria2 tunnel echo: got=%q err=%v", got, e)
		} else if echo.contacts() != 1 {
			r.fail("R11", "destination contacts=%d, want exactly 1", echo.contacts())
		} else {
			r.pass("R11", "RouteLatch → real sing-box sidecar → hysteria2 server → echo peer returned %d bytes byte-identically (1 destination contact)", len(payload))
		}
	}
	// Status counts proofs on LIVE connections, so read it before closing.
	st := svc.mustOK("status").mapData(t)
	conns, _ := st["connections"].(map[string]any)
	proofs, _ := conns["proofs"].(map[string]any)
	if num(proofs["confirmed"]) < 1 {
		r.fail("R11", "no confirmed connection proof on the live real tunnel: %+v", proofs)
	} else {
		r.pass("R11", "status reports %d confirmed connection proof on the live real handshake", num(proofs["confirmed"]))
	}
	if conn != nil {
		_ = conn.Close()
	}

	// Fail-closed on a dead exit: no silent direct fallback, zero contacts.
	// The mixed handoff replies 0x00 before the hysteria2 dial completes, so a
	// dead exit can surface as CONNECT 200 followed by no bytes; the guarantee
	// that matters here is that no destination is ever contacted.
	server.stop()
	r11ServerGone()
	beforeDead := echo.contacts()
	dead, deadBr, deadCode, deadReason, deadErr := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo.port()))
	deadPayload := "r11-dead-exit-probe"
	deadOut := ""
	var deadByteErr error
	if deadErr == nil && deadCode == 200 && dead != nil {
		_, _ = io.WriteString(dead, deadPayload)
		_ = dead.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, len(deadPayload))
		var n int
		n, deadByteErr = io.ReadFull(deadBr, buf)
		deadOut = string(buf[:n])
	}
	if dead != nil {
		_ = dead.Close()
	}
	time.Sleep(300 * time.Millisecond)
	if echo.contacts() != beforeDead {
		r.fail("R11", "dead exit still contacted the destination (%d→%d)", beforeDead, echo.contacts())
	} else if deadErr != nil || deadCode != 200 {
		r.pass("R11", "a dead hysteria2 exit gives HTTP %d (%q) and 0 new destination contacts (no direct fallback)", deadCode, strings.TrimSpace(deadReason))
	} else if deadByteErr != nil || deadOut != deadPayload {
		r.note("R11", "a dead hysteria2 exit still returned CONNECT 200 but delivered no payload (%v); the 0x00 handoff reply is not a destination-connection proof", deadByteErr)
		r.pass("R11", "a dead hysteria2 exit delivers no byte and contacts no destination (0 new contacts, no direct fallback)")
	} else {
		r.fail("R11", "dead exit delivered a byte-identical payload: the hysteria2 server was not actually stopped")
	}
	_ = r11StartServer(t, binary, hysPort, certs)

	// An unapproved destination still goes direct and never reaches the pool.
	beforeUnapproved := echo.contacts()
	uconn, ubr, ucode, uerr := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", direct.port()))
	if uerr == nil && ucode == 200 {
		got, e := tunnelEcho(t, uconn, ubr, "r11-direct")
		if e != nil || got != "r11-direct" {
			r.fail("R11", "unapproved direct echo: got=%q err=%v", got, e)
		} else if echo.contacts() != beforeUnapproved {
			r.fail("R11", "unapproved destination reached the hysteria2 pool (%d→%d contacts)", beforeUnapproved, echo.contacts())
		} else {
			r.pass("R11", "an unapproved loopback destination still goes direct and never reaches the hysteria2 exit")
		}
	} else {
		r.fail("R11", "unapproved direct CONNECT: code=%d err=%v", ucode, uerr)
	}
	if uconn != nil {
		_ = uconn.Close()
	}

	// D21: a unique unapproved canary leaves no trace in state, the sidecar
	// config, the service streams or CLI output; the approved host never
	// appears in the streams or the generated sidecar config either.
	canary := "r11-canary-" + randomHex(t, 6) + ".rl.test"
	cconn, _, ccode, _, _ := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("%s:%d", canary, direct.port()))
	if cconn != nil {
		_ = cconn.Close()
	}
	time.Sleep(500 * time.Millisecond)
	sidecarCfg := filepath.Join(dir, "sidecar", "sing-box.json")
	statePath := filepath.Join(dir, "state.json")
	svc.mu.Lock()
	streams := svc.stdout.String() + svc.stderr.String()
	svc.mu.Unlock()
	rulesOut := svc.mustOK("rules", "list").stdout
	statusOut := svc.mustOK("status").stdout
	canaryLeak := fileContains(t, statePath, canary) || fileContains(t, sidecarCfg, canary) ||
		strings.Contains(streams, canary) || strings.Contains(rulesOut, canary) || strings.Contains(statusOut, canary)
	if ccode != 502 && ccode != 504 {
		r.fail("R11", "unapproved canary direct request returned %d, want a pre-reply direct error", ccode)
	} else if canaryLeak {
		r.fail("R11", "the unapproved canary leaked into state, sidecar config, streams or CLI output")
	} else {
		r.pass("R11", "D21: the unapproved canary appears in no state, sidecar config, service stream or CLI output")
	}
	if strings.Contains(streams, r11DestHost) || fileContains(t, sidecarCfg, r11DestHost) {
		r.fail("R11", "the approved destination hostname leaked into the service streams or the generated sidecar config")
	} else {
		r.pass("R11", "D21: the destination hostname is absent from service streams and the generated sidecar config")
	}

	// Absent executable: reload the same state with no sidecar configured. The
	// hysteria2 exit is excluded with reason=sidecar_unavailable; Xray is not.
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("r11 read state: %v", err)
	}
	_ = svc.stop()

	env2 := newTestEnv(t)
	dir2 := filepath.Join(env2.home, "data")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatalf("r11 data dir2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "state.json"), stateBytes, 0o600); err != nil {
		t.Fatalf("r11 copy state: %v", err)
	}
	svc2 := startService(t, env2, dir2, serviceConfig{})
	t.Cleanup(func() { _ = svc2.stop() })
	excluded := findPoolRow(t, svc2, func(row map[string]any) bool { return str(row["protocol"]) == "hysteria2" })
	if excluded == nil || str(excluded["status"]) != "unsupported" || str(excluded["status_reason"]) != "sidecar_unavailable" {
		r.fail("R11", "no-sidecar hysteria2 row=%+v, want unsupported/sidecar_unavailable", excluded)
	} else {
		r.pass("R11", "with no executable configured the stored hysteria2 exit is unsupported/sidecar_unavailable")
	}
	echo2 := newEchoPeer(t)
	beforeNoSide := echo2.contacts()
	ns, _, nsCode, nsReason, nsErr := connectHTTPStatus(t, svc2.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo2.port()))
	if ns != nil {
		_ = ns.Close()
	}
	time.Sleep(300 * time.Millisecond)
	if nsErr == nil && nsCode == 502 && echo2.contacts() == beforeNoSide {
		r.pass("R11", "with no sidecar the approved hysteria2 destination is HTTP 502 and never goes direct")
	} else {
		r.fail("R11", "no-sidecar approved destination: code=%d reason=%q err=%v contacts %d→%d", nsCode, nsReason, nsErr, beforeNoSide, echo2.contacts())
	}
	socks := newSOCKSExit(t)
	xres := svc2.mustOK("import", "link", fmt.Sprintf("socks5://127.0.0.1:%d#xray-exit", socks.port()))
	if got := num(xres.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "Xray (SOCKS5) import with no sidecar valid=%d, want 1", got)
	} else {
		svc2.mustOK("rules", "add", "--kind", "domain", r11XrayHost)
		xrow := findRecord(t, svc2.mustOK("pool", "list"), "socks5")
		svc2.mustOK("pool", "select", str(xrow["id"]))
		xc, xbr, xcode, xerr := connectHTTP(t, svc2.httpAddr, fmt.Sprintf("%s:%d", r11XrayHost, echo2.port()))
		if xerr == nil && xcode == 200 {
			got, e := tunnelEcho(t, xc, xbr, "r11-xray")
			if e == nil && got == "r11-xray" {
				r.pass("R11", "Xray exits remain usable when the hysteria2 sidecar is absent")
			} else {
				r.fail("R11", "Xray tunnel echo: got=%q err=%v", got, e)
			}
		} else {
			r.fail("R11", "Xray tunnel CONNECT: code=%d err=%v", xcode, xerr)
		}
		if xc != nil {
			_ = xc.Close()
		}
	}
}

// ---------------------------------------------------------------------------
// default-on fake-peer cell: import shape, handoff config, fail-closed
// ---------------------------------------------------------------------------

// TestR11HelperFakeSidecar is this test binary re-executed as the second
// executable. It parses the generated config, writes a copy to a marker, and
// listens on the generated handoff port so the runtime's readiness probe
// succeeds. It proves the handoff shape without a real hysteria2 binary.
func TestR11HelperFakeSidecar(t *testing.T) {
	if os.Getenv("RL_ACCEPT_EXIT_HELPER") != "1" || os.Getenv("RL_ACCEPT_R11_SIDECAR_HELPER") != "1" {
		t.Skip("fake sidecar helper process only")
	}
	raw, err := os.ReadFile(os.Getenv("RL_R11_FAKE_CONFIG"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake sidecar: read config: %v\n", err)
		os.Exit(2)
	}
	if marker := os.Getenv("RL_R11_FAKE_MARKER"); marker != "" {
		_ = os.WriteFile(marker, raw, 0o600)
	}
	var cfg struct {
		Inbounds []struct {
			ListenPort uint16 `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Inbounds) == 0 {
		fmt.Fprintf(os.Stderr, "fake sidecar: bad config: %v\n", err)
		os.Exit(2)
	}
	for _, in := range cfg.Inbounds {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", in.ListenPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake sidecar: listen: %v\n", err)
			os.Exit(2)
		}
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close() }()
			}
		}(ln)
	}
	select {} // the runtime owns this process's lifetime
}

func r11FakeWrapper(t *testing.T) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("r11 executable: %v", err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "handoff.json")
	wrapper := filepath.Join(dir, "sing-box-fake")
	script := fmt.Sprintf("#!/bin/sh\nRL_ACCEPT_EXIT_HELPER=1\nexport RL_ACCEPT_EXIT_HELPER RL_ACCEPT_R11_SIDECAR_HELPER=1 RL_R11_FAKE_CONFIG=\"$3\" RL_R11_FAKE_MARKER=%q\nexec %q -test.run=^TestR11HelperFakeSidecar$ -test.v=false\n",
		marker, exe)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("r11 write wrapper: %v", err)
	}
	return wrapper, marker
}

func TestR11Hysteria2ImportAndFailClosed(t *testing.T) {
	r := report{t: t}
	wrapper, marker := r11FakeWrapper(t)

	env := newTestEnv(t)
	env.vars = append(env.vars, "ROUTELATCH_SINGBOX_EXEC="+wrapper)
	dir := filepath.Join(env.home, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("r11 data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	hysPort := freePort(t)
	link := fmt.Sprintf("hysteria2://%s@127.0.0.1:%d?sni=%s#hy-fake", r11Password, hysPort, r11SNI)
	res := svc.mustOK("import", "link", link)
	if got := num(res.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "fake-sidecar hysteria2 import valid=%d, want 1", got)
	} else {
		r.pass("R11", "hysteria2:// link imported with a local fake sidecar (no binary, no network)")
	}
	jsonPath := filepath.Join(t.TempDir(), "hy-out.json")
	jsonOut := fmt.Sprintf(`{"type":"hysteria2","server":"127.0.0.1","server_port":%d,"password":"%s","tls":{"enabled":true,"server_name":"%s","insecure":false}}`,
		freePort(t), r11Password, r11SNI)
	if err := os.WriteFile(jsonPath, []byte(jsonOut), 0o600); err != nil {
		t.Fatalf("r11 write outbound json: %v", err)
	}
	if got := num(svc.mustOK("import", "file", jsonPath).mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "sing-box outbound JSON import valid=%d, want 1", got)
	} else {
		r.pass("R11", "sing-box hysteria2 outbound JSON imported through the real CLI")
	}
	if !waitFor(5*time.Second, func() bool {
		raw, err := os.ReadFile(marker)
		return err == nil && len(raw) > 0
	}) {
		t.Fatal("fake sidecar never received a generated config")
	}
	handoff, _ := os.ReadFile(marker)
	shapeOK := strings.Contains(string(handoff), `"type": "hysteria2"`) &&
		strings.Contains(string(handoff), `"insecure": false`) &&
		!strings.Contains(string(handoff), `"insecure": true`) &&
		strings.Contains(string(handoff), `"final": "rl-block"`) &&
		!strings.Contains(string(handoff), `"resolve"`) &&
		strings.Contains(string(handoff), fmt.Sprintf("%d", hysPort))
	if shapeOK {
		r.pass("R11", "handoff config pins one hysteria2 outbound with verification on, no resolve rules and a block final")
	} else {
		r.fail("R11", "handoff config did not preserve the D-item shape: %s", string(handoff))
	}
	// D-item: a skip-verify hysteria2 link is quarantined, never dialable.
	insecureLink := fmt.Sprintf("hysteria2://pw@127.0.0.1:%d?insecure=1#no-verify", hysPort)
	insecurePath := filepath.Join(t.TempDir(), "insecure.txt")
	insecurePayload := insecureLink + "\n" + fmt.Sprintf("hysteria2://pw2@127.0.0.1:%d?sni=%s#valid-too\n", freePort(t), r11SNI)
	if err := os.WriteFile(insecurePath, []byte(insecurePayload), 0o600); err != nil {
		t.Fatalf("r11 write insecure fixture: %v", err)
	}
	ires := svc.mustOK("import", "file", insecurePath)
	icounts, _ := ires.mapData(t)["records"].(map[string]any)
	iquar := findPoolRow(t, svc, func(row map[string]any) bool {
		return str(row["status_reason"]) == "insecure_tls_removed"
	})
	if num(icounts["unsupported"]) == 1 && iquar != nil && str(iquar["status"]) == "unsupported" {
		r.pass("R11", "skip-verify hysteria2 link is quarantined as unsupported/insecure_tls_removed")
	} else {
		r.fail("R11", "insecure hysteria2 quarantine: counts=%+v row=%+v", icounts, iquar)
	}
	stateBytes, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("r11 read state: %v", err)
	}
	_ = svc.stop()

	// Reload the same state with no executable configured: hysteria2 is
	// excluded with the documented reason; Xray still dials.
	env2 := newTestEnv(t)
	dir2 := filepath.Join(env2.home, "data")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatalf("r11 data dir2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "state.json"), stateBytes, 0o600); err != nil {
		t.Fatalf("r11 copy state: %v", err)
	}
	svc2 := startService(t, env2, dir2, serviceConfig{})
	t.Cleanup(func() { _ = svc2.stop() })
	excluded := findPoolRow(t, svc2, func(row map[string]any) bool {
		return str(row["protocol"]) == "hysteria2" && num(row["port"]) == int(hysPort)
	})
	if excluded == nil || str(excluded["status"]) != "unsupported" || str(excluded["status_reason"]) != "sidecar_unavailable" {
		r.fail("R11", "no-sidecar hysteria2 row=%+v", excluded)
	} else {
		r.pass("R11", "no executable → hysteria2 unsupported/sidecar_unavailable")
	}
	echo := newEchoPeer(t)
	svc2.mustOK("rules", "add", "--kind", "domain", r11DestHost)
	ns, _, nsCode, nsReason, nsErr := connectHTTPStatus(t, svc2.httpAddr, fmt.Sprintf("%s:%d", r11DestHost, echo.port()))
	if ns != nil {
		_ = ns.Close()
	}
	if nsErr == nil && nsCode == 502 && echo.contacts() == 0 {
		r.pass("R11", "approved hysteria2 destination fails 502 (%s) with 0 destination contacts and no direct fallback", strings.TrimSpace(nsReason))
	} else {
		r.fail("R11", "no-sidecar approved destination: code=%d reason=%q err=%v contacts=%d", nsCode, nsReason, nsErr, echo.contacts())
	}
	socks := newSOCKSExit(t)
	if got := num(svc2.mustOK("import", "link", fmt.Sprintf("socks5://127.0.0.1:%d#xray", socks.port())).mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R11", "Xray SOCKS5 import valid=%d, want 1", got)
	} else {
		r.pass("R11", "Xray SOCKS5 record still imports when the sidecar is absent")
	}
}
