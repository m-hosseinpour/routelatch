// Package instance owns the canonical instance directory and, for the one
// release that follows R13, the legacy-lock migration window described in
// docs/contracts.md §7.5.
//
// All coordination files (state.lock, app.lock, app.owner and the management
// socket) are derived from the selected state/data directory, never from
// XDG_RUNTIME_DIR. That is what makes a desktop session and an SSH session see
// the same lock and socket. The legacy set below is deliberately limited to
// the selected data directory so two independent instances cannot block each
// other (R-19).
package instance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// runUserRoot is /run/user in production; tests replace it so the fallback is
// exercised without depending on the host's runtime directory.
var runUserRoot = "/run/user"

// Dir returns the canonical instance directory for a state or data root.
func Dir(root string) string {
	return filepath.Join(root, "run")
}

// Legacy is one R12 coordination path and the management socket that belongs
// to it. During the migration window the engine probes and holds the lock and
// dials the socket only when the lock's state cannot be verified.
type Legacy struct {
	Lock   string
	Socket string
}

// LegacyPaths returns the R12 lock locations for the selected instance.
//
// With --data-dir the set is that directory only: <root>/state.lock and
// <root>/run/control.sock. A default instance additionally knows the no-runtime
// layout (<stateDir>/state.lock) plus the runtime locations R12 used for the
// default: $XDG_RUNTIME_DIR/routelatch/state.lock when the variable is set, and
// /run/user/<uid>/routelatch/state.lock when that runtime root exists and is
// different. Duplicate textual paths are removed; the (dev,ino) dedupe happens
// after opening.
func LegacyPaths(dataDir, stateDir, xdgRuntime string, uid int) []Legacy {
	absClean := func(path string) string {
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return abs
	}
	var out []Legacy
	if dataDir != "" {
		root := absClean(dataDir)
		return []Legacy{{
			Lock:   filepath.Join(root, "state.lock"),
			Socket: filepath.Join(Dir(root), "control.sock"),
		}}
	}
	root := absClean(stateDir)
	out = append(out, Legacy{
		Lock:   filepath.Join(root, "state.lock"),
		Socket: filepath.Join(Dir(root), "control.sock"),
	})
	runtimeRoot := ""
	if xdgRuntime != "" {
		runtimeRoot = absClean(xdgRuntime)
	}
	runUser := filepath.Join(runUserRoot, strconv.Itoa(uid))
	seen := map[string]bool{filepath.Join(root, "state.lock"): true}
	addRuntime := func(base string) {
		if base == "" {
			return
		}
		lock := filepath.Join(base, "routelatch", "state.lock")
		if seen[lock] {
			return
		}
		seen[lock] = true
		out = append(out, Legacy{Lock: lock, Socket: filepath.Join(base, "routelatch", "control.sock")})
	}
	addRuntime(runtimeRoot)
	if info, err := os.Stat(runUser); err == nil && info.IsDir() {
		addRuntime(runUser)
	}
	return out
}

// Locks is the held legacy-lock set. Close releases every descriptor; process
// death releases them as well. It is safe to call Close more than once.
type Locks struct {
	files []*os.File
}

// Close releases the legacy locks. Its error is informational: process exit
// would release them anyway.
func (l *Locks) Close() error {
	if l == nil {
		return nil
	}
	var first error
	for _, f := range l.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	l.files = nil
	return first
}

// ExitError is a documented migration-window outcome. Exit is 4 when an older
// engine owns one of the locks (even in mode stopped) and 6 when the legacy
// state cannot be verified without weakening the single-instance invariant.
// The embedded Failure keeps the JSON error kind in the frozen set.
type ExitError struct {
	Exit    contract.ExitCode
	Failure *contract.Failure
}

func (e *ExitError) Error() string { return e.Failure.Error() }
func (e *ExitError) Unwrap() error { return e.Failure }

func legacyExit(code contract.ExitCode, message string, cause error) *ExitError {
	return &ExitError{
		Exit: code,
		Failure: &contract.Failure{
			Kind: contract.StateLocked, Path: contract.PathLocal, Op: "instance.legacy",
			Message: message, Err: cause,
		},
	}
}

