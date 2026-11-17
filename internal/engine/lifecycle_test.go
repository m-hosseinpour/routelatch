package engine_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

// TestStopClosesLiveConnectionsAndLeavesNoResidue is the teardown check for the
// engine boundary: Stop closes the pooled connections it handed out, then the
// core, and neither sockets nor goroutines attributable to the raw path remain
// (spike/xray/FINDINGS.md §4). The exit child was already stopped before the
// residue is measured, so only the engine is under observation.
func TestStopClosesLiveConnectionsAndLeavesNoResidue(t *testing.T) {
	echo := newPeer(t, "echo")
	uuid := randomUUID(t)
	exit := startExit(t, exitSpec{Protocol: "vless", Port: int(freePort(t)), UUID: uuid})

	baselineGoroutines := runtime.NumGoroutine()
	baselineSockets := socketFDs(t)

	e, err := engine.Start(engine.Options{
		Exits:         []contract.Record{vlessRecord("x-vless", "127.0.0.1", uint16(exit.port), uuid, nil)},
		PolicyVersion: 1,
		LogLevel:      "none",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	closed, err := e.Dial(context.Background(), dialRequest("x-vless", ipDestination(echo.port()), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := closed.Conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !echo.waitAccepts(1, 3*time.Second) {
		t.Fatalf("first dial never reached the destination")
	}
	if err := closed.Conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := closed.Conn.Write([]byte("y")); err == nil {
		t.Fatalf("a closed pooled connection accepted a write")
	}

	live, err := e.Dial(context.Background(), dialRequest("x-vless", ipDestination(echo.port()), 1))
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if !echo.waitAccepts(2, 3*time.Second) {
		t.Fatalf("live connection never reached the destination")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := e.Stop(stopCtx); err != nil {
		t.Fatalf("second stop: %v", err)
	}

	// Our side is closed immediately, and the client is told it was a cancel.
	got := readWithin(live.Conn, time.Second)
	if got.err == nil {
		t.Fatalf("read after Stop succeeded")
	}
	if kind := failureKind(t, got.err); kind != contract.Canceled {
		t.Fatalf("read after Stop kind = %q, want canceled", kind)
	}

	_, err = e.Dial(context.Background(), dialRequest("x-vless", ipDestination(echo.port()), 1))
	if kind := failureKind(t, err); kind != contract.Stopped {
		t.Fatalf("dial after Stop kind = %q, want stopped", kind)
	}

	exit.stop()
	time.Sleep(200 * time.Millisecond)

	gotGoroutines := waitGoroutines(t, baselineGoroutines, 3*time.Second)
	gotSockets := socketFDs(t)
	t.Logf("after Stop: goroutines %d (baseline %d), sockets %d (baseline %d)",
		gotGoroutines, baselineGoroutines, gotSockets, baselineSockets)
	if gotGoroutines > baselineGoroutines+2 {
		t.Fatalf("goroutines after Stop = %d, baseline %d", gotGoroutines, baselineGoroutines)
	}
	if gotSockets > baselineSockets {
		t.Fatalf("socket fds after Stop = %d, baseline %d", gotSockets, baselineSockets)
	}
}

// TestStopUnblocksBlockedReads checks that a user copy loop blocked in Read is
// released by Stop rather than left behind.
func TestStopUnblocksBlockedReads(t *testing.T) {
	silent := newPeer(t, "accept-silent")
	uuid := randomUUID(t)
	e, err := engine.Start(engine.Options{
		Exits:         []contract.Record{vlessRecord("x-silent", "127.0.0.1", silent.port(), uuid, nil)},
		PolicyVersion: 1,
		LogLevel:      "none",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := e.Dial(context.Background(), dialRequest("x-silent", ipDestination(silent.port()), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := res.Conn.Read(buf)
		readDone <- err
	}()
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatalf("blocked read returned success after Stop")
		}
		if elapsed := time.Since(start); elapsed > contract.LocalCloseDeadline {
			t.Fatalf("blocked read was released after %v, bound %v", elapsed, contract.LocalCloseDeadline)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("blocked read was not released by Stop")
	}
}
