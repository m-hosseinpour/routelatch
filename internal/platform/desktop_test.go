package platform

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func gnomeSystem() *fakeSystem {
	f := newFakeSystem(map[string]string{
		"org.gnome.system.proxy.http host":    "''",
		"org.gnome.system.proxy.http port":    "0",
		"org.gnome.system.proxy.https host":   "''",
		"org.gnome.system.proxy.https port":   "0",
		"org.gnome.system.proxy.socks host":   "''",
		"org.gnome.system.proxy.socks port":   "0",
		"org.gnome.system.proxy ignore-hosts": "[]",
		"org.gnome.system.proxy mode":         "'none'",
	})
	f.look["gsettings"] = true
	return f
}

func kdeSystem() *fakeSystem {
	f := newFakeSystem(map[string]string{
		"ProxyType":         "0",
		"ReversedException": "true",
		"NoProxyFor":        "intranet",
		"httpProxy":         "http://old-proxy 3128",
	})
	f.look["kwriteconfig6"] = true
	f.look["kreadconfig6"] = true
	f.notifyHelp["kwriteconfig6"] = true
	return f
}

func kde5System() *fakeSystem {
	f := newFakeSystem(map[string]string{
		"ProxyType":         "0",
		"ReversedException": "true",
		"NoProxyFor":        "intranet",
		"httpProxy":         "http://old-proxy 3128",
	})
	f.look["kwriteconfig5"] = true
	f.look["kreadconfig5"] = true
	return f
}

func newAdapter(f *fakeSystem) *DesktopAdapter {
	return &DesktopAdapter{
		Runner:   f,
		LookPath: f.LookPath,
		Health:   goodHealth(),
		Now:      func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	}
}

func goodHealth() HealthChecker {
	return &fakeHealth{health: Health{HTTPListen: testTargets.HTTPListen, SOCKSListen: testTargets.SOCKSListen}}
}

func applyGNOME(t *testing.T, f *fakeSystem) contract.DesktopProxyState {
	t.Helper()
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return state
}

func TestDesktopApplyRefusesUnhealthyService(t *testing.T) {
	f := gnomeSystem()
	adapter := newAdapter(f)
	adapter.Health = &fakeHealth{err: errors.New("service down")}
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil || state.Applied {
		t.Fatal("Apply pointed the desktop at an unhealthy service")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Stopped {
		t.Fatalf("error = %v, want stopped", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Apply ran desktop commands before the health check: %v", f.calls)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("desktop setting changed despite unhealthy service: %q", f.values["org.gnome.system.proxy mode"])
	}
}

func TestDesktopApplyRefusesForeignListener(t *testing.T) {
	f := gnomeSystem()
	adapter := newAdapter(f)
	adapter.Health = &fakeHealth{health: Health{HTTPListen: "127.0.0.1:9000", SOCKSListen: testTargets.SOCKSListen}}
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil || state.Applied {
		t.Fatal("Apply accepted a foreign listener")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Stopped {
		t.Fatalf("error = %v, want stopped", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Apply ran desktop commands before the identity check: %v", f.calls)
	}
}

func TestDesktopApplyRefusesWithoutHealthChecker(t *testing.T) {
	f := gnomeSystem()
	adapter := newAdapter(f)
	adapter.Health = nil
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil || state.Applied {
		t.Fatal("Apply ran without a health checker")
	}
	if len(f.calls) != 0 {
		t.Fatalf("Apply ran desktop commands without a health checker: %v", f.calls)
	}
}

func TestGNOMEApplyRecordsPreviousAndWritesModeLast(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)

	if !state.Applied || state.Environment != "gnome" || len(state.Keys) != 8 {
		t.Fatalf("state = %#v, want applied gnome with 8 keys", state)
	}
	if state.AppliedAt.IsZero() {
		t.Fatal("AppliedAt was not recorded")
	}
	want := map[string]string{
		"org.gnome.system.proxy.http host":  "'127.0.0.1'",
		"org.gnome.system.proxy.http port":  "8317",
		"org.gnome.system.proxy.https host": "'127.0.0.1'",
		"org.gnome.system.proxy.https port": "8317",
		"org.gnome.system.proxy.socks host": "'127.0.0.1'",
		"org.gnome.system.proxy.socks port": "8318",
		"org.gnome.system.proxy mode":       "'manual'",
	}
	for key, value := range want {
		if f.values[key] != value {
			t.Errorf("%s = %q, want %q", key, f.values[key], value)
		}
	}
	if !strings.Contains(f.values["org.gnome.system.proxy ignore-hosts"], "'127.0.0.0/8'") {
		t.Errorf("ignore-hosts = %q, want the local/LAN list", f.values["org.gnome.system.proxy ignore-hosts"])
	}

	previous := map[string]string{}
	for _, key := range state.Keys {
		previous[key.Name] = key.Previous
	}
	if previous["org.gnome.system.proxy mode"] != "'none'" {
		t.Errorf("previous mode = %q, want 'none'", previous["org.gnome.system.proxy mode"])
	}
	if previous["org.gnome.system.proxy.http port"] != "0" {
		t.Errorf("previous http port = %q, want 0", previous["org.gnome.system.proxy.http port"])
	}
	if previous["org.gnome.system.proxy ignore-hosts"] != "[]" {
		t.Errorf("previous ignore-hosts = %q, want []", previous["org.gnome.system.proxy ignore-hosts"])
	}

	sets := 0
	lastSet := ""
	for from := len(f.calls) - 1; from >= 0; from-- {
		if f.calls[from].name == "gsettings" && f.calls[from].args[0] == "set" {
			sets++
			if lastSet == "" {
				lastSet = f.calls[from].args[2]
			}
		}
	}
	if sets != 8 {
		t.Fatalf("gsettings set calls = %d, want 8", sets)
	}
	if lastSet != "mode" {
		t.Fatalf("last applied key = %q, want mode", lastSet)
	}
}

func TestGNOMERestoreLeavesUserEditsAlone(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)

	// The user changes two keys after RouteLatch applied its values.
	f.values["org.gnome.system.proxy mode"] = "'auto'"
	f.values["org.gnome.system.proxy.https port"] = "9999"
	f.clearCalls()

	result, err := newAdapter(f).Restore(context.Background(), state)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(result.State.Keys) != 0 || result.State.Applied {
		t.Fatalf("state after full restore = %#v, want cleared", result.State)
	}
	if len(result.Restored) != 6 {
		t.Errorf("restored %d keys, want 6: %v", len(result.Restored), result.Restored)
	}
	if !contains(result.UserModified, "org.gnome.system.proxy mode") ||
		!contains(result.UserModified, "org.gnome.system.proxy.https port") {
		t.Fatalf("user-modified keys = %v, want mode and https port", result.UserModified)
	}

	if f.values["org.gnome.system.proxy mode"] != "'auto'" {
		t.Fatalf("user mode was overwritten: %q", f.values["org.gnome.system.proxy mode"])
	}
	if f.values["org.gnome.system.proxy.https port"] != "9999" {
		t.Fatalf("user https port was overwritten: %q", f.values["org.gnome.system.proxy.https port"])
	}
	if f.values["org.gnome.system.proxy.http host"] != "''" ||
		f.values["org.gnome.system.proxy.http port"] != "0" ||
		f.values["org.gnome.system.proxy.socks host"] != "''" ||
		f.values["org.gnome.system.proxy.socks port"] != "0" ||
		f.values["org.gnome.system.proxy ignore-hosts"] != "[]" {
		t.Fatalf("owned keys were not restored: %#v", f.values)
	}

	for _, call := range f.calls {
		if call.name != "gsettings" || len(call.args) < 4 || call.args[0] != "set" {
			continue
		}
		key := call.args[1] + " " + call.args[2]
		if key == "org.gnome.system.proxy mode" || key == "org.gnome.system.proxy.https port" {
			t.Fatalf("restore wrote a key the user had changed: %s", key)
		}
	}
}

func TestRestoreCanResumeAfterAnIncompleteRun(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	f.writeErr["org.gnome.system.proxy ignore-hosts"] = errors.New("temporary gsettings failure")

	adapter := newAdapter(f)
	first, err := adapter.Restore(context.Background(), state)
	if err == nil {
		t.Fatal("Restore succeeded despite a failed key")
	}
	if !first.State.Applied || len(first.State.Keys) != 1 || first.State.Keys[0].Name != "org.gnome.system.proxy ignore-hosts" {
		t.Fatalf("partial restore marker = %#v, want only the failed key", first.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode was not restored before the failure: %q", f.values["org.gnome.system.proxy mode"])
	}

	second, err := adapter.Restore(context.Background(), first.State)
	if err != nil {
		t.Fatalf("resumed Restore: %v", err)
	}
	if second.State.Applied || len(second.State.Keys) != 0 {
		t.Fatalf("resumed restore left ownership behind: %#v", second.State)
	}
	if f.values["org.gnome.system.proxy ignore-hosts"] != "[]" {
		t.Fatalf("ignore-hosts was not restored on resume: %q", f.values["org.gnome.system.proxy ignore-hosts"])
	}
}

func TestGNOMERestoreAfterCrashWorksOffline(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)

	// Simulate a new process after a crash: no health checker, no detection,
	// only the persisted ownership snapshot and a fake command runner.
	fresh := newAdapter(f)
	fresh.Health = nil
	result, err := fresh.Restore(context.Background(), state)
	if err != nil {
		t.Fatalf("offline restore: %v", err)
	}
	if result.State.Applied || len(result.State.Keys) != 0 {
		t.Fatalf("restore did not clear the ownership marker: %#v", result.State)
	}
	if len(result.Failed) != 0 || len(result.UserModified) != 0 {
		t.Fatalf("unexpected restore result: %#v", result)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" ||
		f.values["org.gnome.system.proxy ignore-hosts"] != "[]" {
		t.Fatalf("crash restore did not return the previous values: %#v", f.values)
	}
}

func TestApplySavesPreviousValuesBeforeWriting(t *testing.T) {
	f := gnomeSystem()
	var saved contract.DesktopProxyState
	modeAtSave := ""
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind:    KindGNOME,
		Targets: testTargets,
		Save: func(_ context.Context, state contract.DesktopProxyState) error {
			saved = state
			modeAtSave = f.values["org.gnome.system.proxy mode"]
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if modeAtSave != "'none'" {
		t.Fatalf("Save ran after a write; mode at save = %q", modeAtSave)
	}
	if !saved.Applied || saved.Environment != "gnome" || len(saved.Keys) != 8 {
		t.Fatalf("saved marker = %#v", saved)
	}
	previous := map[string]string{}
	applied := map[string]string{}
	for _, key := range saved.Keys {
		previous[key.Name] = key.Previous
		applied[key.Name] = key.Applied
	}
	if previous["org.gnome.system.proxy mode"] != "'none'" || previous["org.gnome.system.proxy.http port"] != "0" {
		t.Fatalf("saved previous values = %#v", previous)
	}
	if applied["org.gnome.system.proxy mode"] != "'manual'" || applied["org.gnome.system.proxy.http port"] != "8317" {
		t.Fatalf("saved applied values = %#v", applied)
	}
	if len(state.Keys) != 8 || state.Keys[7].Name != saved.Keys[7].Name {
		t.Fatalf("returned state does not match the saved marker: %#v", state)
	}
}

func TestApplyDoesNotWriteWhenOwnershipCannotBeSaved(t *testing.T) {
	f := gnomeSystem()
	saveErr := errors.New("state is locked")
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind:    KindGNOME,
		Targets: testTargets,
		Save: func(context.Context, contract.DesktopProxyState) error {
			return saveErr
		},
	})
	if err == nil || state.Applied {
		t.Fatal("Apply continued after a failed Save")
	}
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) > 0 && call.args[0] == "set" {
			t.Fatalf("Apply wrote a setting before ownership was saved: %v", call)
		}
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("desktop mode changed despite Save failure: %q", f.values["org.gnome.system.proxy mode"])
	}
}

