package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEndToEnd drives the whole T7 user flow against one product process:
// import → approved destinations → traffic and counters → diagnostics and
// proposals → mode changes → stop. Every assertion goes through the CLI or the
// two loopback traffic listeners.
func TestEndToEnd(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	fx := newFixtureSet(t)
	sub := newSubscriptionServer(t, fx, "sub-token-"+randomHex(t, 10))
	svc := startService(t, env, dir, serviceConfig{
		Diagnostics: map[string]any{"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30},
		Probes:      map[string]any{"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 3, "cooldown_max": 10},
	})
	t.Cleanup(func() { _ = svc.stop() })

	assertLoopbackListeners(t, r, svc)
	assertExternalLoopback(t, r, svc)

	echo := newEchoPeer(t)
	directEcho := newEchoPeer(t)
	httpDest := newHTTPPeer(t, "rl-body-0123456789")
	greeter := newGreeterPeer(t, "g")

	statePath := filepath.Join(dir, "state.json")

	// ---------------------------------------------------------------------
	// A06 / A02: import the file and the subscription, add four rule kinds.
	// ---------------------------------------------------------------------
	res := svc.mustOK("import", "file", fx.filePath)
	if got := num(res.mapData(t)["records"].(map[string]any)["valid"]); got != 3 {
		r.fail("A06", "file import valid=%d, want 3", got)
	} else {
		r.pass("A06", "file import of VLESS+Trojan+SOCKS5: 3 valid, 0 invalid, 0 unsupported")
	}
	pool := svc.mustOK("pool", "list")
	poolRows := pool.list(t, "records")
	usable := 0
	for _, row := range poolRows {
		switch str(row["status"]) {
		case "unknown", "healthy":
			usable++
		}
	}
	if len(poolRows) != 3 || usable != 3 {
		r.fail("A06", "pool list has %d records (%d usable), want 3/3", len(poolRows), usable)
	} else {
		r.pass("A06", "pool lists 3 usable records (vless, trojan, socks5)")
	}
	socksRow := findRecord(t, pool, "socks5")
	vlessRow := findRecord(t, pool, "vless")
	_ = findRecord(t, pool, "trojan")

	subRes := svc.mustOK("import", "subscription", sub.url)
	sub.setSource(str(subRes.mapData(t)["source_id"]))
	if got := num(subRes.mapData(t)["records"].(map[string]any)["valid"]); got != 3 {
		r.fail("A06", "subscription import valid=%d, want 3", got)
	} else {
		r.pass("A06", "subscription import served the same 3 links (dedupe merge)")
	}
	afterSub := svc.mustOK("pool", "list")
	if got := len(afterSub.list(t, "records")); got != 3 {
		r.fail("A06", "dedupe produced %d records, want 3", got)
	} else {
		r.pass("A06", "duplicate links across file and subscription merged into 3 records")
	}

	rulesBase := []struct {
		kind, operand string
	}{
		{"domain", "exact.rl.test"},
		{"domain_suffix", "suffix.rl.test"},
		{"ip", "2001:db8::10"},
		{"cidr", "203.0.113.0/24"},
	}
	for _, rule := range rulesBase {
		svc.mustOK("rules", "add", "--kind", rule.kind, rule.operand)
	}
	rules := svc.mustOK("rules", "list").list(t, "rules")
	gotKinds := map[string]string{}
	gotOrder := make([]string, 0, len(rules))
	for _, rule := range rules {
		operand := str(rule["host"]) + str(rule["addr"]) + str(rule["prefix"])
		gotKinds[str(rule["kind"])] = operand
		gotOrder = append(gotOrder, operand)
	}
	// Stored order is newest first, so the last approval is listed first.
	wantOrder := []string{"203.0.113.0/24", "2001:db8::10", "suffix.rl.test", "exact.rl.test"}
	okRules := len(rules) == 4 &&
		strings.Join(gotOrder, ",") == strings.Join(wantOrder, ",") &&
		gotKinds["domain"] == "exact.rl.test" &&
		gotKinds["domain_suffix"] == "suffix.rl.test" &&
		gotKinds["ip"] == "2001:db8::10" &&
		gotKinds["cidr"] == "203.0.113.0/24"
	if !okRules {
		r.fail("A02", "rules list=%+v order=%v, want the four accepted kinds newest first", gotKinds, gotOrder)
	} else {
		r.pass("A02", "exact domain, domain suffix, IP and CIDR rules accepted and listed newest first")
	}

	// A rule that would widen scope with a port is rejected instead of being
	// silently broadened (§1.1).
	portRule := svc.cli("rules", "add", "--kind", "domain", "exact.rl.test:443")
	if refusedWith(portRule, 2, 6, 7) && len(svc.mustOK("rules", "list").list(t, "rules")) == 4 {
		r.pass("A02", "rules add refuses a port-bearing rule operand (exit %d, %s) and stores nothing", portRule.code, errorKind(portRule))
	} else {
		r.fail("A02", "rules add of a port-bearing operand: exit=%d env=%+v", portRule.code, portRule.env)
	}
	// A local IP is not a rule operand (§1.6).
	localRule := svc.cli("rules", "add", "--kind", "ip", "127.0.0.1")
	if refusedWith(localRule, 2, 6, 7) && len(svc.mustOK("rules", "list").list(t, "rules")) == 4 {
		r.pass("A02", "rules add refuses a loopback IP operand (exit %d) and stores nothing", localRule.code)
	} else {
		r.fail("A02", "rules add of 127.0.0.1: exit=%d, want a documented refusal", localRule.code)
	}

	// The host resolver cannot resolve the fixture domains. A successful
	// tunnel therefore proves the hostname reached the exit unresolved.
	if _, err := net.LookupHost("vless.rl.test"); err == nil {
		r.fail("A08", "the host unexpectedly resolves vless.rl.test; the fixture is not isolated")
	} else {
		r.pass("A08", "fixture destination domains do not resolve on the host (exit-side resolution is required)")
	}
	egress := svc.cli("pool", "test", "--exit", str(vlessRow["id"]), "--target", fmt.Sprintf("vless.rl.test:%d", greeter.port()))
	egressOK := egress.code == 0 &&
		waitFor(3*time.Second, func() bool { return greeter.contacts.Load() == 1 })
	if egressOK {
		rows := egress.list(t, "results")
		egressOK = len(rows) == 1 && str(rows[0]["outcome"]) == "success" && num(rows[0]["bytes_down"]) >= 1
	}
	if egressOK && strings.Contains(fx.vless.Logs(), "returning 1 IP(s) for domain vless.rl.test -> [127.0.0.1]") {
		r.pass("A08", "VLESS exit resolved vless.rl.test itself and reached the destination (egress probe success, 1 contact)")
	} else {
		r.fail("A08", "VLESS egress probe: exit=%d contacts=%d logs=%s", egress.code, greeter.contacts.Load(), fx.vless.Logs())
	}

	// ---------------------------------------------------------------------
	// A02 / A08: routing matrix with live counters.
	// ---------------------------------------------------------------------
	svc.mustOK("pool", "select", str(socksRow["id"]))
	dstPort := int(echo.port())

	// An approved destination goes through the pool: the exit saw the untouched
	// hostname and reached the destination peer.
	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dstPort))
	if err != nil || code != 200 {
		r.fail("A02", "approved destination CONNECT: code=%d err=%v", code, err)
	} else {
		r.pass("A02", "approved exact-domain destination is routed to the pool (HTTP 200)")
	}
	if err == nil && code == 200 {
		obs := fx.socks.observations()
		if len(obs) == 1 && obs[0].Host == "exact.rl.test" && obs[0].OK &&
			waitFor(3*time.Second, func() bool { return echo.contacts() == 1 }) {
			r.pass("A08", "exit received the unresolved hostname exact.rl.test and reached the destination (1 contact)")
		} else {
			r.fail("A08", "exit observations=%+v contacts=%d", obs, echo.contacts())
		}
		// The usable-tunnel assertion is separate: a 200 must be followed by a
		// working byte stream.
		echoStart := time.Now()
		echoOut, echoErr := tunnelEcho(t, conn, br, "probe-1")
		echoElapsed := time.Since(echoStart)
		closeObs := fx.socks.closeTimes()
		closeNote := "no exit close observed"
		if len(closeObs) > 0 {
			closeNote = fmt.Sprintf("exit closed its accepted socket at %v", closeObs[len(closeObs)-1].Format("15:04:05.000"))
		}
		if echoErr == nil && echoOut == "probe-1" {
			r.pass("A02", "pool tunnel carries an echo after the 200")
		} else {
			r.fail("A02", "pool tunnel returned 200, then delivered %d bytes and failed after %s (%v); %s — finding F1: the frontend cancels the dial context the pool session lives on",
				len(echoOut), echoElapsed.Round(time.Microsecond), echoErr, closeNote)
		}
		_ = conn.Close()
	}

	// The same frontend failure must be checked on the other pool protocol and
	// on the SOCKS5 listener, not only on CONNECT via the SOCKS5 exit.
	svc.mustOK("rules", "add", "--kind", "domain", "vless.rl.test")
	svc.mustOK("pool", "select", str(vlessRow["id"]))
	vconn, vbr, vcode, vreason, verr := connectHTTPStatus(t, svc.httpAddr, fmt.Sprintf("vless.rl.test:%d", dstPort))
	if verr == nil && vcode == 200 {
		_, _ = vconn.Write([]byte("vless-probe"))
		_ = vconn.SetReadDeadline(time.Now().Add(3 * time.Second))
		vbuf := make([]byte, 1)
		n, rerr := vbr.Read(vbuf)
		if n == 1 {
			r.pass("A02", "VLESS pool tunnel through the frontend delivered a payload byte")
		} else {
			r.fail("A02", "VLESS CONNECT returned 200 then delivered no byte (%v): the dial-scoped context owns the core session too (finding F1)", rerr)
		}
		_ = vconn.Close()
	} else {
		r.fail("A02", "VLESS CONNECT through the frontend: code=%d reason=%q err=%v", vcode, vreason, verr)
	}
	svc.mustOK("pool", "select", str(socksRow["id"]))
	sconn, scode, serr := socksConnect(t, svc.socksAddr, "exact.rl.test", dstPort)
	if serr == nil && scode == 0 {
		_, _ = sconn.Write([]byte("socks-probe"))
		_ = sconn.SetReadDeadline(time.Now().Add(3 * time.Second))
		sbuf := make([]byte, 1)
		n, rerr := sconn.Read(sbuf)
		if n == 1 {
			r.pass("A02", "the SOCKS5 frontend pool tunnel delivered a payload byte")
		} else {
			r.fail("A02", "SOCKS5 frontend pool tunnel replied 0x00 then delivered no byte (%v): same dial-scoped-context failure (F1)", rerr)
		}
		_ = sconn.Close()
	} else {
		r.fail("A02", "SOCKS5 frontend pool tunnel: reply=%#x err=%v", scode, serr)
	}

	// Suffix, IP and CIDR rules all reach the pool.
	for _, tc := range []struct {
		name, target, wantHost string
	}{
		{"suffix", fmt.Sprintf("deep.suffix.rl.test:%d", dstPort), "deep.suffix.rl.test"},
		{"ip", fmt.Sprintf("[2001:db8::10]:%d", dstPort), "2001:db8::10"},
		{"cidr", fmt.Sprintf("203.0.113.42:%d", dstPort), "203.0.113.42"},
	} {
		before := fx.socks.dialCount()
		c, _, code, err := connectHTTP(t, svc.httpAddr, tc.target)
		if err != nil || code != 200 {
			r.fail("A02", "%s rule: CONNECT code=%d err=%v", tc.name, code, err)
			continue
		}
		_ = c.Close()
		ok := waitFor(3*time.Second, func() bool { return fx.socks.dialCount() == before+1 })
		var saw string
		for _, o := range fx.socks.observations() {
			if o.Host == tc.wantHost && o.OK {
				saw = o.Host
			}
		}
		if ok && saw == tc.wantHost {
			r.pass("A02", "%s rule routed %s through the exit (exit dial %d→%d)", tc.name, tc.wantHost, before, fx.socks.dialCount())
		} else {
			r.fail("A02", "%s rule: exit dials %d→%d, watched=%q", tc.name, before, fx.socks.dialCount(), saw)
		}
	}

	// The suffix rule is label-boundary based: xsuffix.rl.test is not covered.
	boundaryDials := theDials(fx)
	bc, _, boundaryCode, _ := connectHTTP(t, svc.httpAddr, fmt.Sprintf("xsuffix.rl.test:%d", dstPort))
	if bc != nil {
		_ = bc.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if isDirectError(boundaryCode) && theDials(fx) == boundaryDials {
		r.pass("A02", "suffix rule does not match the label-boundary neighbour xsuffix.rl.test (0 exit dials)")
	} else {
		r.fail("A02", "suffix boundary: code=%d exit dials %d→%d", boundaryCode, boundaryDials, theDials(fx))
	}

	// A destination outside the approved list never touches the pool. The
	// documentation address has no route, so the direct dial fails without any
	// packet leaving the machine and without touching the exit.
	dialsBefore := fx.socks.dialCount()
	contactsBefore := echo.contacts()
	unapproved := svc.cli("status")
	c, _, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("[2001:db8::50]:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	time.Sleep(300 * time.Millisecond)
	if isDirectError(code) && fx.socks.dialCount() == dialsBefore && echo.contacts() == contactsBefore {
		r.pass("A02", "unapproved public destination stayed direct (HTTP %d), 0 pool dials, 0 destination contacts", code)
	} else {
		r.fail("A02", "unapproved destination: code=%d pool dials %d→%d contacts %d→%d (status=%s)",
			code, dialsBefore, fx.socks.dialCount(), contactsBefore, echo.contacts(), unapproved.stdout)
	}

	// The plain direct destination (loopback, local exception) works and never
	// touches the pool.
	poolBefore := fx.socks.dialCount()
	conn, br, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if err != nil || code != 200 {
		r.fail("A02", "direct loopback destination: code=%d err=%v", code, err)
	} else if got, err := tunnelEcho(t, conn, br, "direct-echo"); err != nil || got != "direct-echo" {
		r.fail("A02", "direct loopback echo: got=%q err=%v", got, err)
	} else if fx.socks.dialCount() != poolBefore {
		r.fail("A02", "direct loopback destination used the pool (%d→%d dials)", poolBefore, fx.socks.dialCount())
	} else {
		r.pass("A02", "plain direct destination works and uses no exit (local exception)")
	}
	if conn != nil {
		_ = conn.Close()
	}

	// HTTP forwarding is byte-for-byte: 207, headers and body survive.
	req := fmt.Sprintf("GET http://127.0.0.1:%d/fixture HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", httpDest.port(), httpDest.port())
	resp, body, err := httpForwardThroughProxy(t, svc.httpAddr, req, 10*time.Second)
	if err != nil {
		r.fail("A12", "plain HTTP forwarding: %v", err)
	} else if resp.StatusCode != 207 || body != "rl-body-0123456789" || resp.Header.Get("X-RL-Destination") != "loopback-fixture" {
		r.fail("A12", "forwarded response status=%d body=%q header=%q", resp.StatusCode, body, resp.Header.Get("X-RL-Destination"))
	} else {
		r.pass("A12", "plain HTTP forward preserved status 207, destination header and body byte for byte")
	}

	// Proxy-to-self is refused, not looped.
	_, _, selfCode, selfErr := connectHTTP(t, svc.httpAddr, svc.httpAddr)
	if selfErr == nil && selfCode == 403 {
		r.pass("A09", "CONNECT to the proxy's own listener is refused with 403 policy_denied (the self_loop reason stays off the wire by design)")
	} else {
		r.fail("A09", "proxy-to-self: code=%d err=%v, want 403", selfCode, selfErr)
	}

	// ---------------------------------------------------------------------
	// A06: a skip-verify link is quarantined and cannot be selected or dialed.
	// ---------------------------------------------------------------------
	insecureUUID := randomUUID(t)
	insecureLink := fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=tls&allowInsecure=1&type=tcp#insecure-exit", insecureUUID, fx.vless.port)
	insecurePath := filepath.Join(t.TempDir(), "insecure.txt")
	if err := os.WriteFile(insecurePath, []byte(insecureLink+"\n"+fx.links[1]+"\n"), 0o600); err != nil {
		t.Fatalf("write insecure fixture: %v", err)
	}
	insecureRes := svc.mustOK("pool", "add", "--file", insecurePath)
	insecureCounts, _ := insecureRes.mapData(t)["records"].(map[string]any)
	insecureRow := findPoolRow(t, svc, func(row map[string]any) bool {
		return str(row["status_reason"]) == "insecure_tls_removed"
	})
	if num(insecureCounts["valid"]) == 1 && num(insecureCounts["unsupported"]) == 1 &&
		insecureRow != nil && str(insecureRow["status"]) == "unsupported" &&
		insecureRow["insecure_tls"] == true {
		r.pass("A06", "skip-verify link is quarantined as unsupported with reason insecure_tls_removed")
	} else {
		r.fail("A06", "quarantine: counts=%+v row=%+v", insecureCounts, insecureRow)
	}
	insecureWarn := svc.mustOK("status").mapData(t)
	warnings, _ := insecureWarn["warnings"].([]any)
	sawTLSWarning := false
	for _, w := range warnings {
		lower := strings.ToLower(str(w))
		if strings.Contains(lower, "tls") || strings.Contains(lower, "insecure") || strings.Contains(lower, "certificate") {
			sawTLSWarning = true
		}
	}
	if sawTLSWarning {
		r.pass("A06", "status carries the visible TLS-verification remediation hint")
	} else {
		r.fail("A06", "status warnings do not mention TLS verification: %v", warnings)
	}
	selectQuarantined := svc.cli("pool", "select", str(insecureRow["id"]))
	if selectQuarantined.code == 6 && !selectQuarantined.env.OK {
		r.pass("A06", "the quarantined record cannot be selected as an exit (exit 6)")
	} else {
		r.fail("A06", "pool select of the quarantined record: exit=%d env=%+v", selectQuarantined.code, selectQuarantined.env)
	}

	// ---------------------------------------------------------------------
	// A03: a pool with records but zero usable exits, then a truly empty pool.
	// Both must fail cleanly with zero direct contacts; direct traffic works.
	// ---------------------------------------------------------------------
	zeroUsableDials := fx.socks.dialCount()
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		switch str(row["status"]) {
		case "invalid", "unsupported":
			continue
		}
		svc.mustOK("pool", "remove", str(row["id"]))
	}
	st := svc.mustOK("status").mapData(t)
	poolCounts, _ := st["pool"].(map[string]any)
	if num(poolCounts["total"]) == 1 && num(poolCounts["usable"]) == 0 {
		r.pass("A03", "pool retains the quarantined record with zero usable exits (total 1, usable 0)")
	} else {
		r.fail("A03", "zero-usable pool counts: %+v", poolCounts)
	}
	contacts := echo.contacts()
	c0, _, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dstPort))
	if c0 != nil {
		_ = c0.Close()
	}
	if code == 502 {
		r.pass("A03", "approved destination with zero usable exits fails with HTTP 502 (pool_unavailable)")
	} else {
		r.fail("A03", "zero-usable approved destination: code=%d err=%v, want 502", code, err)
	}
	time.Sleep(300 * time.Millisecond)
	if echo.contacts() == contacts && fx.socks.dialCount() == zeroUsableDials {
		r.pass("A03", "zero-usable pool produced 0 destination contacts and 0 exit dials (no direct fallback)")
	} else {
		r.fail("A03", "zero-usable pool: destination contacts %d→%d, exit dials %d→%d", contacts, echo.contacts(), zeroUsableDials, fx.socks.dialCount())
	}
	directOK, directBr, directCode, directErr := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if directErr == nil && directCode == 200 {
		if got, e := tunnelEcho(t, directOK, directBr, "zero-usable-direct"); e == nil && got == "zero-usable-direct" {
			r.pass("A03", "the direct destination still works with zero usable exits")
		} else {
			r.fail("A03", "direct echo with zero usable exits: got=%q err=%v", got, e)
		}
		_ = directOK.Close()
	} else {
		r.fail("A03", "direct destination with zero usable exits: code=%d err=%v", directCode, directErr)
	}
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		svc.mustOK("pool", "remove", str(row["id"]))
	}
	st = svc.mustOK("status").mapData(t)
	poolCounts, _ = st["pool"].(map[string]any)
	if num(poolCounts["total"]) == 0 && num(poolCounts["usable"]) == 0 {
		r.pass("A03", "pool is empty after removing every record (total 0, usable 0)")
	} else {
		r.fail("A03", "pool counts after removal: %+v", poolCounts)
	}
	contacts = echo.contacts()
	dials := fx.socks.dialCount()
	c, _, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	if code == 502 {
		r.pass("A03", "approved destination with an empty pool fails with HTTP 502 (pool_unavailable)")
	} else {
		r.fail("A03", "empty-pool approved destination: code=%d err=%v, want 502", code, err)
	}
	time.Sleep(300 * time.Millisecond)
	if echo.contacts() == contacts && fx.socks.dialCount() == dials {
		r.pass("A03", "empty pool produced 0 destination contacts and 0 exit dials (no direct fallback)")
	} else {
		r.fail("A03", "empty pool: destination contacts %d→%d, exit dials %d→%d", contacts, echo.contacts(), dials, fx.socks.dialCount())
	}
	socksConn, socksCode, socksErr := socksConnect(t, svc.socksAddr, "exact.rl.test", dstPort)
	if socksConn != nil {
		_ = socksConn.Close()
	}
	if socksErr == nil && socksCode == 0x04 {
		r.pass("A03", "SOCKS5 empty-pool approved destination answers 0x04 before any reply")
	} else {
		r.fail("A03", "SOCKS5 empty-pool reply=%#x err=%v, want 0x04", socksCode, socksErr)
	}
	time.Sleep(200 * time.Millisecond)
	if echo.contacts() == contacts && fx.socks.dialCount() == dials {
		r.pass("A03", "the SOCKS5 empty-pool attempt also produced 0 destination contacts and 0 exit dials")
	} else {
		r.fail("A03", "SOCKS5 empty-pool attempt: destination contacts %d→%d, exit dials %d→%d", contacts, echo.contacts(), dials, fx.socks.dialCount())
	}
	conn, br, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if err == nil && code == 200 {
		if got, e := tunnelEcho(t, conn, br, "still-direct"); e == nil && got == "still-direct" {
			r.pass("A03", "the plain direct destination still works while the pool is empty")
		} else {
			r.fail("A03", "direct destination after pool failure: got=%q err=%v", got, e)
		}
		_ = conn.Close()
	} else {
		r.fail("A03", "direct destination after pool failure: code=%d err=%v", code, err)
	}
	socksConn, socksCode, socksErr = socksConnect(t, svc.socksAddr, "127.0.0.1", int(refusableAddr(t)))
	if socksErr == nil && socksCode == 0x05 {
		r.pass("A03", "SOCKS5 direct refused destination answers 0x05 (connect_refused mapping)")
	} else {
		r.fail("A03", "SOCKS5 direct refused reply=%#x err=%v, want 0x05", socksCode, socksErr)
	}

	// Restore the pool for the remaining checks.
	svc.mustOK("import", "file", fx.filePath)
	if got := len(svc.mustOK("pool", "list").list(t, "records")); got != 3 {
		r.fail("A06", "pool restore after removal: %d records, want 3", got)
	}
	svc.mustOK("pool", "select", str(socksRow["id"]))

	// A06: a failed subscription refresh keeps the last good pool.
	beforeRefresh := svc.mustOK("pool", "list")
	sub.SetStatus(503)
	refresh := svc.mustFail(1, "pool", "refresh", "--source", sub.sourceID)
	if refresh.env.Error != nil && refresh.env.Error.Kind == "source_fetch" {
		r.pass("A06", "subscription refresh failure reports source_fetch with exit 1")
	} else {
		r.fail("A06", "refresh failure kind=%+v, want source_fetch", refresh.env.Error)
	}
	afterRefresh := svc.mustOK("pool", "list")
	fpBefore, fpAfter := recordFingerprints(beforeRefresh), recordFingerprints(afterRefresh)
	if sameStrings(fpBefore, fpAfter) {
		r.pass("A06", "failed refresh kept every last-good record with identical identity/status/settings (%d rows unchanged)", len(fpAfter))
	} else {
		r.fail("A06", "failed refresh changed the pool: before=%v after=%v", fpBefore, fpAfter)
	}
	sub.SetStatus(200)

	// ---------------------------------------------------------------------
	// A05: diagnostics create proposals, never rules; accept/reject/recheck.
	// ---------------------------------------------------------------------
	nonce := "request-material-" + randomHex(t, 8)
	rawReq := fmt.Sprintf("GET http://[2001:db8::60]:%d/%s?q=%s HTTP/1.1\r\nHost: [2001:db8::60]:%d\r\nCookie: rl=%s\r\nX-Trace: %s\r\nConnection: close\r\n\r\n",
		dstPort, nonce, nonce, dstPort, nonce, nonce)
	resp, _, err = httpForwardThroughProxy(t, svc.httpAddr, rawReq, 15*time.Second)
	if err != nil || (resp != nil && !isDirectError(resp.StatusCode)) || resp == nil {
		if resp != nil {
			r.fail("A05", "refused direct destination response=%d err=%v, want a pre-reply direct error", resp.StatusCode, err)
		} else {
			r.fail("A05", "refused direct destination: %v", err)
		}
	} else {
		r.pass("A05", "a refused direct public destination fails the user request (HTTP %d) without leaking request material", resp.StatusCode)
	}

	proposal := waitProposal(t, svc, "2001:db8::60", dstPort, 10*time.Second)
	if proposal != nil {
		r.pass("A05", "the eligible direct failure created a proposal for exactly that destination")
	} else {
		r.fail("A05", "no proposal appeared for 2001:db8::60:%d", dstPort)
	}
	rulesNow := svc.mustOK("rules", "list").list(t, "rules")
	if !hasRuleAddr(rulesNow, "2001:db8::60") {
		r.pass("A05", "no rule exists before the user approves the proposal")
	} else {
		r.fail("A05", "a rule for 2001:db8::60 exists without approval")
	}
	proposalsOut := svc.mustOK("proposals", "list")
	if !strings.Contains(proposalsOut.stdout, nonce) && !outputsContain(svc, nonce) {
		r.pass("A05", "no request path, query, header or cookie in evidence, CLI output or service log")
	} else {
		r.fail("A05", "proposal evidence, CLI output or the service log echoed user request material")
	}
	if !fileContains(t, statePath, nonce) {
		r.pass("A05", "no user request material was written to the state file")
	} else {
		r.fail("A05", "state file contains user request material")
	}

	// Manual diagnose, accept, and the routing effect of that acceptance.
	diag := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::61]:%d", dstPort))
	diagData := diag.mapData(t)
	proposalID := str(diagData["proposal_id"])
	if eligible, _ := diagData["eligible"].(bool); eligible && proposalID != "" {
		r.pass("A05", "manual diagnose of a refused destination returned eligible=true with a proposal id")
	} else {
		r.fail("A05", "diagnose eligible=%v proposal_id=%q", diagData["eligible"], proposalID)
	}
	accept := svc.mustOK("proposals", "accept", proposalID)
	acceptedRule := str(accept.mapData(t)["rule_id"])
	rulesAfter := svc.mustOK("rules", "list").list(t, "rules")
	if acceptedRule != "" && hasRuleAddr(rulesAfter, "2001:db8::61") && len(rulesAfter) == len(rulesNow)+1 {
		r.pass("A05", "accept created exactly one new rule for the proposed destination")
	} else {
		r.fail("A05", "accept rule=%q rules=%+v (before %d)", acceptedRule, rulesAfter, len(rulesNow))
	}
	dialsBefore = fx.socks.dialCount()
	contacts = echo.contacts()
	c, _, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("[2001:db8::61]:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	usedPool := waitFor(3*time.Second, func() bool { return fx.socks.dialCount() == dialsBefore+1 })
	if code == 200 && usedPool && waitFor(3*time.Second, func() bool { return echo.contacts() == contacts+1 }) {
		r.pass("A05", "the next request to the accepted destination used the pool (exit dial +1, destination contact +1)")
	} else {
		r.fail("A05", "post-accept request: code=%d exit dials %d→%d contacts %d→%d", code, dialsBefore, fx.socks.dialCount(), contacts, echo.contacts())
	}

	// Reject: the proposal becomes rejected and no rule is created.
	rejDiag := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::62]:%d", dstPort))
	rejID := str(rejDiag.mapData(t)["proposal_id"])
	svc.mustOK("proposals", "reject", rejID)
	rejStatus := proposalStatus(t, svc, rejID)
	if rejStatus == "rejected" && !hasRuleAddr(svc.mustOK("rules", "list").list(t, "rules"), "2001:db8::62") {
		r.pass("A05", "reject marks the proposal rejected and creates no rule")
	} else {
		r.fail("A05", "reject status=%q rule created=%v", rejStatus, hasRuleAddr(svc.mustOK("rules", "list").list(t, "rules"), "2001:db8::62"))
	}

	// Recheck: the probe engine rate-limits destination probes independently
	// of the diagnostic queue. Within that window the command must stay a
	// documented command outcome, not an internal error.
	reDiag := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::63]:%d", dstPort))
	reID := str(reDiag.mapData(t)["proposal_id"])
	reDiag2 := svc.mustOK("diagnose", fmt.Sprintf("[2001:db8::65]:%d", dstPort))
	reID2 := str(reDiag2.mapData(t)["proposal_id"])
	svc.mustOK("proposals", "reject", reID2)
	recheckEarlyAt := time.Now()
	early := svc.cli("proposals", "recheck", reID)
	if early.code == 6 {
		r.pass("A05", "recheck within the diagnostics cooldown is a documented conflict (exit 6, kind %q)", errorKind(early))
	} else {
		r.fail("A05", "recheck within the probe cooldown returned exit=%d kind=%q message=%q; contract lists exits 0/3/5/6 for proposals, so this is finding F2",
			early.code, errorKind(early), errorMessage(early))
	}
	// Past the diagnostics cooldown but inside the probe engine's fixed 60s
	// destination cooldown, the expected command outcome is again a conflict.
	time.Sleep(3200 * time.Millisecond)
	late := svc.cli("proposals", "recheck", reID)
	if late.code == 6 {
		r.pass("A05", "recheck during the probe engine cooldown reports a documented conflict (exit 6, kind %q)", errorKind(late))
	} else {
		r.fail("A05", "recheck inside the probe cooldown returned exit=%d kind=%q message=%q: an expected cooldown surfaces as an internal error instead of a command conflict (finding F2)",
			late.code, errorKind(late), errorMessage(late))
	}

	// Disabling diagnostics is effective: no new automatic proposal, queue 0.
	svc.mustOK("config", "set", "diagnostics.enabled", "false")
	statusOff := svc.mustOK("status").mapData(t)
	diagOff, _ := statusOff["diagnostics"].(map[string]any)
	proposalsBefore := len(svc.mustOK("proposals", "list").list(t, "proposals"))
	c, _, _, _ = connectHTTP(t, svc.httpAddr, fmt.Sprintf("[2001:db8::64]:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	time.Sleep(2 * time.Second)
	proposalsAfter := len(svc.mustOK("proposals", "list").list(t, "proposals"))
	if enabled, _ := diagOff["enabled"].(bool); !enabled && num(diagOff["queued"]) == 0 && proposalsAfter == proposalsBefore {
		r.pass("A05", "diagnostics disabled: queue 0 and no new proposal for a refused destination")
	} else {
		r.fail("A05", "disabled diagnostics: %+v proposals %d→%d", diagOff, proposalsBefore, proposalsAfter)
	}
	r.note("A05", "queue cancellation of an already-running slow probe is not reproducible loopback-only; disable was checked for queue=0 and no new evidence")
	svc.mustOK("config", "set", "diagnostics.enabled", "true")

	// ---------------------------------------------------------------------
	// A04: modes — compatible survive, pending dials are swept, all-direct.
	// ---------------------------------------------------------------------
	svc.mustOK("rules", "add", "--kind", "domain", "stall.rl.test")

	live, liveBr, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if err != nil || code != 200 {
		t.Fatalf("live direct tunnel: code=%d err=%v", code, err)
	}
	if got, err := tunnelEcho(t, live, liveBr, "before-switch"); err != nil || got != "before-switch" {
		t.Fatalf("live direct tunnel echo: got=%q err=%v", got, err)
	}
	mode1 := svc.mustOK("mode", "all-proxy").mapData(t)
	closed1 := mode1["closed"].(map[string]any)
	if num(closed1["count"]) == 0 && num(mode1["kept"]) >= 1 {
		r.pass("A04", "targeted→all-proxy kept the direct local connection (kept=%v, closed=0)", mode1["kept"])
	} else {
		r.fail("A04", "targeted→all-proxy: closed=%v kept=%v", closed1, mode1["kept"])
	}
	if got, err := tunnelEcho(t, live, liveBr, "after-switch"); err != nil || got != "after-switch" {
		r.fail("A04", "direct local tunnel after all-proxy: got=%q err=%v", got, err)
	} else {
		r.pass("A04", "the local-exception tunnel still carries data in all-proxy")
	}

	// All-proxy routes a public Other destination through the pool.
	dialsBefore = fx.socks.dialCount()
	c, _, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("[2001:db8::70]:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	if code == 200 && waitFor(3*time.Second, func() bool { return fx.socks.dialCount() == dialsBefore+1 }) {
		r.pass("A04", "all-proxy sends an unapproved public destination through the pool (exit dial +1)")
	} else {
		r.fail("A04", "all-proxy public destination: code=%d exit dials %d→%d", code, dialsBefore, fx.socks.dialCount())
	}

	// Back to targeted: the local tunnel is compatible and survives.
	svc.mustOK("mode", "targeted")
	if got, err := tunnelEcho(t, live, liveBr, "targeted-again"); err != nil || got != "targeted-again" {
		r.fail("A04", "direct tunnel after all-proxy→targeted: got=%q err=%v", got, err)
	} else {
		r.pass("A04", "all-proxy→targeted kept the local direct connection alive")
	}
	_ = live.Close()

	// A pending pool dial must be canceled by a mode change before success is
	// reported, and within the documented pending-cancel bound.
	fx.socks.stallHost("stall.rl.test")
	svc.mustOK("pool", "select", str(socksRow["id"]))
	acceptsBefore := fx.socks.drainCount()
	pending := make(chan int, 1)
	go func() {
		c, _, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("stall.rl.test:%d", dstPort))
		if c != nil {
			_ = c.Close()
		}
		if err != nil {
			pending <- -1
			return
		}
		pending <- code
	}()
	if !waitFor(10*time.Second, func() bool { return fx.socks.drainCount() > acceptsBefore }) {
		t.Fatalf("stalled exit never accepted the pending dial")
	}
	time.Sleep(150 * time.Millisecond)
	mode2Start := time.Now()
	pendingMode := svc.cli("mode", "all-direct")
	cliReturned := time.Now()
	modeTook := cliReturned.Sub(mode2Start)
	if pendingMode.code == 0 {
		closed2 := pendingMode.mapData(t)["closed"].(map[string]any)
		took := modeTook
		// §3.4 bounds the server-side pending cancel, which the command reports
		// as duration_ms. The CLI round trip includes exec/socket overhead, so it
		// is only loosely bounded here.
		reported := time.Duration(num(pendingMode.mapData(t)["duration_ms"])) * time.Millisecond
		if num(closed2["pending"]) >= 1 && reported <= 50*time.Millisecond && took <= time.Second {
			r.pass("A04", "targeted→all-direct swept the pending dial before success (reported %s, CLI %s, pending=%v)",
				reported, took.Round(time.Millisecond), closed2["pending"])
		} else if num(closed2["pending"]) < 1 {
			r.fail("A04", "mode change did not report the pending cancel: %+v", closed2)
		} else {
			r.fail("A04", "mode change reported success with a pending dial but the sweep took %s (CLI %s); the documented pending-cancel bound is ~50ms",
				reported, took.Round(time.Millisecond))
		}
	} else {
		r.fail("A04", "targeted→all-direct with one pending pool dial returned exit=%d kind=%q after %s; the sweep must cancel a pending dial within the documented bound, not exceed the 5s management deadline (finding F3)",
			pendingMode.code, errorKind(pendingMode), cliReturned.Sub(mode2Start).Round(time.Millisecond))
	}
	// The change may still land after the deadline. Record when, so the report
	// distinguishes "never applied" from "applied late".
	applied := waitFor(15*time.Second, func() bool {
		res := svc.cli("status")
		if res.code != 0 || !res.env.OK {
			return false
		}
		return str(res.mapData(t)["mode"]) == "all-direct"
	})
	appliedAt := time.Now()
	settled := waitFor(5*time.Second, func() bool {
		closes := fx.socks.closeTimes()
		return len(closes) > 0 && closes[len(closes)-1].After(mode2Start)
	})
	if pendingMode.code != 0 {
		r.note("A04", "mode change applied at +%s after the timed-out request", appliedAt.Sub(mode2Start).Round(time.Millisecond))
	}
	if !applied {
		t.Fatalf("all-direct was never applied after the timed-out mode change")
	}
	closes := fx.socks.closeTimes()
	if settled && len(closes) > 0 {
		elapsed := closes[len(closes)-1].Sub(mode2Start)
		// The dial had run for only ~150ms when the mode command started, so its
		// 10s deadline cannot explain a close at +elapsed. A close this close to
		// the commit is the pending-cancel sweep releasing the exit socket.
		if pendingMode.code == 0 {
			r.note("A04", "the mode change returned at +%s with pending=1 and the exit socket closed at +%s; the dial was ~150ms old, so this is the sweep canceling the pending dial, not its 10s deadline", modeTook.Round(time.Millisecond), elapsed.Round(time.Millisecond))
		} else {
			r.note("A04", "the timed-out mode change applied at +%s and the exit socket closed at +%s", modeTook.Round(time.Millisecond), elapsed.Round(time.Millisecond))
		}
	} else {
		r.fail("A04", "the exit did not observe the pending dial teardown at all")
	}
	if code := <-pending; code != 200 {
		r.pass("A04", "the pending pool CONNECT ended without a 200 (code=%d)", code)
	} else {
		r.fail("A04", "the canceled pending pool CONNECT still returned 200")
	}
	fx.socks.unstallHost("stall.rl.test")

	// All-direct keeps serving and uses no exit.
	dialsBefore = fx.socks.dialCount()
	directLive, directLiveBr, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if err == nil && code == 200 {
		if got, e := tunnelEcho(t, directLive, directLiveBr, "all-direct"); e == nil && got == "all-direct" {
			r.pass("A04", "all-direct keeps serving the local direct destination")
		} else {
			r.fail("A04", "all-direct local echo: got=%q err=%v", got, e)
		}
	} else {
		r.fail("A04", "all-direct local CONNECT: code=%d err=%v", code, err)
	}
	contacts = echo.contacts()
	c, _, code, err = connectHTTP(t, svc.httpAddr, fmt.Sprintf("exact.rl.test:%d", dstPort))
	if c != nil {
		_ = c.Close()
	}
	time.Sleep(300 * time.Millisecond)
	if fx.socks.dialCount() == dialsBefore && echo.contacts() == contacts {
		r.pass("A04", "all-direct used no exit for an approved destination (0 exit dials, 0 destination contacts)")
	} else {
		r.fail("A04", "all-direct exit dials %d→%d", dialsBefore, fx.socks.dialCount())
	}
	mode3 := svc.mustOK("mode", "targeted").mapData(t)
	if num(mode3["kept"]) >= 1 {
		if got, e := tunnelEcho(t, directLive, directLiveBr, "targeted-after-direct"); e == nil && got == "targeted-after-direct" {
			r.pass("A04", "all-direct→targeted kept the direct connection alive (kept=%v)", mode3["kept"])
		} else {
			r.fail("A04", "kept direct tunnel after all-direct→targeted: got=%q err=%v", got, e)
		}
	} else {
		r.fail("A04", "all-direct→targeted kept=%v", mode3["kept"])
	}
	_ = directLive.Close()

	// Recheck after the probe cooldown: fresh evidence on a pending proposal,
	// and a rejected proposal stays rejected.
	if wait := 63*time.Second - time.Since(recheckEarlyAt); wait > 0 {
		r.note("A05", "waiting %s for the probe engine's fixed 60s destination cooldown", wait.Round(time.Second))
		time.Sleep(wait)
	}
	recheck := svc.mustOK("proposals", "recheck", reID)
	reData := recheck.mapData(t)
	if eligible, _ := reData["eligible"].(bool); eligible {
		r.pass("A05", "recheck after the cooldown re-ran the bounded direct probe and added evidence")
	} else {
		r.fail("A05", "recheck after cooldown eligible=%v evidence_added=%v", reData["eligible"], reData["evidence_added"])
	}
	recheck2 := svc.mustOK("proposals", "recheck", reID2)
	if got := str(recheck2.mapData(t)["proposal"].(map[string]any)["status"]); got == "rejected" {
		r.pass("A05", "recheck of a rejected proposal keeps it rejected")
	} else {
		r.fail("A05", "recheck of rejected proposal reported status=%q", got)
	}

	// ---------------------------------------------------------------------
	// A09: secret scan and permissions, before shutdown.
	// ---------------------------------------------------------------------
	scanSecrets(t, r, svc, dir, fx, sub, insecureUUID, insecureLink)
	if !outputsContain(svc, nonce) {
		r.pass("A09", "no request path, query, header or cookie appears in any later CLI/log surface either")
	} else {
		r.fail("A09", "a later CLI/log surface echoed request material")
	}
	checkPermissions(t, r, dir)
	r.note("A09", "data dir inventory: %s", reviewStateArea(t, dir))

	// ---------------------------------------------------------------------
	// A04 / A13: stopped closes listeners and live/pending connections; the
	// process still serves management; leaving stopped needs a restart.
	// ---------------------------------------------------------------------
	live2, liveBr2, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("127.0.0.1:%d", directEcho.port()))
	if err != nil || code != 200 {
		t.Fatalf("pre-stop direct tunnel: code=%d err=%v", code, err)
	}
	if got, err := tunnelEcho(t, live2, liveBr2, "pre-stop"); err != nil || got != "pre-stop" {
		t.Fatalf("pre-stop echo: got=%q err=%v", got, err)
	}
	eofsBefore := directEcho.eofs()

	stopData := svc.mustOK("mode", "stopped").mapData(t)
	closedStop := stopData["closed"].(map[string]any)
	if num(closedStop["count"]) >= 1 {
		r.pass("A04", "stopped closed the live connection before reporting success (closed=%v)", closedStop["count"])
	} else {
		r.fail("A04", "stopped close report=%+v, want count>=1", closedStop)
	}
	afterStop := svc.mustOK("status").mapData(t)
	conns, _ := afterStop["connections"].(map[string]any)
	if num(conns["open"]) == 0 && num(conns["dialing"]) == 0 {
		r.pass("A04", "immediately after stopped returns, status reports 0 open and 0 dialing connections")
	} else {
		r.fail("A04", "connections still registered after stopped returned: %+v", conns)
	}
	_ = live2.SetReadDeadline(time.Now().Add(3 * time.Second))
	one := make([]byte, 1)
	if _, err := live2.Read(one); err != nil {
		r.pass("A04", "the live tunnel's client side observes teardown after stopped")
	} else {
		r.fail("A04", "the live tunnel still delivered data after stopped")
	}
	_ = live2.Close()
	if waitFor(3*time.Second, func() bool { return directEcho.eofs() > eofsBefore }) {
		r.pass("A04", "the destination peer observed the live tunnel teardown")
	} else {
		r.fail("A04", "the destination peer did not observe the teardown")
	}
	r.note("A04", "stopped with a pending pool dial reuses the F3 path (management deadline); it is asserted once above instead of repeated here")

	// Both traffic listeners are gone; management still answers.
	httpRefuses := listenerRefuses(t, svc.httpAddr)
	socksRefuses := listenerRefuses(t, svc.socksAddr)
	if httpRefuses && socksRefuses {
		r.pass("A04", "both proxy ports stop accepting after stopped")
	} else {
		r.fail("A04", "listener refusal after stopped: http=%v socks=%v", httpRefuses, socksRefuses)
	}
	stoppedStatus := svc.mustOK("status").mapData(t)
	stopWarnings, _ := stoppedStatus["warnings"].([]any)
	if str(stoppedStatus["mode"]) == "stopped" && len(stopWarnings) > 0 {
		r.pass("A04", "status keeps answering in stopped mode with at least one warning")
	} else {
		r.fail("A04", "stopped status: mode=%v warnings=%v", stoppedStatus["mode"], stopWarnings)
	}
	leave := svc.mustFail(6, "mode", "targeted")
	if leave.env.Error != nil && strings.Contains(strings.ToLower(errorMessage(leave)), "restart") {
		r.pass("A04", "leaving stopped mode is refused with exit 6 and the documented restart hint (%q)", errorMessage(leave))
	} else {
		r.fail("A04", "leave stopped: exit=%d message=%q, want exit 6 with a restart hint", leave.code, errorMessage(leave))
	}

	stateAtStop, _ := os.ReadFile(statePath)
	stopErr := svc.stop()
	stateAfterExit, _ := os.ReadFile(statePath)
	if stopErr != nil {
		r.fail("A13", "SIGINT shutdown returned %v, want clean exit", stopErr)
	} else {
		r.pass("A13", "SIGINT shut the service down cleanly (exit 0)")
	}
	if len(stateAtStop) > 0 && string(stateAtStop) == string(stateAfterExit) {
		r.pass("A13", "shutdown left the committed state byte-identical (no partial write)")
	} else {
		r.fail("A13", "shutdown changed state.json: %d → %d bytes", len(stateAtStop), len(stateAfterExit))
	}
	offline := svc.cli("status")
	if offline.code == 3 {
		r.pass("A10", "status after shutdown exits 3 (service not available)")
	} else {
		r.fail("A10", "status after shutdown exit=%d, want 3", offline.code)
	}
}

