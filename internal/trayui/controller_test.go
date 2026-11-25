package trayui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

func TestPendingReplacementExpiryAndCancel(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()

	res, err := s.ctl.Trigger(ctx, ActionStopListening, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("stop trigger = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpMode) != 0 {
		t.Fatal("a pending action executed before confirmation")
	}
	view := s.ctl.View()
	if view.Pending == nil || view.Pending.Action != ActionStopListening {
		t.Fatalf("pending = %+v", view.Pending)
	}
	confirm, ok := findNode(view.Root, "pending.confirm")
	if !ok || !strings.HasPrefix(confirm.Label, "Confirm: Stop listening") {
		t.Fatalf("confirm node = %+v", confirm)
	}

	// A new action replaces the pending record; only one can exist.
	if _, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: "r-001"}); err != nil {
		t.Fatal(err)
	}
	if p := s.ctl.View().Pending; p == nil || p.Action != ActionRuleRemove || p.Target.RuleID != "r-001" {
		t.Fatalf("pending was not replaced: %+v", p)
	}
	if s.caller.countOp(control.OpRulesRemove) != 0 {
		t.Fatal("replacement executed the destructive action")
	}

	// A non-destructive action clears the pending record instead of leaving a
	// stale confirmation that could fire later.
	if _, err := s.ctl.Trigger(ctx, ActionModeTargeted, Target{}); err != nil {
		t.Fatal(err)
	}
	if s.ctl.View().Pending != nil {
		t.Fatal("pending survived a new action")
	}
	if s.caller.countOp(control.OpMode) != 1 {
		t.Fatal("mode action did not execute")
	}

	// Cancel.
	if _, err := s.ctl.Trigger(ctx, ActionStopListening, Target{}); err != nil {
		t.Fatal(err)
	}
	if !s.ctl.CancelPending() {
		t.Fatal("CancelPending returned false")
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("confirm after cancel = %v", err)
	}

	// Expiry.
	if _, err := s.ctl.Trigger(ctx, ActionStopListening, Target{}); err != nil {
		t.Fatal(err)
	}
	s.clock.Advance(61 * time.Second)
	view = s.ctl.View()
	if view.Pending != nil {
		t.Fatal("expired pending survived View")
	}
	if view.LastAction != "Confirmation expired" {
		t.Fatalf("last action = %q", view.LastAction)
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("confirm after expiry = %v", err)
	}
	if s.caller.countOp(control.OpMode) != 1 {
		t.Fatal("expired confirmation executed a request")
	}
}

func TestConfirmRunsStoredTargetExactlyOnce(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	bad := "r-\x1b[2Jbad\u202e\r\n"
	if _, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: contract.RuleID(bad)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	reqs := s.caller.requests()
	var remove control.Request
	removeCount := 0
	for _, req := range reqs {
		if req.Op == control.OpRulesRemove {
			remove = req
			removeCount++
		}
	}
	if removeCount != 1 || remove.ID != bad {
		t.Fatalf("rules.remove requests = %d id=%q, want one with the stored id", removeCount, remove.ID)
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("second confirm = %v", err)
	}
	if s.caller.countOp(control.OpRulesRemove) != 1 {
		t.Fatal("second confirm executed another request")
	}
}

func TestOneJobAtATimeAndNoDuplicateRequests(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	entered, release := s.caller.gateOn(control.OpPoolRefresh)
	res, err := s.ctl.Trigger(ctx, ActionPoolRefresh, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("refresh trigger = %+v, %v", res, err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("job never started")
	}
	if view := s.ctl.View(); view.Job == nil {
		t.Fatal("View has no running job")
	}
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "job"); !strings.Contains(n.Label, "pool refresh") || n.Enabled {
		t.Fatalf("job node = %+v", n)
	}
	if n, _ := findNode(view.Root, "mode.targeted"); n.Enabled {
		t.Fatal("mode action enabled while a job runs")
	}
	if _, err := s.ctl.Trigger(ctx, ActionPoolRefresh, Target{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second job = %v, want ErrBusy", err)
	}
	if _, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: "r-003"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("mutation during job = %v, want ErrBusy", err)
	}
	if s.caller.countOp(control.OpPoolRefresh) != 1 {
		t.Fatalf("pool refresh requests = %d, want 1", s.caller.countOp(control.OpPoolRefresh))
	}
	if s.caller.countOp(control.OpRulesRemove) != 0 {
		t.Fatal("blocked mutation reached the socket")
	}
	// A read-only desktop refresh is still allowed while a job runs.
	if _, err := s.ctl.Trigger(ctx, ActionDesktopRefresh, Target{}); err != nil {
		t.Fatalf("read-only action during job = %v", err)
	}
	release()
	waitFor(t, "job completion", func() bool { return s.ctl.View().Job == nil })
	if got := s.ctl.View().LastAction; got != "Sources refreshed" {
		t.Fatalf("last action = %q", got)
	}
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatalf("next job after completion: %v", err)
	}
	waitFor(t, "backup completion", func() bool { return s.ctl.View().Job == nil })
}

