package control

import (
	"context"
	"net"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/probes"
	"routelatch/internal/store"
)

func scanRecord(id string, order int) contract.Record {
	return contract.Record{
		ID: contract.RecordID(id), DedupeKey: "sha256:" + id, Status: contract.RecordUnknown,
		Order: order, Protocol: contract.ProtocolSOCKS5, Server: "127.0.0.1", Port: 1080,
		Transport: contract.TransportRaw, Security: contract.SecurityNone,
	}
}

func newScanService(t *testing.T, records []contract.Record) (*Service, *store.Store, store.Paths) {
	t.Helper()
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = records
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.Probes.Targets = []string{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	byID := make(map[contract.ExitID]contract.Record, len(records))
	for _, rec := range records {
		byID[contract.ExitID(rec.ID)] = rec
	}
	engine := probes.NewEngine(probes.Options{
		Record:     func(id contract.ExitID) (contract.Record, bool) { rec, ok := byID[id]; return rec, ok },
		AllRecords: func() []contract.Record { return records },
		Mode:       func() contract.Mode { return contract.ModeTargeted },
		Clock:      time.Now,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	})
	svc.prober = engine
	t.Cleanup(engine.Close)
	return svc, st, paths
}

func TestPoolScanCursorAndLimitPaging(t *testing.T) {
	records := []contract.Record{scanRecord("x-a", 0), scanRecord("x-b", 1), scanRecord("x-c", 2)}
	svc, _, _ := newScanService(t, records)
	ctx := context.Background()

	first, err := svc.Handle(ctx, Request{Op: OpPoolScan, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := first.(PoolScanData)
	if page.Tested != 2 || page.Total != 3 || page.Next != 2 || !page.Truncated || len(page.Results) != 2 {
		t.Fatalf("first page = %+v, want tested 2 total 3 next 2 truncated", page)
	}
	if page.Results[0].Exit != "x-a" || page.Results[1].Exit != "x-b" {
		t.Fatalf("first page order = %+v", page.Results)
	}

	second, err := svc.Handle(ctx, Request{Op: OpPoolScan, Offset: page.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	last := second.(PoolScanData)
	if last.Tested != 1 || last.Total != 3 || last.Next != -1 || last.Truncated || len(last.Results) != 1 {
		t.Fatalf("second page = %+v, want tested 1 next -1 not truncated", last)
	}
	if last.Results[0].Exit != "x-c" {
		t.Fatalf("second page exit = %s, want x-c", last.Results[0].Exit)
	}

	// A cursor at or past the end is a completed scan, not an error.
	after, err := svc.Handle(ctx, Request{Op: OpPoolScan, Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if done := after.(PoolScanData); done.Tested != 0 || done.Next != -1 || done.Truncated {
		t.Fatalf("past-end scan = %+v, want a completed empty page", done)
	}
}

func TestPoolScanAppliesHealthInOneMutation(t *testing.T) {
	now := time.Now().UTC()
	records := []contract.Record{scanRecord("x-a", 0), scanRecord("x-b", 1), scanRecord("x-c", 2)}
	svc, st, _ := newScanService(t, records)
	// Seed a live cooldown that a successful endpoint probe must clear.
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Health = []contract.ExitHealth{{
			Exit: "x-a", Status: contract.HealthCooling,
			CooldownUntil: now.Add(time.Hour), LastFailureKind: contract.Timeout,
			ConsecutiveFailures: 2,
		}}
		state.Records[0].Status = contract.RecordCooling
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Refresh the snapshot after the seed so poolScan builds from it.
	seeded, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc.snap.Store(&seeded)

	data, err := svc.poolScan(context.Background(), Request{Op: OpPoolScan, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if data.Tested != 2 || data.Next != 2 || !data.Truncated {
		t.Fatalf("scan = %+v, want two exits and a cursor", data)
	}

	after, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byExit := map[contract.ExitID]contract.ExitHealth{}
	for _, h := range after.State.Health {
		byExit[h.Exit] = h
	}
	if len(after.State.Health) != 2 {
		t.Fatalf("health rows = %d, want one per scanned exit (single mutation)", len(after.State.Health))
	}
	for _, exit := range []contract.ExitID{"x-a", "x-b"} {
		h, ok := byExit[exit]
		if !ok || h.TransportSuccessAt.IsZero() {
			t.Fatalf("health for %s = %+v, want endpoint evidence after the scan", exit, h)
		}
		if h.Status != contract.HealthUnknown || !h.CooldownUntil.IsZero() || h.ConsecutiveFailures != 0 {
			t.Fatalf("health for %s = %+v, want unknown with the cooldown cleared", exit, h)
		}
	}
	if _, ok := byExit["x-c"]; ok {
		t.Fatal("an unscanned exit gained health evidence")
	}
	for _, rec := range after.State.Records {
		if rec.Status != contract.RecordUnknown {
			t.Fatalf("record %s status = %s, want unknown (endpoint is not egress proof)", rec.ID, rec.Status)
		}
	}
}

func TestPoolScanBudgetCutoffReportsTruncationAndCursor(t *testing.T) {
	records := []contract.Record{scanRecord("x-a", 0), scanRecord("x-b", 1), scanRecord("x-c", 2)}
	svc, _, _ := newScanService(t, records)

	// A zero budget expires before the first probe, exactly like the real
	// budget expiring mid-scan: the caller gets a cursor instead of a hang.
	page, err := svc.poolScanWithBudget(context.Background(), Request{Op: OpPoolScan}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Tested != 0 || page.Total != 3 || page.Next != 0 || !page.Truncated {
		t.Fatalf("budget cutoff = %+v, want tested 0 total 3 next 0 truncated", page)
	}

	// The cursor resumes the scan from the same record.
	resumed, err := svc.poolScanWithBudget(context.Background(), Request{Op: OpPoolScan, Offset: page.Next}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Tested != 3 || resumed.Next != -1 || resumed.Truncated {
		t.Fatalf("resumed scan = %+v, want every exit and completion", resumed)
	}
}

func TestPoolScanAndDesktopClearAreRegisteredOps(t *testing.T) {
	cases := []struct {
		op      string
		command string
		timeout time.Duration
	}{
		{OpPoolScan, "pool scan", 10 * time.Minute},
		{OpDesktopClear, "desktop proxy clear", 30 * time.Second},
	}
	for _, tc := range cases {
		if got := CommandName(tc.op); got != tc.command {
			t.Errorf("CommandName(%s) = %q, want %q", tc.op, got, tc.command)
		}
		if got := requestTimeout(tc.op); got != tc.timeout {
			t.Errorf("requestTimeout(%s) = %v, want %v", tc.op, got, tc.timeout)
		}
	}

	svc, _, _ := newScanService(t, []contract.Record{scanRecord("x-a", 0)})
	raw, err := svc.Handle(context.Background(), Request{Op: OpPoolScan})
	if err != nil {
		t.Fatalf("Handle(%s): %v", OpPoolScan, err)
	}
	if data, ok := raw.(PoolScanData); !ok || data.Total != 1 || data.Tested != 1 {
		t.Fatalf("Handle(%s) = %#v, want a completed one-exit scan", OpPoolScan, raw)
	}
}
