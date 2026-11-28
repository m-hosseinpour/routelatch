package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"routelatch/internal/platform"
)

// fakeGNOME is a minimal in-memory gsettings model for the control-level
// desktop clear/apply path. It never touches a real desktop.
type fakeGNOME struct {
	values   map[string]string
	calls    int
	writeErr map[string]error
}

func newFakeGNOME() *fakeGNOME {
	return &fakeGNOME{
		values: map[string]string{
			"org.gnome.system.proxy.http host":    "''",
			"org.gnome.system.proxy.http port":    "0",
			"org.gnome.system.proxy.https host":   "''",
			"org.gnome.system.proxy.https port":   "0",
			"org.gnome.system.proxy.socks host":   "''",
			"org.gnome.system.proxy.socks port":   "0",
			"org.gnome.system.proxy ignore-hosts": "[]",
			"org.gnome.system.proxy mode":         "'none'",
		},
		writeErr: map[string]error{},
	}
}

func (f *fakeGNOME) LookPath(name string) (string, error) {
	if name == "gsettings" {
		return "/fake/bin/gsettings", nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *fakeGNOME) Output(_ context.Context, name string, args ...string) (string, error) {
	if name != "gsettings" || len(args) < 3 {
		return "", errors.New("unexpected command")
	}
	f.calls++
	key := args[1] + " " + args[2]
	switch args[0] {
	case "get":
		v, ok := f.values[key]
		if !ok {
			return "", errors.New("schema key not found")
		}
		return v + "\n", nil
	case "set":
		if len(args) != 4 {
			return "", errors.New("gsettings set needs schema key value")
		}
		if err := f.writeErr[key]; err != nil {
			delete(f.writeErr, key)
			return "", err
		}
		f.values[key] = args[3]
		return "", nil
	default:
		return "", errors.New("unexpected gsettings subcommand")
	}
}

func (f *fakeGNOME) adapter(health platform.HealthChecker) *platform.DesktopAdapter {
	return &platform.DesktopAdapter{
		Runner: f, LookPath: f.LookPath, Health: health,
		Now: func() time.Time { return time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC) },
	}
}

func gnomeDesktopService(t *testing.T) (*Service, *fakeGNOME) {
	t.Helper()
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.httpAddr = "127.0.0.1:8317"
	svc.socksAddr = "127.0.0.1:8318"
	svc.listenersOpen = true
	fake := newFakeGNOME()
	svc.desktopAdapter = fake.adapter
	return svc, fake
}

func applyDesktop(t *testing.T, svc *Service) DesktopData {
	t.Helper()
	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopSet})
	if err != nil {
		t.Fatalf("desktop.set: %v", err)
	}
	return raw.(DesktopData)
}

func TestDesktopClearWithoutMarkerSnapshotsDisablesAndRestores(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	fake.values["org.gnome.system.proxy mode"] = "'manual'"
	fake.values["org.gnome.system.proxy.http host"] = "'old-proxy'"
	fake.values["org.gnome.system.proxy.http port"] = "3128"

	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(DesktopData)
	if !data.Applied || !data.Cleared || !data.PreviousSet ||
		len(data.ClearedKeys) != 1 || data.ClearedKeys[0] != "org.gnome.system.proxy mode" ||
		data.Notice != "" {
		t.Fatalf("clear without a marker = %+v, want applied+cleared with a mode write", data)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q after clear, want none", fake.values["org.gnome.system.proxy mode"])
	}
	if fake.values["org.gnome.system.proxy.http host"] != "'old-proxy'" {
		t.Fatalf("clear changed a non-disable key: http host = %q", fake.values["org.gnome.system.proxy.http host"])
	}
	marker := svc.Snapshot().State.Desktop
	if !marker.Applied || !marker.Cleared || len(marker.Keys) != 8 {
		t.Fatalf("clear marker = %+v, want an applied+cleared snapshot", marker)
	}
	previous := map[string]string{}
	for _, key := range marker.Keys {
		previous[key.Name] = key.Previous
	}
	if previous["org.gnome.system.proxy mode"] != "'manual'" ||
		previous["org.gnome.system.proxy.http host"] != "'old-proxy'" {
		t.Fatalf("restore point = %#v, want the pre-clear values", previous)
	}

	raw, err = svc.Handle(context.Background(), Request{Op: OpDesktopRestore})
	if err != nil {
		t.Fatal(err)
	}
	restored := raw.(DesktopData)
	if restored.Applied || restored.Cleared {
		t.Fatalf("restore after clear = %+v, want no ownership", restored)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'manual'" ||
		fake.values["org.gnome.system.proxy.http host"] != "'old-proxy'" ||
		fake.values["org.gnome.system.proxy.http port"] != "3128" {
		t.Fatalf("restore did not reinstate the pre-clear values: %#v", fake.values)
	}
	if desktop := svc.Snapshot().State.Desktop; desktop.Applied || desktop.Cleared || len(desktop.Keys) != 0 {
		t.Fatalf("restore left ownership state behind: %+v", desktop)
	}
}

func TestDesktopClearWithoutMarkerWorksWhileStopped(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	svc.listenersOpen = false
	svc.stopped = true
	fake.values["org.gnome.system.proxy mode"] = "'manual'"

	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear})
	if err != nil {
		t.Fatalf("clear while stopped: %v", err)
	}
	data := raw.(DesktopData)
	if !data.Applied || !data.Cleared || len(data.ClearedKeys) != 1 {
		t.Fatalf("clear while stopped = %+v, want a cleared marker", data)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q after clear while stopped, want none", fake.values["org.gnome.system.proxy mode"])
	}
	if desktop := svc.Snapshot().State.Desktop; !desktop.Applied || !desktop.Cleared {
		t.Fatalf("clear while stopped did not persist its marker: %+v", desktop)
	}
}

