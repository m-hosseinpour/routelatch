// Package setup installs and removes the privileged RouteLatch TUN helper
// (docs/contracts.md §10.4). It is reached only through the CLI: `sudo
// routelatch tun setup` and `sudo routelatch tun uninstall`. The management
// socket never calls it, the tray has no direct item, and the package contains
// no unit of its own (D26/D28).
//
// The installed helper is deliberately small and fixed: a root-owned copy of
// this binary under /usr/libexec/routelatch, a system unit with only
// CAP_NET_ADMIN, a socket unit, a two-field root-owned config and one
// tmpfiles.d entry that recreates the /run directory after boot. No interface,
// route, domain or user text is accepted or written.
package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/tun"
)

const (
	// ServiceUnitName and SocketUnitName are the only units setup creates.
	ServiceUnitName = "routelatch-tun.service"
	SocketUnitName  = "routelatch-tun.socket"

	// OwnershipMarker is the first-line comment that identifies a unit created
	// by `routelatch tun setup`. A unit without it is never overwritten or
	// removed unless --force is given, and never silently adopted.
	OwnershipMarker = "# Managed by RouteLatch tun setup"

	// DefaultSocksAddress is the documented default engine SOCKS listener. A
	// user may pass --socks for another loopback listener; nothing else is
	// accepted.
	DefaultSocksAddress = "127.0.0.1:8318"

	maxConfigBytes = 64 << 10
	maxUnitBytes   = 64 << 10
	unitMode       = 0o644
	tmpfilesMode   = 0o644
	binaryMode     = 0o755
	dirMode        = 0o755
	// The runtime directory is created by a root-owned tmpfiles.d entry and
	// owned by the allowed engine uid. 0755 keeps it traversable before the
	// service first starts (the socket itself stays 0600 and is owned by the
	// uid), and the service's RuntimeDirectory uses the same mode so a restart
	// does not silently change the boot-time layout.
	runtimeDirMode = 0o755
	socketWait     = 5 * time.Second

	// startLimitIntervalSec/startLimitBurst bound automatic restarts without
	// disabling the helper under a realistic burst of user actions. The H
	// acceptance run reached the old 5-per-30s limit with five starts in about
	// 30s (first enable, manual restart, SIGKILL restart, toggle, quit), after
	// which both units stayed failed until reset-failed. Twenty starts in five
	// minutes covers that sequence and ordinary quick toggling, while a crash
	// loop still stops after at most 20 attempts instead of restarting forever.
	startLimitIntervalSec = 300
	startLimitBurst       = 20
)

// tmpfilesName is the single boot-time directory entry setup owns. /run is
// cleared on boot, so a directory created only at setup time disappears before
// the socket unit starts; tmpfiles recreates it with the right owner and mode.
const tmpfilesName = "routelatch-tun.conf"

// Paths are the fixed installed locations. Tests replace every path with a
// private tree so no test writes /etc, /usr or /run.
type Paths struct {
	ServiceUnit string
	SocketUnit  string
	Config      string
	Binary      string
	Tmpfiles    string
	RuntimeDir  string
}

// DefaultPaths is the production layout from docs/contracts.md §10.4.
func DefaultPaths() Paths {
	return Paths{
		ServiceUnit: "/etc/systemd/system/" + ServiceUnitName,
		SocketUnit:  "/etc/systemd/system/" + SocketUnitName,
		Config:      tun.DefaultConfigPath,
		Binary:      "/usr/libexec/routelatch/tun-service",
		Tmpfiles:    "/etc/tmpfiles.d/" + tmpfilesName,
		RuntimeDir:  "/run/routelatch-tun",
	}
}

// Status is the JSON payload of `tun setup` and `tun uninstall`.
type Status struct {
	Unit       string   `json:"unit"`
	SocketUnit string   `json:"socket_unit"`
	Config     string   `json:"config"`
	Binary     string   `json:"binary"`
	Tmpfiles   string   `json:"tmpfiles"`
	AllowedUID int      `json:"allowed_uid"`
	Changed    bool     `json:"changed"`
	Managed    bool     `json:"managed"`
	Removed    []string `json:"removed,omitempty"`
}

// Manager owns the privileged install. Every external capability and
// filesystem root is injectable so tests never touch the real manager, the
// real kernel checks or /etc.
type Manager struct {
	Runner     platform.CommandRunner
	LookPath   func(string) (string, error)
	Getenv     func(string) string
	Euid       func() int
	Getuid     func() int
	Executable func() (string, error)
	// Owner reports a file's uid without following a symlink. nil uses the
	// real stat owner; tests replace it because their private files are owned
	// by the test uid while the fake process is root.
	Owner func(string) (int, error)
	Chown func(string, int, int) error
	// KernelVersion and TunDevice are the two platform prerequisites. Tests
	// replace them so setup logic is deterministic on any host.
	KernelVersion func() (major, minor int, err error)
	TunDevice     func() error
	SyncDir       func(string) error
	// Ready waits until the socket unit has published the control socket at
	// the documented path with the expected 0600 mode and allowed uid. Tests
	// replace it because a fake manager creates no real socket.
	Ready func(path string, uid int) error

	Paths        Paths
	Force        bool
	SocksAddress string
}

// fileSnapshot is the previous state of one file this run may replace. It is
// small except for the binary, which is only read when that file changes.
type fileSnapshot struct {
	Path    string
	Exists  bool
	Content []byte
	Mode    os.FileMode
}

type dirSnapshot struct {
	Path   string
	Exists bool
	Mode   os.FileMode
	UID    int
}

