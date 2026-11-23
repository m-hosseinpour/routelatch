// Package autostart manages the XDG desktop entry that starts the RouteLatch
// app (with its tray) at login. It owns one file under
// `$XDG_CONFIG_HOME/autostart` and never starts or stops the app itself
// (docs/contracts.md §7.5).
package autostart

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/systemd"
)

const (
	// Filename is the desktop entry RouteLatch owns.
	Filename = "routelatch.desktop"
	// MarkerKey marks the file as managed by RouteLatch. A file without this
	// exact line is foreign and is never overwritten or removed.
	MarkerKey = "X-RouteLatch-Autostart=true"
	// ConflictService is the documented status warning when a managed service
	// unit exists at the same time.
	ConflictService = "service"

	entryMode    = 0o644
	dirMode      = 0o700
	maxEntrySize = 64 << 10
	// probeTimeout bounds each capability probe so a wedged session bus cannot
	// hold an autostart command.
	probeTimeout = 2 * time.Second
)

// Status is the documented `autostart ...` payload. The CLI renders the same
// fields as JSON and as human output.
type Status struct {
	Path string `json:"path"`
	// Enabled means a regular path exists at Path, including a foreign one.
	// An unsafe path (symlink, non-regular, foreign owner) is an error.
	Enabled bool `json:"enabled"`
	// Managed means that entry carries the ownership marker and passed the
	// managed-path checks, so RouteLatch may rewrite or remove it.
	Managed        bool   `json:"managed"`
	TrayAvailable  bool   `json:"tray_available"`
	PIDFDAvailable bool   `json:"pidfd_available"`
	Conflict       string `json:"conflict,omitempty"`
}

// Manager owns one autostart path. ServiceUnit and the probes are injected so
// tests never touch the real session bus, pidfd or systemctl.
type Manager struct {
	ConfigBase  string
	Executable  string
	ProbeTray   func(context.Context) error
	ProbePIDFD  func() error
	ServiceUnit func(context.Context) (systemd.Unit, error)
	Euid        func() int
}

// ConfigBase resolves the XDG config base for the autostart file. A relative
// XDG_CONFIG_HOME is ignored per the XDG base directory specification.
func ConfigBase(getenv func(string) string, home string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(v) {
		return filepath.Clean(v), nil
	}
	if home == "" {
		return "", errors.New("cannot determine the autostart directory; set XDG_CONFIG_HOME or HOME")
	}
	return filepath.Join(home, ".config"), nil
}

// DesktopEntry renders the managed file. The executable is always quoted so a
// path with spaces or reserved characters stays one Exec argument, and `%` is
// doubled because Exec expands field codes.
func DesktopEntry(executable string) ([]byte, error) {
	if executable == "" || !filepath.IsAbs(executable) {
		return nil, errors.New("the RouteLatch binary path is not absolute; refusing to write an autostart entry")
	}
	quoted, err := quoteExecArgument(executable)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=RouteLatch\n")
	b.WriteString("Comment=Start the RouteLatch proxy app and its tray at login\n")
	b.WriteString("Exec=" + quoted + " run\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("NoDisplay=true\n")
	b.WriteString(MarkerKey + "\n")
	return []byte(b.String()), nil
}

// quoteExecArgument applies the desktop-entry string escaping before the Exec
// quoting rule: in a quoted argument a literal backslash needs four
// backslashes, and `"`, "`", "$` are backslash-escaped. Control characters
// cannot be represented on one key line, so they are rejected.
func quoteExecArgument(s string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", errors.New("the RouteLatch binary path contains a control character that a desktop entry cannot represent")
		case r == '\\':
			b.WriteString(`\\\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '`':
			b.WriteString("\\`")
		case r == '$':
			b.WriteString(`\$`)
		case r == '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// Path is the managed file location.
func (m *Manager) Path() string {
	return filepath.Join(m.ConfigBase, "autostart", Filename)
}

func (m *Manager) dir() string {
	return filepath.Join(m.ConfigBase, "autostart")
}

func (m *Manager) euid() int {
	if m.Euid != nil {
		return m.Euid()
	}
	return os.Geteuid()
}

func (m *Manager) validate() error {
	if m == nil || m.ConfigBase == "" || !filepath.IsAbs(m.ConfigBase) {
		return internalFailure("the user configuration directory is not an absolute path", nil)
	}
	return nil
}

// Status reports the current entry and capability state without writing. A
// path RouteLatch cannot safely manage (symlink, non-regular, foreign owner)
// is rejected with the managed-path rule (§7.5), never reported as ours. A
// foreign regular file without the marker stays enabled/unmanaged.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	if err := m.validate(); err != nil {
		return Status{}, err
	}
	st := Status{Path: m.Path()}
	state, err := m.inspect()
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the existing autostart entry", err)
	}
	if state.unsafe != "" {
		return Status{}, conflictFailure(unsafeMessage(state.unsafe), nil)
	}
	st.Enabled = state.exists
	st.Managed = state.managed
	if managed, err := m.managedService(ctx); err == nil && managed {
		st.Conflict = ConflictService
	}
	st.TrayAvailable, st.PIDFDAvailable = m.capabilities(ctx)
	return st, nil
}

