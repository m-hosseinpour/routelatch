package pool

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func t0() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) }

func record(id string, order int, status contract.RecordStatus) contract.Record {
	return contract.Record{
		ID: contract.RecordID(id), DedupeKey: "sha256:" + strings.Repeat("0", 64),
		Protocol: contract.ProtocolVLESS, Server: "example.com", Port: 443,
		Transport: contract.TransportRaw, Security: contract.SecurityTLS,
		Status: status, Order: order,
	}
}

func health(exit contract.ExitID, status contract.HealthStatus, ewma int64, samples int) contract.ExitHealth {
	return contract.ExitHealth{Exit: exit, Status: status, EWMALatencyMS: ewma, Samples: samples}
}

func TestEffectiveStatusAndCounts(t *testing.T) {
	now := t0()
	state := contract.State{Records: []contract.Record{
		record("x-a", 0, contract.RecordHealthy),
		record("x-b", 1, contract.RecordCooling),
		record("x-c", 2, contract.RecordUnknown),
		record("x-d", 3, contract.RecordUnsupported),
		record("x-e", 4, contract.RecordInvalid),
		record("x-f", 5, contract.RecordCooling),
	}, Health: []contract.ExitHealth{
		health("x-a", contract.HealthHealthy, 100, 3),
		{Exit: "x-b", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute), LastFailureKind: contract.ExitUnreachable},
		{Exit: "x-f", Status: contract.HealthCooling, CooldownUntil: now.Add(-time.Minute)},
	}}
	p := Build(state, now)
	counts := p.Counts()
	if counts.Total != 6 || counts.Healthy != 1 || counts.Cooling != 1 || counts.Unknown != 2 || counts.Unsupported != 1 || counts.Invalid != 1 || counts.Usable != 3 {
		t.Fatalf("counts=%+v", counts)
	}
	if rec, _ := p.Record("x-f"); rec.Status != contract.RecordUnknown {
		t.Fatalf("expired cooldown status=%s", rec.Status)
	}
	if rec, _ := p.Record("x-b"); rec.Status != contract.RecordCooling || !strings.Contains(rec.StatusReason, "exit_unreachable") {
		t.Fatalf("cooling record=%+v", rec)
	}
}

func TestCooldownSequence(t *testing.T) {
	cases := map[int]time.Duration{
		0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute,
		4: 15 * time.Minute, 9: 15 * time.Minute,
	}
	for n, want := range cases {
		if got := CooldownDuration(n); got != want {
			t.Fatalf("CooldownDuration(%d)=%v want %v", n, got, want)
		}
	}
}

func TestDirectProbeNeverCoolsExit(t *testing.T) {
	now := t0()
	st := &contract.State{Records: []contract.Record{record("x-a", 0, contract.RecordHealthy)}}
	ApplyProbe(st, contract.ProbeResult{
		Kind: contract.ProbeDirectTCP, Exit: "x-a", Outcome: contract.OutcomeTimeout, Failure: contract.Timeout,
	}, now)
	if len(st.Health) != 0 || st.Records[0].Status != contract.RecordHealthy {
		t.Fatalf("direct probe cooled an exit: %+v", st.Health)
	}
	// A result with no exit must not create a phantom health row either.
	ApplyProbe(st, contract.ProbeResult{Kind: contract.ProbeEgress, Exit: "", Outcome: contract.OutcomeSuccess, BytesDown: 1}, now)
	if len(st.Health) != 0 {
		t.Fatalf("phantom health row: %+v", st.Health)
	}
}

