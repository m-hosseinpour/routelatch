package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// Marker states from docs/contracts.md §7.5.
const (
	MarkerStarting = "starting"
	MarkerReady    = "ready"
	MarkerStopping = "stopping"
)

// Listeners is the two loopback traffic listener addresses.
type Listeners struct {
	HTTP  string `json:"http"`
	SOCKS string `json:"socks"`
}

// Record is the exact app.owner content: one compact JSON object and one
// trailing newline.
type Record struct {
	State      string    `json:"state"`
	Generation uint64    `json:"generation"`
	PID        int       `json:"pid"`
	StartedAt  string    `json:"started_at"`
	DataDir    string    `json:"data_dir"`
	Listeners  Listeners `json:"listeners"`
	LogPath    string    `json:"log_path"`
}

func validState(state string) bool {
	return state == MarkerStarting || state == MarkerReady || state == MarkerStopping
}

// TunShutdownIntentPath is the runtime marker the supervisor writes before a
// real Quit/stop. A supervised engine that receives SIGTERM without this marker
// is being replaced by a restart, so it leaves TUN coverage fail-closed for the
// replacement to adopt. The file carries no destination or state (D21).
func TunShutdownIntentPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "tun-shutdown.intent")
}

// EncodeRecord is compact JSON plus exactly one newline.
func EncodeRecord(r Record) ([]byte, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// ParseRecord accepts only a complete, valid record with exactly one trailing
// newline. A missing newline, extra bytes, truncation or embedded raw newline
// is invalid.
func ParseRecord(raw []byte) (Record, error) {
	var r Record
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return r, fmt.Errorf("marker is missing its single trailing newline")
	}
	body := raw[:len(raw)-1]
	if bytes.ContainsAny(body, "\r\n") || len(body) == 0 {
		return r, fmt.Errorf("marker is truncated or has extra bytes")
	}
	if !json.Valid(body) {
		return r, fmt.Errorf("marker is not valid JSON")
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, err
	}
	if !validState(r.State) {
		return r, fmt.Errorf("marker state %q is not starting, ready or stopping", r.State)
	}
	if r.Generation == 0 || r.PID <= 1 || r.StartedAt == "" {
		return r, fmt.Errorf("marker identity fields are incomplete")
	}
	if !filepath.IsAbs(r.DataDir) || !filepath.IsAbs(r.LogPath) {
		return r, fmt.Errorf("marker data_dir and log_path must be absolute")
	}
	return r, nil
}

type inodeKey struct {
	dev uint64
	ino uint64
}

// Owner is the held app.owner descriptor. The descriptor, flock and record
// content stay together; the file is never unlinked by RouteLatch.
type Owner struct {
	path string
	f    *os.File
	key  inodeKey
}

func inodeOf(info os.FileInfo) (inodeKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return inodeKey{}, false
	}
	return inodeKey{dev: uint64(st.Dev), ino: st.Ino}, true
}

// OpenOwner opens the stable app.owner inode, creating it once with
// O_CREAT|O_EXCL|O_NOFOLLOW when absent. A symlink, non-regular file, foreign
// owner or unreadable path is rejected without being followed or replaced.
func OpenOwner(path string) (*Owner, error) {
	if err := ensureInstanceDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	openExisting := func() (*Owner, error) {
		owner, err := openOwnerExisting(path)
		if err != nil {
			return nil, err
		}
		if info, err := owner.f.Stat(); err == nil && info.Mode().Perm() != 0o600 {
			if err := owner.f.Chmod(0o600); err != nil {
				owner.Close()
				return nil, err
			}
		}
		return owner, nil
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("app.owner is a symlink; refusing to follow it")
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("app.owner is not a regular file")
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
			return nil, fmt.Errorf("app.owner is not owned by the current uid")
		}
		return openExisting()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return openExisting()
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	key, ok := inodeOf(info)
	if !ok {
		_ = f.Close()
		return nil, fmt.Errorf("app.owner has no inode identity")
	}
	return &Owner{path: path, f: f, key: key}, nil
}

// openOwnerExisting opens an app.owner that must already exist. The second-run
// path uses it so a missing marker is reported as repair-pending, never
// created by a reader.
func openOwnerExisting(path string) (*Owner, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("app.owner is not a regular file")
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("app.owner is owned by uid %d, not the current uid", st.Uid)
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	key, ok := inodeOf(fi)
	if !ok {
		_ = f.Close()
		return nil, fmt.Errorf("app.owner has no inode identity")
	}
	return &Owner{path: path, f: f, key: key}, nil
}

