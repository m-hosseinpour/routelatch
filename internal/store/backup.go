package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"routelatch/internal/contract"
)

type backupEnvelope struct {
	SchemaVersion contract.SchemaVersion `json:"schema_version"`
	CreatedAt     time.Time              `json:"created_at"`
	Checksum      string                 `json:"checksum"`
	State         json.RawMessage        `json:"state"`
}

// Backup implements contract.Store. It writes one envelope with a checksum over
// canonical state bytes.
func (s *Store) Backup(ctx context.Context) (contract.BackupRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.BackupRef{}, canceledFailure(err)
	}
	unlock, err := s.lockForWrite()
	if err != nil {
		return contract.BackupRef{}, err
	}
	defer unlock()

	st, err := s.readStateLocked(ctx)
	if err != nil {
		return contract.BackupRef{}, err
	}
	return s.writeBackupLocked(st)
}

func (s *Store) writeBackupLocked(st contract.State) (contract.BackupRef, error) {
	if err := ensureBackupDir(s.paths.Backup); err != nil {
		return contract.BackupRef{}, err
	}
	canonical, err := canonicalState(st)
	if err != nil {
		return contract.BackupRef{}, internalFailure("cannot encode the backup payload", err)
	}
	sum := sha256.Sum256(canonical)
	env := backupEnvelope{
		SchemaVersion: contract.SchemaVersionCurrent,
		CreatedAt:     s.now().UTC(),
		Checksum:      "sha256:" + hex.EncodeToString(sum[:]),
		State:         canonical,
	}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return contract.BackupRef{}, internalFailure("cannot encode the backup envelope", err)
	}
	name := "state-" + env.CreatedAt.Format("20060102T150405.000000000Z") + ".json"
	path := filepath.Join(s.paths.Backup, name)
	if err := writeFileAtomic(path, raw); err != nil {
		return contract.BackupRef{}, err
	}
	return contract.BackupRef{
		Path: path, SchemaVersion: env.SchemaVersion,
		CreatedAt: env.CreatedAt, Checksum: env.Checksum,
	}, nil
}

// Restore implements contract.Store. It validates the envelope's path,
// checksum and schema, keeps a backup of the current state, then atomically
// replaces the live file. A failed validation changes nothing.
func (s *Store) Restore(ctx context.Context, ref contract.BackupRef) (contract.Snapshot, error) {
	if ref.Path == "" {
		return contract.Snapshot{}, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "store.restore",
			Message: "restore needs a backup file",
		}
	}
	path, err := s.safeBackupPath(ref.Path)
	if err != nil {
		return contract.Snapshot{}, err
	}
	st, env, err := readBackup(path)
	if err != nil {
		return contract.Snapshot{}, err
	}
	if ref.Checksum != "" && ref.Checksum != env.Checksum {
		return contract.Snapshot{}, corruptFailure("the backup checksum does not match the requested backup", nil)
	}
	return s.restoreState(ctx, st)
}

// RestorePath restores a backup named by an absolute path or a file inside the
// backup directory. It is the CLI-facing path form; the checksum inside the
// envelope is always verified.
func (s *Store) RestorePath(ctx context.Context, path string) (contract.Snapshot, error) {
	safe, err := s.safeBackupPath(path)
	if err != nil {
		return contract.Snapshot{}, err
	}
	st, _, err := readBackup(safe)
	if err != nil {
		return contract.Snapshot{}, err
	}
	return s.restoreState(ctx, st)
}

func (s *Store) restoreState(ctx context.Context, st contract.State) (contract.Snapshot, error) {
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

	if old, err := os.ReadFile(s.paths.State); err == nil && json.Valid(old) {
		_ = writeFileAtomic(s.paths.State+".bak", scrubProposalHistory(old))
	}
	st.SchemaVersion = contract.SchemaVersionCurrent
	if st.Mode == "" {
		st.Mode = contract.ModeTargeted
	}
	st.UpdatedAt = s.now().UTC()
	if err := s.writeStateLocked(st, st); err != nil {
		return contract.Snapshot{}, err
	}
	cfg, err := s.readConfigLocked()
	if err != nil {
		return contract.Snapshot{}, err
	}
	return contract.Snapshot{Config: cfg, State: st}, nil
}

// InspectBackup validates one backup and returns its state without changing
// anything. The service uses it to refuse an active restore in a process that
// already stopped the engine.
func (s *Store) InspectBackup(path string) (contract.BackupRef, contract.State, error) {
	safe, err := s.safeBackupPath(path)
	if err != nil {
		return contract.BackupRef{}, contract.State{}, err
	}
	st, env, err := readBackup(safe)
	if err != nil {
		return contract.BackupRef{}, contract.State{}, err
	}
	return contract.BackupRef{
		Path: safe, SchemaVersion: env.SchemaVersion, CreatedAt: env.CreatedAt, Checksum: env.Checksum,
	}, st, nil
}