// Install converges the helper to the documented files and reports whether
// anything was written. It is idempotent: identical files are not rewritten and
// only the socket enable step remains.
func (m *Manager) Install(ctx context.Context) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{}, unsupported("the TUN helper is Linux/systemd-only")
	}
	uid, err := m.targetUID()
	if err != nil {
		return Status{}, err
	}
	socks, err := m.socksAddress()
	if err != nil {
		return Status{}, err
	}
	if err := m.checkPrerequisites(); err != nil {
		return Status{}, err
	}
	if _, err := m.lookPath("systemctl"); err != nil {
		return Status{}, unsupported("systemd (systemctl) is not available; install the TUN helper on a systemd host")
	}
	if _, err := m.lookPath("systemd-tmpfiles"); err != nil {
		return Status{}, unsupported("systemd-tmpfiles is not available; install the TUN helper on a complete systemd host")
	}
	source, err := m.executable()
	if err != nil {
		return Status{}, internalFailure("cannot locate the running RouteLatch binary", err)
	}

	service, err := m.readSafe(m.Paths.ServiceUnit, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the existing TUN service unit; nothing was changed", err)
	}
	socket, err := m.readSafe(m.Paths.SocketUnit, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the existing TUN socket unit; nothing was changed", err)
	}
	config, err := m.readSafe(m.Paths.Config, maxConfigBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the existing TUN service config; nothing was changed", err)
	}
	tmpfiles, err := m.readSafe(m.Paths.Tmpfiles, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the existing TUN runtime tmpfiles entry; nothing was changed", err)
	}
	binary, err := m.statSafe(m.Paths.Binary)
	if err != nil {
		return Status{}, conflict("cannot inspect the installed TUN helper binary; nothing was changed", err)
	}

	managed := service.Managed || socket.Managed
	desiredService, err := m.serviceUnit(uid)
	if err != nil {
		return Status{}, internalFailure("cannot build the TUN service unit", err)
	}
	desiredSocket, err := m.socketUnit(uid)
	if err != nil {
		return Status{}, internalFailure("cannot build the TUN socket unit", err)
	}
	desiredTmpfiles, err := m.tmpfilesEntry(uid)
	if err != nil {
		return Status{}, internalFailure("cannot build the TUN runtime tmpfiles entry", err)
	}
	desiredConfig, err := json.Marshal(tun.ServiceConfig{AllowedUID: uid, SocksAddress: socks})
	if err != nil {
		return Status{}, internalFailure("cannot build the TUN service config", err)
	}
	desiredConfig = append(desiredConfig, '\n')

	// A foreign file is only ever adopted with --force, and only after the
	// caller proved the unrelated unit is not live. Everything else is a
	// conflict with nothing changed. A foreign tmpfiles entry is adopted only
	// with --force too: it would otherwise rewrite the boot-time /run layout.
	if service.Exists && !service.Safe {
		return Status{}, conflict("the TUN service unit is a symlink, non-regular file or foreign-owned; refusing to follow or overwrite it", nil)
	}
	if socket.Exists && !socket.Safe {
		return Status{}, conflict("the TUN socket unit is a symlink, non-regular file or foreign-owned; refusing to follow or overwrite it", nil)
	}
	if config.Exists && !config.Safe {
		return Status{}, conflict("the TUN service config is a symlink, non-regular file or foreign-owned; refusing to follow or overwrite it", nil)
	}
	if tmpfiles.Exists && !tmpfiles.Safe {
		return Status{}, conflict("the TUN runtime tmpfiles entry is a symlink, non-regular file or foreign-owned; refusing to follow or overwrite it", nil)
	}
	if binary.Exists && !binary.Safe {
		return Status{}, conflict("the TUN helper binary path is a symlink, non-regular file or foreign-owned; refusing to replace it", nil)
	}
	if service.Exists && !service.Managed && !m.Force {
		return Status{}, conflict("the TUN service unit was not created by RouteLatch; use --force to replace it after stopping it", nil)
	}
	if socket.Exists && !socket.Managed && !m.Force {
		return Status{}, conflict("the TUN socket unit was not created by RouteLatch; use --force to replace it after stopping it", nil)
	}
	if tmpfiles.Exists && !tmpfiles.Managed && !m.Force {
		return Status{}, conflict("the TUN runtime tmpfiles entry was not created by RouteLatch; use --force to replace it", nil)
	}
	if (service.Exists && !service.Managed) || (socket.Exists && !socket.Managed) {
		if err := m.requireInactive(ctx); err != nil {
			return Status{}, err
		}
	}
	if config.Exists && !bytes.Equal(config.Content, desiredConfig) && !managed && !m.Force {
		return Status{}, conflict("an existing TUN service config was not created by RouteLatch; use --force to replace it", nil)
	}
	if binary.Exists && !m.Force && !managed {
		same, cmpErr := sameFile(m.Paths.Binary, source)
		if cmpErr != nil {
			return Status{}, conflict("cannot compare the existing TUN helper binary; nothing was changed", cmpErr)
		}
		if !same {
			return Status{}, conflict("the TUN helper binary path already holds a different file; use --force to replace it", nil)
		}
	}
	serviceChanged := fileChanged(service, desiredService, unitMode)
	socketChanged := fileChanged(socket, desiredSocket, unitMode)
	configChanged := fileChanged(config, desiredConfig, unitMode)
	tmpfilesChanged := fileChanged(tmpfiles, desiredTmpfiles, tmpfilesMode)
	binaryChanged := true
	if binary.Exists {
		same, cmpErr := sameFile(m.Paths.Binary, source)
		if cmpErr != nil {
			return Status{}, conflict("cannot compare the existing TUN helper binary; nothing was changed", cmpErr)
		}
		binaryChanged = !same || binary.Mode.Perm() != binaryMode
	}

	changed := serviceChanged || socketChanged || configChanged || tmpfilesChanged || binaryChanged
	snaps, dirState, err := m.beginInstall(
		fieldSnapshot{path: m.Paths.Binary, changed: binaryChanged},
		fieldSnapshot{path: m.Paths.Config, changed: configChanged},
		fieldSnapshot{path: m.Paths.Tmpfiles, changed: tmpfilesChanged},
		fieldSnapshot{path: m.Paths.SocketUnit, changed: socketChanged},
		fieldSnapshot{path: m.Paths.ServiceUnit, changed: serviceChanged},
	)
	if err != nil {
		return Status{}, err
	}
	socketWasEnabled := m.socketEnabled(ctx)
	unitChanged := serviceChanged || socketChanged
	activated := false
	fail := func(cause error) (Status, error) {
		if rbErr := m.rollback(ctx, snaps, dirState, activated && !socketWasEnabled, unitChanged); rbErr != nil {
			return Status{}, internalFailure("the TUN helper install failed and its rollback was incomplete", errors.Join(cause, rbErr))
		}
		return Status{}, cause
	}

	if binaryChanged {
		if err := m.copyBinary(source, m.Paths.Binary); err != nil {
			return fail(err)
		}
	}
	if configChanged {
		if err := m.writeFile(m.Paths.Config, desiredConfig, unitMode); err != nil {
			return fail(err)
		}
	}
	if tmpfilesChanged {
		if err := m.writeFile(m.Paths.Tmpfiles, desiredTmpfiles, tmpfilesMode); err != nil {
			return fail(err)
		}
	}
	if socketChanged {
		if err := m.writeFile(m.Paths.SocketUnit, desiredSocket, unitMode); err != nil {
			return fail(err)
		}
	}
	if serviceChanged {
		if err := m.writeFile(m.Paths.ServiceUnit, desiredService, unitMode); err != nil {
			return fail(err)
		}
	}
	if err := m.ensureRuntimeDir(uid); err != nil {
		return fail(err)
	}
	if tmpfilesChanged {
		if _, err := m.tmpfilesCreate(ctx, m.Paths.Tmpfiles); err != nil {
			return fail(err)
		}
	}
	if changed {
		if _, err := m.systemctl(ctx, "daemon-reload"); err != nil {
			return fail(err)
		}
	}
	// The socket unit is the long-lived piece: the service exits after an
	// explicit disable and the socket starts a fresh process on the next
	// enable (§10.4). Enabling it is idempotent.
	activated = true
	if _, err := m.systemctl(ctx, "enable", "--now", SocketUnitName); err != nil {
		return fail(err)
	}
	// Enabling the socket returns before the control path necessarily exists.
	// The engine can only use the helper after the socket is 0600 and owned by
	// the allowed uid, so setup does not report success without that proof.
	if err := m.ready(ctx, filepath.Join(m.Paths.RuntimeDir, "control.sock"), uid); err != nil {
		return fail(err)
	}
	return Status{
		Unit: m.Paths.ServiceUnit, SocketUnit: m.Paths.SocketUnit,
		Config: m.Paths.Config, Binary: m.Paths.Binary, Tmpfiles: m.Paths.Tmpfiles,
		AllowedUID: uid, Changed: changed, Managed: true,
	}, nil
}

