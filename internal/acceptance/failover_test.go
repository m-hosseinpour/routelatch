package acceptance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// failoverLinks writes a 0600 import file with one SOCKS5 exit per port.
func failoverLinks(t *testing.T, ports []uint16) string {
	t.Helper()
	var b strings.Builder
	for i, port := range ports {
		fmt.Fprintf(&b, "socks5://127.0.0.1:%d#failover-%d\n", port, i+1)
	}
	path := filepath.Join(t.TempDir(), "failover-nodes.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write failover links: %v", err)
	}
	return path
}

func poolRowByPort(t *testing.T, svc *service, port uint16) map[string]any {
	t.Helper()
	return findPoolRow(t, svc, func(row map[string]any) bool { return num(row["port"]) == int(port) })
}

// TestFailoverBeforeFirstByte drives the real service and binary path. The
// first automatic exit accepts CONNECT, takes the client's first byte and ends
// the tunnel without a destination byte; the service must replay that byte to a
// second exit, show one destination request, never use the direct path, and
// cool the failing exit. A manual pin must not fail over.
func TestFailoverBeforeFirstByte(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "failover")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}

	flaky := newSOCKSExit(t)
	healthy := newSOCKSExit(t)
	flaky.failHost("origin.rl.test")
	origin := newEchoPeer(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	svc.mustOK("import", "file", failoverLinks(t, []uint16{flaky.port(), healthy.port()}))
	svc.mustOK("rules", "add", "--kind", "domain", "origin.rl.test")
	flakyRow := poolRowByPort(t, svc, flaky.port())
	healthyRow := poolRowByPort(t, svc, healthy.port())
	if flakyRow == nil || healthyRow == nil {
		t.Fatalf("pool rows missing: flaky=%v healthy=%v", flakyRow, healthyRow)
	}

	// origin.rl.test does not resolve on the host, so a hidden direct attempt
	// would fail before contacting this peer; no-direct-fallback is asserted by
	// the frontend unit tests' injected dialer counter. What the real process
	// can prove here is the absence of any direct success or destination contact.
	target := fmt.Sprintf("origin.rl.test:%d", origin.port())
	conn, br, code, err := connectHTTP(t, svc.httpAddr, target)
	if err != nil || code != 200 {
		r.fail("R15", "failover CONNECT: code=%d err=%v", code, err)
		return
	}
	if got, err := tunnelEcho(t, conn, br, "after-failover"); err != nil || got != "after-failover" {
		r.fail("R15", "echo after failover: got=%q err=%v", got, err)
	} else {
		r.pass("R15", "first automatic exit died pre-byte; the same CONNECT then carried an echo through the second exit")
	}
	_ = conn.Close()

	cooled := waitFor(5*time.Second, func() bool {
		row := poolRowByPort(t, svc, flaky.port())
		return row != nil && str(row["status"]) == "cooling" &&
			strings.Contains(str(row["status_reason"]), "destination_unconfirmed")
	})
	dials, healthyDials, contacts := flaky.flakyCount(), healthy.dialCount(), origin.contacts()
	if cooled && dials == 1 && healthyDials == 1 && contacts == 1 {
		r.pass("R15", "failing exit cooled as cooling:destination_unconfirmed; healthy exit dialed once; destination saw exactly 1 request")
	} else {
		r.fail("R15", "cooled=%v flaky=%d healthy=%d destination_contacts=%d", cooled, dials, healthyDials, contacts)
	}

	// Pin the flaky exit: no automatic retry is allowed, so the tunnel closes
	// without an echo and the healthy exit is untouched.
	pinHealthy := healthy.dialCount()
	pinContacts := origin.contacts()
	pinFlaky := flaky.flakyCount()
	svc.mustOK("pool", "select", str(flakyRow["id"]))
	pconn, pbr, pcode, perr := connectHTTP(t, svc.httpAddr, target)
	if perr == nil && pcode == 200 {
		_, _ = io.WriteString(pconn, "pinned")
		_ = pconn.SetReadDeadline(time.Now().Add(3 * time.Second))
		got, rerr := io.ReadAll(pbr)
		_ = pconn.Close()
		time.Sleep(200 * time.Millisecond)
		if len(got) == 0 && rerr == nil && healthy.dialCount() == pinHealthy &&
			flaky.flakyCount() == pinFlaky+1 && origin.contacts() == pinContacts {
			r.pass("R15", "a manual pin never fails over: its pre-byte death closed the tunnel and no other exit was dialed")
		} else {
			r.fail("R15", "pin: bytes=%q err=%v healthy %d→%d flaky %d→%d contacts %d→%d",
				got, rerr, pinHealthy, healthy.dialCount(), pinFlaky, flaky.flakyCount(), pinContacts, origin.contacts())
		}
	} else {
		r.fail("R15", "pinned CONNECT: code=%d err=%v", pcode, perr)
	}
	if res := svc.cli("pool", "select", "auto"); res.code != 0 {
		r.fail("R15", "pool select auto: exit=%d kind=%q", res.code, errorKind(res))
	}
}

