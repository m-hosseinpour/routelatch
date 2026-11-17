package acceptance

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// legacyDialCounter is a loopback TCP endpoint that records every accepted
// connection. It stands in for a legacy record's server so a dial would be
// visible even when it produces no product-level failure.
type legacyDialCounter struct {
	ln   net.Listener
	hits atomic.Int64
}

func newLegacyDialCounter(t *testing.T) *legacyDialCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("counting listener: %v", err)
	}
	c := &legacyDialCounter{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c.hits.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return c
}

func (c *legacyDialCounter) port() uint16 { return uint16(c.ln.Addr().(*net.TCPAddr).Port) }

func legacyRecordDoc(id, protocol, reason, srcID string, port uint16) map[string]any {
	return map[string]any{
		"id":            id,
		"dedupe_key":    "sha256:" + strings.Repeat("0", 32) + id,
		"source_ids":    []any{srcID},
		"display_name":  id,
		"protocol":      protocol,
		"server":        "127.0.0.1",
		"port":          port,
		"credentials":   map[string]any{},
		"transport":     "raw",
		"security":      "none",
		"status":        "unknown",
		"status_reason": reason,
		"order":         0,
		"added_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func writeLegacyState(t *testing.T, dir string, port uint16) {
	t.Helper()
	const srcID = "s-0123456789ab"
	doc := map[string]any{
		"schema_version": 1,
		"policy_version": 1,
		"mode":           "targeted",
		"rules":          []any{},
		"sources": []any{
			map[string]any{
				"id": srcID, "kind": "manual", "label": "legacy", "order": 0,
				"added_at": time.Now().UTC().Format(time.RFC3339Nano), "record_count": 2,
			},
		},
		"records": []any{
			// A stale hand-edited reason must not win over the derived one.
			legacyRecordDoc("x-legacy-http", "http", "engine_record_rejected", srcID, port),
			legacyRecordDoc("x-legacy-vmess", "vmess", "", srcID, port),
		},
		"health":     []any{},
		"desktop":    map[string]any{},
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
}

// writeLegacyStoredStatusState builds a hand-edited pre-R5 state whose
// undialable records keep a stored status other than unknown: cooling with an
// expired cooldown, cooling inside its cooldown, healthy, invalid, and the
// unsupported/engine_record_rejected a pre-R5 start could persist. A supported
// vless record keeps its engine reason so the derivation stays scoped.
func writeLegacyStoredStatusState(t *testing.T, dir string, port uint16) {
	t.Helper()
	const srcID = "s-0123456789ab"
	now := time.Now().UTC()
	httpWith := func(id string, order int, status, reason string) map[string]any {
		doc := legacyRecordDoc(id, "http", reason, srcID, port)
		doc["status"] = status
		doc["order"] = order
		return doc
	}
	cooling := func(id string, order int) map[string]any {
		return httpWith(id, order, "cooling", "cooling:exit_unreachable")
	}
	rejected := func(id, protocol string, order int) map[string]any {
		doc := legacyRecordDoc(id, protocol, "engine_record_rejected", srcID, port)
		doc["status"] = "unsupported"
		doc["order"] = order
		return doc
	}
	health := func(exit string, until time.Time) map[string]any {
		return map[string]any{
			"exit": exit, "status": "cooling", "consecutive_failures": 1,
			"ewma_latency_ms": 0, "samples": 0,
			"last_failure_kind": "exit_unreachable",
			"cooldown_until":    until.Format(time.RFC3339Nano),
		}
	}
	doc := map[string]any{
		"schema_version": 1,
		"policy_version": 1,
		"mode":           "targeted",
		"rules":          []any{},
		"sources": []any{
			map[string]any{
				"id": srcID, "kind": "manual", "label": "legacy", "order": 0,
				"added_at": now.Format(time.RFC3339Nano), "record_count": 6,
			},
		},
		"records": []any{
			cooling("x-http-cooling-expired", 0),
			cooling("x-http-cooling-live", 1),
			httpWith("x-http-healthy", 2, "healthy", ""),
			httpWith("x-http-invalid", 3, "invalid", ""),
			rejected("x-http-engine-rejected", "http", 4),
			rejected("x-vless-engine-rejected", "vless", 5),
		},
		"health": []any{
			health("x-http-cooling-expired", now.Add(-time.Minute)),
			health("x-http-cooling-live", now.Add(time.Hour)),
		},
		"desktop":    map[string]any{},
		"updated_at": now.Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
}

func recordStatuses(t *testing.T, dir string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var doc struct {
		Records []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	out := make(map[string]string, len(doc.Records))
	for _, rec := range doc.Records {
		out[rec.ID] = rec.Status
	}
	return out
}

func findRow(rows []map[string]any, key, value string) map[string]any {
	for _, row := range rows {
		if str(row[key]) == value {
			return row
		}
	}
	return nil
}

func findCheck(checks []any, name string) map[string]any {
	for _, item := range checks {
		if ch, ok := item.(map[string]any); ok && str(ch["name"]) == name {
			return ch
		}
	}
	return nil
}

// TestLegacyUnknownRecordsDeriveUnsupported drives the real product over a
// hand-edited state: a legacy unknown http record and a legacy unknown
// vmess record must report as unsupported with the importer's reasons, without
// rewriting state, and status/pool list/doctor must count the same numbers.
// Importing the same links must land the same status/reasons.
func TestLegacyUnknownRecordsDeriveUnsupported(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "legacy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	legacyPort := newLegacyDialCounter(t)
	writeLegacyState(t, dir, legacyPort.port())

	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000},
	})
	t.Cleanup(func() { _ = svc.stop() })

	// pool list: both records moved out of unknown into unsupported.
	list := svc.mustOK("pool", "list").mapData(t)
	counts, _ := list["counts"].(map[string]any)
	rows := svc.mustOK("pool", "list").list(t, "records")
	httpRow := findRow(rows, "id", "x-legacy-http")
	vmessRow := findRow(rows, "id", "x-legacy-vmess")
	if num(counts["total"]) == 2 && num(counts["unknown"]) == 0 && num(counts["unsupported"]) == 2 {
		r.pass("R5", "pool list counts the two legacy unknown records as unsupported (total 2, unknown 0, unsupported 2)")
	} else {
		r.fail("R5", "pool list counts=%+v, want total 2, unknown 0, unsupported 2", counts)
	}
	if httpRow != nil && str(httpRow["status"]) == "unsupported" && str(httpRow["status_reason"]) == "http_exit_not_verified" {
		r.pass("R5", "the legacy http record reports unsupported with the importer reason http_exit_not_verified (stale reason ignored)")
	} else {
		r.fail("R5", "legacy http row=%+v", httpRow)
	}
	if vmessRow != nil && str(vmessRow["status"]) == "unsupported" && str(vmessRow["status_reason"]) == "protocol_unsupported" {
		r.pass("R5", "the legacy vmess record reports unsupported with the importer reason protocol_unsupported")
	} else {
		r.fail("R5", "legacy vmess row=%+v", vmessRow)
	}

	// status and doctor agree with those counts.
	status, _ := svc.statusNow()
	wantWarning := fmt.Sprintf("%d record(s) are unsupported or quarantined", 2)
	if status.poolTotal == 2 && status.poolUsable == 0 && containsString(status.warnings, wantWarning) {
		r.pass("R5", "status reports total 2, usable 0 and the unsupported warning for the same 2 records")
	} else {
		r.fail("R5", "status total=%d usable=%d warnings=%v, want the 2-record unsupported warning", status.poolTotal, status.poolUsable, status.warnings)
	}
	doctor := svc.mustOK("doctor").mapData(t)
	checks, _ := doctor["checks"].([]any)
	quarantined := findCheck(checks, "quarantined_records")
	poolCheck := findCheck(checks, "pool")
	wantQuarantined := "2 unsupported and 0 invalid records are kept for reporting and never dialed"
	if quarantined != nil && str(quarantined["detail"]) == wantQuarantined &&
		poolCheck != nil && str(poolCheck["status"]) == "warn" && strings.Contains(str(poolCheck["detail"]), "unsupported 2") {
		r.pass("R5", "doctor counts the same 2 unsupported records and repeats the never-dialed wording")
	} else {
		r.fail("R5", "doctor quarantined=%+v pool=%+v", quarantined, poolCheck)
	}

	// Neither record is dialable: an explicit probe is rejected before any TCP
	// connection, and the legacy state is never rewritten by the view.
	probeHTTP := svc.mustOK("pool", "test", "--exit", "x-legacy-http").list(t, "results")
	probeVMess := svc.mustOK("pool", "test", "--exit", "x-legacy-vmess").list(t, "results")
	rejected := func(rows []map[string]any) bool {
		return len(rows) == 1 && str(rows[0]["outcome"]) == "rejected" && str(rows[0]["failure"]) == "unsupported"
	}
	if rejected(probeHTTP) && rejected(probeVMess) && legacyPort.hits.Load() == 0 {
		r.pass("R5", "pool test rejects both legacy records as unsupported and neither endpoint was dialed (%d connections)", legacyPort.hits.Load())
	} else {
		r.fail("R5", "probes http=%+v vmess=%+v dials=%d", probeHTTP, probeVMess, legacyPort.hits.Load())
	}
	statuses := recordStatuses(t, dir)
	if statuses["x-legacy-http"] == "unknown" && statuses["x-legacy-vmess"] == "unknown" {
		r.pass("R5", "the stored state still says unknown for both records: the derivation is view-only")
	} else {
		r.fail("R5", "state statuses changed: http=%q vmess=%q", statuses["x-legacy-http"], statuses["x-legacy-vmess"])
	}
	if !strings.Contains(svc.Stderr(), "engine.excluded") {
		r.pass("R5", "neither record reached the engine (no engine.excluded path was taken)")
	} else {
		r.fail("R5", "the engine was asked to build a derived-unsupported record: %s", svc.Stderr())
	}

	// The real import command must still land unsupported with the same reasons
	// and must not fetch the HTTP URL. A batch with zero usable records is
	// rejected by the documented import rule, so one valid record rides along.
	httpLink := fmt.Sprintf("http://127.0.0.1:%d/#import-http", legacyPort.port())
	vmessLink := fmt.Sprintf("vmess://eyJhZGQiOiIxMjcuMC4wLjEifQ@127.0.0.1:%d#import-vmess", legacyPort.port())
	linkFile := filepath.Join(t.TempDir(), "legacy-links.txt")
	payload := strings.Join([]string{
		httpLink,
		vmessLink,
		fmt.Sprintf("socks5://127.0.0.1:%d#import-valid", refusableAddr(t)),
	}, "\n") + "\n"
	if err := os.WriteFile(linkFile, []byte(payload), 0o600); err != nil {
		t.Fatalf("write import fixture: %v", err)
	}
	importedData := svc.mustOK("import", "file", linkFile).mapData(t)
	importCounts, _ := importedData["records"].(map[string]any)
	if num(importCounts["valid"]) == 1 && num(importCounts["unsupported"]) == 2 && legacyPort.hits.Load() == 0 {
		r.pass("R5", "import stores the http and vmess links as unsupported and never fetches the http URL (%d connections)", legacyPort.hits.Load())
	} else {
		r.fail("R5", "import counts=%+v dials=%d", importCounts, legacyPort.hits.Load())
	}

	imported := svc.mustOK("pool", "list")
	importedRows := imported.list(t, "records")
	importedCounts, _ := imported.mapData(t)["counts"].(map[string]any)
	importedHTTP := findRow(importedRows, "display_name", "import-http")
	importedVMess := findRow(importedRows, "display_name", "import-vmess")
	if importedHTTP != nil && str(importedHTTP["status"]) == "unsupported" && str(importedHTTP["status_reason"]) == "http_exit_not_verified" &&
		importedVMess != nil && str(importedVMess["status"]) == "unsupported" && str(importedVMess["status_reason"]) == "protocol_unsupported" {
		r.pass("R5", "the imported rows carry the same unsupported status and reasons as the derived legacy rows")
	} else {
		r.fail("R5", "imported rows http=%+v vmess=%+v", importedHTTP, importedVMess)
	}
	if num(importedCounts["unsupported"]) == 4 && num(importedCounts["unknown"]) == 1 {
		r.pass("R5", "after import the pool reports unsupported 4 (2 derived + 2 imported) and only the valid record is unknown")
	} else {
		r.fail("R5", "post-import counts=%+v, want unsupported 4 and unknown 1", importedCounts)
	}
	afterStatus, _ := svc.statusNow()
	afterDoctor := svc.mustOK("doctor").mapData(t)
	afterChecks, _ := afterDoctor["checks"].([]any)
	afterQuarantined := findCheck(afterChecks, "quarantined_records")
	if containsString(afterStatus.warnings, fmt.Sprintf("%d record(s) are unsupported or quarantined", 4)) &&
		afterQuarantined != nil && strings.Contains(str(afterQuarantined["detail"]), "4 unsupported") {
		r.pass("R5", "status and doctor both count 4 unsupported records after the import")
	} else {
		r.fail("R5", "post-import status warnings=%v doctor=%+v", afterStatus.warnings, afterQuarantined)
	}
	statuses = recordStatuses(t, dir)
	if statuses["x-legacy-http"] == "unknown" && statuses["x-legacy-vmess"] == "unknown" {
		r.pass("R5", "the import and probes never rewrote the legacy records in state")
	} else {
		r.fail("R5", "post-import state statuses http=%q vmess=%q", statuses["x-legacy-http"], statuses["x-legacy-vmess"])
	}
}

// TestLegacyStoredCoolingRecordsDeriveUnsupported drives the same real product
// over a state whose undialable records were persisted as cooling (expired and
// live cooldown), healthy or unsupported/engine_record_rejected instead of
// unknown. The derived unsupported verdict and the importer's reason must win
// whatever the stored status is, except invalid; a supported protocol keeps its
// stored reason. Nothing may dial, and the derivation must never write
// unsupported to disk.
func TestLegacyStoredCoolingRecordsDeriveUnsupported(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "lgc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	counter := newLegacyDialCounter(t)
	writeLegacyStoredStatusState(t, dir, counter.port())

	svc := startService(t, env, dir, serviceConfig{
		Probes: map[string]any{"targets": []string{}, "timeout_ms": 20000},
	})
	t.Cleanup(func() { _ = svc.stop() })

	// pool list: the stored cooling is no reason to leave the unknown-era record
	// out of the unsupported count, and the pre-R5 engine reason loses to the
	// importer's reason only for protocols v1 can never dial.
	list := svc.mustOK("pool", "list")
	counts, _ := list.mapData(t)["counts"].(map[string]any)
	rows := list.list(t, "records")
	if num(counts["total"]) == 6 && num(counts["unknown"]) == 0 && num(counts["healthy"]) == 0 &&
		num(counts["cooling"]) == 0 && num(counts["unsupported"]) == 5 && num(counts["invalid"]) == 1 &&
		num(counts["usable"]) == 0 {
		r.pass("R5", "pool list counts five undialable records as unsupported and keeps the stored invalid record as invalid (total 6, unknown/healthy/cooling 0, unsupported 5, invalid 1)")
	} else {
		r.fail("R5", "pool list counts=%+v, want total 6, unknown/healthy/cooling 0, unsupported 5, invalid 1", counts)
	}
	rowChecks := []struct {
		id     string
		status string
		reason string
		label  string
	}{
		{"x-http-cooling-expired", "unsupported", "http_exit_not_verified", "the http record stored cooling with an expired cooldown reports unsupported/http_exit_not_verified (never bare unknown)"},
		{"x-http-cooling-live", "unsupported", "http_exit_not_verified", "the http record stored cooling inside its cooldown reports unsupported/http_exit_not_verified (cooling does not survive)"},
		{"x-http-healthy", "unsupported", "http_exit_not_verified", "the http record stored healthy reports unsupported/http_exit_not_verified (healthy does not survive)"},
		{"x-http-invalid", "invalid", "invalid", "the http record stored invalid stays invalid"},
		{"x-http-engine-rejected", "unsupported", "http_exit_not_verified", "the http record stored unsupported/engine_record_rejected reports the importer reason http_exit_not_verified"},
		{"x-vless-engine-rejected", "unsupported", "engine_record_rejected", "the supported vless record stored unsupported/engine_record_rejected keeps engine_record_rejected"},
	}
	for _, want := range rowChecks {
		row := findRow(rows, "id", want.id)
		if row != nil && str(row["status"]) == want.status && str(row["status_reason"]) == want.reason {
			r.pass("R5", "%s (row %s)", want.label, want.id)
		} else {
			r.fail("R5", "row %s=%+v, want %s/%s", want.id, row, want.status, want.reason)
		}
	}

	// status and doctor count the same view.
	status, _ := svc.statusNow()
	wantWarning := fmt.Sprintf("%d record(s) are unsupported or quarantined", 5)
	if status.poolTotal == 6 && status.poolUsable == 0 && status.poolCooling == 0 &&
		containsString(status.warnings, wantWarning) {
		r.pass("R5", "status reports total 6, usable 0, cooling 0 and the 5-record unsupported warning")
	} else {
		r.fail("R5", "status total=%d usable=%d cooling=%d warnings=%v", status.poolTotal, status.poolUsable, status.poolCooling, status.warnings)
	}
	doctor := svc.mustOK("doctor").mapData(t)
	checks, _ := doctor["checks"].([]any)
	quarantined := findCheck(checks, "quarantined_records")
	poolCheck := findCheck(checks, "pool")
	wantQuarantined := "5 unsupported and 1 invalid records are kept for reporting and never dialed"
	if quarantined != nil && str(quarantined["detail"]) == wantQuarantined &&
		poolCheck != nil && str(poolCheck["status"]) == "warn" && strings.Contains(str(poolCheck["detail"]), "cooling 0, unsupported 5, invalid 1") {
		r.pass("R5", "doctor counts 5 unsupported, 1 invalid, 0 cooling and repeats the never-dialed wording")
	} else {
		r.fail("R5", "doctor quarantined=%+v pool=%+v", quarantined, poolCheck)
	}

	// The read-only surfaces above leave the stored statuses alone: the
	// derivation is view-only for cooling and engine-rejected records too.
	statuses := recordStatuses(t, dir)
	if statuses["x-http-cooling-expired"] == "cooling" && statuses["x-http-cooling-live"] == "cooling" &&
		statuses["x-http-healthy"] == "healthy" && statuses["x-http-invalid"] == "invalid" &&
		statuses["x-http-engine-rejected"] == "unsupported" && statuses["x-vless-engine-rejected"] == "unsupported" {
		r.pass("R5", "state still says cooling/cooling/healthy/invalid/unsupported/unsupported after pool list, status and doctor")
	} else {
		r.fail("R5", "state statuses changed: %v", statuses)
	}

	// No record can reach a dial: every explicit probe is rejected as
	// unsupported before a TCP connection, whatever the stored status.
	rejectedRows := 0
	for _, want := range rowChecks {
		results := svc.mustOK("pool", "test", "--exit", want.id).list(t, "results")
		if len(results) == 1 && str(results[0]["outcome"]) == "rejected" && str(results[0]["failure"]) == "unsupported" {
			rejectedRows++
		} else {
			r.fail("R5", "pool test --exit %s results=%+v", want.id, results)
		}
	}
	if rejectedRows == len(rowChecks) && counter.hits.Load() == 0 {
		r.pass("R5", "pool test rejects all six records and the endpoint saw 0 connections")
	} else {
		r.fail("R5", "rejected=%d/%d dials=%d", rejectedRows, len(rowChecks), counter.hits.Load())
	}

	// The probe batch is a mutation, so the documented cooldown normalization
	// applies: the expired health entry turns the stored status unknown. The
	// derivation itself never wrote unsupported, and the other statuses kept.
	statuses = recordStatuses(t, dir)
	if statuses["x-http-cooling-expired"] == "unknown" && statuses["x-http-cooling-live"] == "cooling" &&
		statuses["x-http-healthy"] == "healthy" && statuses["x-http-invalid"] == "invalid" &&
		statuses["x-http-engine-rejected"] == "unsupported" && statuses["x-vless-engine-rejected"] == "unsupported" {
		r.pass("R5", "after the probe mutation only the expired cooldown normalized to unknown; no record was persisted as unsupported by the view")
	} else {
		r.fail("R5", "post-probe state statuses=%v", statuses)
	}
}