// Uninstall stops coverage first, then removes only files that carry the
// RouteLatch ownership marker (units) or are proven copies below the managed
// install. It leaves user state, settings and unrelated files alone.
func (m *Manager) Uninstall(ctx context.Context) (Status, error) {
	if runtime.GOOS != "linux" {
		return Status{}, unsupported("the TUN helper is Linux/systemd-only")
	}
	if m.euid() != 0 {
		return Status{}, denied("tun uninstall must run as root; use 'sudo routelatch tun uninstall'")
	}
	service, err := m.readSafe(m.Paths.ServiceUnit, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the TUN service unit; nothing was removed", err)
	}
	socket, err := m.readSafe(m.Paths.SocketUnit, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the TUN socket unit; nothing was removed", err)
	}
	if service.Exists && !service.Safe {
		return Status{}, conflict("the TUN service unit is a symlink, non-regular file or foreign-owned; it was not removed", nil)
	}
	if socket.Exists && !socket.Safe {
		return Status{}, conflict("the TUN socket unit is a symlink, non-regular file or foreign-owned; it was not removed", nil)
	}
	if service.Exists && !service.Managed {
		return Status{}, conflict("the TUN service unit was not created by RouteLatch; it was not removed", nil)
	}
	if socket.Exists && !socket.Managed {
		return Status{}, conflict("the TUN socket unit was not created by RouteLatch; it was not removed", nil)
	}
	config, err := m.readSafe(m.Paths.Config, maxConfigBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the TUN service config; nothing was removed", err)
	}
	tmpfiles, err := m.readSafe(m.Paths.Tmpfiles, maxUnitBytes)
	if err != nil {
		return Status{}, conflict("cannot inspect the TUN runtime tmpfiles entry; nothing was removed", err)
	}
	tmpfilesOwned := tmpfiles.Exists && tmpfiles.Safe && tmpfiles.Managed
	status := Status{
		Unit: m.Paths.ServiceUnit, SocketUnit: m.Paths.SocketUnit,
		Config: m.Paths.Config, Binary: m.Paths.Binary, Tmpfiles: m.Paths.Tmpfiles,
	}
	// Report the uid the unit actually serves, not the Setup zero value. An
	// uninstall without a readable owned unit leaves it unset (JSON omits 0).
	status.AllowedUID = unitUser(service.Content)
	if status.AllowedUID < 0 {
		status.AllowedUID = unitUser(socket.Content)
	}
	if status.AllowedUID < 0 && config.Exists && config.Safe {
		var cfg tun.ServiceConfig
		if json.Unmarshal(config.Content, &cfg) == nil && cfg.Validate() == nil {
			status.AllowedUID = cfg.AllowedUID
		}
	}
	if status.AllowedUID < 0 {
		status.AllowedUID = 0
	}
	if !service.Exists && !socket.Exists {
		// Idempotent: no owned unit means there is nothing to stop or remove.
		// A config or binary left without an owned unit is not ours to delete;
		// an owned tmpfiles entry and its runtime artifacts are still ours.
		if tmpfilesOwned {
			ok, rmErr := m.removeOwned(m.Paths.Tmpfiles, true)
			if rmErr != nil {
				return Status{}, rmErr
			}
			if ok {
				status.Removed = append(status.Removed, m.Paths.Tmpfiles)
			}
			artifacts, artErr := m.removeRuntimeArtifacts()
			if artErr != nil {
				return Status{}, artErr
			}
			status.Removed = append(status.Removed, artifacts...)
			status.Managed = true
			status.Changed = len(status.Removed) > 0
		}
		return status, nil
	}
	status.Managed = true
	if _, err := m.lookPath("systemctl"); err != nil {
		return Status{}, unsupported("systemd (systemctl) is not available; the installed TUN helper was not removed")
	}
	// Stop the socket before the service: it is the activation path, and a
	// status probe in the gap must not start a fresh helper that adopts the
	// preserved lease. Then stop the service (closing TUN) before deleting any
	// file. A failed step is reported and nothing is deleted, so the caller
	// keeps the fail-closed recovery path.
	if socket.Exists {
		if _, err := m.systemctl(ctx, "disable", "--now", SocketUnitName); err != nil {
			return Status{}, err
		}
	}
	if service.Exists {
		if _, err := m.systemctl(ctx, "stop", ServiceUnitName); err != nil {
			return Status{}, err
		}
		if _, err := m.systemctl(ctx, "disable", ServiceUnitName); err != nil {
			return Status{}, err
		}
	}
	removed := make([]string, 0, 4)
	if service.Exists {
		ok, err := m.removeOwned(m.Paths.ServiceUnit, true)
		if err != nil {
			return Status{}, err
		}
		if ok {
			removed = append(removed, m.Paths.ServiceUnit)
		}
	}
	if socket.Exists {
		ok, err := m.removeOwned(m.Paths.SocketUnit, true)
		if err != nil {
			return Status{}, err
		}
		if ok {
			removed = append(removed, m.Paths.SocketUnit)
		}
	}
	unitContent := service.Content
	if len(unitContent) == 0 {
		unitContent = socket.Content
	}
	if configOwnedBy(unitContent, config) {
		ok, err := m.removeOwned(m.Paths.Config, false)
		if err != nil {
			return Status{}, err
		}
		if ok {
			removed = append(removed, m.Paths.Config)
		}
	}
	if binary, statErr := m.statSafe(m.Paths.Binary); statErr == nil && binary.Exists && binary.Safe {
		ok, err := m.removeOwned(m.Paths.Binary, false)
		if err != nil {
			return Status{}, err
		}
		if ok {
			removed = append(removed, m.Paths.Binary)
		}
	}
	if tmpfilesOwned {
		ok, err := m.removeOwned(m.Paths.Tmpfiles, true)
		if err != nil {
			return Status{}, err
		}
		if ok {
			removed = append(removed, m.Paths.Tmpfiles)
		}
	}
	leaseRemoved, err := m.removeRuntimeArtifacts()
	if err != nil {
		return Status{}, err
	}
	removed = append(removed, leaseRemoved...)
	if _, err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return Status{}, err
	}
	status.Removed = removed
	status.Changed = len(removed) > 0
	return status, nil
}

