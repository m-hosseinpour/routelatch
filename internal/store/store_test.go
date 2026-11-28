package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	// D21: the pre-migration copy keeps the old document but drops proposal
	// history, so an empty proposals array is removed too.
	wantBackup := `{"mode":"all-proxy","policy_version":3,"rules":[],"sources":[],"records":[],"health":[]}`
	if string(backup) != wantBackup {
		t.Fatalf("pre-migration backup = %s", backup)
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

// legacyStateDocument is the pre-D21 state shape: it names one visited
// destination in its proposal history.
type legacyStateDocument struct {
	contract.State
	Proposals []contract.Proposal `json:"proposals"`
}

// legacyProposalState is a version-1 state document an older build could have
// written. The schema version stays literal: it is historical evidence, not the
// version this build writes.
func legacyProposalState(canary string) legacyStateDocument {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	return legacyStateDocument{
		State: contract.State{
			SchemaVersion: 1,
			PolicyVersion: 1,
			Mode:          contract.ModeTargeted,
			Rules:         []contract.Rule{},
			Sources:       []contract.Source{},
			Records:       []contract.Record{},
			Health:        []contract.ExitHealth{},
			UpdatedAt:     now,
		},
		Proposals: []contract.Proposal{{
			ID:   "p-legacy",
			Kind: contract.ProposalRoute,
			Destination: contract.Destination{
				Kind: contract.DestDomain, Host: canary, Port: 443,
			},
			Evidence: []contract.Evidence{{
				Kind: contract.EvidenceDirectRefused, At: now,
				Address: canary + ":443", ProbeKind: contract.ProbeDirectTCP,
				Outcome: contract.OutcomeRefused,
			}},
			Status:    contract.ProposalPending,
			CreatedAt: now,
			ExpiresAt: now.Add(contract.ProposalRetention),
		}},
	}
}

func assertFileLacks(t *testing.T, path, canary string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("%s contains the visited destination", filepath.Base(path))
	}
}

func TestProposalHistoryIsNeverPersisted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	canary := "rl-canary-store.invalid"
	raw, err := json.MarshalIndent(legacyProposalState(canary), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Paths().State, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	snap, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent {
		t.Fatalf("loaded schema = %d", snap.State.SchemaVersion)
	}

	// The next write drops the history from the live file and from its backup.
	if _, err := s.Mutate(ctx, func(st *contract.State) error {
		st.Mode = contract.ModeDirectAll
		st.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertFileLacks(t, s.Paths().State, canary)
	assertFileLacks(t, s.Paths().State+".bak", canary)

	ref, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFileLacks(t, ref.Path, canary)
}

// legacyBackupChecksum is the pre-D21 checksum for the legacyProposalState
// fixture, computed from HEAD's State struct (which still had Proposals
// between Health and Desktop). Keeping it literal makes the test fail if the
// legacy canonical field order drifts.
const legacyBackupChecksum = "sha256:6807d967443db94d4ac7f386d95cb952bf622193bc9b61d1e0492dfbec65557e"

func TestLegacyBackupProposalHistoryIsNotRestored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	canary := "rl-canary-backup.invalid"
	st := legacyProposalState(canary)
	payload, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	// An old backup's checksum covers the proposal payload too; the reader
	// verifies that shape and then discards the proposals (D21).
	canonical, err := legacyCanonicalState(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != legacyBackupChecksum {
		t.Fatalf("legacy canonical shape drifted: %s", got)
	}
	path := writeLegacyBackup(t, s, canonical, legacyBackupChecksum)

	_, _, err = s.InspectBackup(path)
	if err != nil {
		t.Fatalf("legacy backup was rejected: %v", err)
	}
	if _, err := s.RestorePath(ctx, path); err != nil {
		t.Fatalf("legacy backup could not be restored: %v", err)
	}
	assertFileLacks(t, s.Paths().State, canary)
}

func TestLegacyBackupTamperRejected(t *testing.T) {
	canary := "rl-canary-backup.invalid"
	payload, err := json.Marshal(legacyProposalState(canary))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"proposal destination": bytes.Replace(payload, []byte(canary), []byte("tampered.invalid"), 1),
		"state field":          bytes.Replace(payload, []byte(`"policy_version":1`), []byte(`"policy_version":2`), 1),
	}
	for name, tampered := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(tampered, payload) {
				t.Fatal("fixture did not change")
			}
			s := newTestStore(t)
			path := writeLegacyBackup(t, s, tampered, legacyBackupChecksum)
			_, _, err := s.InspectBackup(path)
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
				t.Fatalf("tampered legacy backup err = %v, want state_corrupt", err)
			}
		})
	}
}

