package trayui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

func TestDoctorJobRendersChecksWarningsAndEnvironment(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "diagnostics.doctor"); n.Label != "Run doctor" || !n.Enabled {
		t.Fatalf("doctor item = %+v", n)
	}
	if n, _ := findNode(view.Root, "diagnostics.warnings"); n.Label != "Warnings (1)" {
		t.Fatalf("warnings = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "diagnostics.warning.0"); n.Label != "desktop proxy is not applied" {
		t.Fatalf("warning = %q", n.Label)
	}

	baseline := s.caller.countOp(control.OpDoctor)
	res, err := s.ctl.Trigger(ctx, ActionDoctor, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("doctor = %+v, %v", res, err)
	}
	waitForJobEnd(t, s)
	if got := s.caller.countOp(control.OpDoctor); got != baseline+1 {
		t.Fatalf("doctor requests = %d, want 1", got-baseline)
	}
	if got := len(s.ctl.Snapshot().Doctor); got != 2 {
		t.Fatalf("doctor checks = %d, want 2", got)
	}
	view = s.ctl.View()
	for key, want := range map[string]string{
		"diagnostics.check.0": "[ok] listeners: both loopback",
		"diagnostics.check.1": "[warn] pool: 20 cooling records",
	} {
		if n, ok := findNode(view.Root, key); !ok || n.Enabled || n.Label != want {
			t.Fatalf("%s = %+v, want %q", key, n, want)
		}
	}
	want := "Doctor finished: 1 warnings, 0 errors"
	if got := view.LastAction; got != want {
		t.Fatalf("last action = %q, want %q", got, want)
	}
	waitFor(t, "doctor notification", func() bool {
		note, ok := lastNotification(s)
		return ok && note.Body == want
	})
	for _, key := range []string{
		"diagnostics.env.HTTP_PROXY", "diagnostics.env.HTTPS_PROXY",
		"diagnostics.env.ALL_PROXY", "diagnostics.env.NO_PROXY",
	} {
		if n, _ := findNode(s.ctl.View().Root, key); n.Enabled || !strings.Contains(n.Label, "=") {
			t.Fatalf("env item %q = %+v", key, n)
		}
	}
}

func TestDoctorFailureAndMaliciousResults(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.caller.setFail(control.OpDoctor, contract.StateLocked, "denied")
		if _, err := s.ctl.Trigger(context.Background(), ActionDoctor, Target{}); err != nil {
			t.Fatal(err)
		}
		waitForJobEnd(t, s)
		if got := s.ctl.View().LastAction; got != "Failed: state_locked" {
			t.Fatalf("last action = %q", got)
		}
	})

	t.Run("malicious detail", func(t *testing.T) {
		fx := newFixture()
		bad := "evil\x1b[31m\u202e\r\n" + strings.Repeat("x", 200)
		fx.doctor = control.DoctorData{Checks: []control.Check{{Name: bad, Status: "warn", Detail: bad}}}
		s := newSetup(t, fx, nil)
		if _, err := s.ctl.Trigger(context.Background(), ActionDoctor, Target{}); err != nil {
			t.Fatal(err)
		}
		waitForJobEnd(t, s)
		for _, label := range allLabels(s.ctl.View().Root) {
			if containsControl(label) {
				t.Fatalf("control sequence survived: %q", label)
			}
			if runeLen(label) > LabelLimit {
				t.Fatalf("doctor label exceeds %d runes: %q", LabelLimit, label)
			}
		}
	})
}

func TestBackupMenuAndJobResult(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "data.backup"); n.Label != "Back up state now" || !n.Enabled {
		t.Fatalf("backup item = %+v", n)
	}
	if n, _ := findNode(view.Root, "data.restore"); n.Label != "Restore backup… (CLI)" || n.Enabled {
		t.Fatalf("restore hint = %+v", n)
	}

	baseline := s.caller.countOp(control.OpBackup)
	res, err := s.ctl.Trigger(ctx, ActionBackup, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("backup = %+v, %v", res, err)
	}
	waitForJobEnd(t, s)
	if got := s.caller.countOp(control.OpBackup); got != baseline+1 {
		t.Fatalf("backup requests = %d, want 1", got-baseline)
	}
	if got := s.ctl.View().LastAction; got != "Backup created" {
		t.Fatalf("last action = %q", got)
	}
	waitFor(t, "backup notification", func() bool {
		note, ok := lastNotification(s)
		return ok && note.Body == "Backup created"
	})

	s.caller.setFail(control.OpBackup, contract.StateLocked, "denied")
	if _, err := s.ctl.Trigger(ctx, ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	waitForJobEnd(t, s)
	if got := s.ctl.View().LastAction; got != "Failed: state_locked" {
		t.Fatalf("failed backup last action = %q", got)
	}
}

