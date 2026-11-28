package trayui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

func waitForJobEnd(t *testing.T, s *controllerSetup) {
	t.Helper()
	waitFor(t, "job end", func() bool { return s.ctl.View().Job == nil })
}

func lastNotification(s *controllerSetup) (Notification, bool) {
	notes := s.notifier.all()
	if len(notes) == 0 {
		return Notification{}, false
	}
	return notes[len(notes)-1], true
}

func TestDesktopSetAndRestoreRunOnClickAndSendOneRequest(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()

	if set, _ := findNode(view.Root, "desktop.set"); set.Label != "Set/Re-apply desktop proxy" || !set.Enabled {
		t.Fatalf("set item = %+v", set)
	}
	if restore, _ := findNode(view.Root, "desktop.restore"); restore.Label != "Restore previous desktop proxy" || !restore.Enabled {
		t.Fatalf("restore item = %+v", restore)
	}

	baseline := s.caller.countOp(control.OpDesktopSet)
	res, err := s.ctl.Trigger(ctx, ActionDesktopSet, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("set trigger = %+v, %v", res, err)
	}
	waitForJobEnd(t, s)
	if got := s.caller.countOp(control.OpDesktopSet); got != baseline+1 {
		t.Fatalf("set requests = %d, want 1", got-baseline)
	}
	if got := s.ctl.Snapshot().Desktop; !got.Applied || !got.Known {
		t.Fatalf("desktop after set = %+v", got)
	}
	if got := s.ctl.View().LastAction; got != "Desktop proxy set" {
		t.Fatalf("last action = %q", got)
	}
	waitFor(t, "set notification", func() bool {
		note, ok := lastNotification(s)
		return ok && note.Body == "Desktop proxy set"
	})

	// A restore may leave a setting the user changed untouched; the outcome
	// must say so instead of claiming a full restore.
	s.caller.setDesktop(control.DesktopData{
		Environment: "KDE", Supported: true, Applied: true,
		Restored: []string{"https_proxy"}, UserModified: []string{"http_proxy"},
	})
	baseline = s.caller.countOp(control.OpDesktopRestore)
	res, err = s.ctl.Trigger(ctx, ActionDesktopRestore, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("restore trigger = %+v, %v", res, err)
	}
	waitForJobEnd(t, s)
	if got := s.caller.countOp(control.OpDesktopRestore); got != baseline+1 {
		t.Fatalf("restore requests = %d, want 1", got-baseline)
	}
	if got := s.ctl.Snapshot().Desktop; got.Applied {
		t.Fatalf("desktop after restore = %+v", got)
	}
	want := "Desktop proxy restored; 1 user-modified setting left unchanged"
	if got := s.ctl.View().LastAction; got != want {
		t.Fatalf("last action = %q, want %q", got, want)
	}
	waitFor(t, "restore notification", func() bool {
		note, ok := lastNotification(s)
		return ok && note.Body == want
	})
}

