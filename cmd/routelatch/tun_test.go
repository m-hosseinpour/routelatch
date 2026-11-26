package main

import (
	"path/filepath"
	"testing"
)

func TestTunServiceEntrypointIsHiddenAndFailsClosed(t *testing.T) {
	dir := t.TempDir()

	// It takes no positional arguments: the installed unit passes only
	// --config, and no interface/route/domain can be requested.
	extra := runCLI(t, dir, "__tun-service", "extra")
	if extra.code != 2 {
		t.Fatalf("__tun-service extra exit = %d, want 2 (%+v)", extra.code, extra.env)
	}

	// A missing/untrusted config is refused before any listener or TUN is
	// created. The path is deliberately absent so this is safe on any host.
	missing := filepath.Join(t.TempDir(), "absent.json")
	refused := runCLI(t, dir, "__tun-service", "--config", missing)
	if refused.code != 1 {
		t.Fatalf("__tun-service untrusted config exit = %d, want 1", refused.code)
	}
}