func TestEWMAAndAttribution(t *testing.T) {
	now := t0()
	st := &contract.State{Records: []contract.Record{record("x-a", 0, contract.RecordUnknown)}}
	RecordUsage(st, "x-a", 200*time.Millisecond, now)
	if st.Health[0].EWMALatencyMS != 200 || st.Health[0].Status != contract.HealthHealthy {
		t.Fatalf("first sample: %+v", st.Health[0])
	}
	RecordUsage(st, "x-a", 100*time.Millisecond, now.Add(time.Second))
	if st.Health[0].EWMALatencyMS != 170 {
		t.Fatalf("ewma=%d want 170", st.Health[0].EWMALatencyMS)
	}
	if st.Records[0].Status != contract.RecordHealthy {
		t.Fatalf("record status=%s", st.Records[0].Status)
	}

	// A destination-level outcome never cools an exit.
	for _, res := range []contract.ProbeResult{
		{Exit: "x-a", Kind: contract.ProbeEgress, Outcome: contract.OutcomeUnconfirmed, Failure: contract.DestinationUnconfirmed, Attribution: contract.ProbeAttributionExit},
		{Exit: "x-a", Kind: contract.ProbeEgress, Outcome: contract.OutcomeCanceled, Failure: contract.Canceled, Attribution: contract.ProbeAttributionExit},
		{Exit: "x-a", Kind: contract.ProbeEgress, Outcome: contract.OutcomeRefused, Failure: contract.DestinationRefused, Attribution: contract.ProbeAttributionExit},
	} {
		ApplyProbe(st, res, now.Add(2*time.Second))
		if st.Health[0].Status != contract.HealthHealthy || st.Health[0].ConsecutiveFailures != 0 {
			t.Fatalf("outcome %s destroyed health: %+v", res.Outcome, st.Health[0])
		}
	}

	// A dial-phase transport failure cools; four reach the cap.
	for i := 1; i <= 4; i++ {
		ApplyProbe(st, contract.ProbeResult{
			Exit: "x-a", Kind: contract.ProbeTransport, Outcome: contract.OutcomeTimeout,
			Failure: contract.Timeout, Attribution: contract.ProbeAttributionExit,
		}, now.Add(time.Duration(i)*time.Second))
	}
	h := st.Health[0]
	if h.Status != contract.HealthCooling || h.ConsecutiveFailures != 4 || h.CooldownUntil.IsZero() {
		t.Fatalf("cooling health=%+v", h)
	}
	if !h.CooldownUntil.Equal(now.Add(4 * time.Second).Add(15 * time.Minute)) {
		t.Fatalf("cooldown until=%v", h.CooldownUntil)
	}
	if st.Records[0].Status != contract.RecordCooling {
		t.Fatalf("record not cooling: %s", st.Records[0].Status)
	}

	// A local outage is not attributable.
	st2 := &contract.State{Records: []contract.Record{record("x-b", 0, contract.RecordUnknown)}}
	ApplyProbe(st2, contract.ProbeResult{Exit: "x-b", Outcome: contract.OutcomeTimeout, Failure: contract.Timeout, Attribution: contract.ProbeAttributionLocalOutage}, now)
	if len(st2.Health) != 0 {
		t.Fatalf("local outage cooled the exit: %+v", st2.Health)
	}

	// A successful transport probe clears cooling but does not claim healthy.
	ApplyProbe(st, contract.ProbeResult{Exit: "x-a", Kind: contract.ProbeTransport, Outcome: contract.OutcomeSuccess}, now.Add(20*time.Minute))
	if st.Health[0].Status != contract.HealthUnknown || st.Health[0].ConsecutiveFailures != 0 {
		t.Fatalf("transport success: %+v", st.Health[0])
	}
}

func selectorState(now time.Time) contract.State {
	return contract.State{
		Records: []contract.Record{
			record("x-a", 0, contract.RecordHealthy),
			record("x-b", 1, contract.RecordHealthy),
			record("x-c", 2, contract.RecordUnknown),
		},
		Health: []contract.ExitHealth{
			health("x-a", contract.HealthHealthy, 100, 5),
			health("x-b", contract.HealthHealthy, 110, 5),
			{Exit: "x-c", Status: contract.HealthUnknown},
		},
	}
}

func TestRankingDwellAndImprovement(t *testing.T) {
	now := t0()
	state := selectorState(now)
	s := NewSelector()
	first, fail := s.SelectFor(state, "", now)
	if fail != nil {
		t.Fatal(fail)
	}
	if first.ID != "x-a" {
		t.Fatalf("first=%s", first.ID)
	}
	// B is 5%% better: below the 20%% gate, no switch.
	state.Health[1].EWMALatencyMS = 95
	if rec, _ := s.SelectFor(state, "", now.Add(10*time.Minute)); rec.ID != "x-a" {
		t.Fatalf("flapped on 5%%: %s", rec.ID)
	}
	// B becomes 25%% better but dwell has not elapsed.
	state.Health[1].EWMALatencyMS = 75
	if rec, _ := s.SelectFor(state, "", now.Add(2*time.Minute)); rec.ID != "x-a" {
		t.Fatalf("switched before dwell: %s", rec.ID)
	}
	// At five minutes the switch is allowed.
	if rec, _ := s.SelectFor(state, "", now.Add(5*time.Minute)); rec.ID != "x-b" {
		t.Fatalf("expected switch after dwell, got %s", rec.ID)
	}
}