func TestDesktopClearWithoutMarkerWhenAlreadyDisabledOnlyNotices(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	fake.calls = 0
	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(DesktopData)
	if data.Notice == "" || data.Applied || data.Cleared || data.PreviousSet || len(data.ClearedKeys) != 0 {
		t.Fatalf("clear of an already-disabled proxy = %+v, want an idempotent notice", data)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q, want the unchanged none", fake.values["org.gnome.system.proxy mode"])
	}
	if desktop := svc.Snapshot().State.Desktop; desktop.Applied || desktop.Cleared || len(desktop.Keys) != 0 {
		t.Fatalf("clear of an already-disabled proxy changed state: %+v", desktop)
	}
}

func TestDesktopClearWithoutMarkerWriteFailureLeavesStateHonest(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	fake.values["org.gnome.system.proxy mode"] = "'manual'"
	fake.writeErr["org.gnome.system.proxy mode"] = errors.New("gsettings failed")

	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear}); err == nil {
		t.Fatal("clear reported success after a failed write")
	}
	if fake.values["org.gnome.system.proxy mode"] != "'manual'" {
		t.Fatalf("mode = %q after a failed clear, want the previous manual", fake.values["org.gnome.system.proxy mode"])
	}
	if desktop := svc.Snapshot().State.Desktop; desktop.Applied || desktop.Cleared || len(desktop.Keys) != 0 {
		t.Fatalf("failed clear recorded ownership: %+v", desktop)
	}
}

func TestDesktopClearDisablesOnlyTheDisableKeyAndKeepsPreviousValues(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	applied := applyDesktop(t, svc)
	before := svc.Snapshot().State.Desktop
	if !applied.Applied || applied.Cleared {
		t.Fatalf("apply = %+v", applied)
	}

	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear})
	if err != nil {
		t.Fatal(err)
	}
	cleared := raw.(DesktopData)
	if !cleared.Applied || !cleared.Cleared || len(cleared.ClearedKeys) != 1 ||
		cleared.ClearedKeys[0] != "org.gnome.system.proxy mode" {
		t.Fatalf("clear = %+v, want applied+cleared with only the mode key", cleared)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'none'" {
		t.Fatalf("mode = %q after clear, want none", fake.values["org.gnome.system.proxy mode"])
	}
	if fake.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("clear changed a non-disable key: http host = %q", fake.values["org.gnome.system.proxy.http host"])
	}

	after := svc.Snapshot().State.Desktop
	if !after.Cleared || !after.Applied || len(after.Keys) != len(before.Keys) {
		t.Fatalf("clear state = %+v, want cleared with the ownership marker kept", after)
	}
	previous := map[string]string{}
	for _, key := range before.Keys {
		previous[key.Name] = key.Previous
	}
	for _, key := range after.Keys {
		if key.Name == "org.gnome.system.proxy mode" {
			if key.Applied != "'none'" {
				t.Fatalf("cleared mode applied value = %q, want none", key.Applied)
			}
			continue
		}
		if key.Previous != previous[key.Name] {
			t.Fatalf("%s previous = %q, want the original %q", key.Name, key.Previous, previous[key.Name])
		}
	}
}

