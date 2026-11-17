package main

import (
	"fmt"
	"time"
)

func (e *env) runRouteFallback(echo *peer) {
	// A routing rule that matches all TCP and names a tag that has no handler.
	// The dispatcher must close the link, not use its default handler.
	cfg, err := buildRouteConfig(true)
	if err != nil {
		e.rep.kv("route config", oneLine(err))
		return
	}
	c, err := startClientRaw(cfg)
	if err != nil {
		e.rep.kv("route client", "FAILED: "+oneLine(err))
		return
	}
	echo.reset()
	a := c.dial("", "127.0.0.1", echo.port())
	e.reportAttempt("route-missing-tag", a)
	o := readFor(a.conn, time.Second, 64)
	e.reportRead("route-missing-tag", o, a)
	e.reportTracked("route-missing-tag", a, a.track.waitError(250*time.Millisecond))
	e.rep.kv("route-missing-tag destination contacts", echo.state().accepts)
	closeAttempt(a)
	c.stop()

	// A rule that does not match falls through to the default handler. This
	// config puts direct first on purpose: the adapter must never rely on this.
	cfg2, err := buildRouteConfig(false)
	if err != nil {
		e.rep.kv("no-match config", oneLine(err))
		return
	}
	c2, err := startClientRaw(cfg2)
	if err != nil {
		e.rep.kv("no-match client", "FAILED: "+oneLine(err))
		return
	}
	echo.reset()
	a = c2.dial("", "127.0.0.1", echo.port())
	e.reportAttempt("route-no-match", a)
	if echo.waitAccept(time.Second) {
		e.rep.kv("route-no-match destination contacts", echo.state().accepts)
		_, _, _ = writeFor(a.conn, []byte("ping"), time.Second)
		o = readFor(a.conn, time.Second, 64)
		e.reportRead("route-no-match", o, a)
	} else {
		e.rep.kv("route-no-match destination contacts", 0)
	}
	closeAttempt(a)
	c2.stop()

	// Empty core balancer: no member outbound exists for selector "pool".
	cfg3, err := buildBalancerConfig()
	if err != nil {
		e.rep.kv("balancer config", oneLine(err))
		return
	}
	c3, err := startClientRaw(cfg3)
	if err != nil {
		e.rep.kv("balancer client", "FAILED: "+oneLine(err))
		return
	}
	echo.reset()
	a = c3.dial("", "127.0.0.1", echo.port())
	e.reportAttempt("route-empty-balancer", a)
	if echo.waitAccept(time.Second) {
		e.rep.kv("route-empty-balancer destination contacts", echo.state().accepts)
		_, _, _ = writeFor(a.conn, []byte("ping"), time.Second)
		o = readFor(a.conn, time.Second, 64)
		e.reportRead("route-empty-balancer", o, a)
	} else {
		e.rep.kv("route-empty-balancer destination contacts", 0)
		o = readFor(a.conn, time.Second, 64)
		e.reportRead("route-empty-balancer", o, a)
	}
	e.reportTracked("route-empty-balancer", a, a.track.waitError(250*time.Millisecond))
	closeAttempt(a)
	c3.stop()
}

func (e *env) reportAttempt(prefix string, a *attempt) {
	e.rep.kv(prefix+" dial error", oneLine(a.dialErr))
	e.rep.kv(prefix+" dial return", dur(a.tDial.Sub(a.tStart)))
}

func (e *env) reportTracked(prefix string, a *attempt, errs []trackedErr) {
	if len(errs) == 0 {
		e.rep.kv(prefix+" tracked error", "none")
		return
	}
	for i, te := range errs {
		e.rep.kv(fmt.Sprintf("%s tracked[%d] @%s", prefix, i, dur(te.at.Sub(a.tStart))), oneLine(te.err))
	}
}

func (e *env) reportRead(prefix string, o readOutcome, a *attempt) {
	rel := dur(o.at.Sub(a.tStart))
	if len(o.data) > 0 {
		e.rep.kv(prefix+" read", fmt.Sprintf("%q after %s", o.data, rel))
		return
	}
	e.rep.kv(prefix+" read", fmt.Sprintf("%s after %s", oneLine(o.err), rel))
}

func (e *env) reportPeer(prefix string, p *peer) {
	st := p.state()
	e.rep.kv(prefix+" peer", fmt.Sprintf("accepts=%d bytes=%d eofs=%d", st.accepts, st.bytesIn, st.eofs))
}

