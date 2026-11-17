package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readStateFile(t *testing.T, s *Store) []byte {
	t.Helper()
	raw, err := os.ReadFile(s.Paths().State)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAtomicFailureLeavesNoPartialState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Mutate(ctx, func(st *contract.State) error {
		st.PolicyVersion++
		st.Mode = contract.ModeDirectAll
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readStateFile(t, s)

	original := renameFile
	renameFile = func(string, string) error { return errors.New("injected rename failure") }
	defer func() { renameFile = original }()

	_, err := s.Mutate(ctx, func(st *contract.State) error {
		st.PolicyVersion++
		st.Mode = contract.ModeProxyAll
		return nil
	})
	if err == nil {
		t.Fatal("mutation succeeded despite the injected rename failure")
	}
	after := readStateFile(t, s)
	if string(before) != string(after) {
		t.Fatal("live state changed after a failed atomic replace")
	}
	entries, err := os.ReadDir(filepath.Dir(s.Paths().State))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("partial temp file left behind: %s", entry.Name())
		}
	}
	if _, err := s.Load(ctx); err != nil {
		t.Fatalf("state is unreadable after failed write: %v", err)
	}
}

func TestCorruptStateFailsClosedAndIsNotOverwritten(t *testing.T) {
	s := newTestStore(t)
	corrupt := []byte("{ this is not json")
	if err := os.WriteFile(s.Paths().State, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := s.Load(ctx)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("Load err = %v, want state_corrupt", err)
	}
	_, err = s.Mutate(ctx, func(st *contract.State) error { st.Mode = contract.ModeDirectAll; return nil })
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("Mutate err = %v, want state_corrupt", err)
	}
	raw, err := os.ReadFile(s.Paths().State)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(corrupt) {
		t.Fatal("corrupt state was overwritten")
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	s := newTestStore(t)
	raw := []byte(`{"schema_version":99,"mode":"targeted","rules":[]}`)
	if err := os.WriteFile(s.Paths().State, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(context.Background())
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("Load err = %v, want state_corrupt", err)
	}
}

func TestSecondInstanceGetsStateLocked(t *testing.T) {
	a := newTestStore(t)
	b := newTestStore(t)
	b.paths = a.paths // same data dir
	ctx := context.Background()
	lock, err := a.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = b.Acquire(ctx)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("Acquire err = %v, want state_locked", err)
	}
	_, err = b.Mutate(ctx, func(st *contract.State) error { return nil })
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("Mutate err = %v, want state_locked", err)
	}
	// The owner can still write.
	if _, err := a.Mutate(ctx, func(st *contract.State) error { return nil }); err != nil {
		t.Fatalf("owner mutation failed: %v", err)
	}
}

func TestPermissionsAreCreatedPrivate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Mutate(ctx, func(st *contract.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"dir": s.Paths().StateDir, "file": s.Paths().State} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %v, want no group/other bits", name, info.Mode().Perm())
		}
	}
}

func TestDeprecatedStateMigratesAndBacksUp(t *testing.T) {
	s := newTestStore(t)
	legacy := []byte(`{"mode":"all-proxy","policy_version":3,"rules":[],"sources":[],"records":[],"health":[],"proposals":[]}`)
	if err := os.WriteFile(s.Paths().State, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent || snap.State.Mode != contract.ModeProxyAll {
		t.Fatalf("migrated state = %+v", snap.State)
	}
	backup, err := os.ReadFile(s.Paths().State + ".bak")
	if err != nil {
		t.Fatalf("pre-migration backup missing: %v", err)
	}
	if string(backup) != string(legacy) {
		t.Fatal("pre-migration backup does not contain the old document")
	}
	var onDisk contract.State
	raw := readStateFile(t, s)
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.SchemaVersion != contract.SchemaVersionCurrent {
		t.Fatalf("on-disk schema = %d", onDisk.SchemaVersion)
	}
}

func TestBackupAndRestore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	snap, err := s.Mutate(ctx, func(st *contract.State) error {
		st.PolicyVersion++
		st.Mode = contract.ModeDirectAll
		st.Rules = []contract.Rule{{ID: "r-1", Kind: contract.RuleDomain, Host: "example.com", Origin: contract.RuleOriginUser}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Checksum, "sha256:") {
		t.Fatalf("checksum = %q", ref.Checksum)
	}
	if _, err := s.Mutate(ctx, func(st *contract.State) error {
		st.PolicyVersion++
		st.Mode = contract.ModeTargeted
		st.Rules = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := s.Restore(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State.Mode != contract.ModeDirectAll || len(restored.State.Rules) != 1 {
		t.Fatalf("restored state = %+v", restored.State)
	}
	if restored.State.PolicyVersion != snap.State.PolicyVersion {
		t.Fatalf("restored policy version = %d, want %d", restored.State.PolicyVersion, snap.State.PolicyVersion)
	}

	// Tampering is refused.
	raw, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env["state"] = map[string]any{"schema_version": 1, "mode": "all-proxy"}
	tampered, _ := json.Marshal(env)
	if err := os.WriteFile(ref.Path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(ctx, ref); err == nil {
		t.Fatal("tampered backup was accepted")
	}
}

func TestRestoreRejectsPathOutsideBackupDir(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.RestorePath(ctx, outside)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("RestorePath err = %v, want conflict/state_locked", err)
	}
}
