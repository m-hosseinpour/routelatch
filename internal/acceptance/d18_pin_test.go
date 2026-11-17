package acceptance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA06PinnedTLS covers D18 (§5.5): a certificate pin is a constraint with
// verification on, not a consent path for skip-verify TLS. The checks are:
// a pinned link imports and is usable, the same link with allowInsecure is
// accepted (the insecure switch is ignored, verification stays on), a wrong pin
// cannot complete the handshake, and none of this leaves a record that stops
// the service from starting again.
func TestA06PinnedTLS(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "pin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	echo := newEchoPeer(t)
	uuid := randomUUID(t)
	port := freePort(t)
	certPath, keyPath, pin := selfSignedPin(t, "pin.rl.test")
	startExit(t, exitSpec{
		Protocol: "vless", Port: int(port), UUID: uuid,
		Hosts:       map[string]string{"pin-dest.rl.test": "127.0.0.1"},
		TLSCertFile: certPath, TLSKeyFile: keyPath,
	})
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("rules", "add", "--kind", "domain", "pin-dest.rl.test")

	link := func(pinValue, extra, name string) string {
		return fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=tls&type=tcp&sni=pin.rl.test&pinSHA256=%s%s#%s",
			uuid, port, pinValue, extra, name)
	}
	openTunnel := func(label string) (bool, string) {
		conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("pin-dest.rl.test:%d", echo.port()))
		if conn != nil {
			defer conn.Close()
		}
		if err != nil || code != 200 {
			return false, fmt.Sprintf("CONNECT code=%d err=%v", code, err)
		}
		got, e := tunnelEcho(t, conn, br, label)
		if e != nil || got != label {
			return false, fmt.Sprintf("echo got=%q err=%v", got, e)
		}
		return true, ""
	}

	// A pin-only link imports and drives a real TLS tunnel, with the exit
	// resolving the destination hostname.
	imported := svc.mustOK("import", "link", link(pin, "", "pinned"))
	counts, _ := imported.mapData(t)["records"].(map[string]any)
	if num(counts["valid"]) != 1 || num(counts["invalid"]) != 0 || num(counts["unsupported"]) != 0 {
		r.fail("A06", "pin-only import counts=%+v, want 1/0/0", counts)
		t.FailNow()
	}
	r.pass("A06", "a certificate-pinned VLESS link imports as valid (verification on, no skip-verify)")
	rows := svc.mustOK("pool", "list").list(t, "records")
	if len(rows) != 1 || str(rows[0]["status"]) != "unknown" {
		r.fail("A06", "pinned record row=%+v, want one unknown record", rows)
		t.FailNow()
	}
	goodID := str(rows[0]["id"])
	svc.mustOK("pool", "select", goodID)
	if ok, why := openTunnel("pinned-echo"); ok {
		r.pass("A06", "the pinned TLS exit completes a TLS handshake and carries the tunnel (pin matches the exit certificate)")
	} else {
		r.fail("A06", "pinned TLS tunnel: %s", why)
		t.FailNow()
	}

	// A different (valid-format) pin must fail the handshake: the pin is an
	// exact-certificate constraint, not "verification off".
	wrongPin := flipHex(pin)
	wrong := svc.cli("import", "link", link(wrongPin, "", "wrong-pin"))
	if wrong.code != 0 {
		r.fail("A06", "wrong-pin link import: exit=%d kind=%q message=%q, want a stored record so the pin can be tested",
			wrong.code, errorKind(wrong), errorMessage(wrong))
	} else {
		rows := svc.mustOK("pool", "list").list(t, "records")
		var wrongID string
		for _, row := range rows {
			if id := str(row["id"]); id != goodID {
				wrongID = id
			}
		}
		if wrongID == "" || len(rows) != 2 {
			r.fail("A06", "wrong-pin import did not create a distinct record (rows=%d)", len(rows))
		} else {
			selected := svc.mustOK("pool", "select", wrongID)
			if str(selected.mapData(t)["selected"]) != wrongID {
				r.fail("A06", "pool select did not pin the wrong-pin record (%q)", str(selected.mapData(t)["selected"]))
			}
			contacts := echo.contacts()
			conn, br, code, _ := connectHTTP(t, svc.httpAddr, fmt.Sprintf("pin-dest.rl.test:%d", echo.port()))
			delivered := false
			if conn != nil {
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if code == 200 {
					// The core dial is lazy: a 200 only means a session was
					// built. The pin failure must surface on the first byte.
					got, e := tunnelEcho(t, conn, br, "wrong-pin")
					delivered = e == nil && got == "wrong-pin"
				}
				_ = conn.Close()
			}
			if delivered {
				r.fail("A06", "a wrong certificate pin still carried destination data (CONNECT %d): verification was skipped", code)
			} else if echo.contacts() != contacts {
				r.fail("A06", "the wrong-pin handshake reached the destination (%d contacts)", echo.contacts()-contacts)
			} else if code != 200 {
				r.pass("A06", "a wrong pin fails before the destination (CONNECT %d, 0 destination contacts) while the service stays up", code)
			} else {
				r.pass("A06", "a wrong pin fails the TLS handshake on the first byte (CONNECT 200 but no destination data, 0 contacts)")
			}
			// The failed pin must not affect other traffic or management.
			if _, ok := svc.statusNow(); !ok {
				r.fail("A06", "status stopped answering after the wrong-pin failure")
			}
			svc.mustOK("pool", "select", goodID)
			if ok, why := openTunnel("after-wrong-pin"); ok {
				r.pass("A06", "the good pinned exit still tunnels after the wrong-pin failure")
			} else {
				r.fail("A06", "tunnel after the wrong-pin failure: %s", why)
			}
		}
	}

	// §5.5: the same link with allowInsecure must still be accepted, because the
	// pin keeps verification on; the import reports that allowInsecure was
	// ignored. This is the D18 behaviour under test, not an optional notice.
	insecure := svc.cli("import", "link", link(pin, "&allowInsecure=1", "pinned"))
	if insecure.code != 0 || !insecure.env.OK {
		r.fail("A06", "pinned link with allowInsecure=1: exit=%d kind=%q message=%q; §5.5 requires acceptance (the pin keeps verification on) with an allow_insecure_ignored notice",
			insecure.code, errorKind(insecure), errorMessage(insecure))
	} else {
		notices := insecure.mapData(t)["notices"]
		if !noticesContain(notices, "allow_insecure_ignored") {
			r.fail("A06", "pinned+allowInsecure import is accepted but reports no allow_insecure_ignored notice: %v", notices)
		} else if rows := svc.mustOK("pool", "list").list(t, "records"); len(rows) != 2 {
			r.fail("A06", "pinned+allowInsecure import changed the record set (%d rows, want the same 2 identities)", len(rows))
		} else {
			r.pass("A06", "pinned+allowInsecure imports as accepted with the allow_insecure_ignored notice and dedupes to the same record (verification on)")
		}
		if ok, why := openTunnel("insecure-ignored"); ok {
			r.pass("A06", "the allowInsecure variant is usable: the pin, not the insecure switch, authorises the handshake")
		} else {
			r.fail("A06", "the allowInsecure variant is not usable after import: %s", why)
		}
	}

	// Whatever happened above, a restart on the same data dir must still start
	// and serve: no import may leave a record that bricks the service (D18).
	if err := svc.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	restarted := startService(t, env, dir, svc.cfg)
	t.Cleanup(func() { _ = restarted.stop() })
	restarted.mustOK("pool", "select", goodID)
	if ok, why := openTunnel("after-restart"); ok {
		r.pass("A06", "the service restarts on the same state and the pinned exit still tunnels (no record bricks startup)")
	} else {
		r.fail("A06", "pinned record after restart: %s", why)
	}
}

// selfSignedPin writes a self-signed TLS certificate/key pair for the exit and
// returns the SHA-256 pin of its DER leaf.
func selfSignedPin(t *testing.T, name string) (certPath, keyPath, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{name},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sum := sha256.Sum256(der)
	return certPath, keyPath, hex.EncodeToString(sum[:])
}

// flipHex changes one hex digit so the pin stays well formed but names no
// certificate the exit can present.
func flipHex(s string) string {
	last := s[len(s)-1]
	if last == '0' {
		return s[:len(s)-1] + "1"
	}
	return s[:len(s)-1] + "0"
}

func noticesContain(raw any, want string) bool {
	list, _ := raw.([]any)
	for _, item := range list {
		if strings.Contains(str(item), want) {
			return true
		}
	}
	return false
}
