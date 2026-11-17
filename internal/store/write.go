package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"routelatch/internal/contract"
)

// The write hooks exist so tests can inject a failure after the temp file has
// been fsynced but before it replaces the live file. Production values are the
// real syscalls; nothing outside this package can change them.
var (
	renameFile = os.Rename
	syncDir    = syncDirectory
)

func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeFileAtomic performs the documented sequence: same-directory temp file
// with O_EXCL and 0600, write, fsync, close, rename, fsync directory. On any
// error the temp file is removed and the target is untouched.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	// We hold the single-writer lock here, so any temp file for this target is
	// from a crashed process and can be removed (including a different pid).
	if stale, err := filepath.Glob(path + ".tmp-*"); err == nil {
		for _, old := range stale {
			_ = os.Remove(old)
		}
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return internalFailure("cannot create the temporary state file", err)
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return internalFailure("cannot set temporary state permissions", err)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return internalFailure("cannot write the temporary state file", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return internalFailure("cannot fsync the temporary state file", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return internalFailure("cannot close the temporary state file", err)
	}
	if err := renameFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return internalFailure("cannot atomically replace the state file", err)
	}
	if err := syncDir(dir); err != nil {
		// The rename already committed the new bytes; report the durability
		// failure instead of pretending it did not happen.
		return internalFailure("cannot fsync the state directory", err)
	}
	return nil
}

// writeStateLocked writes a new state document and keeps the previous, still
// valid document as state.json.bak. The backup is best effort; its failure does
// not block a commit the user already requested.
func (s *Store) writeStateLocked(previous, next interface{}) error {
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return internalFailure("cannot encode state", err)
	}
	if old, err := os.ReadFile(s.paths.State); err == nil && json.Valid(old) {
		_ = writeFileAtomic(s.paths.State+".bak", old)
	}
	return writeFileAtomic(s.paths.State, raw)
}

func (s *Store) writeConfigLocked(previous, next interface{}) error {
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return internalFailure("cannot encode config", err)
	}
	return writeFileAtomic(s.paths.Config, raw)
}

// copyBackupLocked is the mandatory pre-migration backup. Unlike the routine
// best-effort .bak in writeStateLocked, a failure here aborts the migration.
func (s *Store) copyBackupLocked(raw []byte) error {
	if err := writeFileAtomic(s.paths.State+".bak", raw); err != nil {
		return &contract.Failure{
			Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "store.migrate",
			Message: "cannot back up the old state before migration; the file was not changed",
			Err:     err,
		}
	}
	return nil
}

// ensureBackupDir creates the backup directory with 0700 permissions.
func ensureBackupDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return internalFailure("cannot create the backup directory", err)
	}
	return os.Chmod(path, 0o700)
}
