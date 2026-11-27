package singbox

import "testing"

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