// AcquireLegacy probes, creates and holds the R12 locks for one instance. It
// must be called after the canonical state lock is held, before any other
// startup work. On failure it releases everything it acquired.
func AcquireLegacy(ctx context.Context, entries []Legacy) (*Locks, error) {
	locks := &Locks{}
	seen := map[inode]bool{}
	var unverifiable []struct {
		entry  Legacy
		reason error
	}
	fail := func(code contract.ExitCode, message string, cause error) (*Locks, error) {
		_ = locks.Close()
		return nil, legacyExit(code, message, cause)
	}

	for _, raw := range entries {
		if err := ctx.Err(); err != nil {
			_ = locks.Close()
			return nil, &contract.Failure{
				Kind: contract.Canceled, Path: contract.PathLocal, Op: "instance.legacy",
				Message: "operation canceled", Err: err,
			}
		}
		lockPath, err := filepath.Abs(raw.Lock)
		if err != nil {
			unverifiable = append(unverifiable, struct {
				entry  Legacy
				reason error
			}{raw, err})
			continue
		}
		lockPath = filepath.Clean(lockPath)

		info, err := os.Lstat(lockPath)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fail(contract.ExitConflict,
					fmt.Sprintf("legacy lock %s is a symlink; refusing to follow it", lockPath), nil)
			}
			if !info.Mode().IsRegular() {
				return fail(contract.ExitConflict,
					fmt.Sprintf("legacy lock %s is not a regular file", lockPath), nil)
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
				return fail(contract.ExitConflict,
					fmt.Sprintf("legacy lock %s is owned by uid %d; refusing to use it", lockPath, st.Uid), nil)
			}
		case errors.Is(err, os.ErrNotExist):
			if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
				unverifiable = append(unverifiable, struct {
					entry  Legacy
					reason error
				}{raw, err})
				continue
			}
		default:
			unverifiable = append(unverifiable, struct {
				entry  Legacy
				reason error
			}{raw, err})
			continue
		}

		// Open with O_NOFOLLOW even after the lstat: the path may be replaced
		// between the two calls, and following a symlink there would defeat the
		// check above.
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if err != nil {
			unverifiable = append(unverifiable, struct {
				entry  Legacy
				reason error
			}{raw, err})
			continue
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			_ = f.Close()
			return fail(contract.ExitConflict,
				fmt.Sprintf("legacy lock %s is not a regular file; refusing to use it", lockPath), err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
			_ = f.Close()
			return fail(contract.ExitConflict,
				fmt.Sprintf("legacy lock %s is not owned by the current uid; refusing to use it", lockPath), nil)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if fi.Mode().Perm() != 0o600 {
			// New files are created 0600. An existing R12 lock is already 0600;
			// do not chmod a live old engine's coordination file just because we
			// probed it.
			if err := f.Chmod(0o600); err != nil {
				_ = f.Close()
				unverifiable = append(unverifiable, struct {
					entry  Legacy
					reason error
				}{raw, err})
				continue
			}
		}
		key := inode{dev: st.Dev, ino: st.Ino}
		if seen[key] {
			// The same file is reachable through another legacy path (for
			// example XDG_RUNTIME_DIR equals /run/user/<uid>); the held
			// descriptor already covers it.
			_ = f.Close()
			continue
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
				return fail(contract.ExitAlreadyRunning,
					"an older RouteLatch instance owns this data dir; stop or upgrade it before starting this version", err)
			}
			unverifiable = append(unverifiable, struct {
				entry  Legacy
				reason error
			}{raw, err})
			continue
		}
		seen[key] = true
		locks.files = append(locks.files, f)
	}

	var lastReason error
	for _, item := range unverifiable {
		if socketAnswers(item.entry.Socket) {
			return fail(contract.ExitAlreadyRunning,
				"an older RouteLatch instance owns this data dir; stop or upgrade it before starting this version", item.reason)
		}
		lastReason = item.reason
	}
	if len(unverifiable) > 0 {
		return fail(contract.ExitConflict,
			fmt.Sprintf("stop the old RouteLatch instance explicitly before starting; cannot verify its legacy lock %s", unverifiable[0].entry.Lock), lastReason)
	}
	return locks, nil
}

type inode struct {
	dev uint64
	ino uint64
}

// socketAnswers reports whether something is serving the legacy management
// socket. A successful dial is enough: the socket exists only while the old
// engine is alive, and its peer permissions are the uid's.
func socketAnswers(path string) bool {
	if path == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