// targetUID resolves the invoking user, never root. Under sudo SUDO_UID names
// the caller; a direct root invocation has no safe target and is refused
// (§10.4).
func (m *Manager) targetUID() (int, error) {
	if m.euid() != 0 {
		return 0, denied("tun setup must run as root; use 'sudo routelatch tun setup'")
	}
	if raw := strings.TrimSpace(m.getenv("SUDO_UID")); raw != "" {
		uid, err := strconv.Atoi(raw)
		if err != nil || uid <= 0 {
			return 0, denied("SUDO_UID is not a valid non-root user id; run setup with sudo from the user that runs RouteLatch")
		}
		return uid, nil
	}
	uid := m.getuid()
	if uid <= 0 {
		return 0, denied("refusing to install the TUN helper for root; run 'sudo routelatch tun setup' as the user that runs RouteLatch")
	}
	return uid, nil
}

func (m *Manager) socksAddress() (string, error) {
	raw := strings.TrimSpace(m.SocksAddress)
	if raw == "" {
		raw = DefaultSocksAddress
	}
	host, port, err := tun.ParseSocksAddress(raw)
	if err != nil {
		return "", &control.OpError{
			Kind: contract.InvalidDestination, Exit: contract.ExitUsage,
			Message: "the TUN SOCKS target must be a loopback IP literal with a valid port (for example 127.0.0.1:8318)",
		}
	}
	return hostPort(host, port), nil
}

func hostPort(host string, port uint16) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.FormatUint(uint64(port), 10)
	}
	return host + ":" + strconv.FormatUint(uint64(port), 10)
}

// checkPrerequisites reports the two host prerequisites with a fixed, safe
// explanation: a kernel that permits unprivileged SO_BINDTODEVICE and the TUN
// device node. Neither value is user-controlled.
func (m *Manager) checkPrerequisites() error {
	major, minor, err := m.kernelVersion()
	if err != nil {
		return unsupported("cannot determine the Linux kernel version; TUN coverage needs Linux 5.7 or newer")
	}
	if major < 5 || (major == 5 && minor < 7) {
		return unsupported(fmt.Sprintf("Linux 5.7 or newer is required for non-root TUN bypass (found %d.%d)", major, minor))
	}
	if err := m.tunDevice(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return unsupported("the TUN device /dev/net/tun is not available; load the tun kernel module and retry")
		}
		return unsupported("the TUN device /dev/net/tun cannot be inspected; check the kernel module and permissions")
	}
	return nil
}

// requireInactive refuses to adopt a foreign unit while it is live. Force
// replaces files, not a running unrelated service.
func (m *Manager) requireInactive(ctx context.Context) error {
	for _, name := range []string{ServiceUnitName, SocketUnitName} {
		out, err := m.systemctlOutput(ctx, "is-active", name)
		state := strings.TrimSpace(out)
		if err != nil {
			// is-active exits non-zero for inactive/unknown units. It prints
			// that state; an error with no state is not proof of inactivity.
			if state == "inactive" || state == "failed" || state == "unknown" {
				continue
			}
			return externalFailure("cannot verify whether the existing TUN unit is active; it was not replaced", err)
		}
		switch state {
		case "active", "activating", "reloading", "deactivating":
			return conflict("the existing TUN unit is active and was not created by RouteLatch; stop it before using --force", nil)
		}
	}
	return nil
}

