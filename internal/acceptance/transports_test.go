package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// transportCell is one R6 matrix row: a TLS inbound on the pinned core and the
// matching imported link. ALPN is explicit because gRPC/XHTTP speak HTTP/2 and
// WS speaks HTTP/1.1; a shared default would negotiate the wrong protocol.
type transportCell struct {
	name    string
	network string
	host    string // wsSettings.host; the exit has no xhttp host check
	path    string
	service string
	alpn    string
}

// TestTransportPeers moves WS, gRPC and XHTTP out of "config-only": each cell
// starts a real pinned-core exit child, imports a certificate-pinned link, adds
// the approved rule, selects the record and carries an HTTP request through the
// product's CONNECT listener to a loopback destination. A flipped pin must not
// deliver data to the same destination.
func TestTransportPeers(t *testing.T) {
	cells := []transportCell{
		{name: "WS", network: "ws", host: "ws.rl.test", path: "/r6-ws", alpn: "http/1.1"},
		{name: "GRPC", network: "grpc", host: "grpc.rl.test", path: "", service: "r6-grpc", alpn: "h2"},
		{name: "XHTTP", network: "xhttp", host: "", path: "/r6-xhttp", alpn: "h2"},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) { runTransportCell(t, cell) })
	}
}

func runTransportCell(t *testing.T, cell transportCell) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "transport")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}

	dest := newHTTPPeer(t, "r6-"+strings.ToLower(cell.name)+"-body")
	uuid := randomUUID(t)
	exitPort := freePort(t)
	certName := strings.ToLower(cell.name) + ".rl.test"
	destHost := strings.ToLower(cell.name) + "-dest.rl.test"
	certPath, keyPath, pin := selfSignedPin(t, certName)
	exit := startExit(t, exitSpec{
		Protocol: "vless", Port: int(exitPort), UUID: uuid,
		Hosts:       map[string]string{destHost: "127.0.0.1"},
		TLSCertFile: certPath, TLSKeyFile: keyPath,
		Transport: cell.network, Host: cell.host, Path: cell.path,
		GRPCService: cell.service, ALPN: []string{cell.alpn},
	})
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("rules", "add", "--kind", "domain", destHost)

	linkFor := func(pinValue, name string) string {
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("security", "tls")
		q.Set("type", cell.network)
		q.Set("sni", certName)
		q.Set("alpn", cell.alpn)
		q.Set("pinSHA256", pinValue)
		if cell.host != "" {
			q.Set("host", cell.host)
		}
		if cell.path != "" {
			q.Set("path", cell.path)
		}
		if cell.service != "" {
			q.Set("serviceName", cell.service)
		}
		if cell.network == "xhttp" {
			q.Set("mode", "auto")
		}
		u := url.URL{
			Scheme:   "vless",
			User:     url.User(uuid),
			Host:     "127.0.0.1:" + strconv.Itoa(int(exitPort)),
			RawQuery: q.Encode(),
			Fragment: name,
		}
		return u.String()
	}

	// openDestination walks the real path: CONNECT through the product frontend,
	// then one HTTP/1.1 request whose status, header and body the destination
	// peer fixes. A tunnel that only returns 200 without bytes fails here.
	openDestination := func(label string, timeout time.Duration) (bool, string) {
		conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("%s:%d", destHost, dest.port()))
		if conn != nil {
			defer conn.Close()
		}
		if err != nil || code != 200 {
			return false, fmt.Sprintf("CONNECT code=%d err=%v", code, err)
		}
		_ = conn.SetDeadline(time.Now().Add(timeout))
		reqText := fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", label, destHost)
		if _, err := io.WriteString(conn, reqText); err != nil {
			return false, fmt.Sprintf("write request: %v", err)
		}
		req, _ := http.NewRequest("GET", "http://"+destHost+"/", nil)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			return false, fmt.Sprintf("read response: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, fmt.Sprintf("read body: %v", err)
		}
		if resp.StatusCode != http.StatusMultiStatus {
			return false, fmt.Sprintf("status=%d, want 207", resp.StatusCode)
		}
		if got := resp.Header.Get("X-RL-Destination"); got != "loopback-fixture" {
			return false, fmt.Sprintf("X-RL-Destination=%q, want the destination header", got)
		}
		if string(body) != dest.body {
			return false, fmt.Sprintf("body=%q, want %q", string(body), dest.body)
		}
		return true, fmt.Sprintf("HTTP %d, %d body bytes, destination header intact", resp.StatusCode, len(body))
	}

	imported := svc.mustOK("import", "link", linkFor(pin, "r6-"+cell.name))
	counts, _ := imported.mapData(t)["records"].(map[string]any)
	if num(counts["valid"]) != 1 || num(counts["invalid"]) != 0 || num(counts["unsupported"]) != 0 {
		r.fail("R6", "%s/TLS pinned link import counts=%+v, want 1/0/0", cell.name, counts)
		t.FailNow()
	}
	rows := svc.mustOK("pool", "list").list(t, "records")
	if len(rows) != 1 {
		r.fail("R6", "%s/TLS import produced %d records, want 1", cell.name, len(rows))
		t.FailNow()
	}
	if got := str(rows[0]["transport"]); got != cell.network {
		r.fail("R6", "%s/TLS record transport=%q, want %q", cell.name, got, cell.network)
		t.FailNow()
	}
	if got := str(rows[0]["security"]); got != "tls" {
		r.fail("R6", "%s/TLS record security=%q, want tls", cell.name, got)
		t.FailNow()
	}
	goodID := str(rows[0]["id"])
	svc.mustOK("pool", "select", goodID)
	if ok, detail := openDestination("r6-"+strings.ToLower(cell.name), 20*time.Second); !ok {
		r.fail("R6", "%s/TLS pinned tunnel failed: %s (exit logs: %s)", cell.name, detail, exit.Logs())
		t.FailNow()
	} else if got := dest.requests.Load(); got != 1 {
		r.fail("R6", "%s/TLS tunnel delivered %d destination requests, want exactly 1", cell.name, got)
		t.FailNow()
	} else {
		r.pass("R6", "%s/TLS: pinned link imported, rule + pool select, CONNECT delivered to %s (%s), exactly 1 destination request",
			cell.name, destHost, detail)
	}

	// Negative: the same link with one flipped pin digit must not deliver data.
	// It is measured at the destination, not at CONNECT: the core dial is lazy,
	// so a 200 alone does not prove a byte arrived (same rule as D18).
	wrong := svc.mustOK("import", "link", linkFor(flipHex(pin), "r6-"+cell.name+"-wrong-pin"))
	if got := num(wrong.mapData(t)["records"].(map[string]any)["valid"]); got != 1 {
		r.fail("R6", "%s/TLS wrong-pin link import valid=%d, want a stored record", cell.name, got)
		t.FailNow()
	}
	rows = svc.mustOK("pool", "list").list(t, "records")
	var wrongID string
	for _, row := range rows {
		if id := str(row["id"]); id != goodID {
			wrongID = id
		}
	}
	if wrongID == "" || len(rows) != 2 {
		r.fail("R6", "%s/TLS wrong-pin import did not create a distinct record (rows=%d)", cell.name, len(rows))
		t.FailNow()
	}
	svc.mustOK("pool", "select", wrongID)
	before := dest.requests.Load()
	ok, detail := openDestination("wrong-pin", 5*time.Second)
	if ok {
		r.fail("R6", "%s/TLS a wrong certificate pin delivered destination data (%s)", cell.name, detail)
	} else if got := dest.requests.Load(); got != before {
		r.fail("R6", "%s/TLS the wrong-pin attempt reached the destination (%d new requests)", cell.name, got-before)
	} else if _, up := svc.statusNow(); !up {
		r.fail("R6", "%s/TLS status stopped answering after the wrong-pin failure", cell.name)
	} else {
		// A failed attempt must not leave the engine unusable; this also keeps
		// the negative from passing on a merely stuck service. The failure text
		// above stays "no destination data" because a product-side refusal is
		// still a correct non-pass, not proof of the exact pin error.
		svc.mustOK("pool", "select", goodID)
		goodAgain, goodDetail := openDestination("after-wrong-pin", 10*time.Second)
		if !goodAgain {
			r.fail("R6", "%s/TLS the good pinned exit no longer tunnels after the wrong-pin attempt: %s", cell.name, goodDetail)
		} else {
			r.pass("R6", "%s/TLS: a wrong pin delivers no destination data (%s, 0 new requests), the service stays up, and the good pin still tunnels (%s)",
				cell.name, detail, goodDetail)
		}
	}
}
