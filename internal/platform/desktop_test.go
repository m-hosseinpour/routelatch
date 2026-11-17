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
	again, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
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

func TestKDEApplyRefusesWhenUserChangedAnAppliedKey(t *testing.T) {
	f := kdeSystem()
	adapter := newAdapter(f)
	state, err := adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.values["httpProxy"] = "http://user-proxy 9999"
	_, err = adapter.Apply(context.Background(), ApplyOptions{Kind: KindKDE, Targets: testTargets, Previous: state})
	if err == nil {
		t.Fatal("Apply reported success over a user-edited setting")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Message, "restore") {
		t.Fatalf("error = %v, want a restore-first message", err)
	}
	if f.values["httpProxy"] != "http://user-proxy 9999" {
		t.Fatalf("Apply overwrote the user's value: %q", f.values["httpProxy"])
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

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
