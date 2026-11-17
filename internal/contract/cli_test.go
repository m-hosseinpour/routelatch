package contract_test

import (
	"testing"

	"routelatch/internal/contract"
)

func TestEveryErrorKindHasAnExitCode(t *testing.T) {
	kinds := contract.ErrorKinds()
	seen := make(map[contract.ErrorKind]bool, len(kinds))
	for _, kind := range kinds {
		if kind == "" {
			t.Fatal("empty error kind in the closed set")
		}
		if seen[kind] {
			t.Fatalf("duplicate error kind %q", kind)
		}
		seen[kind] = true
		if _, ok := contract.ExitCodeFor[kind]; !ok {
			t.Fatalf("error kind %q has no exit code", kind)
		}
	}
	if len(contract.ExitCodeFor) != len(seen) {
		t.Fatalf("ExitCodeFor has %d entries for %d kinds; the set is not closed", len(contract.ExitCodeFor), len(seen))
	}
}

func TestLookupAndToolKindsMapAsDocumented(t *testing.T) {
	if got := contract.ExitCodeFor[contract.NotFound]; got != contract.ExitNotFound {
		t.Fatalf("not_found exit = %d, want %d", got, contract.ExitNotFound)
	}
	if got := contract.ExitCodeFor[contract.ExternalToolFailed]; got != contract.ExitError {
		t.Fatalf("external_tool_failed exit = %d, want %d", got, contract.ExitError)
	}
}
