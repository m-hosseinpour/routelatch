package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceDirIsCanonicalForDefaultData(t *testing.T) {
	cfg := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	runtimeA := filepath.Join(t.TempDir(), "desktop")
	runtimeB := filepath.Join(t.TempDir(), "ssh")
	pathsA, err := ResolvePathsFor("", cfg, state, runtimeA, home)
	if err != nil {
		t.Fatal(err)
	}
	pathsB, err := ResolvePathsFor("", cfg, state, runtimeB, home)
	if err != nil {
		t.Fatal(err)
	}
	instanceDir := filepath.Join(state, "routelatch", "run")
	for _, paths := range []Paths{pathsA, pathsB} {
		if paths.InstanceDir != instanceDir || paths.RuntimeDir != instanceDir {
			t.Fatalf("instance dir = %s / %s, want %s", paths.InstanceDir, paths.RuntimeDir, instanceDir)
		}
		if paths.Lock != filepath.Join(instanceDir, "state.lock") {
			t.Fatalf("lock = %s", paths.Lock)
		}
		if paths.Socket != filepath.Join(instanceDir, "control.sock") {
			t.Fatalf("socket = %s", paths.Socket)
		}
	}
	// A differing XDG_RUNTIME_DIR changes only which legacy lock is watched,
	// never the canonical lock or socket.
	wantLegacy := []string{
		filepath.Join(state, "routelatch", "state.lock"),
		filepath.Join(runtimeA, "routelatch", "state.lock"),
	}
	for _, want := range wantLegacy {
		found := false
		for _, item := range pathsA.LegacyLocks {
			if item.Lock == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("legacy set %+v is missing %s", pathsA.LegacyLocks, want)
		}
	}
	if pathsA.Lock != pathsB.Lock || pathsA.Socket != pathsB.Socket {
		t.Fatalf("desktop and SSH resolved different canonical paths: %s/%s vs %s/%s",
			pathsA.Lock, pathsA.Socket, pathsB.Lock, pathsB.Socket)
	}
}

func TestInstanceDirForExplicitDataDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := ResolvePathsFor(root, "/ignored/config", "/ignored/state", "/run/user/ignored", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instanceDir := filepath.Join(root, "run")
	if paths.InstanceDir != instanceDir || paths.RuntimeDir != instanceDir {
		t.Fatalf("instance dir = %s / %s", paths.InstanceDir, paths.RuntimeDir)
	}
	if paths.Lock != filepath.Join(instanceDir, "state.lock") || paths.Socket != filepath.Join(instanceDir, "control.sock") {
		t.Fatalf("lock/socket = %s / %s", paths.Lock, paths.Socket)
	}
	if len(paths.LegacyLocks) != 1 || paths.LegacyLocks[0].Lock != filepath.Join(root, "state.lock") {
		t.Fatalf("legacy set = %+v, want only the same data dir", paths.LegacyLocks)
	}
	if paths.ConfigDir != root || paths.StateDir != root || paths.Config != filepath.Join(root, "config.json") ||
		paths.State != filepath.Join(root, "state.json") || paths.Backup != filepath.Join(root, "backup") ||
		paths.LogDir != filepath.Join(root, "log") {
		t.Fatalf("data-dir paths moved: %+v", paths)
	}
}

func TestLegacySetsOfTwoDataDirsDoNotOverlap(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	pa, err := ResolvePathsFor(a, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := ResolvePathsFor(b, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if pa.Lock == pb.Lock || pa.Socket == pb.Socket {
		t.Fatal("two data dirs share a canonical lock or socket")
	}
	if len(pa.LegacyLocks) != 1 || len(pb.LegacyLocks) != 1 || pa.LegacyLocks[0].Lock == pb.LegacyLocks[0].Lock {
		t.Fatalf("legacy sets overlap: %+v / %+v", pa.LegacyLocks, pb.LegacyLocks)
	}
}
