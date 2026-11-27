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

func tunFixture() contract.TunStatus {
	return contract.TunStatus{
		Desired: false, State: contract.TunOff, DNS: contract.TunDNSDirect,
		UDP: contract.TunUDPBlocked, Helper: contract.TunHelperInstalled,
	}
}

func TestStatusTunSnapshotAndUnknownPayload(t *testing.T) {
	fx := newFixture()
	fx.tun = contract.TunStatus{
		Desired: true, State: contract.TunStarting, Interface: "rl-tun0",
		Since: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC),
		DNS:   contract.TunDNSDirect, UDP: contract.TunUDPBlocked, IPv6: true,
		Helper: contract.TunHelperInstalled, LastError: contract.TunReasonConflict,
		Detail: "safe fixed detail",
	}
	fx.status["warnings"] = []string{
		"tun_dns_direct", "tun_conflict", "unrelated warning", "tun_future_unknown",
	}
	s := newSetup(t, fx, nil)
	tun := s.ctl.Snapshot().Status.Tun
	if !tun.Known || !tun.Desired || tun.State != contract.TunStarting || tun.Interface != "rl-tun0" {
		t.Fatalf("tun view = %+v", tun)
	}
	if tun.DNS != contract.TunDNSDirect || tun.UDP != contract.TunUDPBlocked || !tun.IPv6 {
		t.Fatalf("tun behavior fields = %+v", tun)
	}
	if tun.Helper != contract.TunHelperInstalled || tun.LastError != contract.TunReasonConflict ||
		tun.Detail != "safe fixed detail" {
		t.Fatalf("tun helper/error = %+v", tun)
	}
	if !tun.WarningsKnown {
		t.Fatal("status warnings were not marked as current")
	}
	if len(tun.Warnings) != 2 || tun.Warnings[0] != contract.TunWarningDNSDirect ||
		tun.Warnings[1] != contract.TunWarningConflict {
		t.Fatalf("tun warnings = %v", tun.Warnings)
	}
	if len(s.ctl.Snapshot().Status.Warnings) != 4 {
		t.Fatalf("general warnings = %v", s.ctl.Snapshot().Status.Warnings)
	}

	fx.noTun = true
	unknown := newSetup(t, fx, nil).ctl.Snapshot().Status.Tun
	if unknown.Known || unknown.State != "" || unknown.Warnings != nil {
		t.Fatalf("missing tun key became %+v, want unknown", unknown)
	}
}

