package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/trayui"
)

// fakeTerminalScript writes one argv entry per line and exits. It is the
// emulator stand-in; RouteLatch itself never passes a shell or user text.
func writeFakeTerminal(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForFile(t *testing.T, path string, wantLines int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(raw), "\n") {
			lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			if len(lines) >= wantLines {
				return lines
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

func TestTrayTerminalAllowlistUsesExplicitArgv(t *testing.T) {
	cases := []struct {
		terminal string
		lead     []string
	}{
		{"konsole", []string{"-e"}},
		{"gnome-terminal", []string{"--"}},
		{"xterm", []string{"-e"}},
	}
	for _, tc := range cases {
		t.Run(tc.terminal, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "argv")
			writeFakeTerminal(t, bin, tc.terminal,
				"#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+record+"'\n")
			t.Setenv("PATH", bin)
			// A hostile $TERMINAL, a shell metacharacter in a label and a URL
			// must never reach argv; only the fixed four/five arguments do.
			t.Setenv("TERMINAL", "/bin/sh -c 'id'")

			term := &trayTerminal{
				executable: "/opt/RouteLatch/routelatch",
				dataDir:    "/data/rl",
				lookPath:   exec.LookPath,
				start:      startDetachedTerminal,
			}
			if err := term.Open(context.Background()); err != nil {
				t.Fatalf("Open: %v", err)
			}
			want := append(append([]string{}, tc.lead...),
				"/opt/RouteLatch/routelatch", "--data-dir", "/data/rl", "menu")
			got := waitForFile(t, record, len(want))
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestTrayTerminalOmitsEmptyDataDir(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv")
	writeFakeTerminal(t, bin, "xterm", "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+record+"'\n")
	t.Setenv("PATH", bin)

	term := &trayTerminal{executable: "/opt/routelatch", lookPath: exec.LookPath, start: startDetachedTerminal}
	if err := term.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []string{"-e", "/opt/routelatch", "menu"}
	got := waitForFile(t, record, len(want))
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if got := term.ManualCommand(); got != "/opt/routelatch menu" {
		t.Fatalf("manual command = %q", got)
	}
}

func TestTrayTerminalMissingIsExplicit(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	// A hostile or merely set $TERMINAL is not an allowlist entry.
	t.Setenv("TERMINAL", "/bin/sh")

	term := &trayTerminal{executable: "/opt/routelatch", dataDir: "/data/rl", lookPath: exec.LookPath, start: startDetachedTerminal}
	err := term.Open(context.Background())
	if !errors.Is(err, trayui.ErrNoTerminal) {
		t.Fatalf("Open = %v, want ErrNoTerminal", err)
	}
	if errors.Is(err, trayui.ErrTerminalUnavailable) {
		t.Fatalf("missing terminal was reported as a launch failure: %v", err)
	}
	if got, want := term.ManualCommand(), "/opt/routelatch --data-dir /data/rl menu"; got != want {
		t.Fatalf("manual command = %q, want %q", got, want)
	}
}

func TestTrayTerminalStartFailureIsReported(t *testing.T) {
	bin := t.TempDir()
	// LookPath accepts the file, but exec cannot start it: a clear launch
	// failure, not a silent fallback to another terminal.
	writeFakeTerminal(t, bin, "konsole", "#!/nonexistent/routelatch-test-interpreter\n")
	t.Setenv("PATH", bin)

	term := &trayTerminal{executable: "/opt/routelatch", lookPath: exec.LookPath, start: startDetachedTerminal}
	err := term.Open(context.Background())
	if !errors.Is(err, trayui.ErrTerminalUnavailable) {
		t.Fatalf("Open = %v, want ErrTerminalUnavailable", err)
	}
	if errors.Is(err, trayui.ErrNoTerminal) {
		t.Fatalf("launch failure was reported as a missing terminal: %v", err)
	}
}

func TestTrayTerminalStartsDetachedWithoutPipes(t *testing.T) {
	bin := t.TempDir()
	writeFakeTerminal(t, bin, "konsole", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)

	captured := make(chan *exec.Cmd, 1)
	term := &trayTerminal{
		executable: "/opt/routelatch",
		dataDir:    "/data/rl",
		lookPath:   exec.LookPath,
		start: func(cmd *exec.Cmd) error {
			err := startDetachedTerminal(cmd)
			captured <- cmd
			return err
		},
	}
	if err := term.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cmd := <-captured
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("terminal is not started in its own session")
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("terminal inherited tray pipes")
	}
}

func TestSealInheritedDescriptorsMarksCloseOnExec(t *testing.T) {
	fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC != 0 {
		t.Fatal("test descriptor already had FD_CLOEXEC")
	}
	sealInheritedDescriptors(TrayOptions{ControlFD: fd})
	flags, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("inherited descriptor survived exec after sealing")
	}
}