func TestDesktopClearIsAvailableWithoutAMarkerOrWhileStopped(t *testing.T) {
	ctx := context.Background()
	const clearLabel = "Clear desktop proxy (disable system proxy)"

	t.Run("no recorded marker", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		view := s.ctl.View()
		node, ok := findNode(view.Root, "desktop.clear")
		if !ok || !node.Enabled || node.Action != ActionDesktopClear || node.Label != clearLabel {
			t.Fatalf("clear item = %+v, want enabled with a sensible label", node)
		}
		if n, ok := findNode(view.Root, "desktop.clear.reason"); ok {
			t.Fatalf("clear reason shown for a supported desktop: %+v", n)
		}
		baseline := s.caller.countOp(control.OpDesktopClear)
		res, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{})
		if err != nil || res.Status != ResultJobStarted {
			t.Fatalf("clear without a marker = %+v, %v; want a started job", res, err)
		}
		waitForJobEnd(t, s)
		if got := s.caller.countOp(control.OpDesktopClear); got != baseline+1 {
			t.Fatalf("desktop.clear requests = %d, want 1", got-baseline)
		}
		if got := s.ctl.View().LastAction; got != "Desktop proxy cleared" {
			t.Fatalf("last action = %q", got)
		}
		desktop := s.ctl.Snapshot().Desktop
		if !desktop.Cleared || !desktop.Applied {
			t.Fatalf("desktop after a marker-less clear = %+v", desktop)
		}
	})

	t.Run("clear while stopped", func(t *testing.T) {
		fx := newFixture()
		fx.status["mode"] = string(contract.ModeStopped)
		fx.status["listeners_open"] = false
		fx.status["resume_requires_restart"] = false
		s := newSetup(t, fx, nil)
		node, ok := findNode(s.ctl.View().Root, "desktop.clear")
		if !ok || !node.Enabled {
			t.Fatalf("clear item while stopped = %+v, want enabled", node)
		}
		if _, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{}); err != nil {
			t.Fatalf("clear while stopped = %v", err)
		}
		waitForJobEnd(t, s)
		if got := s.caller.countOp(control.OpDesktopClear); got != 1 {
			t.Fatalf("desktop.clear requests while stopped = %d, want 1", got)
		}
	})

	t.Run("clears an applied proxy and allows plain quit", func(t *testing.T) {
		fx := newFixture()
		fx.desktop = control.DesktopData{Environment: "GNOME", Supported: true, Applied: true, PreviousSet: true}
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "quit.now"); n.Visible {
			t.Fatal("plain Quit visible while the desktop proxy is applied")
		}
		node, ok := findNode(view.Root, "desktop.clear")
		if !ok || !node.Enabled {
			t.Fatalf("clear item = %+v, want enabled", node)
		}

		res, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{})
		if err != nil || res.Status != ResultJobStarted {
			t.Fatalf("clear trigger = %+v, %v", res, err)
		}
		waitForJobEnd(t, s)
		if got := s.caller.countOp(control.OpDesktopClear); got != 1 {
			t.Fatalf("desktop.clear requests = %d, want 1", got)
		}
		if got := s.ctl.View().LastAction; got != "Desktop proxy cleared" {
			t.Fatalf("last action = %q", got)
		}
		desktop := s.ctl.Snapshot().Desktop
		if !desktop.Cleared || !desktop.Applied || !desktop.PreviousSet {
			t.Fatalf("desktop after clear = %+v", desktop)
		}
		view = s.ctl.View()
		if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "cleared (disabled)") {
			t.Fatalf("desktop status = %q", n.Label)
		}
		// The previous values are still recorded for a later restore, but the
		// disabled proxy no longer forces the Quit choice.
		if n, _ := findNode(view.Root, "quit.now"); !n.Visible || !n.Enabled {
			t.Fatalf("plain Quit after clear = %+v", n)
		}
		if _, ok := findNode(view.Root, "quit.keep"); ok {
			t.Fatal("Quit keep choice survived a cleared desktop proxy")
		}
		if _, ok := findNode(view.Root, "advanced.restart.warning"); ok {
			t.Fatal("restart cut-warning survived a cleared desktop proxy")
		}
		// Clear stays selectable after a clear; the service turns it into an
		// idempotent already-disabled notice.
		if n, _ := findNode(view.Root, "desktop.clear"); !n.Enabled {
			t.Fatalf("clear item after clear = %+v, want enabled", n)
		}
		if n, ok := findNode(view.Root, "desktop.clear.reason"); ok {
			t.Fatalf("clear reason after clear = %+v, want none", n)
		}
	})

	t.Run("already cleared", func(t *testing.T) {
		fx := newFixture()
		fx.desktop = control.DesktopData{Environment: "KDE", Supported: true, Applied: true, Cleared: true, PreviousSet: true}
		fx.clearResult = &control.DesktopData{
			Environment: "KDE", Supported: true, Applied: true, Cleared: true, PreviousSet: true,
			Notice: "the system proxy is already disabled",
		}
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		if n, ok := findNode(view.Root, "desktop.clear"); !ok || !n.Enabled {
			t.Fatalf("clear item for an already-cleared proxy = %+v, want enabled", n)
		}
		if n, ok := findNode(view.Root, "desktop.clear.reason"); ok {
			t.Fatalf("clear reason for an already-cleared proxy = %+v, want none", n)
		}
		if _, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{}); err != nil {
			t.Fatalf("clear of an already-cleared proxy = %v", err)
		}
		waitForJobEnd(t, s)
		if got := s.ctl.View().LastAction; got != "Desktop proxy: the system proxy is already disabled" {
			t.Fatalf("last action = %q, want the already-disabled notice", got)
		}
		waitFor(t, "already-disabled notification", func() bool {
			note, ok := lastNotification(s)
			return ok && strings.Contains(note.Body, "already disabled")
		})
	})

	t.Run("unsupported desktop", func(t *testing.T) {
		fx := newFixture()
		fx.desktop = control.DesktopData{Environment: "sway", Supported: false, PreviousSet: true}
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		if n, ok := findNode(view.Root, "desktop.clear"); !ok || n.Enabled {
			t.Fatalf("clear item = %+v, want disabled", n)
		}
		if n, ok := findNode(view.Root, "desktop.clear.reason"); !ok || !strings.Contains(n.Label, "not supported") {
			t.Fatalf("clear reason = %+v, want an unsupported explanation", n)
		}
		if _, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("clear in an unsupported environment = %v", err)
		}
	})

	t.Run("unknown status", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
		if err := s.ctl.RefreshDesktop(ctx); err == nil {
			t.Fatal("expected a desktop read failure")
		}
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "desktop.clear"); n.Enabled {
			t.Fatal("clear enabled with an unknown desktop state")
		}
		if _, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("clear with an unknown state = %v", err)
		}
	})

	t.Run("user-modified setting is reported", func(t *testing.T) {
		fx := newFixture()
		fx.desktop = control.DesktopData{Environment: "GNOME", Supported: true, Applied: true, PreviousSet: true}
		fx.clearResult = &control.DesktopData{
			Environment: "GNOME", Supported: true, Applied: true, PreviousSet: true,
			UserModified: []string{"org.gnome.system.proxy mode"},
		}
		s := newSetup(t, fx, nil)
		if _, err := s.ctl.Trigger(ctx, ActionDesktopClear, Target{}); err != nil {
			t.Fatal(err)
		}
		waitForJobEnd(t, s)
		if got := s.ctl.View().LastAction; got != "Desktop proxy not cleared; 1 user-modified setting left unchanged" {
			t.Fatalf("last action = %q", got)
		}
		if desktop := s.ctl.Snapshot().Desktop; desktop.Cleared {
			t.Fatalf("desktop claims cleared after a user-modified no-op: %+v", desktop)
		}
	})
}

