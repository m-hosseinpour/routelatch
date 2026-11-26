package trayui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

func TestInProcessControlServerRoundTrip(t *testing.T) {
	fx := newFixture()
	caller := newFixtureCaller(fx)
	client := startServer(t, caller)
	ctl, err := New(Options{Caller: client, Paths: Paths{Log: "/data/log/routelatch.log"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.RefreshAll(context.Background()); err != nil {
		t.Fatalf("RefreshAll over the real socket: %v", err)
	}
	snap := ctl.Snapshot()
	if snap.Engine != EngineReady {
		t.Fatalf("engine = %s", snap.Engine)
	}
	if len(snap.Rules) != 117 {
		t.Fatalf("rules = %d, want 117", len(snap.Rules))
	}
	if len(snap.Pool.Rows) != 176 {
		t.Fatalf("pool = %d, want 176", len(snap.Pool.Rows))
	}
	if snap.Pool.Counts.Usable != 140 {
		t.Fatalf("usable = %d, want 140", snap.Pool.Counts.Usable)
	}
	if !snap.Desktop.Known || !snap.Desktop.Supported || snap.Desktop.Applied {
		t.Fatalf("desktop = %+v", snap.Desktop)
	}
	if snap.Config.HTTPListen != "127.0.0.1:18080" {
		t.Fatalf("config = %+v", snap.Config)
	}
	if snap.Paths.Config != "/data/config.json" {
		t.Fatalf("paths = %+v", snap.Paths)
	}

	// A real OK=false envelope from the server is a failure, not an empty
	// success.
	caller.setFail(control.OpMode, contract.Timeout, "https://secret.example/path")
	_, err = ctl.Trigger(context.Background(), ActionModeTargeted, Target{})
	var envErr *EnvelopeError
	if !errors.As(err, &envErr) {
		t.Fatalf("Trigger error = %v, want EnvelopeError", err)
	}
	if envErr.Kind != contract.Timeout {
		t.Fatalf("kind = %s, want timeout", envErr.Kind)
	}
	if got := ctl.View().LastAction; got != "Failed: timeout" {
		t.Fatalf("last action = %q", got)
	}
}

func TestTransportFailureMakesEngineUnavailableAndRecovers(t *testing.T) {
	fx := newFixture()
	s := newSetup(t, fx, nil)
	if s.ctl.View().Engine != EngineReady {
		t.Fatal("fixture did not load ready")
	}
	s.caller.setTransport(true)
	err := s.ctl.RefreshStatus(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RefreshStatus error = %v, want ErrUnavailable", err)
	}
	snap := s.ctl.Snapshot()
	if snap.Engine != EngineUnavailable {
		t.Fatalf("engine = %s, want unavailable", snap.Engine)
	}
	if len(snap.Rules) != 0 || len(snap.Pool.Rows) != 0 || len(snap.Proposals) != 0 {
		t.Fatalf("stale dynamic lists survived: %d/%d/%d", len(snap.Rules), len(snap.Pool.Rows), len(snap.Proposals))
	}
	view := s.ctl.View()
	status, ok := findNode(view.Root, "status")
	if !ok || status.Label != "Engine unavailable; retrying…" {
		t.Fatalf("status node = %+v", status)
	}
	mode, _ := findNode(view.Root, "mode.targeted")
	if mode.Enabled {
		t.Fatal("mode action stayed enabled while unavailable")
	}
	quit, _ := findNode(view.Root, "quit.now")
	if !quit.Enabled {
		t.Fatal("Quit must stay available while the engine is unavailable")
	}

	s.caller.setTransport(false)
	if err := s.ctl.RefreshAll(context.Background()); err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	if s.ctl.Snapshot().Engine != EngineReady || len(s.ctl.Snapshot().Rules) != 117 {
		t.Fatal("did not recover the ready snapshot")
	}
}

func TestResumeRequiresRestartIsDecodedIncludingUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     any
		present bool
		want    bool
	}{
		{name: "missing is unknown", present: false},
		{name: "false", set: false, present: true, want: false},
		{name: "true", set: true, present: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture()
			if tc.present {
				fx.status["resume_requires_restart"] = tc.set
			}
			s := newSetup(t, fx, nil)
			got := s.ctl.Snapshot().Status.ResumeRequiresRestart
			if !tc.present {
				if got != nil {
					t.Fatalf("pointer = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("pointer = %v, want %t", got, tc.want)
			}
		})
	}
}

func TestFixtureLoaderKeepsFullPoolAndRules(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	snap := s.ctl.Snapshot()
	if len(snap.Rules) != 117 {
		t.Fatalf("rules = %d", len(snap.Rules))
	}
	if len(snap.Pool.Rows) != 176 {
		t.Fatalf("pool rows = %d", len(snap.Pool.Rows))
	}
	if snap.Pool.Counts.Total != 176 || snap.Pool.Counts.Healthy != 100 ||
		snap.Pool.Counts.Unknown != 40 || snap.Pool.Counts.Cooling != 20 ||
		snap.Pool.Counts.Unsupported != 10 || snap.Pool.Counts.Invalid != 6 {
		t.Fatalf("counts = %+v", snap.Pool.Counts)
	}
	if len(snap.Proposals) != 5 {
		t.Fatalf("proposals = %d", len(snap.Proposals))
	}
	if !snap.Env.Available || snap.Env.HTTPProxy == "" {
		t.Fatalf("env = %+v", snap.Env)
	}
	if snap.Paths.Log != "/data/log/routelatch.log" {
		t.Fatalf("log path = %q", snap.Paths.Log)
	}
}

func TestErrorEnvelopeIsNotSuccess(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	s.caller.setFail(control.OpMode, contract.StateLocked, "https://secret.example/destination")
	before := s.caller.countOp(control.OpMode)
	res, err := s.ctl.Trigger(context.Background(), ActionModeTargeted, Target{})
	if err == nil {
		t.Fatalf("Trigger succeeded with result %+v", res)
	}
	var envErr *EnvelopeError
	if !errors.As(err, &envErr) || envErr.Kind != contract.StateLocked {
		t.Fatalf("error = %v", err)
	}
	if s.caller.countOp(control.OpMode) != before+1 {
		t.Fatal("expected exactly one management call")
	}
	if got := s.ctl.View().LastAction; got != "Failed: state_locked" {
		t.Fatalf("last action = %q", got)
	}
	for _, n := range s.notifier.all() {
		if strings.Contains(strings.ToLower(n.Body), "secret") || strings.Contains(n.Body, "http") {
			t.Fatalf("notification leaked caller text: %q", n.Body)
		}
	}
}

func TestStatusAndListPollsAreSeparate(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	statusBefore := s.caller.countOp(control.OpStatus)
	rulesBefore := s.caller.countOp(control.OpRulesList)
	if err := s.ctl.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.caller.countOp(control.OpStatus) != statusBefore+1 {
		t.Fatal("status poll did not send status")
	}
	if s.caller.countOp(control.OpRulesList) != rulesBefore {
		t.Fatal("two-second status poll fetched the list")
	}
	if err := s.ctl.RefreshLists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.caller.countOp(control.OpRulesList) != rulesBefore+1 {
		t.Fatal("list poll did not send rules.list")
	}
	if s.caller.countOp(control.OpStatus) != statusBefore+1 {
		t.Fatal("list poll fetched status again")
	}
	if len(s.ctl.Snapshot().Rules) != 117 {
		t.Fatal("list poll lost the fixture rows")
	}
}

func TestRuleRemovalNotificationHasNoDestination(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: "r-000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	notes := s.notifier.all()
	if len(notes) != 1 || notes[0].Body != "Rule removed (1 connections closed)" {
		t.Fatalf("notifications = %+v", notes)
	}
	if strings.Contains(notes[0].Body, "rule-000.example") || strings.Contains(notes[0].Body, "http") {
		t.Fatalf("notification leaked a destination: %q", notes[0].Body)
	}
}

func TestInitialMenuIsMinimalAndDisabled(t *testing.T) {
	caller := newFixtureCaller(newFixture())
	ctl, err := New(Options{Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	view := ctl.View()
	if view.Loaded {
		t.Fatal("new controller claims a loaded snapshot")
	}
	if got := rootKeys(view.Root); !reflect.DeepEqual(got, []string{"status", "quit"}) {
		t.Fatalf("initial root = %v", got)
	}
	if n, _ := findNode(view.Root, "status"); n.Label != "Loading…" {
		t.Fatalf("status = %q", n.Label)
	}
	// §7.3 requires a minimal Loading… menu with disabled actions. No Quit
	// variant may run before the first snapshot, and the desktop state is not
	// read yet, so no restore/keep choice is offered.
	if n, _ := findNode(view.Root, "quit.now"); !n.Visible || n.Enabled {
		t.Fatalf("Loading Quit = %+v, want visible and disabled", n)
	}
	for _, key := range []string{"quit.restore", "quit.keep", "quit.warning"} {
		if n, ok := findNode(view.Root, key); ok {
			t.Fatalf("%q = %+v, want no action before the first snapshot", key, n)
		}
	}
	if _, err := ctl.Trigger(context.Background(), ActionQuitKeepDesktop, Target{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Loading quit-and-keep = %v, want ErrUnavailable", err)
	}
}

func TestSanitizeBoundaries(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"\x1b[31mred\x1b[0m\r\nnext\tline", LabelLimit, "red next line"},
		{"a\u202eb\u2066c\u061cd", LabelLimit, "abcd"},
		{"\x9b31mhidden", LabelLimit, "hidden"},
		{long, LabelLimit, strings.Repeat("a", LabelLimit-1) + "…"},
		{long, HeaderLimit, strings.Repeat("a", HeaderLimit-1) + "…"},
		{"", LabelLimit, ""},
	}
	for _, tc := range cases {
		got := sanitizeLimit(tc.in, tc.max)
		if got != tc.want {
			t.Fatalf("sanitizeLimit(%q,%d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if containsControl(got) {
			t.Fatalf("sanitized value still has controls: %q", got)
		}
	}
}