// Write replaces the record in place: ftruncate(0), one pwrite at offset zero
// and fsync. It never uses temp+rename, so the flock and content cannot split.
func (o *Owner) Write(r Record) error {
	if o == nil || o.f == nil {
		return fmt.Errorf("app.owner is not open")
	}
	raw, err := EncodeRecord(r)
	if err != nil {
		return err
	}
	if err := o.f.Truncate(0); err != nil {
		return err
	}
	n, err := o.f.WriteAt(raw, 0)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return fmt.Errorf("short app.owner write")
	}
	return o.f.Sync()
}

// Read reads the record through the held descriptor, so a replaced path cannot
// redirect it.
func (o *Owner) Read() (Record, error) {
	if o == nil || o.f == nil {
		return Record{}, fmt.Errorf("app.owner is not open")
	}
	info, err := o.f.Stat()
	if err != nil {
		return Record{}, err
	}
	if info.Size() <= 0 {
		return Record{}, fmt.Errorf("app.owner is empty")
	}
	raw := make([]byte, info.Size())
	n, err := o.f.ReadAt(raw, 0)
	if err != nil && n == 0 {
		return Record{}, err
	}
	return ParseRecord(raw[:n])
}

// Lock takes the second flock with a short retry. It does not write.
func (o *Owner) Lock(retries int, delay time.Duration) error {
	if retries < 1 {
		retries = 1
	}
	var last error
	for i := 0; i < retries; i++ {
		err := syscall.Flock(int(o.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		last = err
		if i+1 < retries {
			time.Sleep(delay)
		}
	}
	return last
}

// Close releases the descriptor without unlinking the owner file.
func (o *Owner) Close() {
	if o == nil || o.f == nil {
		return
	}
	_ = o.f.Close()
	o.f = nil
}

// Verify checks that the path still names the same inode and is still a
// regular file owned by the current uid. It returns ErrDeleted when the path
// is gone.
func (o *Owner) Verify() error {
	info, err := os.Lstat(o.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrDeleted
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("app.owner is no longer a regular file")
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("app.owner owner changed to uid %d", st.Uid)
	}
	key, ok := inodeOf(info)
	if !ok || key != o.key {
		return fmt.Errorf("app.owner inode was replaced")
	}
	return nil
}

// ErrDeleted means the marker path was removed while the supervisor lives.
var ErrDeleted = errors.New("app owner marker was deleted")

// RecreateAfterDelete creates a fresh inode, writes state, takes the flock,
// writes state again, and only then closes the old descriptor. The ordering
// guarantees that the path is never present without a lock and never carries
// an early ready state.
func (o *Owner) RecreateAfterDelete(r Record, retries int, delay time.Duration) error {
	raw, err := EncodeRecord(r)
	if err != nil {
		return err
	}
	if retries < 1 {
		retries = 1
	}
	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Someone recreated the path in the window; never adopt it.
			return fmt.Errorf("app.owner path reappeared during repair")
		}
		return err
	}
	info, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return statErr
	}
	key, ok := inodeOf(info)
	if !ok {
		_ = f.Close()
		return fmt.Errorf("new app.owner has no inode identity")
	}
	write := func() error {
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.WriteAt(raw, 0); err != nil {
			return err
		}
		return f.Sync()
	}
	var last error
	for attempt := 0; attempt < retries; attempt++ {
		if err := write(); err == nil {
			last = nil
			break
		} else {
			last = err
			time.Sleep(delay)
		}
	}
	if last != nil {
		_ = f.Close()
		return last
	}
	locked := false
	for attempt := 0; attempt < retries; attempt++ {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			locked = true
			break
		} else {
			last = err
			time.Sleep(delay)
		}
	}
	if !locked {
		_ = f.Close()
		return last
	}
	// Write the current state again after the lock is held, so a reader that
	// opened the new inode after our create sees a locked record.
	if err := write(); err != nil {
		_ = f.Close()
		return err
	}
	old := o.f
	o.f = f
	o.key = key
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// AppLock is the singleton app-supervisor lock. It is never unlinked or
// replaced by RouteLatch.
type AppLock struct {
	path string
	f    *os.File
}

var ErrAppLocked = errors.New("another app supervisor owns app.lock")

// AcquireAppLock opens or creates app.lock with O_NOFOLLOW and takes the
// flock. A held lock returns ErrAppLocked.
func AcquireAppLock(path string) (*AppLock, error) {
	if err := ensureInstanceDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("app.lock is a symlink; refusing to follow it")
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("app.lock is not a regular file")
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
			return nil, fmt.Errorf("app.lock is not owned by the current uid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("app.lock is not a regular file")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAppLocked
		}
		return nil, err
	}
	return &AppLock{path: path, f: f}, nil
}

