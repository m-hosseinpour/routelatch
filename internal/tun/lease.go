package tun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// leaseRecord is the only persisted service state. It records the user's last
// explicit wish so a service restart in the same boot can rebuild coverage. A
// fresh boot clears /run, so nothing auto-enables before login; the engine's
// persisted config (R16-D) decides that. It carries no destination, domain or
// flow data (D21).
type leaseRecord struct {
	Desired   string `json:"desired"` // "on" | "off"
	BootID    string `json:"boot_id"`
	PID       int    `json:"pid"`
	Interface string `json:"interface"`
	Since     string `json:"since"`
}

func readBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *Service) readLease() (leaseRecord, bool) {
	b, err := os.ReadFile(s.leasePath)
	if err != nil {
		return leaseRecord{}, false
	}
	var rec leaseRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return leaseRecord{}, false
	}
	if rec.Desired != "on" && rec.Desired != "off" {
		return leaseRecord{}, false
	}
	return rec, true
}

// writeLease replaces the lease atomically with owner-only permissions. A
// partial file is never observed by a restarted service.
func (s *Service) writeLease(desired string) error {
	dir := filepath.Dir(s.leasePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create the TUN runtime directory: %w", err)
	}
	rec := leaseRecord{
		Desired:   desired,
		BootID:    s.bootID(),
		PID:       os.Getpid(),
		Interface: InterfaceName,
		Since:     s.now().UTC().Format(time.RFC3339Nano),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode the TUN lease: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".lease-*")
	if err != nil {
		return fmt.Errorf("write the TUN lease: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the TUN lease: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the TUN lease: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the TUN lease: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write the TUN lease: %w", err)
	}
	if err := os.Rename(name, s.leasePath); err != nil {
		return fmt.Errorf("publish the TUN lease: %w", err)
	}
	ok = true
	return nil
}
