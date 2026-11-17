package conns_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/conns"
	"routelatch/internal/contract"
)

func tcpPair(t *testing.T) (local, peer net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	local, err = net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	peer = <-accepted
	if peer == nil {
		t.Fatalf("accept failed")
	}
	t.Cleanup(func() {
		_ = local.Close()
		_ = peer.Close()
		_ = l.Close()
	})
	return local, peer
}

// waitClosed returns how long it took peer to observe the local end going away,
// and whether it happened inside d.
func waitClosed(peer net.Conn, d time.Duration) (time.Duration, bool) {
	start := time.Now()
	_ = peer.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 32)
	for {
		if _, err := peer.Read(buf); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return time.Since(start), false
			}
			return time.Since(start), true
		}
	}
}

func spec(id string, route contract.Route, version contract.PolicyVersion) contract.ConnSpec {
	return contract.ConnSpec{
		ID:   contract.ConnID(id),
		Kind: contract.ConnSOCKS5Connect,
		Destination: contract.Destination{
			Kind: contract.DestDomain,
			Host: "example.test",
			Port: 443,
		},
		Decision: contract.Decision{
			Mode:          contract.ModeTargeted,
			Class:         contract.ClassApproved,
			Route:         route,
			Reason:        contract.ReasonApprovedRule,
			PolicyVersion: version,
		},
	}
}

func mustRegister(t *testing.T, r *conns.Registry, s contract.ConnSpec) contract.ConnHandle {
	t.Helper()
	h, err := r.Register(s)
	if err != nil {
		t.Fatalf("register %s: %v", s.ID, err)
	}
	return h
}

func TestRegisterFencesStalePolicyVersion(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, spec("c-1", contract.RouteDirect, 4))
	if upstream, _ := tcpPair(t); true {
		if err := h.SetUpstream(upstream); err != nil {
			t.Fatalf("set upstream: %v", err)
		}
	}
	if got := r.Version(); got != 0 {
		t.Fatalf("initial version = %d, want 0", got)
	}

	rep, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 5, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rep.FromVersion != 0 || rep.ToVersion != 5 || rep.Closed != 1 || rep.PendingCancel != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if !errors.Is(h.Context().Err(), context.Canceled) {
		t.Fatalf("closed conn context = %v, want canceled", h.Context().Err())
	}

	_, err = r.Register(spec("c-stale", contract.RouteDirect, 4))
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.Canceled {
		t.Fatalf("stale register error = %v (%T), want canceled failure", err, err)
	}

	fresh := mustRegister(t, r, spec("c-2", contract.RouteDirect, 5))
	if got := fresh.ID(); got != "c-2" {
		t.Fatalf("fresh handle id = %q", got)
	}
}

func TestSetVersionSeedsTheFence(t *testing.T) {
	r := conns.New()
	r.SetVersion(5)
	if _, err := r.Register(spec("old", contract.RouteDirect, 4)); failureKindOf(err) != contract.Canceled {
		t.Fatalf("stale register after seed = %v, want canceled", err)
	}
	mustRegister(t, r, spec("current", contract.RouteDirect, 5))
	r.SetVersion(3) // must not move backwards
	if got := r.Version(); got != 5 {
		t.Fatalf("version moved backwards to %d", got)
	}
}

func failureKindOf(err error) contract.ErrorKind {
	var f *contract.Failure
	if !errors.As(err, &f) {
		return ""
	}
	return f.Kind
}

func TestRegisterRejectsDuplicateLiveID(t *testing.T) {
	r := conns.New()
	mustRegister(t, r, spec("dup", contract.RouteDirect, 1))
	_, err := r.Register(spec("dup", contract.RoutePool, 1))
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.Internal {
		t.Fatalf("duplicate register error = %v, want internal failure", err)
	}
}