// ---------------------------------------------------------------------------
// helpers used by the flow
// ---------------------------------------------------------------------------

type subscriptionServer struct {
	url      string
	token    string
	sourceID string
	state    *subState
	server   *http.Server
	ln       net.Listener
}

type subState struct {
	mu     sync.Mutex
	status int
}

func newSubscriptionServer(t *testing.T, fx *fixtureSet, token string) *subscriptionServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscription listen: %v", err)
	}
	state := &subState{status: 200}
	payload := strings.Join(fx.links, "\n") + "\n"
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		code := state.status
		state.mu.Unlock()
		if code != 200 {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, "unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	u := fmt.Sprintf("http://127.0.0.1:%d/nodes.txt?token=%s", ln.Addr().(*net.TCPAddr).Port, token)
	return &subscriptionServer{url: u, token: token, state: state, server: srv, ln: ln}
}

func (s *subscriptionServer) SetStatus(code int) {
	s.state.mu.Lock()
	s.state.status = code
	s.state.mu.Unlock()
}

func (s *subscriptionServer) setSource(id string) { s.sourceID = id }

func waitProposal(t *testing.T, svc *service, addr string, port int, d time.Duration) map[string]any {
	t.Helper()
	var found map[string]any
	waitFor(d, func() bool {
		for _, p := range svc.mustOK("proposals", "list").list(t, "proposals") {
			dest, _ := p["destination"].(map[string]any)
			if str(dest["addr"]) == addr && num(dest["port"]) == port && str(p["status"]) == "pending" {
				found = p
				return true
			}
		}
		return false
	})
	return found
}