// runGate proves the statements that do not depend on a TLS/WS/gRPC transport.
func (e *env) runGate() {
	e.rep.section("A. core.Dial vs an actual destination connection (VLESS/RAW)")
	rig, err := e.newRig(trVLESSRaw)
	if err != nil {
		e.rep.kv("rig", "FAILED: "+oneLine(err))
		return
	}
	defer rig.stop()

	echo := mustPeer(e.rep, "dest-echo", peerEcho, 0)
	closeOnAccept := mustPeer(e.rep, "dest-close-on-accept", peerCloseAfterAccept, 20)
	closeOnData := mustPeer(e.rep, "dest-close-on-data", peerCloseAfterData, 20)
	resetOnData := mustPeer(e.rep, "dest-reset-on-data", peerResetOnData, 60)
	destRefused := freePort()
	e.rep.snapshot(snap("client core up"))
	e.rep.kv("refused destination", "127.0.0.1:"+itoa(destRefused)+" (no listener)")

	// A1: healthy pool connection to a real destination.
	e.rep.section("A1. healthy pool tag -> destination echo")
	echo.reset()
	a := rig.client.dial("pool", "127.0.0.1", echo.port())
	e.reportAttempt("A1", a)
	if rig.front != nil {
		e.rep.kv("A1 forwarder accepts", rig.front.state().accepts)
	}
	accepted := echo.waitAccept(2 * time.Second)
	st := echo.state()
	e.rep.kv("A1 destination accepted", accepted)
	if accepted {
		e.rep.kv("A1 dial returned before accept", a.tDial.Before(st.firstAccept))
		e.rep.kv("A1 dial -> accept gap", delta(a.tDial, st.firstAccept))
	}
	n, werr, wt := writeFor(a.conn, []byte("ping"), time.Second)
	e.rep.kv("A1 write", fmt.Sprintf("n=%d err=%s after %s", n, oneLine(werr), dur(wt.Sub(a.tStart))))
	o := readFor(a.conn, 2*time.Second, 64)
	e.reportRead("A1", o, a)
	e.reportTracked("A1", a, a.track.all())
	tClose := time.Now()
	closeAttempt(a)
	if echo.waitEOF(2 * time.Second) {
		e.rep.kv("A1 close -> destination EOF", dur(time.Since(tClose)))
	}
	e.reportPeer("A1", echo)

	// A2: a fake exit that accepts TCP and never speaks the proxy protocol.
	e.rep.section("A2. dead pool tag -> fake exit accepts TCP and never completes the handshake")
	rig.dead.reset()
	echo.reset()
	a = rig.client.dial("deadpool", "127.0.0.1", echo.port())
	e.reportAttempt("A2", a)
	o = readFor(a.conn, 800*time.Millisecond, 64)
	e.reportRead("A2", o, a)
	e.reportTracked("A2", a, a.track.all())
	e.rep.kv("A2 destination contacts", echo.state().accepts)
	e.rep.kv("A2 fake exit TCP accepts", rig.dead.state().accepts)
	closeAttempt(a)
	if rig.dead.waitEOF(2 * time.Second) {
		e.rep.kv("A2 fake exit saw close", "yes")
	} else {
		e.rep.kv("A2 fake exit saw close", "NO")
	}

	// A3: the exit port is closed.
	e.rep.section("A3. pool tag -> exit TCP refused")
	a = rig.client.dial("refusedpool", "127.0.0.1", echo.port())
	e.reportAttempt("A3", a)
	errs := a.track.waitError(4 * time.Second)
	e.reportTracked("A3", a, errs)
	o = readFor(a.conn, time.Second, 64)
	e.reportRead("A3", o, a)
	closeAttempt(a)

	// A4: exit accepts but then closes (auth/transport rejection shape).
	e.rep.section("A4. pool tag -> exit with a different UUID (server-side rejection)")
	echo.reset()
	a = rig.client.dial("badpool", "127.0.0.1", echo.port())
	e.reportAttempt("A4", a)
	o = readFor(a.conn, time.Second, 64)
	e.reportRead("A4", o, a)
	e.reportTracked("A4", a, a.track.waitError(250*time.Millisecond))
	e.rep.kv("A4 destination contacts", echo.state().accepts)
	closeAttempt(a)

	// A5: an unselected tag must not fall back to direct or default.
	e.rep.section("A5. selected outbound tag missing")
	echo.reset()
	a = rig.client.dial("ghost", "127.0.0.1", echo.port())
	e.reportAttempt("A5", a)
	o = readFor(a.conn, 800*time.Millisecond, 64)
	e.reportRead("A5", o, a)
	e.reportTracked("A5", a, a.track.waitError(300*time.Millisecond))
	e.rep.kv("A5 destination contacts", echo.state().accepts)
	closeAttempt(a)

	e.rep.section("A5b. selected outbound tag is a blackhole")
	echo.reset()
	a = rig.client.dial("blocked", "127.0.0.1", echo.port())
	e.reportAttempt("A5b", a)
	o = readFor(a.conn, 800*time.Millisecond, 64)
	e.reportRead("A5b", o, a)
	e.reportTracked("A5b", a, a.track.waitError(300*time.Millisecond))
	e.rep.kv("A5b destination contacts", echo.state().accepts)
	closeAttempt(a)

	// A6: direct is a different route and is not touched by pool failure.
	e.rep.section("A6. explicit direct tag (independent of pool health)")
	echo.reset()
	a = rig.client.dial("direct", "127.0.0.1", echo.port())
	e.reportAttempt("A6", a)
	n, werr, _ = writeFor(a.conn, []byte("ping"), time.Second)
	e.rep.kv("A6 write", fmt.Sprintf("n=%d err=%s", n, oneLine(werr)))
	o = readFor(a.conn, time.Second, 64)
	e.reportRead("A6", o, a)
	e.reportPeer("A6", echo)
	closeAttempt(a)

	// A7: caller deadline while the tunnel is pending.
	e.rep.section("A7. caller deadline while the exit never answers")
	rig.dead.reset()
	a = rig.client.dialWith("deadpool", "127.0.0.1", echo.port(), 400*time.Millisecond)
	e.reportAttempt("A7", a)
	o = readFor(a.conn, 1200*time.Millisecond, 64)
	e.reportRead("A7", o, a)
	e.reportTracked("A7", a, a.track.all())
	if rig.dead.waitEOF(1500 * time.Millisecond) {
		e.rep.kv("A7 fake exit close observed", "yes")
	} else {
		e.rep.kv("A7 fake exit close observed", "NO (deadline did not tear down the tunnel)")
	}
	closeAttempt(a)

	// C: selected/forced tag vs the dispatcher's default handler.
	e.rep.section("C. routing-selected missing tag and default-handler boundary")
	e.runRouteFallback(echo)

	// B: error matrix through the healthy exit.
	e.rep.section("B. outbound error causes visible to the caller (VLESS/RAW)")
	e.runErrorCase("B1 destination refused", rig.client, "pool", "127.0.0.1", destRefused, 5*time.Second, nil)
	e.runErrorCase("B2 destination DNS failure", rig.client, "pool", "no-such-host.invalid", 80, 6*time.Second, nil)
	e.runErrorCase("B3 destination accepts then FIN", rig.client, "pool", "127.0.0.1", closeOnAccept.port(), 3*time.Second, nil)
	e.runErrorCase("B4 destination accepts, then FIN after data", rig.client, "pool", "127.0.0.1", closeOnData.port(), 3*time.Second, []byte("ping"))
	e.runResetCase("B5 destination resets after data", rig.client, resetOnData.port())

	// B6: caller cancel after a working connection; does cancel tear down?
	e.rep.section("B6. caller cancel (ctx) after a working connection")
	echo.reset()
	a = rig.client.dial("pool", "127.0.0.1", echo.port())
	e.reportAttempt("B6", a)
	_, _, _ = writeFor(a.conn, []byte("ping"), time.Second)
	_ = readFor(a.conn, time.Second, 64)
	a.cancel()
	o = readFor(a.conn, 700*time.Millisecond, 64)
	e.reportRead("B6 after cancel", o, a)
	if echo.waitEOF(1200 * time.Millisecond) {
		e.rep.kv("B6 cancel -> destination EOF", "yes")
	} else {
		e.rep.kv("B6 cancel -> destination EOF", "NO (cancel alone did not close the destination)")
	}
	closeAttempt(a)
	if echo.waitEOF(1200 * time.Millisecond) {
		e.rep.kv("B6 after explicit close", "destination EOF observed")
	} else {
		e.rep.kv("B6 after explicit close", "destination EOF NOT observed")
	}
	e.reportTracked("B6", a, a.track.all())
	e.rep.snapshot(snap("client core still up"))
}

