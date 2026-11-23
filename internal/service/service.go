// Package service manages the opt-in RouteLatch systemd user unit
// (docs/contracts.md §7.5). It resolves the unit path from the user manager's
// own configuration base, writes a managed unit with pinned XDG bases,
// verifies real readiness through the management socket, and rolls a failed
// install back to its snapshot.
//
// Nothing here is wired to package installation: `service install` is the only
// path that creates the unit, and `uninstall` removes only a unit that carries
// the ownership marker.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/store"
	"routelatch/internal/systemd"
)

// ConflictAutostart is the documented status warning when a managed autostart
// entry exists beside the service.
const ConflictAutostart = "autostart"

const (
	defaultReadinessTimeout = 10 * time.Second
	defaultPollInterval     = 200 * time.Millisecond
	unitMode                = 0o644
	unitDirMode             = 0o700
	// managerTimeout bounds the user-manager environment lookup so a wedged
	// bus cannot hold a service command.
	managerTimeout = 2 * time.Second
)

// Status is the documented `service ...` payload.
type Status struct {
	Unit    string `json:"unit"`
	Enabled bool   `json:"enabled"`
	Active  bool   `json:"active"`
	Linger  string `json:"linger"`
	Managed bool   `json:"managed"`
	// Conflict is "autostart" when a managed autostart entry exists. Status
	// still exits 0; setup commands refuse.
	Conflict string `json:"conflict,omitempty"`
}

// ReadyStatus is the readiness evidence from the same data dir's management
// socket. ActiveState alone is not enough for Type=simple.
type ReadyStatus struct {
	Ready         bool
	ListenersOpen bool
	Mode          string
}

// Readiness polls one management socket. An error means the socket is not
// answering yet; the install loop keeps polling until its deadline.
type Readiness func(context.Context) (ReadyStatus, error)

// SocketReadiness returns the production readiness probe for one instance.
func SocketReadiness(socket string) Readiness {
	return func(ctx context.Context) (ReadyStatus, error) {
		client := control.Client{Path: socket, Timeout: time.Second}
		env, err := client.Call(ctx, control.Request{Op: control.OpStatus})
		if err != nil {
			return ReadyStatus{}, err
		}
		if !env.OK || env.Error != nil {
			return ReadyStatus{}, errors.New("management status reported an error")
		}
		data, ok := env.Data.(map[string]any)
		if !ok {
			return ReadyStatus{}, errors.New("management status is not an object")
		}
		ready, _ := data["ready"].(bool)
		listeners, _ := data["listeners_open"].(bool)
		mode, _ := data["mode"].(string)
		return ReadyStatus{Ready: ready, ListenersOpen: listeners, Mode: mode}, nil
	}
}

// Manager owns one user unit. Every external capability and filesystem root is
// injectable so tests never touch the real user manager or instance locks.
type Manager struct {
	Runner     platform.CommandRunner
	Getenv     func(string) string
	Home       string
	Executable string
	Paths      store.Paths
	// DataDirSet is true for --data-dir: the unit pins that absolute root and
	// omits Environment=.
	DataDirSet bool
	Force      bool

	Ready            Readiness
	ReadinessTimeout time.Duration
	PollInterval     time.Duration

	// ProbeLocks refuses app/other headless engines on the same data dir. The
	// production value is ProbeOtherEngines; nil selects it.
	ProbeLocks func(context.Context, store.Paths) error
	// AutostartManaged reports a managed autostart entry. nil resolves the
	// shell XDG base and inspects the entry.
	AutostartManaged func() (bool, error)
	CurrentUser      func() (string, error)
	Euid             func() int
	// SyncDir fsyncs the unit directory. nil selects the real sync. A test
	// seam for the post-rename durability failure.
	SyncDir func(string) error
}

func (m *Manager) getenv(key string) string {
	if m.Getenv != nil {
		return m.Getenv(key)
	}
	return os.Getenv(key)
}

