package trayui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/pool"
)

func fixturePoolID(i int) contract.RecordID {
	return contract.RecordID(fmt.Sprintf("x-%036d", i))
}

func poolRowKey(id contract.RecordID) string {
	return "pool.row." + stableKeyPart(string(id))
}

func countDirectChildrenWithPrefix(n Node, prefix string) int {
	count := 0
	for _, child := range n.Children {
		if strings.HasPrefix(child.Key, prefix) {
			count++
		}
	}
	return count
}

// TestPoolGroupsShowCountsAndTwentyRowPages covers the 176-row fixture, the
// four contract groups, their counts, and the fixed 20-row local pagination.
func TestPoolGroupsShowCountsAndTwentyRowPages(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "pool.summary"); n.Label != "176 records · 140 usable · 100 healthy · 20 cooling" {
		t.Fatalf("pool summary = %q", n.Label)
	}

	groups := []struct {
		key      string
		label    string
		page     string
		rows     int
		hasPages bool
	}{
		{"pool.group.pool.healthy", "Healthy (100)", "Page 1/5", 20, true},
		{"pool.group.pool.untested", "Untested (40)", "Page 1/2", 20, true},
		{"pool.group.pool.cooling", "Cooling (20)", "", 20, false},
		{"pool.group.pool.unsupported", "Unsupported/Invalid (16)", "", 16, false},
	}
	for _, tc := range groups {
		group, ok := findNode(view.Root, tc.key)
		if !ok {
			t.Fatalf("missing group %q", tc.key)
		}
		if group.Label != tc.label {
			t.Fatalf("%s label = %q, want %q", tc.key, group.Label, tc.label)
		}
		if got := countDirectChildrenWithPrefix(group, "pool.row."); got != tc.rows {
			t.Fatalf("%s rows = %d, want %d", tc.key, got, tc.rows)
		}
		page, ok := findNode(group, tc.key+".page")
		if !tc.hasPages {
			if ok {
				t.Fatalf("%s rendered page controls for a single page", tc.key)
			}
			continue
		}
		if !ok || page.Label != tc.page {
			t.Fatalf("%s page = %+v, want %q", tc.key, page, tc.page)
		}
		prev, _ := findNode(group, tc.key+".prev")
		next, _ := findNode(group, tc.key+".next")
		if prev.Enabled || !next.Enabled {
			t.Fatalf("%s page 1 controls: prev=%t next=%t", tc.key, prev.Enabled, next.Enabled)
		}
	}

	// Page navigation changes the healthy rows without crossing group counts.
	if _, err := s.ctl.Trigger(ctx, ActionPageNext, Target{Section: SectionPoolHealthy}); err != nil {
		t.Fatal(err)
	}
	view = s.ctl.View()
	healthy, _ := findNode(view.Root, "pool.group.pool.healthy")
	if page, _ := findNode(healthy, "pool.group.pool.healthy.page"); page.Label != "Page 2/5" {
		t.Fatalf("healthy page after next = %q", page.Label)
	}
	rows := make([]string, 0, PageSize)
	for _, child := range healthy.Children {
		if strings.HasPrefix(child.Key, "pool.row.") {
			rows = append(rows, child.Key)
		}
	}
	if len(rows) != PageSize {
		t.Fatalf("healthy page 2 rows = %d", len(rows))
	}
	if want := poolRowKey(fixturePoolID(PageSize)); rows[0] != want {
		t.Fatalf("healthy page 2 starts at %q, want %q", rows[0], want)
	}

	// The last page clamps and disables Next.
	s.ctl.SetPage(SectionPoolUntested, 99)
	view = s.ctl.View()
	untested, _ := findNode(view.Root, "pool.group.pool.untested")
	if page, _ := findNode(untested, "pool.group.pool.untested.page"); page.Label != "Page 2/2" {
		t.Fatalf("clamped untested page = %q", page.Label)
	}
	if next, _ := findNode(untested, "pool.group.pool.untested.next"); next.Enabled {
		t.Fatal("Next enabled on the last page")
	}
	if got := countDirectChildrenWithPrefix(untested, "pool.row."); got != PageSize {
		t.Fatalf("untested last page rows = %d", got)
	}
}

