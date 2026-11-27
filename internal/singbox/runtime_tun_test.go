package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"routelatch/internal/contract"
)

func TestSetDefaultInterfaceWaitsForExits(t *testing.T) {
	rt, err := New(Options{ConfigDir: t.TempDir(), Executable: "/nonexistent/sing-box"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetDefaultInterface("eth0"); err != nil {
		t.Fatalf("set without exits: %v", err)
	}
	rt.mu.Lock()
	got := rt.opts.DefaultInterface
	rt.mu.Unlock()
	if got != "eth0" {
		t.Fatalf("default_interface=%q, want eth0", got)
	}
	if err := rt.SetDefaultInterface(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	rt.mu.Lock()
	got = rt.opts.DefaultInterface
	rt.mu.Unlock()
	if got != "" {
		t.Fatalf("default_interface=%q, want empty", got)
	}
}

// TestStoredDefaultInterfaceAppliesToLaterSync pins finding #3: the interface
// stored while no hysteria2 exit exists is used by the next Sync, so an exit
// added after TUN enable still gets route.default_interface (§10.6).
func TestStoredDefaultInterfaceAppliesToLaterSync(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(Options{ConfigDir: dir, Executable: "/nonexistent/sing-box"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetDefaultInterface("eth0"); err != nil {
		t.Fatalf("set before exits: %v", err)
	}
	// Sync writes the generated config before it tries to start the executable;
	// the missing executable makes the call fail after the file exists.
	err = rt.Sync(context.Background(), []contract.SidecarExit{testExit("rl-late")})
	if err == nil {
		t.Fatal("Sync with a missing executable succeeded")
	}
	raw, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var doc struct {
		Route struct {
			DefaultInterface string `json:"default_interface"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	if doc.Route.DefaultInterface != "eth0" {
		t.Fatalf("default_interface=%q, want the interface stored before the exit was added", doc.Route.DefaultInterface)
	}
}