func (m *Manager) runner() platform.CommandRunner {
	if m.Runner != nil {
		return m.Runner
	}
	return platform.ExecRunner{}
}

func (m *Manager) euid() int {
	if m.Euid != nil {
		return m.Euid()
	}
	return os.Geteuid()
}

// UserName returns the target user for the linger hint.
func (m *Manager) UserName() (string, error) { return m.currentUser() }

func (m *Manager) currentUser() (string, error) {
	if m.CurrentUser != nil {
		return m.CurrentUser()
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func (m *Manager) pollInterval() time.Duration {
	if m.PollInterval > 0 {
		return m.PollInterval
	}
	return defaultPollInterval
}

func (m *Manager) readinessTimeout() time.Duration {
	if m.ReadinessTimeout > 0 {
		return m.ReadinessTimeout
	}
	return defaultReadinessTimeout
}

// managerBase resolves the user manager's config base. All three commands
// require the manager to confirm it: a unit at the shell's fallback path may
// be invisible to the manager that actually runs it.
func (m *Manager) managerBase(ctx context.Context) (string, bool, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, managerTimeout)
	defer cancel()
	cfg := systemd.Config{Runner: m.runner(), Getenv: m.Getenv, Home: m.Home}
	base, confirmed, err := cfg.ConfigBase(lookupCtx)
	if base == "" {
		return "", false, err
	}
	return base, confirmed, err
}

// checkUser refuses a command run through sudo/pkexec for another user. Only
// the target user may manage that user's service.
func (m *Manager) checkUser() error {
	name, nameErr := m.currentUser()
	if raw := m.getenv("SUDO_USER"); raw != "" {
		if nameErr != nil || raw != name {
			return deniedUser()
		}
	}
	if raw := m.getenv("PKEXEC_UID"); raw != "" {
		uid, err := strconv.Atoi(raw)
		if err != nil || uid != m.euid() {
			return deniedUser()
		}
	}
	return nil
}

// Install writes or converges the managed unit and returns only after real
// readiness. Any failure after the unit file changed runs exactly one rollback.
func (m *Manager) Install(ctx context.Context) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{}, unsupportedManager("the opt-in service is Linux/systemd-only")
	}
	if err := m.checkUser(); err != nil {
		return Status{}, err
	}
	base, confirmed, err := m.managerBase(ctx)
	if err != nil || !confirmed || base == "" {
		return Status{}, unsupportedManager("the systemd user manager could not confirm its configuration base; no unit was written")
	}
	desired, err := m.unitContent()
	if err != nil {
		return Status{}, internalFailure("cannot build the service unit", err)
	}
	path := systemd.UnitPath(base)
	unit, err := systemd.ReadUnit(path)
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the existing service unit; nothing was changed", err)
	}
	if unit.Exists && !unit.Safe {
		return Status{}, conflictFailure("the service unit path is a symlink, non-regular file or foreign-owned; refusing to follow or overwrite it", nil)
	}
	state, err := m.show(ctx)
	if err != nil {
		return Status{}, err
	}
	// The exemption is only for this unit's own engine on this instance. A
	// managed active unit that serves another data dir must not hide an app or
	// headless engine on the target data dir.
	ownEngine := unit.Exists && unit.Managed && unit.Safe && state.Live() && state.MainPID > 0 &&
		sameInstance(unit.Content, desired)
	if unit.Exists && !unit.Managed {
		if state.Live() {
			return Status{}, conflictFailure("a service unit that RouteLatch does not manage is active; stop it before installing (it counts as another headless engine)", nil)
		}
		if !m.Force {
			return Status{}, conflictFailure("a service unit that RouteLatch does not manage already exists; use --force to adopt it after it is inactive", nil)
		}
	}
	if unit.Exists && unit.Managed && !bytes.Equal(unit.Content, desired) && !m.Force {
		return Status{}, conflictFailure("a different RouteLatch service unit is installed; use --force to replace it", nil)
	}
	managedAutostart, err := m.autostartManaged()
	if err != nil {
		return Status{}, conflictFailure("cannot verify whether a managed autostart entry exists; nothing was changed", err)
	}
	if managedAutostart {
		return Status{}, conflictFailure("a managed autostart entry exists; choose autostart or the service, not both (disable autostart first)", nil)
	}
	if !ownEngine {
		if err := m.probeLocks(ctx); err != nil {
			return Status{}, err
		}
	}

	snap := installSnapshot{
		exists: unit.Exists, content: unit.Content, enabled: state.Enabled(),
		live: state.Live(), mainPID: state.MainPID, startTime: state.StartTime,
	}
	identical := unit.Exists && unit.Managed && bytes.Equal(unit.Content, desired)
	written := false
	if !identical || m.Force {
		committed, writeErr := m.writeUnit(path, desired)
		if writeErr != nil {
			if committed {
				// The path entry was replaced before the durability check
				// failed; the snapshot rollback still applies.
				written = true
				if rbErr := m.rollback(ctx, path, snap); rbErr != nil {
					return Status{}, rollbackFailure(writeErr, rbErr)
				}
			}
			return Status{}, writeErr
		}
		written = true
	}

	status, err := m.converge(ctx, path, state, snap, written)
	if err != nil {
		return Status{}, err
	}
	return status, nil
}

