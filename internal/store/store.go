package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"routelatch/internal/contract"
)

// Options configures a Store. Paths is required; Now is optional.
type Options struct {
	Paths Paths
	Now   func() time.Time
	// AllowInsecure is used only by the offline doctor, which must be able to
	// report bad permissions instead of failing before it can inspect them.
	AllowInsecure bool
}

// Store implements contract.Store. It is safe for concurrent use inside one
// process; the file lock is what excludes a second process.
type Store struct {
	paths Paths
	now   func() time.Time
	mu    sync.Mutex

	lock *processLock
}

var _ contract.Store = (*Store)(nil)

// Open resolves and creates the directories with the documented permissions.
// It fails closed when an existing state directory or file is reachable by
// group or other users.
func Open(opts Options) (*Store, error) {
	if opts.Paths.State == "" || opts.Paths.Config == "" {
		return nil, internalFailure("store paths are incomplete; set --data-dir or XDG variables")
	}
	s := &Store{paths: opts.Paths, now: opts.Now}
	if s.now == nil {
		s.now = time.Now
	}
	for _, dir := range []string{s.paths.ConfigDir, s.paths.StateDir, s.paths.RuntimeDir} {
		if dir == "" {
			continue
		}
		if err := ensureDir(dir); err != nil {
			return nil, err
		}
	}
	if !opts.AllowInsecure {
		for _, dir := range []string{s.paths.ConfigDir, s.paths.StateDir, s.paths.RuntimeDir} {
			if err := checkPrivateDir(dir); err != nil {
				return nil, err
			}
		}
		if err := checkPrivateFile(s.paths.State); err != nil {
			return nil, err
		}
		if err := checkPrivateFile(s.paths.Config); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Paths returns the store's absolute locations.
func (s *Store) Paths() Paths { return s.paths }

// Acquire takes the process-lifetime instance lock. A second holder receives
// contract.StateLocked and must not touch any file. The returned Lock is
// released by Release or by process death (flock).
func (s *Store) Acquire(ctx context.Context) (*Lock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil, stateLocked("this process already owns the state lock")
	}
	if err := ctx.Err(); err != nil {
		return nil, canceledFailure(err)
	}
	l, err := acquireFileLock(s.paths.Lock)
	if err != nil {
		return nil, err
	}
	s.lock = l
	return &Lock{store: s, file: l}, nil
}

// Lock is the process singleton token. Releasing it allows another instance.
type Lock struct {
	store *Store
	file  *processLock
	once  sync.Once
}

// Release frees the instance lock. It is idempotent.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.store.mu.Lock()
		if l.store.lock == l.file {
			l.store.lock = nil
		}
		l.store.mu.Unlock()
		_ = l.file.release()
	})
}

// Held reports whether this process currently owns the instance lock.
func (s *Store) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lock != nil
}

// Load implements contract.Store. It migrates an older schema forward (after
// writing a backup) and refuses a newer or unreadable schema without touching
// the file.
func (s *Store) Load(ctx context.Context) (contract.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, canceledFailure(err)
	}
	// A migration writes the file, so it takes the single-writer lock even
	// though an ordinary read does not. A second process that is mid-migration
	// makes this call fail with StateLocked rather than racing a writer.
	if s.stateNeedsMigration() {
		unlock, err := s.lockForWrite()
		if err != nil {
			return contract.Snapshot{}, err
		}
		defer unlock()
	}
	return s.loadLocked(ctx)
}

