package contract_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// TestTunConfigAdditiveJSON pins §10.2: tun_enabled is additive in schema
// version 1, false when an older file omits it, and always emitted (the config
// file lists scalar preferences explicitly; omitempty would hide false).
func TestTunConfigAdditiveJSON(t *testing.T) {
	if contract.SchemaVersionCurrent != 2 {
		t.Fatalf("schema version = %d; version 2 adds the newest-first rule order", contract.SchemaVersionCurrent)
	}
	var old contract.Config
	oldJSON := []byte(`{"schema_version":1,"http_listen":"127.0.0.1:8317","socks_listen":"127.0.0.1:8318","log_level":"info"}`)
	if err := json.Unmarshal(oldJSON, &old); err != nil {
		t.Fatalf("old config without tun_enabled: %v", err)
	}
	if old.TunEnabled {
		t.Fatal("missing tun_enabled decoded true, want false")
	}
	if old.SchemaVersion != 1 {
		t.Fatalf("schema_version=%d, want the file's version 1", old.SchemaVersion)
	}

	for _, want := range []bool{false, true} {
		raw, err := json.Marshal(contract.Config{SchemaVersion: contract.SchemaVersionCurrent, TunEnabled: want})
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		value, present := decoded["tun_enabled"]
		if !present || value != want {
			t.Fatalf("config JSON = %s, want explicit tun_enabled=%v", raw, want)
		}
		if decoded["schema_version"] != float64(contract.SchemaVersionCurrent) {
			t.Fatalf("config JSON = %s, want schema_version %d", raw, contract.SchemaVersionCurrent)
		}
	}
}

// TestTunStatusJSONShape pins the documented names and proves the payload has
// no destination-bearing field. Detail is fixed text, never a destination.
func TestTunStatusJSONShape(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	st := contract.TunStatus{
		Desired: true, State: contract.TunOn, Interface: "rl-tun0", Since: now,
		DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked, IPv6: true,
		Helper: contract.TunHelperInstalled, LastError: contract.TunReasonConflict,
		Detail: "fixed text",
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]bool{
		"desired": true, "state": true, "interface": true, "since": true,
		"dns": true, "udp": true, "ipv6": true, "helper": true,
		"last_error": true, "detail": true,
	}
	for key := range decoded {
		if !wantKeys[key] {
			t.Fatalf("unexpected tun field %q in %s", key, raw)
		}
	}
	for key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing tun field %q in %s", key, raw)
		}
	}
	for _, banned := range []string{"destination", "domain", "host", "url", "address", "flow"} {
		if strings.Contains(string(raw), `"`+banned+`"`) {
			t.Fatalf("tun payload contains destination-bearing key %q: %s", banned, raw)
		}
	}
	if decoded["state"] != "on" || decoded["dns"] != "direct" || decoded["udp"] != "blocked" {
		t.Fatalf("tun JSON = %s", raw)
	}
}

// TestTunStatusSinceOmittedWhenUnknown pins §10.2: `since` is a runtime start
// time. When no start time is known the key is omitted, not serialized as a
// fabricated zero timestamp.
func TestTunStatusSinceOmittedWhenUnknown(t *testing.T) {
	raw, err := json.Marshal(contract.TunStatus{
		State: contract.TunOff, DNS: contract.TunDNSDirect,
		UDP: contract.TunUDPBlocked, Helper: contract.TunHelperMissing,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["since"]; ok {
		t.Fatalf("tun JSON = %s, want `since` omitted when no start time is known", raw)
	}
	if decoded["state"] != "off" {
		t.Fatalf("tun JSON = %s", raw)
	}
}

func TestTunStableValues(t *testing.T) {
	if contract.TunOff != "off" || contract.TunStarting != "starting" ||
		contract.TunOn != "on" || contract.TunStopping != "stopping" ||
		contract.TunStateError != "error" || contract.TunUnavailable != "unavailable" {
		t.Fatal("TunState values drifted from §10.2")
	}
	if contract.TunHelperInstalled != "installed" || contract.TunHelperMissing != "missing" ||
		contract.TunHelperIncompatible != "incompatible" {
		t.Fatal("TunHelper values drifted from §10.2")
	}
	reasons := map[contract.TunReason]string{
		contract.TunReasonHelperUnavailable:   "tun_helper_unavailable",
		contract.TunReasonPlatformUnsupported: "tun_platform_unsupported",
		contract.TunReasonConflict:            "tun_conflict",
		contract.TunReasonServiceUnavailable:  "tun_service_unavailable",
		contract.TunReasonSidecarLoopRisk:     "sidecar_loop_risk",
	}
	for got, want := range reasons {
		if string(got) != want {
			t.Fatalf("reason %q, want %q", got, want)
		}
	}
	warnings := map[contract.TunWarning]string{
		contract.TunWarningDNSDirect:           "tun_dns_direct",
		contract.TunWarningUDPBlocked:          "tun_udp_blocked",
		contract.TunWarningHelperMissing:       "tun_helper_missing",
		contract.TunWarningServiceUnavailable:  "tun_service_unavailable",
		contract.TunWarningDomainUnavailable:   "tun_domain_unavailable",
		contract.TunWarningDesktopProxyOverlap: "tun_desktop_proxy_overlap",
		contract.TunWarningAllDirect:           "tun_all_direct",
		contract.TunWarningConflict:            "tun_conflict",
	}
	for got, want := range warnings {
		if string(got) != want {
			t.Fatalf("warning %q, want %q", got, want)
		}
	}
}