// converge performs the documented idempotent/force sequence and readiness
// check, rolling back on a failure after the write.
func (m *Manager) converge(ctx context.Context, path string, state unitState, snap installSnapshot, written bool) (Status, error) {
	fail := func(err error) (Status, error) {
		if !written {
			return Status{}, err
		}
		rbErr := m.rollback(ctx, path, snap)
		if rbErr == nil {
			return Status{}, err
		}
		return Status{}, rollbackFailure(err, rbErr)
	}

	if written {
		if err := m.systemctl(ctx, "daemon-reload"); err != nil {
			return fail(err)
		}
	}
	switch {
	case !snap.exists:
		// Fresh install: one atomic enable+start.
		if err := m.systemctl(ctx, "enable", "--now", systemd.UnitName); err != nil {
			return fail(err)
		}
	case m.Force:
		if err := m.systemctl(ctx, "enable", systemd.UnitName); err != nil {
			return fail(err)
		}
		if snap.live {
			if err := m.systemctl(ctx, "restart", systemd.UnitName); err != nil {
				return fail(err)
			}
		} else {
			if err := m.systemctl(ctx, "start", systemd.UnitName); err != nil {
				return fail(err)
			}
		}
	default:
		// Identical managed unit: no rewrite or restart, just converge state.
		if !state.Enabled() {
			if err := m.systemctl(ctx, "enable", systemd.UnitName); err != nil {
				return fail(err)
			}
		}
		if !state.Live() {
			if err := m.systemctl(ctx, "start", systemd.UnitName); err != nil {
				return fail(err)
			}
		}
	}

	if err := m.waitReady(ctx); err != nil {
		return fail(err)
	}
	return Status{
		Unit: path, Enabled: true, Active: true,
		Linger: m.linger(ctx), Managed: true,
	}, nil
}

