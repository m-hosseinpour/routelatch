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

func TestDesktopSetAndRestoreUseConfirmationAndOneRequest(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()

	if set, _ := findNode(view.Root, "desktop.set"); set.Label != "Set/Re-apply desktop proxy…" || !set.Enabled {
		t.Fatalf("set item = %+v", set)
	}
	if restore, _ := findNode(view.Root, "desktop.restore"); restore.Label != "Restore previous desktop proxy…" || !restore.Enabled {
		t.Fatalf("restore item = %+v", restore)
	}

	baseline := s.caller.countOp(control.OpDesktopSet)
	res, err := s.ctl.Trigger(ctx, ActionDesktopSet, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("set trigger = %+v, %v", res, err)
	}
	if p := s.ctl.View().Pending; p == nil || p.Action != ActionDesktopSet || p.Label != "Set desktop proxy" {
		t.Fatalf("set pending = %+v", p)
	}
	if got := s.caller.countOp(control.OpDesktopSet); got != baseline {
		t.Fatalf("set reached the socket before confirmation: %d", got-baseline)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("set confirm: %v", err)
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
		UserModified: []string{"http_proxy"},
	})
	baseline = s.caller.countOp(control.OpDesktopRestore)
	res, err = s.ctl.Trigger(ctx, ActionDesktopRestore, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("restore trigger = %+v, %v", res, err)
	}
	if p := s.ctl.View().Pending; p == nil || p.Action != ActionDesktopRestore || p.Label != "Restore previous desktop proxy" {
		t.Fatalf("restore pending = %+v", p)
	}
	if got := s.caller.countOp(control.OpDesktopRestore); got != baseline {
		t.Fatalf("restore reached the socket before confirmation: %d", got-baseline)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("restore confirm: %v", err)
	}
	waitForJobEnd(t, s)
	if got := s.caller.countOp(control.OpDesktopRestore); got != baseline+1 {
		t.Fatalf("restore requests = %d, want 1", got-baseline)
	}
	if got := s.ctl.Snapshot().Desktop; got.Applied {
		t.Fatalf("desktop after restore = %+v", got)
	}
	want := "Desktop proxy restored; 1 user-modified settings left unchanged"
	if got := s.ctl.View().LastAction; got != want {
		t.Fatalf("last action = %q, want %q", got, want)
	}
	waitFor(t, "restore notification", func() bool {
		note, ok := lastNotification(s)
		return ok && note.Body == want
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
	if _, err := s.ctl.Confirm(ctx); err != nil {
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
		for _, key := range []string{"desktop.restore", "desktop.refresh"} {
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
		for _, key := range []string{"desktop.set", "desktop.restore", "desktop.refresh"} {
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
		for _, action := range []Action{ActionDesktopSet, ActionDesktopRestore, ActionDesktopRefresh} {
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
		for _, key := range []string{"desktop.set", "desktop.restore"} {
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
	if p := s.ctl.View().Pending; p == nil || p.Label != "Set desktop proxy" {
		t.Fatalf("pending = %+v", p)
	}
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