func proposalStatus(t *testing.T, svc *service, id string) string {
	t.Helper()
	for _, p := range svc.mustOK("proposals", "list").list(t, "proposals") {
		if str(p["id"]) == id {
			return str(p["status"])
		}
	}
	return ""
}

func hasRuleAddr(rules []map[string]any, addr string) bool {
	for _, r := range rules {
		if str(r["addr"]) == addr {
			return true
		}
	}
	return false
}

// fileContains reports whether the file exists and contains needle.
func fileContains(t *testing.T, path, needle string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), needle)
}

// listenerRefuses reports whether nothing accepts on addr.
func listenerRefuses(t *testing.T, addr string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return true
}

func errorKind(res cliResult) string {
	if res.env.Error == nil {
		return ""
	}
	return res.env.Error.Kind
}

func errorMessage(res cliResult) string {
	if res.env.Error == nil {
		return ""
	}
	return res.env.Error.Message
}

// refusedWith reports whether the command failed with one of the exit codes the
// contract allows for a rejected input (2/6/7 for `rules add`).
func refusedWith(res cliResult, codes ...int) bool {
	if res.env.OK {
		return false
	}
	for _, code := range codes {
		if res.code == code {
			return true
		}
	}
	return false
}

// isDirectError accepts the pre-reply direct-path statuses the taxonomy maps:
// connect_refused/dns_failure -> 502 and timeout -> 504 (docs/contracts.md §3.2).
func isDirectError(code int) bool { return code == 502 || code == 504 }

func theDials(fx *fixtureSet) int64 { return fx.socks.dialCount() }

// outputsContain reports whether any captured product surface (CLI stdout,
// stderr, or the service's own streams) contains s.
func outputsContain(svc *service, s string) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if strings.Contains(svc.stderr.String(), s) || strings.Contains(svc.stdout.String(), s) {
		return true
	}
	for _, out := range svc.outputs {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// recordFingerprints is a stable projection of the pool rows that must not
// change across a failed refresh (identity, status and connection settings).
func recordFingerprints(res cliResult) []string {
	var data struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(res.env.Data, &data); err != nil {
		return nil
	}
	out := make([]string, 0, len(data.Records))
	for _, row := range data.Records {
		out = append(out, strings.Join([]string{
			str(row["id"]), str(row["status"]), str(row["status_reason"]),
			str(row["protocol"]), str(row["server"]), fmt.Sprint(row["port"]),
			str(row["transport"]), str(row["security"]),
		}, "|"))
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