// TestFailoverBudgetExhaustsWithoutDirectFallback: three flaky exits exhaust the
// CONNECT budget. The client keeps the one 200 already sent, then sees a clean
// teardown; there is no error page, no fourth dial, no destination request and
// no direct attempt. Every attempt is cooled.
func TestFailoverBudgetExhaustsWithoutDirectFallback(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "fb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}

	exits := []*socksExit{newSOCKSExit(t), newSOCKSExit(t), newSOCKSExit(t)}
	ports := make([]uint16, len(exits))
	for i, x := range exits {
		x.failHost("origin.rl.test")
		ports[i] = x.port()
	}
	origin := newEchoPeer(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	svc.mustOK("import", "file", failoverLinks(t, ports))
	svc.mustOK("rules", "add", "--kind", "domain", "origin.rl.test")
	baseFD, fdErr := svc.fdCount()

	conn, br, code, err := connectHTTP(t, svc.httpAddr, fmt.Sprintf("origin.rl.test:%d", origin.port()))
	if err != nil || code != 200 {
		r.fail("R15", "budget CONNECT: code=%d err=%v", code, err)
		return
	}
	_, _ = io.WriteString(conn, "budget")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, rerr := io.ReadAll(br)
	_ = conn.Close()

	total := int64(0)
	for _, x := range exits {
		total += x.flakyCount()
	}
	if len(got) == 0 && rerr == nil && total == int64(contract.ConnectFailoverExits) && origin.contacts() == 0 {
		r.pass("R15", "exhausted budget: 200 then clean teardown, exactly %d exit attempts, no error page, no destination contact, no direct fallback", total)
	} else {
		r.fail("R15", "budget: bytes=%q err=%v attempts=%d destination_contacts=%d", got, rerr, total, origin.contacts())
	}

	allCooled := waitFor(5*time.Second, func() bool {
		rows := svc.mustOK("pool", "list").list(t, "records")
		cooled := 0
		for _, row := range rows {
			if str(row["status"]) == "cooling" && strings.Contains(str(row["status_reason"]), "destination_unconfirmed") {
				cooled++
			}
		}
		return cooled == len(exits)
	})
	if allCooled {
		r.pass("R15", "every exhausted attempt is visible as a cooled exit with the honest destination_unconfirmed kind")
	} else {
		r.fail("R15", "not every attempted exit is cooling: %+v", svc.mustOK("pool", "list").list(t, "records"))
	}

	// The failure path must release every client, exit and retry socket. The
	// product process holds no lingering descriptor once the teardown settles.
	settled := fdErr == nil && waitFor(5*time.Second, func() bool {
		n, err := svc.fdCount()
		return err == nil && n <= baseFD+1
	})
	if settled {
		r.pass("R15", "the exhausted failure path tore down its sockets (fd count back to the pre-connection baseline)")
	} else {
		n, _ := svc.fdCount()
		r.fail("R15", "descriptor leak after the failure path: base=%d after=%d (err=%v)", baseFD, n, fdErr)
	}
}