func TestCloseConflictingUsesKeepPredicate(t *testing.T) {
	r := conns.New()
	direct := mustRegister(t, r, spec("direct", contract.RouteDirect, 1))
	pool := mustRegister(t, r, spec("pool", contract.RoutePool, 1))
	clientDirect, clientDirectPeer := tcpPair(t)
	directConn, directPeer := tcpPair(t)
	if err := direct.SetClient(clientDirect); err != nil {
		t.Fatalf("set client: %v", err)
	}
	if err := direct.SetUpstream(directConn); err != nil {
		t.Fatalf("set upstream: %v", err)
	}
	poolConn, poolPeer := tcpPair(t)
	if err := pool.SetClient(poolConn); err != nil {
		t.Fatalf("set client: %v", err)
	}
	if err := pool.SetUpstream(poolPeer); err != nil {
		t.Fatalf("set upstream: %v", err)
	}

	rep, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 2, func(info contract.ConnInfo) bool {
		return info.Route == contract.RouteDirect
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rep.Closed != 1 || rep.Kept != 1 || rep.PendingCancel != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", rep.Errors)
	}

	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].ID != "direct" {
		t.Fatalf("snapshot after sweep = %+v", snap)
	}
	if direct.Context().Err() != nil {
		t.Fatalf("kept conn context canceled: %v", direct.Context().Err())
	}
	if pool.Context().Err() == nil {
		t.Fatalf("closed conn context is still live")
	}
	if _, ok := waitClosed(poolPeer, time.Second); !ok {
		t.Fatalf("closed upstream peer did not observe teardown")
	}

	// The kept connection is still usable in both directions.
	payload := []byte("still-open")
	if _, err := clientDirectPeer.Write(payload); err != nil {
		t.Fatalf("write kept conn: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := clientDirect.Read(buf); err != nil {
		t.Fatalf("read kept conn: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("kept conn payload = %q", buf)
	}
	_ = directPeer
}

func TestSweepCancelsPendingDialInsideDeadline(t *testing.T) {
	r := conns.New()
	pending := mustRegister(t, r, spec("pending", contract.RoutePool, 1))
	client, clientPeer := tcpPair(t)
	if err := pending.SetClient(client); err != nil {
		t.Fatalf("set client: %v", err)
	}
	upstream, upstreamPeer := tcpPair(t)
	if err := mustRegister(t, r, spec("live", contract.RouteDirect, 1)).SetUpstream(upstream); err != nil {
		t.Fatalf("set upstream: %v", err)
	}

	// The dial observes the handle context exactly as a real dial does.
	dialDone := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		<-pending.Context().Done()
		dialDone <- time.Since(start)
	}()

	rep, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 2, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rep.Closed != 1 || rep.PendingCancel != 1 || rep.Kept != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.Duration > contract.LocalCloseDeadline {
		t.Fatalf("local sweep took %v, bound %v", rep.Duration, contract.LocalCloseDeadline)
	}

	select {
	case d := <-dialDone:
		if d > contract.PendingDialCancelDeadline {
			t.Fatalf("pending dial observed cancel after %v, bound %v", d, contract.PendingDialCancelDeadline)
		}
	case <-time.After(contract.PendingDialCancelDeadline):
		t.Fatalf("pending dial was not canceled inside %v", contract.PendingDialCancelDeadline)
	}
	if d, ok := waitClosed(clientPeer, contract.ExitObserveFastPolicy); !ok {
		t.Fatalf("client peer did not observe teardown inside %v (waited %v)", contract.ExitObserveFastPolicy, d)
	}
	if d, ok := waitClosed(upstreamPeer, contract.ExitObserveFastPolicy); !ok {
		t.Fatalf("upstream peer did not observe teardown inside %v (waited %v)", contract.ExitObserveFastPolicy, d)
	}
}

func TestSweepKeepsDialingOnlyWhenKeepAllows(t *testing.T) {
	r := conns.New()
	mustRegister(t, r, spec("pending", contract.RoutePool, 1))
	rep, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 2, func(contract.ConnInfo) bool { return true })
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rep.Kept != 1 || rep.PendingCancel != 0 || rep.Closed != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if len(r.Snapshot()) != 1 {
		t.Fatalf("kept dialing connection is missing from snapshot")
	}
	// Keeping a connection does not reopen the fence it was admitted under.
	if _, err := r.Register(spec("stale", contract.RoutePool, 1)); failureKindOf(err) != contract.Canceled {
		t.Fatalf("stale register after a keeping sweep = %v, want canceled", err)
	}
}

func TestCloseAllClosesEverything(t *testing.T) {
	r := conns.New()
	if _, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 3, nil); err != nil {
		t.Fatalf("initial sweep: %v", err)
	}
	for _, s := range []contract.ConnSpec{spec("a", contract.RouteDirect, 3), spec("b", contract.RoutePool, 3)} {
		h := mustRegister(t, r, s)
		if upstream, _ := tcpPair(t); true {
			if err := h.SetUpstream(upstream); err != nil {
				t.Fatalf("set upstream: %v", err)
			}
		}
	}
	rep, err := r.CloseAll(context.Background(), contract.CloseStop)
	if err != nil {
		t.Fatalf("close all: %v", err)
	}
	if rep.Cause != contract.CloseStop || rep.Closed != 2 || rep.Kept != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.FromVersion != 3 || rep.ToVersion != 3 {
		t.Fatalf("close all changed the version: %+v", rep)
	}
	if got := len(r.Snapshot()); got != 0 {
		t.Fatalf("snapshot after close all has %d conns", got)
	}
}