func (s *Store) stateNeedsMigration() bool {
	raw, err := os.ReadFile(s.paths.State)
	if err != nil {
		return false
	}
	var probe struct {
		SchemaVersion contract.SchemaVersion `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.SchemaVersion < contract.SchemaVersionCurrent
}

func (s *Store) loadLocked(ctx context.Context) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, canceledFailure(err)
	}
	cfg, err := s.readConfigLocked()
	if err != nil {
		return contract.Snapshot{}, err
	}
	st, err := s.readStateLocked(ctx)
	if err != nil {
		return contract.Snapshot{}, err
	}
	return contract.Snapshot{Config: cfg, State: st}, nil
}

// Mutate implements contract.Store. It takes the single-writer lock for the
// whole read-modify-write, applies fn to a deep copy and commits atomically.
func (s *Store) Mutate(ctx context.Context, fn func(*contract.State) error) (contract.Snapshot, error) {
	if fn == nil {
		return contract.Snapshot{}, internalFailure("state mutation callback is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, canceledFailure(err)
	}
	unlock, err := s.lockForWrite()
	if err != nil {
		return contract.Snapshot{}, err
	}
	defer unlock()

	cfg, err := s.readConfigLocked()
	if err != nil {
		return contract.Snapshot{}, err
	}
	current, err := s.readStateLocked(ctx)
	if err != nil {
		return contract.Snapshot{}, err
	}
	next, err := cloneState(current)
	if err != nil {
		return contract.Snapshot{}, internalFailure("state copy failed")
	}
	if err := fn(&next); err != nil {
		return contract.Snapshot{}, err
	}
	next.SchemaVersion = contract.SchemaVersionCurrent
	if err := policyVersionAdvanced(current, next); err != nil {
		return contract.Snapshot{}, err
	}
	next.UpdatedAt = s.now().UTC()
	if err := s.writeStateLocked(current, next); err != nil {
		return contract.Snapshot{}, err
	}
	return contract.Snapshot{Config: cfg, State: next}, nil
}

// MutateConfig implements contract.Store for config.json. Listener changes take
// effect only on the next service start.
func (s *Store) MutateConfig(ctx context.Context, fn func(*contract.Config) error) (contract.Snapshot, error) {
	if fn == nil {
		return contract.Snapshot{}, internalFailure("config mutation callback is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, canceledFailure(err)
	}
	unlock, err := s.lockForWrite()
	if err != nil {
		return contract.Snapshot{}, err
	}
	defer unlock()

	currentCfg, err := s.readConfigLocked()
	if err != nil {
		return contract.Snapshot{}, err
	}
	nextCfg := currentCfg
	if err := fn(&nextCfg); err != nil {
		return contract.Snapshot{}, err
	}
	nextCfg.SchemaVersion = contract.SchemaVersionCurrent
	if err := s.writeConfigLocked(currentCfg, nextCfg); err != nil {
		return contract.Snapshot{}, err
	}
	st, err := s.readStateLocked(ctx)
	if err != nil {
		return contract.Snapshot{}, err
	}
	return contract.Snapshot{Config: nextCfg, State: st}, nil
}

// lockForWrite acquires the advisory lock unless this process already holds the
// instance lock; the returned func releases a transient lock.
func (s *Store) lockForWrite() (func(), error) {
	if s.lock != nil {
		return func() {}, nil
	}
	l, err := acquireFileLock(s.paths.Lock)
	if err != nil {
		return nil, err
	}
	return func() { _ = l.release() }, nil
}

// readStateLocked reads, validates and migrates state. Migration writes the
// migrated document only after a backup copy exists.
func (s *Store) readStateLocked(ctx context.Context) (contract.State, error) {
	raw, err := os.ReadFile(s.paths.State)
	if errors.Is(err, os.ErrNotExist) {
		return defaultState(s.now()), nil
	}
	if err != nil {
		return contract.State{}, corruptFailure("state file cannot be read", err)
	}
	var probe struct {
		SchemaVersion contract.SchemaVersion `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return contract.State{}, corruptFailure("state file is not valid JSON; it was not changed", err)
	}
	if probe.SchemaVersion > contract.SchemaVersionCurrent {
		return contract.State{}, corruptFailure("state was written by a newer RouteLatch; refusing to guess its structure", nil)
	}
	var st contract.State
	if err := json.Unmarshal(raw, &st); err != nil {
		return contract.State{}, corruptFailure("state file could not be decoded; it was not changed", err)
	}
	if probe.SchemaVersion < contract.SchemaVersionCurrent {
		migrated, err := migrateState(probe.SchemaVersion, st)
		if err != nil {
			return contract.State{}, err
		}
		if err := s.copyBackupLocked(raw); err != nil {
			return contract.State{}, err
		}
		if err := s.writeStateLocked(st, migrated); err != nil {
			return contract.State{}, err
		}
		st = migrated
	}
	if st.SchemaVersion != contract.SchemaVersionCurrent {
		return contract.State{}, corruptFailure("state schema version is not current after migration", nil)
	}
	if st.Mode == "" {
		st.Mode = contract.ModeTargeted
	}
	if err := ctx.Err(); err != nil {
		return contract.State{}, canceledFailure(err)
	}
	return st, nil
}

