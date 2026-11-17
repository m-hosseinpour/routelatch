package main

import (
	"fmt"
	"time"
)

// runTransports walks the transport matrix. For every cell it answers the same
// three questions: does a pool dial reach the destination, what does a refused
// destination look like at the caller, and how many transport connections does
// the core keep after the user connections are closed?
func (e *env) runTransports() {
	e.rep.section("E. transport matrix")
	for _, tr := range allTransports() {
		e.rep.section("E. transport " + string(tr))
		before := snap("before " + string(tr))
		rig, err := e.newRig(tr)
		if err != nil {
			e.rep.kv("start", "FAILED: "+oneLine(err))
			continue
		}
		after := snap("after " + string(tr) + " core start")
		e.rep.snapshot(before)
		e.rep.snapshot(after)

		echo := mustPeer(e.rep, "dest-"+string(tr), peerEcho, 0)
		echo.reset()
		if rig.front != nil {
			rig.front.reset()
		}
		a := rig.client.dial("pool", "127.0.0.1", echo.port())
		e.reportAttempt("ready", a)
		accepted := echo.waitAccept(3 * time.Second)
		st := echo.state()
		e.rep.kv("ready destination accepted", accepted)
		if accepted {
			e.rep.kv("ready dial -> accept gap", delta(a.tDial, st.firstAccept))
		}
		if a.dialErr == nil {
			n, werr, _ := writeFor(a.conn, []byte("ping"), 2*time.Second)
			e.rep.kv("ready write", fmt.Sprintf("n=%d err=%s", n, oneLine(werr)))
			o := readFor(a.conn, 3*time.Second, 64)
			e.reportRead("ready", o, a)
		}
		e.reportTracked("ready", a, a.track.all())
		tClose := time.Now()
		closeAttempt(a)
		if echo.waitEOF(2 * time.Second) {
			e.rep.kv("ready close -> destination EOF", dur(time.Since(tClose)))
		} else {
			e.rep.kv("ready close -> destination EOF", "NO within 2s")
		}
		e.reportPeer("ready", echo)

		// Refused destination through the same pool tag.
		destRefused := freePort()
		a2 := rig.client.dial("pool", "127.0.0.1", destRefused)
		e.reportAttempt("refused-dest", a2)
		errs := a2.track.waitError(2500 * time.Millisecond)
		e.reportTracked("refused-dest", a2, errs)
		o := readFor(a2.conn, 2500*time.Millisecond, 64)
		e.reportRead("refused-dest", o, a2)
		closeAttempt(a2)
		if rig.socksPeer != nil {
			ss := rig.socksPeer.state()
			e.rep.kv("socks exit accepts", ss.accepts)
			e.rep.kv("socks exit destination connects", ss.connects)
			e.rep.kv("socks exit failures", fmt.Sprint(ss.failures))
		}

		// Reuse / residue: three user connections, then close only the user conns.
		if rig.front != nil {
			activeBefore := rig.front.state().active
			echo.reset()
			rig.front.reset()
			for i := 0; i < 3; i++ {
				ai := rig.client.dial("pool", "127.0.0.1", echo.port())
				if ai.dialErr != nil {
					e.rep.kv(fmt.Sprintf("reuse[%d] dial", i), oneLine(ai.dialErr))
					continue
				}
				_, _, _ = writeFor(ai.conn, []byte("ping"), time.Second)
				_ = readFor(ai.conn, 2*time.Second, 64)
				closeAttempt(ai)
			}
			echo.waitEOFCount(3, 3*time.Second)
			time.Sleep(500 * time.Millisecond)
			fs := rig.front.state()
			es := echo.state()
			e.rep.kv("reuse user dials", "3")
			e.rep.kv("reuse transport active before test", activeBefore)
			e.rep.kv("reuse transport accepts at forwarder", fs.accepts)
			e.rep.kv("reuse transport still open (500ms after close)", fs.active)
			e.rep.kv("reuse destination accepts", es.accepts)
			e.rep.kv("reuse destination EOFs", es.eofs)
			time.Sleep(2 * time.Second)
			fs2 := rig.front.state()
			e.rep.kv("reuse transport still open (2.5s after close)", fs2.active)
			e.rep.kv("reuse transport closes at forwarder", fs2.closes)
		} else {
			e.rep.kv("reuse", "not measured for socks (no transport pool expected)")
		}

		echo.close()
		rig.stop()
		time.Sleep(300 * time.Millisecond)
		e.rep.snapshot(snap("after " + string(tr) + " stop"))
	}
	e.rep.line("    transports final fds: %s (sockets=%d)", fdBreakdown(), socketFDs())
}