func TestSnapshotReportsStateWithoutUpstreamAddress(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, contract.ConnSpec{
		ID:   "c-1",
		Kind: contract.ConnHTTPConnect,
		Destination: contract.Destination{
			Kind: contract.DestDomain,
			Host: "api.example.test",
			Port: 443,
		},
		Decision: contract.Decision{
			Mode:          contract.ModeTargeted,
			Class:         contract.ClassApproved,
			Route:         contract.RoutePool,
			Reason:        contract.ReasonApprovedRule,
			PolicyVersion: 1,
		},
	})
	upstream, peer := tcpPair(t)
	peerAddr := peer.RemoteAddr().String()
	if err := h.SetUpstream(upstream); err != nil {
		t.Fatalf("set upstream: %v", err)
	}
	h.SetExit("x-1")
	h.SetProof(contract.ProofConfirmed)
	if bc, ok := h.(conns.ByteCounter); ok {
		bc.AddBytes(12, 34)
	} else {
		t.Fatalf("handle does not implement ByteCounter")
	}

	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d", len(snap))
	}
	info := snap[0]
	if info.State != contract.ConnOpen || info.Proof != contract.ProofConfirmed {
		t.Fatalf("unexpected state/proof: %+v", info)
	}
	if info.Exit != "x-1" || info.BytesUp != 12 || info.BytesDown != 34 {
		t.Fatalf("unexpected exit/counters: %+v", info)
	}
	if info.Route != contract.RoutePool || info.PolicyVersion != 1 {
		t.Fatalf("unexpected decision fields: %+v", info)
	}

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), peerAddr) {
		t.Fatalf("snapshot leaks the upstream address %q: %s", peerAddr, raw)
	}
}

func TestProofAndExitAreSetOnce(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, spec("c-1", contract.RoutePool, 1))
	h.SetProof(contract.ProofAccepted)
	h.SetProof(contract.ProofConfirmed)
	h.SetExit("x-1")
	h.SetExit("x-2")

	info := r.Snapshot()[0]
	if info.Proof != contract.ProofAccepted {
		t.Fatalf("proof = %q, want accepted (set once)", info.Proof)
	}
	if info.Exit != "x-1" {
		t.Fatalf("exit = %q, want x-1 (set once)", info.Exit)
	}
}

func TestClosedHandleIsNotReusable(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, spec("c-1", contract.RouteDirect, 1))
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if h.Context().Err() == nil {
		t.Fatalf("closed handle context is not canceled")
	}
	if got := len(r.Snapshot()); got != 0 {
		t.Fatalf("closed handle still in snapshot")
	}

	local, peer := tcpPair(t)
	err := h.SetUpstream(local)
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.Canceled {
		t.Fatalf("SetUpstream on closed handle = %v, want canceled failure", err)
	}
	if _, ok := waitClosed(peer, contract.ExitObserveFastPolicy); !ok {
		t.Fatalf("SetUpstream on a closed handle leaked the conn")
	}

	// A closed id may be reused by a genuinely new connection.
	next := mustRegister(t, r, spec("c-1", contract.RoutePool, 1))
	if next.Context().Err() != nil {
		t.Fatalf("new handle has a canceled context")
	}
}

func TestSetClientKeepsFirstConn(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, spec("c-1", contract.RouteDirect, 1))
	first, _ := tcpPair(t)
	second, secondPeer := tcpPair(t)
	if err := h.SetClient(first); err != nil {
		t.Fatalf("set client: %v", err)
	}
	if err := h.SetClient(second); err != nil {
		t.Fatalf("second set client: %v", err)
	}
	if _, ok := waitClosed(secondPeer, contract.ExitObserveFastPolicy); !ok {
		t.Fatalf("duplicate SetClient leaked its conn")
	}
	if h.Context().Err() != nil {
		t.Fatalf("duplicate SetClient closed the handle")
	}
}

func TestConcurrentRegisterAndSweepFencesEverySurvivor(t *testing.T) {
	r := conns.New()
	const writers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < 40; i++ {
				id := contract.ConnID(strings.Join([]string{"w", string(rune('a' + w)), string(rune('a' + i%26)), string(rune('0' + i/26))}, ""))
				version := contract.PolicyVersion(1 + i%3)
				_, err := r.Register(spec(string(id), contract.RoutePool, version))
				if err == nil {
					continue
				}
				var f *contract.Failure
				if !errors.As(err, &f) || f.Kind != contract.Canceled {
					t.Errorf("unexpected register error: %v", err)
				}
			}
		}(w)
	}
	close(start)
	for v := contract.PolicyVersion(2); v <= 4; v++ {
		if _, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, v, nil); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}
	wg.Wait()

	final, _ := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, 5, nil)
	for _, info := range r.Snapshot() {
		t.Fatalf("connection %s survived the final sweep: %+v", info.ID, final)
	}
}

