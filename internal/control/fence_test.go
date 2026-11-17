package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/probes"
)

// poolFunc adapts a function to contract.PoolDialer inside the fence tests.
type poolFunc func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error)

func (f poolFunc) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	return f(ctx, req)
}

// approvedPoolService builds a service whose approved destinations route to the
// pool under "targeted", with the given inner pool behind the real fence. The
// seeded record is usable, so a dial that finishes under an unchanged route is
// still valid for the exit it used.
func approvedPoolService(t *testing.T, inner contract.PoolDialer) *Service {
	t.Helper()
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Rules = []contract.Rule{{
			ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com",
			Origin: contract.RuleOriginUser,
		}}
		state.Records = []contract.Record{{
			ID: "x-a", DedupeKey: "sha256:test-x-a", Protocol: contract.ProtocolVLESS,
			Server: "example.com", Port: 443, Transport: contract.TransportRaw,
			Security: contract.SecurityTLS, Status: contract.RecordUnknown,
		}}
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	svc.pool = &fencedPool{svc: svc, inner: inner}
	return svc
}

// pendingApprovedDial registers an approved pool connection in the dialing
// state, exactly as the frontend does before the pool dial starts.
func pendingApprovedDial(t *testing.T, svc *Service, host string) (contract.ConnHandle, contract.PoolDialRequest) {
	t.Helper()
	dst := contract.Destination{Kind: contract.DestDomain, Host: host, Port: 443}
	dec, err := svc.policy.Load().Decide(context.Background(), dst)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	h, err := svc.reg.Register(contract.ConnSpec{ID: contract.ConnID("c-" + host), Kind: contract.ConnHTTPConnect, Destination: dst, Decision: dec})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return h, contract.PoolDialRequest{Destination: dst, RuleID: dec.RuleID, PolicyVersion: dec.PolicyVersion}
}

