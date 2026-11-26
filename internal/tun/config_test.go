package tun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixedCoverageConstants(t *testing.T) {
	if InterfaceName != "rl-tun0" || MTU != 1500 {
		t.Fatalf("interface=%q mtu=%d", InterfaceName, MTU)
	}
	if IPv4Gateway != "198.18.0.1/15" || IPv6Gateway != "fc00::1/64" {
		t.Fatalf("gateways=%q %q", IPv4Gateway, IPv6Gateway)
	}
	wantRoutes := []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}
	if len(HalfRoutes) != len(wantRoutes) {
		t.Fatalf("half routes=%v", HalfRoutes)
	}
	for i, want := range wantRoutes {
		if HalfRoutes[i] != want {
			t.Fatalf("half route %d=%q want %q", i, HalfRoutes[i], want)
		}
	}
	if ControlSocketPath != "/run/routelatch-tun/control.sock" || LeasePath != "/run/routelatch-tun/lease.json" {
		t.Fatalf("runtime paths changed: %q %q", ControlSocketPath, LeasePath)
	}
}

func TestLoadServiceConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tun.json")
	if err := os.WriteFile(path, []byte(`{"allowed_uid":1000,"socks_address":"127.0.0.1:1080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AllowedUID != 1000 || cfg.SocksAddress != "127.0.0.1:1080" {
		t.Fatalf("config=%+v", cfg)
	}

	if err := os.WriteFile(path, []byte(`{"allowed_uid":1000,"socks_address":"127.0.0.1:1080","interface":"evil0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServiceConfig(path); err == nil {
		t.Fatal("an unknown config field was accepted")
	}

	if err := os.WriteFile(path, []byte(`{"allowed_uid":-1,"socks_address":"127.0.0.1:1080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServiceConfig(path); err == nil {
		t.Fatal("a negative allowed uid was accepted")
	}

	if err := os.WriteFile(path, []byte(`{"allowed_uid":1000,"socks_address":"192.0.2.1:1080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServiceConfig(path); err == nil {
		t.Fatal("a non-loopback SOCKS address was accepted")
	}

	if _, err := LoadServiceConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing config was accepted")
	}
}

func TestParseSocksAddress(t *testing.T) {
	good := []struct {
		in   string
		host string
		port uint16
	}{
		{"127.0.0.1:1080", "127.0.0.1", 1080},
		{"127.5.6.7:1", "127.5.6.7", 1},
		{"[::1]:65535", "::1", 65535},
	}
	for _, tc := range good {
		host, port, err := ParseSocksAddress(tc.in)
		if err != nil || host != tc.host || port != tc.port {
			t.Fatalf("%q -> %q/%d err=%v", tc.in, host, port, err)
		}
	}
	bad := []string{
		"", "localhost:1080", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536",
		"192.0.2.1:1080", "0.0.0.0:1080", "[::]:1080", "[fe80::1%eth0]:1080",
		"127.0.0.1:http", "127.0.0.1:1080 ",
	}
	for _, in := range bad {
		if _, _, err := ParseSocksAddress(in); err == nil {
			t.Fatalf("%q was accepted", in)
		}
	}
}

func TestSafeTextStripsControlCharacters(t *testing.T) {
	if got := safeText("a\x1b[31mb\r\nc"); strings.ContainsAny(got, "\x1b\r\n") {
		t.Fatalf("control characters survived: %q", got)
	}
}