// TestPoolRowDisabledStatesFollowStatus covers select/test only for usable
// rows, remove for every row, and the stopped-mode shutdown of row mutations.
func TestPoolRowDisabledStatesFollowStatus(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	view := s.ctl.View()
	cases := []struct {
		index      int
		usable     bool
		statusName string
	}{
		{0, true, "healthy"},
		{100, true, "untested"},
		{140, false, "cooling"},
		{160, false, "unsupported"},
		{170, false, "invalid"},
	}
	for _, tc := range cases {
		row := poolRowKey(fixturePoolID(tc.index))
		for suffix, want := range map[string]bool{
			".select": tc.usable,
			".test":   tc.usable,
			".remove": true,
		} {
			n, ok := findNode(view.Root, row+suffix)
			if !ok {
				t.Fatalf("missing %s%s (%s)", row, suffix, tc.statusName)
			}
			if n.Enabled != want {
				t.Fatalf("%s%s enabled = %t, want %t", row, suffix, n.Enabled, want)
			}
		}
	}

	// A stored skip-verify record without a pin is not selectable/probeable even
	// if its cached status says healthy; removal stays available.
	insecure := newFixture()
	insecure.pool[0].InsecureTLS = true
	s = newSetup(t, insecure, nil)
	view = s.ctl.View()
	row := poolRowKey(fixturePoolID(0))
	for _, suffix := range []string{".select", ".test"} {
		if n, _ := findNode(view.Root, row+suffix); n.Enabled {
			t.Fatalf("%s%s enabled for an insecure record", row, suffix)
		}
	}
	if n, _ := findNode(view.Root, row+".remove"); !n.Enabled {
		t.Fatal("remove disabled for an insecure record")
	}

	// Stopped mode disables pool mutation and probes but keeps read-only rows
	// visible (docs/contracts.md §7.3).
	stopped := newFixture()
	stopped.status["mode"] = string(contract.ModeStopped)
	stopped.status["listeners_open"] = false
	stopped.status["resume_requires_restart"] = false
	s = newSetup(t, stopped, nil)
	view = s.ctl.View()
	for _, suffix := range []string{".select", ".test", ".remove"} {
		if n, _ := findNode(view.Root, row+suffix); n.Enabled {
			t.Fatalf("%s%s enabled in stopped mode", row, suffix)
		}
	}

	// Auto/pin radio state follows the service status selection.
	pinned := newFixture()
	pinned.status["selected_exit"] = string(fixturePoolID(0))
	s = newSetup(t, pinned, nil)
	view = s.ctl.View()
	if n, _ := findNode(view.Root, "pool.auto"); n.Checked {
		t.Fatal("Auto select checked while an exit is pinned")
	}
	node, ok := findNode(view.Root, "pool.pinned")
	if !ok || !node.Checked || node.Check != CheckRadio || node.Target.ExitID != contract.ExitID(fixturePoolID(0)) {
		t.Fatalf("pinned node = %+v", node)
	}
}

// TestPoolSelectAndRemoveReachTheSocketOnce covers the non-job pool actions and
// the remove confirmation: a click never executes the destructive request.
func TestPoolSelectAndRemoveReachTheSocketOnce(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	id := fixturePoolID(0)

	if _, err := s.ctl.Trigger(ctx, ActionPoolSelectAuto, Target{}); err != nil {
		t.Fatal(err)
	}
	if got := poolLastRequest(t, s, control.OpPoolSelect); got.ID != "auto" {
		t.Fatalf("auto request id = %q", got.ID)
	}
	if got := s.ctl.View().LastAction; got != "Automatic exit selection restored" {
		t.Fatalf("auto last action = %q", got)
	}

	if _, err := s.ctl.Trigger(ctx, ActionPoolSelect, Target{ExitID: contract.ExitID(id)}); err != nil {
		t.Fatal(err)
	}
	if got := poolLastRequest(t, s, control.OpPoolSelect); got.ID != string(id) {
		t.Fatalf("pin request id = %q", got.ID)
	}
	if got := s.ctl.View().LastAction; got != "Exit pinned" {
		t.Fatalf("pin last action = %q", got)
	}

	before := s.caller.countOp(control.OpPoolRemove)
	res, err := s.ctl.Trigger(ctx, ActionPoolRemove, Target{ExitID: contract.ExitID(id)})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("remove trigger = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpPoolRemove) != before {
		t.Fatal("remove executed before confirmation")
	}
	pending := s.ctl.View().Pending
	if pending == nil || pending.Action != ActionPoolRemove || !strings.Contains(pending.Label, "Remove exit "+shortID(string(id))) {
		t.Fatalf("pending = %+v", pending)
	}

	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	if got := poolLastRequest(t, s, control.OpPoolRemove); got.ID != string(id) {
		t.Fatalf("remove request id = %q", got.ID)
	}
	if s.caller.countOp(control.OpPoolRemove) != before+1 {
		t.Fatal("remove did not run exactly once")
	}
	notes := s.notifier.all()
	if len(notes) == 0 || notes[len(notes)-1].Body != "Exit removed (0 connections closed)" {
		t.Fatalf("remove notification = %+v", notes)
	}
	if strings.Contains(notes[len(notes)-1].Body, string(id)) {
		t.Fatalf("remove notification leaked the exit id: %q", notes[len(notes)-1].Body)
	}
}