// TestSweepLatencyBudget measures what the contract bounds instead of assuming
// it: our local sweep and the pending-dial cancel both have a p99 budget of 50ms
// (docs/contracts.md §3.4). Forty rounds are enough to catch a regression that
// serializes on a copy loop or a dead peer.
func TestSweepLatencyBudget(t *testing.T) {
	r := conns.New()
	const rounds = 40
	sweeps := make([]time.Duration, 0, rounds)
	cancels := make([]time.Duration, 0, rounds)

	for i := 0; i < rounds; i++ {
		version := contract.PolicyVersion(i + 1)
		live := mustRegister(t, r, spec("live", contract.RouteDirect, version))
		upstream, _ := tcpPair(t)
		if err := live.SetUpstream(upstream); err != nil {
			t.Fatalf("set upstream: %v", err)
		}
		pending := mustRegister(t, r, spec("pending", contract.RoutePool, version))
		canceled := make(chan time.Duration, 1)
		go func() {
			start := time.Now()
			<-pending.Context().Done()
			canceled <- time.Since(start)
		}()

		started := time.Now()
		rep, err := r.CloseConflicting(context.Background(), contract.CloseModeSwitch, version+1, nil)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if rep.Closed != 1 || rep.PendingCancel != 1 {
			t.Fatalf("sweep %d report: %+v", i, rep)
		}
		sweeps = append(sweeps, elapsed)

		select {
		case d := <-canceled:
			cancels = append(cancels, d)
		case <-time.After(contract.PendingDialCancelDeadline):
			t.Fatalf("pending dial %d was not canceled inside %v", i, contract.PendingDialCancelDeadline)
		}
	}

	p99Sweep := percentile(sweeps, 0.99)
	p99Cancel := percentile(cancels, 0.99)
	t.Logf("sweep p99=%v max=%v; pending cancel p99=%v max=%v (bounds %v)",
		p99Sweep, maxDuration(sweeps), p99Cancel, maxDuration(cancels), contract.LocalCloseDeadline)
	if p99Sweep > contract.LocalCloseDeadline {
		t.Fatalf("sweep p99 = %v, bound %v", p99Sweep, contract.LocalCloseDeadline)
	}
	if p99Cancel > contract.PendingDialCancelDeadline {
		t.Fatalf("pending cancel p99 = %v, bound %v", p99Cancel, contract.PendingDialCancelDeadline)
	}
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func maxDuration(values []time.Duration) time.Duration {
	var out time.Duration
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}

// blockingConn makes Close slow, to prove that neither a second Close nor a
// sweep reports completion before the socket is actually closed (contract
// ConnHandle/CloseConflicting invariants).
type blockingConn struct {
	net.Conn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return c.Conn.Close()
}

func TestConcurrentCloseWaitsForTheSocket(t *testing.T) {
	r := conns.New()
	h := mustRegister(t, r, spec("c-1", contract.RouteDirect, 1))
	upstream, peer := tcpPair(t)
	blocking := &blockingConn{Conn: upstream, started: make(chan struct{}), release: make(chan struct{})}
	if err := h.SetUpstream(blocking); err != nil {
		t.Fatalf("set upstream: %v", err)
	}

	first := make(chan error, 1)
	go func() { first <- h.Close() }()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatalf("first close never reached the socket")
	}

	second := make(chan error, 1)
	go func() { second <- h.Close() }()
	sweep := make(chan contract.CloseReport, 1)
	go func() {
		rep, _ := r.CloseConflicting(context.Background(), contract.CloseClientGone, 2, nil)
		sweep <- rep
	}()

	select {
	case <-second:
		t.Fatalf("second Close returned while the socket was still open")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case rep := <-sweep:
		t.Fatalf("sweep returned while the socket was still open: %+v", rep)
	case <-time.After(50 * time.Millisecond):
	}

	close(blocking.release)
	if err := <-first; err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second close: %v", err)
	}
	rep := <-sweep
	if rep.Closed != 0 || rep.PendingCancel != 0 {
		t.Fatalf("sweep counted a connection its client closed: %+v", rep)
	}
	if _, ok := waitClosed(peer, contract.ExitObserveFastPolicy); !ok {
		t.Fatalf("peer did not observe teardown after the release")
	}
}
