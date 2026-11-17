package platform

import (
	"context"
	"os/exec"
	"strings"
)

// CommandRunner runs one external command with an argv slice. Implementations
// must not use a shell and must not interpret values: every argument is passed
// through untouched, so a desktop setting value cannot become a command.
type CommandRunner interface {
	Output(ctx context.Context, name string, args ...string) (string, error)
}

// LookPathFunc reports whether an executable exists. It is injectable so tests
// never consult the real PATH or run a real desktop tool.
type LookPathFunc func(name string) (string, error)

// OSLookPath is the production LookPathFunc.
func OSLookPath(name string) (string, error) { return exec.LookPath(name) }

// ExecRunner is the production CommandRunner. The child inherits the process
// environment; only the command and its argv are supplied here.
type ExecRunner struct{}

// Output implements CommandRunner.
func (ExecRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// trimOutput makes command output comparable without hiding a missing value.
func trimOutput(s string) string {
	return strings.TrimRight(s, "\r\n")
}