func writeLegacyBackup(t *testing.T, s *Store, state []byte, checksum string) string {
	t.Helper()
	return writeLegacyBackupSchema(t, s, state, checksum, contract.SchemaVersionCurrent)
}

func writeLegacyBackupSchema(t *testing.T, s *Store, state []byte, checksum string, schema contract.SchemaVersion) string {
	t.Helper()
	env := backupEnvelope{
		SchemaVersion: schema,
		CreatedAt:     time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		Checksum:      checksum,
		State:         state,
	}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBackupDir(s.Paths().Backup); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Paths().Backup, "state-legacy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeprecatedStateDropsProposalHistory(t *testing.T) {
	s := newTestStore(t)
	canary := "rl-canary-migrate.invalid"
	st := legacyProposalState(canary)
	st.SchemaVersion = 0
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Paths().State, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent {
		t.Fatalf("migrated schema = %d", snap.State.SchemaVersion)
	}
	// The pre-migration backup keeps the recovered state but not the history.
	assertFileLacks(t, s.Paths().State+".bak", canary)
	assertFileLacks(t, s.Paths().State, canary)
}

// openScrubState is a legacy state document in the real pre-D21 field order:
// proposals sits between health and desktop. The exact-bytes assertion proves
// the middle-member removal keeps every other field in place.
func openScrubState(canary string) (legacy, scrubbed string) {
	legacy = `{
  "schema_version": 1,
  "policy_version": 9,
  "mode": "all-proxy",
  "rules": [],
  "sources": [],
  "records": [],
  "health": [],
  "proposals": [
    {
      "id": "p-legacy",
      "destination": {"kind": "domain", "host": "` + canary + `", "port": 443}
    }
  ],
  "desktop": {"applied": false},
  "updated_at": "2026-11-17T12:00:00Z"
}`
	scrubbed = `{
  "schema_version": 1,
  "policy_version": 9,
  "mode": "all-proxy",
  "rules": [],
  "sources": [],
  "records": [],
  "health": [],

  "desktop": {"applied": false},
  "updated_at": "2026-11-17T12:00:00Z"
}`
	return legacy, scrubbed
}

