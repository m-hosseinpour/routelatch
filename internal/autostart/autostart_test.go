package autostart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/systemd"
)

const testExecutable = "/opt/Route Latch/bin/routelatch"

func okProbes() (func() error, func() error) {
	return func() error { return nil }, func() error { return nil }
}

func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tray, pidfd := okProbes()
	base := t.TempDir()
	return &Manager{
		ConfigBase: base,
		Executable: testExecutable,
		ProbeTray:  tray,
		ProbePIDFD: pidfd,
		ServiceUnit: func(context.Context) (systemd.Unit, error) {
			return systemd.Unit{}, nil
		},
	}, filepath.Join(base, "autostart", Filename)
}

func expectCode(t *testing.T, err error, want contract.ExitCode) *control.OpError {
	t.Helper()
	var opErr *control.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error = %v, want OpError", err)
	}
	if opErr.Exit != want {
		t.Fatalf("exit = %d, want %d (%v)", opErr.Exit, want, err)
	}
	return opErr
}

func reason(opErr *control.OpError) string {
	value, _ := opErr.Detail["reason"].(string)
	return value
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDesktopEntryExactContent(t *testing.T) {
	raw, err := DesktopEntry(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	want := `[Desktop Entry]
Type=Application
Name=RouteLatch
Comment=Start the RouteLatch proxy app and its tray at login
Exec="/opt/Route Latch/bin/routelatch" run
Terminal=false
NoDisplay=true
X-RouteLatch-Autostart=true
`
	if string(raw) != want {
		t.Fatalf("entry =\n%s\nwant\n%s", raw, want)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("entry has no trailing newline")
	}
}

func TestDesktopEntryQuoting(t *testing.T) {
	tick := "`"
	cases := []struct{ in, want string }{
		{"/usr/bin/routelatch", `"/usr/bin/routelatch"`},
		{"/a b/c", `"/a b/c"`},
		{"/a\"b", `"/a\"b"`},
		{"/a" + tick + "b", "\"/a\\" + tick + "b\""},
		{"/a$b", `"/a\$b"`},
		{"/a\\b", `"/a\\\\b"`},
		{"/a%b/c", `"/a%%b/c"`},
		{"/a;b&c", `"/a;b&c"`},
	}
	for _, tc := range cases {
		got, err := quoteExecArgument(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("quote(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := quoteExecArgument("/a\nb"); err == nil {
		t.Fatal("control character was accepted")
	}
}

func TestDesktopEntryRequiresAbsoluteExecutable(t *testing.T) {
	for _, exe := range []string{"", "relative/path"} {
		if _, err := DesktopEntry(exe); err == nil {
			t.Fatalf("DesktopEntry(%q) accepted a non-absolute path", exe)
		}
	}
}

func TestEnableCreatesManagedEntry(t *testing.T) {
	m, path := testManager(t)
	st, err := m.Enable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.Managed || !st.TrayAvailable || !st.PIDFDAvailable || st.Path != path {
		t.Fatalf("status = %+v", st)
	}
	raw := readFile(t, path)
	if !strings.Contains(raw, `Exec="`+testExecutable+`" run`) {
		t.Fatalf("entry = %s", raw)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("file mode = %04o, want 0644", info.Mode().Perm())
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %04o, want 0700", dir.Mode().Perm())
	}
}

func TestEnableIsIdempotent(t *testing.T) {
	m, path := testManager(t)
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	st, err := m.Enable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.Managed {
		t.Fatalf("status = %+v", st)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("idempotent enable replaced the file")
	}
	if !after.ModTime().Equal(past) {
		t.Fatalf("idempotent enable rewrote the file (mtime %v)", after.ModTime())
	}
	if readFile(t, path) != content {
		t.Fatal("idempotent enable changed the content")
	}
}

func TestEnableUpdatesStaleManagedEntry(t *testing.T) {
	m, path := testManager(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := "[Desktop Entry]\nType=Application\nExec=\"/old/routelatch\" run\n" + MarkerKey + "\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, `Exec="`+testExecutable+`" run`) {
		t.Fatalf("stale entry was not updated:\n%s", got)
	}
}

func TestEnableNeverOverwritesForeignEntry(t *testing.T) {
	m, path := testManager(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Desktop Entry]\nExec=/usr/bin/other\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Lstat(path)
	_, err := m.Enable(context.Background())
	expectCode(t, err, contract.ExitConflict)
	after, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !os.SameFile(before, after) || readFile(t, path) != foreign {
		t.Fatal("foreign entry was overwritten")
	}
}

func TestEnableRejectsSymlinkAndNonRegular(t *testing.T) {
	m, path := testManager(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := m.Enable(context.Background())
	expectCode(t, err, contract.ExitConflict)
	if readFile(t, target) != "keep me" {
		t.Fatal("symlink target was modified")
	}
	if _, err := m.Disable(context.Background()); err == nil {
		t.Fatal("disable accepted a symlink")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable(context.Background()); err == nil {
		t.Fatal("enable accepted a non-regular entry")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
}

func TestEnableRejectsForeignOwner(t *testing.T) {
	m, path := testManager(t)
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Euid = func() int { return os.Geteuid() + 1 }
	_, err := m.Enable(context.Background())
	expectCode(t, err, contract.ExitConflict)
	if _, statErr := os.Lstat(path); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestEnableRequiresTrayBeforeWriting(t *testing.T) {
	m, path := testManager(t)
	m.ProbeTray = func() error { return errors.New("no StatusNotifierWatcher") }
	_, err := m.Enable(context.Background())
	opErr := expectCode(t, err, contract.ExitUnsupported)
	if reason(opErr) != "tray_unavailable" {
		t.Fatalf("reason = %q", reason(opErr))
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("enable wrote an entry without a tray")
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("enable created the autostart directory without a tray")
	}
}

func TestEnableRequiresTrayBeforeDeclaringIdempotent(t *testing.T) {
	m, path := testManager(t)
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Lstat(path)
	m.ProbeTray = func() error { return errors.New("tray host disappeared") }
	_, err := m.Enable(context.Background())
	opErr := expectCode(t, err, contract.ExitUnsupported)
	if reason(opErr) != "tray_unavailable" {
		t.Fatalf("reason = %q", reason(opErr))
	}
	after, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(before, after) {
		t.Fatal("entry changed while the tray was unavailable")
	}
}

func TestEnableRequiresPIDFDBeforeWritingAndIdempotent(t *testing.T) {
	for _, wantErr := range []error{unix.ENOSYS, unix.EPERM} {
		m, path := testManager(t)
		m.ProbePIDFD = func() error { return wantErr }
		_, err := m.Enable(context.Background())
		opErr := expectCode(t, err, contract.ExitUnsupported)
		if reason(opErr) != "pidfd_unavailable" {
			t.Fatalf("reason = %q", reason(opErr))
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%v: entry written without pidfd", wantErr)
		}

		m.ProbeTray = func() error { return nil }
		m.ProbePIDFD = func() error { return nil }
		if _, err := m.Enable(context.Background()); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Lstat(path)
		m.ProbePIDFD = func() error { return wantErr }
		_, err = m.Enable(context.Background())
		opErr = expectCode(t, err, contract.ExitUnsupported)
		if reason(opErr) != "pidfd_unavailable" {
			t.Fatalf("reason = %q", reason(opErr))
		}
		after, statErr := os.Lstat(path)
		if statErr != nil || !os.SameFile(before, after) {
			t.Fatalf("%v: entry changed while pidfd was unavailable", wantErr)
		}
	}
}

func TestTrayFailureTakesPrecedence(t *testing.T) {
	m, _ := testManager(t)
	m.ProbeTray = func() error { return errors.New("no host") }
	m.ProbePIDFD = func() error { return errors.New("ENOSYS") }
	_, err := m.Enable(context.Background())
	opErr := expectCode(t, err, contract.ExitUnsupported)
	if reason(opErr) != "tray_unavailable" {
		t.Fatalf("reason = %q", reason(opErr))
	}
}

func TestManagedServiceConflict(t *testing.T) {
	m, path := testManager(t)
	m.ServiceUnit = func(context.Context) (systemd.Unit, error) {
		return systemd.Unit{Exists: true, Managed: true, Safe: true}, nil
	}
	st, err := m.Status(context.Background())
	if err != nil || st.Conflict != ConflictService {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := m.Enable(context.Background()); err == nil {
		t.Fatal("enable accepted a managed service unit")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("enable wrote an entry next to a managed service")
	}
	if _, err := m.Disable(context.Background()); err == nil {
		t.Fatal("disable ignored a managed service unit")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
}

func TestForeignServiceUnitIsNotAConflict(t *testing.T) {
	m, _ := testManager(t)
	m.ServiceUnit = func(context.Context) (systemd.Unit, error) {
		return systemd.Unit{Exists: true, Managed: false, Safe: true}, nil
	}
	st, err := m.Status(context.Background())
	if err != nil || st.Conflict != "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatalf("enable with a foreign unit: %v", err)
	}
}

func TestServiceUnitProbeFailureRefusesSetup(t *testing.T) {
	m, _ := testManager(t)
	m.ServiceUnit = func(context.Context) (systemd.Unit, error) { return systemd.Unit{}, errors.New("cannot read unit") }
	st, err := m.Status(context.Background())
	if err != nil || st.Conflict != "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if _, err := m.Enable(context.Background()); err == nil {
		t.Fatal("enable ignored an unreadable service unit")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
	if _, err := m.Disable(context.Background()); err == nil {
		t.Fatal("disable ignored an unreadable service unit")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
}

func TestDisableRemovesOnlyManagedEntry(t *testing.T) {
	m, path := testManager(t)
	st, err := m.Disable(context.Background())
	if err != nil || st.Enabled || st.Managed {
		t.Fatalf("disable of absent entry = %+v, %v", st, err)
	}
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err = m.Disable(context.Background())
	if err != nil || st.Enabled || st.Managed {
		t.Fatalf("disable = %+v, %v", st, err)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("managed entry was not removed")
	}
	if st, err := m.Disable(context.Background()); err != nil || st.Enabled {
		t.Fatalf("second disable = %+v, %v", st, err)
	}
}

func TestDisableNeverRemovesForeignEntry(t *testing.T) {
	m, path := testManager(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Desktop Entry]\nExec=/usr/bin/other\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable(context.Background()); err == nil {
		t.Fatal("disable removed a foreign entry")
	} else {
		expectCode(t, err, contract.ExitConflict)
	}
	if readFile(t, path) != foreign {
		t.Fatal("foreign entry changed")
	}
}

func TestStatusIsReadOnly(t *testing.T) {
	m, path := testManager(t)
	failingPIDFD := func() error { return errors.New("EPERM") }
	m.ProbePIDFD = failingPIDFD
	st, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Managed || !st.TrayAvailable || st.PIDFDAvailable {
		t.Fatalf("status = %+v", st)
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("status created the autostart directory")
	}

	m.ProbePIDFD = func() error { return nil }
	if _, err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(context.Background())
	if err != nil || !st.Enabled || !st.Managed {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestStatusReportsUnsafePathAsUnmanaged(t *testing.T) {
	m, path := testManager(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/target", path); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status(context.Background())
	if err != nil || !st.Enabled || st.Managed {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestConfigBase(t *testing.T) {
	if got, err := ConfigBase(func(string) string { return "/tmp/xdg-config" }, ""); err != nil || got != "/tmp/xdg-config" {
		t.Fatalf("xdg base = %q, %v", got, err)
	}
	if got, err := ConfigBase(func(string) string { return "relative/config" }, "/home/u"); err != nil || got != "/home/u/.config" {
		t.Fatalf("fallback base = %q, %v", got, err)
	}
	if _, err := ConfigBase(func(string) string { return "" }, ""); err == nil {
		t.Fatal("missing HOME and XDG_CONFIG_HOME was accepted")
	}
}

func TestManagerValidatesConfigBase(t *testing.T) {
	m := &Manager{ConfigBase: "relative", Executable: testExecutable}
	if _, err := m.Status(context.Background()); err == nil {
		t.Fatal("status accepted a relative config base")
	}
	if _, err := m.Enable(context.Background()); err == nil {
		t.Fatal("enable accepted a relative config base")
	}
}