func TestRankingRequiresTwoSamples(t *testing.T) {
	now := t0()
	state := selectorState(now)
	s := NewSelector()
	if _, fail := s.SelectFor(state, "", now); fail != nil {
		t.Fatal(fail)
	}
	state.Health[1].EWMALatencyMS = 10
	state.Health[1].Samples = 1
	if rec, _ := s.SelectFor(state, "", now.Add(10*time.Minute)); rec.ID != "x-a" {
		t.Fatalf("one sample caused a switch: %s", rec.ID)
	}
}

func TestNoisyLatencyDoesNotFlap(t *testing.T) {
	now := t0()
	state := selectorState(now)
	s := NewSelector()
	if _, fail := s.SelectFor(state, "", now); fail != nil {
		t.Fatal(fail)
	}
	// B alternates between slightly better and worse; never a 20% sustained
	// improvement, and the current exit stays put.
	for i, ewma := range []int64{90, 110, 85, 120, 88, 130} {
		state.Health[1].EWMALatencyMS = ewma
		state.Health[1].Samples = 5
		rec, fail := s.SelectFor(state, "", now.Add(time.Duration(i+1)*time.Minute))
		if fail != nil {
			t.Fatal(fail)
		}
		if rec.ID != "x-a" {
			t.Fatalf("flapped at step %d to %s", i, rec.ID)
		}
	}
}

func TestHealthyBeatsUnknown(t *testing.T) {
	now := t0()
	state := contract.State{
		Records: []contract.Record{
			record("x-u", 0, contract.RecordUnknown),
			record("x-h", 1, contract.RecordHealthy),
		},
		Health: []contract.ExitHealth{
			{Exit: "x-u", Status: contract.HealthUnknown},
			health("x-h", contract.HealthHealthy, 500, 2),
		},
	}
	s := NewSelector()
	// First selection already prefers healthy over unknown regardless of order.
	rec, fail := s.SelectFor(state, "", now)
	if fail != nil || rec.ID != "x-h" {
		t.Fatalf("selected %v fail=%v", rec.ID, fail)
	}
	// A later cooling of H must switch back to the unknown exit.
	state.Health[1] = contract.ExitHealth{Exit: "x-h", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute)}
	rec, fail = s.SelectFor(state, "", now)
	if fail != nil || rec.ID != "x-u" {
		t.Fatalf("recovery selection %v fail=%v", rec.ID, fail)
	}
}

func TestAllCoolingIsPoolUnavailable(t *testing.T) {
	now := t0()
	state := contract.State{
		Records: []contract.Record{record("x-a", 0, contract.RecordCooling), record("x-b", 1, contract.RecordCooling)},
		Health: []contract.ExitHealth{
			{Exit: "x-a", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute)},
			{Exit: "x-b", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute)},
		},
	}
	rec, fail := NewSelector().SelectFor(state, "", now)
	if fail == nil || fail.Kind != contract.PoolUnavailable || rec.ID != "" {
		t.Fatalf("rec=%v fail=%+v", rec.ID, fail)
	}
}

func TestManualSelectionPinned(t *testing.T) {
	now := t0()
	state := selectorState(now)
	state.SelectedExit = "x-a"
	// Make A unusable; manual selection must still return it and never switch.
	state.Health[0] = contract.ExitHealth{Exit: "x-a", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute)}
	rec, fail := NewSelector().SelectFor(state, "", now)
	if fail != nil || rec.ID != "x-a" {
		t.Fatalf("manual rec=%v fail=%v", rec.ID, fail)
	}
	// Requesting an invalid exit is PoolUnavailable, not a silent substitution.
	state.Records[0].Status = contract.RecordUnsupported
	if _, fail := NewSelector().SelectFor(state, "x-a", now); fail == nil || fail.Kind != contract.PoolUnavailable {
		t.Fatalf("expected pool_unavailable, got %+v", fail)
	}
}