func TestGNOMEApplyRollsBackOnFailure(t *testing.T) {
	f := gnomeSystem()
	f.writeErr["org.gnome.system.proxy mode"] = errors.New("gsettings failed")

	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil {
		t.Fatal("Apply succeeded despite a failed write")
	}
	if state.Applied || len(state.Keys) != 0 {
		t.Fatalf("Apply returned an ownership marker after a clean rollback: %#v", state)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" ||
		f.values["org.gnome.system.proxy.http host"] != "''" ||
		f.values["org.gnome.system.proxy.http port"] != "0" ||
		f.values["org.gnome.system.proxy.socks host"] != "''" {
		t.Fatalf("rollback did not restore previous values: %#v", f.values)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Message, "previous values were restored") {
		t.Fatalf("error = %v, want a clear rollback message", err)
	}
	if failure.Kind != contract.ExternalToolFailed || contract.ExitCodeFor[failure.Kind] != contract.ExitError {
		t.Fatalf("kind = %s exit = %d, want external_tool_failed/1", failure.Kind, contract.ExitCodeFor[failure.Kind])
	}
}

type failOnSecondWrite struct {
	*fakeSystem
	counts map[string]int
	fail   bool
}

func (f *failOnSecondWrite) Output(ctx context.Context, name string, args ...string) (string, error) {
	if f.fail && name == "gsettings" && len(args) == 4 && args[0] == "set" {
		key := args[1] + " " + args[2]
		if key == "org.gnome.system.proxy.http host" {
			f.counts[key]++
			if f.counts[key] == 2 {
				return "", errors.New("rollback write failed")
			}
		}
	}
	return f.fakeSystem.Output(ctx, name, args...)
}

func TestGNOMEIncompleteRollbackReturnsRecoverableState(t *testing.T) {
	f := gnomeSystem()
	f.writeErr["org.gnome.system.proxy mode"] = errors.New("mode write failed")
	runner := &failOnSecondWrite{fakeSystem: f, counts: map[string]int{}, fail: true}
	adapter := &DesktopAdapter{
		Runner:   runner,
		LookPath: f.LookPath,
		Health:   goodHealth(),
		Now:      func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	}

	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil {
		t.Fatal("Apply succeeded despite an incomplete rollback")
	}
	if !state.Applied || state.Environment != "gnome" || len(state.Keys) == 0 {
		t.Fatalf("Apply did not return a recoverable ownership marker: %#v", state)
	}
	if f.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("test did not leave a written key behind: %q", f.values["org.gnome.system.proxy.http host"])
	}

	runner.fail = false
	result, err := adapter.Restore(context.Background(), state)
	if err != nil {
		t.Fatalf("recovery Restore: %v", err)
	}
	if f.values["org.gnome.system.proxy.http host"] != "''" {
		t.Fatalf("recovery restore did not return the previous value: %q", f.values["org.gnome.system.proxy.http host"])
	}
	if result.State.Applied || len(result.State.Keys) != 0 {
		t.Fatalf("recovery restore left ownership behind: %#v", result.State)
	}
}