func TestJobInterruptedWhenEngineBecomesUnavailable(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	entered, release := s.caller.gateOn(control.OpBackup)
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	s.caller.setFail(control.OpStatus, contract.Timeout, "https://secret.example/x")
	if err := s.ctl.RefreshStatus(ctx); err == nil {
		t.Fatal("expected unavailable refresh")
	}
	view := s.ctl.View()
	if view.Job == nil || !view.Job.Interrupted {
		t.Fatalf("job = %+v, want interrupted", view.Job)
	}
	if view.LastAction != "Interrupted" {
		t.Fatalf("last action = %q", view.LastAction)
	}
	release()
	waitFor(t, "interrupted job completion", func() bool { return s.ctl.View().Job == nil })
	if got := s.ctl.View().LastAction; got != "Interrupted" {
		t.Fatalf("final last action = %q", got)
	}
	notes := s.notifier.all()
	if len(notes) == 0 || notes[len(notes)-1].Body != "Interrupted" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestRestartEventOrdering(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	res, err := s.ctl.Trigger(ctx, ActionRestartEngine, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("restart trigger = %+v, %v", res, err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("restart confirm: %v", err)
	}
	waitFor(t, "restart request", func() bool {
		_, restarts := s.life.counts()
		return restarts == 1
	})
	if view := s.ctl.View(); view.Job == nil || view.Engine == EngineRestarting {
		t.Fatalf("before restarting event: job=%+v engine=%s", view.Job, view.Engine)
	}

	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleRestarting})
	view := s.ctl.View()
	if view.Engine != EngineRestarting || view.Job == nil {
		t.Fatalf("after restarting: job=%+v engine=%s", view.Job, view.Engine)
	}
	if node, _ := findNode(view.Root, "status"); node.Label != "Restarting engine…" {
		t.Fatalf("status = %q", node.Label)
	}
	if node, _ := findNode(view.Root, "mode.targeted"); node.Enabled {
		t.Fatal("management action enabled during restart")
	}

	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleReady})
	view = s.ctl.View()
	if view.Job != nil {
		t.Fatalf("job survived ready: %+v", view.Job)
	}
	if view.Engine == EngineRestarting {
		t.Fatal("restarting overlay survived ready")
	}
	if view.LastAction != "Engine restart confirmed" {
		t.Fatalf("last action = %q", view.LastAction)
	}
	if notes := s.notifier.all(); len(notes) == 0 || notes[len(notes)-1].Body != "Engine restart confirmed" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestRestartReadyWithoutRestartingIsUnconfirmed(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionRestartEngine, Target{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleReady})
	if s.ctl.View().Job == nil {
		t.Fatal("ready without restarting was accepted as success")
	}
	s.clock.Advance(46 * time.Second)
	view := s.ctl.View()
	if view.Job != nil {
		t.Fatalf("restart job did not expire: %+v", view.Job)
	}
	if view.LastAction != "Engine restart unconfirmed" {
		t.Fatalf("last action = %q", view.LastAction)
	}
	if notes := s.notifier.all(); len(notes) == 0 || notes[len(notes)-1].Body != "Engine restart unconfirmed" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestAutomaticRestartEventsDoNotFakeAJob(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleRestarting})
	if got := s.ctl.View().Engine; got != EngineRestarting {
		t.Fatalf("engine = %s", got)
	}
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleReady})
	view := s.ctl.View()
	if view.Engine != EngineStarting {
		t.Fatalf("engine = %s, want starting until the binder refreshes status", view.Engine)
	}
	if view.Job != nil {
		t.Fatalf("automatic restart created a job: %+v", view.Job)
	}
}