func TestRemoveSourceOwnership(t *testing.T) {
	now := t0()
	shared := record("x-shared", 0, contract.RecordHealthy)
	shared.SourceIDs = []contract.SourceID{"s-a", "s-b"}
	onlyA := record("x-a", 1, contract.RecordUnknown)
	onlyA.SourceIDs = []contract.SourceID{"s-a"}
	state := contract.State{
		Mode: contract.ModeTargeted, PolicyVersion: 3, SelectedExit: "x-a",
		Sources: []contract.Source{{ID: "s-a"}, {ID: "s-b"}},
		Records: []contract.Record{shared, onlyA},
		Health:  []contract.ExitHealth{{Exit: "x-a", Status: contract.HealthUnknown}},
	}
	removed, found := RemoveSource(&state, "s-a")
	if !found || len(removed) != 1 || removed[0].ID != "x-a" {
		t.Fatalf("removed=%v found=%v", removed, found)
	}
	if len(state.Records) != 1 || state.Records[0].ID != "x-shared" || len(state.Records[0].SourceIDs) != 1 || state.Records[0].SourceIDs[0] != "s-b" {
		t.Fatalf("records=%+v", state.Records)
	}
	if st, _ := Build(state, now).Record("x-shared"); len(st.SourceIDs) != 1 {
		t.Fatal("shared record lost its remaining source")
	}
	if state.SelectedExit != "" || state.PolicyVersion != 4 {
		t.Fatalf("selection/version=%q/%d", state.SelectedExit, state.PolicyVersion)
	}
	if len(state.Health) != 0 {
		t.Fatal("health for removed exit survived")
	}
}

func TestSetSelectedBumpsVersion(t *testing.T) {
	state := contract.State{PolicyVersion: 7, Records: []contract.Record{record("x-a", 0, contract.RecordUnknown)}}
	if err := SetSelected(&state, "x-a"); err != nil {
		t.Fatal(err)
	}
	if state.SelectedExit != "x-a" || state.PolicyVersion != 8 {
		t.Fatalf("selected=%q version=%d", state.SelectedExit, state.PolicyVersion)
	}
	err := SetSelected(&state, "x-missing")
	if !errors.Is(err, ErrExitNotFound) {
		t.Fatalf("missing: %v", err)
	}
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.NotFound {
		t.Fatalf("missing kind = %v, want not_found", err)
	}
	ClearSelection(&state)
	if state.SelectedExit != "" || state.PolicyVersion != 9 {
		t.Fatalf("clear selected=%q version=%d", state.SelectedExit, state.PolicyVersion)
	}
}

func TestSelectionDoesNotMutateState(t *testing.T) {
	now := t0()
	state := selectorState(now)
	before := len(state.Records) + len(state.Health) + int(state.PolicyVersion)
	s := NewSelector()
	for i := 0; i < 5; i++ {
		if _, fail := s.SelectFor(state, "", now.Add(time.Duration(i)*time.Minute)); fail != nil {
			t.Fatal(fail)
		}
	}
	after := len(state.Records) + len(state.Health) + int(state.PolicyVersion)
	if before != after {
		t.Fatal("selection mutated state")
	}
}

func TestDialerPoolUnavailableNeverDials(t *testing.T) {
	now := t0()
	state := contract.State{
		PolicyVersion: 4,
		Records:       []contract.Record{record("x-a", 0, contract.RecordCooling)},
		Health:        []contract.ExitHealth{{Exit: "x-a", Status: contract.HealthCooling, CooldownUntil: now.Add(time.Minute)}},
	}
	calls := 0
	d := NewDialer(func() (contract.Snapshot, bool) { return contract.Snapshot{State: state}, true },
		func(context.Context, contract.ExitID, contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			calls++
			return nil, "", "", errors.New("should not dial")
		}, func() time.Time { return now })
	_, err := d.DialPool(context.Background(), contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 4,
	})
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.PoolUnavailable || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDialerFenceProofAndErrorMapping(t *testing.T) {
	now := t0()
	state := contract.State{
		PolicyVersion: 4,
		Records:       []contract.Record{record("x-a", 0, contract.RecordUnknown)},
	}
	var exitSeen contract.ExitID
	d := NewDialer(func() (contract.Snapshot, bool) { return contract.Snapshot{State: state}, true },
		func(_ context.Context, exit contract.ExitID, _ contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			exitSeen = exit
			client, server := net.Pipe()
			server.Close()
			return client, contract.ProofAccepted, "tag-a", nil
		}, func() time.Time { return now })

	// Stale policy version is canceled before any dial.
	_, err := d.DialPool(context.Background(), contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 3,
	})
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.Canceled || exitSeen != "" {
		t.Fatalf("stale err=%v exit=%q", err, exitSeen)
	}

	res, err := d.DialPool(context.Background(), contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != "x-a" || res.Proof != contract.ProofAccepted || res.Tag != "tag-a" || exitSeen != "x-a" {
		t.Fatalf("result=%+v exitSeen=%q", res, exitSeen)
	}
	res.Conn.Close()

	// A nil proof must not be handed to the frontend.
	d2 := NewDialer(func() (contract.Snapshot, bool) { return contract.Snapshot{State: state}, true },
		func(context.Context, contract.ExitID, contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			client, server := net.Pipe()
			server.Close()
			return client, contract.ProofUnknown, "", nil
		}, func() time.Time { return now })
	if _, err := d2.DialPool(context.Background(), contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 4,
	}); !errors.As(err, &f) || f.Kind != contract.Internal {
		t.Fatalf("unknown proof err=%v", err)
	}

	// An engine failure is surfaced as a typed pool failure, never a direct dial.
	d3 := NewDialer(func() (contract.Snapshot, bool) { return contract.Snapshot{State: state}, true },
		func(context.Context, contract.ExitID, contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			return nil, contract.ProofAccepted, "", &contract.Failure{Kind: contract.ExitUnreachable, Message: "exit failed"}
		}, func() time.Time { return now })
	_, err = d3.DialPool(context.Background(), contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 4,
	})
	if !errors.As(err, &f) || f.Kind != contract.ExitUnreachable || f.Path != contract.PathPool || f.Exit != "x-a" {
		t.Fatalf("mapped failure=%+v", f)
	}
}