func TestDesktopApplyAlreadyAppliedStillChecksHealth(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	adapter.Health = &fakeHealth{err: errors.New("service down")}
	f.clearCalls()
	again, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err == nil || again.Applied {
		t.Fatal("already-applied Apply reported success against an unhealthy service")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Stopped {
		t.Fatalf("error = %v, want stopped", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("already-applied Apply ran desktop commands before the health check: %v", f.calls)
	}
}

func TestApplyRefusesUnrestorablePreviousValue(t *testing.T) {
	f := gnomeSystem()
	huge := "'" + strings.Repeat("x", 70*1024) + "'"
	f.values["org.gnome.system.proxy mode"] = huge
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindGNOME, Targets: testTargets})
	if err == nil || state.Applied {
		t.Fatal("Apply accepted a previous value it could not restore")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Unsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
	if f.values["org.gnome.system.proxy mode"] != huge {
		t.Fatal("Apply changed the desktop before rejecting the unrestorable value")
	}
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) > 0 && call.args[0] == "set" {
			t.Fatalf("Apply wrote a setting despite an unrestorable previous value: %v", call)
		}
	}
}

func TestRestoreRefusesControlCharacterInStoredValue(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	state.Keys[0].Previous = "'bad\nvalue'"
	f.clearCalls()
	result, err := newAdapter(f).Restore(context.Background(), state)
	if err == nil {
		t.Fatal("Restore accepted a control character in a stored value")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("error = %v, want state_corrupt", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Restore ran commands with an unsafe stored value: %v", f.calls)
	}
	if !result.State.Applied || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("Restore lost the ownership marker: %#v", result.State)
	}
}

func TestKDEApplyRestoreAndOwnership(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !state.Applied || state.Environment != "kde" || len(state.Keys) != 6 {
		t.Fatalf("state = %#v, want applied kde with 6 keys", state)
	}
	if f.values["ProxyType"] != "1" || f.values["ReversedException"] != "false" {
		t.Fatalf("ProxyType/ReversedException = %q/%q", f.values["ProxyType"], f.values["ReversedException"])
	}
	if f.values["httpProxy"] != "http://127.0.0.1 8317" ||
		f.values["httpsProxy"] != "http://127.0.0.1 8317" ||
		f.values["socksProxy"] != "socks://127.0.0.1 8318" {
		t.Fatalf("KDE proxy values = %#v", f.values)
	}
	if f.values["NoProxyFor"] != NoProxy {
		t.Fatalf("NoProxyFor = %q, want %q", f.values["NoProxyFor"], NoProxy)
	}
	last := fakeCall{}
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" {
			last = call
		}
	}
	if last.name != "kwriteconfig6" || !strings.Contains(strings.Join(last.args, " "), "ProxyType") {
		t.Fatalf("last write = %#v, want ProxyType last", last)
	}
	notified := false
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "--notify") {
			notified = true
		}
	}
	if !notified {
		t.Fatal("kwriteconfig6 was not asked to notify applications")
	}

	// The user chooses a different HTTP proxy after we applied ours.
	f.values["httpProxy"] = "http://user-proxy 9999"
	f.clearCalls()
	result, err := newAdapter(f).Restore(context.Background(), state)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !contains(result.UserModified, "kioslaverc:Proxy Settings/httpProxy") {
		t.Fatalf("user change not reported: %#v", result)
	}
	if f.values["httpProxy"] != "http://user-proxy 9999" {
		t.Fatalf("user httpProxy was overwritten: %q", f.values["httpProxy"])
	}
	if f.values["ProxyType"] != "0" || f.values["ReversedException"] != "true" || f.values["NoProxyFor"] != "intranet" {
		t.Fatalf("previous KDE values were not restored: %#v", f.values)
	}
	if _, exists := f.values["socksProxy"]; exists {
		t.Fatalf("previously absent socksProxy was not deleted: %q", f.values["socksProxy"])
	}
	deleted := false
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "--delete") && strings.Contains(strings.Join(call.args, " "), "socksProxy") {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("restore did not delete the previously absent socksProxy key")
	}
	if result.State.Applied || len(result.State.Keys) != 0 {
		t.Fatalf("state after restore = %#v, want cleared", result.State)
	}
}

