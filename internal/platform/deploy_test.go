package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployUnitIsAUserServiceWithLifecycleOnly(t *testing.T) {
	unitBytes, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", "routelatch.service"))
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	unit := string(unitBytes)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"Type=simple",
		"ExecStart=%h/.local/bin/routelatch run",
		"Restart=on-failure",
		"RestartSec=2s",
		"RestartPreventExitStatus=4 6 10",
		"KillMode=mixed",
		"TimeoutStopSec=20s",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"User=", "Group=", "PrivateNetwork=", "ProtectHome=", "RestrictAddressFamilies=",
		"Environment=HTTP_PROXY", "Environment=HTTPS_PROXY", "Environment=ALL_PROXY",
		"ExecStart=/usr/bin/sudo", "ExecStart=/bin/su",
	} {
		for _, line := range strings.Split(unit, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.Contains(line, forbidden) {
				t.Errorf("active unit line contains %q: %s", forbidden, line)
			}
		}
	}
}

func TestDeployReadmeDocumentsCommandsWithoutExecutingThem(t *testing.T) {
	readmeBytes, err := os.ReadFile(filepath.Join("..", "..", "deploy", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(readmeBytes)
	for _, want := range []string{
		"systemctl --user enable --now routelatch.service",
		"systemctl --user disable --now routelatch.service",
		"routelatch desktop proxy restore",
		"$HOME/.config/systemd/user/routelatch.service",
		"require root",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README is missing %q", want)
		}
	}
}