// Release frees app.lock. It never removes the file.
func (l *AppLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// ErrAppNotRunning means app.lock is absent or free: a new supervisor may
// start and overwrite the stale marker.
var ErrAppNotRunning = errors.New("no app supervisor owns app.lock")

// ExistingState reads the marker while another process holds app.lock. It
// implements the second-run rules: a stale owner lock, a missing/invalid
// marker, starting/stopping, a different data dir or a vetoing socket all mean
// exit 6; only a live owner flock with a complete ready record for this data
// dir can become already_running/0 (and even then the caller still vetoes with
// the socket).
func ExistingState(paths MarkerPaths) (*Owner, Record, error) {
	lock, err := probeAppLock(paths.AppLock)
	if err != nil {
		if errors.Is(err, ErrAppNotRunning) {
			return nil, Record{}, ErrAppNotRunning
		}
		return nil, Record{}, err
	}
	if lock {
		// app.lock is free: the marker is stale, never a success.
		return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
			"a previous app marker is stale; retry the start", "app_marker_stale", nil)
	}
	owner, err := openOwnerExisting(paths.AppOwner)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
				"the app marker is being repaired; retry shortly", "app_marker_repair_pending", err)
		}
		return nil, Record{}, markerInvalid(err)
	}
	// Taking the owner flock proves no live supervisor owns the marker.
	if err := syscall.Flock(int(owner.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(owner.f.Fd()), syscall.LOCK_UN)
		owner.Close()
		return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
			"a previous app marker is stale; retry the start", "app_marker_stale", nil)
	} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		owner.Close()
		return nil, Record{}, markerInvalid(err)
	}
	rec, err := owner.Read()
	if err != nil {
		owner.Close()
		return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
			"the app marker is incomplete; retry shortly", "app_marker_invalid", err)
	}
	if rec.State != MarkerReady {
		owner.Close()
		return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
			"the app is not ready yet; retry shortly", "app_marker_"+rec.State, nil)
	}
	if rec.DataDir != paths.DataDir {
		owner.Close()
		return nil, Record{}, conflictExit(contract.ExitConflict, "app.secondrun",
			"a different data directory's app is running; use the same --data-dir", "app_data_dir_mismatch", nil)
	}
	return owner, rec, nil
}

// MarkerPaths are the app coordination locations for one instance.
type MarkerPaths struct {
	InstanceDir string
	AppLock     string
	AppOwner    string
	Socket      string
	DataDir     string
	LogPath     string
	Config      string
}

// probeAppLock reports whether app.lock is currently free without creating it.
// It returns ErrAppNotRunning when the file does not exist.
func probeAppLock(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, ErrAppNotRunning
		}
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RejectHeadlessConflict refuses `run --headless` while an app supervisor owns
// app.lock, regardless of marker content.
func RejectHeadlessConflict(path string) error {
	free, err := probeAppLock(path)
	if errors.Is(err, ErrAppNotRunning) {
		return nil
	}
	if err != nil {
		return conflictExit(contract.ExitConflict, "run.headless",
			"the app lock path is invalid; refusing to start beside it", "app_marker_invalid", err)
	}
	if free {
		return nil
	}
	return conflictExit(contract.ExitAlreadyRunning, "run.headless",
		"the RouteLatch app (tray) is running for this data dir; stop it from the tray or use the same app", "app_running", nil)
}

// ProbeEngineLock reports whether state.lock is free. The caller must not hold
// it: the engine child takes it for its whole lifetime.
func ProbeEngineLock(path string) (bool, error) {
	if err := ensureInstanceDir(filepath.Dir(path)); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true, nil
}

// ensureInstanceDir creates the canonical instance directory 0700 and rejects
// a symlink or foreign-owner path. It is shared by both locks.
func ensureInstanceDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("instance directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("instance directory %s is a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("instance directory %s is not a directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("instance directory %s is not owned by the current uid", dir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// MarkerPathsFor derives the app paths from the store paths.
func MarkerPathsFor(instanceDir, dataDir, logPath, socket string) MarkerPaths {
	return MarkerPaths{
		InstanceDir: instanceDir,
		AppLock:     filepath.Join(instanceDir, "app.lock"),
		AppOwner:    filepath.Join(instanceDir, "app.owner"),
		Socket:      socket,
		DataDir:     dataDir,
		LogPath:     logPath,
	}
}