// Enable writes the managed entry. It probes tray and pidfd before writing or
// declaring an existing entry idempotent, refuses a foreign or unsafe path and
// never starts the app.
func (m *Manager) Enable(ctx context.Context) (Status, error) {
	if err := m.validate(); err != nil {
		return Status{}, err
	}
	desired, err := DesktopEntry(m.Executable)
	if err != nil {
		return Status{}, internalFailure(err.Error(), err)
	}
	state, err := m.inspect()
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the existing autostart entry; it was not changed", err)
	}
	if state.unsafe != "" {
		return Status{}, conflictFailure(unsafeMessage(state.unsafe), nil)
	}
	if state.exists && !state.managed {
		return Status{}, conflictFailure("an autostart entry that RouteLatch does not manage already exists; it was not overwritten", nil)
	}
	managed, err := m.managedService(ctx)
	if err != nil {
		return Status{}, conflictFailure("cannot verify whether a managed RouteLatch service unit exists; the autostart entry was not changed", err)
	}
	if managed {
		return Status{}, conflictFailure("a managed RouteLatch service unit exists; choose autostart or the service, not both (remove the service unit first)", nil)
	}
	tray, pidfd := m.capabilities(ctx)
	if !tray {
		return Status{}, unsupportedTray()
	}
	if !pidfd {
		return Status{}, unsupportedPIDFD()
	}
	st := Status{Path: m.Path(), TrayAvailable: tray, PIDFDAvailable: pidfd}
	if state.exists && state.managed && bytes.Equal(state.content, desired) {
		if state.mode.Perm() != entryMode {
			if err := os.Chmod(m.Path(), entryMode); err != nil {
				return Status{}, internalFailure("cannot set the autostart entry permissions", err)
			}
		}
		st.Enabled, st.Managed = true, true
		return st, nil
	}
	if err := m.write(desired); err != nil {
		return Status{}, err
	}
	st.Enabled, st.Managed = true, true
	return st, nil
}

// Disable removes only the managed file. A missing entry is a successful
// no-op; a foreign or unsafe path is refused untouched.
func (m *Manager) Disable(ctx context.Context) (Status, error) {
	if err := m.validate(); err != nil {
		return Status{}, err
	}
	state, err := m.inspect()
	if err != nil {
		return Status{}, conflictFailure("cannot inspect the existing autostart entry; it was not changed", err)
	}
	if state.unsafe != "" {
		return Status{}, conflictFailure(unsafeMessage(state.unsafe), nil)
	}
	managed, err := m.managedService(ctx)
	if err != nil {
		return Status{}, conflictFailure("cannot verify whether a managed RouteLatch service unit exists; the autostart entry was not changed", err)
	}
	if managed {
		return Status{}, conflictFailure("a managed RouteLatch service unit exists; remove the service unit before changing autostart", nil)
	}
	if state.exists && !state.managed {
		return Status{}, conflictFailure("an autostart entry that RouteLatch does not manage exists; it was not removed", nil)
	}
	tray, pidfd := m.capabilities(ctx)
	st := Status{Path: m.Path(), TrayAvailable: tray, PIDFDAvailable: pidfd}
	if !state.exists {
		return st, nil
	}
	return st, m.remove(state)
}

