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

func rootKeys(root Node) []string {
	out := make([]string, 0, len(root.Children))
	for _, child := range root.Children {
		out = append(out, child.Key)
	}
	return out
}

func TestDeterministicMenuTreeMatchesContract(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	view := s.ctl.View()
	if view.Root.Key != "root" || view.Root.Label != "RouteLatch" || view.Root.Kind != NodeSubmenu {
		t.Fatalf("root = %+v", view.Root)
	}
	wantKeys := []string{
		"status", "mode", "coverage", "proposals", "rules", "pool", "desktop", "diagnostics",
		"data", "settings", "advanced", "last_action", "quit",
	}
	if got := rootKeys(view.Root); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("root keys = %v, want %v", got, wantKeys)
	}
	if status, _ := findNode(view.Root, "status"); status.Label != "Targeted · 140 usable · 3 open" || status.Enabled {
		t.Fatalf("status = %+v", status)
	}
	mode, _ := findNode(view.Root, "mode.targeted")
	if !mode.Checked || mode.Check != CheckRadio || !mode.Enabled {
		t.Fatalf("mode target = %+v", mode)
	}
	if stop, _ := findNode(view.Root, "mode.stop"); stop.Label != "Stop listening" {
		t.Fatalf("stop = %+v", stop)
	}
	if summary, _ := findNode(view.Root, "coverage.summary"); summary.Label != "Coverage: off · desired off" {
		t.Fatalf("coverage summary = %q", summary.Label)
	}
	if helper, _ := findNode(view.Root, "coverage.helper"); helper.Label != "Helper: installed" {
		t.Fatalf("coverage helper = %q", helper.Label)
	}
	if enable, _ := findNode(view.Root, "coverage.enable"); !enable.Enabled {
		t.Fatalf("enable coverage = %+v", enable)
	}
	if disable, _ := findNode(view.Root, "coverage.disable"); disable.Enabled {
		t.Fatalf("disable coverage = %+v", disable)
	}
	if n, _ := findNode(view.Root, "proposals"); n.Label != "Proposals (3)" {
		t.Fatalf("proposals = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "rules"); n.Label != "Approved rules (117)" {
		t.Fatalf("rules = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "pool.summary"); n.Label != "176 records · 140 usable · 100 healthy · 0 reachable · 20 cooling" {
		t.Fatalf("summary = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "quit.now"); !n.Visible || !n.Enabled {
		t.Fatalf("quit = %+v", n)
	}
	if n, _ := findNode(view.Root, "settings.config"); !strings.Contains(n.Label, "HTTP 127.0.0.1:18080") {
		t.Fatalf("config summary = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "diagnostics.env.HTTP_PROXY"); !strings.Contains(n.Label, "127.0.0.1:18080") {
		t.Fatalf("env = %q", n.Label)
	}

	keys := map[string]bool{}
	walk(view.Root, func(n Node) {
		if keys[n.Key] {
			t.Fatalf("duplicate key %q", n.Key)
		}
		keys[n.Key] = true
		if n.Label == "" {
			t.Fatalf("empty label at %q", n.Key)
		}
		if containsControl(n.Label) {
			t.Fatalf("unsafe label at %q: %q", n.Key, n.Label)
		}
		if runeLen(n.Label) > LabelLimit {
			t.Fatalf("label at %q is %d runes", n.Key, runeLen(n.Label))
		}
	})
	// Repeated builds over identical data keep the same keys.
	if err := s.ctl.RefreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rootKeys(s.ctl.View().Root); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("root order changed after refresh: %v", got)
	}
}

// TestNestedPaginationTwentyRowsAndDepth covers the fixed 20-row local page
// size and the nested page submenu: page 2 lives inside page 1, page 6 is
// reachable through the chain, and no Previous/Next action exists.
func TestNestedPaginationTwentyRowsAndDepth(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	view := s.ctl.View()
	if count := countRuleNodes(view.Root); count != PageSize {
		t.Fatalf("page 1 rule nodes = %d, want %d", count, PageSize)
	}
	rules, _ := findNode(view.Root, "rules")
	page2, ok := directChild(rules, "rules.page.2")
	if !ok || page2.Label != "Page 2/6" || page2.Kind != NodeSubmenu {
		t.Fatalf("rules page 2 = %+v", page2)
	}
	page3, ok := directChild(page2, "rules.page.2.page.3")
	if !ok || page3.Label != "Page 3/6" {
		t.Fatalf("page 3 nested in page 2 = %+v", page3)
	}
	if _, ok := findNode(page3, "rules.page.2.page.3.page.4.page.5.page.6"); !ok {
		t.Fatal("page 6 is not reachable through the nested page chain")
	}
	if depth := maxDepth(view.Root, 0); depth < 6 {
		t.Fatalf("nested pages reach depth %d, want at least 6", depth)
	}

	healthy, _ := findNode(view.Root, "pool.group.pool.healthy")
	if got := countDirectChildrenWithPrefix(healthy, "pool.row."); got != PageSize {
		t.Fatalf("healthy page 1 rows = %d, want %d", got, PageSize)
	}
	healthyPage2, ok := directChild(healthy, "pool.group.pool.healthy.page.2")
	if !ok || healthyPage2.Label != "Page 2/5" {
		t.Fatalf("healthy page 2 = %+v", healthyPage2)
	}
	wantFirst := poolRowKey(fixturePoolID(PageSize))
	if _, ok := directChild(healthyPage2, wantFirst); !ok {
		t.Fatalf("healthy page 2 does not start at %q", wantFirst)
	}
	if _, ok := directChild(healthy, wantFirst); ok {
		t.Fatal("healthy page 1 also renders a page-2 row")
	}
	untested, _ := findNode(view.Root, "pool.group.pool.untested")
	if page, ok := directChild(untested, "pool.group.pool.untested.page.2"); !ok || page.Label != "Page 2/2" {
		t.Fatalf("untested page 2 = %+v", page)
	}
	if _, ok := findNode(view.Root, "pool.group.pool.cooling.page.2"); ok {
		t.Fatal("single-page cooling group rendered a page submenu")
	}

	walk(view.Root, func(n Node) {
		if strings.HasSuffix(n.Key, ".prev") || strings.HasSuffix(n.Key, ".next") {
			t.Fatalf("pagination action node %q still exists", n.Key)
		}
		if n.Label == "Previous page" || n.Label == "Next page" {
			t.Fatalf("pagination action label %q still exists", n.Label)
		}
	})
}

func directChild(n Node, key string) (Node, bool) {
	for _, child := range n.Children {
		if child.Key == key {
			return child, true
		}
	}
	return Node{}, false
}

func maxDepth(n Node, depth int) int {
	max := depth
	for _, child := range n.Children {
		if d := maxDepth(child, depth+1); d > max {
			max = d
		}
	}
	return max
}

func countRuleNodes(root Node) int {
	rules, ok := findNode(root, "rules")
	if !ok {
		return 0
	}
	n := 0
	for _, child := range rules.Children {
		if strings.HasPrefix(child.Key, "rules.r-") {
			n++
		}
	}
	return n
}

func TestStoppedUsesResumeRequiresRestart(t *testing.T) {
	cases := []struct {
		name       string
		set        any
		present    bool
		wantHeader string
		modeOn     bool
		extra      bool
	}{
		{name: "true needs restart", set: true, present: true, wantHeader: "Stopped · listeners closed · restart engine", modeOn: false, extra: true},
		{name: "false can resume in place", set: false, present: true, wantHeader: "Stopped · choose a mode to resume", modeOn: true, extra: false},
		{name: "missing is conservative", present: false, wantHeader: "Stopped · listeners closed · restart recommended", modeOn: false, extra: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture()
			fx.status["mode"] = string(contract.ModeStopped)
			fx.status["ready"] = true
			fx.status["listeners_open"] = false
			if tc.present {
				fx.status["resume_requires_restart"] = tc.set
			}
			s := newSetup(t, fx, nil)
			view := s.ctl.View()
			if got := s.ctl.Snapshot().Engine; got != EngineStopped {
				t.Fatalf("engine = %s", got)
			}
			if status, _ := findNode(view.Root, "status"); status.Label != tc.wantHeader {
				t.Fatalf("header = %q, want %q", status.Label, tc.wantHeader)
			}
			if mode, _ := findNode(view.Root, "mode.targeted"); mode.Enabled != tc.modeOn {
				t.Fatalf("mode enabled = %t, want %t", mode.Enabled, tc.modeOn)
			}
			if _, ok := findNode(view.Root, "restart.resume"); ok != tc.extra {
				t.Fatalf("restart.resume present = %t, want %t", ok, tc.extra)
			}
			if !tc.extra {
				if _, err := s.ctl.Trigger(context.Background(), ActionModeTargeted, Target{}); err != nil {
					t.Fatalf("mode resume: %v", err)
				}
				if s.caller.countOp(control.OpMode) != 1 {
					t.Fatal("resume mode did not reach the socket")
				}
				return
			}
			before := s.caller.countOp(control.OpMode)
			if _, err := s.ctl.Trigger(context.Background(), ActionModeTargeted, Target{}); !errors.Is(err, ErrBlocked) {
				t.Fatalf("mode while restart is required = %v", err)
			}
			if s.caller.countOp(control.OpMode) != before {
				t.Fatal("a stopped process was silently resumed in place")
			}
			node, _ := findNode(view.Root, "restart.resume")
			if node.Action != ActionRestartEngine || !node.Enabled {
				t.Fatalf("restart item = %+v", node)
			}
		})
	}
}

func TestStoppedDisablesDynamicAndMutationActions(t *testing.T) {
	fx := newFixture()
	fx.status["mode"] = string(contract.ModeStopped)
	fx.status["listeners_open"] = false
	fx.status["resume_requires_restart"] = false
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	for _, key := range []string{
		"mode.stop", "proposals.clear", "rules.r-000.remove",
		"pool.auto", "pool.refresh", "pool.rerank", "pool.scan", "pool.test.first20",
		"desktop.set", "diagnostics.doctor",
	} {
		n, ok := findNode(view.Root, key)
		if !ok {
			t.Fatalf("missing %q", key)
		}
		if n.Enabled {
			t.Fatalf("%q enabled in stopped mode", key)
		}
	}
	for _, key := range []string{"mode.targeted", "rules.r-000.details", "desktop.restore", "data.backup", "diagnostics.logpath", "pool.import.terminal"} {
		n, ok := findNode(view.Root, key)
		if !ok {
			t.Fatalf("missing %q", key)
		}
		if !n.Enabled {
			t.Fatalf("%q disabled in stopped mode", key)
		}
	}
}

func TestDesktopClearedConflictStillGatesQuitAndCutWarnings(t *testing.T) {
	fx := newFixture()
	// A cleared marker whose recorded value no longer holds means the user
	// re-enabled the proxy: it is live again, not a disabled setting.
	fx.desktop = control.DesktopData{
		Environment: "GNOME", Supported: true, Applied: true, Cleared: true, PreviousSet: true,
		Conflict: []string{"org.gnome.system.proxy mode"},
	}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "quit.now"); n.Visible {
		t.Fatal("plain Quit visible while a cleared marker conflicts with a live proxy")
	}
	for _, key := range []string{"quit.restore", "quit.keep"} {
		n, ok := findNode(view.Root, key)
		if !ok || !n.Visible || !n.Enabled {
			t.Fatalf("%q = %+v", key, n)
		}
	}
	for _, key := range []string{"mode.desktop-warning", "advanced.restart.warning"} {
		n, ok := findNode(view.Root, key)
		if !ok || n.Enabled || !strings.Contains(n.Label, "cuts it") {
			t.Fatalf("%q = %+v, want the applied-proxy cut warning", key, n)
		}
	}
	if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "conflict") || strings.Contains(n.Label, "cleared (disabled)") {
		t.Fatalf("desktop status = %q, want a visible conflict instead of cleared (disabled)", n.Label)
	}
	if _, err := s.ctl.Trigger(context.Background(), ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("plain Quit with a cleared-but-conflicting desktop proxy = %v", err)
	}
}

