package platform

import (
	"os"
	"path/filepath"
	"testing"
)

var (
	isolatedRoot string
	originalHome string
)

// TestMain gives every test a temporary HOME/XDG tree and an empty PATH. No
// test may read the real home directory or resolve a real desktop tool; the
// desktop tests always inject a fake runner.
func TestMain(m *testing.M) {
	originalHome = os.Getenv("HOME")
	root, err := os.MkdirTemp("", "routelatch-platform-test-")
	if err != nil {
		panic(err)
	}
	isolatedRoot = root
	for _, dir := range []string{"home", "config", "state", "runtime", "empty-path"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			panic(err)
		}
	}
	os.Setenv("HOME", filepath.Join(root, "home"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	os.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	os.Setenv("PATH", filepath.Join(root, "empty-path"))

	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