func TestDesktopRestoreNoOpReportsTheServiceNotice(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	s.caller.setDesktop(control.DesktopData{
		Environment: "GNOME", Supported: true,
		Notice: "no desktop proxy setting was applied by RouteLatch",
	})
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionDesktopRestore, Target{}); err != nil {
		t.Fatal(err)
	}
	waitForJobEnd(t, s)
	if got := s.ctl.View().LastAction; got != "Desktop proxy: nothing to restore" {
		t.Fatalf("last action = %q", got)
	}
}

func TestDesktopAvailabilityMatchesContract(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		fx := newFixture()
		fx.status["mode"] = string(contract.ModeStopped)
		fx.status["listeners_open"] = false
		fx.status["resume_requires_restart"] = false
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "desktop.set"); n.Enabled {
			t.Fatal("Set is enabled in stopped mode")
		}
		for _, key := range []string{"desktop.restore", "desktop.clear", "desktop.refresh"} {
			if n, _ := findNode(view.Root, key); !n.Enabled {
				t.Fatalf("%q is disabled in stopped mode", key)
			}
		}
	})

	t.Run("listeners closed", func(t *testing.T) {
		fx := newFixture()
		fx.status["listeners_open"] = false
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "desktop.set"); n.Enabled {
			t.Fatal("Set is enabled with no listener to point at")
		}
		if n, _ := findNode(view.Root, "desktop.restore"); !n.Enabled {
			t.Fatal("Restore is disabled without listeners")
		}
		baseline := s.caller.countOp(control.OpDesktopSet)
		if _, err := s.ctl.Trigger(context.Background(), ActionDesktopSet, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("set with closed listeners = %v", err)
		}
		if got := s.caller.countOp(control.OpDesktopSet); got != baseline {
			t.Fatal("a blocked Set reached the socket")
		}
	})

	t.Run("unsupported desktop", func(t *testing.T) {
		fx := newFixture()
		fx.desktop = control.DesktopData{Environment: "sway", Supported: false}
		s := newSetup(t, fx, nil)
		view := s.ctl.View()
		for _, key := range []string{"desktop.set", "desktop.restore", "desktop.clear", "desktop.refresh"} {
			n, ok := findNode(view.Root, key)
			if !ok || n.Enabled {
				t.Fatalf("%q = %+v, want disabled", key, n)
			}
		}
		if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "unsupported") {
			t.Fatalf("status = %q", n.Label)
		}
		if n, _ := findNode(view.Root, "desktop.reason"); !strings.Contains(n.Label, "not supported") {
			t.Fatalf("reason = %q", n.Label)
		}
		baseline := s.caller.countOp(control.OpDesktopStatus)
		for _, action := range []Action{ActionDesktopSet, ActionDesktopRestore, ActionDesktopRefresh, ActionDesktopClear} {
			if _, err := s.ctl.Trigger(context.Background(), action, Target{}); !errors.Is(err, ErrBlocked) {
				t.Fatalf("%s in an unsupported environment = %v", action, err)
			}
		}
		if got := s.caller.countOp(control.OpDesktopStatus); got != baseline {
			t.Fatal("a desktop action reached the socket in an unsupported environment")
		}
	})

	t.Run("failed read keeps refresh available", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
		if err := s.ctl.RefreshDesktop(context.Background()); err == nil {
			t.Fatal("expected a read failure")
		}
		view := s.ctl.View()
		for _, key := range []string{"desktop.set", "desktop.restore", "desktop.clear"} {
			if n, _ := findNode(view.Root, key); n.Enabled {
				t.Fatalf("%q is enabled with an unknown desktop state", key)
			}
		}
		if n, _ := findNode(view.Root, "desktop.refresh"); !n.Enabled {
			t.Fatal("Refresh must stay available to recover an unknown state")
		}
	})
}