func TestTunFingerprintRefreshesOnlyOnChange(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	first := s.ctl.View()
	builds := s.ctl.builds
	if first.Fingerprint == "" {
		t.Fatal("empty fingerprint")
	}
	if err := s.ctl.RefreshStatus(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged := s.ctl.View()
	if unchanged.Fingerprint != first.Fingerprint || s.ctl.builds != builds {
		t.Fatalf("identical TUN data rebuilt: fp %q -> %q, builds %d -> %d",
			first.Fingerprint, unchanged.Fingerprint, builds, s.ctl.builds)
	}

	on := tunFixture()
	on.Desired, on.State = true, contract.TunOn
	s.caller.setTun(on)
	if err := s.ctl.RefreshStatus(ctx); err != nil {
		t.Fatal(err)
	}
	changed := s.ctl.View()
	if changed.Fingerprint == first.Fingerprint {
		t.Fatal("TUN state change did not change the fingerprint")
	}
	if s.ctl.builds != builds+1 {
		t.Fatalf("TUN change builds = %d, want %d", s.ctl.builds, builds+1)
	}
	if summary, _ := findNode(changed.Root, "coverage.summary"); summary.Label != "Coverage: on · desired on" {
		t.Fatalf("summary after change = %q", summary.Label)
	}

	// A repeated poll with the same TUN object must not rebuild again.
	after := s.ctl.builds
	if err := s.ctl.RefreshStatus(ctx); err != nil {
		t.Fatal(err)
	}
	_ = s.ctl.View()
	if s.ctl.builds != after {
		t.Fatalf("unchanged TUN object rebuilt: %d -> %d", after, s.ctl.builds)
	}
}

func TestCoverageMenuStateHelperWarningsAndActions(t *testing.T) {
	fx := newFixture()
	fx.tun = contract.TunStatus{
		Desired: true, State: contract.TunOn, Interface: "rl-tun0",
		DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked, IPv6: true,
		Helper: contract.TunHelperInstalled, LastError: contract.TunReasonConflict,
		Detail: "http://secret.example/should-not-render",
	}
	fx.status["warnings"] = []string{
		"tun_dns_direct", "tun_udp_blocked", "tun_desktop_proxy_overlap",
		"tun_all_direct", "tun_conflict", "tun_future_unknown", "unrelated",
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()

	if summary, _ := findNode(view.Root, "coverage.summary"); summary.Label != "Coverage: on · desired on" {
		t.Fatalf("summary = %q", summary.Label)
	}
	if helper, _ := findNode(view.Root, "coverage.helper"); helper.Label != "Helper: installed" {
		t.Fatalf("helper = %q", helper.Label)
	}
	if iface, _ := findNode(view.Root, "coverage.interface"); iface.Label != "Interface: rl-tun0" {
		t.Fatalf("interface = %q", iface.Label)
	}
	if errLine, _ := findNode(view.Root, "coverage.error"); errLine.Label != "Error: coverage conflict" {
		t.Fatalf("error = %q", errLine.Label)
	}
	warnings, _ := findNode(view.Root, "coverage.warnings")
	if warnings.Label != "Warnings (5)" {
		t.Fatalf("warning title = %q, want 5 known identifiers", warnings.Label)
	}
	labels := make([]string, 0, len(warnings.Children))
	for _, child := range warnings.Children {
		labels = append(labels, child.Label)
	}
	for _, want := range []string{
		"DNS: plain DNS goes direct, not through the pool",
		"UDP/QUIC: non-DNS UDP is blocked",
		"Desktop proxy: apps using it bypass TUN",
		"All direct: pool is unused; non-DNS UDP stays blocked",
		"Conflict: coverage could not be applied or verified",
	} {
		if !containsLabel(labels, want) {
			t.Fatalf("missing warning %q in %v", want, labels)
		}
	}
	if enable, _ := findNode(view.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is available while coverage is on")
	}
	if enableReason, _ := findNode(view.Root, "coverage.enable.reason"); !strings.Contains(enableReason.Label, "already on") {
		t.Fatalf("enable reason = %q", enableReason.Label)
	}
	if disable, _ := findNode(view.Root, "coverage.disable"); !disable.Enabled {
		t.Fatal("disable is disabled while coverage is on")
	}

	coverage, _ := findNode(view.Root, "coverage")
	walk(coverage, func(n Node) {
		label := n.Label
		if strings.Contains(label, "secret.example") || strings.Contains(label, "://") {
			t.Fatalf("coverage label leaked dynamic text: %q", label)
		}
		if containsControl(label) || runeLen(label) > LabelLimit {
			t.Fatalf("unsafe coverage label %q", label)
		}
	})
}

func TestCoverageMenuHelperAndUnknownStates(t *testing.T) {
	fx := newFixture()
	fx.tun = contract.TunStatus{
		Desired: true, State: contract.TunUnavailable,
		DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked, Helper: contract.TunHelperMissing,
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if summary, _ := findNode(view.Root, "coverage.summary"); summary.Label != "Coverage: unavailable · desired on · not active" {
		t.Fatalf("summary = %q", summary.Label)
	}
	if helper, _ := findNode(view.Root, "coverage.helper"); !strings.Contains(helper.Label, "missing") {
		t.Fatalf("helper = %q", helper.Label)
	}
	if enable, _ := findNode(view.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is available with a missing helper")
	}
	if reason, _ := findNode(view.Root, "coverage.enable.reason"); !strings.Contains(reason.Label, "helper is missing") {
		t.Fatalf("enable reason = %q", reason.Label)
	}
	if disable, _ := findNode(view.Root, "coverage.disable"); !disable.Enabled {
		t.Fatal("disable must stay available with a missing helper and a requested preference")
	}

	// An incompatible helper blocks enable with a specific reason; disable
	// remains available so the preference can be cleared.
	inc := newFixture()
	inc.tun = contract.TunStatus{
		Desired: true, State: contract.TunStateError,
		DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked,
		Helper: contract.TunHelperIncompatible,
	}
	incompatible := newSetup(t, inc, nil).ctl.View()
	if helper, _ := findNode(incompatible.Root, "coverage.helper"); !strings.Contains(helper.Label, "incompatible") {
		t.Fatalf("incompatible helper = %q", helper.Label)
	}
	if enable, _ := findNode(incompatible.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is available with an incompatible helper")
	}
	if reason, _ := findNode(incompatible.Root, "coverage.enable.reason"); !strings.Contains(reason.Label, "incompatible") {
		t.Fatalf("incompatible enable reason = %q", reason.Label)
	}
	if disable, _ := findNode(incompatible.Root, "coverage.disable"); !disable.Enabled {
		t.Fatal("disable is disabled with an incompatible helper")
	}

	fx.noTun = true
	unknown := newSetup(t, fx, nil).ctl.View()
	if summary, _ := findNode(unknown.Root, "coverage.summary"); !strings.Contains(summary.Label, "unknown") {
		t.Fatalf("unknown summary = %q", summary.Label)
	}
	if warnings, _ := findNode(unknown.Root, "coverage.warnings"); warnings.Label != "Warnings" {
		t.Fatalf("unknown warning title = %q, want no count", warnings.Label)
	}
	if enable, _ := findNode(unknown.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is available without a TUN status")
	}
	if disable, _ := findNode(unknown.Root, "coverage.disable"); disable.Enabled {
		t.Fatal("disable is available without a TUN status")
	}
}

func TestCoverageWarningsRefreshStateAfterTunAction(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	on := tunFixture()
	on.Desired, on.State = true, contract.TunOn
	s.ctl.applyTun(tunFromStatus(on))
	view := s.ctl.View()
	warnings, _ := findNode(view.Root, "coverage.warnings")
	if warnings.Label != "Warnings (refreshing)" {
		t.Fatalf("pre-refresh warning title = %q", warnings.Label)
	}
	if len(warnings.Children) != 1 || warnings.Children[0].Label != "Coverage warnings refreshing…" {
		t.Fatalf("pre-refresh warnings = %+v", warnings.Children)
	}

	if err := s.ctl.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	warnings, _ = findNode(s.ctl.View().Root, "coverage.warnings")
	if warnings.Label != "Warnings (0)" {
		t.Fatalf("post-refresh warning title = %q", warnings.Label)
	}
	if len(warnings.Children) != 1 || warnings.Children[0].Label != "No coverage warnings" {
		t.Fatalf("post-refresh warnings = %+v", warnings.Children)
	}
}

func TestTunCoverageTransitionsAllowIdempotentDisable(t *testing.T) {
	for _, state := range []contract.TunState{contract.TunStarting, contract.TunStopping} {
		t.Run(string(state), func(t *testing.T) {
			fx := newFixture()
			fx.tun = contract.TunStatus{
				Desired: true, State: state, DNS: contract.TunDNSDirect,
				UDP: contract.TunUDPBlocked, Helper: contract.TunHelperInstalled,
			}
			s := newSetup(t, fx, nil)
			view := s.ctl.View()
			if enable, _ := findNode(view.Root, "coverage.enable"); enable.Enabled {
				t.Fatalf("enable is available while coverage is %s", state)
			}
			if reason, _ := findNode(view.Root, "coverage.enable.reason"); !strings.Contains(reason.Label, string(state)) {
				t.Fatalf("%s enable reason = %q", state, reason.Label)
			}
			if disable, _ := findNode(view.Root, "coverage.disable"); !disable.Enabled {
				t.Fatalf("idempotent disable is disabled while coverage is %s", state)
			}

			ctx := context.Background()
			if _, err := s.ctl.Trigger(ctx, ActionTunDisable, Target{}); err != nil {
				t.Fatalf("%s disable trigger = %v", state, err)
			}
			waitFor(t, string(state)+" tun disable", func() bool {
				return s.caller.countOp(control.OpTunDisable) == 1
			})
			waitFor(t, string(state)+" tun disable job", func() bool {
				return s.ctl.View().Job == nil
			})
		})
	}
}

func TestTunActionsStoppedAndUnavailable(t *testing.T) {
	ctx := context.Background()

	stopped := newFixture()
	stopped.status["mode"] = string(contract.ModeStopped)
	stopped.status["listeners_open"] = false
	st := tunFixture()
	st.Desired, st.State = true, contract.TunOn
	stopped.tun = st
	s := newSetup(t, stopped, nil)
	view := s.ctl.View()
	if enable, _ := findNode(view.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is enabled in stopped mode")
	}
	if disable, _ := findNode(view.Root, "coverage.disable"); !disable.Enabled {
		t.Fatal("disable is disabled in stopped mode")
	}
	if _, err := s.ctl.Trigger(ctx, ActionTunEnable, Target{}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("enable in stopped = %v, want ErrBlocked", err)
	}
	if _, err := s.ctl.Trigger(ctx, ActionTunDisable, Target{}); err != nil {
		t.Fatalf("disable in stopped = %v", err)
	}
	waitFor(t, "stopped tun disable", func() bool { return s.caller.countOp(control.OpTunDisable) == 1 })

	down := newSetup(t, newFixture(), nil)
	down.caller.setTransport(true)
	if err := down.ctl.RefreshStatus(ctx); err == nil {
		t.Fatal("transport failure should be reported")
	}
	view = down.ctl.View()
	if enable, _ := findNode(view.Root, "coverage.enable"); enable.Enabled {
		t.Fatal("enable is enabled while the engine is unavailable")
	}
	if disable, _ := findNode(view.Root, "coverage.disable"); disable.Enabled {
		t.Fatal("disable is enabled while the engine is unavailable")
	}
}

func TestTunActionsRunOnTheClickAndRejectDuplicates(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()

	entered, release := s.caller.gateOn(control.OpTunEnable)
	res, err := s.ctl.Trigger(ctx, ActionTunEnable, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("enable trigger = %+v, %v", res, err)
	}
	<-entered
	if _, err := s.ctl.Trigger(ctx, ActionTunEnable, Target{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second enable = %v, want ErrBusy", err)
	}
	release()
	waitFor(t, "tun enable request", func() bool { return s.caller.countOp(control.OpTunEnable) == 1 })
	waitFor(t, "tun enable job", func() bool { return s.ctl.View().Job == nil })
	if n := s.caller.countOp(control.OpTunEnable); n != 1 {
		t.Fatalf("enable requests = %d, want 1", n)
	}
	notes := s.notifier.all()
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1].Body, "coverage") {
		t.Fatalf("notifications = %+v", notes)
	}
	for _, n := range notes {
		if strings.Contains(n.Body, "://") || strings.Contains(n.Body, "example") {
			t.Fatalf("notification leaked dynamic text: %+v", n)
		}
	}

	on := tunFixture()
	on.Desired, on.State = true, contract.TunOn
	s.caller.setTun(on)
	if err := s.ctl.RefreshStatus(ctx); err != nil {
		t.Fatal(err)
	}
	res, err = s.ctl.Trigger(ctx, ActionTunDisable, Target{})
	if err != nil || res.Status != ResultJobStarted {
		t.Fatalf("disable trigger = %+v, %v", res, err)
	}
	waitFor(t, "tun disable request", func() bool { return s.caller.countOp(control.OpTunDisable) == 1 })
	waitFor(t, "tun disable job", func() bool { return s.ctl.View().Job == nil })
	if n := s.caller.countOp(control.OpTunDisable); n != 1 {
		t.Fatalf("disable requests = %d, want 1", n)
	}
}

func TestTunDisableDoesNotClaimOffWhenCoverageUncertain(t *testing.T) {
	fx := newFixture()
	on := tunFixture()
	on.Desired, on.State = true, contract.TunOn
	fx.tun = on
	uncertain := contract.TunStatus{
		Desired: false, State: contract.TunUnavailable,
		DNS: contract.TunDNSDirect, UDP: contract.TunUDPBlocked,
		Helper: contract.TunHelperMissing, LastError: contract.TunReasonConflict,
	}
	fx.tunOpResult = &uncertain
	s := newSetup(t, fx, nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionTunDisable, Target{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "uncertain disable", func() bool {
		notes := s.notifier.all()
		return len(notes) > 0 && strings.Contains(notes[len(notes)-1].Body, "not confirmed off")
	})
	notes := s.notifier.all()
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1].Body, "not confirmed off") {
		t.Fatalf("notifications = %+v, want an uncertain-off result", notes)
	}
	if s.ctl.View().Tun.State != contract.TunUnavailable {
		t.Fatalf("tray overclaimed coverage off: %+v", s.ctl.View().Tun)
	}
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func TestTunTrayTokenAndSafeTooltip(t *testing.T) {
	cases := []struct {
		name string
		tun  TunView
		want string
	}{
		{"off", TunView{Known: true, State: contract.TunOff}, "off"},
		{"on", TunView{Known: true, Desired: true, State: contract.TunOn}, "on"},
		{"on without request", TunView{Known: true, State: contract.TunOn}, "error"},
		{"requested but off", TunView{Known: true, Desired: true, State: contract.TunOff}, "error"},
		{"error state", TunView{Known: true, State: contract.TunStateError}, "error"},
		{"unavailable", TunView{Known: true, State: contract.TunUnavailable}, "error"},
		{"conflict while on", TunView{Known: true, Desired: true, State: contract.TunOn, LastError: contract.TunReasonConflict}, "error"},
		{"starting", TunView{Known: true, Desired: true, State: contract.TunStarting}, "pending"},
		{"unknown", TunView{}, ""},
	}
	for _, tc := range cases {
		if got := TunIcon(tc.tun); got != tc.want {
			t.Fatalf("%s token = %q, want %q", tc.name, got, tc.want)
		}
	}

	view := menuView(EngineReady, disabledItem("status", "Targeted · 3 usable · 1 open"))
	view.Tun = TunView{Known: true, Desired: true, State: contract.TunOn}
	tray := trayFor(view)
	if tray.Tun != "on" || !strings.Contains(tray.Tooltip, "TUN on") {
		t.Fatalf("tray = %+v", tray)
	}
	if containsControl(tray.Tooltip) || runeLen(tray.Tooltip) > HeaderLimit {
		t.Fatalf("unsafe tooltip %q", tray.Tooltip)
	}
	view.Tun = TunView{Known: true, Desired: true, State: contract.TunStateError}
	if tray := trayFor(view); tray.Tun != "error" || !strings.Contains(tray.Tooltip, "TUN error") {
		t.Fatalf("error tray = %+v", tray)
	}
}

func TestBinderRefreshesTrayWhenOnlyTunChanges(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	view := menuView(EngineReady, disabledItem("status", "Targeted · 3 usable"))
	view.Tun = TunView{Known: true, State: contract.TunOff}
	if _, err := binder.Apply(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	view.Tun = TunView{Known: true, Desired: true, State: contract.TunOn}
	if _, err := binder.Apply(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	ops := backend.batch(0)
	if len(ops) != 1 || ops[0].Kind != OpSetTray || ops[0].Tray.Tun != "on" {
		t.Fatalf("TUN-only change ops = %+v", ops)
	}
}
