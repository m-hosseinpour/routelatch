package pool

import (
	"testing"
	"time"

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
	// Must be a scheme v1 can never dial: hysteria2 is now supported and would
	// keep its stored status rather than derive protocol_unsupported.
	emptyStatusScheme.Protocol = contract.Protocol("ftp")
	hysteria2Rec := record("x-hysteria2", 11, contract.RecordUnknown)
	hysteria2Rec.Protocol = contract.ProtocolHysteria2
	emptyStatusHysteria2 := record("x-hysteria2-empty", 12, "")
	emptyStatusHysteria2.Protocol = contract.ProtocolHysteria2
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
		{"hysteria2 unknown stays unknown", hysteria2Rec, contract.RecordUnknown, ""},
		{"hysteria2 empty status stays unknown", emptyStatusHysteria2, contract.RecordUnknown, ""},
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

// TestUndialableDerivationWinsOverStoredStatus locks the rule that a protocol v1
// can never dial reports the importer's verdict whatever the stored status is
// (legacy cooling, healthy, unsupported with a pre-R5 engine reason), while a
// supported protocol keeps every stored status and stored invalid stays invalid.
func TestUndialableDerivationWinsOverStoredStatus(t *testing.T) {
	now := t0()
	withProtocol := func(id string, order int, protocol contract.Protocol, status contract.RecordStatus, reason string) contract.Record {
		rec := record(id, order, status)
		rec.Protocol = protocol
		rec.StatusReason = reason
		return rec
	}
	coolingHealth := func(exit contract.ExitID, until time.Time) contract.ExitHealth {
		h := health(exit, contract.HealthCooling, 0, 0)
		h.CooldownUntil = until
		h.LastFailureKind = contract.ExitUnreachable
		return h
	}

	cases := []struct {
		name   string
		rec    contract.Record
		h      contract.ExitHealth
		status contract.RecordStatus
		reason string
	}{
		{"http stored cooling", withProtocol("x-http-cooling", 0, contract.ProtocolHTTP, contract.RecordCooling, "cooling:timeout"), contract.ExitHealth{}, contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http stored healthy", withProtocol("x-http-healthy", 1, contract.ProtocolHTTP, contract.RecordHealthy, ""), contract.ExitHealth{}, contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http stored unsupported keeps no engine reason", withProtocol("x-http-unsupported", 2, contract.ProtocolHTTP, contract.RecordUnsupported, "engine_record_rejected"), contract.ExitHealth{}, contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http stored cooling with live health", withProtocol("x-http-live", 3, contract.ProtocolHTTP, contract.RecordCooling, "cooling:timeout"), coolingHealth("x-http-live", now.Add(time.Minute)), contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http stored cooling with expired health", withProtocol("x-http-expired", 4, contract.ProtocolHTTP, contract.RecordCooling, "cooling:timeout"), coolingHealth("x-http-expired", now.Add(-time.Minute)), contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"http stored cooling with healthy cache", withProtocol("x-http-cache", 5, contract.ProtocolHTTP, contract.RecordCooling, "cooling:timeout"), health("x-http-cache", contract.HealthHealthy, 10, 3), contract.RecordUnsupported, contract.ReasonHTTPExitNotVerified},
		{"vmess stored cooling", withProtocol("x-vmess-cooling", 6, contract.Protocol("vmess"), contract.RecordCooling, "cooling:timeout"), contract.ExitHealth{}, contract.RecordUnsupported, contract.ReasonProtocolUnsupported},
		{"vmess stored unsupported keeps no engine reason", withProtocol("x-vmess-unsupported", 7, contract.Protocol("vmess"), contract.RecordUnsupported, "engine_record_rejected"), contract.ExitHealth{}, contract.RecordUnsupported, contract.ReasonProtocolUnsupported},
		{"http stored invalid stays invalid", withProtocol("x-http-invalid", 8, contract.ProtocolHTTP, contract.RecordInvalid, ""), contract.ExitHealth{}, contract.RecordInvalid, "invalid"},
		{"vmess stored invalid stays invalid", withProtocol("x-vmess-invalid", 9, contract.Protocol("vmess"), contract.RecordInvalid, "tls_verification_disabled"), contract.ExitHealth{}, contract.RecordInvalid, "tls_verification_disabled"},
		{"vless stored cooling stays cooling", withProtocol("x-vless-cooling", 10, contract.ProtocolVLESS, contract.RecordCooling, "cooling:timeout"), contract.ExitHealth{}, contract.RecordCooling, "cooling:timeout"},
		{"vless stored healthy stays healthy", withProtocol("x-vless-healthy", 11, contract.ProtocolVLESS, contract.RecordHealthy, ""), contract.ExitHealth{}, contract.RecordHealthy, ""},
		{"vless stored unsupported keeps the engine reason", withProtocol("x-vless-unsupported", 12, contract.ProtocolVLESS, contract.RecordUnsupported, "engine_record_rejected"), contract.ExitHealth{}, contract.RecordUnsupported, "engine_record_rejected"},
		{"vless unknown with live health cools", withProtocol("x-vless-live", 13, contract.ProtocolVLESS, contract.RecordUnknown, ""), coolingHealth("x-vless-live", now.Add(time.Minute)), contract.RecordCooling, "cooling:exit_unreachable"},
		{"vless unknown with expired health is unknown", withProtocol("x-vless-expired", 14, contract.ProtocolVLESS, contract.RecordUnknown, ""), coolingHealth("x-vless-expired", now.Add(-time.Minute)), contract.RecordUnknown, ""},
	}
	for _, tc := range cases {
		status, reason := EffectiveStatus(tc.rec, tc.h, now)
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