func (m *Manager) serviceUnit(uid int) ([]byte, error) {
	exe, err := unitArgument(m.Paths.Binary)
	if err != nil {
		return nil, err
	}
	cfg, err := unitArgument(m.Paths.Config)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(OwnershipMarker + "\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=RouteLatch TUN coverage helper\n")
	b.WriteString("After=network.target routelatch-tun.socket\n")
	// The service is socket-activated. BindsTo makes a documented
	// `systemctl stop routelatch-tun.socket` stop the activated service, whose
	// shutdown removes rl-tun0/routes; the socket cannot be unlinked while the
	// service keeps host coverage (docs/contracts.md §10.4).
	b.WriteString("BindsTo=routelatch-tun.socket\n")
	b.WriteString("StartLimitIntervalSec=" + strconv.Itoa(startLimitIntervalSec) + "\n")
	b.WriteString("StartLimitBurst=" + strconv.Itoa(startLimitBurst) + "\n")
	// A start-limit hit must stop coverage, not reboot or restart the host;
	// the documented reset-failed step is the only recovery.
	b.WriteString("StartLimitAction=none\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("User=" + strconv.Itoa(uid) + "\n")
	b.WriteString("ExecStart=" + exe + " __tun-service --config " + cfg + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=1s\n")
	b.WriteString("RuntimeDirectory=routelatch-tun\n")
	b.WriteString("RuntimeDirectoryMode=0755\n")
	b.WriteString("RuntimeDirectoryPreserve=yes\n")
	b.WriteString("UMask=0077\n")
	b.WriteString("AmbientCapabilities=CAP_NET_ADMIN\n")
	b.WriteString("CapabilityBoundingSet=CAP_NET_ADMIN\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("ProtectSystem=strict\n")
	b.WriteString("ProtectHome=yes\n")
	b.WriteString("PrivateTmp=yes\n")
	// A private /dev would hide /dev/net/tun; the contract permits keeping it
	// visible (or an equivalent DeviceAllow) rather than breaking the service.
	// The device cgroup stays closed with an explicit short allowlist, so the
	// service can use the TUN clone device but no unrelated device node.
	b.WriteString("# /dev/net/tun must stay visible; see docs/contracts.md §10.4.\n")
	b.WriteString("PrivateDevices=no\n")
	b.WriteString("DevicePolicy=closed\n")
	b.WriteString("DeviceAllow=/dev/net/tun rw\n")
	b.WriteString("DeviceAllow=/dev/null rw\n")
	b.WriteString("DeviceAllow=/dev/zero rw\n")
	b.WriteString("DeviceAllow=/dev/random rw\n")
	b.WriteString("DeviceAllow=/dev/urandom rw\n")
	b.WriteString("ProtectKernelTunables=yes\n")
	b.WriteString("ProtectKernelModules=yes\n")
	b.WriteString("ProtectControlGroups=yes\n")
	b.WriteString("ProtectClock=yes\n")
	b.WriteString("ProtectHostname=yes\n")
	b.WriteString("ProtectProc=invisible\n")
	b.WriteString("RestrictNamespaces=yes\n")
	b.WriteString("RestrictRealtime=yes\n")
	b.WriteString("RestrictSUIDSGID=yes\n")
	b.WriteString("LockPersonality=yes\n")
	b.WriteString("SystemCallArchitectures=native\n")
	b.WriteString("RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return []byte(b.String()), nil
}

func (m *Manager) socketUnit(uid int) ([]byte, error) {
	socketPath, err := unitArgument(filepath.Join(m.Paths.RuntimeDir, "control.sock"))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(OwnershipMarker + "\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=RouteLatch TUN control socket\n")
	b.WriteString("\n[Socket]\n")
	b.WriteString("ListenStream=" + socketPath + "\n")
	b.WriteString("SocketMode=0600\n")
	b.WriteString("SocketUser=" + strconv.Itoa(uid) + "\n")
	// If the runtime directory is absent before tmpfiles runs, systemd may
	// create it itself. It must then be traversable by the engine uid; the
	// socket inside stays 0600 and SocketUser= above owns it.
	b.WriteString("DirectoryMode=0755\n")
	b.WriteString("RemoveOnStop=yes\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=sockets.target\n")
	return []byte(b.String()), nil
}

// tmpfilesEntry recreates /run/routelatch-tun after boot with the allowed uid
// as owner and a traversable mode. The socket inside is still 0600 and owned by
// that uid; without this entry a fresh boot leaves a root-owned 0700 directory
// (or none at all) and the non-root engine cannot reach control.sock.
func (m *Manager) tmpfilesEntry(uid int) ([]byte, error) {
	dir, err := tmpfilesPath(m.Paths.RuntimeDir)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(OwnershipMarker + "\n")
	b.WriteString("# Recreate the TUN runtime directory after boot for the allowed engine uid.\n")
	b.WriteString("d " + dir + " " + strconv.FormatUint(uint64(runtimeDirMode), 8) + " " + strconv.Itoa(uid) + " - -\n")
	return []byte(b.String()), nil
}

// tmpfilesPath renders a path for the tmpfiles.d line format. The production
// path is fixed and needs no quoting; a test tree may contain spaces, which the
// format accepts as a double-quoted argument.
func tmpfilesPath(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\n\r") {
		return "", errors.New("a TUN runtime path contains a control character that tmpfiles.d cannot represent")
	}
	if !strings.ContainsAny(path, " \t\"'\\") {
		return path, nil
	}
	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// unitArgument applies the systemd escaping a path needs in ExecStart or
// ListenStream. Fixed production paths need none, but a test tree may contain
// spaces; control characters are always refused.
func unitArgument(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", errors.New("a TUN helper path contains a control character that a unit file cannot represent")
	}
	escaped := strings.ReplaceAll(s, "%", "%%")
	escaped = strings.ReplaceAll(escaped, "$", "$$")
	if !strings.ContainsAny(escaped, " \t\"'\\;") {
		return escaped, nil
	}
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// --- filesystem ----------------------------------------------------------

type fileState struct {
	Exists  bool
	Safe    bool
	Managed bool
	Mode    os.FileMode
	Content []byte
}

func (m *Manager) readSafe(path string, limit int64) (fileState, error) {
	st, err := m.statSafe(path)
	if err != nil || !st.Exists || !st.Safe {
		return st, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fileState{Exists: true}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return fileState{Exists: true}, err
	}
	if int64(len(raw)) > limit {
		return fileState{Exists: true}, nil
	}
	st.Content = raw
	st.Managed = hasOwnershipMarker(string(raw))
	return st, nil
}

// statSafe reports existence, regular-file shape and owner without following a
// final symlink. Safe requires a regular file owned by root; tests inject
// Owner so their private tree can stand in for /etc.
func (m *Manager) statSafe(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{Exists: true}, err
	}
	st := fileState{Exists: true, Mode: info.Mode().Perm()}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return st, nil
	}
	owner, err := m.ownerUID(path)
	if err != nil {
		return st, err
	}
	// Safe means a root-owned regular file. Its mode is not trusted: Install
	// converges an owned file to the documented mode even when the content is
	// unchanged, so `mode&022 == 0` holds for every installed unit/config/binary.
	// A symlink or a non-root owner is still a conflict, never a chmod.
	if owner != 0 {
		return st, nil
	}
	st.Safe = true
	return st, nil
}

func (m *Manager) ownerUID(path string) (int, error) {
	if m.Owner != nil {
		return m.Owner(path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("file has no owner")
	}
	return int(st.Uid), nil
}

func (m *Manager) writeFile(path string, content []byte, mode os.FileMode) error {
	if err := m.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".routelatch-tun.tmp-*")
	if err != nil {
		return internalFailure("cannot create a temporary TUN helper file", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return internalFailure("cannot set TUN helper file permissions", err)
	}
	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return internalFailure("cannot write the TUN helper file", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return internalFailure("cannot fsync the TUN helper file", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot close the TUN helper file", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot atomically replace the TUN helper file", err)
	}
	if err := m.chown(path, 0, 0); err != nil {
		return internalFailure("cannot mark the TUN helper file root-owned", err)
	}
	if err := m.syncDir(filepath.Dir(path)); err != nil {
		return internalFailure("the TUN helper file was written but its directory could not be fsynced", err)
	}
	return nil
}

func (m *Manager) copyBinary(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return internalFailure("cannot open the running RouteLatch binary", err)
	}
	defer in.Close()
	if err := m.ensureDir(filepath.Dir(dest)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".routelatch-tun.tmp-*")
	if err != nil {
		return internalFailure("cannot create a temporary TUN helper binary", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(binaryMode); err != nil {
		cleanup()
		return internalFailure("cannot set TUN helper binary permissions", err)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return internalFailure("cannot copy the TUN helper binary", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return internalFailure("cannot fsync the TUN helper binary", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot close the TUN helper binary", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return internalFailure("cannot atomically replace the TUN helper binary", err)
	}
	if err := m.chown(dest, 0, 0); err != nil {
		return internalFailure("cannot mark the TUN helper binary root-owned", err)
	}
	if err := m.syncDir(filepath.Dir(dest)); err != nil {
		return internalFailure("the TUN helper binary was written but its directory could not be fsynced", err)
	}
	return nil
}

func (m *Manager) ensureDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return internalFailure("cannot create the TUN helper directory", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return internalFailure("cannot inspect the TUN helper directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return conflict("the TUN helper directory is a symlink or not a directory; refusing to write through it", nil)
	}
	owner, err := m.ownerUID(dir)
	if err != nil {
		return conflict("cannot verify ownership of the TUN helper directory", err)
	}
	if owner != 0 {
		return conflict("the TUN helper directory is not root-owned; refusing to write into it", nil)
	}
	// Dedicated RouteLatch directories are converged to the documented install
	// mode. Shared system directories (/etc/systemd/system, /etc/tmpfiles.d) are
	// never widened: only group/other write bits are cleared, so a hardened
	// 0700/0750 host layout keeps its mode while a writable one loses the unsafe
	// bits. The caller is root and the directory is root-owned either way.
	perm := info.Mode().Perm()
	if m.isInstallDir(dir) {
		if perm != dirMode {
			if err := os.Chmod(dir, dirMode); err != nil {
				return internalFailure("cannot converge the TUN helper directory permissions", err)
			}
		}
	} else if perm&0o022 != 0 {
		if err := os.Chmod(dir, perm&^0o022); err != nil {
			return internalFailure("cannot clear unsafe write bits on the TUN helper directory", err)
		}
	}
	return nil
}

// isInstallDir reports whether dir is one of the dedicated directories setup
// owns, as opposed to a shared system parent such as /etc/systemd/system.
func (m *Manager) isInstallDir(dir string) bool {
	dir = filepath.Clean(dir)
	if config := m.Paths.Config; config != "" && dir == filepath.Clean(filepath.Dir(config)) {
		return true
	}
	if binary := m.Paths.Binary; binary != "" && dir == filepath.Clean(filepath.Dir(binary)) {
		return true
	}
	return false
}

func (m *Manager) ensureRuntimeDir(uid int) error {
	dir := m.Paths.RuntimeDir
	if err := os.MkdirAll(dir, runtimeDirMode); err != nil {
		return internalFailure("cannot create the TUN runtime directory", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return internalFailure("cannot inspect the TUN runtime directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return conflict("the TUN runtime directory is a symlink or not a directory; refusing to use it", nil)
	}
	owner, err := m.ownerUID(dir)
	if err != nil {
		return conflict("cannot verify ownership of the TUN runtime directory", err)
	}
	if owner != 0 && owner != uid {
		return conflict("the TUN runtime directory is owned by another user; refusing to reuse it", nil)
	}
	if err := os.Chmod(dir, runtimeDirMode); err != nil {
		return internalFailure("cannot protect the TUN runtime directory", err)
	}
	if owner != uid {
		if err := m.chown(dir, uid, -1); err != nil {
			return internalFailure("cannot hand the TUN runtime directory to the allowed user", err)
		}
	}
	return nil
}

// fieldSnapshot names one file whose previous state must be restored when a
// later step of Install fails.
type fieldSnapshot struct {
	path    string
	changed bool
}

func fileChanged(st fileState, desired []byte, mode os.FileMode) bool {
	return !st.Exists || !bytes.Equal(st.Content, desired) || st.Mode.Perm() != mode
}

// beginInstall records the previous state of every file the converged install
// may replace, plus the runtime directory, before the first mutation. Only
// changed files are read so an idempotent run does not copy the binary.
func (m *Manager) beginInstall(fields ...fieldSnapshot) ([]fileSnapshot, dirSnapshot, error) {
	var snaps []fileSnapshot
	for _, field := range fields {
		if !field.changed {
			continue
		}
		snap, err := snapshotFile(field.path)
		if err != nil {
			return nil, dirSnapshot{}, conflict("cannot snapshot an existing TUN helper file; nothing was changed", err)
		}
		snaps = append(snaps, snap)
	}
	dir, err := m.snapshotDir(m.Paths.RuntimeDir)
	if err != nil {
		return nil, dirSnapshot{}, conflict("cannot snapshot the TUN runtime directory; nothing was changed", err)
	}
	return snaps, dir, nil
}

func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{Path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileSnapshot{}, errors.New("the existing TUN helper path is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{Path: path, Exists: true, Content: content, Mode: info.Mode().Perm()}, nil
}

func (m *Manager) snapshotDir(path string) (dirSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return dirSnapshot{Path: path}, nil
	}
	if err != nil {
		return dirSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return dirSnapshot{}, errors.New("the existing TUN runtime path is not a directory")
	}
	uid, err := m.ownerUID(path)
	if err != nil {
		return dirSnapshot{}, err
	}
	return dirSnapshot{Path: path, Exists: true, Mode: info.Mode().Perm(), UID: uid}, nil
}

// rollback restores the previous files, socket enablement and runtime
// directory. It is best-effort and reports every step that failed; a caller
// must not claim a clean rollback when this returns an error.
func (m *Manager) rollback(ctx context.Context, snaps []fileSnapshot, dir dirSnapshot, undoActivation, unitChanged bool) error {
	var errs []error
	// Undo activation before removing the path the socket would listen on.
	if undoActivation {
		if _, err := m.systemctlOutput(ctx, "disable", "--now", SocketUnitName); err != nil {
			errs = append(errs, fmt.Errorf("disable socket: %w", err))
		}
	}
	for i := len(snaps) - 1; i >= 0; i-- {
		snap := snaps[i]
		if snap.Exists {
			if err := m.writeFile(snap.Path, snap.Content, snap.Mode.Perm()); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.Remove(snap.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove newly written helper file: %w", err))
		}
	}
	if dir.Exists {
		if err := os.Chmod(dir.Path, dir.Mode.Perm()); err != nil {
			errs = append(errs, fmt.Errorf("restore runtime directory mode: %w", err))
		}
		if err := m.chown(dir.Path, dir.UID, -1); err != nil {
			errs = append(errs, fmt.Errorf("restore runtime directory owner: %w", err))
		}
	} else if _, err := m.removeRuntimeArtifacts(); err != nil {
		errs = append(errs, err)
	}
	if unitChanged {
		if _, err := m.systemctlOutput(ctx, "daemon-reload"); err != nil {
			errs = append(errs, fmt.Errorf("reload restored units: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) tmpfilesCreate(ctx context.Context, path string) (string, error) {
	bin, err := m.lookPath("systemd-tmpfiles")
	if err != nil {
		return "", unsupported("systemd-tmpfiles is not available; install the TUN helper on a complete systemd host")
	}
	out, err := m.runner().Output(ctx, bin, "--create", path)
	if err != nil {
		return out, externalFailure("systemd-tmpfiles --create failed; the TUN runtime directory is not ready", err)
	}
	return out, nil
}

func (m *Manager) socketEnabled(ctx context.Context) bool {
	out, err := m.systemctlOutput(ctx, "is-enabled", SocketUnitName)
	if err != nil {
		return false
	}
	switch strings.TrimSpace(out) {
	case "enabled", "enabled-runtime", "static", "indirect", "alias", "generated":
		return true
	default:
		return false
	}
}

// ready proves the socket unit published a 0600 control socket owned by the
// allowed uid. "Succeeded" from systemctl alone is not enough for the engine.
func (m *Manager) ready(ctx context.Context, path string, uid int) error {
	if m.Ready != nil {
		return m.Ready(path, uid)
	}
	deadline := time.Now().Add(socketWait)
	var last error
	for {
		last = checkControlSocket(path, uid)
		if last == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return externalFailure("the TUN control socket did not become ready; the helper socket is not usable", last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func checkControlSocket(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("the TUN control path is not a socket")
	}
	if info.Mode().Perm() != 0o600 {
		return errors.New("the TUN control socket is not mode 0600")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid {
		return errors.New("the TUN control socket is not owned by the allowed uid")
	}
	return nil
}

func (m *Manager) removeOwned(path string, markerRequired bool) (bool, error) {
	st, err := m.readSafe(path, maxUnitBytes)
	if err != nil {
		return false, internalFailure("cannot inspect a TUN helper file before removing it", err)
	}
	if !st.Exists {
		return false, nil
	}
	if !st.Safe && markerRequired {
		return false, conflict("a TUN helper unit changed to a symlink or foreign file; it was not removed", nil)
	}
	if markerRequired && !st.Managed {
		return false, conflict("a TUN helper unit lost its RouteLatch marker; it was not removed", nil)
	}
	if err := os.Remove(path); err != nil {
		return false, internalFailure("cannot remove a TUN helper file", err)
	}
	if err := m.syncDir(filepath.Dir(path)); err != nil {
		return false, internalFailure("a TUN helper file was removed but its directory could not be fsynced", err)
	}
	return true, nil
}

// removeRuntimeArtifacts deletes only the two known files in the managed
// runtime directory and then the directory itself when empty. /run is cleared
// on boot; nothing below another path is touched.
func (m *Manager) removeRuntimeArtifacts() ([]string, error) {
	var removed []string
	for _, name := range []string{"lease.json", "control.sock"} {
		path := filepath.Join(m.Paths.RuntimeDir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, internalFailure("cannot inspect a TUN runtime file", err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, internalFailure("cannot remove a TUN runtime file", err)
		}
		removed = append(removed, path)
	}
	if err := os.Remove(m.Paths.RuntimeDir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
		return nil, internalFailure("cannot remove the TUN runtime directory", err)
	}
	return removed, nil
}

// configOwnedBy proves the config belongs to the service unit before uninstall
// deletes it: it must be a root-owned regular file, parse with the fixed shape
// and name the same allowed uid as the unit's User=/SocketUser=. A malformed or
// foreign config is left in place rather than deleted as if it were ours.
func configOwnedBy(unit []byte, config fileState) bool {
	if len(unit) == 0 || !config.Exists || !config.Safe {
		return false
	}
	user := unitUser(unit)
	if user < 0 {
		return false
	}
	var cfg tun.ServiceConfig
	if err := json.Unmarshal(config.Content, &cfg); err != nil {
		return false
	}
	if err := cfg.Validate(); err != nil {
		return false
	}
	return cfg.AllowedUID == user
}

func unitUser(unit []byte) int {
	for _, line := range strings.Split(string(unit), "\n") {
		line = strings.TrimSuffix(line, "\r")
		for _, prefix := range []string{"User=", "SocketUser="} {
			if strings.HasPrefix(line, prefix) {
				uid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
				if err == nil {
					return uid
				}
			}
		}
	}
	return -1
}

func hasOwnershipMarker(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSuffix(line, "\r") == OwnershipMarker {
			return true
		}
	}
	return false
}

func sameFile(a, b string) (bool, error) {
	aha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	ahb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return aha == ahb, nil
}

func hashFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// --- small accessors and documented failures -----------------------------

func (m *Manager) getenv(key string) string {
	if m.Getenv != nil {
		return m.Getenv(key)
	}
	return os.Getenv(key)
}

func (m *Manager) euid() int {
	if m.Euid != nil {
		return m.Euid()
	}
	return os.Geteuid()
}

func (m *Manager) getuid() int {
	if m.Getuid != nil {
		return m.Getuid()
	}
	return os.Getuid()
}

func (m *Manager) executable() (string, error) {
	if m.Executable != nil {
		return m.Executable()
	}
	// /proc/self/exe is the binary this process is actually running, even if
	// the path used to launch it was replaced or is a symlink. os.Executable
	// may return a path that no longer names the running image.
	return os.Readlink("/proc/self/exe")
}

func (m *Manager) runner() platform.CommandRunner {
	if m.Runner != nil {
		return m.Runner
	}
	return platform.ExecRunner{}
}

func (m *Manager) lookPath(name string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath(name)
	}
	return exec.LookPath(name)
}

func (m *Manager) chown(path string, uid, gid int) error {
	if m.Chown != nil {
		return m.Chown(path, uid, gid)
	}
	return os.Chown(path, uid, gid)
}

func (m *Manager) syncDir(dir string) error {
	if m.SyncDir != nil {
		return m.SyncDir(dir)
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *Manager) kernelVersion() (int, int, error) {
	if m.KernelVersion != nil {
		return m.KernelVersion()
	}
	return defaultKernelVersion()
}

func defaultKernelVersion() (int, int, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return 0, 0, err
	}
	return parseKernelRelease(strings.TrimSpace(string(raw)))
}

func parseKernelRelease(release string) (int, int, error) {
	dot := strings.IndexByte(release, '.')
	if dot <= 0 {
		return 0, 0, fmt.Errorf("unrecognized kernel release")
	}
	major, err := strconv.Atoi(trimDigits(release[:dot]))
	if err != nil {
		return 0, 0, err
	}
	rest := release[dot+1:]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, 0, fmt.Errorf("unrecognized kernel release")
	}
	minor, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

func trimDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func (m *Manager) tunDevice() error {
	if m.TunDevice != nil {
		return m.TunDevice()
	}
	_, err := os.Stat("/dev/net/tun")
	return err
}

func (m *Manager) systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := m.systemctlOutput(ctx, args...)
	if err != nil {
		return out, externalFailure("systemctl "+args[0]+" failed; the TUN helper state is unknown", err)
	}
	return out, nil
}

// systemctlOutput always executes the path systemd is actually installed at,
// not a literal name looked up again by the child process environment.
func (m *Manager) systemctlOutput(ctx context.Context, args ...string) (string, error) {
	bin, err := m.lookPath("systemctl")
	if err != nil {
		return "", unsupported("systemd (systemctl) is not available")
	}
	return m.runner().Output(ctx, bin, args...)
}

func conflict(message string, cause error) error {
	return &control.OpError{
		Kind: contract.StateLocked, Exit: contract.ExitConflict,
		Message: message, Cause: cause,
		Detail: map[string]any{"reason": string(contract.TunReasonConflict)},
	}
}

func unsupported(message string) error {
	return &control.OpError{
		Kind: contract.Unsupported, Exit: contract.ExitUnsupported,
		Message: message,
		Detail:  map[string]any{"reason": string(contract.TunReasonPlatformUnsupported)},
	}
}

// denied is a CLI-only privileged-setup failure. It deliberately carries no
// TUN reason: docs/contracts.md §10.3 pins error.detail.reason to the stable
// management/status reasons, and "not root" is not one of them.
func denied(message string) error {
	return &control.OpError{
		Kind: contract.PolicyDenied, Exit: contract.ExitDenied,
		Message: message,
	}
}

func externalFailure(message string, cause error) error {
	return &control.OpError{
		Kind: contract.ExternalToolFailed, Exit: contract.ExitError,
		Message: message, Cause: cause,
	}
}

func internalFailure(message string, cause error) error {
	return &control.OpError{Kind: contract.Internal, Exit: contract.ExitError, Message: message, Cause: cause}
}
