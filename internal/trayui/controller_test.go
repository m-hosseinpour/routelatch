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

func TestTrayActionsRunOnTheClick(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()

	if res, err := s.ctl.Trigger(ctx, ActionStopListening, Target{}); err != nil || res.Status != ResultCompleted {
		t.Fatalf("stop = %+v, %v", res, err)
	}
	if got := s.caller.countOp(control.OpMode); got != 1 {
		t.Fatalf("stop requests = %d, want 1", got)
	}
	if got := s.ctl.View().LastAction; got != "Stopped listening" {
		t.Fatalf("last action = %q", got)
	}

	if res, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: "r-005"}); err != nil || res.Status != ResultCompleted {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	removes := 0
	for _, req := range s.caller.requests() {
		if req.Op == control.OpRulesRemove {
			removes++
			if req.ID != "r-005" {
				t.Fatalf("rules.remove id = %q, want r-005", req.ID)
			}
		}
	}
	if removes != 1 {
		t.Fatalf("rules.remove requests = %d, want 1", removes)
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
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("restart trigger = %+v, %v", res, err)
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

func TestQuitDuringJobInterruptsTheJob(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	entered, release := s.caller.gateOn(control.OpBackup)
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	res, err := s.ctl.Trigger(ctx, ActionQuit, Target{})
	if err != nil || res.Status != ResultCompleted {
		t.Fatalf("quit during job = %+v, %v", res, err)
	}
	if quits, _ := s.life.counts(); quits != 1 {
		t.Fatalf("quit requests = %d", quits)
	}
	release()
	waitFor(t, "job after quit", func() bool { return s.ctl.View().Job == nil })
}

func TestStopAndRestartRunOnceWithAppliedDesktopProxy(t *testing.T) {
	fx := newFixture()
	fx.desktop.Applied = true
	s := newSetup(t, fx, nil)
	ctx := context.Background()

	if res, err := s.ctl.Trigger(ctx, ActionStopListening, Target{}); err != nil || res.Status != ResultCompleted {
		t.Fatalf("stop = %+v, %v", res, err)
	}
	if got := s.caller.countOp(control.OpMode); got != 1 {
		t.Fatalf("stop requests = %d, want 1", got)
	}
	if got := s.ctl.View().LastAction; got != "Stopped listening" {
		t.Fatalf("stop last action = %q", got)
	}

	fx = newFixture()
	fx.desktop.Applied = true
	s = newSetup(t, fx, nil)
	if res, err := s.ctl.Trigger(ctx, ActionRestartEngine, Target{}); err != nil || res.Status != ResultJobStarted {
		t.Fatalf("restart = %+v, %v", res, err)
	}
	waitFor(t, "restart request", func() bool {
		_, restarts := s.life.counts()
		return restarts == 1
	})
}

func TestModeMenuWarnsWhenDesktopProxyMayBeCut(t *testing.T) {
	fx := newFixture()
	fx.desktop.Applied = true
	s := newSetup(t, fx, nil)
	warning, ok := findNode(s.ctl.View().Root, "mode.desktop-warning")
	if !ok || warning.Enabled || !strings.Contains(warning.Label, "applied") || !strings.Contains(warning.Label, "resumes") {
		t.Fatalf("applied warning = %+v", warning)
	}

	unknown := newSetup(t, newFixture(), nil)
	unknown.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
	if err := unknown.ctl.RefreshDesktop(context.Background()); err == nil {
		t.Fatal("expected a desktop read failure")
	}
	warning, ok = findNode(unknown.ctl.View().Root, "mode.desktop-warning")
	if !ok || warning.Enabled || !strings.Contains(warning.Label, "unknown") {
		t.Fatalf("unknown warning = %+v", warning)
	}

	if _, ok := findNode(s.ctl.View().Root, "advanced.restart.warning"); !ok {
		t.Fatal("Advanced -> Restart engine has no desktop-cut warning")
	}

	stopped := newFixture()
	stopped.status["mode"] = string(contract.ModeStopped)
	stopped.status["listeners_open"] = false
	stopped.status["resume_requires_restart"] = true
	stopped.desktop.Applied = true
	stoppedSetup := newSetup(t, stopped, nil)
	for _, key := range []string{"restart.resume", "restart.resume.warning"} {
		if _, ok := findNode(stoppedSetup.ctl.View().Root, key); !ok {
			t.Fatalf("%q missing in the stopped menu", key)
		}
	}

	if _, ok := findNode(newSetup(t, newFixture(), nil).ctl.View().Root, "mode.desktop-warning"); ok {
		t.Fatal("warning rendered while the desktop proxy is not applied")
	}
}

func TestStopAndRestartDoNotDependOnDesktopStatus(t *testing.T) {
	setup := func() *controllerSetup {
		s := newSetup(t, newFixture(), nil)
		s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
		if err := s.ctl.RefreshDesktop(context.Background()); err == nil {
			t.Fatal("expected a desktop read failure")
		}
		return s
	}

	stop := setup()
	if res, err := stop.ctl.Trigger(context.Background(), ActionStopListening, Target{}); err != nil || res.Status != ResultCompleted {
		t.Fatalf("stop with unknown desktop = %+v, %v", res, err)
	}
	if got := stop.caller.countOp(control.OpMode); got != 1 {
		t.Fatalf("stop requests = %d, want 1", got)
	}

	restart := setup()
	if res, err := restart.ctl.Trigger(context.Background(), ActionRestartEngine, Target{}); err != nil || res.Status != ResultJobStarted {
		t.Fatalf("restart with unknown desktop = %+v, %v", res, err)
	}
	waitFor(t, "restart request", func() bool {
		_, restarts := restart.life.counts()
		return restarts == 1
	})
}

func TestQuitUsesAFreshDesktopRead(t *testing.T) {
	ctx := context.Background()

	t.Run("not applied", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		res, err := s.ctl.Trigger(ctx, ActionQuit, Target{})
		if err != nil || res.Status != ResultCompleted {
			t.Fatalf("quit = %+v, %v", res, err)
		}
		if quits, _ := s.life.counts(); quits != 1 {
			t.Fatalf("quit requests = %d, want 1", quits)
		}
		if restores := s.caller.countOp(control.OpDesktopRestore); restores != 0 {
			t.Fatalf("plain Quit restored the desktop proxy %d times", restores)
		}
	})

	t.Run("applied after the snapshot", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setDesktop(control.DesktopData{Environment: "GNOME", Supported: true, Applied: true})
		if _, err := s.ctl.Trigger(ctx, ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("plain Quit = %v, want ErrBlocked", err)
		}
		if got := s.ctl.View().LastAction; !strings.Contains(got, "restore or keep") {
			t.Fatalf("last action = %q", got)
		}
		if note, ok := lastNotification(s); !ok || !strings.Contains(note.Body, "restore or keep") {
			t.Fatalf("notification = %+v", note)
		}
		if quits, _ := s.life.counts(); quits != 0 {
			t.Fatalf("quit ran with an applied proxy: %d", quits)
		}
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "quit.now"); n.Visible {
			t.Fatal("plain Quit stayed visible")
		}
		for _, key := range []string{"quit.restore", "quit.keep"} {
			if n, ok := findNode(view.Root, key); !ok || !n.Visible {
				t.Fatalf("%q = %+v", key, n)
			}
		}
		if n, _ := findNode(view.Root, "quit.warning"); !strings.Contains(n.Label, "applied") {
			t.Fatalf("quit warning = %q", n.Label)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
		if _, err := s.ctl.Trigger(ctx, ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("plain Quit = %v, want ErrBlocked", err)
		}
		if got := s.ctl.View().LastAction; !strings.Contains(got, "restore or keep") {
			t.Fatalf("last action = %q", got)
		}
		if note, ok := lastNotification(s); !ok || !strings.Contains(note.Body, "restore or keep") {
			t.Fatalf("notification = %+v", note)
		}
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "quit.now"); n.Visible {
			t.Fatal("plain Quit stayed visible")
		}
		for _, key := range []string{"quit.restore", "quit.keep"} {
			if n, ok := findNode(view.Root, key); !ok || !n.Visible {
				t.Fatalf("%q = %+v", key, n)
			}
		}
		if n, _ := findNode(view.Root, "quit.warning"); !strings.Contains(n.Label, "unknown") {
			t.Fatalf("quit warning = %q", n.Label)
		}
	})

	t.Run("engine unavailable", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setTransport(true)
		if err := s.ctl.RefreshStatus(ctx); err == nil {
			t.Fatal("status unexpectedly succeeded")
		}
		if _, err := s.ctl.Trigger(ctx, ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("plain Quit = %v, want ErrBlocked", err)
		}
		if got := s.ctl.View().LastAction; !strings.Contains(got, "keep (restore is unavailable here)") {
			t.Fatalf("last action = %q", got)
		}
		if note, ok := lastNotification(s); !ok || !strings.Contains(note.Body, "keep (restore is unavailable here)") {
			t.Fatalf("notification = %+v", note)
		}
	})
}

