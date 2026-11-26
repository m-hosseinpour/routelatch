package tun

import (
	"strings"
	"testing"
)

func TestServiceIdentityGuard(t *testing.T) {
	cfg := ServiceConfig{AllowedUID: 1000, SocksAddress: "127.0.0.1:1080"}

	if err := checkServiceIdentity(0, cfg); err == nil {
		t.Fatal("a root-run service was accepted")
	} else if !strings.Contains(err.Error(), "root") {
		t.Fatalf("root refusal is not explicit: %v", err)
	}
	if err := checkServiceIdentity(1001, cfg); err == nil {
		t.Fatal("a service running as another uid was accepted")
	}
	if err := checkServiceIdentity(1000, cfg); err != nil {
		t.Fatalf("the configured uid was refused: %v", err)
	}
}