// Uninstall checks ownership before doing anything, then stops, disables and
// removes only a managed unit. State, configuration and linger are kept.
func (m *Manager) Uninstall(ctx context.Context) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{}, unsupportedManager("the opt-in service is Linux/systemd-only")
	}
	if err := m.checkUser(); err != nil {
		return Status{}, err
	}
	base, confirmed, lookupErr := m.managerBase(ctx)
	if lookupErr != nil || !confirmed || base == "" {
		return Status{}, unsupportedManager("the systemd user manager could not confirm its configuration base; nothing was changed")
	}
	path := systemd.UnitPath(base)
	unit, err := systemd.ReadUnit(path)
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the service unit; nothing was changed", err)
	}
	if unit.Exists && !unit.Safe {
		return Status{}, conflictFailure("the service unit path is a symlink, non-regular file or foreign-owned; it was not stopped or removed", nil)
	}
	if unit.Exists && !unit.Managed {
		return Status{}, conflictFailure("the service unit was not created by RouteLatch; it was not stopped or removed", nil)
	}
	status := Status{Unit: path, Linger: m.linger(ctx)}
	if !unit.Exists {
		// Idempotent: no owned unit means nothing to stop or remove.
		state, showErr := m.show(ctx)
		if showErr == nil {
			status.Enabled, status.Active = state.Enabled(), state.ActiveState == "active"
		}
		return status, nil
	}
	if err := m.systemctl(ctx, "stop", systemd.UnitName); err != nil {
		return Status{}, err
	}
	if err := m.systemctl(ctx, "disable", systemd.UnitName); err != nil {
		return Status{}, err
	}
	if err := m.removeUnit(path); err != nil {
		return Status{}, err
	}
	if err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return Status{}, err
	}
	status.Enabled, status.Active, status.Managed = false, false, false
	return status, nil
}

// Status is read-only: it resolves the same manager base, inspects the unit,
// queries systemd and reports the autostart conflict without changing anything.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{}, unsupportedManager("the opt-in service is Linux/systemd-only")
	}
	if err := m.checkUser(); err != nil {
		return Status{}, err
	}
	base, confirmed, lookupErr := m.managerBase(ctx)
	if lookupErr != nil || !confirmed || base == "" {
		return Status{}, unsupportedManager("the systemd user manager could not confirm its configuration base")
	}
	path := systemd.UnitPath(base)
	unit, err := systemd.ReadUnit(path)
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the service unit", err)
	}
	if unit.Exists && !unit.Safe {
		return Status{}, conflictFailure("the service unit path is a symlink, non-regular file or foreign-owned; refusing to report it as manageable", nil)
	}
	state, err := m.show(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Unit: path, Enabled: state.Enabled(), Active: state.ActiveState == "active",
		Linger: m.linger(ctx), Managed: unit.Managed,
	}
	if managed, err := m.autostartManaged(); err == nil && managed {
		status.Conflict = ConflictAutostart
	}
	return status, nil
}