func TestDesktopAppliedShapesQuitAndDisablesSetInStopped(t *testing.T) {
	fx := newFixture()
	fx.desktop = control.DesktopData{Environment: "KDE", Supported: true, Applied: true, PreviousSet: true, Conflict: []string{"http_proxy"}}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "quit.now"); n.Visible {
		t.Fatal("plain Quit visible while the desktop proxy is applied")
	}
	for _, key := range []string{"quit.restore", "quit.keep"} {
		n, ok := findNode(view.Root, key)
		if !ok || !n.Visible || !n.Enabled {
			t.Fatalf("%q = %+v", key, n)
		}
	}
	if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "applied") {
		t.Fatalf("desktop status = %q", n.Label)
	}
	if _, err := s.ctl.Trigger(context.Background(), ActionQuit, Target{}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("plain Quit with an applied desktop proxy = %v", err)
	}
}

func TestDesktopReadFailureUsesExplicitQuitChoices(t *testing.T) {
	fx := newFixture()
	s := newSetup(t, fx, nil)
	s.caller.setFail(control.OpDesktopStatus, contract.ExternalToolFailed, "tool failed")
	if err := s.ctl.RefreshDesktop(context.Background()); err == nil {
		t.Fatal("expected desktop refresh failure")
	}
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "quit.now"); n.Visible {
		t.Fatal("plain Quit visible with unknown desktop state")
	}
	if _, ok := findNode(view.Root, "quit.restore"); !ok {
		t.Fatal("restore-and-quit missing")
	}
	if n, _ := findNode(view.Root, "desktop.status"); !strings.Contains(n.Label, "unknown") {
		t.Fatalf("desktop status = %q", n.Label)
	}
}