// ListBackups returns the readable backups, newest first. It validates each
// envelope and skips unreadable ones.
func (s *Store) ListBackups() ([]contract.BackupRef, error) {
	entries, err := os.ReadDir(s.paths.Backup)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internalFailure("cannot list backups", err)
	}
	var refs []contract.BackupRef
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.paths.Backup, entry.Name())
		_, env, err := readBackup(path)
		if err != nil {
			continue
		}
		refs = append(refs, contract.BackupRef{
			Path: path, SchemaVersion: env.SchemaVersion,
			CreatedAt: env.CreatedAt, Checksum: env.Checksum,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].CreatedAt.After(refs[j].CreatedAt) })
	return refs, nil
}

func readBackup(path string) (contract.State, backupEnvelope, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return contract.State{}, backupEnvelope{}, &contract.Failure{
			Kind: contract.StateCorrupt, Path: contract.PathLocal, Op: "store.restore",
			Message: "backup file does not exist; nothing was changed",
		}
	}
	if err != nil {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup file cannot be read; nothing was changed", err)
	}
	var env backupEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup envelope is not valid JSON; nothing was changed", err)
	}
	if env.SchemaVersion > contract.SchemaVersionCurrent {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup was written by a newer RouteLatch; nothing was changed", nil)
	}
	var st contract.State
	if err := json.Unmarshal(env.State, &st); err != nil {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup state cannot be decoded; nothing was changed", err)
	}
	canonical, err := canonicalState(st)
	if err != nil {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup state cannot be canonicalized; nothing was changed", err)
	}
	sum := sha256.Sum256(canonical)
	want := "sha256:" + hex.EncodeToString(sum[:])
	verified := env.Checksum == want
	if !verified {
		// A pre-D21 backup hashed a payload that still carried proposal
		// history. Verify that shape too, then keep using the decoded state,
		// so the proposals are checked but never restored (D21).
		if legacy, legacyErr := legacyCanonicalState(env.State); legacyErr == nil {
			legacySum := sha256.Sum256(legacy)
			verified = env.Checksum == "sha256:"+hex.EncodeToString(legacySum[:])
		}
	}
	if !verified {
		return contract.State{}, backupEnvelope{}, corruptFailure("backup checksum does not match its payload; nothing was changed", nil)
	}
	migrated, err := migrateState(st.SchemaVersion, st)
	if err != nil {
		return contract.State{}, backupEnvelope{}, err
	}
	// An older backup may still name destinations in its proposal history; the
	// state type no longer has that field, so it is dropped on decode (D21).
	return migrated, env, nil
}

func canonicalState(st contract.State) ([]byte, error) {
	st.SchemaVersion = contract.SchemaVersionCurrent
	return json.Marshal(st)
}

// legacyBackupState is the pre-D21 state field order. A backup written before
// proposals became memory-only hashed a payload that included them; this shape
// reproduces that payload for checksum verification only (D21).
type legacyBackupState struct {
	SchemaVersion contract.SchemaVersion     `json:"schema_version"`
	PolicyVersion contract.PolicyVersion     `json:"policy_version"`
	Mode          contract.Mode              `json:"mode"`
	SelectedExit  contract.ExitID            `json:"selected_exit,omitempty"`
	Rules         []contract.Rule            `json:"rules"`
	Sources       []contract.Source          `json:"sources"`
	Records       []contract.Record          `json:"records"`
	Health        []contract.ExitHealth      `json:"health"`
	Proposals     []contract.Proposal        `json:"proposals"`
	Desktop       contract.DesktopProxyState `json:"desktop"`
	UpdatedAt     time.Time                  `json:"updated_at"`
}

func legacyCanonicalState(raw []byte) ([]byte, error) {
	var st legacyBackupState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	st.SchemaVersion = contract.SchemaVersionCurrent
	return json.Marshal(st)
}

func (s *Store) safeBackupPath(input string) (string, error) {
	dir, err := filepath.Abs(s.paths.Backup)
	if err != nil {
		return "", internalFailure("cannot resolve the backup directory", err)
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal, Op: "store.restore",
			Message: "backup path is not usable",
		}
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", &contract.Failure{
			Kind: contract.StateLocked, Path: contract.PathLocal, Op: "store.restore",
			Message: "restore only accepts a backup inside the backup directory; nothing was changed",
		}
	}
	// Resolve symlinks after the lexical check: a link inside the backup
	// directory must not read a file outside it.
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		if resolvedPath, err := filepath.EvalSymlinks(abs); err == nil {
			r, err := filepath.Rel(resolvedDir, resolvedPath)
			if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
				return "", &contract.Failure{
					Kind: contract.StateLocked, Path: contract.PathLocal, Op: "store.restore",
					Message: "restore resolved outside the backup directory; nothing was changed",
				}
			}
			return resolvedPath, nil
		}
	}
	return abs, nil
}