func TestKDEApplyVerifiesButDoesNotWriteWhenAlreadyApplied(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.clearCalls()
	saved := false
	again, err := adapter.Apply(context.Background(), ApplyOptions{
		Kind: KindKDE, Targets: testTargets, Previous: state,
		Save: func(context.Context, contract.DesktopProxyState) error {
			saved = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if saved {
		t.Fatal("no-op Apply rewrote the ownership marker")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call.name, "kwriteconfig") && (len(call.args) == 0 || call.args[0] != "--help") {
			t.Fatalf("second Apply wrote a setting: %v", call)
		}
	}
	if again.AppliedAt != state.AppliedAt || len(again.Keys) != len(state.Keys) {
		t.Fatalf("second Apply changed the ownership marker: %#v", again)
	}
}

func TestKDEApplyReappliesOverUserEditAndSnapshotsIt(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999"
	f.clearCalls()

	var saved contract.DesktopProxyState
	savedBeforeWrite := false
	again, err := adapter.Apply(context.Background(), ApplyOptions{
		Kind: KindKDE, Targets: testTargets, Previous: state,
		Save: func(_ context.Context, marker contract.DesktopProxyState) error {
			saved = marker
			// The ownership marker must be recorded before a single key is
			// written, exactly as in a fresh apply. Count all writes, not just
			// the edited key (ReversedException is written first).
			savedBeforeWrite = true
			for _, call := range f.calls {
				if call.name == "kwriteconfig6" && !contains(call.args, "--help") {
					savedBeforeWrite = false
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("re-apply after a user edit: %v", err)
	}
	if !savedBeforeWrite {
		t.Fatal("ownership was saved after the desktop was already written")
	}
	want, err := valuesFor(KindKDE, testTargets)
	if err != nil {
		t.Fatalf("valuesFor: %v", err)
	}
	const key = "kioslaverc:Proxy Settings/httpProxy"
	if got := f.values["httpProxy"]; got != want[key] {
		t.Fatalf("httpProxy = %q after re-apply, want %q", got, want[key])
	}
	if !saved.Applied || len(saved.Keys) != len(state.Keys) || !again.Applied {
		t.Fatalf("markers = %#v / %#v, want applied with all keys", saved, again)
	}
	for _, k := range saved.Keys {
		if k.Name == key {
			if k.Previous != "http://user-proxy 9999" {
				t.Fatalf("restore point = %q, want the user's edited value", k.Previous)
			}
			if k.Applied != want[key] {
				t.Fatalf("recorded applied value = %q, want %q", k.Applied, want[key])
			}
		}
	}
	// Keys the user never edited still hold RouteLatch's value, so their
	// original restore point must be kept or restore cannot undo the proxy.
	original := make(map[string]string, len(state.Keys))
	for _, k := range state.Keys {
		original[k.Name] = k.Previous
	}
	for _, k := range saved.Keys {
		if k.Name != key && k.Previous != original[k.Name] {
			t.Fatalf("%s restore point = %q, want original %q", k.Name, k.Previous, original[k.Name])
		}
	}
	wrote := false
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "httpProxy") {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("re-apply did not write httpProxy: %v", f.calls)
	}
}

func TestKDERestoreAfterReapplyReturnsEveryKey(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999"
	again, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	result, err := adapter.Restore(context.Background(), again)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := f.values["httpProxy"]; got != "http://user-proxy 9999" {
		t.Fatalf("edited key = %q after restore, want the user's value", got)
	}
	for key, want := range map[string]string{
		"ProxyType": "0", "ReversedException": "true", "NoProxyFor": "intranet",
	} {
		if got := f.values[key]; got != want {
			t.Fatalf("%s = %q after restore, want original %q", key, got, want)
		}
	}
	for _, key := range []string{"httpsProxy", "socksProxy"} {
		if got, ok := f.values[key]; ok {
			t.Fatalf("%s = %q after restore, want it deleted", key, got)
		}
	}
	if result.State.Applied || len(result.State.Keys) != 0 {
		t.Fatalf("state after restore = %#v, want cleared", result.State)
	}
}

func TestKDEApplyReapplyRollsBackToTheUserEditOnFailure(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999"
	// The last managed key fails, so rollback must walk back over httpProxy.
	f.alwaysWriteErr["ProxyType"] = errors.New("write failed")
	f.clearCalls()
	marker, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err == nil {
		t.Fatal("re-apply reported success after a failed write")
	}
	wrote := false
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "httpProxy") {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("the re-apply path never wrote the edited key: %v", f.calls)
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Fatalf("error = %v, want a message pointing at restore", err)
	}
	if got := f.values["httpProxy"]; got != "http://user-proxy 9999" {
		t.Fatalf("rollback lost the user's value: %q", got)
	}
	if marker.Applied && len(marker.Keys) == 0 {
		t.Fatalf("incomplete rollback lost the ownership record: %#v", marker)
	}
}

func TestKDEApplyReapplyRefusesUnrestorableUserValue(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999\nbad"
	f.clearCalls()
	_, err = adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err == nil {
		t.Fatal("re-apply snapshotted a value it cannot safely restore")
	}
	// The old restore-first refusal also returned an error; the re-apply path
	// must have reached validation of the user's value specifically.
	if !strings.Contains(err.Error(), "cannot safely restore") {
		t.Fatalf("error = %v, want the value-validation failure", err)
	}
	if f.values["httpProxy"] != "http://user-proxy 9999\nbad" {
		t.Fatalf("refused re-apply changed the desktop: %q", f.values["httpProxy"])
	}
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && !contains(call.args, "--help") {
			t.Fatalf("refused re-apply wrote a setting: %v", call)
		}
	}
}

func TestKDEApplyReapplyAbortsWhenOwnershipCannotBeSaved(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999"
	f.clearCalls()
	saved := false
	_, err = adapter.Apply(context.Background(), ApplyOptions{
		Kind: KindKDE, Targets: testTargets, Previous: state,
		Save: func(context.Context, contract.DesktopProxyState) error {
			saved = true
			return errors.New("store failed")
		},
	})
	if err == nil {
		t.Fatal("re-apply ignored a failed ownership save")
	}
	// The old restore-first refusal returned an error without calling Save;
	// reaching Save is what makes this a failed re-apply.
	if !saved {
		t.Fatal("re-apply was refused before attempting to save ownership")
	}
	if f.values["httpProxy"] != "http://user-proxy 9999" {
		t.Fatalf("failed save still overwrote the desktop: %q", f.values["httpProxy"])
	}
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && !contains(call.args, "--help") {
			t.Fatalf("re-apply wrote before the save succeeded: %v", call)
		}
	}
}

func TestKDEApplyRefusesDifferentListenersWhenAlreadyApplied(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.clearCalls()
	different := Targets{HTTPListen: "127.0.0.1:9317", SOCKSListen: "127.0.0.1:9318"}
	_, err = adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: different, Previous: state})
	if err == nil {
		t.Fatal("Apply silently kept old values after a listener change")
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused Apply ran commands: %v", f.calls)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Message, "restore") {
		t.Fatalf("error = %v, want a restore-first message", err)
	}
}

func TestKDEWithoutNotifySupportStillApplies(t *testing.T) {
	f := kdeSystem()
	f.notifyHelp["kwriteconfig6"] = false
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply on kwriteconfig6 without --notify: %v", err)
	}
	if !state.Applied {
		t.Fatal("Apply did not record the setting")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call.name, "kwriteconfig") && contains(call.args, "--notify") {
			t.Fatalf("unsupported --notify was passed: %v", call)
		}
	}
}

func TestKDE5FallbackWorksWithoutNotify(t *testing.T) {
	f := kde5System()
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply via kwriteconfig5: %v", err)
	}
	if !state.Applied {
		t.Fatal("Apply did not record the setting")
	}
	usedWrite, usedRead := false, false
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" || call.name == "kreadconfig6" {
			t.Fatalf("KF5 fallback used a KF6 tool: %v", call)
		}
		if call.name == "kwriteconfig5" {
			usedWrite = true
			if contains(call.args, "--notify") {
				t.Fatalf("kwriteconfig5 was passed unsupported --notify: %v", call)
			}
		}
		if call.name == "kreadconfig5" {
			usedRead = true
		}
	}
	if !usedWrite || !usedRead {
		t.Fatalf("KF5 tools not used: write=%v read=%v", usedWrite, usedRead)
	}
	if _, err := newAdapter(f).Restore(context.Background(), state); err != nil {
		t.Fatalf("Restore via kreadconfig5/kwriteconfig5: %v", err)
	}
}