type entryState struct {
	exists  bool
	managed bool
	unsafe  string
	content []byte
	mode    os.FileMode
	info    os.FileInfo
}

// inspect applies the managed-path rule to the autostart directory and file:
// no symlink, a regular file, the current uid and the expected directory. It
// never creates anything and never follows a final symlink.
func (m *Manager) inspect() (entryState, error) {
	dir := m.dir()
	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return entryState{}, nil
	}
	if err != nil {
		return entryState{}, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return entryState{unsafe: "autostart_directory_symlink"}, nil
	}
	if !dirInfo.IsDir() {
		return entryState{unsafe: "autostart_directory_not_directory"}, nil
	}
	if st, ok := dirInfo.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != m.euid() {
		return entryState{unsafe: "autostart_directory_foreign_owner"}, nil
	}
	path := m.Path()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return entryState{}, nil
	}
	if err != nil {
		return entryState{}, err
	}
	state := entryState{exists: true, info: info}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		state.unsafe = "symlink"
		return state, nil
	case !info.Mode().IsRegular():
		state.unsafe = "not_regular"
		return state, nil
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != m.euid() {
		state.unsafe = "foreign_owner"
		return state, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		state.unsafe = "unreadable"
		return state, nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxEntrySize+1))
	if err != nil {
		state.unsafe = "unreadable"
		return state, nil
	}
	if len(raw) > maxEntrySize {
		// Too large to be the file RouteLatch writes; never adopt it.
		state.managed = false
		return state, nil
	}
	state.content = raw
	state.mode = info.Mode()
	state.managed = hasMarker(string(raw))
	return state, nil
}

func hasMarker(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSuffix(line, "\r") == MarkerKey {
			return true
		}
	}
	return false
}

// ensureDir creates the autostart directory and re-applies the managed-path
// rule after creation so a path swapped for a symlink is not written through.
// Only a directory RouteLatch creates is set to 0700: an existing directory
// belongs to the user's XDG configuration, so its mode is left alone.
func (m *Manager) ensureDir() error {
	dir := m.dir()
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return internalFailure("cannot create the autostart directory", err)
		}
		// MkdirAll is subject to the umask; set the exact mode for the new
		// directory. Open with O_NOFOLLOW so a path swapped for a symlink
		// between MkdirAll and the chmod is never followed.
		f, openErr := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if openErr != nil {
			if errors.Is(openErr, syscall.ELOOP) || errors.Is(openErr, syscall.ENOTDIR) {
				return conflictFailure("the autostart directory is a symlink or not a directory; refusing to write through it", nil)
			}
			return internalFailure("cannot open the new autostart directory", openErr)
		}
		chmodErr := f.Chmod(dirMode)
		closeErr := f.Close()
		if chmodErr != nil {
			return internalFailure("cannot set the autostart directory permissions", chmodErr)
		}
		if closeErr != nil {
			return internalFailure("cannot close the autostart directory", closeErr)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return internalFailure("cannot inspect the autostart directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return conflictFailure("the autostart directory is a symlink or not a directory; refusing to write through it", nil)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != m.euid() {
		return conflictFailure("the autostart directory is not owned by the current uid; refusing to write into it", nil)
	}
	return nil
}