// TestModeSwitchCancelsAPendingDialWithinTheBound is the F3 regression: a pool
// dial in flight must not hold the policy fence, so a mode switch commits and
// sweeps at once, canceling the pending dial through the handle context
// (docs/contracts.md §2.6, §3.4). The old code held the read lease for the
// whole dial and waited out its deadline.
func TestModeSwitchCancelsAPendingDialWithinTheBound(t *testing.T) {
	dialing := make(chan struct{})
	inner := poolFunc(func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
		close(dialing)
		<-ctx.Done()
		return contract.PoolDialResult{}, &contract.Failure{
			Kind: contract.Canceled, Path: contract.PathPool, Op: "test.pool", Message: "canceled",
		}
	})
	svc := approvedPoolService(t, inner)
	h, req := pendingApprovedDial(t, svc, "api.example.com")

	dialDone := make(chan error, 1)
	go func() {
		_, err := svc.pool.DialPool(h.Context(), req)
		dialDone <- err
	}()
	select {
	case <-dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("the pool dial never started")
	}

	// Run the switch in a goroutine with a bounded wait: the regression this
	// guards against (the fence held across the dial) makes the call block, and
	// a blocked call must fail the test rather than hang the suite.
	type modeOutcome struct {
		data ModeData
		err  error
	}
	modeDone := make(chan modeOutcome, 1)
	started := time.Now()
	go func() {
		data, err := svc.setMode(context.Background(), string(contract.ModeDirectAll))
		modeDone <- modeOutcome{data: data, err: err}
	}()
	var mode modeOutcome
	select {
	case mode = <-modeDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("the mode switch did not complete within 2s; the in-flight pool dial still holds the policy fence")
	}
	elapsed := time.Since(started)
	if mode.err != nil {
		t.Fatalf("mode switch: %v", mode.err)
	}
	data := mode.data
	if data.Closed.Pending != 1 || data.Closed.Count != 0 {
		t.Fatalf("closed=%+v, want exactly one pending cancel", data.Closed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("the mode switch waited %s for the in-flight dial; the pending-cancel bound is 50ms", elapsed)
	}
	select {
	case err := <-dialDone:
		t.Logf("mode switch completed in %s with pending=%d; the pending dial ended %s after the switch started",
			elapsed, data.Closed.Pending, time.Since(started))
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.Canceled {
			t.Fatalf("pending dial error = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pending dial was not canceled by the sweep")
	}
}

// TestStaleDialCannotCompleteAfterTheSweep is the other half of F3: releasing
// the fence must not let a dial that finishes against a superseded policy
// deliver a usable connection. The registry sweep closes its handle, and the
// fence revalidates the completed dial against the current policy (route and
// exit) and closes what it returned.
func TestStaleDialCannotCompleteAfterTheSweep(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	serverSide := make(chan net.Conn, 1)
	inner := poolFunc(func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		client, server := net.Pipe()
		serverSide <- server
		return contract.PoolDialResult{Conn: client, Exit: "x-1", Proof: contract.ProofAccepted}, nil
	})
	svc := approvedPoolService(t, inner)
	h, req := pendingApprovedDial(t, svc, "api.example.com")

	type outcome struct {
		res contract.PoolDialResult
		err error
	}
	dialDone := make(chan outcome, 1)
	go func() {
		res, err := svc.pool.DialPool(h.Context(), req)
		dialDone <- outcome{res: res, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the pool dial never started")
	}

	// The mode switch commits and sweeps while the dial is still in flight;
	// its cancel is not observed by this deliberately stubborn inner dial. The
	// bounded wait keeps an old-fence regression a failure, not a hang.
	modeErr := make(chan error, 1)
	go func() {
		_, err := svc.setMode(context.Background(), string(contract.ModeDirectAll))
		modeErr <- err
	}()
	select {
	case err := <-modeErr:
		if err != nil {
			t.Fatalf("mode switch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the mode switch did not complete within 2s; the in-flight pool dial still holds the policy fence")
	}
	close(release)

	var got outcome
	select {
	case got = <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the stale dial never returned")
	}
	server := <-serverSide
	defer server.Close()
	var failure *contract.Failure
	if !errors.As(got.err, &failure) || failure.Kind != contract.Canceled {
		t.Fatalf("stale dial = conn %v, err %v; want a canceled failure", got.res.Conn != nil, got.err)
	}
	if got.res.Conn != nil {
		t.Fatal("the stale dial returned a connection")
	}
	// The connection the dial did produce must have been closed.
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := server.Read(one[:]); err == nil {
		t.Fatal("the stale connection is still usable after the sweep")
	}
}

// TestAuditOnlyVersionBumpKeepsAPendingDial: §2.5 says `pool select` raises
// PolicyVersion for audit but closes no connection because the route does not
// change. A pool dial in flight when that commit lands must therefore be kept —
// the same rule the sweep applies to connections that are already live — while
// a route-changing commit still rejects it.
func TestAuditOnlyVersionBumpKeepsAPendingDial(t *testing.T) {
	dialing := make(chan struct{})
	release := make(chan struct{})
	inner := poolFunc(func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
		close(dialing)
		select {
		case <-release:
		case <-ctx.Done():
		}
		client, server := net.Pipe()
		_ = server.Close()
		return contract.PoolDialResult{Conn: client, Exit: "x-a", Proof: contract.ProofAccepted}, nil
	})
	svc := approvedPoolService(t, inner)
	h, req := pendingApprovedDial(t, svc, "api.example.com")

	dialDone := make(chan error, 1)
	go func() {
		_, err := svc.pool.DialPool(h.Context(), req)
		dialDone <- err
	}()
	select {
	case <-dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("the pool dial never started")
	}

	// The observable commit of an audit-only change: a new version and a sweep
	// whose keep predicate leaves the route unchanged.
	after, nextPolicy, err := svc.mutateAndCommit(context.Background(), func(st *contract.State) error {
		st.PolicyVersion++
		return nil
	})
	if err != nil {
		t.Fatalf("audit-only commit: %v", err)
	}
	if _, err := svc.sweep(context.Background(), contract.CloseRuleChange, after.State.PolicyVersion, routeKeep(nextPolicy)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	close(release)

	select {
	case err := <-dialDone:
		if err != nil {
			t.Fatalf("the audit-only version bump cancelled a dial the sweep kept: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pending dial never returned")
	}
}

// TestProbeEngineCooldownMapsToACommandConflict is the F2 regression: the
// prober's own destination cooldown can outlast the diagnostics cooldown, and
// it must stay inside the documented proposals exit set (6 = conflict), not
// surface as kind=internal with exit 1 (docs/contracts.md §7.1).
func TestProbeEngineCooldownMapsToACommandConflict(t *testing.T) {
	for _, err := range []error{
		probes.ErrProbeCooldown,
		fmt.Errorf("recheck: %w", probes.ErrProbeCooldown),
		probes.ErrProbeInFlight,
	} {
		mapped := mapDiagnosticsError(err)
		var opErr *OpError
		if !errors.As(mapped, &opErr) {
			t.Fatalf("%v mapped to %T, want a typed OpError", err, mapped)
		}
		if opErr.Kind != contract.StateLocked || opErr.Exit != contract.ExitConflict {
			t.Fatalf("%v mapped to kind=%q exit=%d, want state_locked/6", err, opErr.Kind, opErr.Exit)
		}
		if want := contract.ExitCodeFor[opErr.Kind]; opErr.Exit != want {
			t.Fatalf("%v: exit override %d disagrees with the kind mapping %d", err, opErr.Exit, want)
		}
	}
}