func TestUnsupportedDesktop(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key == "XDG_CURRENT_DESKTOP" {
			return "XFCE", true
		}
		return "", false
	}
	_, err := DetectDesktop(lookup)
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("DetectDesktop error = %v, want *contract.Failure", err)
	}
	if failure.Kind != contract.Unsupported {
		t.Fatalf("kind = %s, want unsupported", failure.Kind)
	}
	if contract.ExitCodeFor[failure.Kind] != contract.ExitUnsupported {
		t.Fatalf("exit = %d, want %d", contract.ExitCodeFor[failure.Kind], contract.ExitUnsupported)
	}
	if !strings.Contains(failure.Message, "GNOME") || !strings.Contains(failure.Message, "KDE") {
		t.Fatalf("unsupported message does not name the supported desktops: %q", failure.Message)
	}

	f := newFakeSystem(nil)
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: Kind("xfce"), Targets: testTargets})
	if err == nil || state.Applied {
		t.Fatal("Apply accepted an unsupported desktop")
	}
	if len(f.calls) != 0 {
		t.Fatalf("Apply ran commands for an unsupported desktop: %v", f.calls)
	}
}

func TestRestoreRefusesUnknownStateKeyWithoutRunningCommands(t *testing.T) {
	f := gnomeSystem()
	state := contract.DesktopProxyState{
		Applied:     true,
		Environment: "gnome",
		Keys: []contract.DesktopKey{{
			Name:     "org.gnome.system.proxy mode; rm -rf",
			Applied:  "'manual'",
			Previous: "'none'",
		}},
	}
	result, err := newAdapter(f).Restore(context.Background(), state)
	if err == nil {
		t.Fatal("Restore accepted an unknown state key")
	}
	if len(f.calls) != 0 {
		t.Fatalf("Restore ran commands for an unknown key: %v", f.calls)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("error = %v, want state_corrupt", err)
	}
	if !result.State.Applied || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("Restore changed the marker on an unknown key: %#v", result.State)
	}
}

func TestRestoreReportsMissingToolAndKeepsOwnership(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	delete(f.look, "gsettings")
	result, err := newAdapter(f).Restore(context.Background(), state)
	if err == nil {
		t.Fatal("Restore succeeded without gsettings")
	}
	if !result.State.Applied || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("missing tool lost the ownership record: %#v", result.State)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Unsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
	if !strings.Contains(failure.Message, "restore") {
		t.Fatalf("error does not point at the offline restore path: %q", failure.Message)
	}
}