func TestDiagnosticsDisabledOnlyDisablesProposalDecisions(t *testing.T) {
	fx := newFixture()
	fx.status["diagnostics"] = map[string]any{"enabled": false, "queued": 0}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	for _, id := range []string{"p-route-1", "p-route-2"} {
		for _, suffix := range []string{".accept", ".accept-suffix", ".reject", ".recheck"} {
			n, ok := findNode(view.Root, "proposals.pending."+id+suffix)
			if !ok {
				t.Fatalf("missing %s", id+suffix)
			}
			if n.Enabled {
				t.Fatalf("%s enabled with diagnostics off", id+suffix)
			}
		}
	}
	for _, suffix := range []string{".accept", ".reject", ".recheck"} {
		n, ok := findNode(view.Root, "proposals.pending.p-unroute-1"+suffix)
		if !ok {
			t.Fatalf("missing p-unroute-1%s", suffix)
		}
		if n.Enabled {
			t.Fatalf("p-unroute-1%s enabled with diagnostics off", suffix)
		}
	}
	if _, ok := findNode(view.Root, "proposals.pending.p-unroute-1.accept-suffix"); ok {
		t.Fatal("an unroute proposal offered suffix acceptance")
	}
	if _, err := s.ctl.Trigger(context.Background(), ActionProposalAcceptSuffix, Target{ProposalID: "p-route-1"}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("suffix accept with diagnostics off = %v, want ErrBlocked", err)
	}
	if got := s.caller.countOp(control.OpProposalsAccept); got != 0 {
		t.Fatalf("blocked suffix accept reached the socket %d times", got)
	}
	if n, _ := findNode(view.Root, "proposals.decided"); n.Label != "Show decided (2)" {
		t.Fatalf("decided = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "proposals.clear"); !n.Enabled {
		t.Fatal("clear must stay available")
	}
}

func TestUnrouteRecheckIsDisabled(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	view := s.ctl.View()
	n, ok := findNode(view.Root, "proposals.pending.p-unroute-1.recheck")
	if !ok || n.Enabled {
		t.Fatalf("unroute recheck = %+v", n)
	}
	accept, _ := findNode(view.Root, "proposals.pending.p-unroute-1.accept")
	if !accept.Enabled {
		t.Fatal("unroute accept should be available")
	}
	if _, ok := findNode(view.Root, "proposals.pending.p-unroute-1.accept-suffix"); ok {
		t.Fatal("an unroute proposal offered suffix acceptance")
	}
}

func TestMaliciousDynamicLabelsAreSanitized(t *testing.T) {
	fx := newFixture()
	bad := "evil\x1b[31m\u202e\r\nnext\t" + strings.Repeat("x", 200)
	fx.rules[0].Host = bad
	fx.rules[0].Note = bad
	fx.pool[0].DisplayName = bad
	fx.pool[0].Reason = bad
	fx.proposals[0].Note = bad
	fx.proposals[0].Destination = contract.Destination{Kind: contract.DestDomain, Host: "bad\x1b]0;title\x07.example", Port: 443}
	fx.status["warnings"] = []string{bad}
	fx.desktop.Environment = bad
	fx.desktop.Notice = bad
	fx.desktop.Conflict = []string{bad}
	fx.config.HTTPListen = bad
	fx.paths.Config = bad
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	for _, label := range allLabels(view.Root) {
		if containsControl(label) {
			t.Fatalf("control sequence survived: %q", label)
		}
		if runeLen(label) > LabelLimit {
			t.Fatalf("label exceeds %d runes: %q", LabelLimit, label)
		}
	}
	if status, _ := findNode(view.Root, "status"); runeLen(status.Label) > HeaderLimit {
		t.Fatalf("header too long: %d", runeLen(status.Label))
	}
	if n, _ := findNode(view.Root, "rules.r-116.details.note"); containsControl(n.Label) {
		t.Fatalf("rule detail leaked controls: %q", n.Label)
	}
}

func TestEmptyListsRenderDisabledHints(t *testing.T) {
	fx := newFixture()
	fx.rules = nil
	fx.pool = nil
	fx.counts = poolZero()
	fx.proposals = nil
	fx.status["warnings"] = []string{}
	fx.status["pool"] = map[string]any{"total": 0, "usable": 0, "healthy": 0, "cooling": 0}
	s := newSetup(t, fx, nil)
	view := s.ctl.View()
	for _, key := range []string{
		"proposals.pending.empty", "rules.empty", "pool.group.pool.healthy.empty",
		"pool.group.pool.reachable.empty",
		"pool.group.pool.unsupported.empty", "diagnostics.warnings.empty", "diagnostics.results.empty",
	} {
		n, ok := findNode(view.Root, key)
		if !ok || n.Enabled {
			t.Fatalf("%q = %+v", key, n)
		}
	}
	if n, _ := findNode(view.Root, "pool.summary"); !strings.Contains(n.Label, "No exits") {
		t.Fatalf("pool summary = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "pool.group.pool.healthy"); n.Label != "Healthy (0)" {
		t.Fatalf("healthy group = %q", n.Label)
	}
}

func TestStoppedRestartAndFreshProcessResumes(t *testing.T) {
	fx := newFixture()
	fx.status["mode"] = string(contract.ModeStopped)
	fx.status["listeners_open"] = false
	fx.status["resume_requires_restart"] = true
	s := newSetup(t, fx, nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionRestartEngine, Target{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart request", func() bool {
		_, restarts := s.life.counts()
		return restarts == 1
	})
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleRestarting})
	s.ctl.HandleEvent(LifecycleEvent{Kind: LifecycleReady})
	if got := s.ctl.View().LastAction; got != "Engine restart confirmed" {
		t.Fatalf("last action = %q", got)
	}
	// The restarted process starts in the persisted stopped state: a fresh
	// process can leave it without another restart.
	s.caller.mu.Lock()
	s.caller.fx.status["resume_requires_restart"] = false
	s.caller.mu.Unlock()
	if err := s.ctl.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	view := s.ctl.View()
	if status, _ := findNode(view.Root, "status"); status.Label != "Stopped · choose a mode to resume" {
		t.Fatalf("header = %q", status.Label)
	}
	if mode, _ := findNode(view.Root, "mode.targeted"); !mode.Enabled {
		t.Fatal("mode stayed disabled after a fresh stopped process")
	}
	if _, ok := findNode(view.Root, "restart.resume"); ok {
		t.Fatal("stale restart item survived the fresh status")
	}
	if _, err := s.ctl.Trigger(ctx, ActionModeTargeted, Target{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if s.caller.countOp(control.OpMode) != 1 {
		t.Fatal("resume request missing")
	}
}

func TestAutostartDisabledForCustomDataDir(t *testing.T) {
	fx := newFixture()
	s := newSetup(t, fx, func(o *Options) { o.CustomDataDir = true })
	view := s.ctl.View()
	node, _ := findNode(view.Root, "settings.autostart")
	if node.Enabled {
		t.Fatal("Start at login enabled with a custom data directory")
	}
	reason, ok := findNode(view.Root, "settings.autostart.reason")
	if !ok || !strings.Contains(reason.Label, "default data directory") {
		t.Fatalf("reason = %+v", reason)
	}
	if _, err := s.ctl.Trigger(context.Background(), ActionAutostartToggle, Target{}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("autostart toggle = %v", err)
	}
}

func TestShowLogPathNotifiesOnlyThePath(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	if _, err := s.ctl.Trigger(context.Background(), ActionShowLogPath, Target{}); err != nil {
		t.Fatal(err)
	}
	notes := s.notifier.all()
	if len(notes) != 1 || notes[0].Body != "Log: /data/log/routelatch.log" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestJobAppearsAtRoot(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	entered, release := s.caller.gateOn(control.OpBackup)
	if _, err := s.ctl.Trigger(context.Background(), ActionBackup, Target{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	view := s.ctl.View()
	job, ok := findNode(view.Root, "job")
	if !ok || job.Enabled || !strings.Contains(job.Label, "backup") {
		t.Fatalf("job item = %+v", job)
	}
	for _, key := range rootKeys(view.Root) {
		if strings.HasPrefix(key, "pending.") {
			t.Fatalf("confirmation node %q is still rendered", key)
		}
	}
	release()
	waitFor(t, "job end", func() bool { return s.ctl.View().Job == nil })
}
