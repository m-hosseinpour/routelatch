package tun

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// listenControl returns the control listener. A systemd socket unit may hand it
// over (LISTEN_FDS); otherwise the service creates the 0600 Unix socket itself.
// owned is true when this process created the file and must remove it.
func listenControl(path string, allowedUID int) (ln net.Listener, owned bool, err error) {
	if activated, ok, err := activationListener(); ok || err != nil {
		if err != nil {
			return nil, false, err
		}
		if err := verifyControlSocket(path, allowedUID); err != nil {
			_ = activated.Close()
			return nil, false, err
		}
		return activated, false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, false, fmt.Errorf("create the TUN runtime directory: %w", err)
	}
	if fi, statErr := os.Lstat(path); statErr == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, false, fmt.Errorf("the TUN control path exists and is not a socket")
		}
		// A live listener means another instance owns the control surface; a
		// refused connection means a stale inode from an earlier crash.
		if conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, false, fmt.Errorf("another TUN service is listening")
		} else if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			return nil, false, fmt.Errorf("the TUN control socket could not be checked")
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("remove the stale TUN control socket: %w", err)
		}
	}

	addr := &net.UnixAddr{Name: path, Net: "unix"}
	ln, err = net.ListenUnix("unix", addr)
	if err != nil {
		return nil, false, fmt.Errorf("listen on the TUN control socket: %w", err)
	}
	unixLn := ln.(*net.UnixListener)
	unixLn.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, false, fmt.Errorf("protect the TUN control socket: %w", err)
	}
	if err := verifyControlSocket(path, allowedUID); err != nil {
		_ = ln.Close()
		return nil, false, err
	}
	return ln, true, nil
}

// activationListener uses the systemd socket unit's descriptor when present. It
// never trusts an inherited fd that systemd did not announce for this pid.
func activationListener() (net.Listener, bool, error) {
	fdsText := os.Getenv("LISTEN_FDS")
	if fdsText == "" {
		return nil, false, nil
	}
	pidText := os.Getenv("LISTEN_PID")
	// Do not leak the activation protocol to the `ip` helper or any other
	// child process.
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_PID")
	os.Unsetenv("LISTEN_FDNAMES")
	fds, err := strconv.Atoi(fdsText)
	if err != nil || fds != 1 {
		return nil, true, fmt.Errorf("the TUN socket activation expects exactly one descriptor")
	}
	if pidText != "" {
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid != os.Getpid() {
			return nil, true, fmt.Errorf("the TUN socket activation descriptor belongs to another process")
		}
	}
	f := os.NewFile(3, "routelatch-tun-control")
	if f == nil {
		return nil, true, fmt.Errorf("the TUN socket activation descriptor is unavailable")
	}
	ln, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, true, fmt.Errorf("adopt the TUN control socket: %w", err)
	}
	return ln, true, nil
}

// verifyControlSocket enforces the contract's 0600 socket owned by the allowed
// uid. A socket created by systemd with the wrong mode is refused rather than
// served.
func verifyControlSocket(path string, allowedUID int) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("the TUN control socket is missing: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("the TUN control path is not a socket")
	}
	if fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("the TUN control socket is not mode 0600")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != allowedUID {
		return fmt.Errorf("the TUN control socket is not owned by the allowed uid")
	}
	return nil
}

// verifyRootOwnedFile is the defense-in-depth check for the installed service:
// both the executable and its config must be root-owned and not writable by
// group or others.
func verifyRootOwnedFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no file owner", path)
	}
	if st.Uid != 0 {
		return fmt.Errorf("%s is not root-owned", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}
