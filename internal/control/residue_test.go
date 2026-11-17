package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/store"
)

// TestLegacyResidueScrubIsLoggedAndReportedByDoctor covers the operator-facing
// side of the D21 residue scrub: the log gets counts only, and doctor names the
// file it could not parse or verify without printing a destination.
func TestLegacyResidueScrubIsLoggedAndReportedByDoctor(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlresidue")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	canary := "rl-canary-doctor.invalid"
	legacy := `{"schema_version":1,"policy_version":1,"mode":"targeted","rules":[],"sources":[],"records":[],"health":[],` +
		`"proposals":[{"id":"p-legacy","destination":{"kind":"domain","host":"` + canary + `","port":443}}]}`
	if err := os.WriteFile(paths.State, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.State+".bak", []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	residue := st.Residue()
	if residue.Rewritten != 1 || len(residue.Warnings) != 1 {
		t.Fatalf("residue = %+v, want one rewrite and one warning", residue)
	}
	if got, _ := os.ReadFile(paths.State); strings.Contains(string(got), canary) {
		t.Fatal("state still contains the legacy destination")
	}

	var logBuf bytes.Buffer
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: &logBuf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	logText := logBuf.String()
	if !strings.Contains(logText, "residue.scrub") || !strings.Contains(logText, "1 file(s)") {
		t.Fatalf("scrub count missing from the log: %q", logText)
	}
	if !strings.Contains(logText, "residue.warning") || strings.Contains(logText, canary) {
		t.Fatalf("residue warning log = %q", logText)
	}

	data, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var check *Check
	for i := range data.Checks {
		if data.Checks[i].Name == "proposal_residue" {
			check = &data.Checks[i]
		}
	}
	if check == nil {
		t.Fatalf("doctor did not report proposal_residue: %+v", data.Checks)
	}
	if check.Status != "warn" || !strings.Contains(check.Detail, filepath.Base(paths.State)+".bak") {
		t.Fatalf("proposal_residue = %+v", *check)
	}
	if strings.Contains(check.Detail, canary) {
		t.Fatal("doctor detail printed the legacy destination")
	}
}

func TestCorruptStateErrorCarriesResidueNote(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlresidue")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.State, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("NewService accepted a corrupt state")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateCorrupt {
		t.Fatalf("error = %v, want state_corrupt", err)
	}
	if !strings.Contains(failure.Message, "legacy proposal residue") ||
		!strings.Contains(failure.Message, "state.json is not readable JSON") {
		t.Fatalf("doctor cannot see the residue warning: %q", failure.Message)
	}
}
