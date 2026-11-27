package control

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func egressFailure(kind contract.ErrorKind, host string) *contract.Failure {
	return &contract.Failure{
		Kind: kind, Path: contract.PathPool, Op: "pool.dial", Exit: "x-canary",
		Destination: contract.Destination{Kind: contract.DestDomain, Host: host, Port: 443},
		Message:     "the exit transport failed",
	}
}

// TestStatusReportsRecentApprovedEgressDialFailure pins R-28: an exit-level
// dial failure that fails the request closed must be visible in status without
// exposing the destination or exit.
func TestStatusReportsRecentApprovedEgressDialFailure(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.now = time.Now
	canary := "canary.egress-warning.invalid"

	before, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(contract.ExitUnreachable, canary)}}
	if _, err := p.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
		t.Fatal("the fake exit failure was not returned")
	}
	after, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !warningsContain(after.Warnings, egressFailureWarning) {
		t.Fatalf("warnings=%v, want the approved-egress failure warning", after.Warnings)
	}
	if after.Pool != before.Pool {
		t.Fatalf("pool counts changed: before=%+v after=%+v", before.Pool, after.Pool)
	}

	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), egressFailureWarning) {
		t.Fatalf("status JSON lacks the warning: %s", raw)
	}
	for _, secret := range []string{canary, "x-canary"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status JSON leaked %q: %s", secret, raw)
		}
	}
}

func TestSuccessfulApprovedDialClearsEgressFailure(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.now = time.Now

	failed := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(contract.ExitUnreachable, "a.invalid")}}
	if _, err := failed.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
		t.Fatal("the fake exit failure was not returned")
	}
	if status, err := svc.status(ctx); err != nil || !warningsContain(status.Warnings, egressFailureWarning) {
		t.Fatalf("warning missing after failure: %v (%v)", status.Warnings, err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ok := &usagePool{svc: svc, inner: &usageFakePool{res: contract.PoolDialResult{
		Conn: client, Exit: "x-a", Proof: contract.ProofAccepted,
	}}}
	res, err := ok.DialPool(ctx, contract.PoolDialRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Conn.Close()
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if warningsContain(status.Warnings, egressFailureWarning) {
		t.Fatalf("warnings=%v, want the success to clear the signal", status.Warnings)
	}
}

func TestEgressFailureWarningExpires(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	p := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(contract.ExitUnreachable, "a.invalid")}}
	if _, err := p.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
		t.Fatal("the fake exit failure was not returned")
	}
	for _, step := range []struct {
		advance time.Duration
		want    bool
	}{
		{59 * time.Second, true},
		{2 * time.Second, false}, // now past the 60s TTL
	} {
		now = now.Add(step.advance)
		status, err := svc.status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := warningsContain(status.Warnings, egressFailureWarning); got != step.want {
			t.Fatalf("after +%s: warning=%v, want %v (%v)", step.advance, got, step.want, status.Warnings)
		}
	}
}

func TestDestinationFailuresDoNotSetEgressWarning(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []contract.ErrorKind{
		contract.DestinationUnconfirmed, contract.DestinationRefused,
		contract.PoolUnavailable, contract.InvalidDestination,
		contract.ConnectRefused, contract.Canceled,
	} {
		t.Run(string(kind), func(t *testing.T) {
			st, paths := newServiceStore(t)
			svc := newTestService(t, st, paths)
			svc.now = time.Now

			p := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(kind, "a.invalid")}}
			if _, err := p.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
				t.Fatal("the fake failure was not returned")
			}
			status, err := svc.status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if warningsContain(status.Warnings, egressFailureWarning) {
				t.Fatalf("warnings=%v, want no egress warning for %s", status.Warnings, kind)
			}
		})
	}
}

// TestOverBroadTransportKindsDoNotSetEgressWarning is the regression for the
// review finding: SOCKS5 replies 0x03/0x04 (network/host unreachable) surface
// as HandshakeRejected, and Timeout can come from a destination-side stall that
// expires the exit handshake deadline. Neither is evidence that the exit's own
// transport failed, so neither may raise the R-28 warning (docs/contracts.md R-28).
func TestOverBroadTransportKindsDoNotSetEgressWarning(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []contract.ErrorKind{
		contract.HandshakeRejected, contract.Timeout,
	} {
		t.Run(string(kind), func(t *testing.T) {
			st, paths := newServiceStore(t)
			svc := newTestService(t, st, paths)
			svc.now = time.Now

			p := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(kind, "a.invalid")}}
			if _, err := p.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
				t.Fatal("the fake failure was not returned")
			}
			status, err := svc.status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if warningsContain(status.Warnings, egressFailureWarning) {
				t.Fatalf("warnings=%v, want no egress warning for %s", status.Warnings, kind)
			}
		})
	}
}

// TestEgressFailureDoesNotMutateHealth keeps the signal observational: no
// record is cooled, no health row appears and the persisted state is untouched.
func TestEgressFailureDoesNotMutateHealth(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	seedUsageRecord(t, st, "x-a")
	svc := newTestService(t, st, paths)
	svc.now = time.Now

	before := svc.Snapshot().State
	persistedBefore, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &usagePool{svc: svc, inner: &usageFakePool{err: egressFailure(contract.ExitUnreachable, "a.invalid")}}
	if _, err := p.DialPool(ctx, contract.PoolDialRequest{}); err == nil {
		t.Fatal("the fake exit failure was not returned")
	}
	if after := svc.Snapshot().State; !reflect.DeepEqual(before, after) {
		t.Fatalf("in-memory state changed:\nbefore=%+v\nafter=%+v", before, after)
	}
	persistedAfter, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedBefore.State, persistedAfter.State) {
		t.Fatalf("persisted state changed:\nbefore=%+v\nafter=%+v", persistedBefore.State, persistedAfter.State)
	}
	if len(persistedAfter.State.Health) != 0 {
		t.Fatalf("health rows = %+v, want none", persistedAfter.State.Health)
	}
}