func TestDetectDesktop(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want Kind
	}{
		{"gnome", map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}, KindGNOME},
		{"kde", map[string]string{"XDG_CURRENT_DESKTOP": "KDE"}, KindKDE},
		{"plasma session", map[string]string{"DESKTOP_SESSION": "plasma"}, KindKDE},
		{"gnome fallback", map[string]string{"GNOME_DESKTOP_SESSION_ID": "this-is-deprecated"}, KindGNOME},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectDesktop(func(key string) (string, bool) {
				v, ok := tc.vars[key]
				return v, ok
			})
			if err != nil || got != tc.want {
				t.Fatalf("DetectDesktop = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestConflictsReportsSettingsTheUserChanged(t *testing.T) {
	state := contract.DesktopProxyState{
		Applied:     true,
		Environment: string(KindKDE),
		Keys: []contract.DesktopKey{
			{Name: "kioslaverc:Proxy Settings/ProxyType", Applied: "1", Previous: "0"},
			{Name: "kioslaverc:Proxy Settings/httpProxy", Applied: "http://127.0.0.1 8317", Previous: "http://old-proxy 3128"},
		},
	}

	t.Run("matching values are not a conflict", func(t *testing.T) {
		f := kdeSystem()
		f.values["ProxyType"] = "1"
		f.values["httpProxy"] = "http://127.0.0.1 8317"
		changed, err := newAdapter(f).Conflicts(context.Background(), state)
		if err != nil || len(changed) != 0 {
			t.Fatalf("Conflicts = %v, %v; want no conflict", changed, err)
		}
		for _, call := range f.calls {
			if call.name == "kwriteconfig6" && len(call.args) > 0 && call.args[0] != "--help" {
				t.Fatalf("status inspection wrote a setting: %#v", call)
			}
		}
	})

	t.Run("user edit is reported by name", func(t *testing.T) {
		f := kdeSystem()
		f.values["ProxyType"] = "1"
		f.values["httpProxy"] = "http://127.0.0.1 9999"
		changed, err := newAdapter(f).Conflicts(context.Background(), state)
		if err != nil {
			t.Fatalf("Conflicts: %v", err)
		}
		if len(changed) != 1 || changed[0] != "kioslaverc:Proxy Settings/httpProxy" {
			t.Fatalf("Conflicts = %v, want the edited key only", changed)
		}
	})

	t.Run("read failure is an error, not a conflict", func(t *testing.T) {
		f := kdeSystem()
		f.readErr["httpProxy"] = errors.New("kreadconfig failed")
		changed, err := newAdapter(f).Conflicts(context.Background(), state)
		if err == nil || changed != nil {
			t.Fatalf("Conflicts = %v, %v; want an error and no claim", changed, err)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.ExternalToolFailed {
			t.Fatalf("error = %v, want external_tool_failed", err)
		}
	})

	t.Run("unknown recorded key is corrupt state", func(t *testing.T) {
		f := kdeSystem()
		bad := contract.DesktopProxyState{Applied: true, Environment: string(KindKDE), Keys: []contract.DesktopKey{
			{Name: "kioslaverc:Proxy Settings/other", Applied: "x"},
		}}
		changed, err := newAdapter(f).Conflicts(context.Background(), bad)
		if err == nil || changed != nil {
			t.Fatalf("Conflicts = %v, %v; want state_corrupt", changed, err)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
			t.Fatalf("error = %v, want state_corrupt", err)
		}
		if len(f.calls) != 0 {
			for _, call := range f.calls {
				if call.name == "kwriteconfig6" && !(len(call.args) == 1 && call.args[0] == "--help") {
					t.Fatalf("inspection wrote a setting for an unknown key: %#v", call)
				}
			}
		}
	})

	t.Run("conflicts follow restore order", func(t *testing.T) {
		f := kdeSystem()
		deep := contract.DesktopProxyState{Applied: true, Environment: string(KindKDE), Keys: []contract.DesktopKey{
			{Name: "kioslaverc:Proxy Settings/ProxyType", Applied: "1"},
			{Name: "kioslaverc:Proxy Settings/httpProxy", Applied: "http://127.0.0.1 8317"},
		}}
		f.values["ProxyType"] = "0"
		f.values["httpProxy"] = "http://other-proxy 1"
		changed, err := newAdapter(f).Conflicts(context.Background(), deep)
		if err != nil {
			t.Fatalf("Conflicts: %v", err)
		}
		// Restore reports user edits while walking the layouts in reverse.
		want := []string{"kioslaverc:Proxy Settings/ProxyType", "kioslaverc:Proxy Settings/httpProxy"}
		if strings.Join(changed, ",") != strings.Join(want, ",") {
			t.Fatalf("Conflicts = %v, want restore order %v", changed, want)
		}
	})

	t.Run("applied without keys is corrupt state", func(t *testing.T) {
		f := kdeSystem()
		changed, err := newAdapter(f).Conflicts(context.Background(), contract.DesktopProxyState{
			Applied: true, Environment: string(KindKDE),
		})
		if err == nil || changed != nil {
			t.Fatalf("Conflicts = %v, %v; want state_corrupt", changed, err)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
			t.Fatalf("error = %v, want state_corrupt", err)
		}
	})

	t.Run("duplicate key is corrupt state", func(t *testing.T) {
		f := kdeSystem()
		duplicate := contract.DesktopProxyState{Applied: true, Environment: string(KindKDE), Keys: []contract.DesktopKey{
			{Name: "kioslaverc:Proxy Settings/ProxyType", Applied: "1"},
			{Name: "kioslaverc:Proxy Settings/ProxyType", Applied: "1"},
		}}
		changed, err := newAdapter(f).Conflicts(context.Background(), duplicate)
		if err == nil || changed != nil {
			t.Fatalf("Conflicts = %v, %v; want state_corrupt", changed, err)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
			t.Fatalf("error = %v, want state_corrupt", err)
		}
	})

	t.Run("not applied needs no commands", func(t *testing.T) {
		f := kdeSystem()
		changed, err := newAdapter(f).Conflicts(context.Background(), contract.DesktopProxyState{})
		if err != nil || changed != nil || len(f.calls) != 0 {
			t.Fatalf("Conflicts = %v, %v, calls=%v; want a no-op", changed, err, f.calls)
		}
	})
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestClearWithoutMarkerSnapshotsAndDisablesGNOME(t *testing.T) {
	f := gnomeSystem()
	f.values["org.gnome.system.proxy mode"] = "'manual'"
	f.values["org.gnome.system.proxy.http host"] = "'old-proxy'"
	f.values["org.gnome.system.proxy.http port"] = "3128"
	f.clearCalls()

	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(result.Cleared) != 1 || result.Cleared[0] != "org.gnome.system.proxy mode" ||
		!result.StateChanged || result.AlreadyDisabled {
		t.Fatalf("clear without a marker = %+v, want one mode write", result)
	}
	if !result.State.Applied || !result.State.Cleared || result.State.Environment != "gnome" ||
		len(result.State.Keys) != 8 {
		t.Fatalf("clear state = %#v, want an applied+cleared 8-key snapshot", result.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q after clear, want none", f.values["org.gnome.system.proxy mode"])
	}
	// Clear writes only the disable key; the other settings keep their values.
	if f.values["org.gnome.system.proxy.http host"] != "'old-proxy'" ||
		f.values["org.gnome.system.proxy.http port"] != "3128" {
		t.Fatalf("clear changed a non-disable key: %#v", f.values)
	}
	sets := 0
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
			sets++
			if call.args[2] != "mode" {
				t.Fatalf("clear wrote a non-disable key: %v", call)
			}
		}
	}
	if sets != 1 {
		t.Fatalf("clear made %d writes, want exactly one (mode)", sets)
	}
	restorePoint := map[string]string{}
	for _, key := range result.State.Keys {
		restorePoint[key.Name] = key.Previous
	}
	if restorePoint["org.gnome.system.proxy mode"] != "'manual'" ||
		restorePoint["org.gnome.system.proxy.http host"] != "'old-proxy'" {
		t.Fatalf("restore point = %#v, want the pre-clear values", restorePoint)
	}

	restored, err := newAdapter(f).Restore(context.Background(), result.State)
	if err != nil {
		t.Fatalf("Restore after a marker-less clear: %v", err)
	}
	if restored.State.Applied || restored.State.Cleared || len(restored.State.Keys) != 0 {
		t.Fatalf("restore after clear left ownership behind: %#v", restored.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" ||
		f.values["org.gnome.system.proxy.http host"] != "'old-proxy'" ||
		f.values["org.gnome.system.proxy.http port"] != "3128" {
		t.Fatalf("restore did not reinstate the pre-clear values: %#v", f.values)
	}
}

func TestClearWithoutMarkerWorksWithoutHealthChecker(t *testing.T) {
	f := gnomeSystem()
	f.values["org.gnome.system.proxy mode"] = "'auto'"
	adapter := newAdapter(f)
	// Clear must work while the service is stopped, so it never consults the
	// listener health that Apply requires.
	adapter.Health = nil
	result, err := adapter.Clear(context.Background(), ClearOptions{Kind: KindGNOME})
	if err != nil {
		t.Fatalf("Clear without a health checker: %v", err)
	}
	if !result.StateChanged || f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("clear = %+v values=%#v, want mode none", result, f.values)
	}
}

func TestClearWithoutMarkerAlreadyDisabledOnlyNotices(t *testing.T) {
	f := gnomeSystem()
	f.clearCalls()
	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !result.AlreadyDisabled || result.Notice == "" || result.StateChanged || len(result.Cleared) != 0 {
		t.Fatalf("clear of an already-disabled proxy = %+v, want an idempotent notice", result)
	}
	if result.State.Applied || result.State.Cleared || len(result.State.Keys) != 0 {
		t.Fatalf("clear of an already-disabled proxy recorded ownership: %#v", result.State)
	}
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
			t.Fatalf("clear wrote an already-disabled proxy: %v", call)
		}
	}
}

func TestClearWithoutMarkerWriteFailureRollsBackAndLeavesNoMarker(t *testing.T) {
	f := gnomeSystem()
	f.values["org.gnome.system.proxy mode"] = "'manual'"
	f.writeErr["org.gnome.system.proxy mode"] = errors.New("gsettings failed")
	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME})
	if err == nil {
		t.Fatal("Clear reported success after a failed write")
	}
	if !contains(result.Failed, "org.gnome.system.proxy mode") {
		t.Fatalf("failed clear = %+v, want the mode key reported as failed", result)
	}
	if result.StateChanged || result.State.Applied || result.State.Cleared || len(result.State.Keys) != 0 {
		t.Fatalf("failed clear recorded ownership: %#v", result.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" {
		t.Fatalf("failed clear left mode = %q, want the previous manual", f.values["org.gnome.system.proxy mode"])
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.ExternalToolFailed {
		t.Fatalf("error = %v, want external_tool_failed", err)
	}
}

func TestApplyAfterMarkerlessClearKeepsTheOriginalRestorePoint(t *testing.T) {
	f := gnomeSystem()
	f.values["org.gnome.system.proxy mode"] = "'manual'"
	f.values["org.gnome.system.proxy.http host"] = "'old-proxy'"
	f.values["org.gnome.system.proxy.http port"] = "3128"
	cleared, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	f.clearCalls()

	again, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind: KindGNOME, Targets: testTargets, Previous: cleared.State,
	})
	if err != nil {
		t.Fatalf("Apply after a marker-less clear: %v", err)
	}
	if !again.Applied || again.Cleared || len(again.Keys) != 8 {
		t.Fatalf("re-apply state = %#v, want applied with all keys", again)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" ||
		f.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("re-apply did not write the proxy values: %#v", f.values)
	}
	for _, key := range again.Keys {
		switch key.Name {
		case "org.gnome.system.proxy mode":
			if key.Previous != "'manual'" {
				t.Fatalf("mode restore point = %q, want the pre-clear manual", key.Previous)
			}
		case "org.gnome.system.proxy.http host":
			if key.Previous != "'old-proxy'" {
				t.Fatalf("http host restore point = %q, want the pre-clear value", key.Previous)
			}
		}
	}
	if _, err := newAdapter(f).Restore(context.Background(), again); err != nil {
		t.Fatalf("Restore after re-apply: %v", err)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" ||
		f.values["org.gnome.system.proxy.http host"] != "'old-proxy'" {
		t.Fatalf("restore did not return to the pre-clear values: %#v", f.values)
	}
}