func TestSyncStatusesExpiresCooldown(t *testing.T) {
	now := t0()
	state := contract.State{
		Records: []contract.Record{record("x-a", 0, contract.RecordCooling)},
		Health:  []contract.ExitHealth{{Exit: "x-a", Status: contract.HealthCooling, CooldownUntil: now.Add(-time.Second)}},
	}
	SyncStatuses(&state, now)
	if state.Health[0].Status != contract.HealthUnknown || state.Records[0].Status != contract.RecordUnknown {
		t.Fatalf("state=%+v", state)
	}
}

func rerankDialer(state *contract.State) (*Dialer, *int) {
	calls := 0
	d := NewDialer(
		func() (contract.Snapshot, bool) { return contract.Snapshot{State: *state}, true },
		func(context.Context, contract.ExitID, contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			calls++
			return nil, contract.ProofAccepted, "", errors.New("rerank must not dial")
		},
		func() time.Time { return t0() },
	)
	return d, &calls
}

func hasSkip(skips []RerankSkip, exit contract.ExitID, reason RerankSkipReason) bool {
	for _, s := range skips {
		if s.Exit == exit && s.Reason == reason {
			return true
		}
	}
	return false
}

func TestRerankAllUnknownSelectsByOrderWithoutDialing(t *testing.T) {
	now := t0()
	state := contract.State{PolicyVersion: 3, Records: []contract.Record{
		record("x-a", 0, contract.RecordUnknown),
		record("x-b", 1, contract.RecordUnknown),
	}}
	d, calls := rerankDialer(&state)
	res, err := d.Rerank(now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Before != "" || res.After != "x-a" || res.Version != 3 {
		t.Fatalf("rerank=%+v", res)
	}
	if !hasSkip(res.Skipped, "x-b", RerankSkipUnknown) {
		t.Fatalf("missing unknown skip: %+v", res.Skipped)
	}
	if *calls != 0 {
		t.Fatalf("rerank dialed %d times", *calls)
	}
	// A second rerank keeps the established exit and reuses no dialer.
	res, _ = d.Rerank(now.Add(time.Minute))
	if res.Before != "x-a" || res.After != "x-a" || *calls != 0 {
		t.Fatalf("second rerank=%+v calls=%d", res, *calls)
	}
}

func TestRerankPromotesHealthyExit(t *testing.T) {
	now := t0()
	state := contract.State{PolicyVersion: 4, Records: []contract.Record{
		record("x-a", 0, contract.RecordUnknown),
		record("x-b", 1, contract.RecordUnknown),
	}}
	d, calls := rerankDialer(&state)
	if _, err := d.Rerank(now); err != nil {
		t.Fatal(err)
	}
	// x-b gains positive evidence; the healthy exit wins immediately over the
	// established unknown exit.
	state.Records[1].Status = contract.RecordHealthy
	state.Health = []contract.ExitHealth{health("x-b", contract.HealthHealthy, 40, 3)}
	res, err := d.Rerank(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.Before != "x-a" || res.After != "x-b" || res.Version != 4 {
		t.Fatalf("rerank=%+v", res)
	}
	if !hasSkip(res.Skipped, "x-a", RerankSkipUnknown) || *calls != 0 {
		t.Fatalf("skips=%+v calls=%d", res.Skipped, *calls)
	}
}

func TestRerankPinnedSelectionUntouched(t *testing.T) {
	state := contract.State{PolicyVersion: 5, SelectedExit: "x-b", Records: []contract.Record{
		record("x-a", 0, contract.RecordHealthy),
		record("x-b", 1, contract.RecordUnknown),
	}, Health: []contract.ExitHealth{health("x-a", contract.HealthHealthy, 10, 5)}}
	d, calls := rerankDialer(&state)
	res, err := d.Rerank(t0())
	if err != nil {
		t.Fatal(err)
	}
	if res.Before != "x-b" || res.After != "x-b" || res.Version != 5 {
		t.Fatalf("pinned rerank=%+v", res)
	}
	if !hasSkip(res.Skipped, "x-b", RerankSkipPinned) || *calls != 0 {
		t.Fatalf("skips=%+v calls=%d", res.Skipped, *calls)
	}
	if d.selector.Current() != "" {
		t.Fatal("pinned rerank must not replace the automatic current")
	}
	if state.PolicyVersion != 5 {
		t.Fatal("rerank advanced the policy version")
	}
}

func TestRerankRespectsDwellAndSamples(t *testing.T) {
	now := t0()
	state := contract.State{PolicyVersion: 6, Records: []contract.Record{
		record("x-a", 0, contract.RecordHealthy),
		record("x-b", 1, contract.RecordHealthy),
	}, Health: []contract.ExitHealth{
		health("x-a", contract.HealthHealthy, 100, 5),
		health("x-b", contract.HealthHealthy, 130, 5),
	}}
	d, _ := rerankDialer(&state)
	if res, _ := d.Rerank(now); res.After != "x-a" {
		t.Fatalf("first=%+v", res)
	}
	state.Health[1].EWMALatencyMS = 70
	res, _ := d.Rerank(now.Add(2 * time.Minute))
	if res.After != "x-a" || !hasSkip(res.Skipped, "x-b", RerankSkipDwell) {
		t.Fatalf("dwell rerank=%+v", res)
	}
	res, _ = d.Rerank(now.Add(contract.SwitchMinDwell))
	if res.Before != "x-a" || res.After != "x-b" {
		t.Fatalf("post-dwell rerank=%+v", res)
	}

	// A single successful sample cannot promote a candidate.
	state.Health[1] = health("x-b", contract.HealthHealthy, 130, 1)
	state.Health[0] = health("x-a", contract.HealthHealthy, 100, 5)
	d2, _ := rerankDialer(&state)
	if res, _ := d2.Rerank(now); res.After != "x-a" {
		t.Fatalf("initial=%+v", res)
	}
	state.Health[1].EWMALatencyMS = 5
	res, _ = d2.Rerank(now.Add(10 * time.Minute))
	if res.After != "x-a" || !hasSkip(res.Skipped, "x-b", RerankSkipSamples) {
		t.Fatalf("samples rerank=%+v", res)
	}
}

func TestRerankNoTargetSkips(t *testing.T) {
	state := contract.State{PolicyVersion: 7, Records: []contract.Record{
		record("x-cool", 0, contract.RecordCooling),
		record("x-unsup", 1, contract.RecordUnsupported),
		record("x-invalid", 2, contract.RecordInvalid),
	}, Health: []contract.ExitHealth{
		{Exit: "x-cool", Status: contract.HealthCooling, CooldownUntil: t0().Add(time.Minute)},
	}}
	d, calls := rerankDialer(&state)
	res, err := d.Rerank(t0())
	if err != nil {
		t.Fatal(err)
	}
	if res.After != "" || res.Version != 7 {
		t.Fatalf("no-target rerank=%+v", res)
	}
	for exit, reason := range map[contract.ExitID]RerankSkipReason{
		"x-cool": RerankSkipCooling, "x-unsup": RerankSkipUnsupported,
		"x-invalid": RerankSkipInvalid, "": RerankSkipNoTarget,
	} {
		if !hasSkip(res.Skipped, exit, reason) {
			t.Fatalf("missing %s/%s in %+v", exit, reason, res.Skipped)
		}
	}
	if *calls != 0 {
		t.Fatalf("no-target rerank dialed %d times", *calls)
	}

	empty := contract.State{PolicyVersion: 8}
	d2, _ := rerankDialer(&empty)
	res, _ = d2.Rerank(t0())
	if res.After != "" || !hasSkip(res.Skipped, "", RerankSkipNoTarget) {
		t.Fatalf("empty rerank=%+v", res)
	}
}