// TestPoolTestJobsUseTheCappedBatchOnce covers the job classification, the
// documented 20-exit cap request shape, the duplicate-job rule, and the
// selected-exit variant.
func TestPoolTestJobsUseTheCappedBatchOnce(t *testing.T) {
	fx := newFixture()
	selected := fixturePoolID(0)
	fx.status["selected_exit"] = string(selected)
	s := newSetup(t, fx, nil)
	ctx := context.Background()
	view := s.ctl.View()

	first, ok := findNode(view.Root, "pool.test.first20")
	if !ok || first.Label != fmt.Sprintf("Test first %d usable", contract.MaxPoolTestExits) {
		t.Fatalf("first-20 node = %+v", first)
	}
	if contract.MaxPoolTestExits != 20 {
		t.Fatalf("MaxPoolTestExits = %d, want 20", contract.MaxPoolTestExits)
	}
	selectedNode, ok := findNode(view.Root, "pool.test.selected")
	if !ok || selectedNode.Target.ExitID != contract.ExitID(selected) {
		t.Fatalf("selected test node = %+v", selectedNode)
	}

	entered, release := s.caller.gateOn(control.OpPoolTest)
	res, err := s.ctl.Trigger(ctx, ActionPoolTestFirst20, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("first-20 trigger = %+v, %v", res, err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pool test job never reached the socket")
	}
	view = s.ctl.View()
	if view.Job == nil || view.Job.Name != "pool test" {
		t.Fatalf("job = %+v", view.Job)
	}
	// The batch request carries no id: the service owns the documented cap.
	req := poolLastRequest(t, s, control.OpPoolTest)
	if req.ID != "" {
		t.Fatalf("first-20 request id = %q, want the capped no-id form", req.ID)
	}
	if s.caller.countOp(control.OpPoolTest) != 1 {
		t.Fatal("duplicate pool test request was sent")
	}
	if _, err := s.ctl.Trigger(ctx, ActionPoolTest, Target{ExitID: contract.ExitID(selected)}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second pool test while a job runs = %v, want ErrBusy", err)
	}
	if s.caller.countOp(control.OpPoolTest) != 1 {
		t.Fatal("a blocked duplicate reached the socket")
	}

	release()
	waitFor(t, "first pool test notification", func() bool {
		notes := s.notifier.all()
		return len(notes) > 0 && notes[len(notes)-1].Body == "Pool test finished (1 results)"
	})
	notes := s.notifier.all()
	if notes[len(notes)-1].Body != "Pool test finished (1 results)" {
		t.Fatalf("pool test notification = %+v", notes)
	}
	if strings.Contains(notes[len(notes)-1].Body, "destination") || containsControl(notes[len(notes)-1].Body) {
		t.Fatalf("unsafe pool test notification: %q", notes[len(notes)-1].Body)
	}

	// A selected-exit test is one targeted request, not a second batch.
	if _, err := s.ctl.Trigger(ctx, ActionPoolTest, Target{ExitID: contract.ExitID(selected)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "selected pool test job", func() bool { return s.ctl.View().Job == nil })
	if got := poolLastRequest(t, s, control.OpPoolTest); got.ID != string(selected) {
		t.Fatalf("selected test request id = %q", got.ID)
	}
}

// TestLegacyUndialableRowsAreUnsupportedNotUsable uses the real pool view
// derivation to prove a stored-unknown but undialable record is never counted
// usable, never lands in Untested, and a forced select surfaces the service's
// clear rejection instead of claiming success.
func TestLegacyUndialableRowsAreUnsupportedNotUsable(t *testing.T) {
	legacy := func(id string, order int, protocol contract.Protocol) contract.Record {
		return contract.Record{
			ID: contract.RecordID(id), Order: order, Protocol: protocol,
			Server: "127.0.0.1", Port: uint16(4100 + order),
			Transport: contract.TransportRaw, Security: contract.SecurityNone,
			Status: contract.RecordUnknown,
		}
	}
	state := contract.State{Records: []contract.Record{
		legacy("x-legacy-http", 0, contract.ProtocolHTTP),
		legacy("x-legacy-vmess", 1, contract.Protocol("vmess")),
		legacy("x-live-socks", 2, contract.ProtocolSOCKS5),
	}}
	built := pool.Build(state, time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	rows := built.List()
	counts := built.Counts()
	if counts.Total != 3 || counts.Usable != 1 || counts.Unknown != 1 || counts.Unsupported != 2 {
		t.Fatalf("derived counts = %+v", counts)
	}

	fx := newFixture()
	fx.pool = rows
	fx.counts = counts
	fx.status["pool"] = map[string]any{
		"total": counts.Total, "usable": counts.Usable,
		"healthy": counts.Healthy, "cooling": counts.Cooling,
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "pool.summary"); n.Label != "3 records · 1 usable · 0 healthy · 0 cooling" {
		t.Fatalf("summary = %q", n.Label)
	}
	for key, label := range map[string]string{
		"pool.group.pool.healthy":     "Healthy (0)",
		"pool.group.pool.untested":    "Untested (1)",
		"pool.group.pool.cooling":     "Cooling (0)",
		"pool.group.pool.unsupported": "Unsupported/Invalid (2)",
	} {
		if n, _ := findNode(view.Root, key); n.Label != label {
			t.Fatalf("%s label = %q, want %q", key, n.Label, label)
		}
	}

	httpKey := poolRowKey("x-legacy-http")
	if _, ok := findNode(view.Root, httpKey); !ok {
		t.Fatal("legacy http row missing from the menu")
	}
	unsupported, _ := findNode(view.Root, "pool.group.pool.unsupported")
	if _, ok := findNode(unsupported, httpKey); !ok {
		t.Fatal("legacy http row is not in Unsupported/Invalid")
	}
	untested, _ := findNode(view.Root, "pool.group.pool.untested")
	if _, ok := findNode(untested, httpKey); ok {
		t.Fatal("legacy http row is claimed as untested/usable")
	}
	row, _ := findNode(view.Root, httpKey)
	if !strings.Contains(row.Label, contract.ReasonHTTPExitNotVerified) {
		t.Fatalf("legacy http label = %q", row.Label)
	}
	for _, suffix := range []string{".select", ".test"} {
		if n, _ := findNode(view.Root, httpKey+suffix); n.Enabled {
			t.Fatalf("%s enabled for a derived-unsupported record", suffix)
		}
	}
	live := poolRowKey("x-live-socks")
	for _, suffix := range []string{".select", ".test"} {
		if n, _ := findNode(view.Root, live+suffix); !n.Enabled {
			t.Fatalf("%s disabled for an unknown but dialable socks5 record", suffix)
		}
	}

	// A forced select (as if a stale UI sent it) is answered by the service
	// with a clear state_locked error, not a usable claim.
	s.caller.setFail(control.OpPoolSelect, contract.StateLocked, "https://secret.example/exit\x1b[31m\u202e")
	_, err := s.ctl.Trigger(context.Background(), ActionPoolSelect, Target{ExitID: "x-legacy-http"})
	var envErr *EnvelopeError
	if !errors.As(err, &envErr) || envErr.Kind != contract.StateLocked {
		t.Fatalf("legacy select error = %v, want state_locked EnvelopeError", err)
	}
	view = s.ctl.View()
	if view.LastAction != "Failed: state_locked" {
		t.Fatalf("last action = %q", view.LastAction)
	}
	notes := s.notifier.all()
	if len(notes) == 0 {
		t.Fatal("legacy select produced no notification")
	}
	last := notes[len(notes)-1].Body
	if last != "Failed: state_locked" || containsControl(last) || strings.Contains(last, "secret") || strings.Contains(last, "http") {
		t.Fatalf("legacy select notification = %q", last)
	}
}

// TestPoolJobFailureNotificationIsSanitized covers the notification boundary
// for a pool job error whose raw service message carries a URL and controls.
func TestPoolJobFailureNotificationIsSanitized(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	s.caller.setFail(control.OpPoolTest, contract.Timeout, "https://secret.example/exit\x1b[31m\u202e\r\n")
	if _, err := s.ctl.Trigger(context.Background(), ActionPoolTestFirst20, Target{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed pool test notification", func() bool {
		notes := s.notifier.all()
		return len(notes) > 0 && notes[len(notes)-1].Body == "Failed: timeout"
	})
	if got := s.ctl.View().LastAction; got != "Failed: timeout" {
		t.Fatalf("last action = %q", got)
	}
	notes := s.notifier.all()
	if len(notes) == 0 {
		t.Fatal("failed pool job produced no notification")
	}
	last := notes[len(notes)-1]
	if last.Body != "Failed: timeout" {
		t.Fatalf("notification body = %q", last.Body)
	}
	if containsControl(last.Body) || runeLen(last.Body) > HeaderLimit ||
		strings.Contains(last.Body, "secret") || strings.Contains(last.Body, "http") {
		t.Fatalf("notification leaked raw service text: %q", last.Body)
	}
}

func poolLastRequest(t *testing.T, s *controllerSetup, op string) control.Request {
	t.Helper()
	reqs := s.caller.requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Op == op {
			return reqs[i]
		}
	}
	t.Fatalf("no %s request was sent", op)
	return control.Request{}
}