func TestDesktopRestoreAfterClearReturnsOriginalValues(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	applyDesktop(t, svc)
	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear}); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Handle(context.Background(), Request{Op: OpDesktopRestore})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(DesktopData)
	if data.Applied || data.Cleared {
		t.Fatalf("restore after clear = %+v, want no ownership", data)
	}
	for key, want := range map[string]string{
		"org.gnome.system.proxy mode":         "'none'",
		"org.gnome.system.proxy.http host":    "''",
		"org.gnome.system.proxy.http port":    "0",
		"org.gnome.system.proxy.socks host":   "''",
		"org.gnome.system.proxy ignore-hosts": "[]",
	} {
		if got := fake.values[key]; got != want {
			t.Fatalf("%s = %q after restore, want %q", key, got, want)
		}
	}
	if desktop := svc.Snapshot().State.Desktop; desktop.Applied || desktop.Cleared || len(desktop.Keys) != 0 {
		t.Fatalf("restore left ownership state behind: %+v", desktop)
	}
}

func TestDesktopApplyAfterClearRetakesOwnershipAndKeepsRestorePoint(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	applyDesktop(t, svc)
	original := svc.Snapshot().State.Desktop
	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear}); err != nil {
		t.Fatal(err)
	}

	reapplied := applyDesktop(t, svc)
	if !reapplied.Applied || reapplied.Cleared {
		t.Fatalf("re-apply after clear = %+v, want applied without cleared", reapplied)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'manual'" ||
		fake.values["org.gnome.system.proxy.http host"] != "'127.0.0.1'" {
		t.Fatalf("re-apply did not write the proxy values: %+v", fake.values)
	}
	marker := svc.Snapshot().State.Desktop
	if !marker.Applied || marker.Cleared || len(marker.Keys) != len(original.Keys) {
		t.Fatalf("re-apply marker = %+v", marker)
	}
	restorePoint := map[string]string{}
	for _, key := range original.Keys {
		restorePoint[key.Name] = key.Previous
	}
	for _, key := range marker.Keys {
		if key.Previous != restorePoint[key.Name] {
			t.Fatalf("%s restore point = %q after re-apply, want the original %q", key.Name, key.Previous, restorePoint[key.Name])
		}
	}

	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopRestore}); err != nil {
		t.Fatal(err)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'none'" ||
		fake.values["org.gnome.system.proxy.http host"] != "''" {
		t.Fatalf("restore after re-apply did not return the original values: %+v", fake.values)
	}
}

func TestDesktopApplyAfterMarkerlessClearKeepsTheOriginalRestorePoint(t *testing.T) {
	svc, fake := gnomeDesktopService(t)
	fake.values["org.gnome.system.proxy mode"] = "'auto'"
	fake.values["org.gnome.system.proxy.http host"] = "'old-proxy'"
	fake.values["org.gnome.system.proxy.http port"] = "3128"
	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopClear}); err != nil {
		t.Fatal(err)
	}

	reapplied := applyDesktop(t, svc)
	if !reapplied.Applied || reapplied.Cleared {
		t.Fatalf("re-apply after a marker-less clear = %+v, want applied without cleared", reapplied)
	}
	marker := svc.Snapshot().State.Desktop
	restorePoint := map[string]string{}
	for _, key := range marker.Keys {
		restorePoint[key.Name] = key.Previous
	}
	if restorePoint["org.gnome.system.proxy mode"] != "'auto'" ||
		restorePoint["org.gnome.system.proxy.http host"] != "'old-proxy'" ||
		restorePoint["org.gnome.system.proxy.http port"] != "3128" {
		t.Fatalf("restore point after re-apply = %#v, want the pre-clear values", restorePoint)
	}

	if _, err := svc.Handle(context.Background(), Request{Op: OpDesktopRestore}); err != nil {
		t.Fatal(err)
	}
	if fake.values["org.gnome.system.proxy mode"] != "'auto'" ||
		fake.values["org.gnome.system.proxy.http host"] != "'old-proxy'" ||
		fake.values["org.gnome.system.proxy.http port"] != "3128" {
		t.Fatalf("restore after re-apply did not return the pre-clear values: %+v", fake.values)
	}
}
