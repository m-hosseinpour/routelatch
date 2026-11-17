package pool

import (
	"testing"

	"routelatch/internal/contract"
)

// TestEffectiveStatusDerivesLegacyUnsupported covers the view-only derivation
// for a state record that still says unknown about a protocol v1 can never dial
// (docs/contracts.md §5.3).
func TestEffectiveStatusDerivesLegacyUnsupported(t *testing.T) {
	now := t0()
	httpRec := record("x-http", 0, contract.RecordUnknown)
	httpRec.Protocol = contract.ProtocolHTTP
	httpRec.StatusReason = "engine_record_rejected" // stale reason must not win
	emptyStatusHTTP := record("x-http-empty", 1, "")
	emptyStatusHTTP.Protocol = contract.ProtocolHTTP
	vmessRec := record("x-vmess", 2, contract.RecordUnknown)
	vmessRec.Protocol = contract.Protocol("vmess")
	emptyStatusScheme := record("x-unknown-scheme", 3, "")
	emptyStatusScheme.Protocol = contract.Protocol("hysteria2")
	vlessRec := record("x-vless", 4, contract.RecordUnknown)
	emptyStatusVLESS := record("x-vless-empty", 5, "")
	emptyProtocol := record("x-no-proto", 6, contract.RecordUnknown)
	emptyProtocol.Protocol = ""
	unsupportedRec := record("x-unsupported", 7, contract.RecordUnsupported)
	unsupportedRec.StatusReason = contract.ReasonInsecureTLSRemoved
	invalidRec := record("x-invalid", 8, contract.RecordInvalid)
	healthyRec := record("x-healthy", 9, contract.RecordHealthy)
	coolingRec := record("x-cooling", 10, contract.RecordCooling)
	coolingRec.StatusReason = "cooling:timeout"

	cases := []struct {
		name   string
		rec    contract.Record
		status contract.RecordStatus
		reason string
	}{
		{"http unknown", httpRec, contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http empty status", emptyStatusHTTP, contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"unknown scheme unknown", vmessRec, contract.RecordUnsupported, contract.ReasonProtocolUnsupported},
		{"unknown scheme empty status", emptyStatusScheme, contract.RecordUnsupported, contract.ReasonProtocolUnsupported},
		{"vless unknown stays unknown", vlessRec, contract.RecordUnknown, ""},
		{"vless empty status stays unknown", emptyStatusVLESS, contract.RecordUnknown, ""},
		{"empty protocol stays as today", emptyProtocol, contract.RecordUnknown, ""},
		{"stored unsupported keeps its reason", unsupportedRec, contract.RecordUnsupported, contract.ReasonInsecureTLSRemoved},
		{"invalid keeps invalid", invalidRec, contract.RecordInvalid, "invalid"},
		{"healthy keeps healthy", healthyRec, contract.RecordHealthy, ""},
		{"cooling keeps cooling", coolingRec, contract.RecordCooling, "cooling:timeout"},
	}
	for _, tc := range cases {
		status, reason := EffectiveStatus(tc.rec, contract.ExitHealth{}, now)
		if status != tc.status || reason != tc.reason {
			t.Errorf("%s: got %s/%q, want %s/%q", tc.name, status, reason, tc.status, tc.reason)
		}
	}
}

// TestDerivedUnsupportedWinsOverCachedHealth proves a stale health entry cannot
// report a never-dialable protocol as healthy.
func TestDerivedUnsupportedWinsOverCachedHealth(t *testing.T) {
	now := t0()
	rec := record("x-http", 0, contract.RecordUnknown)
	rec.Protocol = contract.ProtocolHTTP
	status, reason := EffectiveStatus(rec, health("x-http", contract.HealthHealthy, 10, 3), now)
	if status != contract.RecordUnsupported || reason != contract.ReasonHTTPExitNotVerified {
		t.Fatalf("got %s/%q, want unsupported/%q", status, reason, contract.ReasonHTTPExitNotVerified)
	}
}

// TestBuildCountsDerivedUnsupported is the agreement point for pool list,
// status warnings and doctor: the record leaves unknown and joins unsupported.
func TestBuildCountsDerivedUnsupported(t *testing.T) {
	now := t0()
	httpRec := record("x-http", 0, contract.RecordUnknown)
	httpRec.Protocol = contract.ProtocolHTTP
	vmessRec := record("x-vmess", 1, contract.RecordUnknown)
	vmessRec.Protocol = contract.Protocol("vmess")
	good := record("x-good", 2, contract.RecordUnknown)
	emptyStatus := record("x-empty", 3, "")
	emptyStatus.Protocol = contract.Protocol("ftp")
	state := contract.State{Records: []contract.Record{httpRec, vmessRec, good, emptyStatus}}

	p := Build(state, now)
	counts := p.Counts()
	if counts.Total != 4 || counts.Unsupported != 3 || counts.Unknown != 1 || counts.Usable != 1 {
		t.Fatalf("counts=%+v", counts)
	}
	for id, want := range map[contract.RecordID]string{
		"x-http":  contract.ReasonHTTPExitNotVerified,
		"x-vmess": contract.ReasonProtocolUnsupported,
		"x-empty": contract.ReasonProtocolUnsupported,
		"x-good":  "",
	} {
		rec, ok := p.Record(contract.ExitID(id))
		if !ok {
			t.Fatalf("record %s missing", id)
		}
		if rec.StatusReason != want {
			t.Errorf("%s reason=%q, want %q", id, rec.StatusReason, want)
		}
		if id != "x-good" && rec.Status != contract.RecordUnsupported {
			t.Errorf("%s status=%s, want unsupported", id, rec.Status)
		}
	}
	if p.Usable("x-http") || p.Usable("x-vmess") || p.Usable("x-empty") {
		t.Fatal("a derived-unsupported record must not be usable")
	}
	if !Dialable(good) {
		t.Fatal("a legacy unknown vless record must stay dialable")
	}
	if Dialable(httpRec) || Dialable(vmessRec) || Dialable(emptyStatus) {
		t.Fatal("a protocol v1 cannot dial must not be dialable")
	}
	if !Dialable(contract.Record{Protocol: "", Server: "example.com", Port: 443, Status: contract.RecordUnknown}) {
		t.Fatal("an empty protocol keeps its previous handling (out of scope)")
	}
}
