package trayui

import (
	"reflect"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

func TestStableKeyPartIsSafeAndDeterministic(t *testing.T) {
	bad := "id/\x1b[31m\u202e"
	first := stableKeyPart(bad)
	second := stableKeyPart(bad)
	if first != second {
		t.Fatal("stable key is not deterministic")
	}
	if first == "" || first == bad || strings.ContainsAny(first, "/\x1b") {
		t.Fatalf("unsafe key part %q", first)
	}
	if stableKeyPart("r-1") == stableKeyPart("r-2") {
		t.Fatal("distinct ids produced the same key")
	}
}

func TestActionRequestTable(t *testing.T) {
	id := contract.ProposalID("p-1")
	rule := contract.RuleID("r-1")
	exit := contract.ExitID("x-1")
	target := Target{ProposalID: id, RuleID: rule, ExitID: exit}

	cases := []struct {
		action Action
		want   control.Request
	}{
		{ActionModeTargeted, control.Request{Op: control.OpMode, Mode: string(contract.ModeTargeted)}},
		{ActionModeAllProxy, control.Request{Op: control.OpMode, Mode: string(contract.ModeProxyAll)}},
		{ActionModeAllDirect, control.Request{Op: control.OpMode, Mode: string(contract.ModeDirectAll)}},
		{ActionStopListening, control.Request{Op: control.OpMode, Mode: string(contract.ModeStopped)}},
		{ActionTunEnable, control.Request{Op: control.OpTunEnable}},
		{ActionTunDisable, control.Request{Op: control.OpTunDisable}},
		{ActionProposalAccept, control.Request{Op: control.OpProposalsAccept, ID: string(id)}},
		{ActionProposalAcceptSuffix, control.Request{Op: control.OpProposalsAccept, ID: string(id), RuleKind: string(contract.RuleDomainSuffix)}},
		{ActionProposalReject, control.Request{Op: control.OpProposalsReject, ID: string(id)}},
		{ActionProposalRecheck, control.Request{Op: control.OpProposalsRecheck, ID: string(id)}},
		{ActionProposalsClear, control.Request{Op: control.OpProposalsClear}},
		{ActionRuleRemove, control.Request{Op: control.OpRulesRemove, ID: string(rule)}},
		{ActionPoolSelectAuto, control.Request{Op: control.OpPoolSelect, ID: "auto"}},
		{ActionPoolSelect, control.Request{Op: control.OpPoolSelect, ID: string(exit)}},
		{ActionPoolRemove, control.Request{Op: control.OpPoolRemove, ID: string(exit)}},
		{ActionPoolRefresh, control.Request{Op: control.OpPoolRefresh, All: true}},
		{ActionPoolRerank, control.Request{Op: control.OpPoolRerank}},
		{ActionPoolTest, control.Request{Op: control.OpPoolTest, ID: string(exit)}},
		{ActionPoolTestFirst20, control.Request{Op: control.OpPoolTest}},
		{ActionPoolScan, control.Request{Op: control.OpPoolScan, Limit: contract.PoolScanBatch}},
		{ActionDesktopRefresh, control.Request{Op: control.OpDesktopStatus}},
		{ActionDesktopSet, control.Request{Op: control.OpDesktopSet}},
		{ActionDesktopRestore, control.Request{Op: control.OpDesktopRestore}},
		{ActionDesktopClear, control.Request{Op: control.OpDesktopClear}},
		{ActionBackup, control.Request{Op: control.OpBackup}},
		{ActionDoctor, control.Request{Op: control.OpDoctor}},
	}
	seen := map[Action]bool{}
	for _, tc := range cases {
		got, ok := requestFor(tc.action, target)
		if !ok {
			t.Fatalf("%s has no request", tc.action)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s request = %+v, want %+v", tc.action, got, tc.want)
		}
		seen[tc.action] = true
	}

	for _, action := range allActions {
		if action == ActionNone {
			continue
		}
		if kindOf(action) == kindNone {
			t.Fatalf("%s has no action kind", action)
		}
		_, hasRequest := requestFor(action, target)
		if kindOf(action) == kindRequest && !hasRequest {
			t.Fatalf("%s is a management action without a request", action)
		}
		if kindOf(action) != kindRequest && hasRequest {
			t.Fatalf("%s is not a management action but has a request", action)
		}
		if seen[action] && kindOf(action) != kindRequest {
			t.Fatalf("%s is listed as a request but has kind %v", action, kindOf(action))
		}
	}
	if _, ok := requestFor(ActionNone, target); ok {
		t.Fatal("ActionNone produced a request")
	}
	for _, action := range []Action{ActionTerminalMenu, ActionShowLogPath, ActionQuit, ActionQuitRestoreDesktop, ActionQuitKeepDesktop, ActionAutostartToggle} {
		if _, ok := requestFor(action, target); ok {
			t.Fatalf("%s must not be a management request", action)
		}
	}
}

func TestActionClassificationAndJobs(t *testing.T) {
	wantJobs := map[Action]string{
		ActionRestartEngine:      "restart engine",
		ActionTunEnable:          "tun enable",
		ActionTunDisable:         "tun disable",
		ActionQuitRestoreDesktop: "quit and restore desktop proxy",
		ActionPoolRefresh:        "pool refresh",
		ActionPoolTest:           "pool test",
		ActionPoolTestFirst20:    "pool test",
		ActionPoolScan:           "pool check",
		ActionProposalRecheck:    "proposal recheck",
		ActionDesktopSet:         "desktop set",
		ActionDesktopRestore:     "desktop restore",
		ActionDesktopClear:       "desktop clear",
		ActionBackup:             "backup",
		ActionDoctor:             "doctor",
		ActionAutostartToggle:    "autostart",
	}
	for _, action := range allActions {
		name, ok := jobName(action)
		want, wantOK := wantJobs[action]
		if ok != wantOK || name != want {
			t.Fatalf("%s job = (%q,%t), want (%q,%t)", action, name, ok, want, wantOK)
		}
	}
	if name, ok := jobName(ActionQuit); ok || name != "" {
		t.Fatal("Quit must be synchronous")
	}
}

func TestActionStringsAreUniqueAndLogSafe(t *testing.T) {
	seen := map[string]Action{}
	for _, action := range allActions {
		name := action.String()
		if name == "" {
			t.Fatalf("%d has an empty name", action)
		}
		if prev, ok := seen[name]; ok {
			t.Fatalf("%d and %d share %q", prev, action, name)
		}
		seen[name] = action
		if strings.ContainsAny(name, " \t\r\n\x1b") {
			t.Fatalf("action name %q is not log-safe", name)
		}
	}
}