func TestKDEClearWithoutMarkerSnapshotsAndDisables(t *testing.T) {
	f := kdeSystem()
	f.values["ProxyType"] = "1"
	f.clearCalls()
	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindKDE})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(result.Cleared) != 1 || result.Cleared[0] != "kioslaverc:Proxy Settings/ProxyType" ||
		!result.StateChanged {
		t.Fatalf("clear without a marker = %+v, want one ProxyType write", result)
	}
	if f.values["ProxyType"] != "0" {
		t.Fatalf("ProxyType = %q after clear, want 0", f.values["ProxyType"])
	}
	if f.values["httpProxy"] != "http://old-proxy 3128" {
		t.Fatalf("clear changed a non-disable key: %#v", f.values)
	}
	sets := 0
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "--") {
			sets++
			if keyOf(call.args) != "ProxyType" {
				t.Fatalf("clear wrote a non-disable key: %v", call)
			}
		}
	}
	if sets != 1 {
		t.Fatalf("clear made %d writes, want exactly one (ProxyType)", sets)
	}
	for _, key := range result.State.Keys {
		if key.Name == "kioslaverc:Proxy Settings/ProxyType" &&
			(key.Applied != "0" || key.Previous != "1") {
			t.Fatalf("ProxyType key = %+v, want applied 0 and previous 1", key)
		}
	}

	// Apply after the clear must accept the partial (absent keys omitted)
	// snapshot and keep the pre-clear restore point.
	again, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind: KindKDE, Targets: testTargets, Previous: result.State,
	})
	if err != nil {
		t.Fatalf("Apply after a marker-less KDE clear: %v", err)
	}
	if !again.Applied || again.Cleared || len(again.Keys) != 6 {
		t.Fatalf("re-apply state = %#v, want applied with all keys", again)
	}
	if f.values["ProxyType"] != "1" {
		t.Fatalf("re-apply ProxyType = %q, want 1", f.values["ProxyType"])
	}
	if _, err := newAdapter(f).Restore(context.Background(), again); err != nil {
		t.Fatalf("Restore after re-apply: %v", err)
	}
	if f.values["ProxyType"] != "1" || f.values["httpProxy"] != "http://old-proxy 3128" {
		t.Fatalf("restore did not return to the pre-clear values: %#v", f.values)
	}
}

func TestKDEClearWithoutMarkerTreatsAnAbsentProxyTypeAsDisabled(t *testing.T) {
	f := kdeSystem()
	delete(f.values, "ProxyType")
	f.clearCalls()
	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindKDE})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !result.AlreadyDisabled || result.Notice == "" || result.StateChanged {
		t.Fatalf("clear with no ProxyType = %+v, want an idempotent notice", result)
	}
	for _, call := range f.calls {
		if call.name == "kwriteconfig6" && contains(call.args, "--") {
			t.Fatalf("clear wrote a missing disable key: %v", call)
		}
	}
}

func keyOf(args []string) string {
	for i, arg := range args {
		if arg == "--key" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestGNOMEClearAdoptsAManuallyDisabledModeAndKeepsTheRestorePoint(t *testing.T) {
	f := gnomeSystem()
	f.values["org.gnome.system.proxy mode"] = "'auto'"
	state := applyGNOME(t, f)
	// The user disabled the proxy in the desktop settings after RouteLatch
	// applied it. Clear must treat that as already disabled, adopt it, and
	// keep the original restore point.
	f.values["org.gnome.system.proxy mode"] = "'none'"
	f.clearCalls()

	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: state})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !result.AlreadyDisabled || !result.StateChanged || len(result.Cleared) != 0 ||
		!result.State.Cleared || !result.State.Applied {
		t.Fatalf("clear of a manually disabled mode = %+v, want adopted ownership", result)
	}
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
			t.Fatalf("clear wrote a mode that was already disabled: %v", call)
		}
	}
	for _, key := range result.State.Keys {
		if key.Name == "org.gnome.system.proxy mode" &&
			(key.Applied != "'none'" || key.Previous != "'auto'") {
			t.Fatalf("mode key = %+v, want applied none and previous auto", key)
		}
	}
	if _, err := newAdapter(f).Restore(context.Background(), result.State); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if f.values["org.gnome.system.proxy mode"] != "'auto'" {
		t.Fatalf("restore mode = %q, want the original auto", f.values["org.gnome.system.proxy mode"])
	}
}

func TestGNOMEClearDisablesOnlyTheModeKeyAndKeepsPreviousValues(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	f.clearCalls()

	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: state})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(result.Cleared) != 1 || result.Cleared[0] != "org.gnome.system.proxy mode" {
		t.Fatalf("cleared keys = %v, want only the mode key", result.Cleared)
	}
	if !result.State.Applied || !result.State.Cleared || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("clear state = %#v, want applied+cleared with the marker kept", result.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q after clear, want none", f.values["org.gnome.system.proxy mode"])
	}
	// Every non-disable key still holds RouteLatch's applied value: clear must
	// not silently restore the whole proxy.
	for key, want := range map[string]string{
		"org.gnome.system.proxy.http host":  "'127.0.0.1'",
		"org.gnome.system.proxy.http port":  "8317",
		"org.gnome.system.proxy.socks host": "'127.0.0.1'",
	} {
		if got := f.values[key]; got != want {
			t.Fatalf("%s = %q after clear, want %q", key, got, want)
		}
	}
	sets := 0
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
			sets++
			if call.args[2] != "mode" {
				t.Fatalf("clear wrote a non-disable key: %v", call)
			}
		}
	}
	if sets != 1 {
		t.Fatalf("clear made %d writes, want exactly one (mode)", sets)
	}

	original := map[string]contract.DesktopKey{}
	for _, key := range state.Keys {
		original[key.Name] = key
	}
	for _, key := range result.State.Keys {
		before := original[key.Name]
		if key.Name == "org.gnome.system.proxy mode" {
			if key.Applied != "'none'" || key.Previous != before.Previous {
				t.Fatalf("cleared mode key = %+v, want applied none and the original previous %q", key, before.Previous)
			}
			continue
		}
		if key != before {
			t.Fatalf("%s = %+v after clear, want unchanged %+v", key.Name, key, before)
		}
	}
}