func TestSettingsShowsCustomPathsAndAutostartStates(t *testing.T) {
	t.Run("custom data directory", func(t *testing.T) {
		fx := newFixture()
		fx.paths = control.ConfigPathData{
			Config: "/opt/rl-data/config.json", State: "/opt/rl-data/state.json", Socket: "/opt/rl-data/run/control.sock",
		}
		s := newSetup(t, fx, func(o *Options) { o.CustomDataDir = true })
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "settings.paths"); n.Enabled ||
			n.Label != "State /opt/rl-data/state.json · config /opt/rl-data/config.json" {
			t.Fatalf("paths = %+v", n)
		}
		if n, _ := findNode(view.Root, "settings.config"); n.Enabled ||
			n.Label != "HTTP 127.0.0.1:18080 · SOCKS 127.0.0.1:18081 · log info" {
			t.Fatalf("config summary = %+v", n)
		}
		if n, _ := findNode(view.Root, "settings.autostart"); n.Enabled || n.Label != "Start at login: Off" {
			t.Fatalf("autostart = %+v", n)
		}
		if n, _ := findNode(view.Root, "settings.autostart.reason"); !strings.Contains(n.Label, "default data directory") {
			t.Fatalf("reason = %q", n.Label)
		}
	})

	t.Run("service conflict", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.auto.mu.Lock()
		s.auto.status.Conflict = autostart.ConflictService
		s.auto.mu.Unlock()
		if err := s.ctl.RefreshAutostart(context.Background()); err != nil {
			t.Fatal(err)
		}
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "settings.autostart"); n.Enabled || n.Label != "Start at login: Off" {
			t.Fatalf("autostart = %+v", n)
		}
		if n, _ := findNode(view.Root, "settings.autostart.reason"); !strings.Contains(n.Label, "service unit") {
			t.Fatalf("reason = %q", n.Label)
		}
		if _, err := s.ctl.Trigger(context.Background(), ActionAutostartToggle, Target{}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("autostart toggle with a service conflict = %v", err)
		}
	})

	t.Run("foreign entry", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.auto.mu.Lock()
		s.auto.status.Enabled = true
		s.auto.status.Managed = false
		s.auto.mu.Unlock()
		if err := s.ctl.RefreshAutostart(context.Background()); err != nil {
			t.Fatal(err)
		}
		view := s.ctl.View()
		n, _ := findNode(view.Root, "settings.autostart")
		if n.Enabled || n.Label != "Start at login: On" || !n.Checked {
			t.Fatalf("autostart = %+v", n)
		}
		if reason, _ := findNode(view.Root, "settings.autostart.reason"); !strings.Contains(reason.Label, "not managed") {
			t.Fatalf("reason = %q", reason.Label)
		}
	})

	t.Run("status error and missing manager", func(t *testing.T) {
		s := newSetup(t, newFixture(), nil)
		s.auto.mu.Lock()
		s.auto.err = errors.New("boom")
		s.auto.mu.Unlock()
		if err := s.ctl.RefreshAutostart(context.Background()); err == nil {
			t.Fatal("expected an autostart status error")
		}
		view := s.ctl.View()
		if n, _ := findNode(view.Root, "settings.autostart"); n.Enabled || n.Label != "Start at login: unavailable" {
			t.Fatalf("autostart = %+v", n)
		}
		if reason, _ := findNode(view.Root, "settings.autostart.reason"); !strings.Contains(reason.Label, "unavailable") {
			t.Fatalf("reason = %q", reason.Label)
		}

		none := newSetup(t, newFixture(), func(o *Options) { o.Autostart = nil })
		view = none.ctl.View()
		if n, _ := findNode(view.Root, "settings.autostart"); n.Enabled {
			t.Fatalf("autostart without a manager = %+v", n)
		}
		if reason, _ := findNode(view.Root, "settings.autostart.reason"); !strings.Contains(reason.Label, "not configured") {
			t.Fatalf("reason = %q", reason.Label)
		}
	})
}

// TestSettingsSurfaceOutcomeD21 keeps D21 at the action boundary: the
// notification and last-action text for desktop, diagnostics, data and
// settings actions never copy a URL, destination, credential or backup path.
func TestSettingsSurfaceOutcomeD21(t *testing.T) {
	const canary = "http://d21-g1-canary.example/share?token=secret"
	fx := newFixture()
	fx.backup = canary
	s := newSetup(t, fx, nil)
	ctx := context.Background()

	var texts []string
	record := func() {
		view := s.ctl.View()
		texts = append(texts, view.LastAction)
	}
	runJob := func(action Action, confirm bool) {
		t.Helper()
		res, err := s.ctl.Trigger(ctx, action, Target{})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		texts = append(texts, res.Message)
		if confirm {
			if _, err := s.ctl.Confirm(ctx); err != nil {
				t.Fatalf("%s confirm: %v", action, err)
			}
		}
		waitForJobEnd(t, s)
		record()
	}

	if _, err := s.ctl.Trigger(ctx, ActionDesktopRefresh, Target{}); err != nil {
		t.Fatal(err)
	}
	record()
	s.caller.setDesktop(control.DesktopData{
		Environment: "KDE", Supported: true, Applied: true,
		UserModified: []string{"http_proxy"},
	})
	runJob(ActionDesktopSet, true)
	s.caller.setDesktop(control.DesktopData{
		Environment: "KDE", Supported: true, Applied: true,
		UserModified: []string{"http_proxy", canary},
	})
	runJob(ActionDesktopRestore, true)
	runJob(ActionBackup, false)
	runJob(ActionDoctor, false)
	runJob(ActionAutostartToggle, false)
	if _, err := s.ctl.Trigger(ctx, ActionShowLogPath, Target{}); err != nil {
		t.Fatal(err)
	}
	record()

	for _, note := range s.notifier.all() {
		texts = append(texts, note.Title, note.Body)
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"d21-g1-canary", "http://", "https://", "token=", "secret", "share?"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("outcome text leaked %q: %q", forbidden, text)
			}
		}
	}
	// The user-modified setting names remain a live in-memory view only.
	if got := s.ctl.Snapshot().Desktop.UserModified; len(got) != 2 {
		t.Fatalf("live user-modified view = %v", got)
	}
	for _, label := range allLabels(s.ctl.View().Root) {
		if strings.Contains(strings.ToLower(label), "d21-g1-canary") {
			t.Fatalf("label leaked the canary: %q", label)
		}
		if containsControl(label) || runeLen(label) > LabelLimit {
			t.Fatalf("unsafe label: %q", label)
		}
	}
}