func TestQuitRestoreFailureDoesNotQuit(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	s.caller.setFail(control.OpDesktopRestore, contract.StateLocked, "denied")
	if _, err := s.ctl.Trigger(ctx, ActionQuitRestoreDesktop, Target{}); err != nil {
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

func TestQuitRestoreOutcomeIsTruthful(t *testing.T) {
	cases := []struct {
		name    string
		desktop control.DesktopData
		want    string
	}{
		{
			name: "nothing to restore",
			desktop: control.DesktopData{
				Environment: "GNOME", Supported: true, Applied: true,
				Notice: "no desktop proxy setting was applied by RouteLatch",
			},
			want: "Quit requested; desktop proxy: nothing to restore",
		},
		{
			name: "user-modified setting left",
			desktop: control.DesktopData{
				Environment: "KDE", Supported: true, Applied: true,
				Restored: []string{"https_proxy"}, UserModified: []string{"http_proxy"},
			},
			want: "Quit requested; desktop proxy restored; 1 user-modified setting left unchanged",
		},
		{
			name: "nothing was restorable",
			desktop: control.DesktopData{
				Environment: "KDE", Supported: true, Applied: true,
				UserModified: []string{"http_proxy"},
			},
			want: "Quit requested; desktop proxy: no setting restored; 1 user-modified setting left unchanged",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture()
			fx.desktop = tc.desktop
			s := newSetup(t, fx, nil)
			ctx := context.Background()
			if _, err := s.ctl.Trigger(ctx, ActionQuitRestoreDesktop, Target{}); err != nil {
				t.Fatal(err)
			}
			waitForJobEnd(t, s)
			if quits, _ := s.life.counts(); quits != 1 {
				t.Fatalf("quit requests = %d, want 1 after a successful restore call", quits)
			}
			if got := s.ctl.View().LastAction; got != tc.want {
				t.Fatalf("last action = %q, want %q", got, tc.want)
			}
			note, ok := lastNotification(s)
			if !ok || note.Body != tc.want {
				t.Fatalf("notification = %+v, want %q", note, tc.want)
			}
		})
	}
}

func TestQuitKeepDesktopQuitsWithoutRestore(t *testing.T) {
	fx := newFixture()
	fx.desktop.Applied = true
	s := newSetup(t, fx, nil)
	ctx := context.Background()
	res, err := s.ctl.Trigger(ctx, ActionQuitKeepDesktop, Target{})
	if err != nil || res.Status != ResultCompleted {
		t.Fatalf("quit and keep = %+v, %v", res, err)
	}
	if quits, _ := s.life.counts(); quits != 1 {
		t.Fatalf("quit requests = %d, want 1", quits)
	}
	if restores := s.caller.countOp(control.OpDesktopRestore); restores != 0 {
		t.Fatalf("quit and keep restored the desktop proxy %d times", restores)
	}
	if got := s.ctl.View().LastAction; got != "Quit requested; desktop proxy kept" {
		t.Fatalf("last action = %q", got)
	}
	notes := s.notifier.all()
	if len(notes) == 0 || notes[len(notes)-1].Body != "Quit requested; desktop proxy kept" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestQuitRestoreDesktopRestoresThenQuits(t *testing.T) {
	fx := newFixture()
	fx.desktop.Applied = true
	fx.desktop.Restored = []string{"http_proxy"}
	s := newSetup(t, fx, nil)
	ctx := context.Background()
	res, err := s.ctl.Trigger(ctx, ActionQuitRestoreDesktop, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("restore and quit = %+v, %v", res, err)
	}
	restoresAtQuit := -1
	s.life.onQuit = func() { restoresAtQuit = s.caller.countOp(control.OpDesktopRestore) }
	waitFor(t, "quit and restore job", func() bool { return s.ctl.View().Job == nil })
	if restoresAtQuit != 1 {
		t.Fatalf("desktop.restore calls at quit = %d, want 1", restoresAtQuit)
	}
	if quits, _ := s.life.counts(); quits != 1 {
		t.Fatalf("quit requests = %d, want 1", quits)
	}
	if got := s.ctl.View().LastAction; got != "Quit requested; desktop proxy restored" {
		t.Fatalf("last action = %q", got)
	}
	notes := s.notifier.all()
	if len(notes) == 0 || notes[len(notes)-1].Body != "Quit requested; desktop proxy restored" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestQuitKeepDesktopOnUnknownStateQuitsWithWarning(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
	ctx := context.Background()
	// Clicking plain Quit triggers the documented fresh desktop read. When it
	// fails, plain Quit is rejected and the explicit keep/restore choice is
	// what the menu then offers.
	if _, err := s.ctl.Trigger(ctx, ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("plain Quit with an unknown desktop state = %v, want ErrBlocked", err)
	}
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "quit.keep"); !n.Enabled || !n.Visible {
		t.Fatalf("quit.keep = %+v, want the explicit choice", n)
	}
	if n, _ := findNode(view.Root, "quit.warning"); !strings.Contains(n.Label, "unknown") {
		t.Fatalf("quit warning = %q", n.Label)
	}
	if _, err := s.ctl.Trigger(ctx, ActionQuitKeepDesktop, Target{}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if quits, _ := s.life.counts(); quits != 1 {
		t.Fatalf("quit requests = %d", quits)
	}
	if restores := s.caller.countOp(control.OpDesktopRestore); restores != 0 {
		t.Fatalf("unknown desktop was restored without the explicit choice: %d", restores)
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
