package systemd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRunner struct {
	out string
	err error
}

func (f fakeRunner) Output(context.Context, string, ...string) (string, error) {
	return f.out, f.err
}

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigBaseUsesManagerEnvironment(t *testing.T) {
	cfg := Config{Runner: fakeRunner{out: "XDG_CONFIG_HOME=/tmp/manager-config\nHOME=/home/user\n"}}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err != nil || !confirmed || base != "/tmp/manager-config" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestConfigBaseUnquotesManagerValues(t *testing.T) {
	cfg := Config{Runner: fakeRunner{out: "XDG_CONFIG_HOME=\"/tmp/manager config\"\nHOME=/home/user\n"}}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err != nil || !confirmed || base != "/tmp/manager config" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestConfigBaseUsesManagerHomeWhenXDGIsAbsent(t *testing.T) {
	cfg := Config{Runner: fakeRunner{out: "HOME=/home/manager-user\n"}}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err != nil || !confirmed || base != "/home/manager-user/.config" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestConfigBaseIgnoresRelativeXDG(t *testing.T) {
	cfg := Config{Runner: fakeRunner{out: "XDG_CONFIG_HOME=relative/config\nHOME=/home/manager-user\n"}}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err != nil || !confirmed || base != "/home/manager-user/.config" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestConfigBaseFallsBackToShellButReportsFailure(t *testing.T) {
	cfg := Config{
		Runner: fakeRunner{err: errors.New("Failed to connect to bus")},
		Getenv: env(map[string]string{"XDG_CONFIG_HOME": "/tmp/shell-config", "HOME": "/home/shell"}),
	}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err == nil || confirmed || base != "/tmp/shell-config" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestConfigBaseWithoutHomeFails(t *testing.T) {
	cfg := Config{Runner: fakeRunner{err: errors.New("no manager")}, Getenv: env(map[string]string{})}
	base, confirmed, err := cfg.ConfigBase(context.Background())
	if err == nil || confirmed || base != "" {
		t.Fatalf("base=%q confirmed=%v err=%v", base, confirmed, err)
	}
}

func TestUnitPath(t *testing.T) {
	want := filepath.Join("/tmp/base", "systemd", "user", UnitName)
	if got := UnitPath("/tmp/base"); got != want {
		t.Fatalf("UnitPath = %q, want %q", got, want)
	}
}

func TestReadUnitStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, UnitName)

	missing, err := ReadUnit(path)
	if err != nil || missing.Exists || missing.Managed || !missing.Safe {
		t.Fatalf("missing = %+v, %v", missing, err)
	}

	unit := "[Unit]\n" + OwnershipMarker + "\n[Service]\nExecStart=/usr/bin/routelatch run --headless\n"
	if err := os.WriteFile(path, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	managed, err := ReadUnit(path)
	if err != nil || !managed.Exists || !managed.Managed || !managed.Safe {
		t.Fatalf("managed = %+v, %v", managed, err)
	}

	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign, err := ReadUnit(path)
	if err != nil || !foreign.Exists || foreign.Managed || !foreign.Safe {
		t.Fatalf("foreign = %+v, %v", foreign, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.service")
	if err := os.WriteFile(target, []byte(OwnershipMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	link, err := ReadUnit(path)
	if err != nil || !link.Exists || link.Managed || link.Safe {
		t.Fatalf("symlink = %+v, %v", link, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	nonRegular, err := ReadUnit(path)
	if err != nil || !nonRegular.Exists || nonRegular.Managed || nonRegular.Safe {
		t.Fatalf("directory = %+v, %v", nonRegular, err)
	}
}

func TestReadUnitRejectsForeignOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, UnitName)
	if err := os.WriteFile(path, []byte(OwnershipMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := currentUID
	currentUID = func() int { return os.Geteuid() + 1 }
	defer func() { currentUID = old }()
	unit, err := ReadUnit(path)
	if err != nil || !unit.Exists || unit.Managed || unit.Safe {
		t.Fatalf("foreign owner = %+v, %v", unit, err)
	}
}