// writeUnit creates the unit directory and atomically replaces the unit with
// no-follow directory checks. The rename itself never follows a final symlink.
// committed reports that the path entry already changed even when the error is
// the post-rename directory fsync; the caller must roll back with the snapshot.
func (m *Manager) writeUnit(path string, content []byte) (committed bool, err error) {
	dir := filepath.Dir(path)
	if err := ensureUnitDir(dir); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".routelatch.service.tmp-*")
	if err != nil {
		return false, internalFailure("cannot create a temporary service unit", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(unitMode); err != nil {
		cleanup()
		return false, internalFailure("cannot set the service unit permissions", err)
	}
	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return false, internalFailure("cannot write the service unit", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return false, internalFailure("cannot fsync the service unit", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return false, internalFailure("cannot close the service unit", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return false, internalFailure("cannot atomically replace the service unit", err)
	}
	if err := m.syncDir(dir); err != nil {
		return true, internalFailure("the service unit was written but its directory could not be fsynced", err)
	}
	return true, nil
}

func (m *Manager) syncDir(dir string) error {
	if m.SyncDir != nil {
		return m.SyncDir(dir)
	}
	return syncDir(dir)
}

func (m *Manager) removeUnit(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return internalFailure("cannot inspect the service unit before removing it", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return conflictFailure("the service unit changed to a symlink or non-regular file; it was not removed", nil)
	}
	if err := os.Remove(path); err != nil {
		return internalFailure("cannot remove the service unit", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return internalFailure("the service unit was removed but its directory could not be fsynced", err)
	}
	return nil
}

func ensureUnitDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, unitDirMode); err != nil {
			return internalFailure("cannot create the user unit directory", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return internalFailure("cannot inspect the user unit directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return conflictFailure("the user unit directory is a symlink or not a directory; refusing to write through it", nil)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return conflictFailure("the user unit directory is not owned by the current uid; refusing to write into it", nil)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// autostartManaged resolves the shell XDG base for the managed entry. An
// unresolvable base cannot contain an entry, so it is not a conflict.
func (m *Manager) autostartManaged() (bool, error) {
	if m.AutostartManaged != nil {
		return m.AutostartManaged()
	}
	base, err := autostart.ConfigBase(m.Getenv, m.Home)
	if err != nil {
		return false, nil
	}
	return autostart.ManagedFile(base)
}

func (m *Manager) probeLocks(ctx context.Context) error {
	if m.ProbeLocks != nil {
		return m.ProbeLocks(ctx, m.Paths)
	}
	return ProbeOtherEngines(m.Paths)
}

// ProbeOtherEngines refuses an app supervisor or another headless engine on
// this data directory. It only probes existing paths with a no-follow
// descriptor and never creates or deletes a lock.
func ProbeOtherEngines(paths store.Paths) error {
	for _, path := range []string{paths.AppLock, paths.Lock} {
		held, err := lockHeld(path)
		if err != nil {
			return conflictFailure("cannot verify the existing RouteLatch locks; stop any older instance explicitly", err)
		}
		if held {
			return engineRunning()
		}
	}
	for _, legacy := range paths.LegacyLocks {
		held, err := lockHeld(legacy.Lock)
		if err != nil {
			return conflictFailure("cannot verify the legacy RouteLatch lock; stop any older instance explicitly", err)
		}
		if held {
			return engineRunning()
		}
	}
	return nil
}

// lockHeld reports whether an existing lock file is held. A missing path is
// free (the engine creates it on start); a symlink, non-regular file or
// foreign owner is an error so install fails closed.
func lockHeld(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("lock path is not a regular file")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return false, errors.New("lock path is not owned by the current uid")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// unitState is the parsed `systemctl show` view that install, status and
// rollback share.
type unitState struct {
	LoadState      string
	ActiveState    string
	MainPID        int
	StartTime      uint64
	UnitFileState  string
	ExecMainStatus int
}

func (u unitState) Live() bool {
	switch u.ActiveState {
	case "active", "activating", "reloading", "deactivating":
		return true
	}
	return false
}

func (u unitState) Enabled() bool {
	switch u.UnitFileState {
	case "enabled", "enabled-runtime", "linked":
		return true
	}
	return false
}

func (m *Manager) show(ctx context.Context) (unitState, error) {
	out, err := m.runner().Output(ctx, "systemctl", "--user", "show", systemd.UnitName,
		"--property=LoadState", "--property=ActiveState", "--property=MainPID",
		"--property=ExecMainStartTimestampMonotonic", "--property=UnitFileState",
		"--property=ExecMainStatus")
	if err != nil {
		return unitState{}, unsupportedManager("the systemd user manager is not available")
	}
	state := unitState{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.ActiveState = value
		case "MainPID":
			state.MainPID, _ = strconv.Atoi(value)
		case "ExecMainStartTimestampMonotonic":
			state.StartTime, _ = strconv.ParseUint(value, 10, 64)
		case "UnitFileState":
			state.UnitFileState = value
		case "ExecMainStatus":
			state.ExecMainStatus, _ = strconv.Atoi(value)
		}
	}
	return state, nil
}

func (m *Manager) systemctl(ctx context.Context, args ...string) error {
	full := append([]string{"--user"}, args...)
	if _, err := m.runner().Output(ctx, "systemctl", full...); err != nil {
		return toolFailure("systemctl "+args[0]+" failed; nothing may be assumed about the service state", err)
	}
	return nil
}

// waitReady polls the same data dir's management socket. It never trusts
// ActiveState: Type=simple is active before listeners bind.
func (m *Manager) waitReady(ctx context.Context) error {
	ready := m.Ready
	if ready == nil {
		ready = SocketReadiness(m.Paths.Socket)
	}
	deadline := time.Now().Add(m.readinessTimeout())
	deadPolls := 0
	for {
		state, err := m.show(ctx)
		if err != nil {
			return err
		}
		if !state.Live() {
			if state.ActiveState == "failed" || state.ExecMainStatus != 0 {
				return childExitFailure(state.ExecMainStatus)
			}
			// Give the manager a moment to move inactive -> activating after
			// start; a unit that stays dead without an exit status is a defect.
			deadPolls++
			if deadPolls >= 10 {
				return internalFailure("the service unit did not start and reported no exit status", nil)
			}
		} else {
			deadPolls = 0
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		status, probeErr := ready(probeCtx)
		cancel()
		if state.Live() && probeErr == nil && status.Ready && (status.Mode == "stopped" || status.ListenersOpen) {
			return nil
		}
		if time.Now().After(deadline) {
			return timeoutFailure(m.readinessTimeout())
		}
		select {
		case <-ctx.Done():
			return canceledFailure(ctx.Err())
		case <-time.After(m.pollInterval()):
		}
	}
}

// linger reads Linger for the current user. loginctl is advisory: a failure is
// reported as unknown instead of turning a successful install into an error.
func (m *Manager) linger(ctx context.Context) string {
	name, err := m.currentUser()
	if err != nil || name == "" {
		return "unknown"
	}
	lingerCtx, cancel := context.WithTimeout(ctx, managerTimeout)
	defer cancel()
	out, err := m.runner().Output(lingerCtx, "loginctl", "show-user", name, "--property=Linger")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && key == "Linger" {
			switch value {
			case "yes":
				return "yes"
			case "no":
				return "no"
			}
		}
	}
	return "unknown"
}

// LingerHint is the only linger action RouteLatch offers. It never runs sudo.
func LingerHint(user string) string {
	return "sudo loginctl enable-linger " + user
}

// installSnapshot is the pre-write state used by rollback.
type installSnapshot struct {
	exists    bool
	content   []byte
	enabled   bool
	live      bool
	mainPID   int
	startTime uint64
}

// rollback restores the snapshot exactly once, following the precedence in
// §7.5: fresh install, same live MainPID/starttime, then stop/restore/start.
func (m *Manager) rollback(ctx context.Context, path string, snap installSnapshot) error {
	now, showErr := m.show(ctx)
	sameProcess := showErr == nil && snap.exists && snap.live && snap.mainPID > 0 &&
		now.Live() && now.MainPID == snap.mainPID && now.StartTime == snap.startTime

	if sameProcess {
		// The previous process is still running and healthy; do not stop it.
		if _, err := m.writeUnit(path, snap.content); err != nil {
			return err
		}
		if err := m.restoreEnabled(ctx, snap.enabled); err != nil {
			return err
		}
		return m.systemctl(ctx, "daemon-reload")
	}

	if err := m.systemctl(ctx, "stop", systemd.UnitName); err != nil {
		return err
	}
	if err := m.systemctl(ctx, "disable", systemd.UnitName); err != nil {
		return err
	}
	if snap.exists {
		if _, err := m.writeUnit(path, snap.content); err != nil {
			return err
		}
	} else {
		if err := m.removeUnit(path); err != nil {
			return err
		}
	}
	if snap.exists {
		if err := m.restoreEnabled(ctx, snap.enabled); err != nil {
			return err
		}
	}
	if err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	// An adopted markerless unit was inactive: the branch above never starts it.
	if snap.exists && snap.live {
		if err := m.systemctl(ctx, "start", systemd.UnitName); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) restoreEnabled(ctx context.Context, enabled bool) error {
	if enabled {
		return m.systemctl(ctx, "enable", systemd.UnitName)
	}
	return m.systemctl(ctx, "disable", systemd.UnitName)
}

// unitContent renders the documented unit. ExecStart values quote for systemd
// with `%` -> `%%` and `$` -> `$$`; Environment values escape `%` only because
// `$` is literal there.
func (m *Manager) unitContent() ([]byte, error) {
	if m.Executable == "" || !filepath.IsAbs(m.Executable) {
		return nil, errors.New("the RouteLatch binary path is not absolute; refusing to write a service unit")
	}
	exe, err := quoteArgument(m.Executable)
	if err != nil {
		return nil, err
	}
	args := []string{exe}
	env := ""
	if m.DataDirSet {
		if m.Paths.DataDir == "" || !filepath.IsAbs(m.Paths.DataDir) {
			return nil, errors.New("the data directory is not absolute; refusing to write a service unit")
		}
		dataDir, err := quoteArgument(m.Paths.DataDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--data-dir", dataDir)
	} else {
		configBase := filepath.Dir(m.Paths.ConfigDir)
		stateBase := filepath.Dir(m.Paths.StateDir)
		if !filepath.IsAbs(configBase) || !filepath.IsAbs(stateBase) {
			return nil, errors.New("the XDG config/state bases are not absolute; refusing to write a service unit")
		}
		configEnv, err := quoteEnvironment("XDG_CONFIG_HOME", configBase)
		if err != nil {
			return nil, err
		}
		stateEnv, err := quoteEnvironment("XDG_STATE_HOME", stateBase)
		if err != nil {
			return nil, err
		}
		env = "Environment=" + configEnv + " " + stateEnv + "\n"
	}
	args = append(args, "run", "--headless")

	var b strings.Builder
	b.WriteString(systemd.OwnershipMarker + "\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=RouteLatch local proxy\n")
	b.WriteString("After=network.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + strings.Join(args, " ") + "\n")
	b.WriteString(env)
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=2s\n")
	b.WriteString("RestartPreventExitStatus=1 4 6 7 9 10\n")
	b.WriteString("KillSignal=SIGTERM\n")
	b.WriteString("KillMode=mixed\n")
	b.WriteString("TimeoutStopSec=20s\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return []byte(b.String()), nil
}

// sameInstance reports whether the old managed unit already serves the same
// data/config roots as the desired one. Only then may its live engine hold the
// target locks; anything else is a different instance and must be probed.
func sameInstance(old, desired []byte) bool {
	oldExec, oldEnv := unitLine(old, "ExecStart="), unitLine(old, "Environment=")
	newExec, newEnv := unitLine(desired, "ExecStart="), unitLine(desired, "Environment=")
	if newEnv == "" {
		// --data-dir unit: no Environment, and the same absolute root is the
		// instance identity. Compare the exact argument, not a substring: one
		// data dir can be a prefix of another and the binary can live below it.
		if oldEnv != "" || strings.Count(oldExec, "--data-dir ") != 1 || strings.Count(newExec, "--data-dir ") != 1 {
			return false
		}
		token := dataDirArgument(newExec)
		return token != "" && dataDirArgument(oldExec) == token
	}
	// Default instance: the pinned config/state bases are the identity.
	return oldEnv == newEnv && !strings.Contains(oldExec, "--data-dir ")
}

func unitLine(content []byte, prefix string) string {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSuffix(line, "\r")
		}
	}
	return ""
}

// dataDirArgument returns the quoted/unquoted argument after --data-dir.
func dataDirArgument(execLine string) string {
	const flag = "--data-dir "
	i := strings.Index(execLine, flag)
	if i < 0 {
		return ""
	}
	rest := execLine[i+len(flag):]
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		for j := 1; j < len(rest); j++ {
			if rest[j] == '\\' {
				j++
				continue
			}
			if rest[j] == '"' {
				return rest[:j+1]
			}
		}
		return ""
	}
	if k := strings.IndexByte(rest, ' '); k >= 0 {
		return rest[:k]
	}
	return rest
}

// quoteArgument applies systemd command-line quoting to one argument.
func quoteArgument(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", errors.New("a service path contains a control character that a unit file cannot represent")
	}
	escaped := strings.ReplaceAll(s, "%", "%%")
	escaped = strings.ReplaceAll(escaped, "$", "$$")
	if escaped == "" {
		return `""`, nil
	}
	needsQuotes := strings.ContainsAny(escaped, " \t\"'\\;")
	if !needsQuotes {
		return escaped, nil
	}
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// quoteEnvironment always quotes one assignment. systemd documents
// Environment="VAR=value" as the safe form for paths with spaces or quotes.
// Specifiers are escaped (`%` -> `%%`), but `$` is literal in Environment= and
// must not be doubled the way it is in ExecStart=.
func quoteEnvironment(key, value string) (string, error) {
	s := key + "=" + value
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", errors.New("an XDG base contains a control character that a unit file cannot represent")
	}
	escaped := strings.ReplaceAll(s, "%", "%%")
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// --- documented failures -------------------------------------------------

func conflictFailure(message string, cause error) error {
	return &control.OpError{
		Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: message, Cause: cause,
	}
}

func engineRunning() error {
	return &control.OpError{
		Kind: contract.StateLocked, Exit: contract.ExitConflict,
		Message: "another RouteLatch app or headless engine is running for this data directory; stop it before installing the service",
		Detail:  map[string]any{"reason": "engine_running"},
	}
}

func unsupportedManager(message string) error {
	return &control.OpError{
		Kind: contract.Unsupported, Exit: contract.ExitUnsupported, Message: message,
		Detail: map[string]any{"reason": "systemd_unavailable"},
	}
}

func deniedUser() error {
	return &control.OpError{
		Kind: contract.PolicyDenied, Exit: contract.ExitDenied,
		Message: "run the service command as the target user; sudo/pkexec for another user is refused",
		Detail:  map[string]any{"reason": "wrong_user"},
	}
}

func toolFailure(message string, cause error) error {
	return &control.OpError{
		Kind: contract.ExternalToolFailed, Exit: contract.ExitError, Message: message, Cause: cause,
	}
}

func internalFailure(message string, cause error) error {
	return &control.OpError{Kind: contract.Internal, Exit: contract.ExitError, Message: message, Cause: cause}
}

func timeoutFailure(timeout time.Duration) error {
	return &control.OpError{
		Kind: contract.Timeout, Exit: contract.ExitTimeout,
		Message: fmt.Sprintf("the service did not become ready within %s", timeout),
		Detail:  map[string]any{"reason": "readiness_timeout"},
	}
}

func canceledFailure(cause error) error {
	return &control.OpError{
		Kind: contract.Canceled, Exit: contract.ExitCanceled, Message: "service install was canceled", Cause: cause,
	}
}

// childExitFailure maps the documented terminal child exits. 4 is the engine's
// "another instance" status; the service command reports it as conflict 6.
func childExitFailure(status int) error {
	message := fmt.Sprintf("the RouteLatch service exited with status %d before becoming ready", status)
	opErr := &control.OpError{Message: message, Detail: map[string]any{"reason": "service_exit_status", "exit_status": status}}
	switch status {
	case 1:
		opErr.Kind, opErr.Exit = contract.Internal, contract.ExitError
	case 4, 6:
		opErr.Kind, opErr.Exit = contract.StateLocked, contract.ExitConflict
	case 7:
		opErr.Kind, opErr.Exit = contract.Unsupported, contract.ExitUnsupported
	case 9:
		opErr.Kind, opErr.Exit = contract.PeerRejected, contract.ExitDenied
	case 10:
		opErr.Kind, opErr.Exit = contract.StateCorrupt, contract.ExitStateCorrupt
	default:
		opErr.Kind, opErr.Exit = contract.Internal, contract.ExitError
	}
	return opErr
}

func rollbackFailure(installErr, rollbackErr error) error {
	cause := fmt.Errorf("install failed (%v); rollback failed (%v)", installErr, rollbackErr)
	return &control.OpError{
		Kind: contract.Internal, Exit: contract.ExitError,
		Message: "the service install failed and its rollback also failed; the unit was left in place for inspection: install: " +
			installErr.Error() + "; rollback: " + rollbackErr.Error(),
		Cause:  cause,
		Detail: map[string]any{"reason": "rollback_failed"},
	}
}