// readConfigLocked returns defaults when config.json is absent. It never
// rewrites a file it could not decode.
func (s *Store) readConfigLocked() (contract.Config, error) {
	raw, err := os.ReadFile(s.paths.Config)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return contract.Config{}, corruptFailure("config file cannot be read", err)
	}
	var probe struct {
		SchemaVersion contract.SchemaVersion `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return contract.Config{}, corruptFailure("config file is not valid JSON; it was not changed", err)
	}
	if probe.SchemaVersion > contract.SchemaVersionCurrent {
		return contract.Config{}, corruptFailure("config was written by a newer RouteLatch", nil)
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return contract.Config{}, corruptFailure("config file could not be decoded; it was not changed", err)
	}
	if probe.SchemaVersion < contract.SchemaVersionCurrent {
		cfg.SchemaVersion = contract.SchemaVersionCurrent
	}
	cfg = withConfigDefaults(cfg)
	return cfg, nil
}

// DefaultConfig is the documented v1 configuration.
func DefaultConfig() contract.Config {
	return contract.Config{
		SchemaVersion: contract.SchemaVersionCurrent,
		HTTPListen:    "127.0.0.1:8317",
		SOCKSListen:   "127.0.0.1:8318",
		LogLevel:      "info",
		Diagnostics: contract.DiagnosticsConfig{
			Enabled: true, QueueLimit: 32, MaxConcurrent: 1, RetentionDays: 30,
		},
		Probes: contract.ProbeConfig{
			Targets: []string{}, TimeoutMS: int(contract.ProbeTimeout / time.Millisecond),
			CooldownBase: int(contract.ProbeCooldownBase / time.Second),
			CooldownMax:  int(contract.ProbeCooldownMax / time.Second),
		},
	}
}

func withConfigDefaults(cfg contract.Config) contract.Config {
	def := DefaultConfig()
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = def.HTTPListen
	}
	if cfg.SOCKSListen == "" {
		cfg.SOCKSListen = def.SOCKSListen
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = def.LogLevel
	}
	if cfg.Diagnostics.QueueLimit <= 0 {
		cfg.Diagnostics.QueueLimit = def.Diagnostics.QueueLimit
	}
	if cfg.Diagnostics.MaxConcurrent <= 0 {
		cfg.Diagnostics.MaxConcurrent = def.Diagnostics.MaxConcurrent
	}
	if cfg.Diagnostics.RetentionDays <= 0 {
		cfg.Diagnostics.RetentionDays = def.Diagnostics.RetentionDays
	}
	if cfg.Probes.TimeoutMS <= 0 {
		cfg.Probes.TimeoutMS = def.Probes.TimeoutMS
	}
	if cfg.Probes.CooldownBase <= 0 {
		cfg.Probes.CooldownBase = def.Probes.CooldownBase
	}
	if cfg.Probes.CooldownMax <= 0 {
		cfg.Probes.CooldownMax = def.Probes.CooldownMax
	}
	if cfg.Probes.Targets == nil {
		cfg.Probes.Targets = []string{}
	}
	return cfg
}

func defaultState(now time.Time) contract.State {
	return contract.State{
		SchemaVersion: contract.SchemaVersionCurrent,
		PolicyVersion: 1,
		Mode:          contract.ModeTargeted,
		Rules:         []contract.Rule{},
		Sources:       []contract.Source{},
		Records:       []contract.Record{},
		Health:        []contract.ExitHealth{},
		Proposals:     []contract.Proposal{},
		UpdatedAt:     now.UTC(),
	}
}

// policyVersionAdvanced enforces §4.4 step 3: a change to mode, rules or the
// manual exit selection must carry a strictly greater policy version.
func policyVersionAdvanced(before, after contract.State) error {
	changed := before.Mode != after.Mode ||
		before.SelectedExit != after.SelectedExit ||
		!reflect.DeepEqual(before.Rules, after.Rules)
	if changed && after.PolicyVersion <= before.PolicyVersion {
		return &contract.Failure{
			Kind: contract.Internal, Path: contract.PathLocal, Op: "store.mutate",
			Message: "a mode, rule or selection change must advance policy_version; the change was not written",
		}
	}
	return nil
}

func migrateState(from contract.SchemaVersion, st contract.State) (contract.State, error) {
	switch from {
	case 0:
		// Version 0 is a state file written before schema_version was added.
		// The field set did not change between those files and v1, so the
		// migration is only the version stamp plus documented defaults.
		st.SchemaVersion = contract.SchemaVersionCurrent
		if st.Mode == "" {
			st.Mode = contract.ModeTargeted
		}
		return st, nil
	case contract.SchemaVersionCurrent:
		return st, nil
	default:
		return contract.State{}, corruptFailure(fmt.Sprintf("no migration path from state schema %d", from), nil)
	}
}

func cloneState(st contract.State) (contract.State, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return contract.State{}, err
	}
	var out contract.State
	if err := json.Unmarshal(raw, &out); err != nil {
		return contract.State{}, err
	}
	return out, nil
}

func ensureDir(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		// Never repair an existing directory silently: Open's permission check
		// must fail closed, and doctor must be able to report the real mode.
		return nil
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return internalFailure("cannot create data directory", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return internalFailure("cannot set data directory permissions", err)
	}
	return nil
}

func checkPrivateDir(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return internalFailure("cannot inspect state directory", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return &contract.Failure{
			Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "store.permissions",
			Message: "state directory is accessible by group or other users (" +
				info.Mode().Perm().String() + "); run: chmod 700 " + filepath.Base(path),
		}
	}
	return nil
}

func checkPrivateFile(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return internalFailure("cannot inspect state file", err)
	}
	if !info.Mode().IsRegular() {
		return corruptFailure("state path is not a regular file", nil)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return &contract.Failure{
			Kind: contract.PolicyDenied, Path: contract.PathLocal, Op: "store.permissions",
			Message: "state file is accessible by group or other users (" +
				info.Mode().Perm().String() + "); run: chmod 600 " + filepath.Base(path),
		}
	}
	return nil
}

func corruptFailure(message string, err error) *contract.Failure {
	return &contract.Failure{
		Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "store.load",
		Message: message, Err: err,
	}
}

func stateLocked(message string) *contract.Failure {
	return &contract.Failure{
		Kind: contract.StateLocked, Path: contract.PathLocal, Op: "store.lock",
		Message: message,
	}
}

func internalFailure(message string, cause ...error) *contract.Failure {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return &contract.Failure{
		Kind: contract.Internal, Path: contract.PathLocal, Op: "store", Message: message, Err: err,
	}
}

func canceledFailure(err error) *contract.Failure {
	return &contract.Failure{
		Kind: contract.Canceled, Path: contract.PathLocal, Op: "store", Message: "operation canceled", Err: err,
	}
}

// processLock is one flock-held file descriptor.
type processLock struct {
	file *os.File
}

func acquireFileLock(path string) (*processLock, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, internalFailure("cannot open the state lock", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, internalFailure("cannot set state lock permissions", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, stateLocked("another RouteLatch instance owns this data dir")
		}
		return nil, internalFailure("cannot lock the state file", err)
	}
	return &processLock{file: f}, nil
}

func (l *processLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	cerr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return cerr
}
