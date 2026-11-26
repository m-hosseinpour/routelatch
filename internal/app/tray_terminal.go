package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"routelatch/internal/trayui"
)

// trayTerminalAllowlist is the fixed §7.3 terminal set. Each entry supplies
// only the argument prefix the terminal needs before the RouteLatch command;
// the command itself is always the current executable, the optional absolute
// data directory and `menu`. There is deliberately no $TERMINAL lookup and no
// shell: a name outside this list can never be executed.
var trayTerminalAllowlist = []struct {
	name string
	lead []string
}{
	{"konsole", []string{"-e"}},
	{"gnome-terminal", []string{"--"}},
	{"xterm", []string{"-e"}},
}

// trayTerminal launches the CLI menu in one allowlisted terminal. lookPath and
// start are seams for tests; production uses exec.LookPath and a detached,
// non-waiting start.
type trayTerminal struct {
	executable string
	dataDir    string
	lookPath   func(string) (string, error)
	start      func(*exec.Cmd) error
}

// newTrayTerminal builds the terminal-menu adapter for the tray child. An empty
// data directory means the default instance; a missing executable is reported
// by Open instead of guessing a path.
func newTrayTerminal(dataDir string) trayui.Terminal {
	executable, _ := os.Executable()
	if dataDir != "" {
		// The contract's manual invocation carries an absolute data directory;
		// a relative environment value would otherwise point the menu at a
		// different instance than the running tray.
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
	}
	return &trayTerminal{
		executable: executable,
		dataDir:    dataDir,
		lookPath:   exec.LookPath,
		start:      startDetachedTerminal,
	}
}

// Open tries the allowlist in order and starts the first terminal found. A
// terminal that exists but cannot start is a failure, not a reason to try a
// different emulator behind the user's back.
func (t *trayTerminal) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := t.manualArgs()
	if len(command) == 0 {
		return trayui.ErrTerminalUnavailable
	}
	for _, terminal := range trayTerminalAllowlist {
		path, err := t.lookPath(terminal.name)
		if err != nil {
			continue
		}
		args := make([]string, 0, len(terminal.lead)+len(command))
		args = append(args, terminal.lead...)
		args = append(args, command...)
		cmd := exec.Command(path, args...)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := t.start(cmd); err != nil {
			return fmt.Errorf("%w: %v", trayui.ErrTerminalUnavailable, err)
		}
		return nil
	}
	return trayui.ErrNoTerminal
}

// ManualCommand is the exact argv shown when no terminal is available. It
// contains only the executable, the data directory and the fixed `menu`
// subcommand; menu labels, destinations and URLs are never part of it.
func (t *trayTerminal) ManualCommand() string {
	return strings.Join(t.manualArgs(), " ")
}

func (t *trayTerminal) manualArgs() []string {
	if t.executable == "" {
		return nil
	}
	args := make([]string, 0, 4)
	args = append(args, t.executable)
	if t.dataDir != "" {
		args = append(args, "--data-dir", t.dataDir)
	}
	return append(args, "menu")
}

// startDetachedTerminal starts the emulator in its own session so closing the
// tray cannot close the terminal, and reaps it without waiting for it. Pipes
// are deliberately left nil: the tray's inherited supervisor descriptors are
// marked close-on-exec separately, so the terminal never receives them.
func startDetachedTerminal(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