// runErrorCase dials one destination and records the three observable channels:
// core.Dial error, tracked outbound error, and the tunnel read error.
func (e *env) runErrorCase(name string, c *client, tag, host string, port int, wait time.Duration, firstWrite []byte) {
	e.rep.section(name)
	a := c.dial(tag, host, port)
	e.reportAttempt(name, a)
	if firstWrite != nil {
		n, werr, _ := writeFor(a.conn, firstWrite, time.Second)
		e.rep.kv(name+" write", fmt.Sprintf("n=%d err=%s", n, oneLine(werr)))
	}
	o := readFor(a.conn, wait, 64)
	e.reportRead(name, o, a)
	e.reportTracked(name, a, a.track.waitError(250*time.Millisecond))
	closeAttempt(a)
}

func (e *env) runResetCase(name string, c *client, port int) {
	e.rep.section(name)
	a := c.dial("pool", "127.0.0.1", port)
	e.reportAttempt(name, a)
	n, werr, _ := writeFor(a.conn, []byte("ping"), time.Second)
	e.rep.kv(name+" first write", fmt.Sprintf("n=%d err=%s", n, oneLine(werr)))
	time.Sleep(120 * time.Millisecond)
	n, werr, _ = writeFor(a.conn, []byte("ping-again"), time.Second)
	e.rep.kv(name+" second write", fmt.Sprintf("n=%d err=%s", n, oneLine(werr)))
	e.reportTracked(name, a, a.track.waitError(500*time.Millisecond))
	o := readFor(a.conn, time.Second, 64)
	e.reportRead(name, o, a)
	closeAttempt(a)
}