// runTeardown measures close/cancel propagation and resource residue.
func (e *env) runTeardown() {
	e.rep.section("D. teardown and residue")
	base := snap("process baseline before a core")
	e.rep.snapshot(base)

	rig, err := e.newRig(trVLESSRaw)
	if err != nil {
		e.rep.kv("rig", "FAILED: "+oneLine(err))
		return
	}
	echo := mustPeer(e.rep, "dest-teardown", peerEcho, 0)
	e.rep.snapshot(snap("after client core start"))

	var lat []time.Duration
	for i := 0; i < 10; i++ {
		echo.reset()
		a := rig.client.dial("pool", "127.0.0.1", echo.port())
		if a.dialErr != nil {
			e.rep.kv(fmt.Sprintf("iter[%d] dial", i), oneLine(a.dialErr))
			continue
		}
		_, _, _ = writeFor(a.conn, []byte("ping"), time.Second)
		_ = readFor(a.conn, time.Second, 64)
		tClose := time.Now()
		closeAttempt(a)
		if echo.waitEOF(2 * time.Second) {
			lat = append(lat, time.Since(tClose))
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.rep.kv("close -> destination EOF samples", fmt.Sprint(len(lat)))
	if len(lat) > 0 {
		min, max, sum := lat[0], lat[0], time.Duration(0)
		for _, d := range lat {
			if d < min {
				min = d
			}
			if d > max {
				max = d
			}
			sum += d
		}
		e.rep.kv("close -> destination EOF", fmt.Sprintf("min=%s mean=%s max=%s", dur(min), dur(sum/time.Duration(len(lat))), dur(max)))
	}
	time.Sleep(300 * time.Millisecond)
	e.rep.snapshot(snap("after 10 closed pool connections"))
	time.Sleep(2 * time.Second)
	e.rep.snapshot(snap("after 2s settle"))
	e.rep.line("    fd breakdown after settle: %s (sockets=%d)", fdBreakdown(), socketFDs())
	if rig.front != nil {
		fs := rig.front.state()
		e.rep.kv("transport conns still open after settle", fs.active)
		e.rep.kv("transport conns closed at forwarder", fs.closes)
	}

	// Pending tunnel: wait until the fake exit accepted, then close the
	// returned conn. This measures teardown of a dial that already has a TCP
	// transport but no completed proxy exchange.
	rig.dead.reset()
	a := rig.client.dial("deadpool", "127.0.0.1", echo.port())
	e.reportAttempt("pending", a)
	accepted := rig.dead.waitAccept(time.Second)
	e.rep.kv("pending fake exit accepted", accepted)
	tClose := time.Now()
	closeAttempt(a)
	if rig.dead.waitEOF(2 * time.Second) {
		e.rep.kv("pending close -> fake exit EOF", dur(time.Since(tClose)))
	} else {
		e.rep.kv("pending close -> fake exit EOF", "NO within 2s")
	}
	time.Sleep(200 * time.Millisecond)
	e.rep.snapshot(snap("after pending close"))

	// Cancel only, on a working connection.
	echo.reset()
	a = rig.client.dial("pool", "127.0.0.1", echo.port())
	_ = echo.waitAccept(time.Second)
	if a.dialErr == nil {
		_, _, _ = writeFor(a.conn, []byte("ping"), time.Second)
		_ = readFor(a.conn, time.Second, 64)
		tCancel := time.Now()
		a.cancel()
		if echo.waitEOF(1500 * time.Millisecond) {
			e.rep.kv("cancel-only -> destination EOF", dur(time.Since(tCancel)))
		} else {
			e.rep.kv("cancel-only -> destination EOF", "NO within 1.5s")
		}
		closeAttempt(a)
		if echo.waitEOF(time.Second) {
			e.rep.kv("close after cancel -> destination EOF", "yes")
		}
	}
	time.Sleep(200 * time.Millisecond)
	e.rep.snapshot(snap("after cancel + close"))

	echo.close()
	rig.stopExits()
	time.Sleep(300 * time.Millisecond)
	e.rep.snapshot(snap("after exits stopped, client core up"))
	if err := rig.client.stop(); err != nil {
		e.rep.kv("client stop error", oneLine(err))
	}
	rig.client = nil
	time.Sleep(500 * time.Millisecond)
	e.rep.snapshot(snap("after client core stop"))
	e.rep.line("    fd breakdown after stop:  %s (sockets=%d)", fdBreakdown(), socketFDs())
}
