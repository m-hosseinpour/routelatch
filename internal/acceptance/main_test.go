// Package acceptance drives the shipped RouteLatch binary as a real process
// against loopback-only peers. It is a client of the product: it never imports
// internal/control, internal/frontend or any other runtime package, and it
// talks to the service only through the documented CLI/management surface
// (docs/contracts.md §7).
//
// Each test gets an isolated temp HOME/XDG/data dir, loopback ports and its own
// exit fixtures: VLESS/Trojan exits run as child processes of this test binary
// (exactly the T1 technique, so the product process still holds one core) and
// the SOCKS5 exit is a small loopback server owned by the test.
package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// productBin is the RouteLatch binary under test. Built once in TestMain so
// every test exercises the same artifact through a real exec boundary.
var productBin string

func TestMain(m *testing.M) {
	// The VLESS/Trojan exit helpers are this same test binary re-executed; they
	// must not build the product again (they may run in a minimal environment).
	if os.Getenv("RL_ACCEPT_EXIT_HELPER") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "rl-acceptance-bin-")
	if err != nil {
		fmt.Printf("FAIL A13 cannot create temp dir for the product binary: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "routelatch")
	build := exec.Command("go", "build", "-o", bin, "./cmd/routelatch")
	build.Dir = filepath.Join("..", "..")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Printf("FAIL A13 cannot build the product binary: %v\n", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	productBin = bin
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// acceptanceItem maps one A-item to the report line the suite prints. The
// lines are printed to stdout so a plain `go test` run shows what passed.
type report struct{ t *testing.T }

func (r report) pass(item, format string, args ...any) {
	fmt.Printf("PASS %-4s %s\n", item, fmt.Sprintf(format, args...))
	r.t.Logf("PASS %s %s", item, fmt.Sprintf(format, args...))
}

func (r report) fail(item, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("FAIL %-4s %s\n", item, msg)
	r.t.Errorf("%s: %s", item, msg)
}

func (r report) note(item, format string, args ...any) {
	fmt.Printf("NOTE %-4s %s\n", item, fmt.Sprintf(format, args...))
	r.t.Logf("NOTE %s %s", item, fmt.Sprintf(format, args...))
}