func TestRemoveTopLevelMemberPositions(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"first", `{"proposals":[1],"keep":2}`, `{"keep":2}`},
		{"middle", `{"a":1,"proposals":[1],"b":2}`, `{"a":1,"b":2}`},
		{"last", `{"a":1,"proposals":[1]}`, `{"a":1}`},
		{"only", `{"proposals":[1]}`, `{}`},
		{"indented middle", "{\n  \"a\": 1,\n  \"proposals\": [\n    {\"x\": \"}\"}\n  ],\n  \"b\": 2\n}", "{\n  \"a\": 1,\n\n  \"b\": 2\n}"},
		{"nested untouched", `{"a":{"proposals":[1]},"b":2}`, `{"a":{"proposals":[1]},"b":2}`},
		{"duplicate keys", `{"proposals":[1],"a":1,"proposals":[2]}`, `{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := removeAllTopLevelMembers([]byte(tc.in), "proposals")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if !json.Valid(got) {
				t.Fatalf("result is not valid JSON: %q", got)
			}
		})
	}
}

func TestOpenScrubsLegacyProposalResidue(t *testing.T) {
	s := newTestStore(t)
	paths := s.Paths()
	canary := "rl-canary-residue.invalid"
	legacy, scrubbed := openScrubState(canary)
	if err := os.WriteFile(paths.State, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.State+".bak", []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(legacyProposalState(canary))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := legacyCanonicalState(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	backupPath := writeLegacyBackupSchema(t, s, payload, "sha256:"+hex.EncodeToString(sum[:]), 1)
	beforeEntries, err := os.ReadDir(paths.Backup)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Residue().Rewritten; got != 3 {
		t.Fatalf("scrubbed %d file(s), want 3 (%+v)", got, reopened.Residue())
	}
	if got, _ := os.ReadFile(paths.State); string(got) != scrubbed {
		t.Fatalf("state bytes changed beyond the proposals member:\n%s", got)
	}
	if got, _ := os.ReadFile(paths.State + ".bak"); string(got) != scrubbed {
		t.Fatalf(".bak bytes changed beyond the proposals member:\n%s", got)
	}
	assertFileLacks(t, backupPath, canary)
	if raw, err := os.ReadFile(backupPath); err != nil || strings.Contains(string(raw), `"proposals"`) {
		t.Fatalf("backup still carries the proposals key: %v", err)
	}
	if _, _, err := readBackup(backupPath); err != nil {
		t.Fatalf("scrubbed backup no longer verifies: %v", err)
	}
	var env backupEnvelope
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 1 || env.CreatedAt.IsZero() {
		t.Fatalf("scrubbed v1 envelope changed: %+v", env)
	}
	var payloadCheck struct {
		SchemaVersion contract.SchemaVersion `json:"schema_version"`
	}
	if err := json.Unmarshal(env.State, &payloadCheck); err != nil {
		t.Fatal(err)
	}
	if payloadCheck.SchemaVersion != env.SchemaVersion {
		t.Fatalf("scrubbed payload schema %d disagrees with envelope %d", payloadCheck.SchemaVersion, env.SchemaVersion)
	}
	afterEntries, err := os.ReadDir(paths.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("scrub changed the backup file count: %d -> %d", len(beforeEntries), len(afterEntries))
	}

	// A second open finds nothing to rewrite.
	again, err := Open(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Residue().Rewritten; got != 0 {
		t.Fatalf("second open rewrote %d file(s)", got)
	}
	if len(again.Residue().Warnings) != 0 {
		t.Fatalf("second open reported warnings: %v", again.Residue().Warnings)
	}
	if got, _ := os.ReadFile(paths.State); string(got) != scrubbed {
		t.Fatalf("second open changed state bytes")
	}
	if got, _ := os.ReadFile(backupPath); strings.Contains(string(got), `"proposals"`) {
		t.Fatalf("second open reintroduced the proposals key")
	}
}

func TestOpenLeavesUnparseableResidueUntouched(t *testing.T) {
	s := newTestStore(t)
	paths := s.Paths()
	badStateBak := []byte("{not json")
	if err := os.WriteFile(paths.State+".bak", badStateBak, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackupDir(paths.Backup); err != nil {
		t.Fatal(err)
	}
	badBackup := filepath.Join(paths.Backup, "state-bad.json")
	if err := os.WriteFile(badBackup, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Residue().Rewritten; got != 0 {
		t.Fatalf("rewrote %d unparseable file(s)", got)
	}
	if got := len(reopened.Residue().Warnings); got < 2 {
		t.Fatalf("warnings = %v, want at least the two unparseable files", reopened.Residue().Warnings)
	}
	if got, _ := os.ReadFile(paths.State + ".bak"); !strings.EqualFold(string(got), string(badStateBak)) {
		t.Fatalf(".bak was changed: %q", got)
	}
	if got, _ := os.ReadFile(badBackup); string(got) != "not json" {
		t.Fatalf("backup was changed: %q", got)
	}
}

func TestOpenScrubsLegacyTempResidue(t *testing.T) {
	s := newTestStore(t)
	paths := s.Paths()
	stateCanary := "rl-canary-state-temp.invalid"
	backupCanary := "rl-canary-backup-temp.invalid"
	legacy, scrubbed := openScrubState(stateCanary)
	stateTemp := filepath.Join(paths.StateDir, "state.json.tmp-999999")
	if err := os.WriteFile(stateTemp, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(legacyProposalState(backupCanary))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := legacyCanonicalState(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	origin := writeLegacyBackup(t, s, payload, "sha256:"+hex.EncodeToString(sum[:]))
	backupTemp := filepath.Join(paths.Backup, "state-20260101T000000.000000000Z.json.tmp-999999")
	if err := os.Rename(origin, backupTemp); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Residue().Rewritten; got != 2 {
		t.Fatalf("scrubbed %d temp file(s), want 2 (%+v)", got, reopened.Residue())
	}
	if got, _ := os.ReadFile(stateTemp); string(got) != scrubbed {
		t.Fatalf("state temp not scrubbed: %s", got)
	}
	assertFileLacks(t, backupTemp, backupCanary)
	if _, err := os.Stat(stateTemp); err != nil {
		t.Fatal("the scrub deleted the state temp instead of rewriting it")
	}
	if _, err := os.Stat(backupTemp); err != nil {
		t.Fatal("the scrub deleted the backup temp instead of rewriting it")
	}
}

func writeConfigFile(t *testing.T, s *Store, raw string) {
	t.Helper()
	if err := os.WriteFile(s.Paths().Config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultConfigSeedsEgressTargets(t *testing.T) {
	want := "tls://cloudflare.com:443,tls://www.gstatic.com:443"
	if got := strings.Join(DefaultConfig().Probes.Targets, ","); got != want {
		t.Fatalf("default targets = %q, want %q", got, want)
	}

	s := newTestStore(t)
	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snap.Config.Probes.Targets, ","); got != want {
		t.Fatalf("loaded targets = %q, want %q", got, want)
	}
}

func TestConfigWithoutTargetsKeyKeepsDefaults(t *testing.T) {
	s := newTestStore(t)
	writeConfigFile(t, s, `{"schema_version":1}`)
	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(snap.Config.Probes.Targets, ","); got != strings.Join(DefaultProbeTargets, ",") {
		t.Fatalf("targets = %q, want the defaults", got)
	}
}

func TestExplicitEmptyTargetsStayEmpty(t *testing.T) {
	s := newTestStore(t)
	writeConfigFile(t, s, `{"schema_version":1,"probes":{"targets":[]}}`)
	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Config.Probes.Targets) != 0 {
		t.Fatalf("targets = %v, want an explicitly empty list", snap.Config.Probes.Targets)
	}
}

func ruleIDs(rules []contract.Rule) []string {
	out := make([]string, len(rules))
	for i, rule := range rules {
		out[i] = string(rule.ID)
	}
	return out
}

func writeVersionedState(t *testing.T, s *Store, st contract.State) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Paths().State, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func migrationRules() []contract.Rule {
	return []contract.Rule{
		{ID: "r-old", Kind: contract.RuleDomain, Host: "old.example", Origin: contract.RuleOriginUser},
		{ID: "r-mid", Kind: contract.RuleDomainSuffix, Host: "mid.example", Origin: contract.RuleOriginUser},
		{ID: "r-new", Kind: contract.RuleDomain, Host: "new.example", Origin: contract.RuleOriginUser},
	}
}

func TestV1RulesMigrateToNewestFirstOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	st := contract.State{
		SchemaVersion: 1, PolicyVersion: 9, Mode: contract.ModeTargeted,
		Rules: migrationRules(), Sources: []contract.Source{}, Records: []contract.Record{},
		Health: []contract.ExitHealth{}, UpdatedAt: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC),
	}
	legacy := writeVersionedState(t, s, st)

	snap, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent {
		t.Fatalf("migrated schema = %d, want %d", snap.State.SchemaVersion, contract.SchemaVersionCurrent)
	}
	if got, want := ruleIDs(snap.State.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated rule order = %v, want %v", got, want)
	}
	// The migration only moves the list; contents and policy generation stay.
	for i, want := range migrationRules() {
		got := snap.State.Rules[len(snap.State.Rules)-1-i]
		if got != want {
			t.Fatalf("migrated rule %d = %+v, want %+v", i, got, want)
		}
	}
	if snap.State.PolicyVersion != 9 {
		t.Fatalf("policy version = %d, want 9 (order is not a routing change)", snap.State.PolicyVersion)
	}
	backup, err := os.ReadFile(s.Paths().State + ".bak")
	if err != nil {
		t.Fatalf("pre-migration backup missing: %v", err)
	}
	if string(backup) != string(legacy) {
		t.Fatalf("pre-migration backup changed the old document:\n%s", backup)
	}

	// A second load finds version 2 and must not reverse again or rewrite.
	migrated := readStateFile(t, s)
	if snap2, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	} else if got, want := ruleIDs(snap2.State.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second load rule order = %v, want %v", got, want)
	}
	if again := readStateFile(t, s); string(again) != string(migrated) {
		t.Fatal("a version-2 state was rewritten on the next load")
	}
}

func TestV2RulesAreNotReversed(t *testing.T) {
	s := newTestStore(t)
	st := contract.State{
		SchemaVersion: contract.SchemaVersionCurrent, PolicyVersion: 4, Mode: contract.ModeTargeted,
		Rules: migrationRules(), Sources: []contract.Source{}, Records: []contract.Record{},
		Health: []contract.ExitHealth{}, UpdatedAt: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC),
	}
	raw := writeVersionedState(t, s, st)

	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ruleIDs(snap.State.Rules), []string{"r-old", "r-mid", "r-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("version-2 rule order = %v, want untouched %v", got, want)
	}
	if after := readStateFile(t, s); string(after) != string(raw) {
		t.Fatal("a current-schema state was rewritten")
	}
}

func TestVersionZeroRulesMigrateToNewestFirst(t *testing.T) {
	s := newTestStore(t)
	st := contract.State{
		PolicyVersion: 3, Mode: "", Rules: migrationRules(),
		Sources: []contract.Source{}, Records: []contract.Record{}, Health: []contract.ExitHealth{},
	}
	writeVersionedState(t, s, st)

	snap, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ruleIDs(snap.State.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated rule order = %v, want %v", got, want)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent || snap.State.Mode != contract.ModeTargeted {
		t.Fatalf("migrated state = schema %d mode %q", snap.State.SchemaVersion, snap.State.Mode)
	}
}

// TestRuleReorderNeedsPolicyVersion pins the guard the migration relies on: the
// order is part of the stored rule set, so an order-only mutation without a new
// policy version is refused instead of being mistaken for no change.
func TestRuleReorderNeedsPolicyVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Mutate(ctx, func(st *contract.State) error {
		st.Rules = migrationRules()
		st.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.Mutate(ctx, func(st *contract.State) error {
		st.Rules[0], st.Rules[2] = st.Rules[2], st.Rules[0]
		return nil
	})
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Internal {
		t.Fatalf("reorder without a policy version err = %v, want internal", err)
	}

	snap, err := s.Mutate(ctx, func(st *contract.State) error {
		st.Rules[0], st.Rules[2] = st.Rules[2], st.Rules[0]
		st.PolicyVersion++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ruleIDs(snap.State.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered rule order = %v, want %v", got, want)
	}
}

// TestV1BackupRestoresAndMigratesRules proves a backup written by the previous
// schema still verifies (its checksum covers the recorded version) and gets the
// same newest-first migration on restore.
func TestV1BackupRestoresAndMigratesRules(t *testing.T) {
	s := newTestStore(t)
	st := contract.State{
		SchemaVersion: 1, PolicyVersion: 6, Mode: contract.ModeTargeted,
		Rules: migrationRules(), Sources: []contract.Source{}, Records: []contract.Record{},
		Health: []contract.ExitHealth{}, UpdatedAt: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC),
	}
	payload, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalState(st)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	path := writeLegacyBackup(t, s, payload, "sha256:"+hex.EncodeToString(sum[:]))

	inspected, got, err := s.InspectBackup(path)
	if err != nil {
		t.Fatalf("v1 backup did not verify: %v", err)
	}
	if inspected.Checksum != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum = %q", inspected.Checksum)
	}
	if ids, want := ruleIDs(got.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("inspected rule order = %v, want %v", ids, want)
	}
	restored, err := s.RestorePath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ruleIDs(restored.State.Rules), []string{"r-new", "r-mid", "r-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored rule order = %v, want %v", got, want)
	}
	if restored.State.SchemaVersion != contract.SchemaVersionCurrent {
		t.Fatalf("restored schema = %d", restored.State.SchemaVersion)
	}
}
