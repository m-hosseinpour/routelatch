// Package systemd resolves the systemd user manager's configuration base and
// inspects the opt-in RouteLatch user unit. R13-C needs only the base and the
// ownership check (to refuse running an autostart entry next to a managed
// service); R13-D builds the install/uninstall flow on the same helpers.
//
// The base comes from `systemctl --user show-environment`, not from the shell:
// a unit written into the shell's XDG_CONFIG_HOME may be invisible to a user
// manager started with a different base (docs/contracts.md §7.5).
package systemd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"routelatch/internal/platform"
)

const (
	// UnitName is the only unit name RouteLatch manages.
	UnitName = "routelatch.service"
	// OwnershipMarker is the first-line comment `service install` writes.
	OwnershipMarker = "# Managed by RouteLatch service install"

	maxUnitBytes = 64 << 10
)

// currentUID is a seam so managed-path tests can exercise a foreign owner
// without root. Production uses the effective uid.
var currentUID = os.Geteuid

// Config resolves the user manager base. Runner and Getenv are test seams;
// production uses the real systemctl and the process environment.
type Config struct {
	Runner platform.CommandRunner
	Getenv func(string) string
	Home   string
}

// ConfigBase returns the XDG config base of the user manager and whether the
// manager itself confirmed it. On a failed systemctl call the shell base is
// still returned with confirmed=false (best-effort detection for status), and
// the error is reported so `service install` can refuse to write a unit the
// manager would not see.
func (c Config) ConfigBase(ctx context.Context) (base string, confirmed bool, err error) {
	runner := c.Runner
	if runner == nil {
		runner = platform.ExecRunner{}
	}
	out, runErr := runner.Output(ctx, "systemctl", "--user", "show-environment")
	if runErr != nil {
		base, fallbackErr := c.shellBase()
		if fallbackErr != nil {
			return "", false, runErr
		}
		return base, false, runErr
	}
	env := parseEnvironment(out)
	home := env["HOME"]
	if home == "" {
		home = c.homeDir()
	}
	if v := env["XDG_CONFIG_HOME"]; filepath.IsAbs(v) {
		return filepath.Clean(v), true, nil
	}
	// A relative XDG_CONFIG_HOME is invalid per the XDG base directory
	// specification; fall back instead of writing outside the manager's view.
	if home == "" {
		return "", true, fmt.Errorf("the systemd user manager did not report HOME or an absolute XDG_CONFIG_HOME")
	}
	return filepath.Join(home, ".config"), true, nil
}

func (c Config) shellBase() (string, error) {
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(v) {
		return filepath.Clean(v), nil
	}
	home := c.homeDir()
	if home == "" {
		return "", errors.New("cannot determine the user configuration directory; set XDG_CONFIG_HOME or HOME")
	}
	return filepath.Join(home, ".config"), nil
}

func (c Config) homeDir() string {
	if c.Home != "" {
		return c.Home
	}
	if c.Getenv != nil {
		// An injected environment is authoritative: falling back to the real
		// process home would leak the test environment into base resolution.
		return c.Getenv("HOME")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// parseEnvironment reads `VAR=value` lines from show-environment. systemd
// quotes values that contain unsafe characters and uses C-style escapes, so a
// quoted value is unquoted with strconv.Unquote.
func parseEnvironment(out string) map[string]string {
	env := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		if strings.HasPrefix(raw, `"`) {
			if unquoted, err := strconv.Unquote(raw); err == nil {
				raw = unquoted
			}
		}
		env[key] = raw
	}
	return env
}

// UnitPath is the manager-visible unit path for a config base.
func UnitPath(base string) string {
	return filepath.Join(base, "systemd", "user", UnitName)
}

// Unit describes the unit file at a resolved path. Safe is false for a
// symlink, a non-regular file or a foreign owner; such a path is never adopted
// or unlinked by RouteLatch. Content is the exact bytes read through the
// no-follow descriptor, used for the install snapshot and comparison.
type Unit struct {
	Path    string
	Exists  bool
	Managed bool
	Safe    bool
	Content []byte
}

// ReadUnit inspects a unit without following a final symlink or writing
// anything. A missing file is not an error.
func ReadUnit(path string) (Unit, error) {
	unit := Unit{Path: path, Safe: true}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return unit, nil
	}
	if err != nil {
		return Unit{Path: path}, err
	}
	unit.Exists = true
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		unit.Safe = false
		return unit, nil
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != currentUID() {
		unit.Safe = false
		return unit, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Unit{Path: path, Exists: true, Safe: false}, nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxUnitBytes+1))
	if err != nil {
		return Unit{Path: path, Exists: true, Safe: false}, nil
	}
	if len(raw) > maxUnitBytes {
		// Never truncate a unit: the install snapshot must restore every byte,
		// so an oversize file is not safe to replace or adopt.
		return Unit{Path: path, Exists: true, Safe: false}, nil
	}
	unit.Content = raw
	unit.Managed = hasOwnershipMarker(string(raw))
	return unit, nil
}

func hasOwnershipMarker(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSuffix(line, "\r") == OwnershipMarker {
			return true
		}
	}
	return false
}
