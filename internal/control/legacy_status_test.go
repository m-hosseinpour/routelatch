package control

import (
	"context"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/pool"
)

func legacyViewRecord(id string, protocol contract.Protocol, status contract.RecordStatus, reason string) contract.Record {
	return contract.Record{
		ID: contract.RecordID(id), DedupeKey: "sha256:" + id,
		Protocol: protocol, Server: "127.0.0.1", Port: 443,
		Transport: contract.TransportRaw, Security: contract.SecurityNone,
		Status: status, StatusReason: reason,
	}
}

func viewState() contract.State {
	return contract.State{
		Mode: contract.ModeTargeted,
		Records: []contract.Record{
			legacyViewRecord("x-http", contract.ProtocolHTTP, contract.RecordUnknown, "engine_record_rejected"),
			legacyViewRecord("x-vmess", contract.Protocol("vmess"), contract.RecordUnknown, ""),
			legacyViewRecord("x-good", contract.ProtocolVLESS, contract.RecordUnknown, ""),
		},
	}
}

// TestStatusWarningsUseDerivedView pins the warning counter to pool.Build, so
// `status` and `pool list` cannot disagree about a derived unsupported record.
func TestStatusWarningsUseDerivedView(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	state := viewState()
	p := pool.Build(state, now)
	if counts := p.Counts(); counts.Unsupported != 2 || counts.Unknown != 1 {
		t.Fatalf("counts=%+v", counts)
	}
	warnings := statusWarnings(contract.Snapshot{State: state}, now, true, false, false, 0)
	want := "2 record(s) are unsupported or quarantined"
	found := false
	for _, w := range warnings {
		if w == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings=%v, want %q", warnings, want)
	}
}

// TestDoctorReportsDerivedUnsupported proves doctor's quarantined count uses the
// same derived status the pool view reports.
func TestDoctorReportsDerivedUnsupported(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	state := viewState()
	if _, err := st.Mutate(ctx, func(s *contract.State) error {
		s.Mode = state.Mode
		s.Records = state.Records
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	svc := newTestService(t, st, paths)
	doctor, err := svc.doctor(ctx)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var quarantined *Check
	for i := range doctor.Checks {
		if doctor.Checks[i].Name == "quarantined_records" {
			quarantined = &doctor.Checks[i]
		}
	}
	if quarantined == nil {
		t.Fatalf("doctor has no quarantined_records check: %+v", doctor.Checks)
	}
	want := "2 unsupported and 0 invalid records are kept for reporting and never dialed"
	if quarantined.Status != "warn" || quarantined.Detail != want {
		t.Fatalf("quarantined check=%+v, want warn/%q", quarantined, want)
	}
}