func (m *Manager) write(content []byte) error {
	if err := m.ensureDir(); err != nil {
		return err
	}
	state, err := m.inspect()
	if err != nil {
		return conflictFailure("cannot inspect the autostart entry before writing; it was not changed", err)
	}
	if state.unsafe != "" || (state.exists && !state.managed) {
		return conflictFailure("the autostart entry changed while enabling; it was not overwritten", nil)
	}
	// The re-inspect above is a safety check, not a lock: the directory entry
	// can change between it and Rename. That TOCTOU window is accepted; the
	// rename replaces the path atomically and never follows a final symlink,
	// and every managed decision is based on no-follow observations.
	tmp, err := os.CreateTemp(m.dir(), "."+Filename+".tmp-*")
	if err != nil {
		return internalFailure("cannot create a temporary autostart file", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(entryMode); err != nil {
		cleanup()
		return internalFailure("cannot set the autostart file permissions", err)
	}
	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return internalFailure("cannot write the autostart file", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return internalFailure("cannot fsync the autostart file", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot close the autostart file", err)
	}
	if err := os.Rename(tmpPath, m.Path()); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot atomically replace the autostart entry", err)
	}
	if err := syncDir(m.dir()); err != nil {
		return internalFailure("the autostart entry was written but its directory could not be fsynced", err)
	}
	return nil
}

// remove rechecks the inode immediately before unlinking so a replacement by
// another file is not deleted as if it were the managed entry.
func (m *Manager) remove(initial entryState) error {
	info, err := os.Lstat(m.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return internalFailure("cannot inspect the autostart entry before removing it", err)
	}
	if initial.info == nil || !os.SameFile(initial.info, info) {
		return conflictFailure("the autostart entry changed while disabling; it was not removed", nil)
	}
	if err := os.Remove(m.Path()); err != nil {
		return internalFailure("cannot remove the autostart entry", err)
	}
	if err := syncDir(m.dir()); err != nil {
		return internalFailure("the autostart entry was removed but its directory could not be fsynced", err)
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

func (m *Manager) capabilities(ctx context.Context) (tray, pidfd bool) {
	if m.ProbeTray != nil {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		tray = m.ProbeTray(probeCtx) == nil
		cancel()
	}
	if m.ProbePIDFD != nil {
		pidfd = m.ProbePIDFD() == nil
	}
	return tray, pidfd
}

func (m *Manager) managedService(ctx context.Context) (bool, error) {
	if m.ServiceUnit == nil {
		return false, nil
	}
	unit, err := m.ServiceUnit(ctx)
	if err != nil {
		return false, err
	}
	return unit.Managed, nil
}

func unsafeMessage(reason string) string {
	switch reason {
	case "symlink":
		return "the autostart entry is a symlink; refusing to follow or overwrite it"
	case "not_regular":
		return "the autostart entry is not a regular file; refusing to change it"
	case "foreign_owner":
		return "the autostart entry is not owned by the current uid; refusing to change it"
	case "unreadable":
		return "the autostart entry cannot be read; refusing to overwrite or remove it"
	case "autostart_directory_symlink":
		return "the autostart directory is a symlink; refusing to use it"
	case "autostart_directory_not_directory":
		return "the autostart path is not a directory; refusing to use it"
	case "autostart_directory_foreign_owner":
		return "the autostart directory is not owned by the current uid; refusing to use it"
	default:
		return "the autostart entry is not safe to manage"
	}
}

func conflictFailure(message string, cause error) error {
	return &control.OpError{
		Kind: contract.StateLocked, Exit: contract.ExitConflict,
		Message: message, Cause: cause,
	}
}

func unsupportedTray() error {
	return &control.OpError{
		Kind: contract.Unsupported, Exit: contract.ExitUnsupported,
		Message: "no StatusNotifier tray host is available; autostart starts the app with its tray and has no headless fallback",
		Detail: map[string]any{
			"reason":     "tray_unavailable",
			"suggestion": "--headless",
		},
	}
}

func unsupportedPIDFD() error {
	return &control.OpError{
		Kind: contract.Unsupported, Exit: contract.ExitUnsupported,
		Message: "pidfd is not available (Linux >= 5.3 with pidfd_open allowed); the app/tray mode and autostart need it",
		Detail: map[string]any{
			"reason":     "pidfd_unavailable",
			"suggestion": "--headless",
		},
	}
}

func internalFailure(message string, cause error) error {
	return &control.OpError{Kind: contract.Internal, Exit: contract.ExitError, Message: message, Cause: cause}
}