func TestGNOMEClearLeavesAUserEditedModeAlone(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	f.values["org.gnome.system.proxy mode"] = "'auto'"
	f.clearCalls()

	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: state})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(result.Cleared) != 0 || !contains(result.UserModified, "org.gnome.system.proxy mode") {
		t.Fatalf("clear of a user-edited mode = %+v, want a user-modified report", result)
	}
	if f.values["org.gnome.system.proxy mode"] != "'auto'" {
		t.Fatalf("user mode was overwritten: %q", f.values["org.gnome.system.proxy mode"])
	}
	if result.State.Cleared || !result.State.Applied || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("clear of a user-edited mode changed the marker: %#v", result.State)
	}
	for _, call := range f.calls {
		if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
			t.Fatalf("clear wrote a user-edited key: %v", call)
		}
	}
}

func TestGNOMEClearWriteFailureReportsAndKeepsOwnership(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	f.writeErr["org.gnome.system.proxy mode"] = errors.New("gsettings failed")

	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: state})
	if err == nil {
		t.Fatal("Clear reported success after a failed write")
	}
	if len(result.Cleared) != 0 || !contains(result.Failed, "org.gnome.system.proxy mode") {
		t.Fatalf("failed clear = %+v, want the mode key reported as failed", result)
	}
	if result.State.Cleared || !result.State.Applied || len(result.State.Keys) != len(state.Keys) {
		t.Fatalf("failed clear changed the ownership marker: %#v", result.State)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" {
		t.Fatalf("failed clear changed mode: %q", f.values["org.gnome.system.proxy mode"])
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.ExternalToolFailed {
		t.Fatalf("error = %v, want external_tool_failed", err)
	}
}

func TestGNOMERestoreAfterClearReturnsTheOriginalValues(t *testing.T) {
	f := gnomeSystem()
	state := applyGNOME(t, f)
	cleared, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: state})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}

	result, err := newAdapter(f).Restore(context.Background(), cleared.State)
	if err != nil {
		t.Fatalf("Restore after Clear: %v", err)
	}
	if result.State.Applied || result.State.Cleared || len(result.State.Keys) != 0 {
		t.Fatalf("restore after clear left ownership behind: %#v", result.State)
	}
	for key, want := range map[string]string{
		"org.gnome.system.proxy mode":         "'none'",
		"org.gnome.system.proxy.http host":    "''",
		"org.gnome.system.proxy.http port":    "0",
		"org.gnome.system.proxy.socks host":   "''",
		"org.gnome.system.proxy.socks port":   "0",
		"org.gnome.system.proxy ignore-hosts": "[]",
	} {
		if got := f.values[key]; got != want {
			t.Fatalf("%s = %q after restore, want %q", key, got, want)
		}
	}
}

func TestGNOMEApplyAfterClearRetakesOwnershipAndKeepsTheOriginalRestorePoint(t *testing.T) {
	f := gnomeSystem()
	original := applyGNOME(t, f)
	cleared, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: original})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	f.clearCalls()

	var saved contract.DesktopProxyState
	savedBeforeWrite := false
	again, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind: KindGNOME, Targets: testTargets, Previous: cleared.State,
		Save: func(_ context.Context, marker contract.DesktopProxyState) error {
			saved = marker
			savedBeforeWrite = true
			for _, call := range f.calls {
				if call.name == "gsettings" && len(call.args) == 4 && call.args[0] == "set" {
					savedBeforeWrite = false
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Apply after Clear: %v", err)
	}
	if !savedBeforeWrite {
		t.Fatal("re-apply after clear saved ownership after writing the desktop")
	}
	if !again.Applied || again.Cleared || len(again.Keys) != len(original.Keys) {
		t.Fatalf("re-apply state = %#v, want applied with all keys", again)
	}
	if !saved.Applied || saved.Cleared {
		t.Fatalf("saved marker = %#v, want applied without cleared", saved)
	}
	if f.values["org.gnome.system.proxy mode"] != "'manual'" ||
		f.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("re-apply did not write the proxy values: %#v", f.values)
	}
	restorePoint := map[string]string{}
	for _, key := range original.Keys {
		restorePoint[key.Name] = key.Previous
	}
	for _, key := range again.Keys {
		if key.Previous != restorePoint[key.Name] {
			t.Fatalf("%s restore point = %q after re-apply, want the original %q", key.Name, key.Previous, restorePoint[key.Name])
		}
	}

	result, err := newAdapter(f).Restore(context.Background(), again)
	if err != nil {
		t.Fatalf("Restore after re-apply: %v", err)
	}
	if result.State.Applied || f.values["org.gnome.system.proxy mode"] != "'none'" ||
		f.values["org.gnome.system.proxy.http host"] != "''" {
		t.Fatalf("restore after re-apply did not return the original values: %#v", f.values)
	}
}

func TestGNOMEApplyAfterClearRollsBackToTheClearedStateOnFailure(t *testing.T) {
	f := gnomeSystem()
	original := applyGNOME(t, f)
	cleared, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindGNOME, State: original})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	f.clearCalls()
	f.writeErr["org.gnome.system.proxy mode"] = errors.New("write failed")

	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{
		Kind: KindGNOME, Targets: testTargets, Previous: cleared.State,
	})
	if err == nil {
		t.Fatal("re-apply after clear reported success after a failed write")
	}
	if state.Applied || len(state.Keys) != 0 {
		t.Fatalf("re-apply returned ownership after a clean rollback: %#v", state)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Message, "previous values were restored") {
		t.Fatalf("error = %v, want a clear rollback message", err)
	}
	// Rollback returns to the live cleared state, not to the pre-RouteLatch
	// restore point: the user's clear must survive a failed re-apply.
	if f.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("rollback mode = %q, want the cleared none", f.values["org.gnome.system.proxy mode"])
	}
	if f.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("rollback http host = %q, want the cleared RouteLatch value", f.values["org.gnome.system.proxy.http host"])
	}
}

func TestKDEClearDisablesOnlyProxyTypeAndKeepsTheRestorePoint(t *testing.T) {
	f := kdeSystem()
	state, err := newAdapter(f).Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.clearCalls()
	result, err := newAdapter(f).Clear(context.Background(), ClearOptions{Kind: KindKDE, State: state})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(result.Cleared) != 1 || result.Cleared[0] != "kioslaverc:Proxy Settings/ProxyType" {
		t.Fatalf("cleared keys = %v, want only ProxyType", result.Cleared)
	}
	if f.values["ProxyType"] != "0" || f.values["httpProxy"] != "http://127.0.0.1 8317" {
		t.Fatalf("KDE clear values = %#v", f.values)
	}
	for _, key := range result.State.Keys {
		switch key.Name {
		case "kioslaverc:Proxy Settings/ProxyType":
			if key.Applied != "0" {
				t.Fatalf("ProxyType applied = %q after clear, want 0", key.Applied)
			}
		default:
			for _, before := range state.Keys {
				if before.Name == key.Name && key != before {
					t.Fatalf("%s changed on clear: %+v -> %+v", key.Name, before, key)
				}
			}
		}
	}

	restored, err := newAdapter(f).Restore(context.Background(), result.State)
	if err != nil {
		t.Fatalf("Restore after KDE clear: %v", err)
	}
	if restored.State.Applied || f.values["ProxyType"] != "0" ||
		f.values["httpProxy"] != "http://old-proxy 3128" {
		t.Fatalf("KDE restore after clear = %#v values=%#v", restored.State, f.values)
	}
}