func TestRestartEventInterruptsAnotherJob(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	entered, release := s.caller.gateOn(control.OpBackup)
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleRestarting})
	view := s.ctl.View()
	if view.Job == nil || !view.Job.Interrupted {
		t.Fatalf("job = %+v", view.Job)
	}
	if view.Engine != EngineRestarting {
		t.Fatalf("engine = %s", view.Engine)
	}
	release()
	waitFor(t, "interrupted job", func() bool { return s.ctl.View().Job == nil })
	if got := s.ctl.View().LastAction; got != "Interrupted" {
		t.Fatalf("last action = %q", got)
	}
}

func TestFingerprintPreventsUnchangedRebuild(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	c := s.ctl
	first := c.View()
	builds := c.builds
	if first.Fingerprint == "" {
		t.Fatal("empty fingerprint")
	}
	if err := c.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := c.View()
	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint changed for identical data:\n%s\n%s", first.Fingerprint, second.Fingerprint)
	}
	if c.builds != builds {
		t.Fatalf("menu rebuilt %d times for identical data", c.builds-builds)
	}

	s.caller.mu.Lock()
	s.caller.fx.status["warnings"] = []string{"a new warning"}
	s.caller.mu.Unlock()
	if err := c.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	third := c.View()
	if third.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change with new data")
	}
	if c.builds != builds+1 {
		t.Fatalf("build count = %d, want %d", c.builds, builds+1)
	}
}

func TestQuitDuringJobNeedsExtraConfirmation(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	entered, release := s.caller.gateOn(control.OpBackup)
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	res, err := s.ctl.Trigger(ctx, ActionQuit, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("quit during job = %+v, %v", res, err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm quit: %v", err)
	}
	if quits, _ := s.life.counts(); quits != 1 {
		t.Fatalf("quit requests = %d", quits)
	}
	release()
	waitFor(t, "job after quit", func() bool { return s.ctl.View().Job == nil })
}

func TestQuitRestoreFailureDoesNotQuit(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionQuitRestoreDesktop, Target{}); err != nil {
		t.Fatal(err)
	}
	s.caller.setFail(control.OpDesktopRestore, contract.StateLocked, "denied")
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed quit job", func() bool { return s.ctl.View().Job == nil })
	if quits, _ := s.life.counts(); quits != 0 {
		t.Fatalf("quit ran after a failed restore: %d", quits)
	}
	if got := s.ctl.View().LastAction; got != "Failed: state_locked" {
		t.Fatalf("last action = %q", got)
	}
}

func TestAutostartToggleRunsLocallyAndUpdatesTheMenu(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if node, _ := findNode(s.ctl.View().Root, "settings.autostart"); node.Label != "Start at login: Off" {
		t.Fatalf("label = %q", node.Label)
	}
	if _, err := s.ctl.Trigger(ctx, ActionAutostartToggle, Target{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "autostart job", func() bool { return s.ctl.View().Job == nil })
	if got := s.ctl.Snapshot().Autostart; !got.Enabled || !got.Managed {
		t.Fatalf("autostart = %+v", got)
	}
	if node, _ := findNode(s.ctl.View().Root, "settings.autostart"); node.Label != "Start at login: On" || !node.Checked {
		t.Fatalf("autostart node = %+v", node)
	}
}

func TestFingerprintIgnoresLoadTimestamp(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	before := s.ctl.View()
	loaded := s.ctl.Snapshot().LoadedAt
	s.clock.Advance(5 * time.Second)
	if err := s.ctl.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := s.ctl.View()
	if s.ctl.Snapshot().LoadedAt.Equal(loaded) {
		t.Fatal("LoadedAt did not change; fixture did not exercise the timestamp")
	}
	if before.Fingerprint != after.Fingerprint {
		t.Fatal("a newer LoadedAt alone changed the menu fingerprint")
	}
}