func TestDesktopConflictAndNoticeAreVisible(t *testing.T) {
	notice := "the desktop setting changed since RouteLatch applied it"
	fx := newFixture()
	fx.desktop = control.DesktopData{
		Environment: "KDE", Supported: true, Applied: true, PreviousSet: true,
		Conflict: []string{"http_proxy", "https_proxy"}, Notice: notice,
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "conflict: http_proxy, https_proxy") {
		t.Fatalf("status = %q", n.Label)
	}
	n, ok := findNode(view.Root, "desktop.notice")
	if !ok || n.Enabled || !strings.Contains(n.Label, "changed since RouteLatch applied it") {
		t.Fatalf("notice = %+v", n)
	}
	if containsControl(n.Label) || runeLen(n.Label) > LabelLimit {
		t.Fatalf("notice label is unsafe: %q", n.Label)
	}
	// A conflict is re-applied explicitly, so Set and Restore stay available.
	for _, key := range []string{"desktop.set", "desktop.restore"} {
		if n, _ := findNode(view.Root, key); !n.Enabled {
			t.Fatalf("%q disabled on a conflict", key)
		}
	}
	if _, err := s.ctl.Trigger(context.Background(), ActionDesktopSet, Target{}); err != nil {
		t.Fatalf("set on conflict: %v", err)
	}
	waitForJobEnd(t, s)
}

func TestUnsupportedDesktopNoticeIsNotShownAsAReason(t *testing.T) {
	fx := newFixture()
	fx.desktop = control.DesktopData{
		Environment: "sway", Supported: false,
		Notice: "the desktop setting changed since RouteLatch applied it",
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if _, ok := findNode(view.Root, "desktop.notice"); ok {
		t.Fatal("an unsupported environment rendered the applied-setting notice")
	}
	if n, _ := findNode(view.Root, "desktop.reason"); !strings.Contains(n.Label, "not supported") {
		t.Fatalf("reason = %q", n.Label)
	}
}
